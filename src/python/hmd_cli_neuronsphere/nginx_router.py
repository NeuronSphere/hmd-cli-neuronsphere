"""nginx route generation for ``hmd_proxy``.

``hmd_proxy`` is the only container that publishes host ports. Everything else
-- the control-plane microservices, every environment's services, Trino, the
Argo UI -- is reached through it, either as an HTTP ``location`` or, for
non-HTTP protocols, as an L4 ``stream`` listener.

Routes are assembled from **fragments** rather than one generated file. The
previous single-file approach used three different mechanisms (a whole-file
rewrite, a string-surgery insert ahead of the catch-all ``location /``, and one
marker-delimited ``stream`` block); none of them compose when N environments
each add and remove their own routes. With fragments each owner writes exactly
one file and deleting an environment is an ``unlink``::

    $HMD_HOME/.cache/nginx/
        neuronsphere.conf             # static shell, generated once
        http.d/00-control-plane.conf  # /hmd_ms_deployment/, /hmd_ms_naming/, ...
        http.d/10-env-<slug>.conf     # /<slug>/<service>/
        stream.d/00-control-plane.conf
        stream.d/10-env-<slug>.conf

The whole directory is bind-mounted into ``hmd_proxy`` at ``/etc/nginx/ns``, so
adding or removing a fragment needs only a reload, never a container restart.

Addressing model:

* Control-plane services keep their historical **unprefixed** paths
  (``http://localhost/hmd_ms_deployment/``), so existing tooling and test
  suites are unaffected.
* Environment services are **prefixed** with the env slug
  (``http://localhost/<slug>/<service>/``).
* The control-plane Floci is streamed on :4566 so ``neuronsphere:4566``
  (and the presigned URLs that bake that hostname) keeps resolving via the
  host's ``/etc/hosts`` entry even though the Floci container itself no longer
  publishes a port.
* Postgres and JanusGraph are deliberately **not** streamed -- databases are
  not reachable from the host. Use ``docker exec <container> psql ...``.
"""

import json
import os
import re
import subprocess
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Tuple

from cement import minimal_logger

logger = minimal_logger("nginx_router")

PROXY_CONTAINER = os.environ.get("HMD_LOCAL_PROXY_CONTAINER", "hmd_proxy")

# Mount point of the nginx cache dir inside hmd_proxy.
_NS_DIR = "/etc/nginx/ns"

_CONTROL_PLANE_FRAGMENT = "00-control-plane.conf"
_ENV_FRAGMENT_PREFIX = "10-env-"

# Control-plane service route aliases. The robot suites build their URL from
# HMD_INSTANCE_NAME, which may arrive as any of these spellings depending on how
# bender is invoked, so route all of them to the same gateway.
_SERVICE_ALIASES = {
    "hmd_ms_deployment": ["ms-deployment", "ms_deployment", "hmd-ms-deployment"],
    "hmd_ms_naming": ["ms-naming", "ms_naming", "hmd-ms-naming"],
}

# k3s NodePort the Trino coordinator is exposed on. The same number is safe in
# every environment because each has its own cluster.
TRINO_NODEPORT = int(os.environ.get("HMD_LOCAL_TRINO_NODEPORT", "31880"))
# Legacy fixed host port for the default environment's Trino, kept so existing
# integration tests keep reaching Trino at host.docker.internal:18080.
LEGACY_TRINO_HOST_PORT = int(os.environ.get("HMD_LOCAL_TRINO_HOST_PORT", "18080"))
_TRINO_NODEPORT_SVC = "trino-local-nodeport"


# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------


def nginx_cache_dir() -> Path:
    return Path(os.environ["HMD_HOME"]) / ".cache" / "nginx"


def base_config_path() -> Path:
    return nginx_cache_dir() / "neuronsphere.conf"


def _http_dir() -> Path:
    return nginx_cache_dir() / "http.d"


def _stream_dir() -> Path:
    return nginx_cache_dir() / "stream.d"


def _env_fragment_name(slug: str) -> str:
    return f"{_ENV_FRAGMENT_PREFIX}{slug}.conf"


# ---------------------------------------------------------------------------
# Base config
# ---------------------------------------------------------------------------


