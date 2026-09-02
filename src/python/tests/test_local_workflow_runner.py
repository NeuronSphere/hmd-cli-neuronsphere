"""Unit tests for LocalWorkflowRunner local-source deploy selection.

A local NeuronSphere has no Artifact Librarian, so the in-container ``hmd deploy``
must take its code from a local source: the mounted workspace (primary, via
``--local``) or a previously-downloaded artifact bundle (secondary, via
``HMD_ARTIFACT_ROOT``). These tests cover the two pure helpers that drive that:

* ``_localize_deploy_script`` — injects ``--local`` into the generated deploy command;
* ``LocalWorkflowRunner._resolve_artifact_bundle_dir`` — selects a bundle dir when one
  holds the repo's ``<repo>_<ver>_build.zip``.

Run directly (``python -m pytest src/python/tests/test_local_workflow_runner.py``) —
no service or Docker required.
"""

import json
import os
import tempfile
import unittest
from unittest import mock

import yaml

from hmd_cli_neuronsphere import bom_seeder as b
from hmd_cli_neuronsphere import local_workflow_runner as lwr
from hmd_cli_neuronsphere.image_cache import ImageUnavailable
from hmd_cli_neuronsphere.local_workflow_runner import (
    LocalWorkflowRunner,
    _localize_deploy_script,
)

# The shape ms-deployment's deploy_base.deploy_node emits.
GENERATED = (
    "export HMD_REPO_INSTANCE_DEPLOYMENT_ID=rid-123\n"
    "hmd --debug  --repo-name hmd-inf-ext-secrets-crds --repo-version 0.1 "
    "--hmd-region reg1 deploy  --instance-name ext-secrets-crds "
    "--environment local --deployment-id local --config-file STDIN <<'EOF'\n"
    '{"deploy": "value", "nested": {"deploy": true}}\n'
    "EOF"
)


class LocalizeDeployScriptTests(unittest.TestCase):
    def test_injects_local_once_on_command_line(self):
        out = _localize_deploy_script(GENERATED)
        lines = out.splitlines()
        self.assertIn(" deploy --local ", lines[1])
        self.assertEqual(lines[1].count("--local"), 1)

    def test_leaves_export_and_heredoc_untouched(self):
        lines = _localize_deploy_script(GENERATED).splitlines()
        self.assertEqual(lines[0], "export HMD_REPO_INSTANCE_DEPLOYMENT_ID=rid-123")
        self.assertEqual(lines[2], '{"deploy": "value", "nested": {"deploy": true}}')
        self.assertEqual(lines[3], "EOF")

    def test_idempotent(self):
        once = _localize_deploy_script(GENERATED)
        self.assertEqual(_localize_deploy_script(once), once)

    def test_already_local_unchanged(self):
        s = "hmd --repo-name x --repo-version 1 deploy --local --instance-name y"
        self.assertEqual(_localize_deploy_script(s), s)

    def test_destroy_variant(self):
        s = "hmd --repo-name x --repo-version 1 deploy --destroy --instance-name y"
        self.assertEqual(
            _localize_deploy_script(s),
            "hmd --repo-name x --repo-version 1 deploy --local --destroy --instance-name y",
        )

    def test_non_deploy_script_untouched(self):
        self.assertEqual(
            _localize_deploy_script("bash src/local/deploy_local.sh"),
            "bash src/local/deploy_local.sh",
        )

    def test_empty(self):
        self.assertEqual(_localize_deploy_script(""), "")


