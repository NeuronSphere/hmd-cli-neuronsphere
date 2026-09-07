import logging
import os

import psycopg2
from boto3 import Session
from mangum import Mangum
from hmd_base_service.fastapi import FastAPIService
from hmd_base_service.fastapi.handlers.rest_handler import RestOperationHandler

# Reuses hmd_ms_base's Lambda response-shaping/tracing wrapper — available on
# PYTHONPATH since this service's Docker image extends hmd-ms-base's.
from hmd_ms_base.hmd_ms_base import lambda_wrapper
from hmd_cli_tools.hmd_cli_tools import gen_password, make_standard_name
from hmd_lib_secrets_backend import create_secret, get_secret, migrate_secret
from psycopg2 import sql

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)


def _new_password_for(user_to_create: str) -> str:
    """Generate a password for a new local-DB user.

    In local NeuronSphere the convention is `password = username` so service
    compose env vars can hardcode credentials. Detected via HMD_ENVIRONMENT.
    Cloud (any other value) keeps the original random-password behavior.
    """
    if os.environ.get("HMD_ENVIRONMENT") == "local":
        return user_to_create
    return gen_password(include_symbols=False)


def _kms_key_id():
    return os.environ.get("HMD_SECRETS_KMS_KEY_ID")


def do_postgres_create_db_account(
    username,
    password,
    host,
    port,
    db_to_create,
    user_to_create,
    user_password,
):
    """Idempotently create a Postgres user + database.

    `user_password` is used only when creating a brand-new user. Never
    issues ALTER USER ... PASSWORD on an existing user.
    """
    conn = psycopg2.connect(
        dbname="postgres", user=username, host=host, password=password, port=port
    )
    conn.autocommit = True

    try:
        with conn.cursor() as cur:
            cur.execute("select 1 from pg_roles where rolname=%s", (user_to_create,))
            user_existed = cur.rowcount > 0

            cur.execute(
                "select 1 from pg_database where datname=%s", (user_to_create,)
            )
            database_exists = cur.rowcount > 0

            try:
                if not user_existed:
                    if not user_password:
                        raise RuntimeError(
                            f"No password available to create new user {user_to_create}"
                        )
                    cur.execute(
                        sql.SQL("CREATE USER {} LOGIN PASSWORD {};").format(
                            sql.Identifier(user_to_create),
                            sql.Literal(user_password),
                        )
                    )

                if not database_exists:
                    cur.execute(
                        sql.SQL("CREATE DATABASE {};").format(
                            sql.Identifier(db_to_create)
                        )
                    )

                cur.execute(
                    sql.SQL("GRANT ALL PRIVILEGES ON DATABASE {} TO {};").format(
                        sql.Identifier(db_to_create), sql.Identifier(user_to_create)
                    ),
                )
                return not user_existed
            except psycopg2.Error as err:
                print(f"{err.pgcode}: {err.pgerror}")
                raise err

    finally:
        conn.close()


def do_create_db_account(
    db_repo_class: str,
    db_instance_name: str,
    db_deployment_id: str,
    db_name: str,
    username: str = None,
):
    """Create a database and user if absent. Migrate existing secrets PS-first; never regenerate."""
    if not username:
        username = db_name

    secret_base = make_standard_name(
        db_instance_name,
        db_repo_class,
        db_deployment_id,
        os.environ["HMD_ENVIRONMENT"],
        os.environ["HMD_REGION"],
        os.environ["HMD_CUSTOMER_CODE"],
    )
    admin_secret_name = f"{secret_base}_db-secret"
    user_secret_name = f"{secret_base}_{username}"
    session = Session()
    kms_key_id = _kms_key_id()

    migrate_secret(session, admin_secret_name, kms_key_id=kms_key_id)
    admin_db_secret = get_secret(session, admin_secret_name)

    for key in ["username", "password", "engine", "host", "port"]:
        assert key in admin_db_secret, f"Property, {key}, is not in admin db secret."

    engine = admin_db_secret["engine"]
    del admin_db_secret["engine"]

    if engine not in ["aurora-postgresql"]:
        return f"Engine {engine} not handled; no action."

    try:
        existing_user_secret = get_secret(session, user_secret_name)
    except KeyError:
        existing_user_secret = None

    if existing_user_secret is not None:
        migrate_status = migrate_secret(
            session, user_secret_name, kms_key_id=kms_key_id
        )
        preserved_password = (
            existing_user_secret.get("password")
            if isinstance(existing_user_secret, dict)
            else None
        )
        user_was_created = do_postgres_create_db_account(
            user_to_create=username,
            db_to_create=db_name,
            user_password=preserved_password,
            **admin_db_secret,
        )
        return (
            f"Secret {user_secret_name} present (migration={migrate_status}); "
            f"new_user_in_db={user_was_created}."
        )

    new_password = _new_password_for(username)
    do_postgres_create_db_account(
        user_to_create=username,
        db_to_create=db_name,
        user_password=new_password,
        **admin_db_secret,
    )
    create_secret(
        session,
        user_secret_name,
        {
            "username": username,
            "password": new_password,
            "engine": engine,
            "host": admin_db_secret["host"],
            "port": admin_db_secret["port"],
        },
        kms_key_id=kms_key_id,
    )
    return f"Secret, {user_secret_name}, created."


def _startup_migrate_scan():
    """Scan SM for prefix-matching secrets and migrate each. No password generation."""
    prefixes_env = os.environ.get("HMD_SECRETS_MIGRATE_PREFIXES", "").strip()
    if not prefixes_env:
        return
    prefixes = [p for p in (p.strip() for p in prefixes_env.split(",")) if p]
    if not prefixes:
        return

    session = Session()
    sm = session.client("secretsmanager")
    kms_key_id = _kms_key_id()
    paginator = sm.get_paginator("list_secrets")
    for page in paginator.paginate():
        for entry in page.get("SecretList", []):
            name = entry.get("Name", "")
            if not any(name.startswith(p) for p in prefixes):
                continue
            try:
                status = migrate_secret(session, name, kms_key_id=kms_key_id)
                logger.info("startup_migrate: %s -> %s", name, status)
            except Exception as ex:
                logger.warning("startup_migrate failed for %s: %s", name, ex)


def get_service():

    svc = FastAPIService({})
    svc.register_handler(RestOperationHandler(), "/api")

    @svc.operation(
        rest_path="/api/create_db_account",
        rest_methods=["POST"],
        args={"db_spec": "json"},
    )
    def create_db_account(evt, ctx):
        db_spec = evt["args"].get("db_spec")
        if db_spec is None:
            raise Exception("No db_spec provided.")

        return do_create_db_account(**db_spec)

    svc.run()
    return svc


initialized = False
mangum = None


@lambda_wrapper
def handler(evt, ctx):
    global svc
    global initialized
    if not initialized:
        svc = get_service()
        try:
            _startup_migrate_scan()
        except Exception as ex:
            logger.warning("startup_migrate_scan failed: %s", ex)
        initialized = True

    logger.info(evt)
    global mangum
    if not mangum:
        mangum = Mangum(svc, lifespan="off")

    class Wrapper:
        def execute(self, evt, ctx):
            logger.info(evt)
            result = mangum(evt, ctx)
            return result

    return Wrapper()
