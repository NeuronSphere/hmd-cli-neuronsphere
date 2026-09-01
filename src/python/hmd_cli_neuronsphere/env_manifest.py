"""Declarative local environment manifests.

A named local environment is *declared* in a manifest -- which plugins to
enable and which additional repo instances to deploy -- rather than being an
incidental by-product of whichever plugin packages happen to be pip-installed.
:mod:`change_set_builder` compiles the manifest into a
``hmd_lang_deployment.change_set`` definition and :mod:`env_reconcile` diffs
that desired state against what is actually deployed.

Manifests live at ``$HMD_HOME/environments/<slug>.{yaml,yml,json}`` (YAML and
JSON are equivalent; discovery tries the extensions in that order).
``HMD_LOCAL_ENV_MANIFEST`` overrides the lookup with an explicit path.

Format::

    version: 1
    name: dev2

    # Allow-list of entry-point names in the
    # `hmd_cli_neuronsphere.get_local_bom_entries` group. The *presence* of this
    # key switches the environment to strict mode: only the listed plugins
    # contribute. Omit it entirely to keep the historical "every installed
    # plugin contributes" behaviour.
    plugins:
      - ns-telemetry
      - ns-trino

    # Optional per-plugin configuration handed to the contributor callable.
    plugin_config:
      ns-telemetry:
        profile: minimal

    # Additional repo instances, on top of whatever the plugins contribute.
    repos:
      - instance_name: my-api
        repo_class_name: hmd-ms-myapi
        source:
          type: local        # only `local` today; `artifact` is reserved
          path: null         # default: $HMD_REPO_HOME/<repo_class_name>
        version: null        # default: the bundled artifact's version
        instance_configuration: {}
        dependencies:
          eks-cluster: local-neuronsphere

An environment with **no** manifest keeps working exactly as it did before this
module existed: :func:`load_manifest` returns ``None`` and every caller falls
back to the legacy resolution path.
"""

import json
import os
import shutil
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional

import yaml
from cement import minimal_logger

logger = minimal_logger("env_manifest")

MANIFEST_VERSION = 1

# Tried in order by :func:`manifest_path`. YAML first: it is the documented
# authoring format, JSON is accepted for generated/checked-in manifests.
MANIFEST_EXTENSIONS = (".yaml", ".yml", ".json")

# Source kinds a declared repo instance can be deployed from. Only local working
# trees are supported today; `artifact` (deploy from a downloaded build bundle)
# is reserved so manifests can already be written against the eventual name and
# get a clear "not yet supported" error instead of "unknown source type".
SOURCE_LOCAL = "local"
SOURCE_ARTIFACT = "artifact"
KNOWN_SOURCE_TYPES = frozenset({SOURCE_LOCAL, SOURCE_ARTIFACT})
SUPPORTED_SOURCE_TYPES = frozenset({SOURCE_LOCAL})


class EnvManifestError(RuntimeError):
    """Raised for an unreadable, malformed or invalid environment manifest."""


@dataclass
class RepoDeclaration:
    """One additional repo instance declared by a manifest."""

    instance_name: str
    repo_class_name: str
    source_type: str = SOURCE_LOCAL
    source_path: Optional[str] = None
    version: Optional[str] = None
    instance_configuration: Dict[str, Any] = field(default_factory=dict)
    dependencies: Dict[str, Any] = field(default_factory=dict)

    def resolved_repo_path(self) -> Optional[str]:
        """The working-tree path this instance deploys from, if resolvable.

        ``source.path`` wins; otherwise ``$HMD_REPO_HOME/<repo_class_name>`` --
        the same convention :func:`local_workflow_runner._repo_path_for` uses to
        locate the directory it mounts into the projectbuilder container.
        """
        if self.source_type != SOURCE_LOCAL:
            return None
        if self.source_path:
            return os.path.expanduser(os.path.expandvars(self.source_path))
        repo_home = os.environ.get("HMD_REPO_HOME", "")
        if not repo_home:
            return None
        return os.path.join(repo_home, self.repo_class_name)


