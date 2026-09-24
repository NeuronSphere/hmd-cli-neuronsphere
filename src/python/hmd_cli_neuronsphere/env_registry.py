"""Named local environment registry.

A local NeuronSphere is split into a single **control plane** (ms-deployment,
ms-naming, artifact-lib as Floci Lambdas, plus the supporting Floci, nginx
proxy, Postgres and JanusGraph) and N **environments**. Each environment is a
self-contained emulated AWS account: its own Floci container, its own EKS/k3s
cluster, its own Postgres and JanusGraph, and its own ``hmd-ms-dbaccount`` --
mirroring the cloud, where every account carries its own dbaccount, RDS and
Neptune.

This module owns the environment *identity*: what each one is called, which
account id and containers it uses, where its state lives, and which host ports
``hmd_proxy`` streams to it. Every derived value is computed once at create
time and then **persisted**, so changing a derivation rule later can never
orphan containers or volumes that were named under the old rule.

State lives in ``$HMD_HOME/.cache/neuronsphere/environments.json``; per-env
data lives under ``$HMD_HOME/.cache/environments/<slug>/``. The control plane
keeps its historical ``$HMD_HOME/{floci/data,postgresql/data,graph_db}`` paths
so upgrading an existing install does not move any data.
"""

import binascii
import json
import os
import re
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional

from cement import minimal_logger

logger = minimal_logger("env_registry")

REGISTRY_VERSION = 1
DEFAULT_ENV_NAME = "local"

# There is one Floci container for the whole install. Every environment is an
# emulated AWS *account* inside it, selected per-request by signing with that
# account's 12-digit id (``floci_deployer.FlociTarget.access_key_id``) -- so
# unlike the container-per-environment layout this replaces, there is no
# per-environment Floci container or network alias to derive.
CONTROL_PLANE_FLOCI_CONTAINER = "floci"
CONTROL_PLANE_FLOCI_ALIAS = "neuronsphere"

# Host ports hmd_proxy publishes for env-scoped nginx `stream{}` listeners. The
# whole range is published up front by the control-plane compose file (a
# compose `ports:` list is static), and individual listeners inside it are
# added/removed at runtime with an nginx reload -- no container restart. Four
# slots per env: Floci, Trino, graph, spare.
PORTS_PER_ENV = 4
DEFAULT_PORT_BASE = 19000
MAX_ENVS = 16
# One further port per environment, in a band directly above the slot ports, for
# the k3s API server stream -- see `LocalEnvironment.k3s_port` for why the API is
# reached through hmd_proxy at all, and why this is a band rather than a fifth
# slot port.
K3S_PORTS_PER_ENV = 1

# Route paths owned by the control plane (and nginx internals). An env slug may
# not shadow one, or `/<slug>/` would swallow a control-plane route.
_RESERVED_SLUGS = frozenset(
    {
        "api",
        "apiop",
        "argo",
        "aws",
        "restapis",
        "hmd_ms_deployment",
        "hmd_ms_naming",
        "hmd_ms_artifact_lib",
        "hmd_ms_dbaccount",
        "ms-deployment",
        "ms_deployment",
        "hmd-ms-deployment",
        "ms-naming",
        "ms_naming",
        "hmd-ms-naming",
    }
)

_SLUG_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,15}$")

# The control-plane account. Environment accounts are allocated above this so
# an env can never be mistaken for the control plane.
CONTROL_PLANE_ACCOUNT_ID = "000000000000"
_ENV_ACCOUNT_BASE = 1


class EnvRegistryError(RuntimeError):
    """Raised for invalid environment names or registry operations."""


