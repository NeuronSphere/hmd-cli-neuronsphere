"""Detect and repair a PostgreSQL major-version bump under Floci RDS.

Floci runs a real postgres container per RDS instance, and **recreates that
container from the current ``FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE`` on every
start while reusing the instance's named volume**. So bumping the postgres major
version in ``hmd-postgres-base`` is destructive: the new binary refuses a data
directory initialised by the old one and the container dies with

    FATAL:  database files are incompatible with server
    DETAIL: The data directory was initialized by PostgreSQL version 14,
            which is not compatible with this version 16.3.

Nothing surfaces that at the point of the change. Floci reports the instance
``available`` regardless -- it has recorded it -- so the first symptom is
whatever connects next failing, one layer removed from the cause.

Both sides of the comparison are readable **statically**, without starting
anything:

* the image's major version from its ``PG_MAJOR`` env (``docker inspect``);
* the volume's from the ``PG_VERSION`` file postgres writes at the root of its
  data directory -- the same marker postgres itself checks on boot.

That is what lets :func:`assert_compatible` run as an ``up`` pre-flight, before
Floci has a chance to spawn a container that crash-loops.
"""

import json
import os
import subprocess
from typing import List, NamedTuple, Optional

from cement import minimal_logger

logger = minimal_logger("pg_upgrade")

# Floci names an RDS instance's volume after the container it backs.
_RDS_VOLUME_PREFIX = "floci-rds-"
# Backups live outside that namespace on purpose -- see _backup_volume_name.
_BACKUP_VOLUME_PREFIX = "hmd-pgbackup-"
_PGDATA = "/var/lib/postgresql/data"


class Mismatch(NamedTuple):
    """One volume whose data directory the configured image cannot read."""

    volume: str
    found: str
    expected: str


def _run(args, timeout: int = 60) -> subprocess.CompletedProcess:
    return subprocess.run(args, capture_output=True, text=True, timeout=timeout)


def configured_postgres_image() -> str:
    """The image Floci will spawn RDS containers from.

    Mirrors the default in the compose files; an override there is passed through
    the same environment variables, so reading them keeps the two in step.
    """
    registry = os.environ.get("HMD_LOCAL_NS_CONTAINER_REGISTRY", "ghcr.io/neuronsphere")
    version = os.environ.get("HMD_POSTGRES_BASE_VERSION", "stable")
    return os.environ.get(
        "FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE",
        f"{registry}/hmd-postgres-base:{version}",
    )


def image_pg_major(image: str) -> Optional[str]:
    """The postgres major version an image ships, from its ``PG_MAJOR`` env.

    Returns None when the image is not present locally: pulling it here would
    turn a pre-flight check into a network round trip, and a missing image is not
    itself the problem this detects.
    """
    r = _run(
        ["docker", "inspect", image, "--format", "{{json .Config.Env}}"], timeout=15
    )
    if r.returncode != 0:
        logger.debug(f"Image {image} not inspectable locally: {r.stderr.strip()}")
        return None
    try:
        env = json.loads(r.stdout.strip() or "[]") or []
    except json.JSONDecodeError:
        return None
    for entry in env:
        if entry.startswith("PG_MAJOR="):
            return entry.split("=", 1)[1]
    return None


def volume_pg_version(volume: str, image: str) -> Optional[str]:
    """The major version that initialised ``volume``'s data directory.

    Reads ``PG_VERSION`` with the postgres image itself rather than a helper like
    alpine, so the check needs no image the platform does not already use.
    """
    r = _run(
        [
            "docker",
            "run",
            "--rm",
            "--entrypoint",
            "cat",
            "-v",
            f"{volume}:{_PGDATA}",
            image,
            f"{_PGDATA}/PG_VERSION",
        ],
        timeout=120,
    )
    if r.returncode != 0:
        # An empty volume has no PG_VERSION yet -- that is a fresh instance, not
        # a mismatch.
        logger.debug(f"No PG_VERSION in {volume}: {r.stderr.strip()}")
        return None
    return r.stdout.strip() or None


