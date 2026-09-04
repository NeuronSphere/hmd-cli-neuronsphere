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

import contextlib
import io
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import yaml

import hmd_cli_neuronsphere
from hmd_cli_neuronsphere import bom_seeder as b
from hmd_cli_neuronsphere import env_reconcile
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
        # `bom_seeder.env_db_identifier`/`graph_cluster_identifier` (real code,
        # not mocked) build the identifier through `make_standard_name`, which
        # joins this into the name with `"_".join(...)` -- it must be a real
        # string or that raises before `stop_rds_instance`/`stop_neptune_container`
        # are ever reached.
        fd.local_customer_code.return_value = "none"
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
        fd.stop_k3s_cluster.assert_called_once_with(
            "ns-dev2-abc", target=fd.env_target.return_value
        )
        fd.delete_k3s_cluster.assert_not_called()
        fd.purge_k3s_container_and_volume.assert_not_called()
        reg.clear_bootstrap.assert_not_called()

    def test_plain_stop_stops_containers_rather_than_removing_them(self):
        _, cli, _ = self._stop(purge=False)
        self.assertEqual(_compose_verbs(cli._exec), ["stop"])

    def test_plain_stop_stops_the_database_and_graph_containers(self):
        """The RDS/Neptune containers are Floci-spawned, not compose services or
        something k3s manages -- nothing else in `stop_environment` reaches them.
        """
        fd, _, _ = self._stop(purge=False)
        fd.stop_rds_instance.assert_called_once()
        self.assertEqual(
            fd.stop_rds_instance.call_args.kwargs.get("target")
            or fd.stop_rds_instance.call_args.args[1],
            fd.env_target.return_value,
        )
        fd.stop_neptune_container.assert_called_once()
        fd.delete_rds_instance.assert_not_called()
        fd.delete_neptune_cluster.assert_not_called()

    def test_purge_deletes_the_cluster_and_its_volume(self):
        fd, cli, reg = self._stop(purge=True)
        fd.delete_k3s_cluster.assert_called_once()
        self.assertEqual(fd.delete_k3s_cluster.call_args.args[0], "ns-dev2-abc")
        fd.stop_k3s_cluster.assert_not_called()
        fd.purge_k3s_container_and_volume.assert_called_once_with(
            "ns-dev2-abc", target=fd.env_target.return_value
        )
        reg.clear_bootstrap.assert_called_once()

    def test_purge_removes_containers(self):
        _, cli, _ = self._stop(purge=True)
        self.assertEqual(_compose_verbs(cli._exec), ["down"])

    def test_purge_deletes_rather_than_stops_the_database_and_graph_containers(self):
        fd, _, _ = self._stop(purge=True)
        fd.delete_rds_instance.assert_called_once()
        fd.delete_neptune_cluster.assert_called_once()
        fd.stop_rds_instance.assert_not_called()
        fd.stop_neptune_container.assert_not_called()


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
        ), mock.patch(
            "hmd_cli_neuronsphere.floci_deployer.control_plane_target",
            return_value="target",
        ), mock.patch(
            "hmd_cli_neuronsphere.bootstrap_dag.control_plane_db_identifier",
            return_value="control-plane-db",
        ), mock.patch(
            "hmd_cli_neuronsphere.floci_deployer.stop_rds_instance"
        ) as stop_rds_mock:
            cli.stop_neuronsphere_extend(purge=purge)
        commands = [call.args[0] for call in exec_mock.call_args_list if call.args]
        return commands, stop_rds_mock

    def test_plain_stop_keeps_the_network(self):
        commands, _ = self._stop(purge=False)
        self.assertNotIn(
            "network", [c[1] for c in commands if c[0] == "docker" and len(c) > 1]
        )
        self.assertEqual(commands[0][-1], "stop")

    def test_purge_removes_the_network(self):
        commands, _ = self._stop(purge=True)
        self.assertTrue(
            any(c[:3] == ["docker", "network", "rm"] for c in commands),
            f"expected a network rm, got {commands}",
        )
        self.assertEqual(commands[0][-1], "down")

    def test_plain_stop_stops_the_control_plane_database_container(self):
        """The control-plane database is a Floci-spawned RDS container, not a
        compose service, so the compose `stop` above never reaches it.
        """
        _, stop_rds_mock = self._stop(purge=False)
        stop_rds_mock.assert_called_once_with("control-plane-db", target="target")

    def test_purge_leaves_the_control_plane_database_stop_to_the_purge(self):
        """`_purge_control_plane_state` (mocked here) deletes it instead."""
        _, stop_rds_mock = self._stop(purge=True)
        stop_rds_mock.assert_not_called()


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
        ), mock.patch.object(envs, "_alias_environment_database"), mock.patch.object(
            envs, "env_registry"
        ) as reg, mock.patch.object(
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
            "hmd_cli_neuronsphere.bom_seeder.seed_bom",
            return_value=("csd-a", [{"repo_instance_name": "local-neuronsphere"}]),
        ), mock.patch(
            "hmd_cli_neuronsphere.env_manifest.load_manifest", return_value=None
        ), mock.patch(
            "hmd_cli_neuronsphere.change_set_builder.declared_repo_paths",
            return_value={},
        ), mock.patch(
            "hmd_cli_neuronsphere.floci_deployer.env_target"
        ), mock.patch(
            "hmd_cli_neuronsphere.floci_deployer.reconcile_k3s_container"
        ), mock.patch(
            "hmd_cli_neuronsphere.floci_deployer.wait_for_k3s_ready"
        ), mock.patch(
            "hmd_cli_neuronsphere.floci_deployer.write_kubeconfig",
            return_value="/tmp/kubeconfig",
        ), mock.patch(
            "hmd_cli_neuronsphere.k3s_operators.provision_k3s_operators"
        ), mock.patch(
            "hmd_cli_neuronsphere.k3s_operators.cluster_incarnation_id",
            return_value="new-uid",
        ), mock.patch(
            "hmd_cli_neuronsphere.local_workflow_runner.LocalWorkflowRunner"
        ) as runner_cls:
            runner_cls.return_value.last_succeeded = []
            env.is_default = False
            envs._bootstrap_environment(env, [], "ns-dev2", upgrade=False, prune=False)
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