@dataclass
class LocalEnvironment:
    """One named local environment.

    Every field is persisted. Nothing here is recomputed on load -- see the
    module docstring for why.
    """

    name: str
    slug: str
    account_id: str
    deployment_id: str
    core_instance_name: str
    db_container: str
    graph_container: str
    state_dir: str
    compose_project: str
    k3s_cluster: str
    kubeconfig: str
    port_slot: int
    port_base: int = DEFAULT_PORT_BASE
    # True for an environment migrated from the pre-multi-env layout, where the
    # control-plane Floci/Postgres/JanusGraph *are* the default env's. Such an
    # env has no compose project of its own.
    legacy_layout: bool = False
    bootstrap: Dict[str, Any] = field(default_factory=dict)

    # -- the single Floci ---------------------------------------------------
    # Derived, not persisted: every environment shares the one Floci container
    # and its `neuronsphere` alias, and is told apart by `account_id` alone.
    # Registries written by an older CLI still carry per-environment values for
    # these; load() drops unknown keys, so those simply fall away.
    @property
    def floci_container(self) -> str:
        return CONTROL_PLANE_FLOCI_CONTAINER

    @property
    def floci_alias(self) -> str:
        return CONTROL_PLANE_FLOCI_ALIAS

    # -- derived host ports ------------------------------------------------
    @property
    def floci_port(self) -> int:
        """The slot's base port.

        No longer carries a Floci stream listener -- the single Floci is reached
        on the control plane's :4566 -- but the slot layout is deliberately
        unchanged: `trino_port`, `graph_port` and `spare_port` are offsets from
        it, and slot 0's spare port is the Deployment GUI's published 19003
        (`bom_seeder._DEFAULT_GUI_PORT`). Renumbering to reclaim one port would
        move every environment's Trino and the GUI.
        """
        return self.port_base + self.port_slot * PORTS_PER_ENV

    @property
    def trino_port(self) -> int:
        return self.floci_port + 1

    @property
    def graph_port(self) -> int:
        return self.floci_port + 2

    @property
    def k3s_port(self) -> int:
        """Host port hmd_proxy streams to this environment's k3s API server.

        The API used to be reached on the port Floci publishes on the
        `floci-eks-*` container itself. Docker re-creates that forward every time
        `down`/`up` stops and restarts the container, and a re-created one
        silently truncates any write past roughly one MTU: the first ~1440 bytes
        arrive, the rest never do. A TLS 1.3 ClientHello carrying a post-quantum
        key share is 1449 bytes -- which is what kubectl (Go >= 1.24) and
        OpenSSL >= 3.5 send by default -- so the apiserver waits forever for the
        rest of a hello that never lands, and every client fails with
        `net/http: TLS handshake timeout` against a cluster that is perfectly
        healthy. (A TLS 1.2 hello is 163 bytes and connects instantly, which is
        why `curl` -- LibreSSL, no ML-KEM -- makes the API look reachable.)

        Routing through hmd_proxy sidesteps that forward entirely, and restores
        the invariant `nginx_router` already documents: hmd_proxy is the only
        container that publishes host ports.

        Deliberately a band above the slot ports rather than a fifth slot port:
        widening the stride would renumber every existing environment's Trino,
        graph and spare port, and slot 0's spare is the Deployment GUI's
        published 19003.
        """
        return self.port_base + MAX_ENVS * PORTS_PER_ENV + self.port_slot

    @property
    def spare_port(self) -> int:
        """Reserved, currently unused by any environment-scoped listener.

        The Deployment GUI used to be served here while it was deployed per
        environment; it is now a control-plane container fixed at slot 0's spare
        port (``bom_seeder._DEFAULT_GUI_PORT``, 19003). The slot stays reserved so
        the next port-routed UI has somewhere to go without renumbering.
        """
        return self.floci_port + 3

    # -- derived paths -----------------------------------------------------
    @property
    def state_path(self) -> Path:
        return Path(self.state_dir)

    @property
    def floci_data_dir(self) -> Path:
        return self.state_path / "floci" / "data"

    @property
    def postgres_data_dir(self) -> Path:
        return self.state_path / "postgresql" / "data"

    @property
    def graph_data_dir(self) -> Path:
        return self.state_path / "graph_db"

    @property
    def kubeconfig_path(self) -> Path:
        return Path(self.kubeconfig)

    @property
    def is_default(self) -> bool:
        return self.slug == DEFAULT_ENV_NAME

    def state_dirs(self) -> List[Path]:
        """Every directory that must exist before the env compose file starts.

        ``floci_data_dir`` is deliberately absent: the environment's Floci state
        lives inside the single control-plane Floci's data dir, namespaced by
        account. The property is kept only so
        :func:`legacy_env_floci_state` can spot a pre-collapse install.
        """
        return [
            self.postgres_data_dir,
            self.graph_data_dir,
            self.kubeconfig_path.parent,
        ]

    def compose_env(self) -> Dict[str, str]:
        """``NS_ENV_*`` variables consumed by ``docker-compose.environment.yml``.

        No ``NS_ENV_FLOCI_*``: that compose file no longer defines a Floci
        service.
        """
        return {
            "NS_ENV_SLUG": self.slug,
            "NS_ENV_ACCOUNT_ID": self.account_id,
            "NS_ENV_DB_CONTAINER": self.db_container,
            "NS_ENV_GRAPH_CONTAINER": self.graph_container,
            "NS_ENV_STATE_DIR": str(self.state_path),
            "NS_ENV_DEPLOYMENT_ID": self.deployment_id,
        }


