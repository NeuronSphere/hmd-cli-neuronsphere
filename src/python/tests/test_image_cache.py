"""Unit tests for the host-side local Lambda image cache helper.

Floci runs local Lambda containers straight off the host Docker cache through
the mounted ``docker.sock``, and the image reference the CDKTF factory hands it
is a **bare** ``<repo>:<version>`` tag (hmd-lib-cdktf-factories'
``lambda_function``). A bare reference is unpullable by construction -- Docker
resolves it against Docker Hub, not ghcr.io -- so if nothing has put that tag in
the host cache, Floci fails with a 404 "pull access denied" long after the
deploy reported success.

``image_cache`` closes that gap on the host, where a real container CLI exists:
it finds the image under any of the registry-prefixed refs the platform uses, or
pulls it, then tags it under the bare ref Floci looks for. Which CLI that is is
resolved, not hardcoded -- a developer host runs ``docker``, the cloud builders
run nerdctl and have no ``docker`` binary at all.

Run directly (``python -m pytest src/python/tests/test_image_cache.py``) -- no
container runtime required, and no container CLI either: every ``subprocess.run``
is mocked and ``shutil.which`` is stubbed, so the suite never reads the host's
PATH.
"""

import unittest
from unittest import mock

from hmd_cli_neuronsphere import image_cache as ic

REPO = "hmd-ms-transform"
VERSION = "0.2.457"
BARE = f"{REPO}:{VERSION}"

# Env vars that steer candidate resolution; cleared per-test so a developer's
# own shell can't change the expected ordering.
_ENV_KEYS = (
    "HMD_CONTAINER_REGISTRY",
    "HMD_LOCAL_NS_CONTAINER_REGISTRY",
    "HMD_LOCAL_IMAGE_PULL_REGISTRIES",
    "HMD_DOCKER_USE_NERDCTL",
)


def _which(*installed):
    """Stand in for ``shutil.which``, resolving only ``installed`` clients.

    Every test states the clients its host has. Reading the real PATH is what
    broke this suite in CI: the builders run nerdctl and have no ``docker``, so
    the ``container_cli()`` guard fired before the mocked ``subprocess.run``
    could be reached.
    """
    return lambda name: f"/usr/local/bin/{name}" if name in installed else None


class _Docker:
    """Fake ``subprocess.run`` standing in for the container CLI.

    Client-agnostic on purpose: ``docker`` and ``nerdctl`` speak the same
    ``image inspect`` / ``pull`` / ``tag`` verbs, so the fake matches on the verb
    and records ``cmd[0]`` in :attr:`clis` for the tests that care which client
    was driven.

    :param cached: refs that ``<cli> image inspect`` should find.
    :param pullable: refs that ``<cli> pull`` should succeed for.
    """

    def __init__(self, cached=(), pullable=()):
        self.cached = set(cached)
        self.pullable = set(pullable)
        self.inspected = []
        self.pulled = []
        self.tagged = []
        self.clis = []

    def __call__(self, cmd, *args, **kwargs):
        self.clis.append(cmd[0])
        if cmd[1:3] == ["image", "inspect"]:
            ref = cmd[3]
            self.inspected.append(ref)
            return mock.Mock(returncode=0 if ref in self.cached else 1)
        if cmd[1] == "pull":
            ref = cmd[2]
            self.pulled.append(ref)
            ok = ref in self.pullable
            if ok:
                self.cached.add(ref)
            return mock.Mock(returncode=0 if ok else 1, stdout="", stderr="denied")
        if cmd[1] == "tag":
            self.tagged.append((cmd[2], cmd[3]))
            self.cached.add(cmd[3])
            return mock.Mock(returncode=0, stdout="", stderr="")
        raise AssertionError(f"unexpected container command: {cmd}")


class _EnvIsolated(unittest.TestCase):
    def setUp(self):
        patcher = mock.patch.dict("os.environ", {k: "" for k in _ENV_KEYS}, clear=False)
        patcher.start()
        self.addCleanup(patcher.stop)
        for key in _ENV_KEYS:
            del ic.os.environ[key]


