"""Unit tests for the Floci API Gateway quirks handled in ``floci_deployer``.

Floci runs with ``FLOCI_STORAGE_MODE: persistent`` and persists every API
Gateway **v1** entity (REST APIs, resources, stages, deployments) with all of
its fields null:

    "000000000000/us-west-2::a8b73e0e83" : { "id": null, "name": null, ... }

Floci 1.5.34 rehydrates those records into its store, so ``GET /restapis``
serves back "ghost" entries. botocore drops null members when parsing, so the
dict that reaches our code has no ``name``/``id`` key at all -- the naive
``api["name"]`` lookup raised ``KeyError: 'name'`` and broke every
``hmd neuronsphere up`` over a non-purged ``$HMD_HOME/floci/data``. A ghost has
no id, so it can never be deleted through the API; it can only be ignored, or
dropped from the persistent store before Floci starts.

These tests lock in both halves of the contract:

* the listing paths skip ghosts instead of crashing, and still find/reuse real
  gateways alongside them;
* ``prune_apigateway_ghosts`` drops the ghost records from the v1 stores --
  and only those, so a gateway the deployment DAG created survives a restart.

Run directly (``python -m pytest src/python/tests/test_floci_deployer.py``) or
via unittest -- no Floci, service, or Docker required.
"""

import itertools
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from hmd_cli_neuronsphere import floci_deployer as fd

# A REST API record as botocore parses it once Floci's null fields are dropped:
# only the non-null members survive, so `name` and `id` are simply absent.
GHOST_API = {"createdDate": 0, "endpointConfiguration": {"types": ["REGIONAL"]}}


class CreateApiGatewayGhosts(unittest.TestCase):
    def _client(self, items):
        client = mock.MagicMock()
        client.get_rest_apis.return_value = {"items": items}
        client.create_rest_api.return_value = {"id": "new123"}
        return client

    def test_ghost_only_falls_through_to_create(self):
        """A store holding nothing but ghosts must not raise -- just create."""
        client = self._client([GHOST_API])
        with mock.patch.object(fd, "_get_client", return_value=client):
            api_id = fd.create_api_gateway(api_name="neuronsphere-svc")

        self.assertEqual(api_id, "new123")
        client.create_rest_api.assert_called_once()
        client.delete_rest_api.assert_not_called()

    def test_real_api_found_alongside_ghost(self):
        """Ghosts are skipped, but a real same-named gateway is still reused."""
        client = self._client(
            [GHOST_API, {"id": "real456", "name": "neuronsphere-svc"}]
        )
        with mock.patch.object(fd, "_get_client", return_value=client):
            api_id = fd.create_api_gateway(api_name="neuronsphere-svc")

        self.assertEqual(api_id, "real456")
        client.create_rest_api.assert_not_called()

    def test_recreate_deletes_only_the_real_api(self):
        """`recreate` must delete the named gateway; a ghost has no id to delete."""
        client = self._client(
            [GHOST_API, {"id": "real456", "name": "neuronsphere-svc"}]
        )
        with mock.patch.object(fd, "_get_client", return_value=client):
            api_id = fd.create_api_gateway(api_name="neuronsphere-svc", recreate=True)

        client.delete_rest_api.assert_called_once_with(restApiId="real456")
        client.create_rest_api.assert_called_once()
        self.assertEqual(api_id, "new123")

    def test_other_named_api_is_left_alone(self):
        """A gateway for a different service is neither reused nor deleted."""
        client = self._client([{"id": "other", "name": "neuronsphere-elsewhere"}])
        with mock.patch.object(fd, "_get_client", return_value=client):
            api_id = fd.create_api_gateway(api_name="neuronsphere-svc", recreate=True)

        client.delete_rest_api.assert_not_called()
        self.assertEqual(api_id, "new123")