@dataclass
class ControlPlane:
    compose_project: str
    network: str
    floci_data_dir: str
    bootstrapped: bool = False


@dataclass
class Registry:
    control_plane: ControlPlane
    environments: Dict[str, LocalEnvironment] = field(default_factory=dict)
    default_env: str = DEFAULT_ENV_NAME
    version: int = REGISTRY_VERSION

    def get(self, name: str) -> Optional[LocalEnvironment]:
        return self.environments.get(_slugify(name))

    def used_slots(self) -> set:
        return {e.port_slot for e in self.environments.values()}

    def used_accounts(self) -> set:
        return {e.account_id for e in self.environments.values()}


# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------


def _hmd_home() -> Path:
    home = os.environ.get("HMD_HOME")
    if not home:
        raise EnvRegistryError("HMD_HOME is not set")
    return Path(home)


def registry_path() -> Path:
    return _hmd_home() / ".cache" / "neuronsphere" / "environments.json"


def legacy_marker_path() -> Path:
    """The pre-multi-env bootstrap marker, read once during migration."""
    return _hmd_home() / ".cache" / "neuronsphere" / "bootstrap.json"


def environments_root() -> Path:
    return _hmd_home() / ".cache" / "environments"


# ---------------------------------------------------------------------------
# Naming
# ---------------------------------------------------------------------------


def _slugify(name: str) -> str:
    return (name or "").strip().lower()


def validate_slug(name: str) -> str:
    """Validate and normalize an environment name.

    The name becomes a URL path segment (``/<slug>/<service>/``), a Docker
    container-name suffix and a k3s cluster-name component, so it is kept to
    lowercase alphanumerics and hyphens.
    """
    slug = _slugify(name)
    if not slug:
        raise EnvRegistryError("Environment name must not be empty.")
    if not _SLUG_RE.match(slug):
        raise EnvRegistryError(
            f"Invalid environment name '{name}'. Use 1-16 characters, lowercase "
            f"letters, digits and hyphens, starting with a letter or digit."
        )
    if slug in _RESERVED_SLUGS:
        raise EnvRegistryError(
            f"Environment name '{slug}' is reserved -- it would shadow a "
            f"control-plane route at http://localhost/{slug}/."
        )
    return slug


def _hmd_home_hash() -> str:
    """Match ``floci_deployer._HMD_HOME_HASH`` without importing it.

    Importing ``floci_deployer`` here would pull boto3 and its module-level
    endpoint resolution into every registry read, including the ones that run
    before Docker exists.
    """
    import hashlib

    home = os.environ.get("HMD_HOME")
    if not home:
        return ""
    return hashlib.sha256(os.path.abspath(home).encode()).hexdigest()[:8]


def allocate_port_slot(reg: Registry, name: str) -> int:
    """Pick a stable port slot for ``name``.

    Hashing the name first means the same environment name usually lands on the
    same ports across machines and re-creations, which makes bookmarks and
    scripts stable; the lowest-free fallback keeps allocation correct when two
    names collide.
    """
    used = reg.used_slots()
    if len(used) >= MAX_ENVS:
        raise EnvRegistryError(
            f"All {MAX_ENVS} environment port slots are in use. Delete an "
            f"environment with `hmd neuronsphere env delete <name>` first."
        )
    preferred = binascii.crc32(name.encode()) % MAX_ENVS
    if preferred not in used:
        return preferred
    for slot in range(MAX_ENVS):
        if slot not in used:
            return slot
    raise EnvRegistryError("No free environment port slot.")  # pragma: no cover


