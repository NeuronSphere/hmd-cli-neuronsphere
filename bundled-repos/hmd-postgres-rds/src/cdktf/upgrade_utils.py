import logging
import subprocess

logger = logging.getLogger(__name__)


class PgBouncerManager:
    """Manages PgBouncer traffic during major version upgrades.

    Uses kubectl scale to pause/resume traffic by scaling the PgBouncer
    deployment to 0 replicas and back.

    :param kubeconfig_path: Path to a kubeconfig file for kubectl
    :param namespace: Kubernetes namespace of the PgBouncer deployment
    :param deployment_name: Name of the PgBouncer deployment
    """

    def __init__(self, kubeconfig_path: str, namespace: str, deployment_name: str):
        self.kubeconfig_path = kubeconfig_path
        self.namespace = namespace
        self.deployment_name = deployment_name
        self._original_replicas = None

    def _kubectl(self, args: list) -> str:
        """Run a kubectl command and return stdout."""
        cmd = [
            "kubectl",
            f"--kubeconfig={self.kubeconfig_path}",
            f"--namespace={self.namespace}",
        ] + args
        logger.info(f"Running: {' '.join(cmd)}")
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        return result.stdout.strip()

    def _get_current_replicas(self) -> int:
        """Get the current replica count of the deployment."""
        output = self._kubectl([
            "get", "deployment", self.deployment_name,
            "-o", "jsonpath={.spec.replicas}",
        ])
        return int(output) if output else 1

    def pause_traffic(self):
        """Scale PgBouncer deployment to 0 to stop traffic to the database."""
        self._original_replicas = self._get_current_replicas()
        logger.info(
            f"Pausing PgBouncer: scaling {self.deployment_name} "
            f"from {self._original_replicas} to 0 replicas"
        )
        self._kubectl([
            "scale", "deployment", self.deployment_name, "--replicas=0",
        ])
        logger.info(f"PgBouncer deployment {self.deployment_name} scaled to 0")

    def resume_traffic(self):
        """Scale PgBouncer deployment back to its original replica count."""
        replicas = self._original_replicas if self._original_replicas else 1
        logger.info(
            f"Resuming PgBouncer: scaling {self.deployment_name} "
            f"to {replicas} replicas"
        )
        self._kubectl([
            "scale", "deployment", self.deployment_name, f"--replicas={replicas}",
        ])
        logger.info(f"PgBouncer deployment {self.deployment_name} scaled to {replicas}")


def _get_pg8000():
    """Lazily import pg8000, returning None if not installed."""
    try:
        import pg8000

        return pg8000
    except ImportError:
        return None


def run_analyze(host: str, port: int, user: str, password: str, databases: list) -> dict:
    """Run ANALYZE on every database to refresh optimizer statistics.

    pg_upgrade does not preserve optimizer statistics, so query plans can
    regress until autovacuum's auto-analyze catches up. Best-effort: a
    failure analyzing one database does not stop the rest.

    :param host: Database host
    :param port: Database port
    :param user: Database user
    :param password: Database password
    :param databases: List of database names to analyze
    :return: Dict of {db_name: error message, or None on success}
    :raises ImportError: If pg8000 is not installed
    """
    pg8000 = _get_pg8000()
    if pg8000 is None:
        raise ImportError("pg8000 is required for post-upgrade maintenance operations")

    results = {}
    for db_name in databases:
        try:
            conn = pg8000.connect(
                host=host, port=port, user=user, password=password, database=db_name
            )
            try:
                cursor = conn.cursor()
                cursor.execute("ANALYZE;")
                conn.commit()
                results[db_name] = None
            finally:
                conn.close()
        except Exception as e:
            logger.warning(f"ANALYZE failed for database {db_name}: {e}")
            results[db_name] = str(e)

    return results


def check_collation_drift(host: str, port: int, user: str, password: str) -> list:
    """Check for collation-version drift on each database's default collation.

    pg_upgrade does not rebuild indexes. If the collation library version
    used by the engine shifts between major versions, indexes relying on a
    database's default collation can silently sort/compare incorrectly
    post-upgrade until REINDEXed -- same row counts, wrong query results.
    PostgreSQL 15+ tracks this via pg_database.datcollversion (the version
    the database was created/last refreshed with) versus
    pg_database_collation_actual_version() (what the OS/library provides
    now).

    Only covers each database's default collation (the common case for
    text-column indexes); databases using explicit non-default
    CREATE COLLATION objects would need a separate pg_collation.collversion
    check, which is out of scope here.

    :param host: Database host
    :param port: Database port
    :param user: Database user
    :param password: Database password
    :return: List of dicts with datname/recorded_version/actual_version for any mismatches
    :raises ImportError: If pg8000 is not installed
    """
    pg8000 = _get_pg8000()
    if pg8000 is None:
        raise ImportError("pg8000 is required for post-upgrade maintenance operations")

    conn = pg8000.connect(
        host=host, port=port, user=user, password=password, database="postgres"
    )
    try:
        cursor = conn.cursor()
        cursor.execute(
            "SELECT datname, datcollversion, "
            "pg_database_collation_actual_version(oid) AS actual_version "
            "FROM pg_database WHERE datistemplate = false "
            "AND datcollversion IS DISTINCT FROM pg_database_collation_actual_version(oid)"
        )
        return [
            {"datname": row[0], "recorded_version": row[1], "actual_version": row[2]}
            for row in cursor.fetchall()
        ]
    finally:
        conn.close()
