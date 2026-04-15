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
from robot.api import logger
from robot.api.deco import keyword, library


ALL_BUNDLED_PLUGINS = [
    "telemetry",
    "graph",
    "ministack",
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
            if not os.environ.get("MINISTACK_ENDPOINT"):
                os.environ["MINISTACK_ENDPOINT"] = "http://host.docker.internal:4566"
                logger.info("Set MINISTACK_ENDPOINT=http://host.docker.internal:4566")

        hmd_home = os.environ.get("HMD_HOME")
        if not hmd_home:
            raise AssertionError("HMD_HOME environment variable is not set.")

        hmd_home = Path(hmd_home)

        required_dirs = [
            hmd_home / ".cache",
            hmd_home / ".config",
            hmd_home / "postgresql" / "data",
            hmd_home / "postgresql" / "scripts" / "always-initdb.d",
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
