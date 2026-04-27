"""kubectl wrapper for tests against the local Floci EKS k3s cluster."""

import json
import os
import subprocess
from pathlib import Path

from robot.api import logger
from robot.api.deco import keyword, library


def _kubeconfig_path() -> str:
    return os.environ.get(
        "KUBECONFIG",
        os.path.join(
            os.environ.get("HMD_HOME", os.path.expanduser("~/.hmd")),
            ".cache",
            "k3s",
            "kubeconfig",
        ),
    )


@library
class K3sLib:
    """Thin wrapper around `kubectl` for tests."""

    @keyword
    def kubectl(self, *args, timeout_sec: int = 60) -> str:
        """Run `kubectl <args>` against the cached kubeconfig and return stdout."""
        cmd = ["kubectl", "--kubeconfig", _kubeconfig_path(), *args]
        logger.info(f"+ {' '.join(cmd)}")
        result = subprocess.run(
            cmd, capture_output=True, text=True, timeout=timeout_sec
        )
        if result.returncode != 0:
            raise AssertionError(
                f"kubectl failed (exit={result.returncode}): {result.stderr}"
            )
        return result.stdout

    @keyword
    def kubeconfig_should_exist(self):
        """Assert the kubeconfig was written to disk."""
        path = Path(_kubeconfig_path())
        if not path.exists():
            raise AssertionError(f"kubeconfig not found at {path}")
        return str(path)

    @keyword
    def k3s_should_have_ready_node(self):
        """Assert at least one Ready node in the cluster."""
        stdout = self.kubectl("get", "nodes", "-o", "json")
        nodes = json.loads(stdout).get("items", [])
        if not nodes:
            raise AssertionError("k3s has no nodes")
        for n in nodes:
            for cond in n.get("status", {}).get("conditions", []):
                if cond.get("type") == "Ready" and cond.get("status") == "True":
                    return
        raise AssertionError("No node in Ready status")

    @keyword
    def argo_pods_should_be_running(self, namespace: str = "argo"):
        """Assert workflow-controller and argo-server pods are Running."""
        stdout = self.kubectl("get", "pods", "-n", namespace, "-o", "json")
        pods = json.loads(stdout).get("items", [])
        running = {
            p["metadata"]["name"]: p["status"].get("phase")
            for p in pods
            if p["status"].get("phase") == "Running"
        }
        wanted = ("workflow-controller", "argo-server")
        missing = [
            w for w in wanted if not any(name.startswith(w) for name in running)
        ]
        if missing:
            raise AssertionError(
                f"Argo pods not Running in ns={namespace}: missing {missing}; saw {list(running)}"
            )