def _resolver_directive() -> str:
    """Docker's embedded DNS, so stream upstreams resolve at connection time.

    nginx resolves a literal ``proxy_pass host:port`` once, when the config
    loads, and **refuses to start** if the name does not resolve. Every stream
    upstream here is a container that may legitimately be down at that moment --
    an environment that is not running, or the control-plane Floci during the
    same ``compose up`` that starts the proxy. Resolving through a variable
    instead defers the lookup to the connection, so one stopped container can
    neither prevent nginx from starting nor take every other route down with it.
    """
    resolver = os.environ.get("HMD_LOCAL_NGINX_RESOLVER", "127.0.0.11")
    return f"resolver {resolver} valid=10s ipv6=off;"


def _stream_server(port: int, upstream: str, var: str = "ns_upstream") -> str:
    """A stream listener whose upstream is resolved per connection.

    ``var`` must be unique per server block within one ``stream {}`` context.
    """
    return (
        f"server {{\n"
        f"    listen {port};\n"
        f'    set ${var} "{upstream}";\n'
        f"    proxy_pass ${var};\n"
        f"}}"
    )


def _stream_var_name(*parts) -> str:
    """A config-safe nginx variable name derived from a route's identity."""
    raw = "_".join(str(p) for p in parts)
    return "ns_" + re.sub(r"[^0-9a-zA-Z_]", "_", raw)


def render_base_config(config_path: Optional[Path] = None) -> Path:
    """Write the static nginx shell that includes every fragment.

    Rewritten on each ``up`` so an upgrade picks up shell changes; the
    fragments it includes carry all the actual routes. A glob that matches no
    files is legal in nginx, so this is valid before any fragment exists.
    """
    path = config_path or base_config_path()
    _http_dir().mkdir(parents=True, exist_ok=True)
    _stream_dir().mkdir(parents=True, exist_ok=True)

    config = f"""events {{
    worker_connections 1024;
}}
http {{
    server {{
        listen 80 default_server;
        server_name _;
        include {_NS_DIR}/http.d/*.conf;
        location / {{
            return 404 '{{"error": "no route defined"}}';
        }}
    }}
}}
stream {{
    {_resolver_directive()}
    include {_NS_DIR}/stream.d/*.conf;
}}
"""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(config)
    logger.info(f"Wrote nginx base config to {path}")
    return path


def bootstrap_config_text(floci_container: str = "floci") -> str:
    """The self-contained config hmd_proxy starts on, before any route exists.

    It must not ``include`` the fragment dirs -- the bind-mounted directory may
    not be visible inside the container yet, and stale fragments from a previous
    run can reference containers that are not up.

    It **must**, however, already listen on 4566. Floci publishes no host port
    of its own: ``localhost:4566`` is this stream and nothing else. ``up`` polls
    that address to decide whether Floci came up, so a placeholder without it
    guarantees a 300-second timeout and a "Ready (degraded)" bootstrap no matter
    how healthy Floci actually is.
    """
    return f"""events {{}}
http {{
    server {{
        listen 80 default_server;
        server_name _;
        location / {{ return 503 'NeuronSphere is starting...'; }}
    }}
}}
stream {{
    {_resolver_directive()}
{_indent(_stream_server(4566, f"{floci_container}:4566", "ns_floci"), 4)}
}}
"""


def config_serves_floci(path: Path) -> bool:
    """Whether the config at ``path`` results in something listening on 4566.

    Two ways it can: the listener is inline (the bootstrap placeholder), or the
    config includes ``stream.d`` *and* the control-plane fragment that carries
    it is actually on disk. The second half matters -- a full include-based
    config whose fragment directory was wiped looks complete but serves nothing
    on 4566, which is precisely the state that hangs ``up``.
    """
    try:
        text = Path(path).read_text()
    except OSError:
        return False
    if "listen 4566;" in text:
        return True
    if f"{_NS_DIR}/stream.d/" in text:
        try:
            fragment = (_stream_dir() / _CONTROL_PLANE_FRAGMENT).read_text()
        except OSError:
            return False
        return "listen 4566;" in fragment
    return False


