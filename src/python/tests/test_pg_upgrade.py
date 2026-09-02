"""PostgreSQL major-version detection under Floci RDS (``pg_upgrade``).

Floci recreates an RDS instance's container from the *current* postgres image on
every start while reusing the instance's volume, so a major-version bump in
`hmd-postgres-base` leaves the old data directory behind and the new binary
refuses it. Nothing reports that at the point of the change -- Floci still calls
the instance `available` -- so the check has to happen before Floci starts.

Run directly: ``python -m pytest src/python/tests/test_pg_upgrade.py``
"""

import os
import unittest
from unittest import mock

from hmd_cli_neuronsphere import pg_upgrade as pu


def _proc(stdout="", returncode=0, stderr=""):
    return mock.Mock(stdout=stdout, returncode=returncode, stderr=stderr)


class ConfiguredImageTests(unittest.TestCase):
    def test_tracks_the_compose_default(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(
                pu.configured_postgres_image(),
                "ghcr.io/neuronsphere/hmd-postgres-base:stable",
            )

    def test_an_explicit_floci_override_wins(self):
        # The same variable the compose file passes to Floci, so the check and
        # the emulator cannot disagree about which image will be used.
        with mock.patch.dict(
            os.environ,
            {"FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE": "postgres:16-alpine"},
            clear=True,
        ):
            self.assertEqual(pu.configured_postgres_image(), "postgres:16-alpine")


class ImageMajorTests(unittest.TestCase):
    def test_reads_pg_major_from_the_image_config(self):
        env = '["PGDATA=/var/lib/postgresql/data","PG_MAJOR=14","PG_VERSION=14.20"]'
        with mock.patch.object(pu, "_run", return_value=_proc(env)):
            self.assertEqual(pu.image_pg_major("img"), "14")

    def test_an_absent_image_is_not_a_mismatch(self):
        """Pulling here would turn a pre-flight into a network round trip, and a
        missing image is not the problem this detects."""
        with mock.patch.object(pu, "_run", return_value=_proc(returncode=1)):
            self.assertIsNone(pu.image_pg_major("img"))


class MismatchTests(unittest.TestCase):
    def _find(self, image_major, volumes):
        def fake_run(args, timeout=60):
            if args[1] == "inspect":
                return _proc(f'["PG_MAJOR={image_major}"]')
            if args[1] == "volume":
                return _proc(" ".join(volumes))
            raise AssertionError(args)

        with mock.patch.object(pu, "_run", side_effect=fake_run):
            with mock.patch.object(
                pu, "volume_pg_version", side_effect=lambda v, i: volumes[v]
            ):
                return pu.find_mismatches("img")

    def test_matching_versions_report_nothing(self):
        self.assertEqual(self._find("14", {"floci-rds-a": "14"}), [])

    def test_a_differing_version_is_reported(self):
        found = self._find("16", {"floci-rds-a": "14"})
        self.assertEqual(len(found), 1)
        self.assertEqual((found[0].found, found[0].expected), ("14", "16"))

    def test_an_uninitialised_volume_is_not_a_mismatch(self):
        # A brand-new instance has no PG_VERSION yet.
        self.assertEqual(self._find("16", {"floci-rds-a": None}), [])

    def test_only_floci_rds_volumes_are_considered(self):
        def fake_run(args, timeout=60):
            if args[1] == "inspect":
                return _proc('["PG_MAJOR=16"]')
            return _proc("floci-rds-a some-other-volume")

        with mock.patch.object(pu, "_run", side_effect=fake_run):
            with mock.patch.object(pu, "volume_pg_version", return_value="14"):
                found = pu.find_mismatches("img")
        self.assertEqual([m.volume for m in found], ["floci-rds-a"])


class AssertCompatibleTests(unittest.TestCase):
    def test_passes_when_nothing_mismatches(self):
        with mock.patch.object(pu, "find_mismatches", return_value=[]):
            pu.assert_compatible("img")  # must not raise

    def test_blocks_with_both_remedies(self):
        m = pu.Mismatch(volume="floci-rds-a", found="14", expected="16")
        with mock.patch.object(pu, "find_mismatches", return_value=[m]):
            with self.assertRaises(SystemExit) as ctx:
                pu.assert_compatible("img")
        message = str(ctx.exception)
        self.assertIn("floci-rds-a", message)
        self.assertIn("14", message)
        self.assertIn("16", message)
        # Both a way to keep the data and a way to discard it.
        self.assertIn("db upgrade", message)
        self.assertIn("down --purge", message)


class BackupNamingTests(unittest.TestCase):
    def test_backup_records_the_version_it_holds(self):
        self.assertEqual(
            pu._backup_volume_name("floci-rds-a", "14"),
            "hmd-pgbackup-floci-rds-a-pg14",
        )

    def test_the_dump_image_matches_the_old_data(self):
        # The configured image cannot read it -- that is the whole problem -- so
        # the dump runs on a postgres matching the data directory.
        self.assertEqual(pu._dump_image("14"), "postgres:14-alpine")


if __name__ == "__main__":
    unittest.main()


class BackupIsNotRescannedTests(unittest.TestCase):
    """A backup must not be mistaken for a live instance volume.

    It holds the *old* data directory by definition, so if the scan saw it, a
    successful migration would leave `up` permanently blocked -- reporting a
    mismatch and pointing at a migration that had already run.
    """

    def test_the_backup_is_outside_flocis_namespace(self):
        backup = pu._backup_volume_name("floci-rds-a", "14")
        self.assertFalse(backup.startswith(pu._RDS_VOLUME_PREFIX))

    def test_a_backup_volume_is_not_scanned(self):
        volumes = "floci-rds-a " + pu._backup_volume_name("floci-rds-a", "14")

        def fake_run(args, timeout=60):
            if args[1] == "inspect":
                return _proc('["PG_MAJOR=16"]')
            return _proc(volumes)

        with mock.patch.object(pu, "_run", side_effect=fake_run):
            with mock.patch.object(pu, "volume_pg_version", return_value="14"):
                found = [m.volume for m in pu.find_mismatches("img")]
        self.assertEqual(found, ["floci-rds-a"])