class AddApiGatewayRouteGhosts(unittest.TestCase):
    def test_ghost_resource_skipped_root_still_found(self):
        """A pathless ghost resource must not mask the real root resource."""
        api_client = mock.MagicMock()
        api_client.get_resources.return_value = {
            "items": [
                {"id": None},  # ghost: no path
                {"resourceMethods": {}},  # ghost: no path, no id
                {"id": "root1", "path": "/"},
            ]
        }
        api_client.create_resource.return_value = {"id": "proxy1"}

        lambda_client = mock.MagicMock()
        lambda_client.get_function.return_value = {
            "Configuration": {
                "FunctionArn": "arn:aws:lambda:us-west-2:000000000000:function:svc"
            }
        }

        # `_get_client` is per-Floci-account now: every call passes the target
        # the route is being created in, so the fake has to accept it.
        def _client(service, target=None):
            return lambda_client if service == "lambda" else api_client

        with mock.patch.object(fd, "_get_client", side_effect=_client):
            fd.add_api_gateway_route("api1", "svc", "svc")

        # `{proxy+}` is created as a child of the *real* root resource.
        api_client.create_resource.assert_called_once()
        self.assertEqual(
            api_client.create_resource.call_args.kwargs["parentId"], "root1"
        )
        # Methods registered on both the root and the proxy resource.
        put_targets = {
            c.kwargs["resourceId"] for c in api_client.put_method.call_args_list
        }
        self.assertEqual(put_targets, {"root1", "proxy1"})