@dataclass
class EnvManifest:
    """A parsed environment manifest.

    ``plugins is None`` means the manifest did not mention plugins at all, which
    keeps the non-strict "every installed plugin contributes" behaviour. An
    empty list is a deliberate "no plugins".
    """

    name: str
    version: int = MANIFEST_VERSION
    plugins: Optional[List[str]] = None
    plugin_config: Dict[str, Dict[str, Any]] = field(default_factory=dict)
    repos: List[RepoDeclaration] = field(default_factory=list)
    path: Optional[str] = None

    @property
    def strict_plugins(self) -> bool:
        """Whether only the listed plugins may contribute BOM entries."""
        return self.plugins is not None

    def enabled_plugins(self) -> Optional[set]:
        """The allow-list to hand to ``_collect_plugin_bom_entries``."""
        return None if self.plugins is None else set(self.plugins)


# ---------------------------------------------------------------------------
# Paths / discovery
# ---------------------------------------------------------------------------


def _hmd_home() -> Path:
    home = os.environ.get("HMD_HOME")
    if not home:
        raise EnvManifestError("HMD_HOME is not set")
    return Path(home)


def manifests_root() -> Path:
    """Where user-authored manifests live.

    Deliberately **not** under ``.cache``: a manifest is authored input a
    developer edits and may commit, not machine state that ``down --purge`` is
    free to delete.
    """
    return _hmd_home() / "environments"


def manifest_path(slug: str) -> Optional[Path]:
    """The manifest file for ``slug``, or ``None`` when it has none.

    ``HMD_LOCAL_ENV_MANIFEST`` short-circuits the lookup with an explicit path
    (useful for one-off runs and for tests).
    """
    override = os.environ.get("HMD_LOCAL_ENV_MANIFEST")
    if override:
        path = Path(os.path.expanduser(override))
        return path if path.is_file() else None

    root = manifests_root()
    for ext in MANIFEST_EXTENSIONS:
        candidate = root / f"{slug}{ext}"
        if candidate.is_file():
            return candidate
    return None


def install_manifest(slug: str, source: str) -> Path:
    """Copy an authored manifest into ``manifests_root()`` for ``slug``.

    Keeps the source file's extension so a JSON manifest stays JSON. Returns the
    installed path.
    """
    src = Path(os.path.expanduser(source))
    if not src.is_file():
        raise EnvManifestError(f"Manifest file not found: {source}")
    ext = src.suffix.lower()
    if ext not in MANIFEST_EXTENSIONS:
        raise EnvManifestError(
            f"Manifest must be one of {', '.join(MANIFEST_EXTENSIONS)}; got '{ext}'"
        )
    root = manifests_root()
    root.mkdir(parents=True, exist_ok=True)
    dest = root / f"{slug}{ext}"
    # Parse before installing so an invalid manifest never lands in HMD_HOME.
    load_manifest_file(src)
    already_installed = dest.exists() and src.resolve() == dest.resolve()
    if not already_installed:
        shutil.copyfile(src, dest)
    logger.debug(f"Installed manifest for '{slug}': {dest}")
    return dest


# ---------------------------------------------------------------------------
# Loading
# ---------------------------------------------------------------------------


def _parse_document(path: Path) -> Any:
    text = path.read_text()
    try:
        if path.suffix.lower() == ".json":
            return json.loads(text)
        # yaml.safe_load parses JSON too, but routing by extension keeps the
        # error messages pointed at the format the author actually wrote.
        return yaml.safe_load(text)
    except (yaml.YAMLError, json.JSONDecodeError) as e:
        raise EnvManifestError(f"Could not parse manifest {path}: {e}") from e