class ReconcileGraphUnconditionalTests(unittest.TestCase):
    """A newly-required graph deploys on plain `up`, not just `up --upgrade`.

    Everything else a plugin/manifest change adds stays gated behind
    `--upgrade` -- that gate exists so an editing mistake or an uninstalled
    plugin doesn't silently change a running environment. But a graph that
    only now appears in the desired state is not a discretionary change: it
    is the CLI's own dependency of something already declared, so it must
    converge the same way Phase A's core instance and the k3s container
    reconcile already do -- lazy in whether it exists, unconditional in
    whether a converging `up` deploys it.
    """

    graph_entry = {
        "repo_instance_name": b.GRAPH_INSTANCE,
        "repo_class_name": b.GRAPH_REPO_CLASS,
        "repo_class_version": "0.1.0",
    }
    other_entry = {
        "repo_instance_name": "trino",
        "repo_class_name": "hmd-ms-trino",
        "repo_class_version": "0.2.0",
    }

    def _reconcile(self, *, upgrade, include_other=True, graph_run_result=True):
        env = mock.MagicMock()
        env.slug = "dev2"

        add = [self.graph_entry] + ([self.other_entry] if include_other else [])
        plan = env_reconcile.ReconcilePlan(add=list(add), desired=list(add))

        runner = mock.MagicMock()
        runner.last_succeeded = []

        def _run(csd_nid, nodes, **_kwargs):
            if csd_nid == "csd-graph":
                return graph_run_result
            runner.last_succeeded = [self.other_entry["repo_instance_name"]]
            return True

        runner.run.side_effect = _run

        with mock.patch.object(envs, "print_step"), mock.patch(
            "builtins.print"
        ), mock.patch.object(
            envs, "_alias_environment_graph"
        ) as alias_graph, mock.patch.object(
            envs, "_observed_releases", return_value={}
        ), mock.patch(
            "hmd_cli_neuronsphere.env_reconcile.compute_plan", return_value=plan
        ), mock.patch(
            "hmd_cli_neuronsphere.env_reconcile.merge_snapshot"
        ) as merge_snapshot, mock.patch(
            "hmd_cli_neuronsphere.env_reconcile.write_snapshot"
        ) as write_snapshot, mock.patch(
            "hmd_cli_neuronsphere.env_reconcile.load_release_map", return_value={}
        ), mock.patch(
            "hmd_cli_neuronsphere.bom_seeder.seed_bom",
            side_effect=[
                ("csd-graph", [self.graph_entry]),
                ("csd-rest", [self.other_entry]),
            ],
        ) as seed_bom, mock.patch(
            "hmd_cli_neuronsphere.change_set_builder.declared_repo_paths",
            return_value={},
        ):
            ok = envs._reconcile_environment(
                env, runner, "http://ms-deployment", None, upgrade=upgrade, prune=False
            )
        return SimpleNamespace(
            ok=ok,
            runner=runner,
            seed_bom=seed_bom,
            alias_graph=alias_graph,
            merge_snapshot=merge_snapshot,
            write_snapshot=write_snapshot,
        )

    def test_graph_deploys_without_upgrade(self):
        r = self._reconcile(upgrade=False)

        self.assertTrue(r.ok)
        # Only the graph's own changeset was seeded and run -- the other
        # addition stayed gated behind --upgrade, exactly as before.
        r.seed_bom.assert_called_once()
        self.assertEqual(r.seed_bom.call_args.kwargs["bom"], [self.graph_entry])
        r.runner.run.assert_called_once()
        r.alias_graph.assert_called_once()
        r.merge_snapshot.assert_called_once()
        self.assertEqual(r.merge_snapshot.call_args.args[2], [self.graph_entry])

    def test_failed_graph_deploy_is_not_recorded_as_settled(self):
        # No other addition here: with the graph the only entry, a failed
        # graph deploy leaves `pending` empty and exercises the
        # write-the-whole-snapshot branch, which must exclude it.
        r = self._reconcile(upgrade=False, include_other=False, graph_run_result=False)

        self.assertFalse(r.ok)
        r.alias_graph.assert_not_called()
        r.merge_snapshot.assert_not_called()
        r.write_snapshot.assert_called_once()
        snapshotted_names = {
            e["repo_instance_name"] for e in r.write_snapshot.call_args.args[1]
        }
        self.assertNotIn(b.GRAPH_INSTANCE, snapshotted_names)

    def test_graph_still_deploys_with_upgrade(self):
        # --upgrade must not skip the graph or seed it twice (once via the
        # unconditional path, once via the gated `pending` path).
        r = self._reconcile(upgrade=True)

        self.assertTrue(r.ok)
        seeded_boms = [c.kwargs["bom"] for c in r.seed_bom.call_args_list]
        self.assertEqual(seeded_boms, [[self.graph_entry], [self.other_entry]])
        r.alias_graph.assert_called_once()


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

    def test_the_mcp_server_can_be_switched_off_by_the_operator(self):
        """The knob the key bootstrap reads back out of /health/, so one variable
        turns off both the server and the minting step."""
        self.assertEqual(
            self._service()["environment"]["MCP_ENABLED"],
            "${HMD_LOCAL_GUI_MCP_ENABLED:-true}",
        )

    def test_api_key_auth_is_on_because_okta_is_not_emulated(self):
        self.assertEqual(self._service()["environment"]["MCP_API_KEYS_ENABLED"], "true")


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


