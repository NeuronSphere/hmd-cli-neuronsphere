import json
import logging
import re

from cdktf import TerraformResourceLifecycle
from constructs import Construct

from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_lib_cdktf.terraform_imports.aws.db_parameter_group import (
    DbParameterGroup,
    DbParameterGroupParameter,
)
from hmd_lib_cdktf.terraform_imports.aws.rds_cluster_parameter_group import (
    RdsClusterParameterGroup,
    RdsClusterParameterGroupParameter,
)
from hmd_lib_cdktf.terraform_imports.terraform_aws_modules.aws.rds_aurora import (
    TerraformAwsModulesAwsRdsAurora as RdsAurora,
)
from hmd_lib_cdktf_factories.secrets_manager import SecretsManager
from hmd_lib_cdktf_factories.vpc import Vpc
from hmd_cli_tools.cdktf_tools import TerraformStateMigrator
from hmd_cli_tools.hmd_cli_tools import (
    get_session,
    get_cloud_region,
    get_secret_cache,
    get_cached_secret,
)
from hmd_cli_tools.rds_tools import (
    get_rds_cluster_identifier,
    get_current_engine_version,
    get_current_instance_class,
    is_major_version_upgrade,
    create_cluster_snapshot,
    generate_snapshot_id,
    get_all_database_names,
    get_row_counts,
    compare_row_counts,
)

logger = logging.getLogger(__name__)

SERVERLESS_ENGINE_MODE = "serverless"

# Aurora PostgreSQL added Graviton support starting at engine version 13.3.
GRAVITON_INSTANCE_RE = re.compile(r"^db\.(r6g|r7g|t4g|m6g|m7g)\.")
GRAVITON_MIN_PG_MAJOR = 13


def _aurora_pg_family(engine_version: str) -> str:
    """Derive the Aurora PostgreSQL parameter group family from an engine version."""
    return f"aurora-postgresql{engine_version.split('.')[0]}"