class PruneApigatewayGhosts(unittest.TestCase):
    """Takes the Floci data directory itself, not an ``$HMD_HOME`` to derive it.

    Each environment keeps its Floci state under
    ``$HMD_HOME/.cache/environments/<slug>/floci/data`` while the control plane
    keeps its own at ``$HMD_HOME/floci/data``, so there is no single rule for
    getting from one to the other -- the caller passes the directory.
    """

    GHOST = {"id": None, "name": None, "description": None}
    CLI_API = {"id": "537b9444d7", "name": "neuronsphere-hmd_ms_dbaccount"}
    DAG_API = {"id": "a8b73e0e83", "name": "transform_hmd-ms-transform_x-rest-api"}

    def _data_dir(self, tmp, stores):
        data = Path(tmp) / "floci" / "data"
        data.mkdir(parents=True)
        for name, content in stores.items():
            (data / name).write_text(json.dumps(content))
        return data

    def test_a_real_gateway_survives_a_restart(self):
        """The regression: a CDKTF gateway the DAG created must not be wiped.

        Wiping it left the Lambda deployed with no gateway in front of it, and
        no later step recreates one -- `setup_service` only recreates the
        gateways this CLI owns -- so every `/<env>/<service>/` request fell
        through nginx's catch-all and answered `no route defined`.
        """
        with tempfile.TemporaryDirectory() as tmp:
            data = self._data_dir(
                tmp,
                {
                    "apigateway-apis.json": {
                        "000000000001/us-west-2::537b9444d7": self.CLI_API,
                        "000000000001/us-west-2::a8b73e0e83": self.DAG_API,
                    },
                    "apigateway-stages.json": {
                        "000000000001/us-west-2::a8b73e0e83::transform": {
                            "stageName": "transform"
                        },
                    },
                },
            )

            removed = fd.prune_apigateway_ghosts(data)

            self.assertEqual(removed, 0)
            apis = json.loads((data / "apigateway-apis.json").read_text())
            self.assertEqual(len(apis), 2)
            stages = json.loads((data / "apigateway-stages.json").read_text())
            self.assertEqual(len(stages), 1)

    def test_ghosts_are_dropped_and_real_gateways_kept(self):
        with tempfile.TemporaryDirectory() as tmp:
            data = self._data_dir(
                tmp,
                {
                    "apigateway-apis.json": {
                        "000000000001/us-west-2::ghost": self.GHOST,
                        "000000000001/us-west-2::a8b73e0e83": self.DAG_API,
                    },
                },
            )

            removed = fd.prune_apigateway_ghosts(data)

            self.assertEqual(removed, 1)
            apis = json.loads((data / "apigateway-apis.json").read_text())
            self.assertEqual(list(apis.values()), [self.DAG_API])

    def test_records_orphaned_by_a_dropped_gateway_go_with_it(self):
        """A stage/resource whose REST API is gone would never be reachable."""
        with tempfile.TemporaryDirectory() as tmp:
            data = self._data_dir(
                tmp,
                {
                    "apigateway-apis.json": {
                        "000000000001/us-west-2::ghost": self.GHOST,
                        "000000000001/us-west-2::a8b73e0e83": self.DAG_API,
                    },
                    "apigateway-resources.json": {
                        "000000000001/us-west-2::ghost::r1": {"path": None},
                        "000000000001/us-west-2::a8b73e0e83::r2": {"path": "/"},
                        # Left behind by an earlier run that dropped its API.
                        "000000000001/us-west-2::longgone::r3": {"path": "/"},
                    },
                },
            )

            removed = fd.prune_apigateway_ghosts(data)

            self.assertEqual(removed, 3)
            resources = json.loads((data / "apigateway-resources.json").read_text())
            self.assertEqual(
                list(resources), ["000000000001/us-west-2::a8b73e0e83::r2"]
            )

    def test_v2_and_other_services_are_untouched(self):
        with tempfile.TemporaryDirectory() as tmp:
            keep = {
                "apigatewayv2-apis.json": {"000000000001/us-west-2::v2": self.GHOST},
                "lambda-functions.json": {"fn": {"id": None}},
                "s3-buckets.json": {"bucket": {"id": None}},
            }
            data = self._data_dir(
                tmp,
                {
                    "apigateway-apis.json": {
                        "000000000001/us-west-2::ghost": self.GHOST
                    },
                    **keep,
                },
            )

            fd.prune_apigateway_ghosts(data)

            for name, content in keep.items():
                self.assertEqual(
                    json.loads((data / name).read_text()), content, f"{name} changed"
                )

    def test_a_sibling_environments_state_is_untouched(self):
        """Pruning one environment must not reach into another's data dir."""
        with tempfile.TemporaryDirectory() as tmp:
            mine = Path(tmp) / "environments" / "dev2" / "floci" / "data"
            theirs = Path(tmp) / "floci" / "data"
            for d in (mine, theirs):
                d.mkdir(parents=True)
                (d / "apigateway-apis.json").write_text(
                    json.dumps({"000000000001/us-west-2::ghost": self.GHOST})
                )

            fd.prune_apigateway_ghosts(mine)

            self.assertEqual(
                json.loads((mine / "apigateway-apis.json").read_text()), {}
            )
            self.assertEqual(
                len(json.loads((theirs / "apigateway-apis.json").read_text())), 1
            )

    def test_missing_dir_is_a_noop(self):
        """A cold `$HMD_HOME` has no floci/data yet -- must never raise."""
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(fd.prune_apigateway_ghosts(Path(tmp) / "nope"), 0)

    def test_a_missing_api_store_leaves_children_alone(self):
        """Without the API list every child looks orphaned -- do nothing."""
        with tempfile.TemporaryDirectory() as tmp:
            data = self._data_dir(
                tmp,
                {
                    "apigateway-stages.json": {
                        "000000000001/us-west-2::a8b73e0e83::transform": {
                            "stageName": "transform"
                        },
                    },
                },
            )

            self.assertEqual(fd.prune_apigateway_ghosts(data), 0)
            self.assertEqual(
                len(json.loads((data / "apigateway-stages.json").read_text())), 1
            )

    def test_unreadable_state_is_non_fatal(self):
        """`up` must not fail because a state file could not be parsed."""
        with tempfile.TemporaryDirectory() as tmp:
            data = self._data_dir(tmp, {})
            (data / "apigateway-apis.json").write_text("not json")

            self.assertEqual(fd.prune_apigateway_ghosts(data), 0)

    def test_write_failure_is_non_fatal(self):
        """`up` must not fail because a state file could not be rewritten."""
        with tempfile.TemporaryDirectory() as tmp:
            data = self._data_dir(
                tmp,
                {
                    "apigateway-apis.json": {
                        "000000000001/us-west-2::ghost": self.GHOST
                    },
                },
            )

            with mock.patch.object(
                Path, "write_text", side_effect=PermissionError("read-only")
            ):
                self.assertEqual(fd.prune_apigateway_ghosts(data), 0)