def _repo_from_dict(raw: Dict[str, Any]) -> RepoDeclaration:
    source = raw.get("source") or {}
    if not isinstance(source, dict):
        raise EnvManifestError(
            f"repos[].source must be a mapping, got {type(source).__name__}"
        )
    return RepoDeclaration(
        instance_name=raw.get("instance_name"),
        repo_class_name=raw.get("repo_class_name"),
        source_type=source.get("type") or SOURCE_LOCAL,
        source_path=source.get("path"),
        version=raw.get("version"),
        instance_configuration=raw.get("instance_configuration") or {},
        dependencies=raw.get("dependencies") or {},
    )


def parse_manifest(doc: Any, path: Optional[str] = None) -> EnvManifest:
    """Build an :class:`EnvManifest` from an already-parsed document.

    Structural coercion only -- semantic checks live in
    :mod:`validators.env_manifest_validator` so the CLI can report every problem
    at once instead of failing on the first one.
    """
    if not isinstance(doc, dict):
        raise EnvManifestError(
            f"Manifest must be a mapping, got {type(doc).__name__}"
            + (f" ({path})" if path else "")
        )

    raw_repos = doc.get("repos") or []
    if not isinstance(raw_repos, list):
        raise EnvManifestError(
            f"'repos' must be a list, got {type(raw_repos).__name__}"
        )
    repos = []
    for raw in raw_repos:
        if not isinstance(raw, dict):
            raise EnvManifestError(
                f"Each entry in 'repos' must be a mapping, got {type(raw).__name__}"
            )
        repos.append(_repo_from_dict(raw))

    plugins = doc.get("plugins", None)
    if plugins is not None and not isinstance(plugins, list):
        raise EnvManifestError(
            f"'plugins' must be a list, got {type(plugins).__name__}"
        )

    plugin_config = doc.get("plugin_config") or {}
    if not isinstance(plugin_config, dict):
        raise EnvManifestError(
            f"'plugin_config' must be a mapping, got {type(plugin_config).__name__}"
        )

    return EnvManifest(
        name=doc.get("name"),
        version=doc.get("version", MANIFEST_VERSION),
        plugins=list(plugins) if plugins is not None else None,
        plugin_config=plugin_config,
        repos=repos,
        path=path,
    )


def load_manifest_file(path) -> EnvManifest:
    """Parse and validate the manifest at ``path``.

    :raises EnvManifestError: on a malformed or invalid manifest.
    """
    path = Path(path)
    manifest = parse_manifest(_parse_document(path), path=str(path))

    from .validators.env_manifest_validator import validate_manifest

    errors = validate_manifest(manifest)
    if errors:
        detail = "\n".join(f"  - {e}" for e in errors)
        raise EnvManifestError(f"Invalid environment manifest {path}:\n{detail}")
    return manifest


def load_manifest(slug: str) -> Optional[EnvManifest]:
    """The manifest for environment ``slug``, or ``None`` when it has none.

    ``None`` is the compatibility path: every caller falls back to the legacy
    "resolve from installed plugins" behaviour, so pre-manifest environments are
    untouched by this feature.
    """
    path = manifest_path(slug)
    if path is None:
        return None
    return load_manifest_file(path)


def manifest_from_bom_file(slug: str, bom_file: str) -> EnvManifest:
    """Wrap a legacy flat BOM array as a manifest, preserving its semantics.

    ``--bom-file`` / ``HMD_LOCAL_BOM_FILE`` predate manifests. Their entries
    become declared repos and ``plugins`` stays ``None`` (non-strict), so an
    existing BOM file behaves exactly as it always has.
    """
    from .bom_seeder import load_bom_from_file

    repos = []
    for entry in load_bom_from_file(bom_file):
        repos.append(
            RepoDeclaration(
                instance_name=entry.get("repo_instance_name"),
                repo_class_name=entry.get("repo_class_name"),
                version=entry.get("repo_class_version"),
                instance_configuration=entry.get("instance_configuration") or {},
                dependencies=entry.get("dependencies") or {},
            )
        )
    return EnvManifest(name=slug, repos=repos, path=bom_file)