class MsDeploymentVersionTests(unittest.TestCase):
    """The control plane's ms-deployment version, most specific source first."""

    def setUp(self):
        self._env = mock.patch.dict(os.environ, {}, clear=False)
        self._env.start()
        for var in ("HMD_MS_DEPLOYMENT_VERSION", "HMD_REPO_HOME"):
            os.environ.pop(var, None)

    def tearDown(self):
        self._env.stop()

    def test_no_repo_checkout_falls_back_to_the_pin(self):
        """Previously the floating `stable` tag, which is whatever was published
        last rather than a version this CLI was tested against."""
        from hmd_cli_neuronsphere import hmd_cli_neuronsphere as cli

        self.assertEqual(
            cli._read_repo_version(
                "", "hmd-ms-deployment", default=envs.MS_DEPLOYMENT_VERSION
            ),
            envs.MS_DEPLOYMENT_VERSION,
        )

    def test_the_pin_is_not_the_floating_tag(self):
        self.assertNotEqual(envs.MS_DEPLOYMENT_VERSION, "stable")

    def test_a_checked_out_repo_still_wins(self):
        """Local iteration is the point of HMD_REPO_HOME; the pin must not
        shadow a developer's working tree."""
        from hmd_cli_neuronsphere import hmd_cli_neuronsphere as cli

        with tempfile.TemporaryDirectory() as home:
            meta = Path(home) / "hmd-ms-deployment" / "meta-data"
            meta.mkdir(parents=True)
            (meta / "VERSION").write_text("9.9\n")
            self.assertEqual(
                cli._read_repo_version(
                    home, "hmd-ms-deployment", default=envs.MS_DEPLOYMENT_VERSION
                ),
                "9.9",
            )

    def test_other_callers_keep_the_stable_default(self):
        """ms-naming has no pin, so its fallback must not change."""
        from hmd_cli_neuronsphere import hmd_cli_neuronsphere as cli

        self.assertEqual(cli._read_repo_version("", "hmd-ms-naming"), "stable")