class ResolveArtifactBundleDirTests(unittest.TestCase):
    def _node(self):
        return {
            "repo_class_name": "hmd-inf-ext-secrets-crds",
            "repo_class_version": "0.1",
        }

    def test_none_when_env_unset(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertIsNone(
                LocalWorkflowRunner._resolve_artifact_bundle_dir(self._node())
            )

    def test_selects_dir_when_bundle_present(self):
        with tempfile.TemporaryDirectory() as d:
            open(os.path.join(d, "hmd-inf-ext-secrets-crds_0.1_build.zip"), "w").close()
            with mock.patch.dict(
                os.environ, {"HMD_LOCAL_DEPLOY_ARTIFACT_ROOT": d}, clear=True
            ):
                self.assertEqual(
                    LocalWorkflowRunner._resolve_artifact_bundle_dir(self._node()), d
                )

    def test_none_when_bundle_absent(self):
        with tempfile.TemporaryDirectory() as d:
            with mock.patch.dict(
                os.environ, {"HMD_LOCAL_DEPLOY_ARTIFACT_ROOT": d}, clear=True
            ):
                self.assertIsNone(
                    LocalWorkflowRunner._resolve_artifact_bundle_dir(self._node())
                )


RAW_KUBECONFIG = {
    "apiVersion": "v1",
    "clusters": [
        {
            "name": "default",
            "cluster": {
                "server": "https://localhost:6500",
                "certificate-authority-data": "ZmFrZQ==",
            },
        }
    ],
    "contexts": [{"name": "default", "context": {"cluster": "default"}}],
    "current-context": "default",
    "kind": "Config",
}


class KubeconfigForContainerTests(unittest.TestCase):
    def _write_kubeconfig(self, tmpdir):
        path = os.path.join(tmpdir, "kubeconfig")
        with open(path, "w") as f:
            yaml.safe_dump(RAW_KUBECONFIG, f)
        return path

    def test_rewrites_server_to_in_network_endpoint(self):
        with tempfile.TemporaryDirectory() as d:
            path = self._write_kubeconfig(d)
            runner = LocalWorkflowRunner("http://x", cluster_name="neuronsphere")
            with mock.patch.object(runner, "_local_kubeconfig_path", return_value=path):
                out_path = runner._kubeconfig_for_container()
            self.assertNotEqual(out_path, path)
            with open(out_path) as f:
                cfg = yaml.safe_load(f)
            cluster = cfg["clusters"][0]["cluster"]
            self.assertEqual(cluster["server"], "https://floci-eks-neuronsphere:6443")
            self.assertTrue(cluster["insecure-skip-tls-verify"])
            self.assertNotIn("certificate-authority-data", cluster)

    def test_falls_back_to_raw_path_without_cluster_name(self):
        with tempfile.TemporaryDirectory() as d:
            path = self._write_kubeconfig(d)
            runner = LocalWorkflowRunner("http://x")
            with mock.patch.object(runner, "_local_kubeconfig_path", return_value=path):
                self.assertEqual(runner._kubeconfig_for_container(), path)

    def test_falls_back_when_no_kubeconfig_found(self):
        runner = LocalWorkflowRunner("http://x", cluster_name="neuronsphere")
        with mock.patch.object(runner, "_local_kubeconfig_path", return_value=None):
            self.assertIsNone(runner._kubeconfig_for_container())

    def test_falls_back_on_unparseable_kubeconfig(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "kubeconfig")
            with open(path, "w") as f:
                f.write("not: valid: yaml: [")
            runner = LocalWorkflowRunner("http://x", cluster_name="neuronsphere")
            with mock.patch.object(runner, "_local_kubeconfig_path", return_value=path):
                self.assertEqual(runner._kubeconfig_for_container(), path)


class RunStatusTests(unittest.TestCase):
    """A destroy run settles nodes into DESTROYED, not DEPLOYED.

    Getting this backwards would mark torn-down instances as deployed, so the
    next reconcile would keep proposing to destroy them forever.
    """

    NODES = [
        {
            "instance_name": "local-neuronsphere",
            "repo_class_name": "hmd-cli-neuronsphere",
            "rid_nid": "rid-core",
        },
        {
            "instance_name": "my-api",
            "repo_class_name": "hmd-ms-myapi",
            "rid_nid": "rid-api",
        },
    ]

    def _runner(self, execute_returns=True):
        runner = LocalWorkflowRunner("http://x")
        self.statuses = []
        self.csd_statuses = []
        mock.patch.object(
            runner,
            "_set_status",
            side_effect=lambda rid, status: self.statuses.append((rid, status)),
        ).start()
        mock.patch.object(
            runner,
            "_set_csd_status",
            side_effect=lambda csd, status: self.csd_statuses.append(status),
        ).start()
        mock.patch.object(
            runner,
            "_execute_in_projectbuilder",
            return_value=lwr._NodeResult(execute_returns),
        ).start()
        self.addCleanup(mock.patch.stopall)
        return runner

    def test_deploy_marks_nodes_deployed(self):
        runner = self._runner()
        self.assertTrue(runner.run("csd-1", self.NODES))
        self.assertEqual(
            self.statuses, [("rid-core", "DEPLOYED"), ("rid-api", "DEPLOYED")]
        )
        self.assertEqual(self.csd_statuses, ["STARTED", "COMPLETED"])

    def test_destroy_marks_nodes_destroyed(self):
        runner = self._runner()
        self.assertTrue(runner.run("csd-1", self.NODES, destroy=True))
        self.assertEqual(
            self.statuses, [("rid-core", "DESTROYED"), ("rid-api", "DESTROYED")]
        )
        self.assertEqual(self.csd_statuses, ["STARTED", "DESTROYED"])

    def test_last_succeeded_tracks_what_landed(self):
        runner = self._runner()
        runner.run("csd-1", self.NODES)
        self.assertEqual(runner.last_succeeded, ["local-neuronsphere", "my-api"])

    def test_a_failure_stops_the_run_and_is_not_recorded(self):
        runner = self._runner(execute_returns=False)
        self.assertFalse(runner.run("csd-1", self.NODES))
        # The core no-op still landed; the real node did not.
        self.assertEqual(runner.last_succeeded, ["local-neuronsphere"])
        self.assertIn(("rid-api", "FAILED"), self.statuses)


class EnvRoutePrefixTests(unittest.TestCase):
    """In-container deploys must address the environment's own services.

    Only the control plane is served unprefixed. ms-dbaccount is per-environment
    and routed at ``/<slug>/hmd_ms_dbaccount/``, so a deploy tool that joins its
    path onto a bare ``http://hmd_proxy`` hits a control-plane path where that
    service does not exist -- which is a 404 on ``create_db_account`` and a
    failed ``hmd-database-account`` node.
    """

    class _Env:
        def __init__(self, slug, legacy=False):
            self.slug = slug
            self.deployment_id = slug
            self.k3s_cluster = f"ns-{slug}"
            self.floci_container = f"floci-{slug}"
            self.floci_alias = f"neuronsphere-{slug}"
            self.legacy_layout = legacy

    def test_prefix_is_the_environment_slug(self):
        runner = LocalWorkflowRunner("http://x", env=self._Env("dev2"))
        self.assertEqual(runner.env_route_prefix(), "/dev2")

    def test_a_legacy_environment_is_prefixed_too(self):
        # A legacy environment shares the control plane's containers, not its
        # unprefixed routes -- write_env_routes prefixes on slug regardless.
        runner = LocalWorkflowRunner("http://x", env=self._Env("local", legacy=True))
        self.assertEqual(runner.env_route_prefix(), "/local")

    def test_no_environment_means_no_prefix(self):
        self.assertEqual(LocalWorkflowRunner("http://x").env_route_prefix(), "")

    def test_proxy_url_points_at_the_environments_dbaccount(self):
        runner = LocalWorkflowRunner(
            "http://localhost/hmd_ms_deployment", env=self._Env("dev2")
        )
        with mock.patch.dict(os.environ, {}, clear=True):
            proxy = runner.ns_local_proxy()
        self.assertEqual(proxy, "http://hmd_proxy/dev2")
        # The join hmd-cli-dbaccount performs must match the nginx location.
        self.assertEqual(
            f"{proxy}/hmd_ms_dbaccount/api/create_db_account",
            "http://hmd_proxy/dev2/hmd_ms_dbaccount/api/create_db_account",
        )

    def test_proxy_url_rewrites_the_host_for_in_container_use(self):
        runner = LocalWorkflowRunner(
            "http://127.0.0.1/hmd_ms_deployment", env=self._Env("local")
        )
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(runner.ns_local_proxy(), "http://hmd_proxy/local")

    def test_proxy_url_is_overridable(self):
        runner = LocalWorkflowRunner("http://x", env=self._Env("dev2"))
        with mock.patch.dict(
            os.environ, {"NS_LOCAL_PROXY": "http://elsewhere:8080"}, clear=True
        ):
            self.assertEqual(runner.ns_local_proxy(), "http://elsewhere:8080")


class RepoPathOverrideTests(unittest.TestCase):
    """A manifest may declare a repo outside HMD_REPO_HOME."""

    def setUp(self):
        # No bundled artifacts unless a test makes one: the real external/ dir
        # is populated by a build, so leaving it in scope would make these
        # assertions depend on how the tree was prepared.
        self._roots = mock.patch.object(b, "_artifact_roots", return_value=[])
        self._roots.start()
        b._reset_artifact_version_index()

    def tearDown(self):
        self._roots.stop()
        b._reset_artifact_version_index()

    def test_declared_path_wins_when_it_exists(self):
        with tempfile.TemporaryDirectory() as d:
            runner = LocalWorkflowRunner("http://x", repo_paths={"hmd-ms-myapi": d})
            self.assertEqual(runner._repo_path("hmd-ms-myapi"), d)

    def test_falls_back_to_repo_home_when_the_declared_path_is_gone(self):
        runner = LocalWorkflowRunner(
            "http://x", repo_paths={"hmd-ms-myapi": "/nonexistent/path"}
        )
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertIsNone(runner._repo_path("hmd-ms-myapi"))

    def test_undeclared_repo_uses_repo_home(self):
        with tempfile.TemporaryDirectory() as d:
            os.makedirs(os.path.join(d, "hmd-ms-other"))
            runner = LocalWorkflowRunner("http://x", repo_paths={})
            with mock.patch.dict(os.environ, {"HMD_REPO_HOME": d}, clear=True):
                self.assertEqual(
                    runner._repo_path("hmd-ms-other"),
                    os.path.join(d, "hmd-ms-other"),
                )


class UnresolvedWorkspaceTests(unittest.TestCase):
    """An unmountable repo must fail the node, not run docker from '/'.

    Without a resolvable workspace, the projectbuilder container falls back
    to its image's default WORKDIR and any relative-path tool inside the
    deploy script (e.g. hmd-cli-cdktf's ``os.chdir("src/cdktf")``) blows up
    with a confusing downstream FileNotFoundError. The runner must catch the
    unresolved workspace itself and fail clearly before touching docker.
    """

    def setUp(self):
        self._roots = mock.patch.object(b, "_artifact_roots", return_value=[])
        self._roots.start()
        b._reset_artifact_version_index()

    def tearDown(self):
        self._roots.stop()
        b._reset_artifact_version_index()

    def test_fails_without_invoking_docker(self):
        runner = LocalWorkflowRunner("http://x", repo_paths={})
        node = {
            "instance_name": "hive-metastore",
            "repo_class_name": "hmd-inf-hive-metastore",
            "rid_nid": "rid-1",
            "script": "hmd deploy --local",
        }
        with mock.patch.dict(os.environ, {}, clear=True):
            with mock.patch(
                "hmd_cli_neuronsphere.local_workflow_runner.subprocess.run"
            ) as mock_run:
                result = runner._execute_in_projectbuilder(node)

        self.assertFalse(result.success)
        mock_run.assert_not_called()


class BundledArtifactSourceTests(unittest.TestCase):
    """The code deployed is the build the node is registered as.

    A checkout used to be mounted unconditionally, so a developer's tree
    deployed under the bundled artifact's version number. Now the artifact wins
    and the tree is mounted only under a local version override.
    """

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = self._tmp.name
        self.repo_home = os.path.join(self.root, "repos")
        os.makedirs(os.path.join(self.repo_home, "hmd-ms-myapi"))

        artifacts = os.path.join(self.root, "external")
        self.bundle = os.path.join(artifacts, "myapi")
        os.makedirs(os.path.join(self.bundle, "meta-data"))
        with open(os.path.join(self.bundle, "meta-data", "VERSION"), "w") as f:
            f.write("1.0.0\n")
        with open(os.path.join(self.bundle, "meta-data", "manifest.json"), "w") as f:
            f.write('{"name": "hmd-ms-myapi"}')

        self._roots = mock.patch.object(b, "_artifact_roots", return_value=[artifacts])
        self._roots.start()
        b._reset_artifact_version_index()
        self._env = mock.patch.dict(
            os.environ, {"HMD_REPO_HOME": self.repo_home}, clear=True
        )
        self._env.start()

    def tearDown(self):
        self._env.stop()
        self._roots.stop()
        b._reset_artifact_version_index()
        self._tmp.cleanup()

    def test_the_bundled_artifact_is_mounted_over_a_checkout(self):
        runner = LocalWorkflowRunner("http://x", repo_paths={})
        self.assertEqual(runner._repo_path("hmd-ms-myapi"), self.bundle)

    def test_a_local_override_mounts_the_checkout(self):
        runner = LocalWorkflowRunner("http://x", repo_paths={})
        with mock.patch.dict(
            os.environ,
            {b._local_version_env_var("hmd-ms-myapi"): "local"},
            clear=False,
        ):
            self.assertEqual(
                runner._repo_path("hmd-ms-myapi"),
                os.path.join(self.repo_home, "hmd-ms-myapi"),
            )

    def test_a_declared_source_path_still_loses_to_the_artifact(self):
        declared = os.path.join(self.root, "elsewhere")
        os.makedirs(declared)
        runner = LocalWorkflowRunner("http://x", repo_paths={"hmd-ms-myapi": declared})
        self.assertEqual(runner._repo_path("hmd-ms-myapi"), self.bundle)

    def test_a_declared_source_path_is_used_under_an_override(self):
        declared = os.path.join(self.root, "elsewhere")
        os.makedirs(declared)
        runner = LocalWorkflowRunner("http://x", repo_paths={"hmd-ms-myapi": declared})
        with mock.patch.dict(
            os.environ, {b.PREFER_LOCAL_VERSIONS_ENV: "true"}, clear=False
        ):
            self.assertEqual(runner._repo_path("hmd-ms-myapi"), declared)

    def test_a_repo_with_no_artifact_still_uses_its_checkout(self):
        os.makedirs(os.path.join(self.repo_home, "hmd-ms-unbundled"))
        runner = LocalWorkflowRunner("http://x", repo_paths={})
        self.assertEqual(
            runner._repo_path("hmd-ms-unbundled"),
            os.path.join(self.repo_home, "hmd-ms-unbundled"),
        )


class EnsureK3sImageTests(unittest.TestCase):
    """Per-node staging of a chart's image into the k3s node's containerd.

    k3s reads images from its own containerd, not the host docker daemon.
    hmd-cli-helm has an importer for exactly this, but it no-ops in the deploy
    path: it needs a ``docker`` CLI to reach the host daemon and the
    projectbuilder container ships nerdctl instead. So for a chart whose image was
    just built locally (`hmd build`, then a local version override) the runner
    imports it here, on the host, where a real docker CLI exists.

    Best-effort throughout: an image that is not in the host cache is one k3s can
    pull from the registry, which is the normal published-version path.
    """

    IMAGE_REPO = "ghcr.io/hmdlabs/hmd-app-neuronsphere"

    def _repo(self, tmpdir, deploy_commands, image_repository=IMAGE_REPO):
        meta = os.path.join(tmpdir, "meta-data")
        os.makedirs(meta)
        manifest = {"deploy": {"commands": deploy_commands}}
        if image_repository is not None:
            manifest["deploy"]["default_configuration"] = {
                "image": {"repository": image_repository}
            }
        with open(os.path.join(meta, "manifest.json"), "w") as f:
            json.dump(manifest, f)
        return tmpdir

    def _runner(self, repo_dir):
        runner = LocalWorkflowRunner("http://x", cluster_name="neuronsphere")
        runner._repo_path = lambda repo_class_name: repo_dir
        return runner

    def _node(self, version="0.1"):
        return {
            "repo_class_name": "hmd-app-neuronsphere",
            "instance_name": "deployment-gui",
            "repo_class_version": version,
        }

    def test_no_op_for_a_repo_without_a_chart_image(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["cdktf"]], image_repository=None))
            with mock.patch.object(lwr, "_import_image_into_k3s") as imp:
                self.assertTrue(runner._ensure_k3s_image(self._node()))
            imp.assert_not_called()

    def test_no_op_for_a_repo_that_does_not_deploy_helm(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["docker"], ["cdktf"]]))
            with mock.patch.object(lwr, "_import_image_into_k3s") as imp:
                self.assertTrue(runner._ensure_k3s_image(self._node()))
            imp.assert_not_called()

    def test_imports_under_the_ref_the_chart_asks_for(self):
        """`ctr images import` names the image from the tar, so the ref imported
        must be the one the chart's pod spec will ask for."""
        chart_ref = f"{self.IMAGE_REPO}:0.1"
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["cdktf"], ["helm"]]))
            with mock.patch.object(
                lwr, "container_cli", return_value="docker"
            ), mock.patch.object(
                lwr, "_image_cached", side_effect=lambda ref, cli: ref == chart_ref
            ), mock.patch.object(
                lwr, "_tag"
            ) as tag, mock.patch.object(
                lwr, "_import_image_into_k3s", return_value=True
            ) as imp:
                self.assertTrue(runner._ensure_k3s_image(self._node()))
            # Already under the chart's ref, so there is nothing to re-tag.
            tag.assert_not_called()
            imp.assert_called_once_with(chart_ref, "floci-eks-neuronsphere", "docker")

    def test_retags_the_first_cached_candidate(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["cdktf"], ["helm"]]))
            with mock.patch.dict(
                os.environ, {"HMD_CONTAINER_REGISTRY": "local.reg"}, clear=False
            ), mock.patch.object(
                lwr, "container_cli", return_value="docker"
            ), mock.patch.object(
                lwr, "_image_cached", side_effect=lambda ref, cli: "local.reg" in ref
            ), mock.patch.object(
                lwr, "_tag", return_value=True
            ) as tag, mock.patch.object(
                lwr, "_import_image_into_k3s", return_value=True
            ):
                self.assertTrue(runner._ensure_k3s_image(self._node()))
            tag.assert_called_once_with(
                "local.reg/hmd-app-neuronsphere:0.1", f"{self.IMAGE_REPO}:0.1", "docker"
            )

    def test_a_cache_miss_leaves_the_registry_pull_to_k3s(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["cdktf"], ["helm"]]))
            with mock.patch.object(
                lwr, "container_cli", return_value="docker"
            ), mock.patch.object(
                lwr, "_image_cached", return_value=False
            ), mock.patch.object(
                lwr, "_import_image_into_k3s"
            ) as imp:
                self.assertTrue(runner._ensure_k3s_image(self._node()))
            imp.assert_not_called()

    def test_a_failed_import_does_not_fail_the_node(self):
        """The registry pull is still a valid outcome, so this stays advisory."""
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["cdktf"], ["helm"]]))
            with mock.patch.object(
                lwr, "container_cli", return_value="docker"
            ), mock.patch.object(
                lwr, "_image_cached", return_value=True
            ), mock.patch.object(
                lwr, "_tag", return_value=True
            ), mock.patch.object(
                lwr, "_import_image_into_k3s", return_value=False
            ):
                self.assertTrue(runner._ensure_k3s_image(self._node()))

    def test_no_op_without_a_container_cli(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["cdktf"], ["helm"]]))
            with mock.patch.object(
                lwr, "container_cli", return_value=None
            ), mock.patch.object(lwr, "_import_image_into_k3s") as imp:
                self.assertTrue(runner._ensure_k3s_image(self._node()))
            imp.assert_not_called()


