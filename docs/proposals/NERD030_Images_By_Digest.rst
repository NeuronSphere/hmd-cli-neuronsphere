.. NERD030 Images By Digest

NERD030 Images By Digest
========================

.. req:: An image an environment needs shall be staged by the tool that knows it needs it
    :id: HMD_CLI_NEURONSPHERE_NERD030
    :status: proposed

    ``nsctl`` shall be able to enumerate the container images an environment's
    declared instances will run, and stage them into the cluster's node from
    the host's image store.

    An image shall be staged by digest, for the node's platform.

    Staging shall report what it moved and what was already present, and shall
    fail loudly when it moved nothing.

Motivation
----------

A local cluster cannot pull private images: nsctl seeds a dummy pull secret,
and ``ghcr.io`` answers a credential-less request for a private package with
``401 Unauthorized``. Every image an environment runs therefore has to come
from the host's image store, and today nothing in ``nsctl`` knows which those
are. The knowledge lives in hand-maintained lists in each application's
Makefile::

    NS_TRANSFORM_IMAGES ?= ghcr.io/neuronsphere/hmd-img-content-item-sync:0.1.5 \
        ghcr.io/evident-vascular/ev-tf-model-predictions:0.1.3 \
        ...

In one bring-up that list named four images. The environment needed nine: the
four, plus ``hmd-ms-transform``, ``hmd-img-transform-notify-worker``,
``hmd-img-transform-client``, ``hmd-img-transform-worker`` and the librarian's.
Each missing one was found the same way -- a pod in ``ImagePullBackOff``, a
``describe`` to learn which image, a ``docker save | ctr images import``, and
back to waiting. The list cannot be right, because the application repo does
not know what the platform services it depends on will run.

Two failure modes made hand-staging worse than merely tedious.

**An import can succeed and carry nothing.** ``docker save`` of a multi-arch
image exports the index alone, so ``ctr images import`` registers a
``manifest.list`` with no layers for the node's platform. ``ctr`` prints::

    application/vnd.docker.distribution.manifest.list.v2+json sha256:18ea...
    Importing    elapsed: 3.5 s    total:   0.0 B    (0.0 B/s)

``ctr images ls`` then shows the tag, and the kubelet pulls anyway despite
``imagePullPolicy: IfNotPresent`` -- failing on the registry it has no
credential for, which reads as a pull-secret problem rather than an import
that did nothing. ``total: 0.0 B`` is printed on the working case too.

**A tag is not an identity.** The node held
``ghcr.io/hmdlabs/hmd-img-transform-client:0.2.23`` at digest ``8904976e``
while the host held the same tag at ``07130ca5``. The older build lacked the
``environment == "local"`` short-circuit its own source comments describe, so
workflow pods took the cloud auth path and died first on
``NoCredentialsError`` and then on a missing Okta secret -- both failures the
newer build exists to prevent, and both pointing at Okta rather than at the
image. Nothing compares digests today, so the tag matching was taken as proof.

Specifications
--------------

.. spec:: The image set is derived, not listed
    :id: HMD_CLI_NEURONSPHERE_NERD030_SPEC001
    :status: proposed

    ``nsctl env images <env>`` shall print the images the environment's
    declared instances will run, from the same declaration ``env apply``
    reconciles: each instance's chart and each transform configuration
    registered against it.

    Printing is the first half and is useful alone: it replaces a list in an
    application Makefile that cannot see the platform services beneath it.

.. spec:: Staging is by digest and by platform
    :id: HMD_CLI_NEURONSPHERE_NERD030_SPEC002
    :status: proposed

    ``nsctl env images <env> --stage`` shall move each image from the host's
    store into the node, selecting the node's platform explicitly
    (``docker save --platform``, Docker 27 and later) so that what lands is a
    platform manifest with its layers rather than an index.

    An image already present in the node shall be compared by digest, not by
    tag, and re-staged when the host's differs.

.. spec:: Staging reports what it did
    :id: HMD_CLI_NEURONSPHERE_NERD030_SPEC003
    :status: proposed

    Staging shall report per image: staged, already present at the same digest,
    or replaced, with the digest in each case.

    A transfer of zero bytes for an image not already present shall be an
    error, not a success line. That is the multi-arch case above, and it is
    currently indistinguishable from a working import.

Alternatives considered
-----------------------

**Give the cluster a real pull secret.** Correct where credentials exist to
give it, and orthogonal: an image built locally and never pushed -- the arm64
rebuilds this environment needed -- has no registry to pull from.

**Keep the lists, document the traps.** What happens today. The list in one
application repo was missing five of the nine images its own environment
needed, and cannot be completed by anyone working in that repo.

Out of scope
------------

Publishing multi-arch images is the image owners' business, not nsctl's; three
``ev-tf-*`` repos pinned ``platforms: ["linux/amd64"]`` and simply could not
run on an arm64 node until that changed. Staging by platform makes the failure
legible -- there is no arm64 manifest to select -- but does not fix it.
