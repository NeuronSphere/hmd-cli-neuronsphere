"""Unit tests for the NERD0005 environment graph Redis cache (env_cache.py).

These cover the guarantees called out in SPEC0008:
  * correct partitioning of nouns/relationships into topology vs volatile layers;
  * a write-through status update leaves the topology key untouched (anti-churn);
  * structural changes invalidate topology (targeted DEL), not unrelated keys;
  * fail-open behavior when Redis errors;
  * equivalence of stored vs rehydrated entities (cache-hit == cache-miss data);
  * key namespacing isolates entries by HMD_DID / HMD_REGION / environment_type.

We drive EnvCache with an in-memory fake Redis (no server needed). The
load_environment() call-site wiring is exercised by the integration tests, which
run against a real DbEngineClient/Postgres.
"""

import fnmatch
import os

import pytest
from hmd_schema_loader import DefaultLoader

from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.repo_class import RepoClass
from hmd_lang_deployment.repo_class_version import RepoClassVersion
from hmd_lang_deployment.repo_instance import RepoInstance
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_lang_deployment.repo_instance_deployment_has_repo_class_version import (
    RepoInstanceDeploymentHasRepoClassVersion,
)
from hmd_lang_deployment.repo_instance_has_repo_instance_deployment import (
    RepoInstanceHasRepoInstanceDeployment,
)
from hmd_lang_deployment.repo_instance_isa_repo_class import RepoInstanceIsaRepoClass
from hmd_lang_deployment.repo_instance_req_repo_instance import (
    RepoInstanceReqRepoInstance,
)

from hmd_ms_deployment import env_cache as env_cache_mod
from hmd_ms_deployment.env_cache import EnvCache, is_volatile


# --------------------------------------------------------------------------
# Fakes / helpers
# --------------------------------------------------------------------------
class FakeRedis:
    """Minimal in-memory stand-in supporting exactly the ops EnvCache uses."""

    def __init__(self):
        self.store = {}
        self.fail = False  # when True, every op raises (fail-open testing)

    def _maybe_fail(self):
        if self.fail:
            raise RuntimeError("redis unavailable")

    def ping(self):
        self._maybe_fail()
        return True

    def get(self, key):
        self._maybe_fail()
        return self.store.get(key)

    def set(self, key, value, ex=None):
        self._maybe_fail()
        self.store[key] = value
        return True

    def delete(self, *keys):
        self._maybe_fail()
        n = 0
        for k in keys:
            if k in self.store:
                del self.store[k]
                n += 1
        return n

    def scan_iter(self, match=None):
        self._maybe_fail()
        for k in list(self.store.keys()):
            if match is None or fnmatch.fnmatch(k, match):
                yield k


@pytest.fixture()
def loader():
    return DefaultLoader("schemas/local")


def make_cache(fake=None):
    """An EnvCache already 'connected' to a fake, bypassing feature-flag/init."""
    cache = EnvCache()
    cache._initialized = True
    cache._client = fake if fake is not None else FakeRedis()
    return cache


def sample_graph():
    """A tiny two-instance environment graph split across the two layers."""
    env = Environment(
        identifier="env-1", type="dev", account_number="123456789012", hmd_region="reg1"
    )
    rc = RepoClass(identifier="rc-1", repo_class_name="hmd-vpc")
    rcv = RepoClassVersion(identifier="rcv-1", version="0.1.1")
    ri = RepoInstance(identifier="ri-1", name="base-vpc")
    ri2 = RepoInstance(identifier="ri-2", name="db")

    # topology relationships
    isa = RepoInstanceIsaRepoClass(identifier="isa-1", ref_from="ri-1", ref_to="rc-1")
    req = RepoInstanceReqRepoInstance(
        identifier="req-1", ref_from="ri-1", ref_to="ri-2", role="db"
    )

    # volatile
    rid = RepoInstanceDeployment(
        identifier="rid-1", deployment_id="aaa", status="DEPLOYED"
    )
    rihrid = RepoInstanceHasRepoInstanceDeployment(
        identifier="rihrid-1", ref_from="ri-1", ref_to="rid-1", current="true"
    )
    ridhrcv = RepoInstanceDeploymentHasRepoClassVersion(
        identifier="ridhrcv-1", ref_from="rid-1", ref_to="rcv-1"
    )

    topology = [env, rc, rcv, ri, ri2, isa, req]
    volatile = [rid, rihrid, ridhrcv]
    return topology, volatile


