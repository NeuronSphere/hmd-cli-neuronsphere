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

# The k3s ingress controller (Traefik) fronts every UI an environment's charts
# expose via an Ingress -- Airflow, Argo, Trino. It is a ClusterIP inside the
# cluster, so it gets a NodePort like Trino's, and hmd_proxy Host-routes to it.
TRAEFIK_NODEPORT = int(os.environ.get("HMD_LOCAL_TRAEFIK_NODEPORT", "31080"))
_TRAEFIK_NODEPORT_SVC = "traefik-local-nodeport"
_TRAEFIK_NAMESPACE = "kube-system"
_TRAEFIK_SELECTOR = {
    "app.kubernetes.io/instance": "traefik-kube-system",
    "app.kubernetes.io/name": "traefik",
}
# Ingress hosts the charts render are `<app>.<env-slug>.neuronsphere.io`, so one
# wildcard server block per environment covers every UI it deploys, now and later.
INGRESS_DOMAIN = os.environ.get("HMD_LOCAL_INGRESS_DOMAIN", "neuronsphere.io")

# Control-plane service route aliases. The robot suites build their URL from
# HMD_INSTANCE_NAME, which may arrive as any of these spellings depending on how
# bender is invoked, so route all of them to the same gateway.
_SERVICE_ALIASES = {
    "hmd_ms_deployment": ["ms-deployment", "ms_deployment", "hmd-ms-deployment"],
    "hmd_ms_naming": ["ms-naming", "ms_naming", "hmd-ms-naming"],
}

# The k3s API server's port inside the `floci-eks-*` container. Reached through
# an hmd_proxy stream rather than the port Floci publishes on that container --
# see `env_registry.LocalEnvironment.k3s_port` for why.
K3S_API_PORT = int(os.environ.get("HMD_LOCAL_K3S_API_PORT", "6443"))

# k3s NodePort the Trino coordinator is exposed on. The same number is safe in
# every environment because each has its own cluster.
TRINO_NODEPORT = int(os.environ.get("HMD_LOCAL_TRINO_NODEPORT", "31880"))
# Legacy fixed host port for the default environment's Trino, kept so existing
# integration tests keep reaching Trino at host.docker.internal:18080.
LEGACY_TRINO_HOST_PORT = int(os.environ.get("HMD_LOCAL_TRINO_HOST_PORT", "18080"))
_TRINO_NODEPORT_SVC = "trino-local-nodeport"

# Only the account in the credential scope is read by Floci; the region and
# date are structural filler, kept constant so the rendered config is stable.
_SIGV4_REGION = os.environ.get("AWS_DEFAULT_REGION", "us-west-2")

# Carries the caller's own Authorization past the account selector. An
# environment route has to spend Authorization on Floci's SigV4 credential
# scope -- a 12-digit access key *is* the account, and it is the only thing
# Floci resolves a v1 REST API's owner from -- so a caller's bearer token rides
# here instead. In the cloud this header does not exist and Authorization is
# never a credential scope, so nothing there behaves differently.
_ALT_AUTH_HEADER = "X-NS-Authorization"


def _sigv4_credential(account_id: str) -> str:
    """The credential scope Floci reads the account out of."""
    return (
        f"AWS4-HMAC-SHA256 Credential={account_id}/20200101/{_SIGV4_REGION}"
        "/execute-api/aws4_request, SignedHeaders=host, Signature=x"
    )


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


