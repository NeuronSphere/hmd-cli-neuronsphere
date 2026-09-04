"""Compile an environment manifest into a ``hmd_lang_deployment.change_set`` definition.

The local BOM entry shape has always *been* a change_set entry -- ``seed_bom``
built one from the other inline. This module makes that explicit and makes the
change_set definition the **declared, diffable desired state** of an
environment rather than a by-product of whichever plugin packages happen to be
installed.

Entries are emitted in variant C of the ``change_set.definition`` schema
(``hmd-lang-deployment/src/schemas/hmd_lang_deployment/change_set.hms``). That
schema is a three-way ``oneOf`` with ``additionalProperties: false`` on every
branch, and variant C requires *all* of::

    deployment_id, repo_instance_name, repo_class_name,
    repo_class_version, instance_configuration, dependencies

so an extra key -- or a missing ``dependencies`` -- fails all three branches
with a validation error that does not name the offending entry.
:func:`normalize_entry` is the single place that guarantees the shape.
"""

import hashlib
import json
import os
from typing import Any, Dict, List, Optional

from cement import minimal_logger

logger = minimal_logger("change_set_builder")

# Exactly the keys allowed in a variant-C change_set definition entry.
CHANGE_SET_ENTRY_KEYS = (
    "deployment_id",
    "repo_instance_name",
    "repo_class_name",
    "repo_class_version",
    "instance_configuration",
    "dependencies",
)


def normalize_entry(
    entry: Dict[str, Any],
    repo_path: Optional[str] = None,
    shadow_batch: Optional[Dict[str, Any]] = None,
    warned_repos: Optional[set] = None,
) -> Dict[str, Any]:
    """Coerce a BOM-ish dict into exactly one variant-C change_set entry.

    Resolves ``repo_class_version`` the way the rest of the CLI does -- the
    artifact bundled with the plugin package wins, then the declared version,
    and a local working tree only when the developer asks for it via
    ``HMD_LOCAL_VERSION_<REPO_CLASS>`` or
    ``HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS``; see
    :func:`bom_seeder.resolve_repo_version` -- and defaults the two required
    mappings.

    :param repo_path: The working tree to read repo metadata from, for a repo
        declared with an explicit ``source.path`` outside ``HMD_REPO_HOME``. It
        selects *which* tree is consulted, not whether the tree's VERSION wins.
    :param shadow_batch: see :func:`bom_seeder.resolve_repo_version`. Pass the
        same dict across every entry in one definition so version-resolution
        warnings for the whole definition collapse into one combined warning
        rather than one per entry.
    :param warned_repos: see :func:`bom_seeder.resolve_repo_version`. Pass the
        same set across every entry in one definition for the same reason.
    """
    from .bom_seeder import _get_repo_version

    repo_class_name = entry.get("repo_class_name")
    return {
        "deployment_id": entry.get("deployment_id", "local"),
        "repo_instance_name": entry.get("repo_instance_name"),
        "repo_class_name": repo_class_name,
        "repo_class_version": _get_repo_version(
            repo_class_name,
            bom_version=entry.get("repo_class_version"),
            repo_path=repo_path,
            shadow_batch=shadow_batch,
            warned_repos=warned_repos,
        ),
        "instance_configuration": dict(entry.get("instance_configuration") or {}),
        "dependencies": dict(entry.get("dependencies") or {}),
    }


def entry_from_declaration(repo) -> Dict[str, Any]:
    """Turn a manifest :class:`env_manifest.RepoDeclaration` into a raw BOM entry."""
    return {
        "repo_instance_name": repo.instance_name,
        "repo_class_name": repo.repo_class_name,
        "deployment_id": "local",
        "repo_class_version": repo.version,
        "instance_configuration": dict(repo.instance_configuration or {}),
        "dependencies": dict(repo.dependencies or {}),
    }