def allocate_account_id(reg: Registry) -> str:
    """Allocate the next unused 12-digit emulated AWS account id."""
    used = reg.used_accounts() | {CONTROL_PLANE_ACCOUNT_ID}
    n = _ENV_ACCOUNT_BASE
    while f"{n:012d}" in used:
        n += 1
    return f"{n:012d}"


# ---------------------------------------------------------------------------
# Load / save
# ---------------------------------------------------------------------------


def _default_control_plane() -> ControlPlane:
    h = _hmd_home_hash()
    return ControlPlane(
        compose_project=os.environ.get("HMD_LOCAL_COMPOSE_PROJECT_NAME")
        or (f"local_neuronsphere-{h}" if h else "local_neuronsphere"),
        network=os.environ.get("HMD_LOCAL_DOCKER_NETWORK")
        or (f"neuronsphere_default-{h}" if h else "neuronsphere_default"),
        floci_data_dir=str(_hmd_home() / "floci" / "data"),
    )


def _build_environment(reg: Registry, name: str) -> LocalEnvironment:
    slug = validate_slug(name)
    h = _hmd_home_hash()
    slot = allocate_port_slot(reg, slug)
    return LocalEnvironment(
        name=slug,
        slug=slug,
        account_id=allocate_account_id(reg),
        deployment_id=slug,
        # The instance name is identical in every environment: repo_instance is
        # unique by name *per Environment*, so `local-neuronsphere` in `dev2` is
        # a different instance than the one in `local`. This matches the cloud.
        core_instance_name="local-neuronsphere",
        db_container=f"hmd_db-{slug}",
        graph_container=f"global-graph-{slug}",
        state_dir=str(environments_root() / slug),
        compose_project=f"ns-{h}-env-{slug}" if h else f"ns-env-{slug}",
        k3s_cluster=f"ns-{slug}-{h}" if h else f"ns-{slug}",
        kubeconfig=str(environments_root() / slug / "k3s" / "kubeconfig"),
        port_slot=slot,
        port_base=int(os.environ.get("HMD_LOCAL_ENV_PORT_BASE", DEFAULT_PORT_BASE)),
    )


def _legacy_environment(marker: Optional[Dict[str, Any]]) -> LocalEnvironment:
    """Synthesize the default env for an install that predates this registry.

    In the legacy layout the control-plane Floci/Postgres/JanusGraph *are* the
    default environment's, and the k3s cluster keeps the name Floci already
    spawned its container and volume under. Nothing is moved or redeployed.
    """
    h = _hmd_home_hash()
    return LocalEnvironment(
        name=DEFAULT_ENV_NAME,
        slug=DEFAULT_ENV_NAME,
        account_id=CONTROL_PLANE_ACCOUNT_ID,
        deployment_id=DEFAULT_ENV_NAME,
        core_instance_name="local-neuronsphere",
        db_container="hmd_db",
        graph_container="global-graph",
        state_dir=str(_hmd_home()),
        compose_project=os.environ.get("HMD_LOCAL_COMPOSE_PROJECT_NAME")
        or (f"local_neuronsphere-{h}" if h else "local_neuronsphere"),
        k3s_cluster=os.environ.get("HMD_LOCAL_K3S_CLUSTER_NAME")
        or (f"neuronsphere-{h}" if h else "neuronsphere"),
        kubeconfig=os.environ.get("HMD_LOCAL_K3S_KUBECONFIG")
        or str(_hmd_home() / ".cache" / "k3s" / "kubeconfig"),
        port_slot=0,
        port_base=int(os.environ.get("HMD_LOCAL_ENV_PORT_BASE", DEFAULT_PORT_BASE)),
        legacy_layout=True,
        bootstrap=dict(marker or {}),
    )


