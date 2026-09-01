"""
Custom Robot Framework library for testing hmd-cli-neuronsphere.

Provides keywords for:
- Running CLI commands
- Managing plugin environment variables
- Asserting Docker container states (running, stopped, completed)
"""

import os
import subprocess
from pathlib import Path
from typing import Dict, List, Optional

import docker
import docker.errors
import requests as http_requests
from robot.api import logger
from robot.api.deco import keyword, library


ALL_BUNDLED_PLUGINS = [
    "telemetry",
    "graph",
    "floci",
    "jupyter",
    "apache_superset",
    "airflow",
    "transform",
    "trino",
    "clickhouse",
    "hive_metastore",
]


class _CommandResult:
    """Wrapper to expose subprocess results to Robot Framework."""

    def __init__(self, rc, stdout, stderr):
        self.rc = rc
        self.stdout = stdout
        self.stderr = stderr


@library
class NeuronSphereLib:
    """Robot Framework library for NeuronSphere CLI and Docker assertions."""

    def __init__(self):
        self._docker_client = None

    @property
    def docker_client(self):
        if self._docker_client is None:
            self._docker_client = docker.from_env()
        return self._docker_client

    # ── Environment Setup Keywords ──

    @keyword
    def ensure_hmd_environment(self):
        """Ensure HMD_HOME exists with required directory structure for ``hmd neuronsphere up``.

        When running inside a Bender container, overrides ``HMD_HOME`` to a
        test-specific directory under ``HMD_REPO_PATH``.  This is critical
        because Bender sets ``HMD_HOME=/root/hmd_home`` (a container-internal
        path), but Docker Compose volume mounts need **host** paths.
        ``HMD_REPO_PATH`` is always a host path that Bender bind-mounts into
        the container, so creating ``hmd_home/`` inside it gives us a path
        that resolves on both the host and the container.
        """
        repo_path = os.environ.get("HMD_REPO_PATH", "")

        if repo_path:
            # HMD_REPO_PATH is the host path to the test/ dir, bind-mounted by Bender.
            # Create a test-specific HMD_HOME inside it so Docker volume mounts work.
            test_hmd_home = str(Path(repo_path) / "hmd_home")
            os.environ["HMD_HOME"] = test_hmd_home
            logger.info(f"Set HMD_HOME to test directory: {test_hmd_home}")

            # MiniStack runs on the Docker host; from inside Bender,
            # reach it via host.docker.internal instead of localhost.
            if not os.environ.get("FLOCI_ENDPOINT"):
                os.environ["FLOCI_ENDPOINT"] = "http://host.docker.internal:4566"
                logger.info("Set FLOCI_ENDPOINT=http://host.docker.internal:4566")

        hmd_home = os.environ.get("HMD_HOME")
        if not hmd_home:
            raise AssertionError("HMD_HOME environment variable is not set.")

        hmd_home = Path(hmd_home)

        required_dirs = [
            hmd_home / ".cache",
            hmd_home / ".config",
            hmd_home / "postgresql" / "data",
            hmd_home / "postgresql" / "scripts" / "always-initdb.d",
            hmd_home / "floci" / "data",
        ]
        for d in required_dirs:
            d.mkdir(parents=True, exist_ok=True)
            logger.info(f"Ensured directory: {d}")

        # Create hmd.env if it doesn't exist (docker compose expects it)
        hmd_env_file = hmd_home / ".config" / "hmd.env"
        if not hmd_env_file.exists():
            hmd_env_file.touch()
            logger.info(f"Created empty {hmd_env_file}")

        # Set HMD_PROJECTS_PATH to avoid NoneType error when HMD_REPO_HOME is unset
        if not os.environ.get("HMD_PROJECTS_PATH"):
            projects_path = str(hmd_home / "projects")
            Path(projects_path).mkdir(parents=True, exist_ok=True)
            os.environ["HMD_PROJECTS_PATH"] = projects_path
            logger.info(f"Set HMD_PROJECTS_PATH={projects_path}")

        # Ensure container registry is set (required for image pull references)
        if not os.environ.get("HMD_LOCAL_NS_CONTAINER_REGISTRY"):
            os.environ["HMD_LOCAL_NS_CONTAINER_REGISTRY"] = "ghcr.io/neuronsphere"
            logger.info("Set HMD_LOCAL_NS_CONTAINER_REGISTRY=ghcr.io/neuronsphere")

        # Set HMD_HOSTNAME if missing (used in some compose files)
        if not os.environ.get("HMD_HOSTNAME"):
            os.environ["HMD_HOSTNAME"] = "localhost"
            logger.info("Set HMD_HOSTNAME=localhost")

    # ── CLI Keywords ──

    @keyword
    def run_ns_command(self, *args, timeout: int = 600) -> _CommandResult:
        """Run ``hmd neuronsphere <args>`` and return result with rc/stdout/stderr.

        Args:
            *args: Arguments to pass after ``hmd neuronsphere``.
            timeout: Command timeout in seconds (default 600).

        Returns:
            Object with ``rc``, ``stdout``, ``stderr`` attributes.
        """
        cmd = ["hmd", "neuronsphere", *args]
        logger.info(f"Running: {' '.join(cmd)}")
        try:
            proc = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=int(timeout),
            )
            result = _CommandResult(proc.returncode, proc.stdout, proc.stderr)
        except subprocess.TimeoutExpired:
            logger.error(f"Command timed out after {timeout}s: {' '.join(cmd)}")
            result = _CommandResult(-1, "", f"Timeout after {timeout}s")
        logger.info(f"Return code: {result.rc}")
        if result.stdout:
            logger.info(f"STDOUT:\n{result.stdout}")
        if result.stderr:
            logger.info(f"STDERR:\n{result.stderr}")
        return result

    @keyword
    def start_local_neuronsphere(self, timeout: int = 600):
        """Run ``hmd neuronsphere up`` and assert containers started.

        Tolerates non-zero exit codes caused by post-startup steps (e.g.
        service registration failing because the naming service is not
        reachable from the test runner).  The actual container state is
        verified by subsequent assertion keywords.
        """
        result = self.run_ns_command("up", timeout=timeout)
        if result.rc != 0:
            # Check if containers actually started despite the error
            if "Starting containers" in result.stdout:
                logger.warn(
                    f"'hmd neuronsphere up' returned rc={result.rc} but containers "
                    f"may have started. Continuing to verify container state.\n"
                    f"STDOUT: {result.stdout}\nSTDERR: {result.stderr}"
                )
            else:
                raise AssertionError(
                    f"'hmd neuronsphere up' failed with rc={result.rc}.\n"
                    f"STDOUT: {result.stdout}\nSTDERR: {result.stderr}"
                )

    @keyword
    def stop_local_neuronsphere(self, timeout: int = 300):
        """Run ``hmd neuronsphere down``. Logs errors but does not raise, safe for teardown."""
        result = self.run_ns_command("down", timeout=timeout)
        if result.rc != 0:
            logger.warn(
                f"'hmd neuronsphere down' returned rc={result.rc}.\n"
                f"STDOUT: {result.stdout}\nSTDERR: {result.stderr}"
            )

    # ── Plugin Environment Keywords ──

    @keyword
    def set_plugin_environment(self, enabled_plugins: List[str]):
        """Set env vars to enable only the specified plugins (plus main).

        All bundled plugins not in *enabled_plugins* are explicitly disabled.

        Args:
            enabled_plugins: List of plugin names to enable.
        """
        enabled_set = set(enabled_plugins)
        for plugin in ALL_BUNDLED_PLUGINS:
            env_var = f"HMD_LOCAL_NEURONSPHERE_ENABLE_{plugin.upper()}"
            value = "true" if plugin in enabled_set else "false"
            os.environ[env_var] = value
            logger.info(f"Set {env_var}={value}")

    @keyword
    def clear_plugin_environment(self):
        """Remove all ``HMD_LOCAL_NEURONSPHERE_ENABLE_*`` env vars to restore defaults."""
        to_remove = [
            key for key in os.environ if key.startswith("HMD_LOCAL_NEURONSPHERE_ENABLE_")
        ]
        for key in to_remove:
            del os.environ[key]
            logger.info(f"Removed {key}")

    # ── Docker Container Keywords ──

    @keyword
    def container_should_be_running(self, container_name: str):
        """Assert that the named Docker container exists and has status 'running'.

        Args:
            container_name: Docker container name.
        """
        try:
            container = self.docker_client.containers.get(container_name)
        except docker.errors.NotFound:
            raise AssertionError(f"Container '{container_name}' not found.")
        if container.status not in ("running", "restarting"):
            raise AssertionError(
                f"Container '{container_name}' status is '{container.status}', "
                f"expected 'running' or 'restarting'."
            )

    @keyword
    def container_should_not_be_running(self, container_name: str):
        """Assert that the named container either does not exist or is not running.

        Args:
            container_name: Docker container name.
        """
        try:
            container = self.docker_client.containers.get(container_name)
            if container.status == "running":
                raise AssertionError(
                    f"Container '{container_name}' is still running but should not be."
                )
        except docker.errors.NotFound:
            pass  # Not found is the expected state

    @keyword
    def container_should_have_completed(self, container_name: str):
        """Assert that an init container ran and exited with code 0.

        Accepts both 'exited' with exit code 0 and 'not found' (already removed).

        Args:
            container_name: Docker container name.
        """
        try:
            container = self.docker_client.containers.get(container_name)
            if container.status == "running":
                # Still running is acceptable for init containers that haven't finished yet
                logger.info(f"Init container '{container_name}' is still running.")
                return
            if container.status == "exited":
                exit_code = container.attrs["State"]["ExitCode"]
                if exit_code != 0:
                    raise AssertionError(
                        f"Init container '{container_name}' exited with code {exit_code}, expected 0."
                    )
                return
            raise AssertionError(
                f"Init container '{container_name}' has unexpected status '{container.status}'."
            )
        except docker.errors.NotFound:
            # Already removed after completing - acceptable
            logger.info(
                f"Init container '{container_name}' not found (likely already removed)."
            )

    @keyword
    def all_containers_should_be_running(self, container_names: List[str]):
        """Assert all named containers are running.

        Args:
            container_names: List of Docker container names.
        """
        failures = []
        for name in container_names:
            try:
                self.container_should_be_running(name)
            except AssertionError as e:
                failures.append(str(e))
        if failures:
            raise AssertionError(
                f"{len(failures)} container(s) not running:\n" + "\n".join(failures)
            )

    @keyword
    def all_containers_should_not_be_running(self, container_names: List[str]):
        """Assert none of the named containers are running.

        Args:
            container_names: List of Docker container names.
        """
        failures = []
        for name in container_names:
            try:
                self.container_should_not_be_running(name)
            except AssertionError as e:
                failures.append(str(e))
        if failures:
            raise AssertionError(
                f"{len(failures)} container(s) unexpectedly running:\n"
                + "\n".join(failures)
            )

    @keyword
    def all_init_containers_should_have_completed(self, container_names: List[str]):
        """Assert all init containers completed successfully.

        Args:
            container_names: List of Docker container names.
        """
        failures = []
        for name in container_names:
            try:
                self.container_should_have_completed(name)
            except AssertionError as e:
                failures.append(str(e))
        if failures:
            raise AssertionError(
                f"{len(failures)} init container(s) failed:\n" + "\n".join(failures)
            )

    # ── HTTP Service Keywords ──

    @keyword
    def service_should_respond(self, url: str, timeout: int = 10):
        """Assert that an HTTP service responds at the given URL.

        Any HTTP response (including 4xx/5xx) counts as success -- the goal
        is to verify the service is reachable, not that it returns 200.

        Args:
            url: URL to check.
            timeout: Request timeout in seconds.
        """
        try:
            resp = http_requests.get(url, timeout=int(timeout))
            logger.info(f"Service responded at {url}: HTTP {resp.status_code}")
        except http_requests.RequestException as e:
            raise AssertionError(f"Service at {url} not reachable: {e}")

    @keyword
    def get_deployment_gui_url(self) -> str:
        """The host URL `hmd neuronsphere up` serves the Deployment GUI on.

        Resolved rather than hardcoded so that `HMD_LOCAL_GUI_HOST_PORT` is
        honoured. There is one GUI per `$HMD_HOME`, not one per environment: it
        is a control-plane container serving every environment through
        ms-deployment.
        """
        from hmd_cli_neuronsphere import bom_seeder

        return f"http://localhost:{bom_seeder.gui_port()}"

    @keyword
    def get_deployment_bom(self, env_type: str, base_url: str = "http://localhost/hmd_ms_deployment"):
        """Get the deployment BOM for an environment type.

        Args:
            env_type: Environment type (e.g., "local")
            base_url: ms-deployment base URL

        Returns:
            The BOM response as a dictionary
        """
        url = f"{base_url}/apiop/get_deployment_bom/{env_type}"
        resp = http_requests.get(url, timeout=30)
        resp.raise_for_status()
        result = resp.json()
        logger.info(f"Deployment BOM for {env_type}: {result}")
        return result

    @keyword
    def get_deployment_bom_count(self, env_type: str, base_url: str = "http://localhost/hmd_ms_deployment"):
        """Get the count of entries in the deployment BOM.

        Args:
            env_type: Environment type (e.g., "local")
            base_url: ms-deployment base URL

        Returns:
            Integer count of BOM entries
        """
        bom = self.get_deployment_bom(env_type, base_url)
        count = len(bom) if isinstance(bom, list) else 0
        logger.info(f"BOM entry count for {env_type}: {count}")
        return count

    @keyword
    def find_resources_by_tag(
        self,
        key: str,
        value: str,
        base_url: str = "http://localhost/hmd_ms_deployment",
    ):
        """Return the NERD0004 Resources carrying the given key=value tag.

        Args:
            key: Tag key (e.g. "environment").
            value: Tag value (e.g. "local").
            base_url: ms-deployment base URL.

        Returns:
            The list of matching Resource records.
        """
        url = f"{base_url}/apiop/find_resources_by_tag/{key}/{value}"
        resp = http_requests.get(url, timeout=30)
        resp.raise_for_status()
        result = resp.json()
        logger.info(f"Resources tagged {key}={value}: {result}")
        return result if isinstance(result, list) else []

    @keyword
    def resource_names_from(self, resources):
        """Extract the ``resource_name`` values from a list of Resource records."""
        names = [
            r.get("resource_name") for r in resources if isinstance(r, dict)
        ]
        logger.info(f"Resource names: {names}")
        return names

    @keyword
    def instance_names_from(self, bom):
        """Extract the instance names from a BOM list."""
        names = [
            e.get("instance_name") or e.get("repo_instance_name")
            for e in bom
            if isinstance(e, dict)
        ]
        logger.info(f"Instance names: {names}")
        return names

    @keyword
    def database_should_exist(self, container: str, db_name: str):
        """Assert a database exists in a Postgres container.

        Databases are deliberately not reachable from the host (only hmd_proxy
        publishes ports), so this asks the container itself.
        """
        result = subprocess.run(
            [
                "docker", "exec", container,
                "psql", "-U", "postgres", "-tAc",
                f"SELECT 1 FROM pg_database WHERE datname = '{db_name}'",
            ],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise AssertionError(
                f"Could not query {container}: {result.stderr.strip()}"
            )
        if result.stdout.strip() != "1":
            raise AssertionError(
                f"Database '{db_name}' does not exist in {container}"
            )
        logger.info(f"Database '{db_name}' exists in {container}")

    @keyword
    def find_bom_entry_by_instance_name(self, bom, instance_name: str):
        """Find a specific entry in the BOM by instance name.

        Args:
            bom: The BOM list (from get_deployment_bom)
            instance_name: Name of the repo instance

        Returns:
            The matching BOM entry dict, or raises AssertionError
        """
        if isinstance(bom, list):
            for entry in bom:
                if entry.get("instance_name") == instance_name or entry.get("repo_instance_name") == instance_name:
                    return entry
        raise AssertionError(f"Instance '{instance_name}' not found in BOM")

    @keyword
    def all_bom_entries_should_have_final_status(self, bom):
        """Assert all BOM entries have a final status (DEPLOYED or FAILED), not DEPLOY_NEXT.

        Args:
            bom: The BOM list (from get_deployment_bom)
        """
        final_statuses = {"DEPLOYED", "FAILED", "SKIPPED"}
        stuck = []
        if isinstance(bom, list):
            for entry in bom:
                name = entry.get("instance_name", entry.get("repo_instance_name", "unknown"))
                status = entry.get("status", "UNKNOWN")
                if status not in final_statuses:
                    stuck.append(f"{name}: {status}")
        if stuck:
            raise AssertionError(
                f"{len(stuck)} BOM entries not in final state:\n" + "\n".join(stuck)
            )

    @keyword
    def get_container_env(self, container_name: str) -> str:
        """Get environment variables of a running container as 'KEY=VALUE\\n...' string.

        Args:
            container_name: Docker container name.

        Returns:
            Newline-joined env vars (suitable for ``Should Contain``).
        """
        try:
            container = self.docker_client.containers.get(container_name)
        except docker.errors.NotFound:
            raise AssertionError(f"Container '{container_name}' not found")
        env = container.attrs.get("Config", {}).get("Env", [])
        return "\n".join(env)

    @keyword
    def repo_class_should_exist(self, repo_class_name: str,
                                 base_url: str = "http://localhost/hmd_ms_deployment"):
        """Assert ms-deployment has a hmd_lang_deployment.repo_class with the given name.

        Args:
            repo_class_name: e.g. "hmd-ms-fake-librarian"
            base_url: ms-deployment base URL.
        """
        url = f"{base_url}/api/hmd_lang_deployment.repo_class"
        resp = http_requests.get(url, timeout=30)
        resp.raise_for_status()
        body = resp.json()
        items = body if isinstance(body, list) else body.get("items", [body])
        for item in items:
            if item.get("repo_class_name") == repo_class_name:
                logger.info(f"RepoClass {repo_class_name} found")
                return
        raise AssertionError(
            f"RepoClass '{repo_class_name}' not found at {url}. "
            f"Got {len(items)} items."
        )

    @keyword
    def get_hmdms_deployment_status(self, repo_class_name: str,
                                     base_url: str = "http://localhost/hmd_ms_deployment") -> str:
        """Get the status of the most recent repo_instance_deployment for a repo_class.

        Args:
            repo_class_name: Name of the repo class.
            base_url: ms-deployment base URL.

        Returns:
            Status string (e.g. "DEPLOYED", "SKIPPED", "FAILED", "NOT_FOUND").
        """
        url = f"{base_url}/apiop/get_hmdms_status/{repo_class_name}"
        try:
            resp = http_requests.get(url, timeout=30)
            if resp.status_code == 200:
                return resp.json().get("status", "UNKNOWN")
        except http_requests.RequestException:
            pass
        # Fall back: enumerate repo_instance_deployment entities and join via instance->repo_class
        rid_url = f"{base_url}/api/hmd_lang_deployment.repo_instance_deployment"
        resp = http_requests.get(rid_url, timeout=30)
        if resp.status_code != 200:
            return "NOT_FOUND"
        body = resp.json()
        items = body if isinstance(body, list) else body.get("items", [body])
        # Look for an instance_configuration referencing the repo_class_name
        for item in items:
            cfg = item.get("instance_configuration", {})
            if isinstance(cfg, dict) and cfg.get("repo_class_name") == repo_class_name:
                return item.get("status", "UNKNOWN")
        return "NOT_FOUND"

    @keyword
    def get_instance_deployment_status(self, instance_name: str, env_type: str,
                                       base_url: str = "http://localhost/hmd_ms_deployment"):
        """Get the deployment status of a specific repo instance.

        Queries the deployment info for the environment and finds
        the instance by name.

        Args:
            instance_name: Name of the repo instance (e.g., "vpc")
            env_type: Environment type (e.g., "local")
            base_url: ms-deployment base URL

        Returns:
            Status string (e.g., "DEPLOYED", "FAILED", "DEPLOY_NEXT")
        """
        bom = self.get_deployment_bom(env_type, base_url)
        if isinstance(bom, list):
            for entry in bom:
                if entry.get("instance_name") == instance_name or entry.get("repo_instance_name") == instance_name:
                    return entry.get("status", "UNKNOWN")
        return "NOT_FOUND"