def _vhost_dir() -> Path:
    return nginx_cache_dir() / "vhost.d"


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
    _vhost_dir().mkdir(parents=True, exist_ok=True)

    # `vhost.d` is included at the `http {}` level, not inside the server block:
    # Host-routed UIs need whole `server {}` blocks, whereas `http.d` fragments
    # are `location`s spliced into the one path-routed server. Keeping
    # `default_server` on that server is what preserves today's behaviour --
    # a request only reaches a vhost when its Host actually matches.
    config = f"""events {{
    worker_connections 1024;
}}
http {{
    map $http_upgrade $connection_upgrade {{
        default upgrade;
        ''      close;
    }}
    include {_NS_DIR}/vhost.d/*.conf;
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
    logger.debug(f"Wrote nginx base config to {path}")
    return path


def bootstrap_config_text(floci_host: str = "neuronsphere") -> str:
    """The self-contained config hmd_proxy starts on, before any route exists.

    It must not ``include`` the fragment dirs -- the bind-mounted directory may
    not be visible inside the container yet, and stale fragments from a previous
    run can reference containers that are not up.

    It **must**, however, already listen on 4566. Floci publishes no host port
    of its own: ``localhost:4566`` is this stream and nothing else. ``up`` polls
    that address to decide whether Floci came up, so a placeholder without it
    guarantees a 300-second timeout and a "Ready (degraded)" bootstrap no matter
    how healthy Floci actually is.

    ``floci_host`` must be an explicit network *alias*, never a Compose service
    key -- see :func:`_env_floci_host`.
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
{_indent(_stream_server(4566, f"{floci_host}:4566", "ns_floci"), 4)}
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
    config_path: Optional[Path] = None, floci_host: str = "neuronsphere"
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
    path.write_text(bootstrap_config_text(floci_host))
    logger.debug(f"Wrote nginx bootstrap config (with the :4566 stream) to {path}")
    return path


# ---------------------------------------------------------------------------
# Location blocks
# ---------------------------------------------------------------------------


def _env_floci_host(env) -> str:
    """The DNS name of the Floci serving ``env`` -- always ``neuronsphere``.

    Every environment shares the single Floci container and is distinguished by
    the account its requests are signed for, not by hostname. This still reads
    the explicit network *alias* rather than the Compose service key ``floci``:
    Compose registers each service key as a network alias on the shared network,
    so ``floci`` can resolve to more than one container.

    Note this host only selects the container. An HTTP route through it reaches
    whichever account the *upstream request* is signed for, so per-environment
    API Gateway routes stay distinct by rest-api id, which Floci scopes per
    account.
    """
    return getattr(env, "floci_alias", None) or env.floci_container


