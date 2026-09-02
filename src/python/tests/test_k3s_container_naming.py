"""Every Docker call about a k3s cluster must name the right container.

Floci 2.0 qualifies the name of the container (and volume) it spawns by account
for every account except the default one::

    defaultAccount ? cluster.getName() : accountId + "." + cluster.getName()

Since each named environment is its own Floci *account*, an environment's k3s
container is ``floci-eks-<account>.<cluster>`` while the control plane's stays
``floci-eks-<cluster>``. Assuming the unqualified form is not cosmetic: the
``docker exec`` that fetches the real kubeconfig fails, ``write_kubeconfig``
falls through to a synthesized config carrying a placeholder token, and every
later ``kubectl`` call dies with a bare ``401 Unauthorized`` that reads like a
broken cluster rather than a misspelled name.

These tests run the helpers *for real* against a mocked ``docker``, rather than
patching them out. That distinction is the point of the file: the previous
suite patched ``_k3s_container_image``/``_k3s_container_running`` wholesale, so
it never noticed that their bodies referenced a ``target`` they had no parameter
for -- a ``NameError`` on the "cluster already exists" path, i.e. every restart.
"""

import unittest
from unittest import mock

from hmd_cli_neuronsphere import floci_deployer as fd


class _Env:
    """The minimum of ``env_registry.LocalEnvironment`` ``env_target`` reads."""

    slug = "local"
    account_id = "000000000001"
    k3s_cluster = "ns-local-57aa833c"
    legacy_layout = False


ENV = _Env()
QUALIFIED = "floci-eks-000000000001.ns-local-57aa833c"
LEGACY = "floci-eks-ns-local-57aa833c"


def _docker(stdout: str = "", returncode: int = 0):
    """Patch subprocess.run and capture the argv of every docker invocation."""
    calls = []

    def run(args, **kwargs):
        calls.append(list(args))
        return mock.Mock(returncode=returncode, stdout=stdout, stderr="")

    return mock.patch.object(fd.subprocess, "run", side_effect=run), calls


class ContainerNameResolution(unittest.TestCase):
    def test_environment_cluster_is_account_qualified(self):
        target = fd.env_target(ENV)
        with mock.patch.object(
            fd, "_existing_container_names", return_value={QUALIFIED}
        ):
            self.assertEqual(fd.k3s_container_name(ENV.k3s_cluster, target), QUALIFIED)

    def test_control_plane_cluster_is_not_qualified(self):
        target = fd.control_plane_target()
        self.assertEqual(
            fd.k3s_container_name("neuronsphere", target), "floci-eks-neuronsphere"
        )

    def test_pre_2_0_container_keeps_working(self):
        """Floci re-adopts a container created under the old name, so we must too."""
        target = fd.env_target(ENV)
        with mock.patch.object(fd, "_existing_container_names", return_value={LEGACY}):
            self.assertEqual(fd.k3s_container_name(ENV.k3s_cluster, target), LEGACY)


class HelpersAddressTheRightContainer(unittest.TestCase):
    """Each helper is called for real; only ``docker`` is mocked."""

    def setUp(self):
        self.target = fd.env_target(ENV)
        patcher = mock.patch.object(
            fd, "_existing_container_names", return_value={QUALIFIED}
        )
        patcher.start()
        self.addCleanup(patcher.stop)

    def _assert_names_qualified(self, fn, *args, **kwargs):
        patcher, calls = _docker(stdout="ghcr.io/x:1")
        with patcher:
            fn(*args, **kwargs)
        self.assertTrue(calls, f"{fn.__name__} made no docker call")
        for argv in calls:
            self.assertIn(
                QUALIFIED,
                argv,
                f"{fn.__name__} addressed {argv}, not {QUALIFIED}",
            )

    def test_container_image(self):
        self._assert_names_qualified(
            fd._k3s_container_image, ENV.k3s_cluster, self.target
        )

    def test_container_running(self):
        self._assert_names_qualified(
            fd._k3s_container_running, ENV.k3s_cluster, self.target
        )

    def test_host_port(self):
        self._assert_names_qualified(fd._k3s_host_port, ENV.k3s_cluster, self.target)

    def test_container_logs(self):
        self._assert_names_qualified(
            fd._k3s_container_logs, ENV.k3s_cluster, self.target
        )

    def test_stop(self):
        self._assert_names_qualified(
            fd.stop_k3s_cluster, ENV.k3s_cluster, target=self.target
        )

    def test_start(self):
        self._assert_names_qualified(
            fd.start_k3s_container, ENV.k3s_cluster, target=self.target
        )

    def test_eks_ip(self):
        self._assert_names_qualified(
            fd._floci_eks_ip, ENV.k3s_cluster, target=self.target
        )


class VolumePurge(unittest.TestCase):
    """A missed volume is worse than a missed container.

    A leftover ``/var/lib/rancher/k3s`` volume is adopted by the next ``up``,
    whose respawned container registers as a brand-new Node while the previous
    one lingers forever as NotReady -- so pods pinned to the dead node can never
    schedule. ``docker volume rm`` on a name that does not exist is a no-op, so
    purge names both forms rather than guessing.
    """

    def test_purge_removes_both_volume_names(self):
        target = fd.env_target(ENV)
        with mock.patch.object(
            fd, "_existing_container_names", return_value={QUALIFIED}
        ):
            patcher, calls = _docker()
            with patcher:
                fd.purge_k3s_container_and_volume(ENV.k3s_cluster, target=target)
        volume_rm = [c for c in calls if c[:3] == ["docker", "volume", "rm"]]
        self.assertEqual(len(volume_rm), 1)
        self.assertIn(QUALIFIED, volume_rm[0])
        self.assertIn(LEGACY, volume_rm[0])

    def test_control_plane_purge_names_only_its_own_volume(self):
        self.assertEqual(
            fd.k3s_volume_candidates("neuronsphere", fd.control_plane_target()),
            ["floci-eks-neuronsphere"],
        )


if __name__ == "__main__":
    unittest.main()
