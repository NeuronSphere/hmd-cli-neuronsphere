"""Host-side staging of the Docker images Floci runs local Lambdas from.

Floci has no ECR emulator. A local Lambda runs straight from the **host** Docker
image cache through the mounted ``docker.sock``, and the reference it is given is
a bare ``<repo>:<version>`` tag -- see ``hmd_lib_cdktf_factories.lambda_function``,
which switches to that form whenever ``stack.environment == "local"``.

That contract only holds if something has already put the tag in the host cache.
A bare reference is unpullable by construction: Docker resolves an unqualified
name against Docker Hub, never ghcr.io, so when the tag is missing Floci fails
with ``pull access denied for <repo>`` -- and it fails at container-start time,
long after the deploy node reported success.

Until now the only thing that ever wrote that tag was ``hmd build`` on the host.
``hmd_cli_docker.deploy_local`` is meant to stage the alternate refs, but a
DAG-driven local deploy runs it inside an ``hmd-img-projectbuilder`` container
that has no ``docker`` CLI and a nerdctl with no containerd socket, so it
logs and returns. The same is true of ``hmd-cli-helm``'s image import into k3s.

This module runs on the host, where a real container CLI exists, and closes the
gap: find the image under whichever registry prefix the platform tagged it with,
or pull it from the published registry using the developer's own ``docker
login``, then tag it under the bare ref Floci looks for.

Which CLI that is comes from :func:`container_cli` rather than a hardcoded name:
a developer host runs ``docker``, while the cloud builders run nerdctl and have
no ``docker`` binary at all.
"""

import os
import shutil
import subprocess
from typing import List, Optional

from cement import minimal_logger

logger = minimal_logger("image_cache")

# The published registry every bundled NeuronSphere image defaults to. Same
# value (and same role) as ``floci_deployer``'s fallback and
# ``hmd_cli_docker.LOCAL_FLOCI_REGISTRY_DEFAULT``.
PUBLISHED_REGISTRY_DEFAULT = "ghcr.io/neuronsphere"

# Extra registry prefixes to try, comma-separated -- e.g. a private
# ``ghcr.io/hmdlabs`` a developer is logged in to, or a local registry mirror.
EXTRA_REGISTRIES_ENV = "HMD_LOCAL_IMAGE_PULL_REGISTRIES"

# Which container client to shell out to, read exactly the way
# ``hmd_lib_containers.get_client`` reads it: set means nerdctl (as
# ``hmd_nerdctl``), unset means docker.
USE_NERDCTL_ENV = "HMD_DOCKER_USE_NERDCTL"

# The clients we know how to drive, in the order each mode prefers them. Both
# speak the same ``image inspect`` / ``pull`` / ``tag`` verbs.
_NERDCTL_FIRST = ("hmd_nerdctl", "nerdctl", "docker")
_DOCKER_FIRST = ("docker", "hmd_nerdctl", "nerdctl")


def _is_truthy(value: Optional[str]) -> bool:
    return (value or "").strip().lower() in {"1", "true", "yes", "on"}


def container_cli() -> Optional[str]:
    """The container CLI on this host, or None if none is installed.

    ``$HMD_DOCKER_USE_NERDCTL`` sets the preference -- the same switch
    ``hmd_lib_containers.get_client`` reads -- and PATH settles the rest, so a
    host that has only one of the clients gets that one either way.
    """
    preferred = (
        _NERDCTL_FIRST if _is_truthy(os.environ.get(USE_NERDCTL_ENV)) else _DOCKER_FIRST
    )
    for name in preferred:
        if shutil.which(name):
            return name
    return None


class ImageUnavailable(Exception):
    """No image could be found or pulled for a repo/version.

    Carries every ref that was tried so the caller can report exactly what the
    local platform looked for.
    """

    def __init__(
        self,
        repo_name: str,
        version: str,
        tried: List[str],
        reason: Optional[str] = None,
    ):
        self.repo_name = repo_name
        self.version = version
        self.tried = list(tried)
        self.reason = reason
        refs = "\n".join(f"    {ref}" for ref in self.tried)
        why = f" ({reason})" if reason else ""
        super().__init__(
            f"No Docker image is available for {repo_name}:{version}{why}, so Floci "
            f"has nothing to run its Lambda from. Tried:\n{refs}\n"
            f"  Fix it by building the image locally (`hmd build` in {repo_name}), or "
            f"by making the published image pullable (`docker login ghcr.io`). Set "
            f"{EXTRA_REGISTRIES_ENV} to add registries to the list above."
        )