def write_bootstrap_config(
    config_path: Optional[Path] = None, floci_container: str = "floci"
) -> Path:
    """Write the 503-plus-Floci-stream placeholder so hmd_proxy can start.

    A config that already serves 4566 is left alone: on a normal restart the
    full include-based config is on disk and still correct, and replacing it
    with a placeholder would drop every route until the reload later in ``up``.
    One that does *not* serve 4566 is overwritten even if it exists -- that is
    either the pre-multi-environment single-file config or an older placeholder,
    and keeping it is what makes ``up`` hang waiting for a port nobody listens
    on.
    """
    path = Path(config_path or base_config_path())
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists() and config_serves_floci(path):
        return path
    path.write_text(bootstrap_config_text(floci_container))
    logger.info(f"Wrote nginx bootstrap config (with the :4566 stream) to {path}")
    return path


# ---------------------------------------------------------------------------
# Location blocks
# ---------------------------------------------------------------------------


def _api_location(path: str, upstream_host: str, gw_id: str, stage: str) -> str:
    """A location proxying ``/<path>/`` to an API Gateway stage.

    The trailing slash on both the location and the ``proxy_pass`` target makes
    nginx strip the prefix, so the Lambda receives clean ``/api/...`` paths
    rather than ``/<path>/api/...`` (which FastAPI would 404).
    """
    return f"""location /{path}/ {{
    proxy_pass {upstream_host}/restapis/{gw_id}/{stage}/_user_request_/;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 300s;
    proxy_connect_timeout 75s;
}}"""


def _passthrough_location(path: str, upstream: str) -> str:
    """A location proxying ``/<path>/`` straight at a URL (no API Gateway)."""
    return f"""location /{path}/ {{
    proxy_pass {upstream.rstrip('/')}/;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Host $host;
    proxy_read_timeout 300s;
    proxy_connect_timeout 75s;
}}"""


def _marker(route: str) -> Tuple[str, str]:
    return (f"# >>> ns route {route} >>>", f"# <<< ns route {route} <<<")


def _wrap(route: str, block: str) -> str:
    begin, end = _marker(route)
    return f"{begin}\n{block}\n{end}\n"


def _write_fragment(path: Path, blocks: Iterable[str], header: str) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    body = "\n".join(blocks)
    path.write_text(
        f"# {header}\n# Generated by hmd-cli-neuronsphere. Do not edit.\n{body}"
    )
    return path


# ---------------------------------------------------------------------------
# Control plane
# ---------------------------------------------------------------------------


def write_control_plane_routes(
    services: Dict[str, str],
    stage: str = "local",
    extra_locations: Optional[Dict[str, str]] = None,
    upstream_host: str = "http://neuronsphere:4566",
) -> Path:
    """Write the control-plane HTTP fragment.

    :param services: ``{service_name: api_gateway_id}``.
    :param extra_locations: ``{path: upstream_url}`` for routes that bypass
        API Gateway.
    """
    blocks: List[str] = []
    routed = set()

    for service_name, gw_id in services.items():
        blocks.append(
            _wrap(
                service_name, _api_location(service_name, upstream_host, gw_id, stage)
            )
        )
        routed.add(service_name)

    for canonical, aliases in _SERVICE_ALIASES.items():
        if canonical not in services:
            continue
        gw_id = services[canonical]
        for alias in aliases:
            if alias in routed:
                continue
            blocks.append(
                _wrap(alias, _api_location(alias, upstream_host, gw_id, stage))
            )
            routed.add(alias)

    for path, upstream in (extra_locations or {}).items():
        clean = path.strip("/")
        blocks.append(_wrap(clean, _passthrough_location(clean, upstream)))

    return _write_fragment(
        _http_dir() / _CONTROL_PLANE_FRAGMENT, blocks, "control-plane HTTP routes"
    )