class WriteKubeconfigServerUrl(unittest.TestCase):
    """Where the kubeconfig's `server:` points, and why.

    k3s writes `https://127.0.0.1:6443`, which is only meaningful inside its own
    container. That gets rewritten to a host-reachable address -- and which one
    matters: the port Floci publishes on the `floci-eks-*` container is
    re-created by Docker every time `down`/`up` restarts it, and a re-created
    forward truncates writes past ~1 MTU. The 1449-byte post-quantum TLS 1.3
    ClientHello kubectl sends never arrives whole, so the handshake hangs against
    a perfectly healthy cluster. Callers with an environment therefore pass its
    `k3s_port` -- an hmd_proxy stream listener -- instead.
    """

    K3S_YAML = (
        "apiVersion: v1\nclusters:\n- cluster:\n"
        "    server: https://127.0.0.1:6443\n  name: default\n"
    )

    def _write(self, host_port, published="6500"):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "kubeconfig"
            with mock.patch.object(fd, "_resolve_target") as target, mock.patch.object(
                fd.requests, "get", side_effect=fd.requests.RequestException()
            ), mock.patch.object(
                fd, "_k3s_host_port", return_value=published
            ), mock.patch.object(
                fd, "k3s_container_name", return_value="floci-eks-x"
            ), mock.patch.object(
                fd.subprocess,
                "run",
                return_value=mock.Mock(returncode=0, stdout=self.K3S_YAML),
            ):
                target.return_value = mock.Mock(endpoint="http://floci:4566")
                kwargs = {} if host_port is None else {"host_port": host_port}
                fd.write_kubeconfig("cluster", out, **kwargs)
            return out.read_text()

    def test_an_explicit_host_port_wins_over_flocis_published_one(self):
        text = self._write(19064, published="6500")
        self.assertIn("server: https://localhost:19064", text)
        self.assertNotIn("6500", text)

    def test_falls_back_to_flocis_published_port(self):
        """Platform mode has no hmd_proxy env streams to point at."""
        self.assertIn("server: https://localhost:6500", self._write(None))

    def test_the_in_container_address_never_survives(self):
        self.assertNotIn("127.0.0.1:6443", self._write(19064))

    def test_flocis_own_kubeconfig_endpoint_is_rewritten_too(self):
        """It used to be returned verbatim, keeping the in-container address."""
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "kubeconfig"
            resp = mock.Mock(status_code=200, text=self.K3S_YAML)
            with mock.patch.object(fd, "_resolve_target") as target, mock.patch.object(
                fd.requests, "get", return_value=resp
            ), mock.patch.object(fd, "_k3s_host_port", return_value="6500"):
                target.return_value = mock.Mock(endpoint="http://floci:4566")
                fd.write_kubeconfig("cluster", out, host_port=19064)
            text = out.read_text()
        self.assertIn("server: https://localhost:19064", text)
        self.assertNotIn("127.0.0.1:6443", text)


if __name__ == "__main__":
    unittest.main()


