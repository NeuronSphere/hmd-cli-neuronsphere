"""Unit tests for the host-side local Lambda image cache helper.

Floci runs local Lambda containers straight off the host Docker cache through
the mounted ``docker.sock``, and the image reference the CDKTF factory hands it
is a **bare** ``<repo>:<version>`` tag (hmd-lib-cdktf-factories'
``lambda_function``). A bare reference is unpullable by construction -- Docker
resolves it against Docker Hub, not ghcr.io -- so if nothing has put that tag in
the host cache, Floci fails with a 404 "pull access denied" long after the
deploy reported success.

``image_cache`` closes that gap on the host, where a real Docker CLI exists: it
finds the image under any of the registry-prefixed refs the platform uses, or
pulls it, then tags it under the bare ref Floci looks for.

Run directly (``python -m pytest src/python/tests/test_image_cache.py``) -- no
Docker required; every ``subprocess.run`` is mocked.
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
)


class _Docker:
    """Fake ``subprocess.run`` standing in for the docker CLI.

    :param cached: refs that ``docker image inspect`` should find.
    :param pullable: refs that ``docker pull`` should succeed for.
    """

    def __init__(self, cached=(), pullable=()):
        self.cached = set(cached)
        self.pullable = set(pullable)
        self.inspected = []
        self.pulled = []
        self.tagged = []

    def __call__(self, cmd, *args, **kwargs):
        if cmd[:3] == ["docker", "image", "inspect"]:
            ref = cmd[3]
            self.inspected.append(ref)
            return mock.Mock(returncode=0 if ref in self.cached else 1)
        if cmd[:2] == ["docker", "pull"]:
            ref = cmd[2]
            self.pulled.append(ref)
            ok = ref in self.pullable
            if ok:
                self.cached.add(ref)
            return mock.Mock(returncode=0 if ok else 1, stdout="", stderr="denied")
        if cmd[:2] == ["docker", "tag"]:
            self.tagged.append((cmd[2], cmd[3]))
            self.cached.add(cmd[3])
            return mock.Mock(returncode=0, stdout="", stderr="")
        raise AssertionError(f"unexpected docker command: {cmd}")


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

    def test_missing_docker_cli_raises_unavailable(self):
        """No docker on PATH: report it, don't traceback on FileNotFoundError."""
        with mock.patch.object(ic.shutil, "which", return_value=None):
            with self.assertRaises(ic.ImageUnavailable) as ctx:
                ic.ensure_lambda_image(REPO, VERSION)
        self.assertIn("docker", str(ctx.exception))

    def test_tag_failure_still_returns_a_usable_ref(self):
        """If tagging fails, the cached prefixed ref is still what Floci resolves."""
        built = f"ghcr.io/neuronsphere/{REPO}:{VERSION}"
        docker = _Docker(cached=[built])
        with mock.patch.object(ic.subprocess, "run", docker):
            with mock.patch.object(ic, "_docker_tag", return_value=False):
                self.assertEqual(ic.ensure_lambda_image(REPO, VERSION), built)


if __name__ == "__main__":
    unittest.main()
