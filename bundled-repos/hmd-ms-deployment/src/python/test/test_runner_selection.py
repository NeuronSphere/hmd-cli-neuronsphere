"""The runner selection: submit to a local DAG runner instead of Argo.

Locally there is no Argo at all -- the ARGO_* variables are not set, so
generating a workflow raises KeyError. ``skip_async`` existed to stop this
service doing what it does in the cloud; configuring a runner endpoint makes
that a positive choice instead, and the cloud path is unchanged because nothing
sets the variable there.
"""

import datetime

import pytest

from hmd_ms_deployment import deployment_manager as dm


class FakeResponse:
    def __init__(self, status_code=200, payload=None, text=""):
        self.status_code = status_code
        self._payload = payload or {}
        self.text = text

    def json(self):
        return self._payload


class FakeClient:
    def __init__(self):
        self.upserted = []

    def upsert(self, entity):
        self.upserted.append(entity)


class FakeCSD:
    identifier = "csd-1"
    workflow_name = None
    workflow_submitted_at = None


def test_no_runner_configured_means_argo(monkeypatch):
    monkeypatch.delenv("HMD_WORKFLOW_RUNNER_URL", raising=False)
    assert dm._get_runner_url() is None


def test_an_empty_value_is_not_a_runner(monkeypatch):
    # An empty string is how a template that always sets the variable spells
    # "unset". Treating it as an endpoint would send every cloud deploy to "".
    monkeypatch.setenv("HMD_WORKFLOW_RUNNER_URL", "")
    assert dm._get_runner_url() is None


def test_submit_local_workflow_posts_the_node_manifest(monkeypatch):
    manifest = {"nodes": [{"instance_name": "thing"}], "destroy": False}
    monkeypatch.setattr(
        dm, "generate_local_deploy_manifest", lambda client, rs, csd_nid: dict(manifest)
    )

    posted = {}

    def fake_post(url, json=None, headers=None):
        posted["url"] = url
        posted["json"] = json
        posted["headers"] = headers
        return FakeResponse(payload={"metadata": {"name": "nsrun-abc"}})

    monkeypatch.setattr(dm.requests, "post", fake_post)

    manager = dm.DeploymentManager.__new__(dm.DeploymentManager)
    manager.client = FakeClient()
    manager.rs = object()
    csd = FakeCSD()

    result = manager.submit_local_workflow(csd, "local", "http://hmd_proxy/nsrunner/")

    # The route is Argo's, so selecting a runner stays a client choice rather
    # than a second protocol.
    assert posted["url"] == "http://hmd_proxy/nsrunner/api/v1/workflows/local"
    # The payload is not Argo's: there is no argoproj.io CRD locally.
    assert posted["json"]["namespace"] == "local"
    assert posted["json"]["workflow"]["nodes"] == manifest["nodes"]
    assert posted["json"]["workflow"]["csd_nid"] == "csd-1"
    assert posted["json"]["workflow"]["environment"] == "local"
    # No bearer token: the runner publishes no host port and is reachable only
    # from the platform network.
    assert not posted["headers"]

    # The workflow name is recorded exactly as Argo's is, so everything that
    # resolves logs and DAGs by workflow_name keeps working.
    assert result == {"workflow_name": "nsrun-abc"}
    assert csd.workflow_name == "nsrun-abc"
    assert isinstance(csd.workflow_submitted_at, datetime.datetime)
    assert manager.client.upserted == [csd]


def test_a_failed_submission_raises(monkeypatch):
    monkeypatch.setattr(
        dm, "generate_local_deploy_manifest", lambda client, rs, csd_nid: {"nodes": []}
    )
    monkeypatch.setattr(
        dm.requests,
        "post",
        lambda url, json=None, headers=None: FakeResponse(
            status_code=409, text="environment local is already deploying"
        ),
    )

    manager = dm.DeploymentManager.__new__(dm.DeploymentManager)
    manager.client = FakeClient()
    manager.rs = object()

    with pytest.raises(Exception) as err:
        manager.submit_local_workflow(FakeCSD(), "local", "http://runner")
    assert "already deploying" in str(err.value)


def test_an_unnamed_workflow_is_not_recorded(monkeypatch):
    # A runner that answered 200 without a name has told us nothing to store,
    # and writing "<unknown>" would make get_deployment_logs chase a workflow
    # that does not exist.
    monkeypatch.setattr(
        dm, "generate_local_deploy_manifest", lambda client, rs, csd_nid: {"nodes": []}
    )
    monkeypatch.setattr(
        dm.requests,
        "post",
        lambda url, json=None, headers=None: FakeResponse(payload={}),
    )

    manager = dm.DeploymentManager.__new__(dm.DeploymentManager)
    manager.client = FakeClient()
    manager.rs = object()
    csd = FakeCSD()

    assert manager.submit_local_workflow(csd, "local", "http://runner") == {
        "workflow_name": "<unknown>"
    }
    assert csd.workflow_name is None
    assert manager.client.upserted == []
