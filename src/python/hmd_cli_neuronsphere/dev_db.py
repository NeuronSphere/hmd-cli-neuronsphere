"""Local dev-loop database helpers.

Two flows for making a Postgres database available to a repo's ``hmd deploy --local``
dependency config (the ``db-credentials`` role):

1. ``provision_and_register_db`` -- create the DB/user via the running
   ``hmd_ms_dbaccount`` Lambda (local ``password == username`` convention) AND register
   it as a NERD ``database.neuronsphere.io/postgres`` Resource in the local resource
   store.
2. ``register_db_resource`` -- register a Resource for a DB created *without* dbaccount
   (developer brought their own), for flexibility.

The Resource is tagged ``environment=local`` and owned by a ``local-databases`` instance
of the core ``hmd-cli-neuronsphere`` RepoClass, so the dev-config resolver's
``db-credentials -> postgres`` binding finds it via ``find_resources_by_selector``.
"""

import os
from typing import Dict, Optional

from cement import minimal_logger

from . import _rest
from .bom_seeder import _get_repo_version
from .floci_deployer import (
    _local_db_secret_base,
    _post_create_db_account,
)

logger = minimal_logger("dev_db")

CORE_REPO_CLASS = "hmd-cli-neuronsphere"
DB_OWNER_INSTANCE = "local-databases"
POSTGRES_RESOURCE_DEFINITION = {
    "resource_namespace": "database.neuronsphere.io",
    "resource_definition_name": "postgres",
    "version": "0.1.0",
}


def _ms_deployment_url() -> str:
    return os.environ.get(
        "HMD_DEPLOYMENT_SERVICE_URL", "http://localhost/hmd_ms_deployment"
    )


def _ensure_db_owner_rid(base_url: str) -> str:
    """Ensure the env-linked ``local-databases`` owner instance and return its RID id.

    Owned by the core ``hmd-cli-neuronsphere`` RepoClass (all local default Resources
    are owned by one RepoClass). Idempotent: the RCV registration tolerates an existing
    version and ``register_deployed_instance`` reuses the instance if present.
    """
    version = _get_repo_version(CORE_REPO_CLASS)
    # Guarantee the RCV exists so register_deployed_instance can resolve it. Empty deps
    # is a no-op when `up` already registered this version (tolerate_exists).
    _rest.post_apiop_idempotent(
        base_url,
        "add_repo_class_version",
        {
            "repo_class_name": CORE_REPO_CLASS,
            "version": version,
            "dependencies": {},
            "default_configuration": {},
        },
    )
    result = _rest.post_apiop(
        base_url,
        "register_deployed_instance",
        {
            "environment": "local",
            "repo_class_name": CORE_REPO_CLASS,
            "version": version,
            "instance_name": DB_OWNER_INSTANCE,
            "deployment_id": "local",
            "instance_configuration": {},
        },
    )
    return result["repo_instance_deployment_id"]


def _postgres_resource(
    db_name: str, username: str, host: str, port: int, secret_name: Optional[str]
) -> Dict:
    """Build the ``postgres`` Resource dict (effective output = postgres + database)."""
    if not secret_name:
        secret_name = f"{_local_db_secret_base()}_{username}"
    return {
        "resource_name": f"local-db-{db_name}",
        "resource_definition": POSTGRES_RESOURCE_DEFINITION,
        "output": {
            "host": host,
            "port": int(port),
            "database_name": db_name,
            "secret_name": secret_name,
            "engine_version": "15",
        },
        "tags": [
            {"key": "environment", "value": "local"},
            {"key": "database", "value": db_name},
            {"key": "role", "value": "db-credentials"},
        ],
    }


def register_db_resource(
    db_name: str,
    username: str,
    host: str = "hmd_db",
    port: int = 5432,
    secret_name: Optional[str] = None,
) -> Dict:
    """Register (only) a Postgres DB as a local NERD Resource. No dbaccount call."""
    base_url = _ms_deployment_url()
    rid = _ensure_db_owner_rid(base_url)
    resource = _postgres_resource(db_name, username, host, port, secret_name)
    logger.info(f"Registering local postgres Resource for database '{db_name}'")
    return _rest.post_apiop(
        base_url,
        "submit_resources",
        {"repo_instance_deployment_id": rid, "resources": [resource]},
    )


def provision_and_register_db(
    db_name: str,
    username: str,
    host: str = "hmd_db",
    port: int = 5432,
) -> Dict:
    """Provision a DB/user via hmd_ms_dbaccount, then register it as a Resource."""
    did = os.environ.get("HMD_DID", "aaa")
    logger.info(f"Provisioning database '{db_name}' via hmd_ms_dbaccount")
    _post_create_db_account(did, db_name, username, origin="dev")
    return register_db_resource(db_name, username, host, port)