def declared_repo_paths(manifest) -> Dict[str, str]:
    """Map ``repo_class_name`` -> working tree, for manifest repos with a path.

    Only entries whose resolved path differs from the ``HMD_REPO_HOME``
    convention are returned: the runner already finds those on its own, and a
    smaller map keeps the override explicit about what it is actually overriding.
    """
    paths: Dict[str, str] = {}
    if manifest is None:
        return paths
    repo_home = os.environ.get("HMD_REPO_HOME", "")
    for repo in manifest.repos:
        path = repo.resolved_repo_path()
        if not path:
            continue
        default = os.path.join(repo_home, repo.repo_class_name) if repo_home else None
        if default and os.path.normpath(default) == os.path.normpath(path):
            continue
        paths[repo.repo_class_name] = path
    return paths


def build_definition(
    env=None,
    manifest=None,
    _shadow_batch: Optional[Dict[str, Any]] = None,
    _warned_repos: Optional[set] = None,
) -> List[Dict[str, Any]]:
    """The Phase B change_set definition for ``env`` under ``manifest``.

    Phase B is everything except the core ``local-neuronsphere`` instance, which
    the bootstrap applies first on its own (see ``environments._run_full_bootstrap``).

    Merge order -- first occurrence of a ``repo_instance_name`` wins, matching
    ``bom_seeder._dedupe_bom``'s keep-first rule:

    1. the manifest's declared ``repos`` (the user's explicit intent overrides
       anything a plugin or built-in contributes under the same name),
    2. a legacy ``HMD_LOCAL_BOM_FILE``, if still set,
    3. the built-in ``LOCAL_BOM``,
    4. ``EXT_SECRETS_BOM`` unless opted out,
    5. entries contributed by the enabled plugin packages.

    The result is de-duped, topologically sorted (so cross-plugin dependency
    edges resolve regardless of ``entry_points()`` scan order) and stamped with
    the environment's ``deployment_id``.

    :param _shadow_batch: Internal -- lets :func:`full_definition` share one
        version-resolution warning batch across this call and its own core-BOM
        normalization, so the two combine into a single flushed warning
        instead of two. Omit for a standalone call, which collects and
        flushes its own batch.
    :param _warned_repos: Internal counterpart to ``_shadow_batch`` for the
        no-bundled-artifact-or-declared-version warning; same sharing rule.
    """
    from . import bom_seeder as b

    repo_paths = declared_repo_paths(manifest)

    entries: List[Dict[str, Any]] = []
    declared_paths_by_class: Dict[str, Optional[str]] = {}
    if manifest is not None:
        for repo in manifest.repos:
            entries.append(entry_from_declaration(repo))
            declared_paths_by_class[repo.repo_class_name] = repo.resolved_repo_path()

    bom_file = os.environ.get("HMD_LOCAL_BOM_FILE")
    if bom_file:
        logger.debug(
            f"Merging legacy BOM file into the manifest definition: {bom_file}"
        )
        entries.extend(b.load_bom_from_file(bom_file))

    entries.extend(b.LOCAL_BOM)

    if not b._is_falsy(os.environ.get("HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS")):
        entries.extend(b.EXT_SECRETS_BOM)

    enabled = manifest.enabled_plugins() if manifest is not None else None
    plugin_config = manifest.plugin_config if manifest is not None else None
    plugin_entries = b._collect_plugin_bom_entries(
        enabled=enabled, config=plugin_config
    )
    if plugin_entries:
        logger.debug(
            f"Appending {len(plugin_entries)} BOM entrie(s) from enabled plugins"
        )
    entries.extend(plugin_entries)

    # Enabled plugin packages still map `database-instance`/`eks-cluster`/
    # `graph-db` (etc.) dependencies at `CORE_INSTANCE_NAME` from before those
    # roles were split into their own Phase A/lazy instances -- see
    # `bom_seeder.resolve_plugin_bom`, which normalizes the exact same way for
    # `hmd neuronsphere up`'s first bootstrap. This definition feeds the
    # reconcile diff and the delta changeset a later `up` seeds (`env_reconcile`
    # / `_reconcile_environment`), so skipping it here left a plain restart
    # re-seeding an entry like `trino` with its `graph-db` role still pointed at
    # `local-neuronsphere` -- which ms-deployment then rejects.
    if b.graph_enabled() and b.bom_requires_graph(entries):
        entries.append(b.graph_bom_entry(env))
    b._repoint_database_instance(entries)
    b._repoint_eks_cluster_dependency(entries)
    b._repoint_graph_database(entries)

    # Shared across every entry in this definition so a repo class whose
    # version resolution warns (shadowed by a local tree, or neither bundled
    # nor declared) reports once for the whole definition -- not once per
    # entry, and not once per repo class named by more than one entry.
    own_batch = {} if _shadow_batch is None else _shadow_batch
    own_warned: set = set() if _warned_repos is None else _warned_repos

    normalized = [
        normalize_entry(
            e,
            repo_path=declared_paths_by_class.get(e.get("repo_class_name")),
            shadow_batch=own_batch,
            warned_repos=own_warned,
        )
        for e in entries
    ]
    # ext-secrets' local ClusterSecretStore needs the host's Docker credentials
    # injected into its instance_configuration; do it after normalization so the
    # mapping is guaranteed to exist, and before hashing so a credentials change
    # is visible to the reconcile diff.
    b._inject_docker_credentials(normalized)
    # Same placement, same reason, for the account those stores sign their lookups
    # with. `seed_bom` injects it too, so this is not what makes a deploy correct
    # -- it is what keeps this definition (which `full_definition` feeds to
    # `env_reconcile.compute_plan` as desired state) hashing to the same thing
    # that was actually seeded. Omitting it left ext-secrets permanently drifted.
    b._inject_floci_account(normalized, env)

    definition = b.scope_bom_entries(b._topo_sort_bom(b._dedupe_bom(normalized)), env)
    logger.debug(
        f"Built change_set definition with {len(definition)} entrie(s) "
        f"({len(repo_paths)} declared repo path override(s))"
    )
    if _shadow_batch is None:
        b._flush_shadowed_local_warnings(own_batch)
    return definition


