"""Diff an environment's declared desired state against what is deployed.

``hmd neuronsphere up`` used to be additive-only: :func:`bom_seeder.compute_new_bom_entries`
found entries missing from the deployment graph and nothing else, so disabling a
plugin or deleting a declared repo instance left its ``repo_instance`` ``DEPLOYED``
and its workloads running forever. This module computes the full three-way plan --
what to add, what to redeploy, and what to destroy -- so an environment can actually
converge on its manifest.

Three sources feed the diff:

- **Desired**: the compiled change_set definition
  (:func:`change_set_builder.full_definition`).
- **Actual**: the deployment graph, via :func:`bom_seeder._repo_instance_status`,
  already scoped to this environment by ``deployment_id``. This is authoritative
  for what exists.
- **Last applied**: a snapshot written next to the environment's state after each
  successful apply. The graph records *that* an instance is deployed but comparing
  its stored configuration is expensive and lossy, so drift detection compares
  entry digests against this snapshot instead.

A missing snapshot means "no drift information", not "everything changed": an
environment bootstrapped before snapshots existed reports its deployed entries as
unchanged rather than proposing a wholesale redeploy.
"""

import json
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, List, Optional

from cement import minimal_logger

logger = minimal_logger("env_reconcile")

SNAPSHOT_VERSION = 1
SNAPSHOT_FILENAME = "applied-changeset.json"

# A deployment whose most recent status is this is considered live. Everything
# else -- absent, FAILED, SKIPPED, DESTROYED -- is eligible for (re)deployment,
# matching compute_new_bom_entries' long-standing rule.
DEPLOYED = "DEPLOYED"


@dataclass
class ReconcilePlan:
    """What ``up`` would do to make this environment match its manifest."""

    add: List[Dict] = field(default_factory=list)
    change: List[Dict] = field(default_factory=list)
    remove: List[str] = field(default_factory=list)
    unchanged: List[str] = field(default_factory=list)
    desired: List[Dict] = field(default_factory=list)
    # True when the plan could not be computed (the graph was unreachable), as
    # opposed to a genuinely empty plan. Callers must not prune on a stale plan.
    degraded: bool = False

    @property
    def has_deployments(self) -> bool:
        return bool(self.add or self.change)

    @property
    def is_empty(self) -> bool:
        return not (self.add or self.change or self.remove)

    def summary(self) -> str:
        """A one-line ``+a ~c -r`` summary."""
        return (
            f"+{len(self.add)} add, ~{len(self.change)} change, "
            f"-{len(self.remove)} remove, {len(self.unchanged)} unchanged"
        )

    def render(self, indent: str = "  ") -> List[str]:
        """Human-readable plan lines, most consequential first."""
        lines = []
        for name in self.remove:
            lines.append(f"{indent}- {name} (declared no longer; destroy)")
        for entry in self.add:
            lines.append(
                f"{indent}+ {entry['repo_instance_name']} "
                f"({entry['repo_class_name']}@{entry['repo_class_version']})"
            )
        for entry in self.change:
            lines.append(
                f"{indent}~ {entry['repo_instance_name']} "
                f"({entry['repo_class_name']}@{entry['repo_class_version']}; "
                "configuration changed)"
            )
        return lines


# ---------------------------------------------------------------------------
# Protected instances
# ---------------------------------------------------------------------------


def protected_instance_names() -> set:
    """Instances the CLI owns, which reconcile must never propose destroying.

    ``local-neuronsphere`` produces every core Resource the rest of the graph
    depends on, and ``local-databases`` owns the dev-database Resources. Neither
    is ever declared in a manifest, so a naive "deployed but not desired" rule
    would list both for destruction on the first prune.
    """
    from .bom_seeder import CORE_INSTANCE_NAME, LOCAL_CORE_BOM
    from .dev_db import DB_OWNER_INSTANCE

    names = {CORE_INSTANCE_NAME, DB_OWNER_INSTANCE}
    names.update(
        e["repo_instance_name"] for e in LOCAL_CORE_BOM if e.get("repo_instance_name")
    )
    return names


# ---------------------------------------------------------------------------
# Applied-state snapshot
# ---------------------------------------------------------------------------


def snapshot_path(env) -> Optional[Path]:
    """Where this environment's last-applied definition is recorded.

    Lives under the environment's own state directory, so ``down --purge`` --
    which already removes that directory -- correctly resets drift tracking to
    "no information" along with the rest of the environment's state.
    """
    if env is None:
        return None
    try:
        return env.state_path / SNAPSHOT_FILENAME
    except (AttributeError, TypeError):
        return None


