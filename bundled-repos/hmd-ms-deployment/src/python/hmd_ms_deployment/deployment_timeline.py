"""Per-instance deployment timeline records for a ChangeSetDeployment.

Returns a flat list of RepoInstanceDeployment records (one per instance, per
environment) with start/end timestamps so consumers can render a Gantt chart.
"""

import logging
from typing import Dict, List, Optional

from hmd_graphql_client.relationship_support import RelationshipSupport
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient

logger = logging.getLogger(f"HMD.{__name__}")

TERMINAL_RID_STATUSES = {"DEPLOYED", "FAILED", "DESTROYED", "SKIPPED"}


def _isoformat(value) -> Optional[str]:
    if value is None:
        return None
    try:
        return value.isoformat()
    except AttributeError:
        return str(value)


def get_deployment_timeline_records(
    client: HmdLangDeploymentClient,
    rs: RelationshipSupport,
    csd_nid: str,
) -> Dict[str, List[Dict]]:
    """Build per-instance timeline records for a ChangeSetDeployment.

    For each ChangeSetEnvDeployment under the CSD, walk its
    RepoInstanceDeployment relationships and return one record per RID with
    {repo_instance_name, repo_class_name, environment, deployment_id, status,
     start, end}. Timestamps are ISO-8601 strings (or null).

    When ``rid.start`` is null, fall back to the CSD's ``deploy_time``; when
    ``rid.end`` is null on a terminal-status RID, fall back to the entity's
    ``_updated`` metadata timestamp. The ``start_is_fallback`` /
    ``end_is_fallback`` flags let consumers render approximate timestamps
    distinctly.

    :param client: The deployment client.
    :param rs: RelationshipSupport for entity resolution.
    :param csd_nid: The ChangeSetDeployment identifier.
    :returns: ``{"csd_id": csd_nid, "deploy_time": iso-str|None, "instances": [...]}``.
    """
    csd = client.get_change_set_deployment_hmd_lang_deployment(csd_nid)
    deploy_time_iso = _isoformat(getattr(csd, "deploy_time", None))

    instances: List[Dict] = []

    for csed_rel in client.get_from_change_set_deployment_has_change_set_env_deployment_hmd_lang_deployment(csd):
        csed = rs.ref_to(csed_rel)

        env_rels = client.get_from_change_set_env_deployment_has_environment_hmd_lang_deployment(csed)
        environment_type = rs.ref_to(env_rels[0]).type if env_rels else None

        for rid_rel in client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(csed):
            rid = rs.ref_to(rid_rel)

            ri_rels = client.get_to_repo_instance_has_repo_instance_deployment_hmd_lang_deployment(rid)
            instance_name = None
            repo_class_name = None
            if ri_rels:
                ri = rs.ref_from(ri_rels[0])
                instance_name = ri.name
                rc_rels = client.get_from_repo_instance_isa_repo_class_hmd_lang_deployment(ri)
                if rc_rels:
                    repo_class_name = rs.ref_to(rc_rels[0]).repo_class_name

            raw_start = _isoformat(rid.start)
            start = raw_start or deploy_time_iso

            raw_end = _isoformat(rid.end)
            end = raw_end
            status_upper = (rid.status or "").upper()
            if not end and status_upper in TERMINAL_RID_STATUSES:
                end = _isoformat(getattr(rid, "_updated", None))

            instances.append({
                "rid_nid": rid.identifier,
                "repo_instance_name": instance_name,
                "repo_class_name": repo_class_name,
                "environment": environment_type,
                "deployment_id": rid.deployment_id,
                "status": rid.status,
                "start": start,
                "end": end,
                "start_is_fallback": raw_start is None and start is not None,
                "end_is_fallback": raw_end is None and end is not None,
            })

    return {
        "csd_id": csd_nid,
        "deploy_time": deploy_time_iso,
        "instances": instances,
    }