def write_nginx_config(
    services: Dict[str, str],
    config_path: Path = None,
    api_id: str = None,
    stage: str = "local",
    extra_locations: Optional[Dict[str, str]] = None,
    upstream_host: str = "http://neuronsphere:4566",
) -> Path:
    """Render a **self-contained** config for the single-stack (platform) layout.

    Platform mode has no environments: every service lives in the one Floci and
    is routed unprefixed. It also mounts only this one file into ``hmd_proxy``
    (not the fragment directory), so this deliberately inlines every location
    rather than using ``include`` -- an include of an unmounted directory would
    fail nginx's config test and leave the proxy serving nothing.

    Signature preserved so the platform code path is unchanged. ``api_id``, when
    given, overrides every service's gateway id (the historical shared-gateway
    behaviour).
    """
    resolved = {name: (api_id or gw) for name, gw in services.items()}

    blocks: List[str] = []
    routed = set()
    for service_name, gw_id in resolved.items():
        blocks.append(_api_location(service_name, upstream_host, gw_id, stage))
        routed.add(service_name)
    for canonical, aliases in _SERVICE_ALIASES.items():
        if canonical not in resolved:
            continue
        for alias in aliases:
            if alias not in routed:
                blocks.append(
                    _api_location(alias, upstream_host, resolved[canonical], stage)
                )
                routed.add(alias)

    # Argo is enabled by default; expose its UI/API at /argo/.
    if extra_locations is None and os.environ.get(
        "HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO", "true"
    ).lower() not in ("false", "0", "no"):
        extra_locations = {
            "argo": os.environ.get(
                "HMD_LOCAL_ARGO_UPSTREAM", "http://host.docker.internal:30246"
            )
        }
    for path, upstream in (extra_locations or {}).items():
        blocks.append(_passthrough_location(path.strip("/"), upstream))

    locations = "\n".join(_indent(b, 8) for b in blocks)
    config = f"""events {{
    worker_connections 1024;
}}
http {{
    server {{
        listen 80 default_server;
        server_name _;
{locations}
        location / {{
            return 404 '{{"error": "no route defined"}}';
        }}
    }}
}}
"""
    path = config_path or base_config_path()
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(config)
    logger.info(f"Wrote single-stack nginx config to {path}")
    return path


def _indent(block: str, spaces: int) -> str:
    pad = " " * spaces
    return "\n".join(pad + line if line else line for line in block.splitlines())


def write_control_plane_streams(floci_container: str = "floci") -> Path:
    """Write the control-plane stream fragment.

    Only Floci is streamed. It carries :4566 for the control-plane Floci so
    that ``neuronsphere:4566`` -- the hostname baked into presigned S3/API URLs
    and mapped to 127.0.0.1 in the host's ``/etc/hosts`` -- keeps working now
    that the Floci container itself publishes nothing.

    Postgres and JanusGraph are intentionally absent: databases must not be
    reachable from the host.
    """
    block = _wrap(
        "floci",
        _stream_server(4566, f"{floci_container}:4566", "ns_floci"),
    )
    return _write_fragment(
        _stream_dir() / _CONTROL_PLANE_FRAGMENT, [block], "control-plane streams"
    )


# ---------------------------------------------------------------------------
# Environments
# ---------------------------------------------------------------------------


def write_env_routes(
    env,
    services: Dict[str, str],
    stage: str = "local",
    extra_locations: Optional[Dict[str, str]] = None,
) -> Path:
    """Write an environment's HTTP fragment: ``/<slug>/<service>/`` per service."""
    upstream_host = f"http://{env.floci_container}:4566"
    blocks: List[str] = []

    for service_name, gw_id in services.items():
        route = f"{env.slug}/{service_name}"
        blocks.append(_wrap(route, _api_location(route, upstream_host, gw_id, stage)))

    for path, upstream in (extra_locations or {}).items():
        route = f"{env.slug}/{path.strip('/')}"
        blocks.append(_wrap(route, _passthrough_location(route, upstream)))

    return _write_fragment(
        _http_dir() / _env_fragment_name(env.slug),
        blocks,
        f"environment '{env.slug}' HTTP routes",
    )