def load_snapshot(env) -> Dict[str, str]:
    """Map ``repo_instance_name`` -> entry digest from the last successful apply.

    An empty map means "no drift information available" and is handled as such
    by :func:`compute_plan`; it never means "everything changed".
    """
    path = snapshot_path(env)
    if path is None or not path.is_file():
        return {}
    try:
        doc = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as e:
        logger.warning(f"Could not read applied-changeset snapshot {path}: {e}")
        return {}
    entries = doc.get("entries") or []
    return {
        e["repo_instance_name"]: e["hash"]
        for e in entries
        if e.get("repo_instance_name") and e.get("hash")
    }


def _snapshot_entry(entry: Dict, digest: str) -> Dict:
    return {
        "repo_instance_name": entry.get("repo_instance_name"),
        "repo_class_name": entry.get("repo_class_name"),
        "repo_class_version": entry.get("repo_class_version"),
        "hash": digest,
    }


def _write_snapshot(env, entries: List[Dict]) -> Optional[Path]:
    """Atomically persist snapshot entries.

    Best-effort: failing to write degrades drift detection to "no information"
    on the next run, which is safe, so it must never fail a deploy that already
    succeeded.
    """
    path = snapshot_path(env)
    if path is None:
        return None
    doc = {"version": SNAPSHOT_VERSION, "entries": entries}
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_suffix(".json.tmp")
        tmp.write_text(json.dumps(doc, indent=2))
        os.replace(tmp, path)
    except OSError as e:
        logger.warning(f"Could not write applied-changeset snapshot {path}: {e}")
        return None
    return path


def write_snapshot(env, definition: List[Dict]) -> Optional[Path]:
    """Record the whole ``definition`` as successfully applied.

    Used after a full bootstrap, where every entry really was deployed.
    """
    from .change_set_builder import entry_hash

    return _write_snapshot(env, [_snapshot_entry(e, entry_hash(e)) for e in definition])


def merge_snapshot(env, definition: List[Dict], applied: List[Dict]) -> Optional[Path]:
    """Record only the entries that were actually applied this run.

    A partial apply -- ``--upgrade`` deploying just the delta, or a run where
    some nodes failed -- must not record entries that never deployed, or the
    next run would consider them settled and never retry them. Entries not in
    this apply keep whatever digest the snapshot already had; entries that have
    neither are omitted, so they keep reading as "not yet applied".
    """
    from .change_set_builder import entry_hash

    if not applied:
        return None
    applied_names = {e.get("repo_instance_name") for e in applied}
    previous = load_snapshot(env)

    entries = []
    for entry in definition:
        name = entry.get("repo_instance_name")
        if name in applied_names:
            entries.append(_snapshot_entry(entry, entry_hash(entry)))
        elif name in previous:
            entries.append(_snapshot_entry(entry, previous[name]))
    return _write_snapshot(env, entries)


# ---------------------------------------------------------------------------
# The plan
# ---------------------------------------------------------------------------


def compute_plan(base_url: str, env=None, manifest=None) -> ReconcilePlan:
    """Diff the desired change_set definition against the deployment graph.

    Fail-safe: if the graph cannot be queried the plan comes back ``degraded``
    with nothing to do, so a transient ms-deployment outage can never be read as
    "everything was removed from the manifest".
    """
    import requests

    from . import bom_seeder
    from .change_set_builder import entry_hash, full_definition

    desired = full_definition(env=env, manifest=manifest)
    plan = ReconcilePlan(desired=desired)

    try:
        status_by_name = bom_seeder._repo_instance_status(base_url, env)
    except (requests.RequestException, ValueError, KeyError) as e:
        logger.warning(f"Could not compute the reconcile plan: {e}")
        plan.degraded = True
        return plan

    snapshot = load_snapshot(env)
    desired_names = set()

    for entry in desired:
        name = entry.get("repo_instance_name")
        desired_names.add(name)
        if status_by_name.get(name) != DEPLOYED:
            plan.add.append(entry)
            continue
        recorded = snapshot.get(name)
        if recorded is not None and recorded != entry_hash(entry):
            plan.change.append(entry)
        else:
            plan.unchanged.append(name)

    protected = protected_instance_names()
    plan.remove = sorted(
        name
        for name, status in status_by_name.items()
        if status == DEPLOYED and name not in desired_names and name not in protected
    )
    return plan
