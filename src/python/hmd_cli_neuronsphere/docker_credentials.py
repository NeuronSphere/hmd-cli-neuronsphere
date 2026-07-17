"""Harvest the host's Docker registry credentials into a dockerconfigjson.

Used to seed the local ``hmd-docker-repo-secret`` (see
``bom_seeder._resolve_bom``) so local k3s can pull private images (e.g.
``ghcr.io/hmdlabs/*``) the same way cloud does -- by reusing whatever the
developer already authenticated on the host via ``docker login``, instead of
inventing a separate local-only credential mechanism.

Best-effort throughout: a missing Docker config, a missing credential-helper
binary, or a failed lookup for one registry never raises -- it's logged (host
name only, never a resolved secret value) and skipped, mirroring this
codebase's "repo mounts are best-effort" convention.
"""

import base64
import json
import os
import subprocess
from typing import Dict, Optional

from cement import minimal_logger

logger = minimal_logger("docker_credentials")


def _docker_config_path() -> str:
    """The host's Docker config path, honoring $DOCKER_CONFIG like the Docker CLI."""
    docker_config_dir = os.environ.get("DOCKER_CONFIG") or os.path.join(
        os.path.expanduser("~"), ".docker"
    )
    return os.path.join(docker_config_dir, "config.json")


def _resolve_via_helper(helper: str, host: str) -> Optional[Dict[str, str]]:
    """Run ``docker-credential-<helper> get`` for ``host``. ``None`` on any failure."""
    try:
        proc = subprocess.run(
            [f"docker-credential-{helper}", "get"],
            input=host,
            capture_output=True,
            text=True,
            timeout=10,
        )
        if proc.returncode != 0:
            return None
        data = json.loads(proc.stdout)
        username, secret = data.get("Username"), data.get("Secret")
        if not username or not secret:
            return None
        return {"username": username, "secret": secret}
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError):
        return None


def local_docker_config_json() -> Optional[str]:
    """A dockerconfigjson string built from the host's Docker credential store.

    Covers every registry host the developer is already authenticated to (not
    just ``ghcr.io``), resolving each via its literal ``auths[host].auth`` entry
    if present, else via the registry's credential helper (``credHelpers[host]``
    or the top-level ``credsStore`` -- the common case on macOS/Docker Desktop,
    where ``auths`` entries exist but carry no literal ``auth`` field).

    :returns: A JSON string ``{"auths": {host: {"auth": "..."}, ...}}``, or
        ``None`` if the host has no Docker config or nothing resolved (e.g. the
        developer never ran ``docker login``) -- callers must treat that as
        "skip," not an error.
    """
    try:
        with open(_docker_config_path()) as f:
            config = json.load(f)
    except (OSError, json.JSONDecodeError):
        return None

    auths = config.get("auths") or {}
    cred_helpers = config.get("credHelpers") or {}
    default_helper = config.get("credsStore")

    resolved: Dict[str, Dict[str, str]] = {}
    for host in set(auths.keys()) | set(cred_helpers.keys()):
        literal_auth = (auths.get(host) or {}).get("auth")
        if literal_auth:
            resolved[host] = {"auth": literal_auth}
            continue
        helper = cred_helpers.get(host) or default_helper
        if not helper:
            continue
        creds = _resolve_via_helper(helper, host)
        if not creds:
            logger.warning(f"Could not resolve Docker credentials for '{host}'")
            continue
        auth = base64.b64encode(
            f"{creds['username']}:{creds['secret']}".encode()
        ).decode()
        resolved[host] = {"auth": auth}

    if not resolved:
        return None

    logger.info(f"Resolved local Docker credentials for: {', '.join(sorted(resolved))}")
    return json.dumps({"auths": resolved})
