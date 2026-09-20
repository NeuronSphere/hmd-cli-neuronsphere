"""Unit tests for the k3s container self-heal in ``reconcile_k3s_container``.

Cluster *creation* moved to a real Terraform resource (the ``eks-cluster`` /
hmd-inf-eks-cluster Phase A DAG node's ``src/local/cdktf`` overlay) -- Terraform's
own refresh reconciles the EKS API record on every ``apply``. What it cannot see
is the Docker container/volume Floci backs that record with:

Floci pins the k3s node image into its *persistent* cluster record at creation
time. A cluster created before the wrapper image was wired up (or against a
stale/old image) keeps respawning that image and crash-loops on the bad
``--kube-apiserver-arg=storage-backend`` flag that Floci hands raw k3s -- and
none of that is drift on any ``aws_eks_cluster`` attribute Terraform tracks.

These tests lock in the recovery contract:

* no cluster exists yet -> nothing to reconcile; the DAG node creates it fresh.
* a healthy, wrapper-imaged, running cluster is a no-op (no recreate);
* a *stopped* container on the expected image is restarted in place, never
  recreated -- that is what a non-purge ``down`` leaves behind, and recreating
  it would drop the datastore and force a full BOM redeploy on the next ``up``;
* ... unless the restart fails (e.g. its Docker network was removed), in which
  case recreating is the only way forward;
* a stale cluster (wrong image, or a missing container) is deleted so the DAG
  node's next ``apply`` refreshes to "not found" and creates it fresh from the
  current ``FLOCI_SERVICES_EKS_DEFAULT_IMAGE``.

Run directly (``python -m pytest src/python/tests/test_k3s_self_heal.py``) or via
unittest -- no service or Docker required.
"""

import unittest
from unittest import mock

from botocore.exceptions import ClientError

from hmd_cli_neuronsphere import floci_deployer as fd

# What the *running* Floci is configured to spawn. Deliberately not the
# `ghcr.io/neuronsphere` compose default: the registry is a cement config value
# that never reaches os.environ, so reconstructing the expectation from the
# environment alone produced a permanent mismatch against a perfectly healthy
# container -- see `configured_k3s_wrapper_image`.
_EXPECTED = "ghcr.io/hmdlabs/hmd-img-k3s-floci:0.3.4"


class _ResourceNotFoundException(Exception):
    pass


def _eks(describe_side_effect):
    eks = mock.MagicMock()
    eks.exceptions.ResourceNotFoundException = _ResourceNotFoundException
    eks.describe_cluster.side_effect = describe_side_effect
    return eks


