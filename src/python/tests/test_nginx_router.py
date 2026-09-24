"""nginx fragment assembly (``nginx_router``).

The control plane and every environment each own exactly one HTTP fragment and
one stream fragment, so adding or removing an environment cannot disturb any
other owner. These tests cover that isolation, the idempotency of single-route
edits, and the invariant that databases are never streamed to the host.

Run directly: ``python -m pytest src/python/tests/test_nginx_router.py``
"""

import json
import os
import socket
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from hmd_cli_neuronsphere import nginx_router as nr


def _no_k3s():
    """No k3s container, so no k3s listener -- keeps stream tests hermetic."""
    return mock.patch.object(nr, "k3s_api_upstream", return_value=None)


class _Env:
    def __init__(self, slug, port_base=19000, slot=0):
        self.slug = slug
        # One Floci serves every account; environments differ by account id, not
        # by hostname (env_registry.LocalEnvironment.floci_container/_alias).
        self.floci_container = "floci"
        self.floci_alias = "neuronsphere"
        self.db_container = f"hmd_db-{slug}"
        self.graph_container = f"global-graph-{slug}"
        self.k3s_cluster = f"ns-{slug}-abc"
        self.kubeconfig = f"/tmp/{slug}/kubeconfig"
        self.port_base = port_base
        self.port_slot = slot
        self.legacy_layout = False

    @property
    def is_default(self):
        return self.slug == "local"

    @property
    def floci_port(self):
        return self.port_base + self.port_slot * 4

    @property
    def trino_port(self):
        return self.floci_port + 1

    @property
    def spare_port(self):
        return self.floci_port + 3

    @property
    def k3s_port(self):
        # A band above the slot ports, matching
        # env_registry.LocalEnvironment.k3s_port.
        return self.port_base + 16 * 4 + self.port_slot

    @property
    def is_default(self):
        return self.slug == "local"