# --------------------------------------------------------------------------
# SPEC0008: partitioning
# --------------------------------------------------------------------------
def test_partitioning_topology_vs_volatile():
    topology, volatile = sample_graph()
    assert all(not is_volatile(e) for e in topology)
    assert all(is_volatile(e) for e in volatile)
    # Precisely the volatile membership from the NERD.
    vol_types = {type(e).__name__ for e in volatile}
    assert vol_types == {
        "RepoInstanceDeployment",
        "RepoInstanceHasRepoInstanceDeployment",
        "RepoInstanceDeploymentHasRepoClassVersion",
    }


# --------------------------------------------------------------------------
# SPEC0008: store/load equivalence (cache-hit data == what was stored)
# --------------------------------------------------------------------------
def test_store_then_load_roundtrip_equivalent(loader):
    cache = make_cache()
    topology, volatile = sample_graph()

    cache.store_layers("dev", topology, volatile)
    topo_out, status_out = cache.load_layers("dev", loader)

    assert cache.stats["hit"] == 1
    assert {type(e).__name__ for e in topo_out} == {
        type(e).__name__ for e in topology
    }
    # identifiers and key attributes survive the round trip
    by_id = {e.identifier: e for e in topo_out + status_out}
    assert by_id["ri-1"].name == "base-vpc"
    assert by_id["rid-1"].status == "DEPLOYED"
    assert by_id["req-1"].ref_from == "ri-1" and by_id["req-1"].ref_to == "ri-2"
    assert by_id["rihrid-1"].current == "true"


def test_missing_status_layer_is_a_miss(loader):
    """Topology present but status expired -> full miss (never partially stale)."""
    cache = make_cache()
    topology, volatile = sample_graph()
    cache.store_layers("dev", topology, volatile)
    # simulate short-TTL status expiry
    cache._client.delete(cache._status_key("dev"))

    topo_out, status_out = cache.load_layers("dev", loader)
    assert topo_out is None and status_out is None
    assert cache.stats["miss"] == 1


# --------------------------------------------------------------------------
# SPEC0008: anti-churn -- write-through never touches topology
# --------------------------------------------------------------------------
def test_writethrough_leaves_topology_untouched(loader):
    cache = make_cache()
    topology, volatile = sample_graph()
    cache.store_layers("dev", topology, volatile)

    topo_key = cache._topology_key("dev")
    topo_before = cache._client.get(topo_key)

    # Activate a NEW deployment for ri-1 (old rid-1 drops out of the slice).
    new_rid = RepoInstanceDeployment(
        identifier="rid-2", deployment_id="aaa", status="DEPLOYED"
    )
    new_rihrid = RepoInstanceHasRepoInstanceDeployment(
        identifier="rihrid-2", ref_from="ri-1", ref_to="rid-2", current="true"
    )
    new_ridhrcv = RepoInstanceDeploymentHasRepoClassVersion(
        identifier="ridhrcv-2", ref_from="rid-2", ref_to="rcv-1"
    )
    cache.splice_status("dev", "ri-1", [new_rihrid, new_rid, new_ridhrcv])

    # Topology byte-for-byte unchanged -- the core anti-churn guarantee.
    assert cache._client.get(topo_key) == topo_before

    # Status reflects the new slice; old rid-1 slice for ri-1 is gone.
    _, status_out = cache.load_layers("dev", loader)
    ids = {e.identifier for e in status_out}
    assert {"rid-2", "rihrid-2", "ridhrcv-2"} <= ids
    assert "rid-1" not in ids and "rihrid-1" not in ids and "ridhrcv-1" not in ids


def test_writethrough_preserves_other_instances(loader):
    cache = make_cache()
    topology, volatile = sample_graph()
    # add a second instance's volatile slice
    rid_b = RepoInstanceDeployment(
        identifier="rid-b", deployment_id="aaa", status="DEPLOY_NEXT"
    )
    rihrid_b = RepoInstanceHasRepoInstanceDeployment(
        identifier="rihrid-b", ref_from="ri-2", ref_to="rid-b", current="false"
    )
    cache.store_layers("dev", topology, volatile + [rid_b, rihrid_b])

    new_rid = RepoInstanceDeployment(
        identifier="rid-2", deployment_id="aaa", status="DEPLOYED"
    )
    new_rihrid = RepoInstanceHasRepoInstanceDeployment(
        identifier="rihrid-2", ref_from="ri-1", ref_to="rid-2", current="true"
    )
    cache.splice_status("dev", "ri-1", [new_rihrid, new_rid])

    _, status_out = cache.load_layers("dev", loader)
    ids = {e.identifier for e in status_out}
    # ri-2's slice is untouched by an ri-1 write-through
    assert {"rid-b", "rihrid-b"} <= ids