class ImageCandidatesTests(_EnvIsolated):
    def test_defaults_to_published_registry_then_bare(self):
        self.assertEqual(
            ic.image_candidates(REPO, VERSION),
            [f"ghcr.io/neuronsphere/{REPO}:{VERSION}", BARE],
        )

    def test_build_registry_wins(self):
        ic.os.environ["HMD_CONTAINER_REGISTRY"] = "ghcr.io/hmdlabs"
        ic.os.environ["HMD_LOCAL_NS_CONTAINER_REGISTRY"] = "localhost:5000"
        self.assertEqual(
            ic.image_candidates(REPO, VERSION),
            [
                f"ghcr.io/hmdlabs/{REPO}:{VERSION}",
                f"localhost:5000/{REPO}:{VERSION}",
                f"ghcr.io/neuronsphere/{REPO}:{VERSION}",
                BARE,
            ],
        )

    def test_extra_registries_are_inserted_before_the_default(self):
        ic.os.environ["HMD_LOCAL_IMAGE_PULL_REGISTRIES"] = "ghcr.io/hmdlabs, ghcr.io/x"
        self.assertEqual(
            ic.image_candidates(REPO, VERSION),
            [
                f"ghcr.io/hmdlabs/{REPO}:{VERSION}",
                f"ghcr.io/x/{REPO}:{VERSION}",
                f"ghcr.io/neuronsphere/{REPO}:{VERSION}",
                BARE,
            ],
        )

    def test_duplicates_collapse_preserving_order(self):
        ic.os.environ["HMD_CONTAINER_REGISTRY"] = "ghcr.io/neuronsphere"
        ic.os.environ["HMD_LOCAL_NS_CONTAINER_REGISTRY"] = "ghcr.io/neuronsphere"
        self.assertEqual(
            ic.image_candidates(REPO, VERSION),
            [f"ghcr.io/neuronsphere/{REPO}:{VERSION}", BARE],
        )

    def test_trailing_slashes_are_normalized(self):
        ic.os.environ["HMD_CONTAINER_REGISTRY"] = "ghcr.io/hmdlabs/"
        self.assertEqual(
            ic.image_candidates(REPO, VERSION)[0], f"ghcr.io/hmdlabs/{REPO}:{VERSION}"
        )


class EnsureLambdaImageTests(_EnvIsolated):
    def setUp(self):
        super().setUp()
        # The common host: docker installed, no nerdctl. Tests that care about
        # the other shapes re-patch `which` themselves.
        patcher = mock.patch.object(ic.shutil, "which", _which("docker"))
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_bare_tag_cached_is_a_no_op(self):
        """The `hmd build` fast path: nothing to pull, nothing to tag."""
        docker = _Docker(cached=[BARE])
        with mock.patch.object(ic.subprocess, "run", docker):
            self.assertEqual(ic.ensure_lambda_image(REPO, VERSION), BARE)
        self.assertEqual(docker.pulled, [])
        self.assertEqual(docker.tagged, [])

    def test_prefixed_tag_cached_is_tagged_not_pulled(self):
        built = f"ghcr.io/hmdlabs/{REPO}:{VERSION}"
        ic.os.environ["HMD_CONTAINER_REGISTRY"] = "ghcr.io/hmdlabs"
        docker = _Docker(cached=[built])
        with mock.patch.object(ic.subprocess, "run", docker):
            self.assertEqual(ic.ensure_lambda_image(REPO, VERSION), BARE)
        self.assertEqual(docker.pulled, [])
        self.assertEqual(docker.tagged, [(built, BARE)])

    def test_pulls_published_image_then_tags_it_bare(self):
        published = f"ghcr.io/neuronsphere/{REPO}:{VERSION}"
        docker = _Docker(pullable=[published])
        with mock.patch.object(ic.subprocess, "run", docker):
            self.assertEqual(ic.ensure_lambda_image(REPO, VERSION), BARE)
        self.assertEqual(docker.pulled, [published])
        self.assertEqual(docker.tagged, [(published, BARE)])

    def test_never_pulls_the_bare_ref(self):
        """A bare ref resolves to Docker Hub -- pulling it is the bug, not the fix."""
        docker = _Docker()
        with mock.patch.object(ic.subprocess, "run", docker):
            with self.assertRaises(ic.ImageUnavailable):
                ic.ensure_lambda_image(REPO, VERSION)
        self.assertNotIn(BARE, docker.pulled)

    def test_pull_order_follows_candidate_order(self):
        ic.os.environ["HMD_CONTAINER_REGISTRY"] = "ghcr.io/hmdlabs"
        published = f"ghcr.io/neuronsphere/{REPO}:{VERSION}"
        docker = _Docker(pullable=[published])
        with mock.patch.object(ic.subprocess, "run", docker):
            ic.ensure_lambda_image(REPO, VERSION)
        self.assertEqual(
            docker.pulled, [f"ghcr.io/hmdlabs/{REPO}:{VERSION}", published]
        )

    def test_unavailable_names_every_ref_tried_and_both_fixes(self):
        docker = _Docker()
        with mock.patch.object(ic.subprocess, "run", docker):
            with self.assertRaises(ic.ImageUnavailable) as ctx:
                ic.ensure_lambda_image(REPO, VERSION)
        err = ctx.exception
        self.assertEqual(err.tried, [f"ghcr.io/neuronsphere/{REPO}:{VERSION}", BARE])
        message = str(err)
        self.assertIn(f"ghcr.io/neuronsphere/{REPO}:{VERSION}", message)
        self.assertIn(BARE, message)
        self.assertIn("hmd build", message)
        self.assertIn("docker login", message)

    def test_missing_container_cli_raises_unavailable(self):
        """No client at all on PATH: report it, don't traceback on OSError."""
        with mock.patch.object(ic.shutil, "which", _which()):
            with self.assertRaises(ic.ImageUnavailable) as ctx:
                ic.ensure_lambda_image(REPO, VERSION)
        self.assertIn("no container CLI", str(ctx.exception))

    def test_nerdctl_env_selects_the_nerdctl_client(self):
        """`HMD_DOCKER_USE_NERDCTL` steers us the same way hmd_lib_containers is steered."""
        ic.os.environ["HMD_DOCKER_USE_NERDCTL"] = "true"
        docker = _Docker(cached=[BARE])
        with mock.patch.object(ic.shutil, "which", _which("docker", "hmd_nerdctl")):
            with mock.patch.object(ic.subprocess, "run", docker):
                self.assertEqual(ic.ensure_lambda_image(REPO, VERSION), BARE)
        self.assertEqual(set(docker.clis), {"hmd_nerdctl"})

    def test_nerdctl_only_host_needs_no_env(self):
        """The CI shape: nerdctl is installed, docker isn't, nothing is set."""
        published = f"ghcr.io/neuronsphere/{REPO}:{VERSION}"
        docker = _Docker(pullable=[published])
        with mock.patch.object(ic.shutil, "which", _which("hmd_nerdctl")):
            with mock.patch.object(ic.subprocess, "run", docker):
                self.assertEqual(ic.ensure_lambda_image(REPO, VERSION), BARE)
        self.assertEqual(set(docker.clis), {"hmd_nerdctl"})
        self.assertEqual(docker.tagged, [(published, BARE)])

    def test_tag_failure_still_returns_a_usable_ref(self):
        """If tagging fails, the cached prefixed ref is still what Floci resolves."""
        built = f"ghcr.io/neuronsphere/{REPO}:{VERSION}"
        docker = _Docker(cached=[built])
        with mock.patch.object(ic.subprocess, "run", docker):
            with mock.patch.object(ic, "_tag", return_value=False):
                self.assertEqual(ic.ensure_lambda_image(REPO, VERSION), built)