class ReconcileK3sContainerSelfHeal(unittest.TestCase):
    def _assert_recreated(self, delete, wait_gone, name="neuronsphere", target=None):
        """The stale cluster was deleted from the right account.

        ``target`` is load-bearing, not incidental: each named environment owns
        a cluster in its *own* Floci account, so a delete that defaulted to the
        control plane would leave the stale cluster running and try to remove
        someone else's. Omitted here means the control plane, which is what
        ``reconcile_k3s_container`` resolves when given no target.
        """
        expected_target = target if target is not None else fd.control_plane_target()
        delete.assert_called_once_with(name, target=expected_target)
        wait_gone.assert_called_once_with(name, target=expected_target)

    def test_nonexistent_cluster_is_left_alone(self):
        """No cluster yet -> nothing to reconcile; the DAG node creates it."""
        eks = _eks(describe_side_effect=[_ResourceNotFoundException("no")])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "configured_k3s_wrapper_image", return_value=_EXPECTED
        ), mock.patch.object(fd, "_get_client", return_value=eks), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete, mock.patch.object(
            fd, "start_k3s_container"
        ) as start:
            fd.reconcile_k3s_container(name="neuronsphere")
        delete.assert_not_called()
        start.assert_not_called()

    def test_healthy_existing_cluster_is_noop(self):
        """Existing cluster on the wrapper image and running -> no action."""
        eks = _eks(describe_side_effect=[{"cluster": {"status": "ACTIVE"}}])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "configured_k3s_wrapper_image", return_value=_EXPECTED
        ), mock.patch.object(fd, "_get_client", return_value=eks), mock.patch.object(
            fd, "_k3s_container_image", return_value=_EXPECTED
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=True
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete, mock.patch.object(
            fd, "start_k3s_container"
        ) as start:
            fd.reconcile_k3s_container(name="neuronsphere")
        delete.assert_not_called()
        start.assert_not_called()

    def test_stale_wrong_image_is_recreated(self):
        """Existing cluster pinned to the wrong image -> delete only."""
        eks = _eks(describe_side_effect=[{"cluster": {"status": "ACTIVE"}}])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "configured_k3s_wrapper_image", return_value=_EXPECTED
        ), mock.patch.object(fd, "_get_client", return_value=eks), mock.patch.object(
            fd, "_k3s_container_image", return_value="rancher/k3s:latest"
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=True
        ), mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ) as wait_gone, mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.reconcile_k3s_container(name="neuronsphere")
        self._assert_recreated(delete, wait_gone)

    def test_stopped_container_is_restarted_not_recreated(self):
        """A container stopped by a non-purge `down` -> docker start, no delete.

        This is the whole point of stopping rather than deleting the cluster:
        the datastore, the `kube-system` UID and every Helm release survive, so
        the next `up` reconciles instead of redeploying the entire BOM.
        """
        eks = _eks(describe_side_effect=[{"cluster": {"status": "ACTIVE"}}])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "configured_k3s_wrapper_image", return_value=_EXPECTED
        ), mock.patch.object(fd, "_get_client", return_value=eks), mock.patch.object(
            fd, "_k3s_container_image", return_value=_EXPECTED
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=False
        ), mock.patch.object(
            fd, "start_k3s_container", return_value=True
        ) as start, mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.reconcile_k3s_container(name="neuronsphere")
        start.assert_called_once_with("neuronsphere", target=fd.control_plane_target())
        delete.assert_not_called()

    def test_stopped_container_recreated_when_restart_fails(self):
        """A stopped container that will not start -> fall back to recreate.

        `down --purge` removes the Docker network, and a stopped endpoint pins
        it by id, so `docker start` can legitimately fail. Recreating is then
        the only way to get a cluster at all.
        """
        eks = _eks(describe_side_effect=[{"cluster": {"status": "ACTIVE"}}])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "configured_k3s_wrapper_image", return_value=_EXPECTED
        ), mock.patch.object(fd, "_get_client", return_value=eks), mock.patch.object(
            fd, "_k3s_container_image", return_value=_EXPECTED
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=False
        ), mock.patch.object(
            fd, "start_k3s_container", return_value=False
        ) as start, mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ) as wait_gone, mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.reconcile_k3s_container(name="neuronsphere")
        start.assert_called_once_with("neuronsphere", target=fd.control_plane_target())
        self._assert_recreated(delete, wait_gone)

    def test_missing_container_is_recreated(self):
        """Cluster record exists but no container spawned -> delete, no start."""
        eks = _eks(describe_side_effect=[{"cluster": {"status": "ACTIVE"}}])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "configured_k3s_wrapper_image", return_value=_EXPECTED
        ), mock.patch.object(fd, "_get_client", return_value=eks), mock.patch.object(
            fd, "_k3s_container_image", return_value=""
        ), mock.patch.object(
            fd, "_existing_container_names", return_value=set()
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=False
        ), mock.patch.object(
            fd, "start_k3s_container"
        ) as start, mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ) as wait_gone, mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.reconcile_k3s_container(name="neuronsphere")
        # Nothing to start -- there is no container.
        start.assert_not_called()
        self._assert_recreated(delete, wait_gone)

    def test_recreate_deletes_from_the_same_account_it_reconciles(self):
        """A named environment's stale cluster is deleted from *its* Floci.

        Defaulting the delete to the control plane would leave the
        environment's broken cluster running while addressing someone else's.
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
        eks = _eks(describe_side_effect=[{"cluster": {"status": "ACTIVE"}}])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "configured_k3s_wrapper_image", return_value=_EXPECTED
        ), mock.patch.object(fd, "_get_client", return_value=eks), mock.patch.object(
            fd, "_k3s_container_image", return_value="rancher/k3s:latest"
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=True
        ), mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ) as wait_gone, mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.reconcile_k3s_container(name="ns-dev2-abc", target=target)
        self._assert_recreated(delete, wait_gone, name="ns-dev2-abc", target=target)

    def test_unreadable_container_is_left_alone(self):
        """`docker inspect` failed on a container that exists -> do not recreate.

        `_k3s_container_image` returns "" both for a container that is gone and
        for one it could not inspect (daemon hiccup, timeout). Only the first is
        staleness; treating the second as stale would delete a live cluster and
        its datastore -- every Helm release on it -- over a transient failure.
        """
        eks = _eks(describe_side_effect=[{"cluster": {"status": "ACTIVE"}}])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "configured_k3s_wrapper_image", return_value=_EXPECTED
        ), mock.patch.object(fd, "_get_client", return_value=eks), mock.patch.object(
            fd, "_k3s_container_image", return_value=""
        ), mock.patch.object(
            fd, "_existing_container_names", return_value={"floci-eks-neuronsphere"}
        ), mock.patch.object(
            fd, "_k3s_container_running", return_value=False
        ), mock.patch.object(
            fd, "start_k3s_container"
        ) as start, mock.patch.object(
            fd, "_wait_for_cluster_gone"
        ), mock.patch.object(
            fd, "delete_k3s_cluster"
        ) as delete:
            fd.reconcile_k3s_container(name="neuronsphere")
        start.assert_not_called()
        delete.assert_not_called()

    def test_unexpected_describe_error_propagates(self):
        """A non-"not found" error from describe_cluster is not swallowed."""
        boom = ClientError(
            {"Error": {"Code": "AccessDeniedException", "Message": "no"}},
            "DescribeCluster",
        )
        eks = _eks(describe_side_effect=[boom])
        with mock.patch.object(fd, "ensure_k3s_wrapper_image"), mock.patch.object(
            fd, "configured_k3s_wrapper_image", return_value=_EXPECTED
        ), mock.patch.object(fd, "_get_client", return_value=eks):
            with self.assertRaises(ClientError):
                fd.reconcile_k3s_container(name="neuronsphere")


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


class ConfiguredK3sWrapperImageTests(unittest.TestCase):
    """`HMD_LOCAL_NS_CONTAINER_REGISTRY` is a cement config value, not an ambient
    environment variable: compose reads it from `$HMD_HOME/.config/hmd.env` and
    substitutes it into `FLOCI_SERVICES_EKS_DEFAULT_IMAGE`, but it never reaches
    this process's `os.environ`.

    Rebuilding the expectation from `os.environ` alone therefore fell back to the
    `ghcr.io/neuronsphere` default while Floci was spawning `ghcr.io/hmdlabs/...`,
    and `reconcile_k3s_container` destroyed the cluster *and its volume* on every
    single `up` -- only for Floci to respawn the very same image again. The
    running container is the one place compose's substitution is recorded, so it
    is what these tests pin.
    """

    def test_explicit_override_wins(self):
        with mock.patch.dict(
            fd.os.environ, {"HMD_LOCAL_K3S_WRAPPER_IMAGE": "local/k3s:dev"}, clear=False
        ), mock.patch.object(fd, "floci_env_value") as read:
            self.assertEqual(fd.configured_k3s_wrapper_image(), "local/k3s:dev")
        read.assert_not_called()

    def test_running_floci_beats_the_reconstructed_default(self):
        """The regression itself: no registry in os.environ, hmdlabs in Floci."""
        env = {
            k: v
            for k, v in fd.os.environ.items()
            if k
            not in ("HMD_LOCAL_K3S_WRAPPER_IMAGE", "HMD_LOCAL_NS_CONTAINER_REGISTRY")
        }
        with mock.patch.dict(fd.os.environ, env, clear=True), mock.patch.object(
            fd, "floci_env_value", return_value=_EXPECTED
        ) as read:
            self.assertEqual(fd.configured_k3s_wrapper_image(), _EXPECTED)
        read.assert_called_once_with("FLOCI_SERVICES_EKS_DEFAULT_IMAGE")

    def test_falls_back_to_the_compose_default_when_floci_is_down(self):
        """Nothing to read back yet -- mirror what compose would substitute."""
        env = {
            k: v
            for k, v in fd.os.environ.items()
            if k not in ("HMD_LOCAL_K3S_WRAPPER_IMAGE",)
        }
        env["HMD_LOCAL_NS_CONTAINER_REGISTRY"] = "ghcr.io/example"
        with mock.patch.dict(fd.os.environ, env, clear=True), mock.patch.object(
            fd, "floci_env_value", return_value=None
        ):
            self.assertEqual(
                fd.configured_k3s_wrapper_image(),
                "ghcr.io/example/hmd-img-k3s-floci:0.3.4",
            )
