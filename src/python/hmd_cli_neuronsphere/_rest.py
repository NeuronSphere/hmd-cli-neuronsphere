"""Shared REST helpers for ms-deployment CRUD/apiop calls.

Used by both bom_seeder (extend mode) and hmdms_seeder (HMDMS service plugins).
Always uses hmd-ms-base CRUD endpoints (PUT /api/<entity>) — never GraphQL.
"""

from typing import Dict, Optional

import requests
from cement import minimal_logger

logger = minimal_logger("ms_deployment_rest")


def put_entity(base_url: str, entity_type: str, data: Dict) -> Dict:
    """Create/upsert an entity via the CRUD PUT endpoint.

    :param base_url: ms-deployment base URL (e.g. http://localhost/hmd_ms_deployment)
    :param entity_type: Fully-qualified entity name (e.g. hmd_lang_deployment.repo_class)
    :param data: Entity attributes
    :returns: Response JSON
    """
    url = f"{base_url}/api/{entity_type}"
    resp = requests.put(url, json=data, timeout=30)
    resp.raise_for_status()
    return resp.json()


def put_entity_idempotent(
    base_url: str, entity_type: str, data: Dict
) -> Optional[Dict]:
    """Like put_entity but tolerates 4xx conflict-style responses.

    Returns the response JSON on success, None on conflict.
    """
    url = f"{base_url}/api/{entity_type}"
    try:
        resp = requests.put(url, json=data, timeout=30)
        if resp.status_code in (200, 201):
            return resp.json()
        if resp.status_code in (409, 422):
            logger.debug(
                f"Idempotent PUT skipped (status={resp.status_code}): {entity_type}"
            )
            return None
        resp.raise_for_status()
    except requests.RequestException as e:
        logger.warning(f"Idempotent PUT failed for {entity_type}: {e}")
        return None
    return None


def post_apiop(base_url: str, operation: str, payload: Dict = None) -> Dict:
    """Call an apiop endpoint."""
    url = f"{base_url}/apiop/{operation}"
    if payload is not None:
        resp = requests.post(url, json=payload, timeout=60)
    else:
        resp = requests.post(url, timeout=60)
    resp.raise_for_status()
    return resp.json()


def post_apiop_idempotent(
    base_url: str, operation: str, payload: Dict = None
) -> Optional[Dict]:
    """Call an apiop, tolerating conflict-style responses."""
    url = f"{base_url}/apiop/{operation}"
    try:
        if payload is not None:
            resp = requests.post(url, json=payload, timeout=60)
        else:
            resp = requests.post(url, timeout=60)
        if resp.status_code in (200, 201):
            return resp.json()
        if resp.status_code in (409, 422):
            logger.debug(f"Idempotent apiop skipped: {operation}")
            return None
        resp.raise_for_status()
    except requests.RequestException as e:
        logger.warning(f"Idempotent apiop failed for {operation}: {e}")
        return None
    return None