class TfstateBucketAgreementTests(unittest.TestCase):
    """The tfstate bucket name is derived twice and must agree.

    `provision_resources` creates it; `hmd_lib_cdktf.HmdCdkTfStack` points its
    S3Backend at it from inside the projectbuilder container. A divergence makes
    `tofu init` fail with "S3 bucket does not exist" -- which reads like a
    provisioning failure rather than a naming one, and only at deploy time.

    The library's source is read as text rather than imported: importing it pulls
    in the jsii/CDKTF native runtime, which a unit test should not need.
    """

    def _backend_line(self):
        import importlib.util

        # `find_spec` on a dotted path raises ModuleNotFoundError (rather than
        # returning None) when the top-level package itself isn't installed --
        # it has to import `hmd_lib_cdktf` to look up the submodule's location.
        try:
            spec = importlib.util.find_spec("hmd_lib_cdktf.hmd_lib_cdktf")
        except ModuleNotFoundError:
            spec = None
        if spec is None or not spec.origin:
            self.skipTest("hmd_lib_cdktf is not installed")
        for line in Path(spec.origin).read_text().splitlines():
            if "tfstate" in line and "bucket=" in line:
                return line.strip()
        self.skipTest("no tfstate backend line found in hmd_lib_cdktf")

    def test_both_sides_name_the_bucket_the_same_way(self):
        backend = self._backend_line()
        self.assertIn("hmd.", backend)
        self.assertIn(".tfstate", backend)
        # account before region, matching provision_resources' f-string.
        self.assertLess(backend.index("account"), backend.index("hmd_region"), backend)

    def test_the_cli_builds_the_same_shape(self):
        import inspect

        source = inspect.getsource(fd.provision_resources)
        self.assertIn('f"hmd.{target.account_id}.{hmd_region}.tfstate"', source)


class StartNeptuneContainerReadinessTests(unittest.TestCase):
    """``docker start`` returning is not the same as Gremlin Server being up.

    A consumer aliased right after ``up`` returns can otherwise race a JVM
    still booting -- this pins that ``start_neptune_container`` only reports
    success once the port actually answers.
    """

    def _run(self, container, docker_start_rc, port_open_results):
        results = iter(port_open_results)

        def fake_run(cmd, **kwargs):
            if cmd[:2] == ["docker", "start"]:
                return mock.MagicMock(returncode=docker_start_rc, stderr="")
            if cmd[:2] == ["docker", "exec"]:
                return mock.MagicMock(returncode=next(results))
            raise AssertionError(f"unexpected command: {cmd}")

        with mock.patch.object(
            fd, "_stopped_floci_container", return_value=container
        ), mock.patch.object(
            fd.subprocess, "run", side_effect=fake_run
        ), mock.patch.object(
            fd.time, "sleep"
        ):
            return fd.start_neptune_container("graph-dev2")

    def test_waits_out_a_slow_boot_then_succeeds(self):
        # Port refused twice (JVM still booting), then accepts.
        ok = self._run(
            "floci-neptune-abc", docker_start_rc=0, port_open_results=[1, 1, 0]
        )
        self.assertTrue(ok)

    def test_fails_if_the_port_never_opens(self):
        with mock.patch.object(fd, "_gremlin_port_open", return_value=False):
            ok = self._run("floci-neptune-abc", docker_start_rc=0, port_open_results=[])
        self.assertFalse(ok)

    def test_fails_immediately_if_docker_start_fails(self):
        ok = self._run("floci-neptune-abc", docker_start_rc=1, port_open_results=[])
        self.assertFalse(ok)

    def test_the_probe_targets_the_address_gremlin_actually_binds(self):
        """Gremlin Server binds one address, not the wildcard.

        A running hmd-img-gremlin-server has a single listener on
        ``::ffff:<container ip>:8182`` and nothing on loopback, so the old
        ``127.0.0.1`` probe was refused however long the JVM had been up --
        every restart reported a healthy graph as one that never came up, which
        made the whole restart path a no-op.
        """
        probes = []

        def fake_run(cmd, **kwargs):
            if cmd[:2] == ["docker", "start"]:
                return mock.MagicMock(returncode=0, stderr="")
            probes.append(cmd[-1])
            return mock.MagicMock(returncode=0)

        with mock.patch.object(
            fd, "_stopped_floci_container", return_value="floci-neptune-abc"
        ), mock.patch.object(
            fd.subprocess, "run", side_effect=fake_run
        ), mock.patch.object(
            fd.time, "sleep"
        ):
            fd.start_neptune_container("graph-dev2")

        self.assertTrue(probes)
        self.assertNotIn("127.0.0.1", probes[0])
        self.assertIn("$(hostname)", probes[0])