class ContainerCliTests(_EnvIsolated):
    """Client resolution: the env var states a preference, PATH has the last word."""

    def test_docker_by_default(self):
        with mock.patch.object(ic.shutil, "which", _which("docker", "hmd_nerdctl")):
            self.assertEqual(ic.container_cli(), "docker")

    def test_nerdctl_when_the_env_asks_for_it(self):
        ic.os.environ["HMD_DOCKER_USE_NERDCTL"] = "true"
        with mock.patch.object(ic.shutil, "which", _which("docker", "hmd_nerdctl")):
            self.assertEqual(ic.container_cli(), "hmd_nerdctl")

    def test_falls_back_to_whatever_is_installed(self):
        """A docker-less host uses nerdctl even with the env var unset."""
        with mock.patch.object(ic.shutil, "which", _which("nerdctl")):
            self.assertEqual(ic.container_cli(), "nerdctl")

    def test_nerdctl_env_still_falls_back_to_docker(self):
        ic.os.environ["HMD_DOCKER_USE_NERDCTL"] = "true"
        with mock.patch.object(ic.shutil, "which", _which("docker")):
            self.assertEqual(ic.container_cli(), "docker")

    def test_none_when_nothing_is_installed(self):
        with mock.patch.object(ic.shutil, "which", _which()):
            self.assertIsNone(ic.container_cli())


class ImageCachedTests(_EnvIsolated):
    """The lookup floci_deployer.resolve_image_uri shares with the stager."""

    def test_reports_a_cached_ref(self):
        docker = _Docker(cached=[BARE])
        with mock.patch.object(ic.shutil, "which", _which("docker")):
            with mock.patch.object(ic.subprocess, "run", docker):
                self.assertTrue(ic.image_cached(BARE))
                self.assertFalse(ic.image_cached(f"{REPO}:9.9.9"))

    def test_no_cli_is_not_cached_rather_than_an_error(self):
        with mock.patch.object(ic.shutil, "which", _which()):
            self.assertFalse(ic.image_cached(BARE))


if __name__ == "__main__":
    unittest.main()
