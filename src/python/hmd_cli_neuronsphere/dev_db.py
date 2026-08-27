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


def _ensure_db_owner_rid(base_url: str, env=None) -> str:
    """Ensure the env-linked ``local-databases`` owner instance and return its RID id.

    Owned by the core ``hmd-cli-neuronsphere`` RepoClass (all local default Resources
    are owned by one RepoClass). Idempotent: the RCV registration tolerates an existing
    version and ``register_deployed_instance`` reuses the instance if present.

    The instance name stays ``local-databases`` in every environment --
    repo_instance is unique by name *per Environment* -- so the environment
    and deployment id are what disambiguate it.
    """
    version = _get_repo_version(CORE_REPO_CLASS)
    environment = env.slug if env is not None else "local"
    deployment_id = env.deployment_id if env is not None else "local"
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
            "environment": environment,
            "repo_class_name": CORE_REPO_CLASS,
            "version": version,
            "instance_name": DB_OWNER_INSTANCE,
            "deployment_id": deployment_id,
            "instance_configuration": {},
        },
    )
    return result["repo_instance_deployment_id"]


def _postgres_resource(
    db_name: str,
    username: str,
    host: str,
    port: int,
    secret_name: Optional[str],
    did: Optional[str] = None,
    environment_name: str = "local",
) -> Dict:
    """Build the ``postgres`` Resource dict (effective output = postgres + database)."""
    if not secret_name:
        secret_name = f"{_local_db_secret_base(did, environment_name)}_{username}"
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
            {"key": "environment", "value": environment_name},
            {"key": "database", "value": db_name},
            {"key": "role", "value": "db-credentials"},
        ],
    }


def register_db_resource(
    db_name: str,
    username: str,
    host: str = None,
    port: int = 5432,
    secret_name: Optional[str] = None,
    env=None,
) -> Dict:
    """Register (only) a Postgres DB as a local NERD Resource. No dbaccount call.

    ``host`` defaults to the environment's own Postgres container.
    """
    base_url = _ms_deployment_url()
    host = host or (env.db_container if env is not None else "hmd_db")
    did = env.deployment_id if env is not None else None
    environment_name = env.slug if env is not None else "local"
    rid = _ensure_db_owner_rid(base_url, env)
    resource = _postgres_resource(
        db_name, username, host, port, secret_name, did, environment_name
    )
    logger.info(f"Registering local postgres Resource for database '{db_name}'")
    return _rest.post_apiop(
        base_url,
        "submit_resources",
        {"repo_instance_deployment_id": rid, "resources": [resource]},
    )


def provision_and_register_db(
    db_name: str,
    username: str,
    host: str = None,
    port: int = 5432,
    env=None,
) -> Dict:
    """Provision a DB/user via hmd_ms_dbaccount, then register it as a Resource.

    dbaccount is per-environment, so this targets the environment's own
    ``/<slug>/hmd_ms_dbaccount/`` route and its own Postgres.

    Every environment is prefixed, legacy layout included: ``write_env_routes``
    prefixes on ``env.slug`` unconditionally, and a legacy environment shares the
    control plane's *containers*, not its unprefixed routes. Excepting it here
    sent the call to a control-plane path where ms-dbaccount is not routed, and
    it 404'd.
    """
    did = env.deployment_id if env is not None else os.environ.get("HMD_DID", "aaa")
    route_prefix = env.slug if env is not None else ""
    logger.info(f"Provisioning database '{db_name}' via hmd_ms_dbaccount")
    _post_create_db_account(
        did, db_name, username, origin="dev", route_prefix=route_prefix
    )
    return register_db_resource(db_name, username, host, port, env=env)
