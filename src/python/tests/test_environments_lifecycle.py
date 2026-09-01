"""``down`` stops; ``down --purge`` destroys.

A non-purge ``down`` used to delete the environment's k3s cluster outright.
Floci's ``delete_cluster`` drops the cluster's ``floci-eks-<name>`` volume with
it, so the next ``up`` got a brand-new cluster with a new ``kube-system`` UID --
which ``_bootstrap_environment`` correctly reads as "the cluster was recreated
since the last bootstrap" and answers by redeploying the entire BOM. The
documented restart fast-path was therefore never actually taken.

These tests pin the split that fixes it:

* plain ``down`` stops the k3s container, stops (not removes) the containers,
  and leaves the Docker network alone;
* ``down --purge`` deletes the cluster, removes the containers and the network,
  drops the persisted state and clears the bootstrap marker.

The network is the subtle one: a stopped container's endpoint pins its network
by *id*, so removing and recreating the network would leave the k3s container
unable to start and force exactly the recreate this change avoids.

Floci reaps the k3s container (and its volume) whenever the environment's own
``floci-<env>`` container stops, so a replaced cluster is not always avoidable.
The second group of tests pins what happens then: only the instances that
actually deployed onto k3s are redeployed, not the whole BOM -- unless nothing
is recorded about which those are, in which case the full bootstrap is still the
only honest answer.

Run directly: ``python -m pytest src/python/tests/test_environments_lifecycle.py``
"""

import os
import unittest
from pathlib import Path
from unittest import mock

import yaml

import hmd_cli_neuronsphere
from hmd_cli_neuronsphere import bom_seeder as b
from hmd_cli_neuronsphere import environments as envs
from hmd_cli_neuronsphere import floci_deployer


class _Env:
    slug = "dev2"
    name = "dev2"
    legacy_layout = False
    k3s_cluster = "ns-dev2-abc"
    compose_project = "ns-abc-env-dev2"
    deployment_id = "dev2"

    def compose_env(self):
        return {}

    @property
    def state_path(self):  # pragma: no cover - only touched under --purge
        return "/nonexistent/dev2"


def _compose_verbs(exec_mock):
    """The trailing verb of every command `_exec` was handed."""
    return [call.args[0][-1] for call in exec_mock.call_args_list if call.args]


class StopEnvironmentTests(unittest.TestCase):
    def _stop(self, purge):
        fd = mock.MagicMock()
        cli = mock.MagicMock()
        cli._get_base_command.return_value = ["docker", "compose"]
        with mock.patch.dict(
            "sys.modules",
            {
                "hmd_cli_neuronsphere.floci_deployer": fd,
                "hmd_cli_neuronsphere.hmd_cli_neuronsphere": cli,
            },
        ), mock.patch.object(envs, "nginx_router"), mock.patch.object(
            envs, "env_registry"
        ) as reg, mock.patch.object(
            envs.shutil, "rmtree"
        ):
            envs.stop_environment(_Env(), purge=purge)
        return fd, cli, reg

    def test_plain_stop_preserves_the_cluster(self):
        fd, cli, reg = self._stop(purge=False)
        fd.stop_k3s_cluster.assert_called_once_with("ns-dev2-abc")
        fd.delete_k3s_cluster.assert_not_called()
        fd.purge_k3s_container_and_volume.assert_not_called()
        reg.clear_bootstrap.assert_not_called()

    def test_plain_stop_stops_containers_rather_than_removing_them(self):
        _, cli, _ = self._stop(purge=False)
        self.assertEqual(_compose_verbs(cli._exec), ["stop"])

    def test_purge_deletes_the_cluster_and_its_volume(self):
        fd, cli, reg = self._stop(purge=True)
        fd.delete_k3s_cluster.assert_called_once()
        self.assertEqual(fd.delete_k3s_cluster.call_args.args[0], "ns-dev2-abc")
        fd.stop_k3s_cluster.assert_not_called()
        fd.purge_k3s_container_and_volume.assert_called_once_with("ns-dev2-abc")
        reg.clear_bootstrap.assert_called_once()

    def test_purge_removes_containers(self):
        _, cli, _ = self._stop(purge=True)
        self.assertEqual(_compose_verbs(cli._exec), ["down"])


