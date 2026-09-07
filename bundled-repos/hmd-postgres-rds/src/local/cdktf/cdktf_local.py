"""Floci-safe local CDKTF stack for hmd-postgres-rds.

The real ``src/cdktf/cdktf_local.py`` provisions an Aurora PostgreSQL *cluster*
through the ``terraform-aws-modules/rds-aurora`` module, on top of a VPC and
security-group data lookup. None of that survives contact with Floci:

- Floci's RDS emulation is instance-shaped. ``aws_db_instance`` applies cleanly
  and is idempotent (a second ``terraform plan`` reports no changes); the Aurora
  module's cluster/instance/subnet-group graph is not emulated.
- ``DataAwsVpc``/``DataAwsSecurityGroups`` have nothing to resolve against --
  there is no local VPC, and local network isolation is the Docker network.
- The upgrade machinery (snapshot gating, collation drift, pgbouncer pausing)
  presumes real Aurora.

What this overlay keeps identical is everything downstream consumers derive:
the ``{base_name}_db-secret`` connection secret and the produced Resource's
``secret_name``. ``hmd-database-account`` and every chart's ``awsDbSecretName``
helper build that name independently, so a divergence here surfaces far away as
a service failing to read credentials that look correctly named.

The Resource is emitted as the *base* ``database.neuronsphere.io/postgres`` type
rather than the cloud stack's ``aws.neuronsphere.io/aurora-postgres`` subtype:
locally this is a single instance, so the cluster-level fields that subtype adds
(reader endpoint, cluster ARN) have no truthful value to report. Consumers
depend on the base type, which is what resolution matches on.

Applied by ``LocalWorkflowRunner`` (``src/local/cdktf`` overlays ``src/cdktf``)
only in local mode; the cloud deploy is unaffected.
"""

import logging

from constructs import Construct

from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_lib_cdktf.terraform_imports.aws.db_instance import DbInstance
from hmd_lib_cdktf_factories.secrets_manager import SecretsManager

logger = logging.getLogger(__name__)

# Floci's RDS emulation runs a real container per instance, and reuses that
# instance's named volume across restarts. A postgres major-version change is
# therefore destructive -- the existing data directory fails to start with
# "database files are incompatible" -- so the engine version tracks whatever
# `hmd-postgres-base` is built on and is not silently defaulted here.
LOCAL_ENGINE_VERSION = "14"
LOCAL_DB_USERNAME = "postgres"
# Matches the password baked into ghcr.io/neuronsphere/hmd-postgres-base
# (`ENV POSTGRES_PASSWORD`). Floci passes MasterUserPassword to the container as
# POSTGRES_PASSWORD, so a different value here would leave the image initialised
# with one password and the secret advertising another.
LOCAL_DB_PASSWORD = "admin"


class CdkTfStack(HmdCdkTfStack):
    def __init__(
        self,
        scope: Construct,
        ns: str,
        instance_name: str,
        repo_name: str,
        deployment_id: str,
        environment: str,
        hmd_region: str,
        customer_code,
        repo_version: str,
        account_number: str,
        profile: str,
        config: dict,
    ):
        super().__init__(
            scope,
            ns,
            instance_name,
            repo_name,
            deployment_id,
            environment,
            hmd_region,
            customer_code,
            repo_version,
            account_number,
            profile,
            config,
        )

        db_username = config.get("db_username", LOCAL_DB_USERNAME)
        db_password = config.get("db_password", LOCAL_DB_PASSWORD)
        engine_version = config.get("engine_version", LOCAL_ENGINE_VERSION)

        # `identifier` must be RFC-1035-ish: lowercase alphanumerics and hyphens.
        identifier = self.base_name.replace("_", "-").lower()

        # Explicit placement rather than Floci's implicit "default" subnet group.
        # Floci seeds a region's default VPC and subnets once per *region* but
        # stores them per *account* (Ec2Service.seededRegions is keyed by region
        # alone), so every account after the first has no default VPC at all and
        # CreateDBInstance fails with "No subnets available for DB subnet group
        # default". The CLI creates this group per account
        # (floci_deployer.ensure_rds_subnet_group) and passes its name in.
        db_subnet_group_name = config.get("db_subnet_group_name")

        instance = DbInstance(
            self,
            "ns-postgres",
            identifier=identifier,
            engine="postgres",
            db_subnet_group_name=db_subnet_group_name,
            engine_version=engine_version,
            instance_class=config.get("instance_type", "db.t3.micro"),
            allocated_storage=config.get("allocated_storage", 20),
            username=db_username,
            password=db_password,
            # Nothing local takes a snapshot, and requiring one blocks teardown.
            skip_final_snapshot=True,
            apply_immediately=True,
            tags=self.global_tags,
        )

        # Floci publishes `Endpoint.Address` as its own IP on a port from its
        # 7001-7099 RDS proxy range, and that proxy does not survive a Floci
        # restart -- connections to it are reset while the backend container
        # keeps serving normally. The CLI therefore gives each backend container
        # a stable DNS alias on the Docker network
        # (`floci_deployer.ensure_rds_network_alias`) and passes it in here, so
        # the secret names the container directly on 5432. Falls back to the
        # emulator's endpoint when no alias was supplied.
        host = config.get("db_host") or instance.address
        port = config.get("db_port") or instance.port

        connection_info = {
            "username": db_username,
            "password": db_password,
            # A contract token, not a description of the local engine. This is a
            # plain `aws_db_instance`, but `hmd-ms-dbaccount.do_create_db_account`
            # gates on the exact string "aurora-postgresql" and *silently returns
            # success* for anything else:
            #
            #     if engine not in ["aurora-postgresql"]:
            #         return f"Engine {engine} not handled; no action."
            #
            # Reporting "postgres" truthfully therefore made every database and
            # user request a 200 that created nothing, surfacing much later as an
            # ExternalSecret stuck on a secret nobody had written.
            "engine": "aurora-postgresql",
            "host": host,
            "port": port,
        }
        SecretsManager(self).db_root_secret(connection_info)

        if hasattr(self, "add_resource_output"):
            self.add_resource_output(
                self.base_name,
                {
                    "resource_namespace": "database.neuronsphere.io",
                    "resource_definition_name": "postgres",
                    "version": "0.1.0",
                },
                {
                    "host": host,
                    "port": port,
                    "secret_name": f"{self.base_name}_db-secret",
                    "engine_version": engine_version,
                },
            )
        else:
            logger.warning(
                "hmd-lib-cdktf does not provide HmdCdkTfStack.add_resource_output "
                "(older version installed) — skipping NERD0004 Resource output emission."
            )
