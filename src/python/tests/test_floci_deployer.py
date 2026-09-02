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
* ``clear_apigateway_state`` drops the v1 store files (and only those).

Run directly (``python -m pytest src/python/tests/test_floci_deployer.py``) or
via unittest -- no Floci, service, or Docker required.
"""

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


class ClearApigatewayState(unittest.TestCase):
    """Takes the Floci data directory itself, not an ``$HMD_HOME`` to derive it.

    Each environment keeps its Floci state under
    ``$HMD_HOME/.cache/environments/<slug>/floci/data`` while the control plane
    keeps its own at ``$HMD_HOME/floci/data``, so there is no single rule for
    getting from one to the other -- the caller passes the directory.
    """

    def test_removes_v1_files_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            data = Path(tmp) / "floci" / "data"
            data.mkdir(parents=True)
            v1 = ["apigateway-apis.json", "apigateway-resources.json"]
            keep = [
                "apigatewayv2-apis.json",
                "lambda-functions.json",
                "s3-buckets.json",
            ]
            for name in v1 + keep:
                (data / name).write_text("{}")

            fd.clear_apigateway_state(data)

            for name in v1:
                self.assertFalse((data / name).exists(), f"{name} should be removed")
            for name in keep:
                self.assertTrue((data / name).exists(), f"{name} must be preserved")

    def test_a_sibling_environments_state_is_untouched(self):
        """Clearing one environment must not reach into another's data dir."""
        with tempfile.TemporaryDirectory() as tmp:
            mine = Path(tmp) / "environments" / "dev2" / "floci" / "data"
            theirs = Path(tmp) / "floci" / "data"
            for d in (mine, theirs):
                d.mkdir(parents=True)
                (d / "apigateway-apis.json").write_text("{}")

            fd.clear_apigateway_state(mine)

            self.assertFalse((mine / "apigateway-apis.json").exists())
            self.assertTrue((theirs / "apigateway-apis.json").exists())

    def test_missing_dir_is_a_noop(self):
        """A cold `$HMD_HOME` has no floci/data yet -- must never raise."""
        with tempfile.TemporaryDirectory() as tmp:
            fd.clear_apigateway_state(Path(tmp) / "nope")

    def test_unlink_failure_is_non_fatal(self):
        """`up` must not fail because a state file could not be removed."""
        with tempfile.TemporaryDirectory() as tmp:
            data = Path(tmp) / "floci" / "data"
            data.mkdir(parents=True)
            (data / "apigateway-apis.json").write_text("{}")

            with mock.patch.object(
                Path, "unlink", side_effect=PermissionError("read-only")
            ):
                fd.clear_apigateway_state(tmp)


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

        spec = importlib.util.find_spec("hmd_lib_cdktf.hmd_lib_cdktf")
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