def _api_location(
    path: str,
    upstream_host: str,
    gw_id: str,
    stage: str,
    account_id: Optional[str] = None,
) -> str:
    """A location proxying ``/<path>/`` to an API Gateway stage.

    The trailing slash on both the location and the ``proxy_pass`` target makes
    nginx strip the prefix, so the Lambda receives clean ``/api/...`` paths
    rather than ``/<path>/api/...`` (which FastAPI would 404).

    ``account_id`` is required for any API owned by a non-default Floci account
    -- i.e. every named environment. One Floci serves all accounts and resolves
    which one from the SigV4 credential scope, but ``proxy_pass`` issues an
    *unsigned* request, so there is nothing to resolve from and the invocation
    lands in the default account. The REST API is not there, and Floci answers
    404: indistinguishable from a route that was never wired, which is what made
    this read as a missing service rather than a missing credential.

    Floci parses the account out of the credential scope without verifying the
    signature, so a static header suffices. It is set *unconditionally*,
    displacing whatever the caller sent. It used to fill only an empty
    ``Authorization``, which made the two uses of that header mutually
    exclusive: a request carrying a real JWT kept it, left Floci nothing to
    resolve the account from, and 404d -- so only the environment whose account
    happens to be Floci's default could serve an authenticated request at all.
    The caller's token travels beside it in :data:`_ALT_AUTH_HEADER`, which
    ``hmd-lib-auth``'s ``auth_token()`` reads back.
    """
    account = (
        f'\n    proxy_set_header Authorization "{_sigv4_credential(account_id)}";'
        f"\n    proxy_set_header {_ALT_AUTH_HEADER} $http_authorization;"
        if account_id
        else ""
    )
    return f"""location /{path}/ {{
    proxy_pass {upstream_host}/restapis/{gw_id}/{stage}/_user_request_/;{account}
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
    logger.debug(f"Wrote single-stack nginx config to {path}")
    return path


def _indent(block: str, spaces: int) -> str:
    pad = " " * spaces
    return "\n".join(pad + line if line else line for line in block.splitlines())


def write_control_plane_streams(floci_host: str = "neuronsphere") -> Path:
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
        _stream_server(4566, f"{floci_host}:4566", "ns_floci"),
    )
    return _write_fragment(
        _stream_dir() / _CONTROL_PLANE_FRAGMENT, [block], "control-plane streams"
    )


# The Deployment GUI container in the control-plane compose file, and the port it
# listens on inside it.
GUI_UPSTREAM = "hmd_deployment_gui:8000"


def _container_vhost_server(port: int, upstream: str, var: str) -> str:
    """A port-listening server block proxying to a container by name.

    The upstream goes through a variable and a ``resolver`` for the reason
    :func:`_resolver_directive` gives: a literal ``proxy_pass host:port`` is
    resolved once, at config load, and a name that does not resolve makes
    ``nginx -t`` fail -- which would reject the whole reload and take every
    *other* route down with it. Deferring the lookup to the request means a GUI
    container that failed to start costs only its own 502.

    ``Host`` is forwarded verbatim: nothing downstream selects on it (there is no
    Ingress here), and ``localhost`` already satisfies the app's allowed-hosts
    list -- so no ``proxy_redirect`` pair is needed either.
    """
    return f"""server {{
    listen {port};
    server_name _;
    {_resolver_directive()}
    set ${var} "{upstream}";
    location / {{
        proxy_pass http://${var};
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 300s;
        proxy_connect_timeout 75s;
    }}
}}"""


def write_control_plane_vhosts() -> Path:
    """Write the control-plane vhost fragment.

    Just the Deployment GUI today. It is served at the *root* path of a published
    host port (``http://localhost:19003/``) rather than behind the wildcard
    ``*.<slug>.neuronsphere.io`` vhost, so reaching it costs no /etc/hosts entry.
    The port is already inside the range hmd_proxy publishes
    (``env_registry.env_port_range``), so adding this listener needs a reload, not
    a container restart.

    Unlike the environment vhosts this proxies straight to a container on the
    Docker network, with no Ingress in between -- see
    :func:`_container_vhost_server`.

    An always-written fragment: when the GUI is disabled the file is truncated to
    its header, which removes a listener a previous run may have added.
    """
    from .bom_seeder import gui_enabled, gui_port

    blocks = []
    if gui_enabled():
        blocks.append(
            _wrap(
                "deployment-gui",
                _container_vhost_server(gui_port(), GUI_UPSTREAM, "ns_gui"),
            )
        )
    return _write_fragment(
        _vhost_dir() / _CONTROL_PLANE_FRAGMENT, blocks, "control-plane vhosts"
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
    upstream_host = f"http://{_env_floci_host(env)}:4566"
    blocks: List[str] = []

    account_id = getattr(env, "account_id", None)
    for service_name, gw_id in services.items():
        route = f"{env.slug}/{service_name}"
        blocks.append(
            _wrap(
                route,
                _api_location(route, upstream_host, gw_id, stage, account_id),
            )
        )

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

    :param entries: ``[(host_port, upstream)]``, defaulting to none. All ports
        must fall inside the range ``hmd_proxy`` publishes (see
        ``env_registry.env_port_range``) -- a listener outside it is unreachable
        from the host.

    There is no per-environment Floci listener: one Floci serves every account
    and is already streamed on the control plane's :4566
    (:func:`write_control_plane_streams`). Host-side callers pick the account
    with their credentials, not with a port.
    """
    if entries is None:
        entries = []

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


def k3s_api_upstream(env) -> Optional[str]:
    """``<floci-eks ip>:6443`` for this env's k3s API server, or None if absent.

    Cheap and side-effect free -- one ``docker inspect`` -- unlike
    :func:`trino_upstream`, which also has to apply a NodePort service. That
    matters because this is resolved on every rewrite of the fragment (see
    :func:`env_stream_entries`), not just when the route is first wired.
    """
    from .floci_deployer import _floci_eks_ip, env_target

    try:
        ip = _floci_eks_ip(env.k3s_cluster, target=env_target(env))
    except Exception as e:  # pragma: no cover - defensive
        logger.debug(f"Could not resolve floci-eks IP: {e}")
        return None
    if not ip:
        logger.debug("Could not resolve floci-eks IP; k3s API host route not wired.")
        return None
    return f"{ip}:{K3S_API_PORT}"


def env_stream_entries(
    env,
    trino_upstream: Optional[str] = None,
    k3s_upstream: Optional[str] = None,
) -> List[Tuple[int, str]]:
    """Assemble an environment's full stream listener list.

    ``k3s_upstream`` is resolved here when not supplied, rather than left to the
    caller like ``trino_upstream``. Every writer rewrites the whole fragment, so
    a route only one of them knows about would be dropped by the next one --
    wiring k3s and then deploying Trino would silently take kubectl offline
    again. Resolving it on each rewrite makes the k3s route survive them all.
    """
    entries: List[Tuple[int, str]] = []
    if trino_upstream:
        entries.append((env.trino_port, trino_upstream))
        # The default env additionally keeps the historical fixed Trino port so
        # existing integration tests need no change.
        if env.is_default and env.trino_port != LEGACY_TRINO_HOST_PORT:
            entries.append((LEGACY_TRINO_HOST_PORT, trino_upstream))
    if k3s_upstream is None:
        k3s_upstream = k3s_api_upstream(env)
    if k3s_upstream:
        entries.append((env.k3s_port, k3s_upstream))
    return entries


def _vhost_server(server_name: str, upstream: str) -> str:
    """A Host-routed server block proxying everything to the ingress controller.

    ``Host`` is forwarded verbatim because Traefik picks the Ingress rule from
    it -- rewriting it would make every UI resolve to the same (or no) backend.
    The path is passed through unchanged for the same reason, which is why
    ``_passthrough_location`` is not reusable here: it strips its own prefix.
    The Upgrade/Connection pair is what lets Argo stream workflow logs.
    """
    return f"""server {{
    listen 80;
    server_name {server_name};
    location / {{
        proxy_pass http://{upstream};
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 300s;
        proxy_connect_timeout 75s;
    }}
}}"""


# hmd-cli-helm's `_set_local_standard_values` renders every local chart with
# `--set alb.hostname=<instance>.local.neuronsphere.io`. `--set` beats any values
# file, and the slug there is the literal string "local" in *every* environment --
# so an Ingress hostname is derived from the instance name alone, never env.slug.
_HELM_LOCAL_SLUG = "local"


def ingress_host_for(instance_name: str) -> str:
    """The Ingress hostname hmd-cli-helm gives ``instance_name``'s chart."""
    return f"{instance_name}.{_HELM_LOCAL_SLUG}.{INGRESS_DOMAIN}"


def _port_vhost_server(
    port: int, upstream: str, ingress_host: str, public_origin: str
) -> str:
    """A port-listening server block for one Ingress-exposed UI.

    The wildcard vhost below reaches a UI by hostname, which costs the user an
    /etc/hosts entry. An environment also reserves a spare host port that
    hmd_proxy already publishes, so a UI can instead be served at its *root* path
    on ``http://localhost:<port>/`` with no DNS at all -- which is how the
    Deployment GUI is reached.

    ``Host`` is set rather than forwarded, the one difference from
    :func:`_vhost_server`: Traefik selects the Ingress rule from it and the browser
    sends ``localhost:<port>``, which matches no rule. ``proxy_redirect`` undoes
    that substitution on the way back, so an absolute ``Location`` built from the
    rewritten Host does not send the browser to a name it cannot resolve.
    """
    return f"""server {{
    listen {port};
    server_name _;
    location / {{
        proxy_pass http://{upstream};
        proxy_set_header Host {ingress_host};
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_redirect http://{ingress_host}/ {public_origin}/;
        proxy_redirect https://{ingress_host}/ {public_origin}/;
        proxy_read_timeout 300s;
        proxy_connect_timeout 75s;
    }}
}}"""


def write_env_vhosts(env, upstream: str, port_routes=()) -> Path:
    """Write an environment's Host-routed vhost fragment.

    One wildcard block per environment (``*.<slug>.neuronsphere.io``) rather
    than one per app: the charts derive their Ingress hosts from the environment
    name, so a wildcard covers every UI the environment deploys without this
    module having to know which apps exist.

    ``port_routes`` is an optional sequence of ``(port, ingress_host)`` pairs, each
    additionally served at ``http://localhost:<port>/`` (see
    :func:`_port_vhost_server`). They go in this same fragment rather than one of
    their own: :func:`_write_fragment` rewrites a fragment wholesale, so a second
    writer aimed at a second file would still have to be kept in step with
    :func:`remove_env_routes` -- one file keeps writing and removal atomic.
    """
    blocks = [
        _wrap(
            f"{env.slug}:vhost",
            _vhost_server(f"*.{env.slug}.{INGRESS_DOMAIN}", upstream),
        )
    ]
    for port, ingress_host in port_routes:
        blocks.append(
            _wrap(
                f"{env.slug}:vhost:{port}",
                _port_vhost_server(
                    port, upstream, ingress_host, f"http://localhost:{port}"
                ),
            )
        )
    return _write_fragment(
        _vhost_dir() / _env_fragment_name(env.slug),
        blocks,
        f"environment '{env.slug}' ingress vhosts",
    )


def remove_env_routes(env) -> None:
    """Delete an environment's HTTP, stream and vhost fragments. Idempotent."""
    for path in (
        _http_dir() / _env_fragment_name(env.slug),
        _stream_dir() / _env_fragment_name(env.slug),
        _vhost_dir() / _env_fragment_name(env.slug),
    ):
        try:
            path.unlink()
            logger.debug(f"Removed nginx fragment {path}")
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
    _upsert_service_route(route_path, rest_api_id, stage_name, env=env)
    reload()


def _upsert_service_route(
    route_path: str, rest_api_id: str, stage_name: str, *, env=None
) -> str:
    """Splice one API Gateway route into its fragment **without** reloading.

    Split out of :func:`add_service_route` so the bulk refresh can upsert N
    routes and reload once, instead of reloading per route. Returns the route
    it wrote.
    """
    route_path = route_path.strip("/")
    if env is not None:
        fragment = _http_dir() / _env_fragment_name(env.slug)
        upstream_host = f"http://{_env_floci_host(env)}:4566"
        route = f"{env.slug}/{route_path}"
        # Same reason as write_env_routes: an unsigned proxy_pass would resolve
        # in the default account, where this API does not exist.
        account_id = getattr(env, "account_id", None)
    else:
        fragment = _http_dir() / _CONTROL_PLANE_FRAGMENT
        upstream_host = "http://neuronsphere:4566"
        route = route_path
        account_id = None

    block = _wrap(
        route,
        _api_location(route, upstream_host, rest_api_id, stage_name, account_id),
    )
    _upsert_block(fragment, route, block)
    return route


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
# DAG-deployed services
# ---------------------------------------------------------------------------
# Services deployed through the real deployment DAG (`hmd deploy --local`, the
# `up` bootstrap) get a CDKTF-managed API Gateway that neither `write_env_routes`
# nor `write_control_plane_routes` knows about -- those writers only see the
# Lambdas this CLI deploys itself. Their gateways have to be discovered from
# Floci after the DAG has run, which is also the only way to learn the real
# stage name: CDKTF stages are named after the stack, not "local", so a route
# built with the default stage returns `{"message": "Stage not found"}`.

# Gateways this CLI creates itself (floci_deployer.setup_service names them
# `neuronsphere-<service>`); already routed by the bulk writers, so discovery
# skips them rather than writing a second, redundant route.
_CLI_GATEWAY_PREFIX = "neuronsphere-"

# CDKTF names its REST API `<instance>_<repo_class>_<did>_<env>_<region>_<customer>-rest-api`.
# The leading segment is the repo *instance* name, which is what the service is
# addressed as: `transform_hmd-ms-transform_local_..._-rest-api` -> /<env>/transform/.
_REST_API_SUFFIX = "-rest-api"


def _route_path_for_gateway(name: str) -> Optional[str]:
    """The route segment a CDKTF gateway name should be served under."""
    if not name or name.startswith(_CLI_GATEWAY_PREFIX):
        return None
    if not name.endswith(_REST_API_SUFFIX):
        return None
    # Strip the suffix before splitting so a name carrying no `_` at all still
    # yields a usable route rather than one ending in "-rest-api".
    stem = name[: -len(_REST_API_SUFFIX)]
    instance = stem.split("_", 1)[0].strip()
    return instance or None


def deployed_service_routes(env, target=None) -> Dict[str, Tuple[str, str]]:
    """``{route_path: (rest_api_id, stage_name)}`` for DAG-deployed gateways.

    Reads the environment's own Floci account -- a DAG-deployed service lives
    there, not in the control-plane Floci. An API with no deployed stage is
    skipped: it would 404 anyway, and it is usually a gateway mid-deploy.
    """
    from .floci_deployer import _get_client, env_target

    if target is None and env is not None:
        target = env_target(env)

    client = _get_client("apigateway", target)
    routes: Dict[str, Tuple[str, str]] = {}
    for api in client.get_rest_apis(limit=500).get("items", []):
        route_path = _route_path_for_gateway(api.get("name", ""))
        if not route_path or route_path in routes:
            continue
        stages = client.get_stages(restApiId=api["id"]).get("item", [])
        if not stages:
            logger.debug(f"Gateway {api.get('name')} has no deployed stage; skipping.")
            continue
        routes[route_path] = (api["id"], stages[0]["stageName"])
    return routes


def refresh_deployed_service_routes(env, target=None) -> int:
    """Route every DAG-deployed service in ``env``; reload once. Returns the count.

    Unconditional rather than driven by what this run deployed: ``write_env_routes``
    rewrites the whole fragment on every ``up``, so an already-bootstrapped
    environment that deploys nothing still needs its DAG routes put back.
    """
    routes = deployed_service_routes(env, target=target)
    if not routes:
        return 0
    for route_path, (rest_api_id, stage_name) in routes.items():
        _upsert_service_route(route_path, rest_api_id, stage_name, env=env)
        logger.debug(f"Routed /{env.slug}/{route_path}/ -> {rest_api_id}/{stage_name}")
    reload()
    return len(routes)


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


def _ensure_nodeport(
    name, namespace, selector, port, target_port, node_port, env=None
) -> bool:
    """Idempotently apply a NodePort service fronting an in-cluster workload.

    A NodePort is how anything on the Docker network reaches a ClusterIP inside
    k3s: ``hmd_proxy`` connects to ``floci-eks-<cluster>:<nodePort>``. Applied as
    a *separate* service rather than by mutating the workload's own -- Traefik's
    service is owned by a k3s Addon, which reverts out-of-band edits.
    """
    svc = {
        "apiVersion": "v1",
        "kind": "Service",
        "metadata": {
            "name": name,
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
                    "nodePort": node_port,
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
        logger.warning(f"Could not apply NodePort service {name}: {e}")
        return False
    if r.returncode != 0:
        logger.warning(f"Could not apply NodePort service {name}: {r.stderr.strip()}")
        return False
    return True


def _ensure_trino_nodeport(namespace, selector, port, target_port, env=None) -> bool:
    """Idempotently apply a NodePort service exposing the Trino coordinator."""
    return _ensure_nodeport(
        _TRINO_NODEPORT_SVC,
        namespace,
        selector,
        port,
        target_port,
        TRINO_NODEPORT,
        env=env,
    )


def trino_upstream(env) -> Optional[str]:
    """``<floci-eks ip>:<nodePort>`` for this env's Trino, or None if absent."""
    from .floci_deployer import _floci_eks_ip, env_target

    found = _find_trino_coordinator_service(env)
    if not found:
        logger.debug("Trino coordinator service not found; skipping host route.")
        return None
    namespace, selector, port, target_port = found
    if not _ensure_trino_nodeport(namespace, selector, port, target_port, env):
        return None
    ip = _floci_eks_ip(env.k3s_cluster, target=env_target(env))
    if not ip:
        logger.warning("Could not resolve floci-eks IP; Trino host route not wired.")
        return None
    return f"{ip}:{TRINO_NODEPORT}"


def configure_k3s_host_route(env) -> bool:
    """Wire this environment's k3s API server to its host stream port.

    Must run before anything writes or uses the kubeconfig: `write_kubeconfig`
    points the server URL at :attr:`~env_registry.LocalEnvironment.k3s_port`, and
    every later `kubectl` -- the CoreDNS patch, the ingress class, the operator
    installs -- goes through it. No-op when the k3s container isn't up. Caller
    reloads nginx when this returns True.
    """
    upstream = k3s_api_upstream(env)
    if not upstream:
        return False
    write_env_streams(env, env_stream_entries(env, k3s_upstream=upstream))
    logger.debug(
        f"Wired k3s API host route for '{env.slug}': :{env.k3s_port} -> {upstream}"
    )
    return True


def configure_trino_host_route(env) -> bool:
    """Wire this environment's Trino coordinator to its host stream port.

    No-op when Trino isn't deployed. Caller reloads nginx when this returns
    True.
    """
    upstream = trino_upstream(env)
    if not upstream:
        return False
    write_env_streams(env, env_stream_entries(env, trino_upstream=upstream))
    logger.debug(
        f"Wired Trino host route for '{env.slug}': :{env.trino_port} -> {upstream}"
    )
    return True


# ---------------------------------------------------------------------------
# Ingress host route
# ---------------------------------------------------------------------------
# Charts expose their UIs (Airflow, Argo, Trino) through Ingress objects, exactly
# as they do in the cloud. Locally the ingress controller is the k3s Traefik, so
# reaching those UIs from the host means: NodePort in front of Traefik, then a
# Host-routed nginx vhost in hmd_proxy that forwards `Host` untouched.


def ingress_upstream(env) -> Optional[str]:
    """``<floci-eks ip>:<nodePort>`` for this env's ingress controller, or None."""
    from .floci_deployer import _floci_eks_ip, env_target

    if not _ensure_nodeport(
        _TRAEFIK_NODEPORT_SVC,
        _TRAEFIK_NAMESPACE,
        _TRAEFIK_SELECTOR,
        80,
        "web",
        TRAEFIK_NODEPORT,
        env=env,
    ):
        return None
    ip = _floci_eks_ip(env.k3s_cluster, target=env_target(env))
    if not ip:
        logger.warning("Could not resolve floci-eks IP; ingress route not wired.")
        return None
    return f"{ip}:{TRAEFIK_NODEPORT}"


def configure_ingress_host_route(env) -> bool:
    """Wire this environment's ingress controller to a Host-routed nginx vhost.

    Caller reloads nginx when this returns True.
    """
    upstream = ingress_upstream(env)
    if not upstream:
        return False
    write_env_vhosts(env, upstream)
    logger.debug(
        f"Wired ingress vhost for '{env.slug}': "
        f"*.{env.slug}.{INGRESS_DOMAIN} -> {upstream}"
    )
    return True


def ingress_hosts(env) -> List[str]:
    """Every Ingress hostname declared in this environment's cluster."""
    try:
        r = _kubectl(["get", "ingress", "-A", "-o", "json"], env=env)
    except (subprocess.SubprocessError, OSError):
        return []
    if r.returncode != 0:
        return []
    try:
        items = json.loads(r.stdout).get("items", [])
    except (json.JSONDecodeError, TypeError):
        return []

    hosts = []
    for item in items:
        for rule in item.get("spec", {}).get("rules") or []:
            host = rule.get("host")
            if host and host not in hosts:
                hosts.append(host)
    return hosts


def unresolvable_ingress_hosts(env) -> List[str]:
    """Ingress hostnames that do not resolve to loopback on this machine.

    ``/etc/hosts`` has no wildcards, so the vhost's ``*.<slug>`` server_name
    cannot help until each name resolves. Unlike the ``neuronsphere`` alias
    (see ``floci_deployer.ensure_neuronsphere_hosts_entry``, which aborts), a
    missing UI hostname breaks nothing else -- so callers warn, never abort.
    """
    import socket

    missing = []
    for host in ingress_hosts(env):
        try:
            ip = socket.gethostbyname(host)
        except socket.gaierror:
            missing.append(host)
            continue
        if not (ip.startswith("127.") or ip == "::1"):
            missing.append(host)
    return missing