class StopNeuronsphereExtendTests(unittest.TestCase):
    def _stop(self, purge):
        from hmd_cli_neuronsphere import hmd_cli_neuronsphere as cli

        reg = mock.MagicMock()
        with mock.patch.object(cli, "load_hmd_env"), mock.patch.object(
            cli, "print_header"
        ), mock.patch.object(cli, "print_step"), mock.patch.object(
            cli, "print_shutdown_summary"
        ), mock.patch.object(
            cli, "LocalPluginLoader"
        ), mock.patch.object(
            cli, "_get_base_command", return_value=["docker", "compose"]
        ), mock.patch.object(
            cli, "_exec"
        ) as exec_mock, mock.patch.object(
            cli, "_purge_control_plane_state"
        ), mock.patch(
            "hmd_cli_neuronsphere.environments.control_plane_compose_files",
            return_value=[],
        ), mock.patch(
            "hmd_cli_neuronsphere.environments.stop_environment"
        ), mock.patch(
            "hmd_cli_neuronsphere.env_registry.load", return_value=reg
        ), mock.patch(
            "hmd_cli_neuronsphere.env_registry.list_envs", return_value=[]
        ), mock.patch(
            "hmd_cli_neuronsphere.env_registry.environments_root",
            return_value=mock.MagicMock(),
        ), mock.patch(
            "hmd_cli_neuronsphere.env_registry.registry_path",
            return_value=mock.MagicMock(),
        ), mock.patch.object(
            cli.shutil, "rmtree"
        ):
            cli.stop_neuronsphere_extend(purge=purge)
        return [call.args[0] for call in exec_mock.call_args_list if call.args]

    def test_plain_stop_keeps_the_network(self):
        commands = self._stop(purge=False)
        self.assertNotIn(
            "network", [c[1] for c in commands if c[0] == "docker" and len(c) > 1]
        )
        self.assertEqual(commands[0][-1], "stop")

    def test_purge_removes_the_network(self):
        commands = self._stop(purge=True)
        self.assertTrue(
            any(c[:3] == ["docker", "network", "rm"] for c in commands),
            f"expected a network rm, got {commands}",
        )
        self.assertEqual(commands[0][-1], "down")


if __name__ == "__main__":
    unittest.main()


class ClusterRecreatedTests(unittest.TestCase):
    """A replaced k3s cluster redeploys what was on it, not everything.

    Only the instances that installed a Helm release died with the cluster.
    S3 buckets, cdktf-to-Floci stacks and Lambdas live in Floci, whose state
    persists across the restart, so redeploying them is pure waste.
    """

    def _bootstrap(self, releases):
        env = mock.MagicMock()
        env.slug = "dev2"
        env.bootstrap = {"csd_nid": "csd-1", "k3s_uid": "old-uid"}

        with mock.patch.object(envs, "print_step"), mock.patch.object(
            envs, "_service_specs", return_value=[]
        ), mock.patch.object(envs, "env_registry") as reg, mock.patch.object(
            envs, "_reconcile_environment", return_value=True
        ) as reconcile, mock.patch.object(
            envs, "_run_full_bootstrap", return_value=True
        ) as full, mock.patch(
            "hmd_cli_neuronsphere.env_reconcile.load_release_map",
            return_value=releases,
        ), mock.patch(
            "hmd_cli_neuronsphere.bom_seeder.ensure_environment"
        ), mock.patch(
            "hmd_cli_neuronsphere.bom_seeder.resync_local_resources", return_value=0
        ), mock.patch(
            "hmd_cli_neuronsphere.bom_seeder.seed_base_resource_definitions",
            return_value=[],
        ), mock.patch(
            "hmd_cli_neuronsphere.env_manifest.load_manifest", return_value=None
        ), mock.patch(
            "hmd_cli_neuronsphere.change_set_builder.declared_repo_paths",
            return_value={},
        ), mock.patch(
            "hmd_cli_neuronsphere.local_workflow_runner.LocalWorkflowRunner"
        ):
            envs._bootstrap_environment(
                env, [], "ns-dev2", "new-uid", upgrade=False, prune=False
            )
        return reconcile, full, reg

    def test_known_releases_reconcile_instead_of_full_bootstrap(self):
        reconcile, full, reg = self._bootstrap({"redis": "redis-dev2"})
        full.assert_not_called()
        reconcile.assert_called_once()
        # Forced: recovering a cluster is not a discretionary upgrade.
        self.assertIs(reconcile.call_args.kwargs["upgrade"], True)

    def test_the_new_cluster_uid_is_recorded(self):
        # Otherwise every later `up` re-detects the same "cluster was recreated".
        _, _, reg = self._bootstrap({"redis": "redis-dev2"})
        self.assertEqual(reg.record_bootstrap.call_args.kwargs["k3s_uid"], "new-uid")

    def test_no_recorded_releases_falls_back_to_the_full_bootstrap(self):
        # With no record of which instances live on k3s, narrowing would be a
        # guess -- and a wrong guess leaves workloads silently missing.
        reconcile, full, _ = self._bootstrap({})
        full.assert_called_once()
        reconcile.assert_not_called()


