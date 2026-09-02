"""Traefik's baked manifest must actually get patched, or no UI is reachable.

The k3s image bakes a rendered Traefik manifest that requests `hostPort: 80` and
`443`. k3s ServiceLB creates `svclb-*` pods for every `type: LoadBalancer`
service and those are `system-node-critical`, so a chart exposing 443 (the OTEL
collector gateway does) permanently preempts Traefik -- priority 0 -- off that
port:

    0/1 nodes are available: 1 node(s) didn't have free ports for the
    requested pod ports

Traefik needs no host port locally; it is reached through a NodePort
(`nginx_router.ingress_upstream`). The patch that strips those ports has existed
all along -- it just never ran: the container was resolved *without* `env`,
producing the unqualified name, which does not exist for an environment's
cluster (Floci 2.0 qualifies it by account). Every `docker exec` failed, the
return codes were discarded, and the function was a silent no-op whose only
symptom appeared several layers away as an unreachable
`superset.local.neuronsphere.io`.
"""

import types
import unittest
from unittest import mock

from hmd_cli_neuronsphere import k3s_operators as ko


def _env():
    return types.SimpleNamespace(
        slug="local",
        account_id="000000000001",
        deployment_id="local",
        k3s_cluster="ns-local-57aa833c",
        legacy_layout=False,
    )


QUALIFIED = "floci-eks-000000000001.ns-local-57aa833c"


class ThePatchAddressesTheRightContainer(unittest.TestCase):
    def test_environment_container_is_account_qualified(self):
        calls = []

        def fake_exec(container, args):
            calls.append(container)
            return mock.Mock(returncode=0, stdout="", stderr="")

        with mock.patch.object(ko, "_k3s_container", return_value=QUALIFIED) as resolve:
            with mock.patch.object(ko, "_docker_exec", side_effect=fake_exec):
                ko._patch_traefik_manifest("ns-local-57aa833c", _env())

        # The environment must reach the resolver, or it returns the
        # control-plane name and every exec below silently fails.
        self.assertIsNotNone(resolve.call_args.args[1])
        self.assertTrue(calls)
        self.assertEqual(set(calls), {QUALIFIED})


class FailuresAreReportedNotSwallowed(unittest.TestCase):
    def test_a_failed_strip_warns_and_names_the_consequence(self):
        failure = mock.Mock(returncode=1, stdout="", stderr="no such container")
        with mock.patch.object(ko, "_k3s_container", return_value="wrong-name"):
            with mock.patch.object(ko, "_docker_exec", return_value=failure):
                with self.assertLogs(level="WARNING") as logs:
                    ko._patch_traefik_manifest("ns-local-57aa833c", _env())
        joined = "\n".join(logs.output)
        self.assertIn("Pending", joined)
        self.assertIn("no UI will be reachable", joined)

    def test_docker_unavailable_warns_too(self):
        with mock.patch.object(ko, "_k3s_container", return_value="c"):
            with mock.patch.object(ko, "_docker_exec", return_value=None):
                with self.assertLogs(level="WARNING") as logs:
                    ko._patch_traefik_manifest("ns-local-57aa833c", _env())
        self.assertIn("docker unavailable", "\n".join(logs.output))

    def test_a_failed_strip_does_not_go_on_to_the_second_edit(self):
        """The second edit would fail the same way; one clear warning beats two."""
        failure = mock.Mock(returncode=1, stdout="", stderr="boom")
        with mock.patch.object(ko, "_k3s_container", return_value="c"):
            with mock.patch.object(ko, "_docker_exec", return_value=failure) as exec_:
                with self.assertLogs(level="WARNING"):
                    ko._patch_traefik_manifest("ns-local-57aa833c", _env())
        self.assertEqual(exec_.call_count, 1)


class TheIngressClassArgIsAdded(unittest.TestCase):
    def test_missing_arg_is_inserted(self):
        seen = []

        def fake_exec(container, args):
            seen.append(args)
            if args[0] == "grep":
                return mock.Mock(returncode=1, stdout="", stderr="")  # not present
            return mock.Mock(returncode=0, stdout="", stderr="")

        with mock.patch.object(ko, "_k3s_container", return_value=QUALIFIED):
            with mock.patch.object(ko, "_docker_exec", side_effect=fake_exec):
                ko._patch_traefik_manifest("ns-local-57aa833c", _env())

        inserted = [
            a for a in seen if a[0] == "sed" and ko._ALB_INGRESS_CLASS in " ".join(a)
        ]
        self.assertTrue(inserted, seen)


if __name__ == "__main__":
    unittest.main()