def _needs_legacy_migration() -> Optional[Dict[str, Any]]:
    """Return the legacy bootstrap marker (possibly ``{}``) when this HMD_HOME
    was already bootstrapped by a pre-multi-env CLI, else ``None``."""
    marker_path = legacy_marker_path()
    if marker_path.exists():
        try:
            return json.loads(marker_path.read_text())
        except (json.JSONDecodeError, OSError):
            return {}
    # No marker, but persisted Floci state means a prior `up` ran.
    floci_data = _hmd_home() / "floci" / "data"
    try:
        if floci_data.is_dir() and any(floci_data.iterdir()):
            return {}
    except OSError:
        pass
    return None


def load() -> Registry:
    """Read the registry, migrating a pre-multi-env install on first use."""
    path = registry_path()
    if path.exists():
        try:
            raw = json.loads(path.read_text())
        except (json.JSONDecodeError, OSError) as e:
            raise EnvRegistryError(f"Could not read {path}: {e}")
        cp = raw.get("control_plane") or {}
        reg = Registry(
            control_plane=ControlPlane(
                compose_project=cp.get("compose_project")
                or _default_control_plane().compose_project,
                network=cp.get("network") or _default_control_plane().network,
                floci_data_dir=cp.get("floci_data_dir")
                or str(_hmd_home() / "floci" / "data"),
                bootstrapped=bool(cp.get("bootstrapped", False)),
            ),
            default_env=raw.get("default_env") or DEFAULT_ENV_NAME,
            version=int(raw.get("version", REGISTRY_VERSION)),
        )
        for slug, data in (raw.get("environments") or {}).items():
            known = {f for f in LocalEnvironment.__dataclass_fields__}
            reg.environments[slug] = LocalEnvironment(
                **{k: v for k, v in data.items() if k in known}
            )
        return reg

    reg = Registry(control_plane=_default_control_plane())
    marker = _needs_legacy_migration()
    if marker is not None:
        reg.environments[DEFAULT_ENV_NAME] = _legacy_environment(marker)
        reg.control_plane.bootstrapped = True
        logger.debug("Migrated pre-multi-environment layout into the env registry.")
    return reg


def legacy_env_floci_state(reg: Registry) -> List["LocalEnvironment"]:
    """Environments still holding state from the container-per-environment layout.

    Before the multi-account collapse each environment ran its own Floci with its
    own ``<state_dir>/floci/data``. That state cannot be merged into the single
    Floci: it is namespaced on disk by account prefix in a format Floci does not
    document, so anything we did here would be a guess.

    Silently ignoring those directories would be worse than failing -- the
    environment's Lambdas, API gateways, buckets and secrets would appear to have
    vanished while `up` reported success. So `up` refuses and asks for a purge;
    see ``hmd_cli_neuronsphere._assert_no_legacy_env_floci_state``.

    A legacy-layout environment is exempt: its "own" Floci data dir *is* the
    control plane's, which is still exactly where its state belongs.
    """
    stale = []
    for env in reg.environments.values():
        if getattr(env, "legacy_layout", False):
            continue
        try:
            if env.floci_data_dir.is_dir() and any(env.floci_data_dir.iterdir()):
                stale.append(env)
        except OSError:
            continue
    return stale


def save(reg: Registry) -> None:
    """Persist the registry atomically.

    ``env create`` and a concurrent ``up`` can both write, and a torn registry
    would strand running containers with no record of their names -- so write
    to a temp file in the same directory and rename over the target.
    """
    path = registry_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = {
        "version": reg.version,
        "default_env": reg.default_env,
        "control_plane": asdict(reg.control_plane),
        "environments": {
            slug: {
                k: v
                for k, v in asdict(env).items()
                # dataclasses.asdict keeps only real fields; properties are
                # derived on read and must not be persisted.
                if k in LocalEnvironment.__dataclass_fields__
            }
            for slug, env in reg.environments.items()
        },
    }
    tmp = path.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(payload, indent=2, sort_keys=True))
    os.replace(tmp, path)


# ---------------------------------------------------------------------------
# Operations
# ---------------------------------------------------------------------------


