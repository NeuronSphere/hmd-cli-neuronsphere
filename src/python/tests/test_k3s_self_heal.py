"""Unit tests for the k3s cluster self-heal in ``ensure_k3s_cluster``.

Floci pins the k3s node image into its *persistent* cluster record at creation
time. A cluster created before the wrapper image was wired up (or against a
stale/old image) keeps respawning that image and crash-loops on the bad
``--kube-apiserver-arg=storage-backend`` flag that Floci hands raw k3s. Because
``create_cluster`` is idempotency-blocked (``ResourceInUseException``), the
deployer would otherwise trust the broken cluster forever.

These tests lock in the recovery contract:

* a healthy, wrapper-imaged, running cluster is a no-op (no recreate);
* a stale cluster (wrong image OR not running) is deleted and recreated so
  Floci respawns from the current ``FLOCI_SERVICES_EKS_DEFAULT_IMAGE``;
* a fresh (not-yet-existing) cluster is simply created.

Run directly (``python -m pytest src/python/tests/test_k3s_self_heal.py``) or via
unittest — no service or Docker required.
"""

import unittest
from unittest import mock

from botocore.exceptions import ClientError

from hmd_cli_neuronsphere import floci_deployer as fd


def _in_use_error() -> ClientError:
    return ClientError(
        {"Error": {"Code": "ResourceInUseException", "Message": "exists"}},
        "CreateCluster",
    )


class EnsureK3sClusterSelfHeal(unittest.TestCase):
    def _eks(self, create_side_effect):
        eks = mock.MagicMock()
        eks.create_cluster.side_effect = create_side_effect
        eks.describe_cluster.return_value = {"cluster": {"status": "ACTIVE"}}
        return eks

    def test_fresh_cluster_created(self):
        """No existing cluster -> single create_cluster, no delete."""
        eks = self._eks(create_side_effect=[None])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "_get_client", return_value=eks
        ), mock.patch.object(fd, "delete_k3s_cluster") as delete:
            fd.ensure_k3s_cluster(name="neuronsphere")
        self.assertEqual(eks.create_cluster.call_count, 1)
        delete.assert_not_called()

    def test_healthy_existing_cluster_is_noop(self):
        """Existing cluster on the wrapper image and running -> no recreate."""
        eks = self._eks(create_side_effect=[_in_use_error()])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "_get_client", return_value=eks
        ), mock.patch.object(
            fd, "_k3s_container_image", return_value=fd.K3S_WRAPPER_IMAGE
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=True
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.ensure_k3s_cluster(name="neuronsphere")
        # create attempted once (got InUse), no recreate, no delete.
        self.assertEqual(eks.create_cluster.call_count, 1)
        delete.assert_not_called()

    def test_stale_wrong_image_is_recreated(self):
        """Existing cluster pinned to the wrong image -> delete + recreate."""
        eks = self._eks(create_side_effect=[_in_use_error(), None])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "_get_client", return_value=eks
        ), mock.patch.object(
            fd, "_k3s_container_image", return_value="rancher/k3s:latest"
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=True
        ), mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.ensure_k3s_cluster(name="neuronsphere")
        delete.assert_called_once_with("neuronsphere")
        self.assertEqual(eks.create_cluster.call_count, 2)

    def test_stale_stopped_container_is_recreated(self):
        """Existing cluster whose container crashed/stopped -> delete + recreate."""
        eks = self._eks(create_side_effect=[_in_use_error(), None])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "_get_client", return_value=eks
        ), mock.patch.object(
            fd, "_k3s_container_image", return_value=fd.K3S_WRAPPER_IMAGE
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=False
        ), mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.ensure_k3s_cluster(name="neuronsphere")
        delete.assert_called_once_with("neuronsphere")
        self.assertEqual(eks.create_cluster.call_count, 2)

    def test_missing_container_is_recreated(self):
        """Cluster record exists but no container spawned -> delete + recreate."""
        eks = self._eks(create_side_effect=[_in_use_error(), None])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "_get_client", return_value=eks
        ), mock.patch.object(
            fd, "_k3s_container_image", return_value=""
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=False
        ), mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.ensure_k3s_cluster(name="neuronsphere")
        delete.assert_called_once_with("neuronsphere")
        self.assertEqual(eks.create_cluster.call_count, 2)

    def test_unexpected_client_error_propagates(self):
        """A non-InUse error from create_cluster is not swallowed."""
        boom = ClientError(
            {"Error": {"Code": "AccessDeniedException", "Message": "no"}},
            "CreateCluster",
        )
        eks = self._eks(create_side_effect=[boom])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "_get_client", return_value=eks
        ):
            with self.assertRaises(ClientError):
                fd.ensure_k3s_cluster(name="neuronsphere")


if __name__ == "__main__":
    unittest.main()
