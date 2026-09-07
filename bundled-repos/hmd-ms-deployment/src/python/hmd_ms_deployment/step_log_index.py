"""Resolve Argo workflow steps to their archived pod logs in S3.

Argo garbage-collects Workflow objects after a few days, but the pod logs it
archived to S3 stay there indefinitely. That makes the log the only durable
place a "which step ran which RepoInstance" mapping can live, so
``DeployBase.deploy_node`` echoes a ``##HMD_STEP`` marker as the first line of
every step's stdout and this module reads it back.

Nothing here talks to the Argo API, so the mapping resolves identically for a
workflow that is still running and for one that was collected months ago.
"""

import logging
import re
from typing import Dict, List, Optional, Set, Tuple

from botocore.exceptions import ClientError

logger = logging.getLogger(f"HMD.{__name__}")

# Emitted by DeployBase.deploy_node as the first line of each step's stdout.
# Keep in sync with the echo there.
MARKER_RE = re.compile(r"##HMD_STEP env=(\S+) instance=(\S+) rid=(\S+)")

# Pre-marker deployments still echoed "{repo_class}:{instance}: {version}" near
# the top of the log. It is less precise (no env, no RID) but lets deployments
# that ran before the marker existed still resolve their steps.
LEGACY_RE = re.compile(r"^(?P<repo_class>[^\s:]+):(?P<instance>[^\s:]+): (?P<version>\S+)$")

# How much of each pod log to read when looking for the marker. The marker is
# the first line, so this is generous; a ranged GET keeps the cost per pod tiny
# even for a multi-megabyte deploy log.
HEAD_BYTES = 2048


def list_pod_prefixes(
    s3_client,
    bucket: str,
    namespace: str,
    workflow_name: str,
    date_prefix: Optional[str] = None,
) -> Tuple[Set[str], Optional[str]]:
    """List the S3 "directory" prefix of every archived pod for a workflow.

    Argo archives logs as ``{namespace}/{YYYY-MM-DD}/{workflow_name}/{pod}/main.log``.
    When ``date_prefix`` is known (from ``ChangeSetDeployment.workflow_submitted_at``)
    this is a single delimited list; when it is not, every date directory is
    probed for the workflow, which is slower but still correct.

    :returns: ``(set of pod prefixes, resolved date directory or None)``.
    """
    paginator = s3_client.get_paginator("list_objects_v2")
    pod_prefixes: Set[str] = set()
    resolved_date = date_prefix

    if date_prefix:
        prefix = f"{namespace}/{date_prefix}/{workflow_name}/"
        for page in paginator.paginate(Bucket=bucket, Prefix=prefix, Delimiter="/"):
            for common_prefix in page.get("CommonPrefixes", []):
                pod_prefixes.add(common_prefix["Prefix"])
        return pod_prefixes, resolved_date

    # Without a date, search each date directory for this workflow.
    for page in paginator.paginate(Bucket=bucket, Prefix=f"{namespace}/", Delimiter="/"):
        for common_prefix in page.get("CommonPrefixes", []):
            date_dir = common_prefix["Prefix"]
            workflow_prefix = f"{date_dir}{workflow_name}/"
            check_response = s3_client.list_objects_v2(
                Bucket=bucket, Prefix=workflow_prefix, MaxKeys=1
            )
            if not check_response.get("Contents"):
                continue
            resolved_date = date_dir.split("/")[1]
            for inner_page in paginator.paginate(
                Bucket=bucket, Prefix=workflow_prefix, Delimiter="/"
            ):
                for cp in inner_page.get("CommonPrefixes", []):
                    pod_prefixes.add(cp["Prefix"])
            return pod_prefixes, resolved_date

    return pod_prefixes, resolved_date


def pod_name_from_prefix(pod_prefix: str) -> str:
    """``argo-aaa/2026-09-03/wf-abc/wf-abc-run-script-123/`` -> ``wf-abc-run-script-123``."""
    return pod_prefix.rstrip("/").split("/")[-1]


def parse_step_marker(log_head: str) -> Optional[Dict[str, Optional[str]]]:
    """Extract ``{env, instance, rid}`` from the head of a pod log.

    Prefers the explicit ``##HMD_STEP`` marker and falls back to the legacy
    ``{repo_class}:{instance}: {version}`` echo, which carries an instance name
    but no environment or RID. Returns None when neither is present.
    """
    match = MARKER_RE.search(log_head)
    if match:
        return {"env": match.group(1), "instance": match.group(2), "rid": match.group(3)}

    for line in log_head.splitlines():
        legacy = LEGACY_RE.match(line.strip())
        if legacy:
            return {
                "env": None,
                "instance": legacy.group("instance"),
                "rid": None,
            }

    return None


def build_step_pod_index(
    s3_client, bucket: str, pod_prefixes
) -> Dict[str, List[str]]:
    """Map each RepoInstance name to the pods that ran it, oldest first.

    One instance can map to several pods: the ``run-script`` template carries
    ``retryStrategy.limit: 2``, so a retried step archives a log per attempt.
    Callers that want "the" log for a step should take the last entry.

    A pod whose log is missing, unreadable, or carries no recognisable marker is
    skipped rather than failing the whole index -- a step that has not started
    yet simply has no log to identify it.
    """
    index: Dict[str, List[str]] = {}

    for pod_prefix in sorted(pod_prefixes):
        log_key = f"{pod_prefix}main.log"
        try:
            response = s3_client.get_object(
                Bucket=bucket, Key=log_key, Range=f"bytes=0-{HEAD_BYTES - 1}"
            )
            log_head = response["Body"].read().decode("utf-8", errors="replace")
        except ClientError as e:
            logger.debug("Could not read log head for %s: %s", log_key, e)
            continue
        except Exception as e:  # noqa: BLE001 - never let one bad pod break the index
            logger.warning("Unexpected error reading log head for %s: %s", log_key, e)
            continue

        marker = parse_step_marker(log_head)
        if not marker:
            logger.debug("No step marker in %s", log_key)
            continue

        index.setdefault(marker["instance"], []).append(pod_name_from_prefix(pod_prefix))

    return index