def write_env_streams(env, entries: Optional[List[Tuple[int, str]]] = None) -> Path:
    """Write an environment's stream fragment.

    :param entries: ``[(host_port, upstream)]``. Defaults to the environment's
        Floci on its allocated port. All ports must fall inside the range
        ``hmd_proxy`` publishes (see ``env_registry.env_port_range``) -- a
        listener outside it is unreachable from the host.
    """
    if entries is None:
        entries = [(env.floci_port, f"{env.floci_container}:4566")]

    blocks = [
        _wrap(
            f"{env.slug}:{port}",
            _stream_server(port, upstream, _stream_var_name(env.slug, port)),
        )
        for port, upstream in entries
    ]
    return _write_fragment(
        _stream_dir() / _env_fragment_name(env.slug),
        blocks,
        f"environment '{env.slug}' streams",
    )


def env_stream_entries(
    env, trino_upstream: Optional[str] = None
) -> List[Tuple[int, str]]:
    """Assemble an environment's full stream listener list."""
    entries = [(env.floci_port, f"{env.floci_container}:4566")]
    if trino_upstream:
        entries.append((env.trino_port, trino_upstream))
        # The default env additionally keeps the historical fixed Trino port so
        # existing integration tests need no change.
        if env.is_default and env.trino_port != LEGACY_TRINO_HOST_PORT:
            entries.append((LEGACY_TRINO_HOST_PORT, trino_upstream))
    return entries


def remove_env_routes(env) -> None:
    """Delete an environment's HTTP and stream fragments. Idempotent."""
    for path in (
        _http_dir() / _env_fragment_name(env.slug),
        _stream_dir() / _env_fragment_name(env.slug),
    ):
        try:
            path.unlink()
            logger.info(f"Removed nginx fragment {path}")
        except FileNotFoundError:
            pass
        except OSError as e:
            logger.warning(f"Could not remove nginx fragment {path}: {e}")


# ---------------------------------------------------------------------------
# Incremental single-route edits
# ---------------------------------------------------------------------------


def add_service_route(
    route_path: str, rest_api_id: str, stage_name: str, *, env=None
) -> None:
    """Add or replace one API Gateway route, then reload.

    Complements the bulk writers: services deployed through the DAG
    (``hmd deploy --local``, CDKTF-managed gateways) are not in the registry
    those writers work from, so their route is spliced in separately. Bounded
    by its own marker comments, so re-running after a redeploy replaces exactly
    that route and nothing else.
    """
    route_path = route_path.strip("/")
    if env is not None:
        fragment = _http_dir() / _env_fragment_name(env.slug)
        upstream_host = f"http://{env.floci_container}:4566"
        route = f"{env.slug}/{route_path}"
    else:
        fragment = _http_dir() / _CONTROL_PLANE_FRAGMENT
        upstream_host = "http://neuronsphere:4566"
        route = route_path

    block = _wrap(route, _api_location(route, upstream_host, rest_api_id, stage_name))
    _upsert_block(fragment, route, block)
    reload()


def _upsert_block(fragment: Path, route: str, block: str) -> None:
    begin, end = _marker(route)
    text = fragment.read_text() if fragment.exists() else ""
    pattern = re.compile(re.escape(begin) + r".*?" + re.escape(end) + r"\n?", re.DOTALL)
    if pattern.search(text):
        text = pattern.sub(block, text)
    else:
        text = (text.rstrip() + "\n" if text.strip() else "") + block
    fragment.parent.mkdir(parents=True, exist_ok=True)
    fragment.write_text(text)


# ---------------------------------------------------------------------------
# Reload
# ---------------------------------------------------------------------------