def full_definition(env=None, manifest=None) -> List[Dict[str, Any]]:
    """:func:`build_definition` plus the core Phase A instance.

    This is the complete desired state of an environment -- what the reconcile
    diff compares the deployment graph against.
    """
    from . import bom_seeder as b

    shadow_batch: Dict[str, Any] = {}
    warned_repos: set = set()
    core = [
        normalize_entry(e, shadow_batch=shadow_batch, warned_repos=warned_repos)
        for e in b.LOCAL_CORE_BOM
    ]
    combined = core + build_definition(
        env=env,
        manifest=manifest,
        _shadow_batch=shadow_batch,
        _warned_repos=warned_repos,
    )
    b._flush_shadowed_local_warnings(shadow_batch)
    return b.scope_bom_entries(b._topo_sort_bom(b._dedupe_bom(combined)), env)


def entry_hash(entry: Dict[str, Any]) -> str:
    """A stable digest of one entry, used to detect configuration drift.

    ``deployment_id`` is excluded: it is stamped per environment and is not part
    of what the user declared, so including it would make every entry read as
    "changed" the moment an environment is renamed or re-slugged.
    """
    payload = {k: entry.get(k) for k in CHANGE_SET_ENTRY_KEYS if k != "deployment_id"}
    return hashlib.sha256(
        json.dumps(payload, sort_keys=True, default=str).encode()
    ).hexdigest()


def definition_hash(definition: List[Dict[str, Any]]) -> str:
    """A stable digest of a whole definition, order-independent."""
    return hashlib.sha256(
        "".join(sorted(entry_hash(e) for e in definition)).encode()
    ).hexdigest()