class _TempHome(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self._env = mock.patch.dict(
            os.environ, {"HMD_HOME": self._tmp.name}, clear=False
        )
        self._env.start()
        # Every one of these changes what gets rendered, so a developer (or CI
        # box) that exports one would otherwise silently flip an assertion --
        # `HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO=false` in the shell turns
        # `test_argo_route_on_by_default` into a failure about a default that is
        # in fact still correct. patch.dict restores them on stop.
        for var in (
            "HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO",
            "HMD_LOCAL_ARGO_UPSTREAM",
            "HMD_LOCAL_TRINO_HOST_PORT",
            "HMD_LOCAL_ENV_PORT_BASE",
            "HMD_LOCAL_ENV_PORT_RANGE",
            "HMD_LOCAL_NEURONSPHERE_ENABLE_GUI",
            "HMD_LOCAL_GUI_HOST_PORT",
        ):
            os.environ.pop(var, None)

    def tearDown(self):
        self._env.stop()
        self._tmp.cleanup()

    def http_dir(self):
        return Path(self._tmp.name) / ".cache" / "nginx" / "http.d"

    def stream_dir(self):
        return Path(self._tmp.name) / ".cache" / "nginx" / "stream.d"

    def vhost_dir(self):
        return Path(self._tmp.name) / ".cache" / "nginx" / "vhost.d"


class BaseConfigTests(_TempHome):
    def test_includes_both_fragment_dirs(self):
        text = nr.render_base_config().read_text()
        self.assertIn("include /etc/nginx/ns/http.d/*.conf;", text)
        self.assertIn("include /etc/nginx/ns/stream.d/*.conf;", text)

    def test_creates_fragment_dirs_so_the_glob_is_valid(self):
        nr.render_base_config()
        self.assertTrue(self.http_dir().is_dir())
        self.assertTrue(self.stream_dir().is_dir())

    def test_has_a_catch_all_404(self):
        self.assertIn("no route defined", nr.render_base_config().read_text())

    def test_bootstrap_config_is_self_contained(self):
        # Written before the container starts, when the fragment dir may not be
        # mounted yet -- an include would fail nginx's config test.
        text = nr.write_bootstrap_config().read_text()
        self.assertNotIn("include", text)
        self.assertIn("503", text)

    def test_bootstrap_config_streams_floci(self):
        """The placeholder must already serve :4566.

        Floci publishes no host port; `localhost:4566` is this stream and
        nothing else. `up` polls that address to decide whether Floci came up,
        so a placeholder without it makes a fresh bootstrap wait out the full
        timeout and report "Ready (degraded)" however healthy Floci is.
        """
        text = nr.write_bootstrap_config().read_text()
        self.assertIn("listen 4566;", text)
        self.assertIn("neuronsphere:4566", text)

    def test_bootstrap_config_names_the_control_plane_floci_alias(self):
        text = nr.write_bootstrap_config(floci_host="neuronsphere-legacy").read_text()
        self.assertIn("neuronsphere-legacy:4566", text)

    def test_bootstrap_config_does_not_clobber_a_working_config(self):
        nr.render_base_config()
        nr.write_control_plane_streams()
        nr.write_bootstrap_config()
        self.assertIn("include", nr.base_config_path().read_text())

    def test_bootstrap_config_replaces_one_that_cannot_serve_floci(self):
        """A pre-multi-environment single-file config has no :4566 listener.

        Leaving it in place is what makes `up` hang for 300s polling a port
        nothing is listening on, so it is overwritten despite existing.
        """
        nr.base_config_path().parent.mkdir(parents=True, exist_ok=True)
        nr.base_config_path().write_text(
            "events {}\nhttp { server { listen 80; location / { return 404; } } }\n"
        )
        nr.write_bootstrap_config()
        self.assertIn("listen 4566;", nr.base_config_path().read_text())

    def test_an_include_config_without_its_fragment_is_not_serving_floci(self):
        # The shell looks complete, but the fragment carrying the listener is
        # gone, so nothing is on 4566.
        nr.render_base_config()
        self.assertFalse(nr.config_serves_floci(nr.base_config_path()))
        nr.write_control_plane_streams()
        self.assertTrue(nr.config_serves_floci(nr.base_config_path()))


class StreamResolutionTests(_TempHome):
    """Stream upstreams resolve per connection, not at config load.

    nginx resolves a literal ``proxy_pass host:port`` once, when the config
    loads, and refuses to start if the name does not resolve. Every stream
    upstream here is a container that can legitimately be down at that moment,
    so a literal would let one stopped environment stop the whole proxy from
    starting -- taking every other environment's routes with it.
    """

    def test_base_config_declares_a_resolver(self):
        text = nr.render_base_config().read_text()
        self.assertIn("resolver 127.0.0.11", text)

    def test_control_plane_stream_uses_a_variable(self):
        text = nr.write_control_plane_streams().read_text()
        self.assertIn('set $ns_floci "neuronsphere:4566";', text)
        self.assertIn("proxy_pass $ns_floci;", text)

    def test_env_streams_use_distinct_variables(self):
        env = _Env("dev2", slot=1)
        text = nr.write_env_streams(
            env, [(19004, "floci-dev2:4566"), (19005, "10.0.0.5:31880")]
        ).read_text()
        self.assertIn("proxy_pass $ns_dev2_19004;", text)
        self.assertIn("proxy_pass $ns_dev2_19005;", text)

    def test_a_hyphenated_slug_yields_a_valid_variable_name(self):
        # nginx variable names allow only [0-9a-zA-Z_].
        text = nr.write_env_streams(
            _Env("my-env"), [(19008, "floci-my-env:4566")]
        ).read_text()
        self.assertIn("proxy_pass $ns_my_env_19008;", text)
        self.assertNotIn("$ns_my-env", text)

    def test_resolver_is_overridable(self):
        with mock.patch.dict(
            os.environ, {"HMD_LOCAL_NGINX_RESOLVER": "10.1.2.3"}, clear=False
        ):
            self.assertIn("resolver 10.1.2.3", nr.render_base_config().read_text())


class ControlPlaneRouteTests(_TempHome):
    def test_routes_are_unprefixed(self):
        nr.write_control_plane_routes({"hmd_ms_deployment": "gw1"})
        text = (self.http_dir() / "00-control-plane.conf").read_text()
        self.assertIn("location /hmd_ms_deployment/ {", text)
        self.assertIn("http://neuronsphere:4566/restapis/gw1/local/", text)

    def test_service_aliases_are_emitted(self):
        nr.write_control_plane_routes({"hmd_ms_deployment": "gw1"})
        text = (self.http_dir() / "00-control-plane.conf").read_text()
        for alias in ("ms-deployment", "ms_deployment", "hmd-ms-deployment"):
            self.assertIn(f"location /{alias}/ {{", text)

    def test_streams_expose_floci_but_never_a_database(self):
        nr.write_control_plane_streams()
        text = (self.stream_dir() / "00-control-plane.conf").read_text()
        self.assertIn("listen 4566;", text)
        # The upstream is carried by a variable so it resolves per connection
        # (see StreamResolutionTests); what matters here is that it is Floci.
        self.assertIn("neuronsphere:4566", text)
        # Databases must not be reachable from the host.
        self.assertNotIn("5432", text)
        self.assertNotIn("8182", text)


class EnvRouteTests(_TempHome):
    def test_routes_are_prefixed_and_target_the_env_floci(self):
        env = _Env("dev2", slot=1)
        nr.write_env_routes(env, {"hmd_ms_transform": "gw9"})
        text = (self.http_dir() / "10-env-dev2.conf").read_text()
        self.assertIn("location /dev2/hmd_ms_transform/ {", text)
        self.assertIn("http://neuronsphere:4566/restapis/gw9/local/", text)

    def test_env_streams_default_to_no_listeners(self):
        """There is no per-environment Floci listener to publish.

        One Floci serves every account and is already streamed on the control
        plane's :4566; a host-side caller picks the account with its credentials,
        not with a port.
        """
        env = _Env("dev2", slot=1)
        nr.write_env_streams(env)
        text = (self.stream_dir() / "10-env-dev2.conf").read_text()
        self.assertNotIn("listen ", text)
        with _no_k3s():
            self.assertEqual(nr.env_stream_entries(env), [])

    def test_env_streams_still_publish_a_trino_listener(self):
        env = _Env("dev2", slot=1)
        with _no_k3s():
            entries = nr.env_stream_entries(env, trino_upstream="10.0.0.5:31880")
        self.assertIn((env.trino_port, "10.0.0.5:31880"), entries)

    def test_k3s_api_gets_a_stream_listener_on_its_own_port(self):
        """kubectl reaches the cluster through hmd_proxy, not Floci's forward.

        Floci's published port on the `floci-eks-*` container is re-created by
        Docker every time `down`/`up` restarts it, and a re-created forward
        truncates writes past ~1 MTU -- which silently eats the 1449-byte
        post-quantum TLS 1.3 ClientHello kubectl sends and hangs the handshake
        against a perfectly healthy cluster.
        """
        env = _Env("dev2", slot=1)
        with mock.patch.object(nr, "k3s_api_upstream", return_value="10.0.0.9:6443"):
            self.assertTrue(nr.configure_k3s_host_route(env))
        text = (self.stream_dir() / "10-env-dev2.conf").read_text()
        self.assertIn(f"listen {env.k3s_port};", text)
        self.assertIn("10.0.0.9:6443", text)

    def test_no_k3s_route_when_the_cluster_is_absent(self):
        env = _Env("dev2", slot=1)
        with _no_k3s():
            self.assertFalse(nr.configure_k3s_host_route(env))
        self.assertFalse((self.stream_dir() / "10-env-dev2.conf").exists())

    def test_the_k3s_route_survives_a_later_trino_rewrite(self):
        """Every writer rewrites the whole fragment, so k3s must be re-resolved.

        Wiring k3s and then deploying Trino would otherwise drop the k3s
        listener and take kubectl offline again.
        """
        env = _Env("dev2", slot=1)
        with mock.patch.object(nr, "k3s_api_upstream", return_value="10.0.0.9:6443"):
            nr.configure_k3s_host_route(env)
            nr.write_env_streams(
                env, nr.env_stream_entries(env, trino_upstream="10.0.0.5:31880")
            )
        text = (self.stream_dir() / "10-env-dev2.conf").read_text()
        self.assertIn(f"listen {env.k3s_port};", text)
        self.assertIn(f"listen {env.trino_port};", text)

    def test_an_explicit_k3s_upstream_is_not_re_resolved(self):
        env = _Env("dev2", slot=1)
        with mock.patch.object(nr, "k3s_api_upstream") as lookup:
            entries = nr.env_stream_entries(env, k3s_upstream="10.0.0.9:6443")
        lookup.assert_not_called()
        self.assertIn((env.k3s_port, "10.0.0.9:6443"), entries)

    def test_k3s_and_trino_never_share_a_listener_port(self):
        env = _Env("dev2", slot=1)
        with mock.patch.object(nr, "k3s_api_upstream", return_value="10.0.0.9:6443"):
            ports = [
                p
                for p, _ in nr.env_stream_entries(env, trino_upstream="10.0.0.5:31880")
            ]
        self.assertEqual(len(ports), len(set(ports)))

    def test_environments_do_not_share_fragments(self):
        a, bb = _Env("alpha", slot=0), _Env("beta", slot=1)
        nr.write_env_routes(a, {"svc": "gwa"})
        nr.write_env_routes(bb, {"svc": "gwb"})
        self.assertIn("gwa", (self.http_dir() / "10-env-alpha.conf").read_text())
        self.assertIn("gwb", (self.http_dir() / "10-env-beta.conf").read_text())
        self.assertNotIn("gwb", (self.http_dir() / "10-env-alpha.conf").read_text())

    def test_removing_one_env_leaves_the_others_and_the_control_plane(self):
        a, bb = _Env("alpha", slot=0), _Env("beta", slot=1)
        nr.write_control_plane_routes({"hmd_ms_deployment": "gw1"})
        nr.write_env_routes(a, {"svc": "gwa"})
        nr.write_env_routes(bb, {"svc": "gwb"})
        nr.write_env_streams(a)
        nr.write_env_streams(bb)

        nr.remove_env_routes(a)

        self.assertFalse((self.http_dir() / "10-env-alpha.conf").exists())
        self.assertFalse((self.stream_dir() / "10-env-alpha.conf").exists())
        self.assertTrue((self.http_dir() / "10-env-beta.conf").exists())
        self.assertTrue((self.http_dir() / "00-control-plane.conf").exists())

    def test_remove_is_idempotent(self):
        nr.render_base_config()
        nr.remove_env_routes(_Env("never-created"))  # must not raise

    def test_trino_stream_adds_legacy_port_only_for_the_default_env(self):
        with _no_k3s():
            default_entries = nr.env_stream_entries(
                _Env("local"), trino_upstream="1.2.3.4:31880"
            )
            other_entries = nr.env_stream_entries(
                _Env("dev2", slot=1), trino_upstream="1.2.3.4:31880"
            )
        self.assertIn(nr.LEGACY_TRINO_HOST_PORT, [p for p, _ in default_entries])
        self.assertNotIn(nr.LEGACY_TRINO_HOST_PORT, [p for p, _ in other_entries])


class AddServiceRouteTests(_TempHome):
    def setUp(self):
        super().setUp()
        self._reload = mock.patch.object(nr, "reload", return_value=True)
        self._reload.start()

    def tearDown(self):
        self._reload.stop()
        super().tearDown()

    def test_adds_a_route_into_the_env_fragment(self):
        env = _Env("dev2", slot=1)
        nr.write_env_routes(env, {})
        nr.add_service_route("device", "rest1", "local", env=env)
        text = (self.http_dir() / "10-env-dev2.conf").read_text()
        self.assertIn("location /dev2/device/ {", text)
        self.assertIn("restapis/rest1/local/", text)

    def test_rerunning_replaces_rather_than_duplicates(self):
        env = _Env("dev2", slot=1)
        nr.write_env_routes(env, {})
        nr.add_service_route("device", "rest1", "local", env=env)
        nr.add_service_route("device", "rest2", "local", env=env)
        text = (self.http_dir() / "10-env-dev2.conf").read_text()
        self.assertEqual(text.count("location /dev2/device/ {"), 1)
        self.assertIn("rest2", text)
        self.assertNotIn("rest1", text)

    def test_control_plane_route_when_no_env_given(self):
        nr.write_control_plane_routes({})
        nr.add_service_route("device", "rest1", "local")
        text = (self.http_dir() / "00-control-plane.conf").read_text()
        self.assertIn("location /device/ {", text)


class SingleStackConfigTests(_TempHome):
    """Platform (legacy) mode mounts only one file, so it must not use include."""

    def test_is_self_contained(self):
        path = nr.write_nginx_config({"hmd_ms_naming": "gw1"})
        text = path.read_text()
        self.assertNotIn("include", text)
        self.assertIn("location /hmd_ms_naming/ {", text)
        self.assertIn("no route defined", text)

    def test_api_id_overrides_every_service(self):
        text = nr.write_nginx_config({"a": "x", "b": "y"}, api_id="shared").read_text()
        self.assertIn("restapis/shared/", text)
        self.assertNotIn("restapis/x/", text)

    def test_argo_route_on_by_default(self):
        text = nr.write_nginx_config({"a": "x"}).read_text()
        self.assertIn("location /argo/ {", text)

    def test_argo_route_can_be_disabled(self):
        with mock.patch.dict(
            os.environ, {"HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO": "false"}
        ):
            text = nr.write_nginx_config({"a": "x"}).read_text()
        self.assertNotIn("location /argo/ {", text)


class ReloadTests(_TempHome):
    def test_config_is_tested_before_reloading(self):
        calls = []

        def fake_run(args, **kwargs):
            calls.append(args)
            return mock.Mock(returncode=0, stderr="", stdout="")

        with mock.patch.object(nr.subprocess, "run", side_effect=fake_run):
            self.assertTrue(nr.reload())
        self.assertIn("-t", calls[0])
        self.assertIn("reload", calls[1])

    def test_a_bad_config_does_not_reload(self):
        def fake_run(args, **kwargs):
            if "-t" in args:
                return mock.Mock(returncode=1, stderr="bad config", stdout="")
            raise AssertionError("reload must not run after a failed config test")

        with mock.patch.object(nr.subprocess, "run", side_effect=fake_run):
            self.assertFalse(nr.reload())


class _FakeApiGateway:
    """Stands in for the Floci apigateway client in route discovery."""

    def __init__(self, apis, stages):
        self._apis = apis
        self._stages = stages

    def get_rest_apis(self, limit=500):
        return {"items": self._apis}

    def get_stages(self, restApiId):
        names = self._stages.get(restApiId, [])
        return {"item": [{"stageName": n} for n in names]}


class DeployedServiceRouteTests(_TempHome):
    """Routing services the deployment DAG created, not the ones this CLI deploys."""

    CDKTF = "transform_hmd-ms-transform_local_local_reg1_hmdtr1-rest-api"

    def setUp(self):
        super().setUp()
        self._reload = mock.patch.object(nr, "reload", return_value=True)
        self.reload = self._reload.start()

    def tearDown(self):
        self._reload.stop()
        super().tearDown()

    def _discover(self, apis, stages):
        client = _FakeApiGateway(apis, stages)
        with mock.patch(
            "hmd_cli_neuronsphere.floci_deployer._get_client", return_value=client
        ), mock.patch(
            "hmd_cli_neuronsphere.floci_deployer.env_target", return_value=None
        ):
            return nr.deployed_service_routes(_Env("local"))

    def test_route_is_the_repo_instance_name(self):
        routes = self._discover(
            [{"id": "abc", "name": self.CDKTF}], {"abc": ["some_cdktf_stage"]}
        )
        self.assertEqual(routes, {"transform": ("abc", "some_cdktf_stage")})

    def test_the_real_stage_name_is_used_not_local(self):
        """The whole point: a CDKTF stage is not named `local`, so a route built
        with the default stage returns `Stage not found`."""
        stage = "transform_hmd-ms-transform_local_local_reg1_hmdtr1_api_gateway_stage"
        routes = self._discover([{"id": "abc", "name": self.CDKTF}], {"abc": [stage]})
        self.assertEqual(routes["transform"][1], stage)

    def test_cli_created_gateways_are_skipped(self):
        """`neuronsphere-*` gateways are already routed by write_env_routes."""
        routes = self._discover(
            [{"id": "d", "name": "neuronsphere-hmd_ms_dbaccount"}], {"d": ["local"]}
        )
        self.assertEqual(routes, {})

    def test_gateways_without_a_deployed_stage_are_skipped(self):
        routes = self._discover([{"id": "abc", "name": self.CDKTF}], {"abc": []})
        self.assertEqual(routes, {})

    def test_names_that_are_not_rest_apis_are_skipped(self):
        routes = self._discover(
            [{"id": "x", "name": "transform_hmd-ms-transform_local"}], {"x": ["local"]}
        )
        self.assertEqual(routes, {})

    def test_a_name_without_the_cdktf_underscores_keeps_a_clean_route(self):
        routes = self._discover([{"id": "y", "name": "myapi-rest-api"}], {"y": ["s"]})
        self.assertEqual(routes, {"myapi": ("y", "s")})

    def test_refresh_does_not_clobber_routes_from_write_env_routes(self):
        env = _Env("local")
        nr.write_env_routes(env, {"hmd_ms_dbaccount": "gw-db"})
        with mock.patch.object(
            nr, "deployed_service_routes", return_value={"transform": ("abc", "st")}
        ):
            self.assertEqual(nr.refresh_deployed_service_routes(env), 1)
        text = (self.http_dir() / "10-env-local.conf").read_text()
        self.assertIn("location /local/hmd_ms_dbaccount/", text)
        self.assertIn("location /local/transform/", text)

    def test_refresh_is_idempotent_and_reloads_once_per_run(self):
        env = _Env("local")
        nr.write_env_routes(env, {})
        with mock.patch.object(
            nr, "deployed_service_routes", return_value={"transform": ("abc", "st")}
        ):
            nr.refresh_deployed_service_routes(env)
            nr.refresh_deployed_service_routes(env)
        text = (self.http_dir() / "10-env-local.conf").read_text()
        self.assertEqual(text.count("location /local/transform/ {"), 1)
        self.assertEqual(self.reload.call_count, 2)

    def test_nothing_discovered_means_no_reload(self):
        env = _Env("local")
        with mock.patch.object(nr, "deployed_service_routes", return_value={}):
            self.assertEqual(nr.refresh_deployed_service_routes(env), 0)
        self.reload.assert_not_called()


class IngressVhostTests(_TempHome):
    """Host-routed UIs (Airflow, Argo) reached through the k3s ingress."""

    def test_base_config_includes_vhosts_at_http_level(self):
        """`server {}` cannot nest, so vhost.d must be included outside the
        path-routed server -- which must itself stay the default_server."""
        text = nr.render_base_config().read_text()
        before, sep, after = text.partition("include /etc/nginx/ns/vhost.d/*.conf;")
        self.assertTrue(sep, "vhost.d is not included")
        self.assertNotIn("server {", before)
        self.assertIn("listen 80 default_server;", after)

    def test_base_config_defines_the_upgrade_map(self):
        text = nr.render_base_config().read_text()
        self.assertIn("map $http_upgrade $connection_upgrade {", text)

    def test_creates_the_vhost_dir_so_the_glob_is_valid(self):
        nr.render_base_config()
        self.assertTrue(self.vhost_dir().is_dir())

    def test_vhost_is_a_wildcard_server_forwarding_host(self):
        nr.write_env_vhosts(_Env("local"), "172.18.0.10:31080")
        text = (self.vhost_dir() / "10-env-local.conf").read_text()
        self.assertIn("server_name *.local.neuronsphere.io;", text)
        self.assertIn("proxy_pass http://172.18.0.10:31080;", text)
        # Traefik picks the Ingress rule from Host; rewriting it breaks routing.
        self.assertIn("proxy_set_header Host $host;", text)

    def test_environments_get_their_own_vhost_fragment(self):
        nr.write_env_vhosts(_Env("local"), "10.0.0.1:31080")
        nr.write_env_vhosts(_Env("dev2", slot=1), "10.0.0.2:31080")
        # Each environment still owns a fragment of its own...
        self.assertTrue((self.vhost_dir() / "10-env-dev2.conf").exists())
        # ...but only the default one claims the wildcard. Every environment's
        # charts render the same `*.local.` hostnames, so a second block with
        # that server_name is a conflict nginx resolves by preferring whichever
        # fragment it read first. `*.dev2.neuronsphere.io` -- what this used to
        # write -- matched nothing the charts ever asked for.
        self.assertNotIn(
            "server_name *.",
            (self.vhost_dir() / "10-env-dev2.conf").read_text(),
        )
        self.assertIn(
            "*.local.neuronsphere.io",
            (self.vhost_dir() / "10-env-local.conf").read_text(),
        )

    def test_a_non_default_environment_still_gets_its_port_routes(self):
        """A port is how a second environment's UI is reached at all."""
        nr.write_env_vhosts(
            _Env("dev2", slot=1),
            "10.0.0.2:31080",
            port_routes=[(19081, nr.ingress_host_for("airflow"))],
        )
        text = (self.vhost_dir() / "10-env-dev2.conf").read_text()
        self.assertIn("listen 19081;", text)
        self.assertIn("proxy_set_header Host airflow.local.neuronsphere.io;", text)

    def test_removing_an_env_removes_its_vhost(self):
        env = _Env("dev2", slot=1)
        nr.write_env_vhosts(env, "10.0.0.2:31080")
        nr.write_env_routes(env, {})
        with mock.patch.object(nr, "reload", return_value=True):
            nr.remove_env_routes(env)
        self.assertFalse((self.vhost_dir() / "10-env-dev2.conf").exists())
        self.assertFalse((self.http_dir() / "10-env-dev2.conf").exists())


class ControlPlaneGuiVhostTests(_TempHome):
    """The Deployment GUI is reached on a port, not a hostname.

    It is a control-plane container, not a workload behind an Ingress, so its
    vhost proxies straight to the container and lives in the control-plane
    fragment rather than any environment's.
    """

    def _fragment_path(self):
        return self.vhost_dir() / "00-control-plane.conf"

    def test_gui_listens_on_the_gui_port(self):
        nr.write_control_plane_vhosts()
        text = self._fragment_path().read_text()
        self.assertIn("listen 19003;", text)
        self.assertIn("server_name _;", text)
        self.assertIn(f'set $ns_gui "{nr.GUI_UPSTREAM}";', text)
        self.assertIn("proxy_pass http://$ns_gui;", text)

    def test_the_gui_upstream_resolves_per_request(self):
        """A literal `proxy_pass hmd_deployment_gui:8000` is resolved once, at
        config load, and a name that does not resolve fails `nginx -t` -- which
        would reject the reload and take every other route down with it."""
        nr.write_control_plane_vhosts()
        text = self._fragment_path().read_text()
        self.assertIn("resolver ", text)
        self.assertNotIn(f"proxy_pass http://{nr.GUI_UPSTREAM};", text)

    def test_host_is_forwarded_verbatim(self):
        """No Ingress downstream selects on Host, and `localhost` already
        satisfies the GUI's DJANGO_ALLOWED_HOSTS -- so nothing is rewritten and no
        proxy_redirect pair is needed."""
        nr.write_control_plane_vhosts()
        text = self._fragment_path().read_text()
        self.assertIn("proxy_set_header Host $host;", text)
        self.assertNotIn("proxy_redirect", text)

    def test_the_port_is_overridable(self):
        os.environ["HMD_LOCAL_GUI_HOST_PORT"] = "19107"
        nr.write_control_plane_vhosts()
        self.assertIn("listen 19107;", self._fragment_path().read_text())

    def test_opting_out_writes_no_listener(self):
        """The fragment is still written, so a listener a previous run added is
        removed rather than left behind."""
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_GUI"] = "false"
        nr.write_control_plane_vhosts()
        text = self._fragment_path().read_text()
        self.assertNotIn("listen 19003;", text)

    def test_rewriting_is_idempotent(self):
        first = nr.write_control_plane_vhosts().read_text()
        self.assertEqual(first, nr.write_control_plane_vhosts().read_text())
        self.assertEqual(first.count("listen 19003;"), 1)

    def test_environment_vhosts_are_a_separate_fragment(self):
        """The GUI no longer rides on an environment's spare port, so an
        environment's fragment carries only its wildcard Host block."""
        nr.write_control_plane_vhosts()
        nr.write_env_vhosts(_Env("local"), "172.18.0.10:31080")
        env_text = (self.vhost_dir() / "10-env-local.conf").read_text()
        self.assertIn("server_name *.local.neuronsphere.io;", env_text)
        self.assertNotIn("listen 19003;", env_text)


class PortRoutedVhostTests(_TempHome):
    """`write_env_vhosts(port_routes=...)` still serves an Ingress-exposed UI at
    the root of a published host port. Nothing uses it since the Deployment GUI
    moved to the control plane, but it remains the mechanism for the next one."""

    UPSTREAM = "172.18.0.10:31080"

    def _fragment(self, env, port_routes):
        nr.write_env_vhosts(env, self.UPSTREAM, port_routes=port_routes)
        return (self.vhost_dir() / f"10-env-{env.slug}.conf").read_text()

    def test_host_is_rewritten_to_the_ingress_hostname(self):
        """Traefik selects the Ingress rule by Host, and the browser sends
        `localhost:<port>` -- so unlike the wildcard vhost, Host must be set."""
        text = self._fragment(_Env("local"), [(19003, nr.ingress_host_for("some-app"))])
        self.assertIn("listen 19003;", text)
        self.assertIn("proxy_set_header Host some-app.local.neuronsphere.io;", text)

    def test_ingress_hostname_is_always_the_literal_local_slug(self):
        """hmd-cli-helm's _set_local_standard_values hardcodes
        `alb.hostname=<instance>.local.neuronsphere.io` in *every* environment, so
        the Host header must not be derived from the environment slug."""
        text = self._fragment(
            _Env("dev2", slot=1), [(19007, nr.ingress_host_for("some-app"))]
        )
        self.assertIn("proxy_set_header Host some-app.local.neuronsphere.io;", text)
        self.assertNotIn("some-app.dev2.neuronsphere.io", text)

    def test_absolute_redirects_are_mapped_back_to_the_browser_origin(self):
        text = self._fragment(_Env("local"), [(19003, nr.ingress_host_for("some-app"))])
        self.assertIn(
            "proxy_redirect http://some-app.local.neuronsphere.io/ "
            "http://localhost:19003/;",
            text,
        )

    def test_wildcard_vhost_survives_alongside_the_port_block(self):
        """One fragment file, rewritten wholesale -- the port block must be
        appended to the wildcard block, not replace it."""
        text = self._fragment(_Env("local"), [(19003, nr.ingress_host_for("some-app"))])
        self.assertIn("server_name *.local.neuronsphere.io;", text)
        self.assertIn("listen 19003;", text)

    def test_port_routes_are_optional(self):
        nr.write_env_vhosts(_Env("local"), self.UPSTREAM)
        text = (self.vhost_dir() / "10-env-local.conf").read_text()
        self.assertIn("server_name *.local.neuronsphere.io;", text)
        self.assertNotIn("listen 19003;", text)

    def test_removing_an_env_removes_the_port_block_too(self):
        env = _Env("dev2", slot=1)
        self._fragment(env, [(env.spare_port, nr.ingress_host_for("some-app"))])
        with mock.patch.object(nr, "reload", return_value=True):
            nr.remove_env_routes(env)
        self.assertFalse((self.vhost_dir() / "10-env-dev2.conf").exists())


class NodePortTests(_TempHome):
    """One NodePort helper serves both Trino and the ingress controller."""

    def _applied_spec(self, fn):
        captured = {}

        def fake_run(args, **kwargs):
            captured["spec"] = json.loads(kwargs["input"])
            return mock.Mock(returncode=0, stderr="", stdout="")

        with mock.patch.object(nr.subprocess, "run", side_effect=fake_run):
            self.assertTrue(fn())
        return captured["spec"]

    def test_trino_nodeport_keeps_its_own_port_and_name(self):
        spec = self._applied_spec(
            lambda: nr._ensure_trino_nodeport(
                "trino-local", {"a": "b"}, 8080, "http-coord"
            )
        )
        self.assertEqual(spec["spec"]["type"], "NodePort")
        self.assertEqual(spec["spec"]["ports"][0]["nodePort"], nr.TRINO_NODEPORT)
        self.assertEqual(spec["metadata"]["name"], nr._TRINO_NODEPORT_SVC)

    def test_traefik_nodeport_targets_the_web_entrypoint(self):
        spec = self._applied_spec(
            lambda: nr._ensure_nodeport(
                nr._TRAEFIK_NODEPORT_SVC,
                nr._TRAEFIK_NAMESPACE,
                nr._TRAEFIK_SELECTOR,
                80,
                "web",
                nr.TRAEFIK_NODEPORT,
            )
        )
        self.assertEqual(spec["metadata"]["namespace"], "kube-system")
        self.assertEqual(spec["spec"]["ports"][0]["nodePort"], nr.TRAEFIK_NODEPORT)
        self.assertEqual(spec["spec"]["ports"][0]["targetPort"], "web")

    def test_a_failed_apply_is_reported(self):
        with mock.patch.object(
            nr.subprocess,
            "run",
            return_value=mock.Mock(returncode=1, stderr="nope", stdout=""),
        ):
            self.assertFalse(
                nr._ensure_nodeport("n", "ns", {"a": "b"}, 80, "web", 31080)
            )


class IngressHostResolutionTests(_TempHome):
    """`/etc/hosts` has no wildcards, so every Ingress host needs its own entry."""

    def _kubectl_returning(self, items):
        return mock.patch.object(
            nr,
            "_kubectl",
            return_value=mock.Mock(returncode=0, stdout=json.dumps({"items": items})),
        )

    def test_hosts_are_collected_from_every_ingress(self):
        items = [
            {"spec": {"rules": [{"host": "airflow.local.neuronsphere.io"}]}},
            {"spec": {"rules": [{"host": "argo.local.neuronsphere.io"}]}},
        ]
        with self._kubectl_returning(items):
            self.assertEqual(
                nr.ingress_hosts(_Env("local")),
                ["airflow.local.neuronsphere.io", "argo.local.neuronsphere.io"],
            )

    def test_a_kubectl_failure_is_not_fatal(self):
        with mock.patch.object(nr, "_kubectl", return_value=mock.Mock(returncode=1)):
            self.assertEqual(nr.ingress_hosts(_Env("local")), [])

    def test_only_unresolvable_hosts_are_reported(self):
        items = [{"spec": {"rules": [{"host": "a.local"}, {"host": "b.local"}]}}]

        def fake_gethostbyname(host):
            if host == "a.local":
                return "127.0.0.1"
            raise socket.gaierror("not found")

        with self._kubectl_returning(items), mock.patch(
            "socket.gethostbyname", side_effect=fake_gethostbyname
        ):
            self.assertEqual(nr.unresolvable_ingress_hosts(_Env("local")), ["b.local"])

    def test_a_host_resolving_off_loopback_is_reported(self):
        """A stale public DNS record must not be mistaken for a working setup."""
        items = [{"spec": {"rules": [{"host": "a.local"}]}}]
        with self._kubectl_returning(items), mock.patch(
            "socket.gethostbyname", return_value="93.184.216.34"
        ):
            self.assertEqual(nr.unresolvable_ingress_hosts(_Env("local")), ["a.local"])


class NoAmbiguousComposeAliasTests(_TempHome):
    """No generated fragment may name a Compose *service key* as a host.

    Compose registers each service key as a network alias on every service in
    every project sharing the network. ``floci`` and ``db`` are service keys in
    BOTH docker-compose.control-plane.yml and docker-compose.environment.yml, so
    Docker DNS round-robins them between the control-plane container and every
    environment's. A route built on one reaches the wrong emulated AWS account
    about half the time, which surfaces as Floci answering
    ``{"message":"Invalid API id specified"}`` for a gateway that was in fact
    created in the other account.
    """

    # Substrings, so they are precise: "floci-dev2:4566" and
    # "neuronsphere-local:4566" do not contain "floci:4566".
    AMBIGUOUS = ('"floci:4566"', "//floci:4566", '"db:5432"', "//db:5432")

    def test_no_generated_fragment_names_a_colliding_service_key(self):
        nr.render_base_config()
        nr.write_bootstrap_config()
        nr.write_control_plane_streams()
        nr.write_control_plane_routes({"hmd_ms_deployment": "gw1"})
        for env in (_Env("local", slot=0), _Env("dev2", slot=1)):
            nr.write_env_routes(env, {"hmd_ms_transform": "gw9"})
            nr.write_env_streams(env)

        checked = []
        for path in Path(self._tmp.name, ".cache", "nginx").rglob("*.conf"):
            checked.append(path)
            text = path.read_text()
            for bad in self.AMBIGUOUS:
                self.assertNotIn(
                    bad, text, f"{path.name} names the ambiguous host {bad!r}"
                )
        self.assertTrue(checked, "no fragments were rendered, so nothing was checked")

    def test_a_legacy_env_routes_via_the_alias_not_the_shared_container_name(self):
        """A legacy env's ``floci_container`` *is* the ambiguous ``floci``.

        It shares the control-plane Floci, so its alias is ``neuronsphere`` --
        which is why the env upstreams must read the alias, not the container.
        """
        env = _Env("local")
        env.floci_container = "floci"
        env.floci_alias = "neuronsphere"
        env.legacy_layout = True
        nr.write_env_routes(env, {"hmd_ms_dbaccount": "gw3"})
        text = (self.http_dir() / "10-env-local.conf").read_text()
        self.assertIn("http://neuronsphere:4566/restapis/gw3/local/", text)
        self.assertNotIn("//floci:4566", text)


if __name__ == "__main__":
    unittest.main()