def image_candidates(repo_name: str, version: str) -> List[str]:
    """Every ref the local platform may hold this repo's image under, best first.

    Mirrors (and is the single source of) the order
    ``floci_deployer.resolve_image_uri`` resolves in:

      1. ``$HMD_CONTAINER_REGISTRY/<repo>:<ver>`` -- what ``hmd build`` tagged;
      2. ``$HMD_LOCAL_NS_CONTAINER_REGISTRY/<repo>:<ver>``;
      3. each prefix in ``$HMD_LOCAL_IMAGE_PULL_REGISTRIES``;
      4. ``ghcr.io/neuronsphere/<repo>:<ver>`` -- the published registry;
      5. bare ``<repo>:<ver>`` -- what Floci is handed.

    Entries whose env var is unset are skipped; duplicates collapse, keeping the
    earliest occurrence.
    """
    prefixes = [
        os.environ.get("HMD_CONTAINER_REGISTRY"),
        os.environ.get("HMD_LOCAL_NS_CONTAINER_REGISTRY"),
        *(part.strip() for part in os.environ.get(EXTRA_REGISTRIES_ENV, "").split(",")),
        PUBLISHED_REGISTRY_DEFAULT,
    ]

    candidates: List[str] = []
    for prefix in prefixes:
        if not prefix:
            continue
        ref = f"{prefix.rstrip('/')}/{repo_name}:{version}"
        if ref not in candidates:
            candidates.append(ref)
    candidates.append(f"{repo_name}:{version}")
    return candidates


def _image_cached(ref: str, cli: str) -> bool:
    """True if ``ref`` is in the host image cache."""
    return (
        subprocess.run([cli, "image", "inspect", ref], capture_output=True).returncode
        == 0
    )


def _pull(ref: str, cli: str) -> bool:
    """Pull ``ref``, returning success. A miss is expected, so it only logs."""
    result = subprocess.run([cli, "pull", ref], capture_output=True, text=True)
    if result.returncode == 0:
        return True
    logger.debug(f"Could not pull {ref}: {(result.stderr or '').strip()}")
    return False


def _tag(source: str, target: str, cli: str) -> bool:
    """Tag ``source`` as ``target``, returning success."""
    result = subprocess.run(
        [cli, "tag", source, target], capture_output=True, text=True
    )
    if result.returncode == 0:
        return True
    logger.warning(
        f"Could not tag {source} as {target}: {(result.stderr or '').strip()}"
    )
    return False


def image_cached(ref: str) -> bool:
    """True if ``ref`` is in the host image cache, resolving the CLI first.

    The public form of :func:`_image_cached`, used by
    ``floci_deployer.resolve_image_uri`` so image lookup lives in one place. No
    container CLI means nothing can be cached from here, not an error.
    """
    cli = container_cli()
    return bool(cli) and _image_cached(ref, cli)


def ensure_lambda_image(repo_name: str, version: str) -> str:
    """Make ``<repo>:<version>`` resolvable from the host Docker cache.

    Cached first (a local ``hmd build`` always wins over a published image), then
    pulled. The bare ref is never pulled -- an unqualified name would go to
    Docker Hub, which is the failure this exists to prevent.

    :returns: the ref Floci will resolve -- the bare tag once staged, or the
        cached/pulled ref itself if tagging failed (Floci resolves that too, via
        ``floci_deployer.resolve_image_uri``).
    :raises ImageUnavailable: nothing could be found or pulled.
    """
    candidates = image_candidates(repo_name, version)
    bare = f"{repo_name}:{version}"

    cli = container_cli()
    if cli is None:
        raise ImageUnavailable(
            repo_name,
            version,
            candidates,
            reason="no container CLI (docker or nerdctl) is on PATH",
        )

    if _image_cached(bare, cli):
        logger.debug(f"{bare} is already in the host image cache")
        return bare

    pullable = [ref for ref in candidates if ref != bare]

    for ref in pullable:
        if _image_cached(ref, cli):
            logger.info(f"Staging cached image {ref} as {bare} for Floci")
            return bare if _tag(ref, bare, cli) else ref

    for ref in pullable:
        logger.info(f"{bare} is not cached; trying to pull {ref}")
        if _pull(ref, cli):
            logger.info(f"Pulled {ref}; staging it as {bare} for Floci")
            return bare if _tag(ref, bare, cli) else ref

    raise ImageUnavailable(repo_name, version, candidates)