def rds_volumes() -> List[str]:
    """Every Floci RDS data volume on this machine."""
    r = _run(
        [
            "docker",
            "volume",
            "ls",
            "--format",
            "{{.Name}}",
            "--filter",
            f"name={_RDS_VOLUME_PREFIX}",
        ],
        timeout=15,
    )
    if r.returncode != 0:
        return []
    return [v for v in r.stdout.split() if v.startswith(_RDS_VOLUME_PREFIX)]


def find_mismatches(image: Optional[str] = None) -> List[Mismatch]:
    """Volumes the configured image cannot start against.

    Best-effort by design: anything we cannot determine (image absent, Docker
    unavailable, volume not yet initialised) yields no mismatch rather than a
    false alarm, because this gates `up`.
    """
    image = image or configured_postgres_image()
    expected = image_pg_major(image)
    if not expected:
        return []
    mismatches = []
    for volume in rds_volumes():
        found = volume_pg_version(volume, image)
        if found and found != expected:
            mismatches.append(Mismatch(volume=volume, found=found, expected=expected))
    return mismatches


def assert_compatible(image: Optional[str] = None) -> None:
    """Refuse to start when the configured image cannot read existing data.

    Failing here is the whole point: left alone, Floci recreates the container,
    postgres exits, and the error surfaces as an unrelated connection failure
    later on.
    """
    mismatches = find_mismatches(image)
    if not mismatches:
        return
    lines = "\n".join(
        f"      {m.volume}: initialised by PostgreSQL {m.found}, "
        f"image ships {m.expected}"
        for m in mismatches
    )
    raise SystemExit(
        f"\n  ERROR: the configured PostgreSQL image cannot read existing "
        f"database files.\n\n{lines}\n\n"
        f"  Floci recreates an RDS instance's container from the current image "
        f"on every\n  start but keeps its volume, so a major-version bump leaves "
        f"the data behind.\n\n"
        f"  Migrate the data (dump, re-initialise, restore -- a backup volume is "
        f"kept):\n\n"
        f"      hmd neuronsphere db upgrade\n\n"
        f"  Or discard it and start clean:\n\n"
        f"      hmd neuronsphere down --purge\n"
    )


# ---------------------------------------------------------------------------
# Migration
# ---------------------------------------------------------------------------


def _backup_volume_name(volume: str, major: str) -> str:
    """Deliberately outside Floci's ``floci-rds-`` namespace.

    A backup named inside it would be picked up by :func:`rds_volumes` and, since
    it holds the *old* data directory by definition, reported as a mismatch on
    every subsequent run -- so a successful migration would leave `up` blocked,
    pointing at a migration that had already happened. It also keeps Floci from
    ever mistaking a backup for an instance's live volume.
    """
    return f"{_BACKUP_VOLUME_PREFIX}{volume}-pg{major}"


def _dump_image(major: str) -> str:
    """An image able to read a data directory of ``major``.

    The *old* postgres, which by definition the configured image is not. Stock
    upstream rather than ``hmd-postgres-base``: the previous NeuronSphere tag may
    no longer exist, and nothing here needs the extensions it adds -- reading a
    data directory and running ``pg_dumpall`` are core postgres.
    """
    return os.environ.get("HMD_LOCAL_PG_DUMP_IMAGE", f"postgres:{major}-alpine")