class DeploymentGuiImageTests(unittest.TestCase):
    """A locally built image wins over a published one, the same rule every
    Lambda image follows."""

    def _resolve(self, cached):
        with mock.patch(
            "hmd_cli_neuronsphere.image_cache.image_cached",
            side_effect=lambda ref: ref in cached,
        ):
            return envs.deployment_gui_image()

    VERSION = envs.GUI_IMAGE_VERSION

    def test_a_cached_local_build_wins(self):
        bare = f"hmd-app-neuronsphere:{self.VERSION}"
        self.assertEqual(self._resolve({bare}), bare)

    def test_nothing_cached_falls_through_to_the_published_ref(self):
        """ghcr.io/hmdlabs is the app's own registry -- its manifest's
        image.repository -- and is not one of image_candidates' prefixes."""
        self.assertEqual(
            self._resolve(set()),
            f"ghcr.io/hmdlabs/hmd-app-neuronsphere:{self.VERSION}",
        )

    def test_the_version_comes_from_the_pin_not_a_bundled_artifact(self):
        """The app is no longer a pre_build_artifacts entry, so there is no
        `external/` VERSION to read -- the resolver must not fall through to the
        0.1.0 sentinel, which names an image that was never published."""
        self.assertNotEqual(self.VERSION, "0.1.0")
        self.assertIn(self.VERSION, self._resolve(set()))

    def test_an_explicit_pin_overrides_the_shipped_version(self):
        with mock.patch.dict(
            os.environ, {"HMD_LOCAL_VERSION_HMD_APP_NEURONSPHERE": "0.1.99"}
        ):
            self.assertEqual(
                self._resolve(set()),
                "ghcr.io/hmdlabs/hmd-app-neuronsphere:0.1.99",
            )


class McpApiKeyTests(unittest.TestCase):
    """`up` mints the MCP bearer token once and prints it.

    The control plane has always run the GUI with ``MCP_API_KEYS_ENABLED`` --
    Okta is not emulated locally -- but nothing created a credential, so ``/mcp/``
    answered 401 to every caller and the only way in was a hand-run management
    command. ``MCPApiKey`` stores a SHA-256 and nothing else, so the plaintext
    exists for exactly one moment; these tests pin that it is printed then, that a
    second ``up`` is a no-op rather than a rotation, and that every failure mode
    is survivable, since no MCP key is worth failing `up` over.
    """

    KEY = "nsmcp_" + "x" * 43

    MINTED = (
        "Created MCP API key 'local dev' for testadmin.\n"
        "\n"
        f"{KEY}\n"
        "\n"
        "This is the only time the key is shown. Store it now.\n"
    )

    EXISTS = (
        "An active key named 'local dev' already exists for testadmin; "
        "leaving it alone.\n"
    )

    HEALTHY = {"status": "healthy", "mcp": {"enabled": True, "tools": 5}}

    @staticmethod
    def _completed(returncode=0, stdout="", stderr=""):
        return subprocess.CompletedProcess(
            args=["docker"], returncode=returncode, stdout=stdout, stderr=stderr
        )

    #: Distinguishes "caller said nothing" from "/health/ never answered".
    _DEFAULT = object()

    def _run(self, health=_DEFAULT, result=None, gui=True):
        """Drive `ensure_mcp_api_key` over stubbed docker/HTTP, capturing stdout."""
        health = self.HEALTHY if health is self._DEFAULT else health
        out = io.StringIO()
        with mock.patch.object(b, "gui_enabled", return_value=gui), mock.patch.object(
            envs, "_gui_health", return_value=health
        ) as health_mock, mock.patch.object(
            envs, "_docker_exec", return_value=result
        ) as exec_mock, contextlib.redirect_stdout(
            out
        ):
            envs.ensure_mcp_api_key()
        return out.getvalue(), exec_mock, health_mock

    def test_a_minted_key_is_printed_with_the_endpoint(self):
        printed, _, _ = self._run(result=self._completed(stdout=self.MINTED))
        self.assertIn(self.KEY, printed)
        # The trailing slash is the whole difference between a 401 you can fix
        # and Django's 404 from the Mount("/") fallback.
        self.assertIn("http://localhost:19003/mcp/", printed)

    def test_the_command_is_idempotent_and_names_the_superuser(self):
        _, exec_mock, _ = self._run(result=self._completed(stdout=self.MINTED))
        container, argv = exec_mock.call_args.args
        self.assertEqual(container, envs.GUI_CONTAINER)
        self.assertIn("create_mcp_api_key", argv)
        self.assertIn("--if-not-exists", argv)
        self.assertEqual(argv[argv.index("--user") + 1], "testadmin")
        self.assertEqual(argv[argv.index("--name") + 1], envs.MCP_KEY_NAME)

    def test_an_overridden_superuser_is_the_one_the_key_is_issued_to(self):
        """Otherwise `up` would mint against an account that does not exist."""
        with mock.patch.dict(os.environ, {"HMD_LOCAL_GUI_SUPERUSER": "alice"}):
            _, exec_mock, _ = self._run(result=self._completed(stdout=self.MINTED))
        argv = exec_mock.call_args.args[1]
        self.assertEqual(argv[argv.index("--user") + 1], "alice")

    def test_a_second_up_prints_nothing_and_rotates_nothing(self):
        printed, _, _ = self._run(result=self._completed(stdout=self.EXISTS))
        self.assertEqual(printed, "")

    def test_an_image_without_the_command_does_not_fail_up(self):
        printed, _, _ = self._run(
            result=self._completed(returncode=1, stderr="Unknown command")
        )
        self.assertEqual(printed, "")

    def test_docker_being_unavailable_does_not_fail_up(self):
        printed, _, _ = self._run(result=None)
        self.assertEqual(printed, "")

    def test_a_gui_that_never_becomes_ready_is_not_exec_into(self):
        printed, exec_mock, _ = self._run(health=None)
        self.assertEqual(printed, "")
        exec_mock.assert_not_called()

    def test_no_key_is_minted_when_the_mcp_server_is_switched_off(self):
        printed, exec_mock, _ = self._run(health={"mcp": {"enabled": False}})
        self.assertEqual(printed, "")
        exec_mock.assert_not_called()

    def test_a_disabled_gui_is_not_probed_at_all(self):
        printed, exec_mock, health_mock = self._run(gui=False)
        self.assertEqual(printed, "")
        exec_mock.assert_not_called()
        health_mock.assert_not_called()