class EnsureNodeImageTests(unittest.TestCase):
    """Per-node staging of the image Floci runs a local Lambda from.

    The CDKTF factory hands Floci a bare ``<repo>:<version>`` tag, which is
    unpullable (an unqualified name resolves to Docker Hub). Nothing inside the
    projectbuilder container can stage it -- there is no docker CLI in there --
    so the runner does it on the host before the node deploys, and fails the
    node with an actionable message rather than letting Floci 404 at Lambda
    start time, after the deploy claimed success.
    """

    def _repo(self, tmpdir, deploy_commands, version="0.2.457"):
        """A repo dir with the manifest/VERSION the gate reads."""
        meta = os.path.join(tmpdir, "meta-data")
        os.makedirs(meta)
        with open(os.path.join(meta, "manifest.json"), "w") as f:
            json.dump({"deploy": {"commands": deploy_commands}}, f)
        with open(os.path.join(meta, "VERSION"), "w") as f:
            f.write(f"{version}\n")
        return tmpdir

    def _runner(self, repo_dir):
        runner = LocalWorkflowRunner("http://x")
        runner._repo_path = lambda repo_class_name: repo_dir
        return runner

    def _node(self, version="0.2.457"):
        node = {"repo_class_name": "hmd-ms-transform", "instance_name": "transform"}
        if version is not None:
            node["repo_class_version"] = version
        return node

    def test_lambda_repo_stages_its_image(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["docker"], ["cdktf"]]))
            with mock.patch.object(lwr, "ensure_lambda_image") as ensure:
                self.assertTrue(runner._ensure_node_image(self._node())[0])
            ensure.assert_called_once_with("hmd-ms-transform", "0.2.457")

    def test_unavailable_image_fails_the_node(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["docker"], ["cdktf"]]))
            with mock.patch.dict(os.environ, {}, clear=True):
                with mock.patch.object(
                    lwr,
                    "ensure_lambda_image",
                    side_effect=ImageUnavailable("hmd-ms-transform", "0.2.457", ["a"]),
                ):
                    ok, message = runner._ensure_node_image(self._node())
                    self.assertFalse(ok)
                    self.assertTrue(message)

    def test_skip_env_downgrades_failure_to_a_warning(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["docker"], ["cdktf"]]))
            with mock.patch.dict(
                os.environ, {"HMD_LOCAL_SKIP_IMAGE_PREPULL": "true"}, clear=True
            ):
                with mock.patch.object(
                    lwr,
                    "ensure_lambda_image",
                    side_effect=ImageUnavailable("hmd-ms-transform", "0.2.457", ["a"]),
                ):
                    self.assertTrue(runner._ensure_node_image(self._node())[0])

    def test_helm_only_repo_is_not_staged(self):
        """No docker deploy command means no Lambda image to stage."""
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["helm"]]))
            with mock.patch.object(lwr, "ensure_lambda_image") as ensure:
                self.assertTrue(runner._ensure_node_image(self._node())[0])
            ensure.assert_not_called()

    def test_unresolvable_repo_is_not_staged(self):
        """No manifest to read (bundle-only deploy): behave as before, skip."""
        runner = self._runner(None)
        with mock.patch.object(lwr, "ensure_lambda_image") as ensure:
            self.assertTrue(runner._ensure_node_image(self._node())[0])
        ensure.assert_not_called()

    def test_version_falls_back_to_the_repo_tree(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["docker"]], version="0.3.9"))
            with mock.patch.object(lwr, "ensure_lambda_image") as ensure:
                self.assertTrue(runner._ensure_node_image(self._node(version=None))[0])
            ensure.assert_called_once_with("hmd-ms-transform", "0.3.9")

    def test_destroy_does_not_stage(self):
        """A teardown removes a Lambda; it never needs the image."""
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["docker"]]))
            with mock.patch.object(runner, "_ensure_node_image") as ensure:
                with mock.patch.object(lwr.subprocess, "run") as run:
                    run.return_value = mock.Mock(returncode=0, stdout="", stderr="")
                    runner._execute_in_projectbuilder(
                        {**self._node(), "script": "hmd deploy"}, destroy=True
                    )
            ensure.assert_not_called()

    def test_deploy_short_circuits_before_running_the_container(self):
        with tempfile.TemporaryDirectory() as d:
            runner = self._runner(self._repo(d, [["docker"]]))
            with mock.patch.object(
                runner, "_ensure_node_image", return_value=(False, "unavailable")
            ):
                with mock.patch.object(lwr.subprocess, "run") as run:
                    result = runner._execute_in_projectbuilder(
                        {**self._node(), "script": "hmd deploy"}
                    )
            self.assertFalse(result.success)
            self.assertEqual(result.stderr, "unavailable")
            run.assert_not_called()


