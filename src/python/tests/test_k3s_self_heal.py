"""Unit tests for the k3s cluster self-heal in ``ensure_k3s_cluster``.

Floci pins the k3s node image into its *persistent* cluster record at creation
time. A cluster created before the wrapper image was wired up (or against a
stale/old image) keeps respawning that image and crash-loops on the bad
``--kube-apiserver-arg=storage-backend`` flag that Floci hands raw k3s. Because
``create_cluster`` is idempotency-blocked (``ResourceInUseException``), the
deployer would otherwise trust the broken cluster forever.

These tests lock in the recovery contract:

* a healthy, wrapper-imaged, running cluster is a no-op (no recreate);
* a *stopped* container on the expected image is restarted in place, never
  recreated -- that is what a non-purge ``down`` leaves behind, and recreating
  it would drop the datastore and force a full BOM redeploy on the next ``up``;
* ... unless the restart fails (e.g. its Docker network was removed), in which
  case recreating is the only way forward;
* a stale cluster (wrong image, or a missing container) is deleted and recreated
  so Floci respawns from the current ``FLOCI_SERVICES_EKS_DEFAULT_IMAGE``;
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

    def _assert_recreated(self, delete, eks, name="neuronsphere", target=None):
        """The stale cluster was deleted from the right account, then recreated.

        ``target`` is load-bearing, not incidental: each named environment owns
        a cluster in its *own* Floci account, so a delete that defaulted to the
        control plane would leave the stale cluster running and try to remove
        someone else's. It must be the same target the create was issued
        against -- omitted here means the control plane, which is what
        ``ensure_k3s_cluster`` resolves when given no target.
        """
        delete.assert_called_once_with(
            name, target=target if target is not None else fd.control_plane_target()
        )
        self.assertEqual(eks.create_cluster.call_count, 2)

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
        self._assert_recreated(delete, eks)

    def test_stopped_container_is_restarted_not_recreated(self):
        """A container stopped by a non-purge `down` -> docker start, no recreate.

        This is the whole point of stopping rather than deleting the cluster:
        the datastore, the `kube-system` UID and every Helm release survive, so
        the next `up` reconciles instead of redeploying the entire BOM.
        """
        eks = self._eks(create_side_effect=[_in_use_error()])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "_get_client", return_value=eks
        ), mock.patch.object(
            fd, "_k3s_container_image", return_value=fd.K3S_WRAPPER_IMAGE
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=False
        ), mock.patch.object(
            fd, "start_k3s_container", return_value=True
        ) as start, mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.ensure_k3s_cluster(name="neuronsphere")
        # The account-selecting target must reach the docker-name resolver:
        # an environment's container is `floci-eks-<account>.<cluster>`.
        start.assert_called_once_with("neuronsphere", target=fd.control_plane_target())
        delete.assert_not_called()
        self.assertEqual(eks.create_cluster.call_count, 1)

    def test_stopped_container_recreated_when_restart_fails(self):
        """A stopped container that will not start -> fall back to recreate.

        `down --purge` removes the Docker network, and a stopped endpoint pins
        it by id, so `docker start` can legitimately fail. Recreating is then
        the only way to get a cluster at all.
        """
        eks = self._eks(create_side_effect=[_in_use_error(), None])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "_get_client", return_value=eks
        ), mock.patch.object(
            fd, "_k3s_container_image", return_value=fd.K3S_WRAPPER_IMAGE
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=False
        ), mock.patch.object(
            fd, "start_k3s_container", return_value=False
        ) as start, mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.ensure_k3s_cluster(name="neuronsphere")
        # The account-selecting target must reach the docker-name resolver:
        # an environment's container is `floci-eks-<account>.<cluster>`.
        start.assert_called_once_with("neuronsphere", target=fd.control_plane_target())
        self._assert_recreated(delete, eks)

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
            fd, "start_k3s_container"
        ) as start, mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.ensure_k3s_cluster(name="neuronsphere")
        # Nothing to start -- there is no container.
        start.assert_not_called()
        self._assert_recreated(delete, eks)

    def test_recreate_deletes_from_the_same_account_it_creates_in(self):
        """A named environment's stale cluster is deleted from *its* Floci.

        The delete and the create must address the same account. Defaulting the
        delete to the control plane would leave the environment's broken cluster
        running while the recreate hit ``ResourceInUseException`` forever.
        """
        target = fd.FlociTarget(
            name="dev2",
            endpoint="http://localhost:19004",
            internal_endpoint="http://floci-dev2:4566",
            account_id="000000000002",
            container="floci-dev2",
            alias="neuronsphere-dev2",
            region="us-west-2",
        )
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
            fd.ensure_k3s_cluster(name="ns-dev2-abc", target=target)
        self._assert_recreated(delete, eks, name="ns-dev2-abc", target=target)

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


class K3sContainerNamingTests(unittest.TestCase):
    """Floci 2.0 qualifies the k3s container name by account.

        defaultAccount ? cluster.getName() : accountId + "." + cluster.getName()

    Getting this wrong is not cosmetic. `write_kubeconfig` reads the real
    kubeconfig with `docker exec` on this name; when that fails it falls through
    to a synthesized config carrying a placeholder token, so every later kubectl
    call fails with "the server has asked for the client to provide credentials"
    -- which reads like a broken cluster, not a naming mistake. It also silently
    breaks CoreDNS records, the Traefik patch and NodePort routing.
    """

    class _Env:
        legacy_layout = False

        def __init__(self, account_id):
            self.slug = "dev2"
            self.account_id = account_id

    def test_the_control_plane_keeps_the_unqualified_name(self):
        name = fd.k3s_container_name("ns-local", fd.control_plane_target())
        self.assertEqual(name, "floci-eks-ns-local")

    def test_an_environment_gets_an_account_qualified_name(self):
        target = fd.env_target(self._Env("000000000001"))
        with mock.patch.object(fd, "_existing_container_names", return_value=set()):
            name = fd.k3s_container_name("ns-local", target)
        self.assertEqual(name, "floci-eks-000000000001.ns-local")

    def test_a_pre_2_0_container_keeps_its_legacy_name(self):
        """Floci claims such a container itself when the account label matches,
        so recreating under the qualified name would orphan its workloads."""
        target = fd.env_target(self._Env("000000000001"))
        with mock.patch.object(
            fd, "_existing_container_names", return_value={"floci-eks-ns-local"}
        ):
            name = fd.k3s_container_name("ns-local", target)
        self.assertEqual(name, "floci-eks-ns-local")

    def test_the_qualified_container_wins_when_both_exist(self):
        target = fd.env_target(self._Env("000000000001"))
        with mock.patch.object(
            fd,
            "_existing_container_names",
            return_value={"floci-eks-ns-local", "floci-eks-000000000001.ns-local"},
        ):
            name = fd.k3s_container_name("ns-local", target)
        self.assertEqual(name, "floci-eks-000000000001.ns-local")

    def test_two_accounts_never_share_a_container_name(self):
        a = fd.k3s_container_name("ns-local", fd.env_target(self._Env("000000000001")))
        b = fd.k3s_container_name("ns-local", fd.env_target(self._Env("000000000002")))
        self.assertNotEqual(a, b)