class EnsureNeptuneRunningTests(unittest.TestCase):
    """`available` is never proof of a served graph.

    Floci stops the Neptune container it spawned on shutdown and, unlike RDS,
    never brings it back -- while the cluster record survives and keeps
    reporting `available` forever. A `down`/`up` therefore lands in exactly that
    state, and the old status-only wait burned its whole 300s timeout there
    before reporting "no graph" and failing the control-plane DAG.
    """

    def _run(self, running=None, stopped=None, start_ok=True, cluster=True):
        self.started = []

        def fake_start(identifier, target=None):
            self.started.append(identifier)
            return start_ok

        # A fake clock, so the "still spawning" case reaches its deadline in
        # three iterations rather than spinning for a real 300 seconds.
        clock = itertools.count(0, 100)

        with mock.patch.object(
            fd, "_resolve_target", return_value=mock.MagicMock(account_id="1")
        ), mock.patch.object(
            fd, "neptune_container_name", side_effect=lambda i, t=None: running
        ), mock.patch.object(
            fd, "_stopped_floci_container", return_value=stopped
        ), mock.patch.object(
            fd, "start_neptune_container", side_effect=fake_start
        ), mock.patch.object(
            fd, "neptune_cluster_exists", return_value=cluster
        ), mock.patch.object(
            fd.time, "time", side_effect=lambda: next(clock)
        ), mock.patch.object(
            fd.time, "sleep"
        ) as sleep:
            self.sleep = sleep
            return fd.ensure_neptune_running("graph-cp", timeout=300)

    def test_a_running_container_is_returned_untouched(self):
        name = self._run(running="floci-neptune-abc")
        self.assertEqual(name, "floci-neptune-abc")
        # Starting an already-running container would be a pointless restart.
        self.assertEqual(self.started, [])

    def test_a_stopped_container_is_started(self):
        """The down/up regression: the graph must come back, not time out."""
        names = iter([None, "floci-neptune-abc"])

        with mock.patch.object(
            fd, "_resolve_target", return_value=mock.MagicMock(account_id="1")
        ), mock.patch.object(
            fd, "neptune_container_name", side_effect=lambda i, t=None: next(names)
        ), mock.patch.object(
            fd, "_stopped_floci_container", return_value="floci-neptune-abc"
        ), mock.patch.object(
            fd, "start_neptune_container", return_value=True
        ) as start, mock.patch.object(
            fd.time, "sleep"
        ) as sleep:
            name = fd.ensure_neptune_running("graph-cp")

        self.assertEqual(name, "floci-neptune-abc")
        start.assert_called_once()
        sleep.assert_not_called()

    def test_a_container_that_will_not_start_is_a_failure_not_a_missing_graph(self):
        self.assertIsNone(self._run(stopped="floci-neptune-abc", start_ok=False))
        self.sleep.assert_not_called()

    def test_no_cluster_record_returns_immediately(self):
        """Nothing was ever deployed here -- there is nothing to wait for."""
        self.assertIsNone(self._run(cluster=False))
        # The 300s dead wait must not come back.
        self.sleep.assert_not_called()

    def test_a_cluster_with_no_container_yet_is_waited_on(self):
        """The one genuinely transient state: Floci is still spawning it."""
        self.assertIsNone(self._run(cluster=True))
        self.assertTrue(self.sleep.called)