def _engine_major(engine_version: str) -> str:
    return engine_version.split(".")[0]


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

        self._upgrade_context = {}

        db_username = config["db_username"]
        db_engine = config["db_engine"]
        engine_version = config["engine_version"]

        # Guard: Graviton instance classes require Aurora PostgreSQL 13.3+.
        instance_type = config.get("instance_type", "db.r4.large")
        if GRAVITON_INSTANCE_RE.match(instance_type) and int(
            _engine_major(engine_version)
        ) < GRAVITON_MIN_PG_MAJOR:
            raise ValueError(
                f"Graviton instance type {instance_type!r} requires Aurora "
                f"PostgreSQL >= {GRAVITON_MIN_PG_MAJOR}; engine_version is "
                f"{engine_version!r}."
            )

        vpc = Vpc(self)
        base_vpc = config["dependencies"]["base-vpc"]
        base_vpc_data = vpc.get(base_vpc)

        major = _engine_major(engine_version)
        derived_family = _aurora_pg_family(engine_version)
        # `create_before_destroy` lets the new family-tagged param group come up
        # and the cluster pointer swing before the old one is destroyed during
        # a major-version upgrade.
        param_group_lifecycle = TerraformResourceLifecycle(create_before_destroy=True)

        db_parameter_group_config = config["db_parameter_group"]
        db_parameter_group = DbParameterGroup(
            self,
            f"db-parameter-group-pg{major}",
            name=f"{self.base_name.replace('_', '-')}-db-param-group-pg{major}",
            family=db_parameter_group_config.get("family", derived_family),
            lifecycle=param_group_lifecycle,
            parameter=[
                DbParameterGroupParameter(
                    name=value["name"],
                    value=value["value"],
                    apply_method=value.get("apply_method", "immediate"),
                )
                for value in db_parameter_group_config.get("parameter_values", [])
            ],
        )

        cluster_parameter_group_config = config["cluster_parameter_group"]
        cluster_parameter_group = RdsClusterParameterGroup(
            self,
            f"rds-cluster-parameter-group-pg{major}",
            name=f"{self.base_name.replace('_', '-')}-cluster-param-group-pg{major}",
            family=cluster_parameter_group_config.get("family", derived_family),
            lifecycle=param_group_lifecycle,
            parameter=[
                RdsClusterParameterGroupParameter(
                    name=value["name"],
                    value=value["value"],
                    apply_method=value.get("apply_method", "immediate"),
                )
                for value in cluster_parameter_group_config.get("parameter_values", [])
            ],
        )

        additional_args = {
            "engine_mode": config.get("engine_mode", SERVERLESS_ENGINE_MODE)
        }
        is_serverless = additional_args["engine_mode"] == SERVERLESS_ENGINE_MODE

        if is_serverless:
            additional_args["replica_scale_enabled"] = False
            additional_args["replica_count"] = 0
        else:
            additional_args["replica_scale_enabled"] = True
            additional_args["replica_scale_min"] = config.get("replica_scale_min", 1)
            additional_args["replica_scale_max"] = config.get("replica_scale_max", 2)
            additional_args["instance_type"] = instance_type

        args = {
            "name": f"{self.base_name.replace('_', '-')}",
            "engine": db_engine,
            "engine_version": engine_version,
            "vpc_id": base_vpc_data.id,
            "db_subnet_group_name": f"{base_vpc['instance_name']}_{base_vpc['repo_name']}_{deployment_id}_{environment}_{hmd_region}_{customer_code}",
            "tags": self.global_tags,
            "monitoring_interval": 15,
            "iam_role_name": f"{instance_name}-{repo_name}-monitoring",
            "apply_immediately": True,
            "db_parameter_group_name": db_parameter_group.id,
            "db_cluster_parameter_group_name": cluster_parameter_group.id,
            "username": db_username,
            "publicly_accessible": config.get("publicly_accessible", True),
            "allowed_cidr_blocks": [base_vpc_data.cidr_block],
            "ca_cert_identifier": "rds-ca-rsa2048-g1",
            "snapshot_identifier": config.get("snapshot_identifier", None),
            "global_cluster_identifier": config.get("global_cluster_identifier", None),
            "allow_major_version_upgrade": config.get(
                "allow_major_version_upgrade", False
            ),
        }

        if is_serverless:
            args["scaling_configuration"] = {
                "min_capacity": "2",
                "max_capacity": "16",
                "auto_pause": "true",
                "seconds_until_auto_pause": "300",
                "timeout_action": "RollbackCapacityChange",
            }

        args.update(additional_args)

        rds_aurora = RdsAurora(self, "ns-aurora-postgress", **args)

        connection_info = {
            "username": db_username,
            "password": rds_aurora.cluster_master_password_output,
            "engine": db_engine,
            "host": rds_aurora.cluster_endpoint_output,
            "port": rds_aurora.cluster_port_output,
        }

        sm = SecretsManager(self)
        root_secret = sm.db_root_secret(connection_info)

        if hasattr(self, "add_resource_output"):
            self.add_resource_output(
                self.base_name,
                {
                    "resource_namespace": "aws.neuronsphere.io",
                    "resource_definition_name": "aurora-postgres",
                    "version": "0.1.0",
                },
                {
                    "host": rds_aurora.cluster_endpoint_output,
                    "port": rds_aurora.cluster_port_output,
                    "database_name": rds_aurora.cluster_database_name_output,
                    "secret_name": f"{self.base_name}_db-secret",
                    "engine_version": rds_aurora.cluster_engine_version_actual_output,
                    "cluster_identifier": rds_aurora.cluster_id_output,
                    "cluster_arn": rds_aurora.cluster_arn_output,
                    "reader_endpoint": rds_aurora.cluster_reader_endpoint_output,
                },
            )
        else:
            logger.warning(
                "hmd-lib-cdktf does not provide HmdCdkTfStack.add_resource_output "
                "(older version installed) — skipping NERD0004 Resource output emission."
            )

    def do_migrate_state(self):
        migrator = TerraformStateMigrator()
        migrator.auto_detect_module_migrations()

        # Re-home parameter groups under their version-suffixed addresses so the
        # rename in 0.7 does not destroy/recreate. The migrator no-ops when the
        # source address is absent from state, so this is safe to leave in place.
        major = _engine_major(self.config["engine_version"])
        migrator.add_resource_migration(
            "aws_db_parameter_group.db-parameter-group",
            f"aws_db_parameter_group.db-parameter-group-pg{major}",
        )
        migrator.add_resource_migration(
            "aws_rds_cluster_parameter_group.rds-cluster-parameter-group",
            f"aws_rds_cluster_parameter_group.rds-cluster-parameter-group-pg{major}",
        )

        migrator.execute()

    def _get_session(self):
        """Create a boto3 session for use in deploy hooks."""
        return get_session(get_cloud_region(self.hmd_region), self.profile)

    def _get_upgrade_config(self):
        """Get upgrade configuration with defaults."""
        return self.config.get("upgrade_config", {})

    def do_pre_deploy(self):
        """Pre-deploy hook: detect upgrades (major version or instance class) and prepare safety measures."""
        upgrade_config = self._get_upgrade_config()

        if upgrade_config.get("skip_upgrade_safety", False):
            logger.info("Upgrade safety checks are disabled (skip_upgrade_safety=true)")
            return

        session = self._get_session()
        target_version = self.config["engine_version"]
        target_instance_class = self.config.get("instance_type")
        cluster_id = get_rds_cluster_identifier(self.base_name)

        current_version = get_current_engine_version(session, cluster_id)
        if current_version is None:
            logger.info(
                f"Cluster {cluster_id} does not exist yet — skipping upgrade safety (fresh deploy)"
            )
            return

        is_major_upgrade = is_major_version_upgrade(current_version, target_version)

        # Fail-closed: refuse to proceed with a major upgrade unless the consumer
        # has explicitly opted in. Otherwise a routine version_spec bump that
        # picks up new defaults would silently trigger an in-place upgrade.
        if is_major_upgrade and not self.config.get(
            "allow_major_version_upgrade", False
        ):
            raise Exception(
                f"Major version upgrade detected ({current_version} -> {target_version}) "
                f"for cluster {cluster_id}, but allow_major_version_upgrade is false. "
                f"Set allow_major_version_upgrade=true in deployment config to opt in. "
                f"See docs/upgrade.rst for the recommended procedure."
            )

        current_instance_class = None
        is_instance_class_change = False
        if target_instance_class:
            current_instance_class = get_current_instance_class(session, cluster_id)
            is_instance_class_change = (
                current_instance_class is not None
                and current_instance_class != target_instance_class
            )

        if not is_major_upgrade and not is_instance_class_change:
            logger.info(
                f"No upgrade detected (engine {current_version} -> {target_version}, "
                f"instance {current_instance_class} -> {target_instance_class}) — "
                f"skipping upgrade safety"
            )
            return

        if is_major_upgrade:
            logger.info(
                f"MAJOR VERSION UPGRADE DETECTED: {current_version} -> {target_version} "
                f"for cluster {cluster_id}"
            )
        if is_instance_class_change:
            logger.info(
                f"INSTANCE CLASS CHANGE DETECTED: {current_instance_class} -> "
                f"{target_instance_class} for cluster {cluster_id}"
            )

        self._upgrade_context["is_major_upgrade"] = is_major_upgrade
        self._upgrade_context["is_instance_class_change"] = is_instance_class_change
        self._upgrade_context["current_version"] = current_version
        self._upgrade_context["target_version"] = target_version
        self._upgrade_context["current_instance_class"] = current_instance_class
        self._upgrade_context["target_instance_class"] = target_instance_class
        self._upgrade_context["cluster_id"] = cluster_id

        # Step 1: Create a manual snapshot
        snapshot_id = generate_snapshot_id(cluster_id, current_version, target_version)
        snapshot_timeout = upgrade_config.get("snapshot_wait_timeout", 1800)
        purpose = (
            "pre-major-version-upgrade"
            if is_major_upgrade
            else "pre-instance-class-change"
        )

        logger.info(f"Creating pre-upgrade snapshot: {snapshot_id}")
        snapshot_arn = create_cluster_snapshot(
            session=session,
            cluster_id=cluster_id,
            snapshot_id=snapshot_id,
            tags={
                "purpose": purpose,
                "source_version": current_version,
                "target_version": target_version,
                "source_instance_class": current_instance_class or "unknown",
                "target_instance_class": target_instance_class or "unchanged",
                **self.global_tags,
            },
            wait=True,
            timeout=snapshot_timeout,
        )
        self._upgrade_context["snapshot_id"] = snapshot_id
        self._upgrade_context["snapshot_arn"] = snapshot_arn
        logger.info(f"Pre-upgrade snapshot created: {snapshot_arn}")

        # Step 2: Pause PgBouncer traffic. Always pause for a major version
        # upgrade; for an instance-class-only swap, the failover window is
        # short enough that pgbouncer reconnect handles it — opt in via
        # pause_on_instance_change if a stricter quiet period is needed.
        should_pause = is_major_upgrade or (
            is_instance_class_change
            and upgrade_config.get("pause_on_instance_change", False)
        )
        pgbouncer_config = upgrade_config.get("pgbouncer")
        if should_pause and pgbouncer_config:
            self._pause_pgbouncer(session, pgbouncer_config)
        elif should_pause:
            logger.info("No PgBouncer configuration — skipping traffic pause")

        # Step 3: Capture pre-upgrade row counts (only meaningful for engine upgrades)
        if is_major_upgrade and upgrade_config.get("verify_row_counts", True):
            self._capture_pre_upgrade_row_counts(session)

    def do_post_deploy(self):
        """Post-deploy hook: verify data integrity and resume traffic after upgrade."""
        if not (
            self._upgrade_context.get("is_major_upgrade")
            or self._upgrade_context.get("is_instance_class_change")
        ):
            return

        upgrade_config = self._get_upgrade_config()
        session = self._get_session()
        pgbouncer_config = upgrade_config.get("pgbouncer")
        verification_error = None

        try:
            # Verify row counts if we captured pre-upgrade counts (engine upgrades only)
            if (
                upgrade_config.get("verify_row_counts", True)
                and "pre_row_counts" in self._upgrade_context
            ):
                self._verify_row_counts(session, upgrade_config)
        except Exception as e:
            verification_error = e
        finally:
            # ALWAYS resume PgBouncer if we paused it, regardless of verification outcome
            if pgbouncer_config and self._upgrade_context.get("pgbouncer_paused"):
                try:
                    self._resume_pgbouncer(session, pgbouncer_config)
                except Exception as resume_err:
                    logger.critical(
                        f"CRITICAL: Failed to resume PgBouncer: {resume_err}. "
                        f"Manual intervention required!"
                    )

        # Post-upgrade maintenance (engine upgrades only): refresh optimizer
        # statistics and check for collation-version drift. Best-effort --
        # runs regardless of verification outcome and never raises, since
        # these are diagnostic/hygiene follow-ups, not data-loss checks.
        if self._upgrade_context.get("is_major_upgrade"):
            self._run_post_upgrade_maintenance(session, upgrade_config)

        if verification_error:
            snapshot_id = self._upgrade_context.get("snapshot_id", "unknown")
            raise Exception(
                f"Post-upgrade verification failed: {verification_error}. "
                f"Recovery snapshot: {snapshot_id}"
            )

        if self._upgrade_context.get("is_major_upgrade"):
            logger.info("Major version upgrade completed successfully")
        else:
            logger.info("Instance class change completed successfully")

    def _run_post_upgrade_maintenance(self, session, upgrade_config):
        """Run ANALYZE and check for collation-version drift after an engine upgrade.

        Both are best-effort: failures are logged, never raised, since they
        are diagnostic/hygiene follow-ups rather than data-loss checks (that
        remains the job of _verify_row_counts).
        """
        from upgrade_utils import run_analyze, check_collation_drift

        try:
            creds = self._get_db_credentials(session)
            host = creds["host"]
            port = int(creds.get("port", 5432))
            user = creds["username"]
            password = creds["password"]
        except Exception as e:
            logger.warning(
                f"Could not retrieve credentials for post-upgrade maintenance: {e}"
            )
            return

        if upgrade_config.get("run_post_upgrade_analyze", True):
            try:
                databases = get_all_database_names(host, port, user, password)
                results = run_analyze(host, port, user, password, databases)
                failures = {db: err for db, err in results.items() if err}
                if failures:
                    logger.warning(f"ANALYZE failed for some databases: {failures}")
                else:
                    logger.info(f"ANALYZE completed for {len(results)} databases")
            except Exception as e:
                logger.warning(f"Post-upgrade ANALYZE failed: {e}")

        if upgrade_config.get("check_collation_drift", True):
            try:
                drift = check_collation_drift(host, port, user, password)
                if drift:
                    affected = ", ".join(d["datname"] for d in drift)
                    logger.critical(
                        f"COLLATION VERSION DRIFT DETECTED in database(s): {affected}. "
                        f"Indexes relying on the default collation in these databases "
                        f"may sort/compare incorrectly. REINDEX affected indexes, then "
                        f"run ALTER DATABASE <name> REFRESH COLLATION VERSION. "
                        f"Details: {drift}"
                    )
                else:
                    logger.info("No collation-version drift detected")
            except Exception as e:
                logger.warning(f"Collation drift check failed: {e}")

    def _pause_pgbouncer(self, session, pgbouncer_config):
        """Pause PgBouncer traffic by scaling the deployment to 0."""
        from hmd_cli_tools.k8s_tools import create_kubeconfig
        from upgrade_utils import PgBouncerManager

        eks_instance_name = pgbouncer_config["eks_instance_name"]
        eks_repo_name = pgbouncer_config.get("eks_repo_name", "hmd-inf-eks")
        namespace = pgbouncer_config["namespace"]
        deployment_name = pgbouncer_config["deployment_name"]
        aws_region = get_cloud_region(self.hmd_region)

        logger.info(f"Pausing PgBouncer: {deployment_name} in {namespace}")

        with create_kubeconfig(
            instance_name=eks_instance_name,
            session=session,
            profile=self.profile,
            aws_region=aws_region,
            repo_name=eks_repo_name,
        ) as kubeconfig_path:
            manager = PgBouncerManager(kubeconfig_path, namespace, deployment_name)
            manager.pause_traffic()
            self._upgrade_context["pgbouncer_paused"] = True
            self._upgrade_context["pgbouncer_original_replicas"] = manager._original_replicas

    def _resume_pgbouncer(self, session, pgbouncer_config):
        """Resume PgBouncer traffic by scaling the deployment back up."""
        from hmd_cli_tools.k8s_tools import create_kubeconfig
        from upgrade_utils import PgBouncerManager

        eks_instance_name = pgbouncer_config["eks_instance_name"]
        eks_repo_name = pgbouncer_config.get("eks_repo_name", "hmd-inf-eks")
        namespace = pgbouncer_config["namespace"]
        deployment_name = pgbouncer_config["deployment_name"]
        aws_region = get_cloud_region(self.hmd_region)

        logger.info(f"Resuming PgBouncer: {deployment_name} in {namespace}")

        with create_kubeconfig(
            instance_name=eks_instance_name,
            session=session,
            profile=self.profile,
            aws_region=aws_region,
            repo_name=eks_repo_name,
        ) as kubeconfig_path:
            manager = PgBouncerManager(kubeconfig_path, namespace, deployment_name)
            manager._original_replicas = self._upgrade_context.get(
                "pgbouncer_original_replicas", 1
            )
            manager.resume_traffic()
            self._upgrade_context["pgbouncer_paused"] = False

    def _get_db_credentials(self, session):
        """Retrieve database credentials from Secrets Manager."""
        secret_name = f"{self.base_name}_db-secret"
        cache = get_secret_cache(session)
        secret = get_cached_secret(cache, secret_name)
        if isinstance(secret, str):
            secret = json.loads(secret)
        return secret

    def _capture_pre_upgrade_row_counts(self, session):
        """Capture row counts before the upgrade for later verification."""
        try:
            creds = self._get_db_credentials(session)
            host = creds["host"]
            port = int(creds.get("port", 5432))
            user = creds["username"]
            password = creds["password"]

            databases = get_all_database_names(host, port, user, password)
            row_counts = get_row_counts(host, port, user, password, databases)
            self._upgrade_context["pre_row_counts"] = row_counts

            total_tables = sum(len(tables) for tables in row_counts.values())
            logger.info(
                f"Captured pre-upgrade row counts: "
                f"{len(databases)} databases, {total_tables} tables"
            )
        except ImportError:
            logger.warning(
                "pg8000 not installed — skipping row count verification"
            )
        except Exception as e:
            logger.warning(f"Could not capture pre-upgrade row counts: {e}")

    def _verify_row_counts(self, session, upgrade_config):
        """Capture post-upgrade row counts and compare with pre-upgrade."""
        creds = self._get_db_credentials(session)
        host = creds["host"]
        port = int(creds.get("port", 5432))
        user = creds["username"]
        password = creds["password"]

        databases = get_all_database_names(host, port, user, password)
        post_counts = get_row_counts(host, port, user, password, databases)

        tolerance = upgrade_config.get("row_count_tolerance_pct", 0.0)
        pre_counts = self._upgrade_context["pre_row_counts"]

        passed, mismatches = compare_row_counts(pre_counts, post_counts, tolerance)

        if passed:
            logger.info("Row count verification passed — all counts match")
        else:
            mismatch_detail = "\n  ".join(mismatches)
            raise Exception(
                f"Row count mismatch detected after upgrade:\n  {mismatch_detail}"
            )
