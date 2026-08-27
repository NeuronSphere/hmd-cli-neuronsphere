"""Validator for declarative environment manifests (see :mod:`env_manifest`).

Returns every problem it finds rather than raising on the first one: a manifest
is hand-authored, and reporting one typo per run turns a five-minute edit into
five round trips.

The checks that matter most are the ones whose failure mode is *silent*:

- An ``instance_name`` colliding with a core instance (``local-neuronsphere``,
  ``local-databases``) would have the environment's own infrastructure adopted
  into the user's declared set -- and therefore be a candidate for ``--prune``.
- A ``source.type: local`` repo whose working tree is missing fails deep inside
  the projectbuilder container instead of before anything starts, because
  ``local_workflow_runner._repo_path_for`` silently resolves it to ``None``.
"""

import os
from typing import Any, Dict, List

from ..env_manifest import (
    KNOWN_SOURCE_TYPES,
    MANIFEST_VERSION,
    SOURCE_LOCAL,
    SUPPORTED_SOURCE_TYPES,
)


def _protected_instance_names() -> set:
    """Instance names the CLI owns, which a manifest may not claim.

    They are created by the bootstrap itself; treating one as user-declared
    would make it eligible for destruction the moment it is absent from the
    manifest. Imported lazily -- ``bom_seeder`` imports ``floci_deployer``,
    which is far too heavy for a validator module import.
    """
    from ..bom_seeder import CORE_INSTANCE_NAME, LOCAL_CORE_BOM
    from ..dev_db import DB_OWNER_INSTANCE

    names = {CORE_INSTANCE_NAME, DB_OWNER_INSTANCE}
    names.update(
        e["repo_instance_name"] for e in LOCAL_CORE_BOM if e.get("repo_instance_name")
    )
    return names


def _validate_dependencies(deps: Any, where: str, errors: List[str]) -> None:
    """``dependencies`` must be a mapping of role -> instance name(s).

    The change_set schema types each value as ``string`` or ``array<string>``
    (variant C in ``hmd_lang_deployment/change_set.hms``); anything else fails
    server-side validation with a message that does not name the offender.
    """
    if not isinstance(deps, dict):
        errors.append(
            f"{where}: 'dependencies' must be a mapping, got {type(deps).__name__}"
        )
        return
    for role, target in deps.items():
        if isinstance(target, str):
            continue
        if isinstance(target, list) and all(isinstance(t, str) for t in target):
            continue
        errors.append(
            f"{where}: dependency '{role}' must be an instance name or a list of "
            f"instance names, got {type(target).__name__}"
        )


def _validate_repo(repo, index: int, seen: Dict[str, int], errors: List[str]) -> None:
    where = f"repos[{index}]"

    if not repo.instance_name:
        errors.append(f"{where}: 'instance_name' is required")
    if not repo.repo_class_name:
        errors.append(f"{where}: 'repo_class_name' is required")

    if repo.instance_name:
        where = f"repos[{index}] ('{repo.instance_name}')"
        if repo.instance_name in seen:
            errors.append(
                f"{where}: duplicate instance_name, already declared at "
                f"repos[{seen[repo.instance_name]}]"
            )
        else:
            seen[repo.instance_name] = index
        if repo.instance_name in _protected_instance_names():
            errors.append(
                f"{where}: '{repo.instance_name}' is reserved for the local "
                "NeuronSphere core and cannot be declared as a repo instance"
            )

    if repo.source_type not in KNOWN_SOURCE_TYPES:
        errors.append(
            f"{where}: unknown source type '{repo.source_type}'; expected one of "
            f"{', '.join(sorted(KNOWN_SOURCE_TYPES))}"
        )
    elif repo.source_type not in SUPPORTED_SOURCE_TYPES:
        errors.append(
            f"{where}: source type '{repo.source_type}' is not supported yet; "
            f"use 'type: {SOURCE_LOCAL}' to deploy from a local working tree"
        )
    elif repo.repo_class_name:
        # A local instance is deployed by mounting its working tree into the
        # projectbuilder container, so the tree has to exist before `up` starts.
        path = repo.resolved_repo_path()
        if path is None:
            errors.append(
                f"{where}: cannot locate a local working tree -- set "
                "'source.path' or export HMD_REPO_HOME"
            )
        elif not os.path.isdir(path):
            errors.append(f"{where}: local repo path does not exist: {path}")

    if not isinstance(repo.instance_configuration, dict):
        errors.append(
            f"{where}: 'instance_configuration' must be a mapping, got "
            f"{type(repo.instance_configuration).__name__}"
        )
    _validate_dependencies(repo.dependencies, where, errors)


def validate_manifest(manifest) -> List[str]:
    """Return a list of human-readable problems; empty means valid.

    :param manifest: an :class:`env_manifest.EnvManifest`
    """
    errors: List[str] = []

    if not manifest.name:
        errors.append("'name' is required")

    try:
        version = int(manifest.version)
    except (TypeError, ValueError):
        errors.append(f"'version' must be an integer, got {manifest.version!r}")
    else:
        if version != MANIFEST_VERSION:
            errors.append(
                f"unsupported manifest version {version}; this CLI understands "
                f"version {MANIFEST_VERSION}"
            )

    if manifest.plugins is not None:
        for i, name in enumerate(manifest.plugins):
            if not isinstance(name, str) or not name.strip():
                errors.append(f"plugins[{i}]: must be a non-empty plugin name")
        duplicates = {n for n in manifest.plugins if manifest.plugins.count(n) > 1}
        for name in sorted(duplicates):
            errors.append(f"plugins: '{name}' is listed more than once")

    for name, config in manifest.plugin_config.items():
        if not isinstance(config, dict):
            errors.append(
                f"plugin_config['{name}']: must be a mapping, got "
                f"{type(config).__name__}"
            )
        elif manifest.plugins is not None and name not in manifest.plugins:
            # Not fatal server-side, but it means the config silently does
            # nothing -- almost always a typo in one of the two names.
            errors.append(
                f"plugin_config['{name}']: '{name}' is not in the 'plugins' "
                "allow-list, so this configuration would be ignored"
            )

    seen: Dict[str, int] = {}
    for i, repo in enumerate(manifest.repos):
        _validate_repo(repo, i, seen, errors)

    return errors
