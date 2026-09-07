"""Ad-hoc dev-loop deploy-config resolver.

Builds the ``--config-file`` a developer passes to ``hmd deploy --local`` for a repo
that is NOT a persisted RepoInstance. Given the repo's ``meta-data/manifest.json``
``deploy`` block (its ``default_configuration`` + ``dependencies``), it resolves each
dependency role against the Resources the local NeuronSphere already provides and
returns the same ``{...default_configuration, dependencies, details}`` shape
ms-deployment bakes for a real deploy -- WITHOUT persisting the consumer or touching
the ChangeSet graph.

Resolution is **definition-driven** (NERD0004 SPEC0008): a role is resolved from its
own declared ``resource`` requirement -- the local Resources whose ResourceDefinition
matches the required type (or a subtype), honoring ``version_spec``/``tag_selector``.
A role with no resolvable resource requirement is emitted as an editable *scaffold*
(``{"_scaffold": True, ...}``); resolution is best-effort and never raises.
"""

import logging
from copy import deepcopy
from typing import Dict, List, Optional

from .resource_information import ResourceInformation, parse_tag_selector

LOGGER = logging.getLogger(f"HMD.{__name__}")

_LOCAL_SELECTOR = {"environment": "local"}


def _scaffold(role: str, dep: Optional[Dict]) -> Dict:
    """A placeholder role config the developer edits (no matching local Resource)."""
    dep = dep or {}
    return {
        "_scaffold": True,
        "role": role,
        "repo_class_name": dep.get("repo_class_name"),
        "version_spec": dep.get("version_spec"),
        "hmd_resources": [],
    }


def generate_dev_deployment_config(
    client,
    env_info,
    *,
    instance_name: str,
    default_configuration: Optional[Dict],
    dependencies: Optional[Dict],
    bindings: Optional[Dict] = None,
) -> Dict:
    """Resolve a dev deploy config from a repo's manifest ``deploy`` block.

    :param client: HmdLangDeploymentClient.
    :param env_info: A loaded ``EnvironmentInformation`` for the target environment.
    :param instance_name: Name to stamp as the top-level ``instance_name``.
    :param default_configuration: The repo manifest's ``deploy.default_configuration``.
    :param dependencies: The repo manifest's ``deploy.dependencies``
        (``{role: {repo_class_name, version_spec, resource?, ...}}``).
    :param bindings: Accepted for signature stability; unused -- resolution is driven
        by each role's declared ``resource`` requirement, not a caller-supplied table.
    :returns: ``{...default_configuration, instance_name, dependencies, details}``.
    """
    ri = ResourceInformation(client)

    config: Dict = dict(deepcopy(default_configuration or {}))
    config["instance_name"] = instance_name
    config["dependencies"] = {}

    for role, dep in (dependencies or {}).items():
        try:
            role_cfg = _resolve_role(ri, role, dep)
        except Exception as e:  # never let one role break the whole config
            LOGGER.warning("dev-config: role %s unresolved: %s", role, e)
            role_cfg = _scaffold(role, dep)
        config["dependencies"][role] = role_cfg

    config["details"] = {}
    return config


def _resolve_role(ri, role, dep) -> Dict:
    """Resolve a role from its declared resource requirement (SPEC0008).

    Definition-driven: when the manifest dependency declares an authoritative
    ``resource`` block, supply the local Resources that satisfy that resource type
    (the exact ResourceDefinition or any subtype, honoring ``version_spec`` and
    ``tag_selector``). A role without a resolvable resource requirement is left as
    an editable scaffold for the developer to fill in.
    """
    resource = dep.get("resource") if isinstance(dep, dict) else None
    if resource and resource.get("resource_definition_name"):
        matches = _match_local_resources(ri, resource)
        if matches:
            return {"hmd_resources": matches}
    return _scaffold(role, dep)


def _match_local_resources(ri, resource: Dict) -> List[Dict]:
    """Local Resources whose ResourceDefinition satisfies ``resource`` (SPEC0008).

    Accepts the exact ``(namespace, name, version)`` and any subtype (a definition
    that ``isa`` the target), filtered by ``version_spec`` and ``tag_selector``.
    Reuses ``ResourceInformation`` primitives so matching mirrors the apply path.
    """
    targets = ri._resolve_target_definitions(
        resource["resource_namespace"],
        resource["resource_definition_name"],
        resource.get("version"),
        resource.get("version_spec"),
    )
    accepted = set()
    for t in targets:
        accepted.add((t.resource_namespace, t.resource_definition_name, t.version))
        for d in ri._get_descendants(t):
            accepted.add((d.resource_namespace, d.resource_definition_name, d.version))
    if not accepted:
        return []

    tag_selector = resource.get("tag_selector")
    selector = parse_tag_selector(tag_selector) if tag_selector else []

    matches: List[Dict] = []
    for r in ri.find_resources_by_selector(_LOCAL_SELECTOR):
        if selector and not ri._resource_satisfies_selector(r, selector):
            continue
        serialized = ri.serialize_resource(r)
        rd = serialized.get("resource_definition") or {}
        key = (
            rd.get("resource_namespace"),
            rd.get("resource_definition_name"),
            rd.get("version"),
        )
        if key in accepted:
            matches.append(serialized)
    return matches
