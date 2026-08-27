"""Port validation for the control-plane / environment layout.

The layout's core invariant is that ``hmd_proxy`` is the only container that
publishes host ports -- that is what lets several environments coexist. Strict
mode enforces it; the reservation checks stop anything else from claiming the
proxy's ports or the per-environment stream range.

Run directly: ``python -m pytest src/python/tests/test_port_validator.py``
"""

import os
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

from hmd_cli_neuronsphere.validators import port_validator as pv


def _compose(tmpdir: Path, name: str, body: str) -> str:
    path = tmpdir / name
    path.write_text(textwrap.dedent(body))
    return str(path)


class _TempDir(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()


class ReservedPortTests(_TempDir):
    def test_proxy_ports_are_reserved(self):
        for port in (80, 4566):
            self.assertIn(port, pv.RESERVED_PORTS)

    def test_env_range_is_reserved(self):
        ranges = pv.reserved_port_ranges()
        self.assertTrue(ranges)
        low, high, owner = ranges[0]
        self.assertLess(low, high)
        self.assertIn("hmd_proxy", owner)

    def test_the_proxy_itself_is_not_a_conflict(self):
        port_map = {80: [("proxy", "control-plane.yml")]}
        self.assertEqual(pv.check_reserved_port_conflicts(port_map), {})

    def test_another_service_on_a_reserved_port_is_a_conflict(self):
        port_map = {4566: [("floci", "x.yml")]}
        conflicts = pv.check_reserved_port_conflicts(port_map)
        self.assertIn(4566, conflicts)

    def test_a_service_inside_the_env_range_is_a_conflict(self):
        low, _, _ = pv.reserved_port_ranges()[0]
        conflicts = pv.check_reserved_port_conflicts({low + 3: [("svc", "x.yml")]})
        self.assertIn(low + 3, conflicts)


class StrictModeTests(_TempDir):
    CONTROL_PLANE = """
        services:
          proxy:
            ports: ["80:80", "4566:4566"]
          db:
            image: postgres
          floci:
            image: floci/floci
    """
    LEAKY = """
        services:
          proxy:
            ports: ["80:80"]
          db:
            ports: ["5432:5432"]
    """

    def test_proxy_only_layout_passes(self):
        f = _compose(self.tmp, "cp.yml", self.CONTROL_PLANE)
        pv.validate_ports([f], strict=True)  # must not raise

    def test_a_published_database_port_is_rejected(self):
        f = _compose(self.tmp, "leaky.yml", self.LEAKY)
        with self.assertRaises(SystemExit) as ctx:
            pv.validate_ports([f], strict=True)
        message = str(ctx.exception)
        self.assertIn("5432", message)
        self.assertIn("db", message)
        self.assertIn("hmd_proxy", message)

    def test_non_strict_mode_only_warns(self):
        f = _compose(self.tmp, "leaky.yml", self.LEAKY)
        with mock.patch.object(pv, "check_ports_in_use", return_value={}):
            pv.validate_ports([f])  # must not raise

    def test_find_published_non_proxy_ports(self):
        f = _compose(self.tmp, "leaky.yml", self.LEAKY)
        offenders = pv.find_published_non_proxy_ports(pv.extract_host_ports([f]))
        self.assertEqual(list(offenders), [5432])


class ContainerPortDiscoveryTests(_TempDir):
    def test_ports_are_unioned_across_every_known_project(self):
        seen = []

        def fake_run(args, **kwargs):
            project = [a for a in args if a.startswith("label=")][0]
            seen.append(project)
            if "env-dev2" in project:
                return mock.Mock(returncode=0, stdout="0.0.0.0:19004->4566/tcp\n")
            return mock.Mock(returncode=0, stdout="0.0.0.0:80->80/tcp\n")

        with mock.patch.object(pv.subprocess, "run", side_effect=fake_run):
            ports = pv.get_neuronsphere_container_ports(
                project_names=["local_neuronsphere-abc", "ns-abc-env-dev2"]
            )
        self.assertEqual(ports, {80, 19004})
        self.assertEqual(len(seen), 2)

    def test_docker_failure_is_not_fatal(self):
        with mock.patch.object(pv.subprocess, "run", side_effect=OSError("no docker")):
            self.assertEqual(
                pv.get_neuronsphere_container_ports(project_names=["x"]), set()
            )


if __name__ == "__main__":
    unittest.main()