def reload(container: str = None) -> bool:
    """Validate the config, then reload nginx. Returns True on success.

    ``nginx -t`` runs first because a reload with a bad config leaves the
    *previous* config serving with only a message on stderr -- which surfaces
    much later as every route 404ing, with nothing pointing at the real cause.
    """
    container = container or PROXY_CONTAINER
    test = subprocess.run(
        ["docker", "exec", container, "nginx", "-t"],
        capture_output=True,
        text=True,
    )
    if test.returncode != 0:
        logger.warning(f"nginx config test failed:\n{test.stderr.strip()}")
        return False

    result = subprocess.run(
        ["docker", "exec", container, "nginx", "-s", "reload"],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        logger.warning(f"nginx reload failed: {result.stderr.strip()}")
        return False
    return True


# ---------------------------------------------------------------------------
# Trino host route
# ---------------------------------------------------------------------------
# The Trino coordinator is a ClusterIP inside an environment's k3s cluster,
# unreachable from the host or the bender test container. Expose it as a
# NodePort and L4-stream a host port through hmd_proxy to
# floci-eks-<cluster>:<nodePort>, so clients reach it at
# host.docker.internal:<port> without a `kubectl port-forward`.


def _kubectl(args: List[str], env=None, timeout: int = 30):
    """Run kubectl against a specific environment's cluster."""
    proc_env = dict(os.environ)
    if env is not None:
        proc_env["KUBECONFIG"] = str(env.kubeconfig)
    return subprocess.run(
        ["kubectl", *args],
        capture_output=True,
        text=True,
        timeout=timeout,
        env=proc_env,
    )


def _find_trino_coordinator_service(env=None):
    """``(namespace, selector, port, target_port)`` for the deployed Trino
    coordinator ClusterIP service, or ``None`` when Trino isn't deployed."""
    try:
        r = _kubectl(["get", "svc", "-A", "-o", "json"], env=env)
    except (subprocess.SubprocessError, OSError):
        return None
    if r.returncode != 0:
        return None
    try:
        items = json.loads(r.stdout).get("items", [])
    except (json.JSONDecodeError, TypeError):
        return None
    for it in items:
        if not it["metadata"]["name"].endswith("hmd-inf-trino"):
            continue
        spec = it.get("spec", {})
        selector = spec.get("selector")
        ports = spec.get("ports") or []
        if not selector or not ports:
            continue
        return (
            it["metadata"]["namespace"],
            selector,
            ports[0].get("port", 8080),
            ports[0].get("targetPort", "http-coord"),
        )
    return None


def _ensure_trino_nodeport(namespace, selector, port, target_port, env=None) -> bool:
    """Idempotently apply a NodePort service exposing the Trino coordinator."""
    svc = {
        "apiVersion": "v1",
        "kind": "Service",
        "metadata": {
            "name": _TRINO_NODEPORT_SVC,
            "namespace": namespace,
            "labels": {"app.kubernetes.io/managed-by": "hmd-cli-neuronsphere"},
        },
        "spec": {
            "type": "NodePort",
            "selector": selector,
            "ports": [
                {
                    "name": "http",
                    "port": port,
                    "targetPort": target_port,
                    "nodePort": TRINO_NODEPORT,
                    "protocol": "TCP",
                }
            ],
        },
    }
    try:
        proc_env = dict(os.environ)
        if env is not None:
            proc_env["KUBECONFIG"] = str(env.kubeconfig)
        r = subprocess.run(
            ["kubectl", "apply", "-f", "-"],
            input=json.dumps(svc),
            capture_output=True,
            text=True,
            timeout=30,
            env=proc_env,
        )
    except (subprocess.SubprocessError, OSError) as e:
        logger.warning(f"Could not apply Trino NodePort service: {e}")
        return False
    if r.returncode != 0:
        logger.warning(f"Could not apply Trino NodePort service: {r.stderr.strip()}")
        return False
    return True


def trino_upstream(env) -> Optional[str]:
    """``<floci-eks ip>:<nodePort>`` for this env's Trino, or None if absent."""
    from .floci_deployer import _floci_eks_ip

    found = _find_trino_coordinator_service(env)
    if not found:
        logger.debug("Trino coordinator service not found; skipping host route.")
        return None
    namespace, selector, port, target_port = found
    if not _ensure_trino_nodeport(namespace, selector, port, target_port, env):
        return None
    ip = _floci_eks_ip(env.k3s_cluster)
    if not ip:
        logger.warning("Could not resolve floci-eks IP; Trino host route not wired.")
        return None
    return f"{ip}:{TRINO_NODEPORT}"


def configure_trino_host_route(env) -> bool:
    """Wire this environment's Trino coordinator to its host stream port.

    No-op when Trino isn't deployed. Caller reloads nginx when this returns
    True.
    """
    upstream = trino_upstream(env)
    if not upstream:
        return False
    write_env_streams(env, env_stream_entries(env, trino_upstream=upstream))
    logger.info(
        f"Wired Trino host route for '{env.slug}': :{env.trino_port} -> {upstream}"
    )
    return True