class DeployedLambdaEndpointTests(unittest.TestCase):
    """A deploy must run against *its own* environment's emulated AWS account.

    One Floci serves every account, so the endpoint is the same for all of them
    and the **credentials** are what select the account: Floci reads the account
    id straight off a 12-digit SigV4 access key id.

    The failure this guards against is silent rather than loud. Signed with the
    wrong key, a deploy still succeeds -- it just creates the environment's CDKTF
    resources, and reads its secrets and SSM parameters, in another account.
    Downstream that surfaces far away as a service's DB credentials not existing
    ("Unable to read secret name: ...").
    """

    class _Env:
        def __init__(self, slug, legacy=False, account_id="000000000002"):
            self.slug = slug
            self.deployment_id = slug
            self.k3s_cluster = f"ns-{slug}"
            self.account_id = account_id
            self.legacy_layout = legacy
            self.kubeconfig = f"/nonexistent/{slug}/kubeconfig"

    def _endpoint_passed_to_docker(self, env, environ=None, _want="AWS_ENDPOINT_URL"):
        """Run a node and return one env var the container was given."""
        with tempfile.TemporaryDirectory() as d:
            meta = os.path.join(d, "meta-data")
            os.makedirs(meta)
            with open(os.path.join(meta, "manifest.json"), "w") as f:
                json.dump({"deploy": {"commands": [["cdktf"]]}}, f)
            with open(os.path.join(meta, "VERSION"), "w") as f:
                f.write("0.2.457\n")

            runner = LocalWorkflowRunner("http://x", env=env)
            runner._repo_path = lambda repo_class_name: d
            node = {
                "repo_class_name": "hmd-ms-transform",
                "instance_name": "transform",
                "repo_class_version": "0.2.457",
                "script": "hmd deploy --local",
            }
            with mock.patch.dict(os.environ, environ or {}, clear=True):
                with mock.patch.object(
                    runner, "_ensure_node_image", return_value=(True, "")
                ):
                    with mock.patch.object(lwr.subprocess, "run") as run:
                        run.return_value = mock.Mock(returncode=0, stdout="", stderr="")
                        runner._execute_in_projectbuilder(node)
            cmd = run.call_args[0][0]

        return self._env_var(cmd, _want)

    def _env_var(self, cmd, name):
        for arg in cmd:
            if arg.startswith(f"{name}="):
                return arg.split("=", 1)[1]
        self.fail(f"no {name} in {cmd}")

    def _akid_passed_to_docker(self, env):
        return self._endpoint_passed_to_docker(env, _want="AWS_ACCESS_KEY_ID")

    def test_every_deploy_reaches_the_one_floci(self):
        for env in (self._Env("dev2"), self._Env("local"), None):
            with self.subTest(env=getattr(env, "slug", None)):
                self.assertEqual(
                    self._endpoint_passed_to_docker(env), "http://neuronsphere:4566"
                )

    def test_environment_deploy_signs_as_its_own_account(self):
        akid = self._akid_passed_to_docker(self._Env("dev2", account_id="000000000002"))
        self.assertEqual(akid, "000000000002")

    def test_environment_deploy_never_signs_as_the_control_plane(self):
        # The whole point: signing as 000000000000 puts this environment's
        # resources in the control plane's account.
        akid = self._akid_passed_to_docker(
            self._Env("local", account_id="000000000001")
        )
        self.assertNotEqual(akid, "000000000000")

    def test_an_ambient_access_key_cannot_redirect_the_account(self):
        akid = self._endpoint_passed_to_docker(
            self._Env("dev2", account_id="000000000002"),
            environ={"AWS_ACCESS_KEY_ID": "000000000009"},
            _want="AWS_ACCESS_KEY_ID",
        )
        self.assertEqual(akid, "000000000002")

    def test_legacy_environment_signs_as_the_control_plane(self):
        # A legacy-layout environment *is* the control-plane account.
        akid = self._akid_passed_to_docker(self._Env("local", legacy=True))
        self.assertEqual(akid, "000000000000")

    def test_no_environment_uses_the_control_plane(self):
        self.assertEqual(
            self._endpoint_passed_to_docker(None, _want="AWS_ACCESS_KEY_ID"),
            "000000000000",
        )

    def test_explicit_override_wins(self):
        endpoint = self._endpoint_passed_to_docker(
            self._Env("dev2"),
            environ={"FLOCI_WORKLOAD_ENDPOINT_DOCKER": "http://elsewhere:4566"},
        )
        self.assertEqual(endpoint, "http://elsewhere:4566")


if __name__ == "__main__":
    unittest.main()