# --------------------------------------------------------------------------
# SPEC0008: targeted invalidation on structural changes
# --------------------------------------------------------------------------
def test_invalidate_all_topology_leaves_status(loader):
    cache = make_cache()
    topology, volatile = sample_graph()
    cache.store_layers("dev", topology, volatile)
    cache.store_layers("test", topology, volatile)

    cache.invalidate_all_topology()  # add_repo_class_version path

    assert cache._client.get(cache._topology_key("dev")) is None
    assert cache._client.get(cache._topology_key("test")) is None
    # status layers survive a version registration
    assert cache._client.get(cache._status_key("dev")) is not None
    assert cache._client.get(cache._status_key("test")) is not None


def test_invalidate_all_clears_both_layers():
    cache = make_cache()
    topology, volatile = sample_graph()
    cache.store_layers("dev", topology, volatile)

    cache.invalidate_all()  # apply_changeset path

    assert cache._client.get(cache._topology_key("dev")) is None
    assert cache._client.get(cache._status_key("dev")) is None


def test_invalidate_environment_only_targets_that_env():
    cache = make_cache()
    topology, volatile = sample_graph()
    cache.store_layers("dev", topology, volatile)
    cache.store_layers("test", topology, volatile)

    cache.invalidate_environment("dev")

    assert cache._client.get(cache._topology_key("dev")) is None
    assert cache._client.get(cache._status_key("dev")) is None
    assert cache._client.get(cache._topology_key("test")) is not None


# --------------------------------------------------------------------------
# SPEC0008: fail-open
# --------------------------------------------------------------------------
def test_fail_open_on_read_error(loader):
    fake = FakeRedis()
    cache = make_cache(fake)
    topology, volatile = sample_graph()
    cache.store_layers("dev", topology, volatile)

    fake.fail = True  # every subsequent redis op raises
    topo_out, status_out = cache.load_layers("dev", loader)
    assert topo_out is None and status_out is None  # caller falls back to Postgres
    assert cache.stats["fallback"] == 1


def test_fail_open_on_write_and_invalidate_do_not_raise():
    fake = FakeRedis()
    fake.fail = True
    cache = make_cache(fake)
    topology, volatile = sample_graph()
    # None of these may raise even though every redis op errors
    cache.store_layers("dev", topology, volatile)
    cache.splice_status("dev", "ri-1", volatile)
    cache.invalidate_all_topology()
    cache.invalidate_all()
    cache.invalidate_environment("dev")


# --------------------------------------------------------------------------
# SPEC0008: key namespacing / isolation
# --------------------------------------------------------------------------
def test_keys_namespaced_by_did_region_env():
    cache = make_cache()
    key = cache._topology_key("dev")
    assert key == (
        f"deploy:cache:{os.environ['HMD_DID']}:{os.environ['HMD_REGION']}:dev:topology"
    )


def test_env_isolation(loader):
    cache = make_cache()
    topology, volatile = sample_graph()
    cache.store_layers("dev", topology, volatile)

    # a different environment_type does not read dev's entries
    topo_out, status_out = cache.load_layers("test", loader)
    assert topo_out is None and status_out is None


def test_did_region_isolation(loader, monkeypatch):
    cache = make_cache()
    topology, volatile = sample_graph()
    cache.store_layers("dev", topology, volatile)

    # same env_type, different DID -> different key namespace -> miss
    monkeypatch.setenv("HMD_DID", "zzz")
    topo_out, status_out = cache.load_layers("dev", loader)
    assert topo_out is None and status_out is None


# --------------------------------------------------------------------------
# Feature flag default-off
# --------------------------------------------------------------------------
def test_disabled_by_default(monkeypatch):
    monkeypatch.delenv("HMD_ENV_CACHE_ENABLED", raising=False)
    cache = EnvCache()
    assert cache.is_enabled is False
    # all ops are safe no-ops when disabled
    topology, volatile = sample_graph()
    cache.store_layers("dev", topology, volatile)
    assert cache.load_layers("dev", DefaultLoader("schemas/local")) == (None, None)