class DeploymentGuiComposeTests(unittest.TestCase):
    """The Deployment GUI is a control-plane container, not a deployed workload.

    It used to reach k3s through the deployment DAG -- CDKTF, Helm, Traefik, an
    Ingress-host rewrite and an ms-dbaccount round trip -- for a UI whose only job
    is to talk to the control plane it now sits beside.
    """

    @staticmethod
    def _service():
        path = (
            Path(hmd_cli_neuronsphere.__file__).parent
            / "services"
            / "docker-compose.control-plane.yml"
        )
        return yaml.safe_load(path.read_text())["services"]["deployment-gui"]

    def test_the_service_publishes_no_host_port(self):
        """hmd_proxy is the only container that publishes ports; that invariant is
        what lets several environments coexist on one machine."""
        self.assertNotIn("ports", self._service())

    def test_the_service_is_behind_the_gui_profile(self):
        """Which is how HMD_LOCAL_NEURONSPHERE_ENABLE_GUI=false still turns it off."""
        self.assertEqual(self._service()["profiles"], [envs.GUI_COMPOSE_PROFILE])

    def test_the_service_points_at_the_control_plane_database(self):
        env = self._service()["environment"]
        self.assertEqual(env["DB_HOST"], "hmd_db")
        self.assertEqual(env["DB_NAME"], b.GUI_DB_NAME)
        # Local convention: password == username (ensure_core_databases_direct).
        self.assertEqual(env["DB_USER"], env["DB_PASSWORD"])

    def test_the_service_calls_ms_deployment_through_the_proxy(self):
        self.assertEqual(
            self._service()["environment"]["DEPLOYMENT_API_URL"],
            b.GUI_DEPLOYMENT_API_URL,
        )

    def test_migrate_retries_until_the_database_exists(self):
        """ensure_core_databases_direct creates it *after* `compose up` returns, so
        a single migrate attempt would leave the container dead on a cold boot."""
        command = "\n".join(self._service()["command"])
        self.assertIn("until python manage.py migrate --noinput", command)

    def test_the_database_is_a_core_control_plane_database(self):
        names = [db["db_name"] for db in floci_deployer.CORE_DATABASES]
        self.assertIn(b.GUI_DB_NAME, names)


class ControlPlaneComposeEnvTests(unittest.TestCase):
    def setUp(self):
        self._env = mock.patch.dict(os.environ, {}, clear=False)
        self._env.start()
        for var in (
            "COMPOSE_PROFILES",
            "HMD_DEPLOYMENT_GUI_IMAGE",
            "HMD_LOCAL_GUI_HOST_PORT",
            "HMD_LOCAL_NEURONSPHERE_ENABLE_GUI",
        ):
            os.environ.pop(var, None)

    def tearDown(self):
        self._env.stop()

    def test_the_profile_and_image_are_exported(self):
        with mock.patch.object(
            envs, "deployment_gui_image", return_value="some/ref:0.1"
        ):
            envs.export_control_plane_compose_env()
        self.assertEqual(os.environ["COMPOSE_PROFILES"], envs.GUI_COMPOSE_PROFILE)
        self.assertEqual(os.environ["HMD_DEPLOYMENT_GUI_IMAGE"], "some/ref:0.1")
        self.assertEqual(os.environ["HMD_LOCAL_GUI_HOST_PORT"], "19003")

    def test_opting_out_leaves_the_profile_unset(self):
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_GUI"] = "false"
        envs.export_control_plane_compose_env()
        self.assertNotIn("COMPOSE_PROFILES", os.environ)
        self.assertNotIn("HMD_DEPLOYMENT_GUI_IMAGE", os.environ)

    def test_an_unresolvable_image_does_not_fail_the_export(self):
        """The compose file's own default still applies; a missing image is not a
        reason to abort `up`."""
        with mock.patch.object(
            envs, "deployment_gui_image", side_effect=RuntimeError("no registry")
        ):
            envs.export_control_plane_compose_env()
        self.assertEqual(os.environ["COMPOSE_PROFILES"], envs.GUI_COMPOSE_PROFILE)
        self.assertNotIn("HMD_DEPLOYMENT_GUI_IMAGE", os.environ)


class DeploymentGuiImageTests(unittest.TestCase):
    """A locally built image wins over a published one, the same rule every
    Lambda image follows."""

    def _resolve(self, cached, source="bundled"):
        with mock.patch(
            "hmd_cli_neuronsphere.bom_seeder.resolve_repo_version"
        ) as version, mock.patch(
            "hmd_cli_neuronsphere.image_cache.image_candidates",
            return_value=["hmd-app-neuronsphere:0.1.73"],
        ), mock.patch(
            "hmd_cli_neuronsphere.image_cache.image_cached",
            side_effect=lambda ref: ref in cached,
        ):
            version.return_value = mock.Mock(version="0.1.73", source=source)
            return envs.deployment_gui_image()

    def test_a_cached_local_build_wins(self):
        self.assertEqual(
            self._resolve({"hmd-app-neuronsphere:0.1.73"}),
            "hmd-app-neuronsphere:0.1.73",
        )

    def test_nothing_cached_falls_through_to_the_published_ref(self):
        """ghcr.io/hmdlabs is the app's own registry -- its manifest's
        image.repository -- and is not one of image_candidates' prefixes."""
        self.assertEqual(
            self._resolve(set()),
            "ghcr.io/hmdlabs/hmd-app-neuronsphere:0.1.73",
        )

    def test_a_guessed_version_yields_no_ref(self):
        """The `default` tier is the 0.1.0 sentinel; a ref built from it names an
        image that was never published, so the compose file's own `:stable`
        default is the better answer."""
        self.assertIsNone(self._resolve(set(), source="default"))