def upgrade_volume(mismatch: Mismatch, image: Optional[str] = None) -> bool:
    """Dump, re-initialise and restore one volume in place.

    In place because Floci derives the volume name from the container it spawns
    and will look for exactly that name again; a differently-named copy would
    simply be ignored.

    The old data is copied to a backup volume first and **never deleted** -- this
    rewrites a database, and an automated migration that also destroys its only
    copy is not one worth offering.
    """
    image = image or configured_postgres_image()
    volume, old_major = mismatch.volume, mismatch.found
    backup = _backup_volume_name(volume, old_major)
    dump_image = _dump_image(old_major)

    logger.debug(f"Backing up {volume} to {backup}")
    if _run(["docker", "volume", "create", backup], timeout=30).returncode != 0:
        logger.error(f"Could not create backup volume {backup}")
        return False
    copy = _run(
        [
            "docker",
            "run",
            "--rm",
            "--entrypoint",
            "sh",
            "-v",
            f"{volume}:/from",
            "-v",
            f"{backup}:/to",
            dump_image,
            "-c",
            "cp -a /from/. /to/",
        ],
        timeout=600,
    )
    if copy.returncode != 0:
        logger.error(f"Backup of {volume} failed: {copy.stderr.strip()}")
        return False

    # pg_dumpall needs a running server, so start the OLD postgres on the volume.
    helper = f"hmd-pg-upgrade-{volume[-12:]}"
    _run(["docker", "rm", "-f", helper], timeout=60)
    started = _run(
        [
            "docker",
            "run",
            "-d",
            "--name",
            helper,
            "-v",
            f"{volume}:{_PGDATA}",
            "-e",
            "POSTGRES_PASSWORD=admin",
            dump_image,
        ],
        timeout=300,
    )
    if started.returncode != 0:
        logger.error(
            f"Could not start {dump_image} on {volume}: {started.stderr.strip()}"
        )
        return False
    try:
        if not _wait_ready(helper):
            return False
        dumped = _run(
            [
                "docker",
                "exec",
                helper,
                "pg_dumpall",
                "-U",
                "postgres",
                "--clean",
                "--if-exists",
            ],
            timeout=1800,
        )
        if dumped.returncode != 0:
            logger.error(f"pg_dumpall failed on {volume}: {dumped.stderr.strip()}")
            return False
        dump_sql = dumped.stdout
    finally:
        _run(["docker", "rm", "-f", helper], timeout=120)

    # An empty data directory is what makes the new image run initdb.
    wiped = _run(
        [
            "docker",
            "run",
            "--rm",
            "--entrypoint",
            "sh",
            "-v",
            f"{volume}:{_PGDATA}",
            dump_image,
            "-c",
            f"rm -rf {_PGDATA}/..?* {_PGDATA}/.[!.]* {_PGDATA}/*",
        ],
        timeout=300,
    )
    if wiped.returncode != 0:
        logger.error(f"Could not clear {volume}: {wiped.stderr.strip()}")
        return False

    _run(["docker", "rm", "-f", helper], timeout=60)
    started = _run(
        [
            "docker",
            "run",
            "-d",
            "--name",
            helper,
            "-v",
            f"{volume}:{_PGDATA}",
            "-e",
            "POSTGRES_PASSWORD=admin",
            image,
        ],
        timeout=300,
    )
    if started.returncode != 0:
        logger.error(f"Could not start {image} on {volume}: {started.stderr.strip()}")
        return False
    try:
        if not _wait_ready(helper):
            return False
        restored = subprocess.run(
            [
                "docker",
                "exec",
                "-i",
                helper,
                "psql",
                "-U",
                "postgres",
                "-d",
                "postgres",
            ],
            input=dump_sql,
            capture_output=True,
            text=True,
            timeout=1800,
        )
        # Not ON_ERROR_STOP: the new image's baked dbs_init.sh has already created
        # some of the roles and databases the dump recreates, so "already exists"
        # is expected and not a failed migration.
        if restored.returncode != 0:
            logger.warning(
                f"Restore reported errors on {volume}: {restored.stderr.strip()[:400]}"
            )
    finally:
        _run(["docker", "rm", "-f", helper], timeout=120)

    logger.debug(f"Upgraded {volume} from {old_major} to {mismatch.expected}")
    return True


def _wait_ready(container: str, timeout: int = 180) -> bool:
    """Wait for a postgres container to accept connections."""
    import time

    start = time.time()
    while time.time() - start < timeout:
        r = _run(
            ["docker", "exec", container, "pg_isready", "-U", "postgres"], timeout=30
        )
        if r.returncode == 0:
            return True
        if (
            _run(
                ["docker", "inspect", "-f", "{{.State.Running}}", container], timeout=15
            ).stdout.strip()
            != "true"
        ):
            logs = _run(["docker", "logs", "--tail", "20", container], timeout=30)
            logger.error(
                f"{container} exited during upgrade: {logs.stdout or logs.stderr}"
            )
            return False
        time.sleep(2)
    logger.error(f"{container} never became ready")
    return False


def upgrade_all(image: Optional[str] = None) -> int:
    """Migrate every mismatched volume. Returns the number upgraded."""
    image = image or configured_postgres_image()
    mismatches = find_mismatches(image)
    if not mismatches:
        return 0
    upgraded = 0
    for m in mismatches:
        if upgrade_volume(m, image=image):
            upgraded += 1
    return upgraded