class DeploymentGuiRouteTests(unittest.TestCase):
    def test_env_status_reports_the_mcp_endpoint(self):
        """`hmd neuronsphere env status` is where an operator looks for the URL;
        the key itself is unrecoverable, so only the endpoint can be surfaced."""
        env = mock.MagicMock()
        env.slug = "local"
        env.floci_port = 6500
        env.trino_port = 6502
        env.bootstrap = {}
        with mock.patch.object(b, "gui_enabled", return_value=True), mock.patch(
            "subprocess.run",
            return_value=subprocess.CompletedProcess(
                args=["docker"], returncode=1, stdout="", stderr=""
            ),
        ):
            routes = envs.environment_status(env)["routes"]
        self.assertEqual(routes["deployment_gui_mcp"], "http://localhost:19003/mcp/")


class ControlPlaneGraphAliasTests(unittest.TestCase):
    """The `control-plane-graph-alias` node must survive a `down`/`up`.

    `hmd-inf-neptune`'s deploy_local.sh is idempotent -- an existing cluster is
    "nothing to do" -- so on every `up` after the first, the node ahead of this
    one spawns no container. Floci left the old one *stopped* on `down` and
    never restarts it, while the cluster keeps reporting `available`. Waiting on
    that status alone burned 300s and then failed the node, taking ms-naming,
    artifact-lib and ms-deployment down with it.
    """

    def _run(self, running, cluster=True):
        target = mock.MagicMock(account_id="000000000001")
        with mock.patch.object(
            floci_deployer, "ensure_neptune_running", return_value=running
        ) as ensure, mock.patch.object(
            floci_deployer, "neptune_cluster_exists", return_value=cluster
        ), mock.patch.object(
            floci_deployer, "ensure_neptune_network_alias", return_value=True
        ) as alias:
            ok = envs._ensure_control_plane_graph(target)
        return ok, ensure, alias

    def test_a_restarted_container_is_aliased_as_global_graph(self):
        ok, ensure, alias = self._run("floci-neptune-abc")
        self.assertTrue(ok)
        ensure.assert_called_once()
        # The alias, not Floci's Gremlin proxy, is what consumers address.
        self.assertEqual(alias.call_args[0][1], "global-graph")

    def test_an_unstartable_container_fails_the_node(self):
        """Unlike an environment's lazy graph, this one is required:
        artifact-lib's `neptune-db` dependency has no postgres fallback."""
        ok, _, alias = self._run(None, cluster=True)
        self.assertFalse(ok)
        alias.assert_not_called()

    def test_a_missing_cluster_fails_the_node(self):
        ok, _, alias = self._run(None, cluster=False)
        self.assertFalse(ok)
        alias.assert_not_called()