def resolve_env(
    name: Optional[str] = None, reg: Optional[Registry] = None
) -> LocalEnvironment:
    """Resolve an environment by explicit name, ``HMD_LOCAL_ENV``, or default.

    Raises if the requested environment does not exist -- creating one is an
    explicit action (`env create`), never a side effect of a typo.
    """
    reg = reg if reg is not None else load()
    requested = name or os.environ.get("HMD_LOCAL_ENV") or reg.default_env
    env = reg.get(requested)
    if env is None:
        known = ", ".join(sorted(reg.environments)) or "(none)"
        raise EnvRegistryError(
            f"Unknown local environment '{requested}'. Known environments: {known}. "
            f"Create it with `hmd neuronsphere env create {requested}`."
        )
    return env


def ensure_default_env(reg: Optional[Registry] = None) -> LocalEnvironment:
    """Find or create the default ``local`` environment."""
    owns_reg = reg is None
    reg = reg if reg is not None else load()
    env = reg.get(reg.default_env)
    if env is None:
        env = _build_environment(reg, reg.default_env)
        reg.environments[env.slug] = env
        if owns_reg:
            save(reg)
    return env


def create_env(name: str, reg: Optional[Registry] = None) -> LocalEnvironment:
    """Register a new environment. Does not start anything."""
    owns_reg = reg is None
    reg = reg if reg is not None else load()
    slug = validate_slug(name)
    if slug in reg.environments:
        raise EnvRegistryError(f"Environment '{slug}' already exists.")
    env = _build_environment(reg, slug)
    reg.environments[slug] = env
    if owns_reg:
        save(reg)
    return env


def remove_env(name: str, reg: Optional[Registry] = None) -> None:
    owns_reg = reg is None
    reg = reg if reg is not None else load()
    slug = validate_slug(name)
    if slug not in reg.environments:
        raise EnvRegistryError(f"Unknown local environment '{slug}'.")
    del reg.environments[slug]
    if reg.default_env == slug:
        reg.default_env = next(iter(reg.environments), DEFAULT_ENV_NAME)
    if owns_reg:
        save(reg)


def list_envs(reg: Optional[Registry] = None) -> List[LocalEnvironment]:
    reg = reg if reg is not None else load()
    return [reg.environments[k] for k in sorted(reg.environments)]


def set_default(name: str, reg: Optional[Registry] = None) -> LocalEnvironment:
    owns_reg = reg is None
    reg = reg if reg is not None else load()
    env = reg.get(name)
    if env is None:
        raise EnvRegistryError(f"Unknown local environment '{name}'.")
    reg.default_env = env.slug
    if owns_reg:
        save(reg)
    return env


def record_bootstrap(
    env: LocalEnvironment,
    csd_nid: str,
    mode: str = "extend",
    k3s_uid: Optional[str] = None,
) -> None:
    """Persist an environment's bootstrap state (replaces ``bootstrap.json``)."""
    reg = load()
    target = reg.get(env.slug)
    if target is None:
        reg.environments[env.slug] = env
        target = env
    target.bootstrap = {"mode": mode, "csd_nid": csd_nid}
    if k3s_uid:
        target.bootstrap["k3s_uid"] = k3s_uid
    env.bootstrap = dict(target.bootstrap)
    save(reg)


def clear_bootstrap(env: LocalEnvironment) -> None:
    reg = load()
    target = reg.get(env.slug)
    if target is not None:
        target.bootstrap = {}
        save(reg)
    env.bootstrap = {}


def env_port_range() -> str:
    """The contiguous host port range hmd_proxy publishes for env streams.

    Published up front because a compose ``ports:`` list is static; individual
    ``stream{}`` listeners inside the range come and go with an nginx reload.
    """
    override = os.environ.get("HMD_LOCAL_ENV_PORT_RANGE")
    if override:
        return override
    base = int(os.environ.get("HMD_LOCAL_ENV_PORT_BASE", DEFAULT_PORT_BASE))
    return f"{base}-{base + MAX_ENVS * (PORTS_PER_ENV + K3S_PORTS_PER_ENV) - 1}"
