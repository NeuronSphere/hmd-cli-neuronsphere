.. NERD020 Stacks via the Artifact Librarian

NERD020 Stacks via the Artifact Librarian
=========================================

.. req:: Publish a stack to, and install it from, a tenant's deployed Artifact Librarian, with the same lock, the same zips and the same verbs as the OCI form
    :id: HMD_CLI_NEURONSPHERE_NERD020
    :status: proposed

    A stack (``NERD017``) shall be **pushable to and addable from an
    ``hmd-ms-artifact-lib`` a tenant has deployed** -- the private
    transport existing customers already run -- beside the OCI registry
    transport that is the free, public one. One ``neuronsphere.lock``
    serves both; the zips are byte-identical in both; the verbs are the
    same verbs with a different reference scheme. Neither store mirrors
    the other, and ``nsctl`` does not prefer one: a lock entry says where
    its zip lives, and a stack reference says where the stack lives.

Motivation
----------

Single build zips are already symmetric across the two stores. On the
librarian side, ``nsctl artifact pull`` fetches from a tenant's librarian
by ``content_path``, ``nsctl artifact register <dir>`` puts into the control
plane's, and ``hmd build`` with ``HMD_AUTO_PUBLISH`` publishes to the
cloud one. On the OCI side, ``nsctl artifact push`` and ``artifact pull``
(``NERD016`` SPEC009) move the same zip through a registry, and a pull from
a registry offers the zip to the local librarian afterwards. A lock entry
keeps both coordinates -- ``content_path`` always, ``source`` when the
class is published as an OCI artifact -- and ``nsctl stack build`` already
takes a companion from the cloud librarian by ``content_path`` when no
free tier answers (``NERD019`` SPEC002, tier 4).

Stacks themselves are not symmetric. ``stack.Install`` takes an
``oci.Fetcher``; ``stack push`` writes only to a registry; the librarian
``Put`` that ``stack add`` performs is to the *control plane's* librarian,
so that ``artifact unpack`` and in-container ``pre_build_artifacts`` keep
working, and never to a tenant's. A customer whose classes live only in
their deployed librarian can assemble a stack from them but has nowhere
private to publish it and no way for a colleague to add it. That is the
gap: the paid transport must carry the whole stack, not only its parts.

Scope and terminology
---------------------

* The **OCI form** of a stack is ``NERD017`` SPEC002: one image manifest,
  the pinned lock as the config blob, one layer per zip.
* The **librarian form** is what this NERD defines: the same zips as
  ordinary content items, the pinned lock inside the subject's zip.
* A **tenant's librarian** is an ``hmd-ms-artifact-lib`` instance reached
  through a profile (``nsctl login``) or ``--url``, as ``artifact pull``
  reaches it today; the **local librarian** is the control plane's.

Out of scope: mirroring one store into the other at push time (a later
verb, if wanted); a stack that pulls in other stacks by lock
(``NERD010``'s open question, unchanged); anonymous access to a librarian
(it has no public namespace); and the licence of what is published, which
is the author's declaration under ``NERD017`` SPEC011 and rides inside
each zip whichever store holds it.

.. spec:: A stack in the librarian is its subject's build zip
    :id: HMD_CLI_NEURONSPHERE_NERD020_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD020
    :status: proposed

    No new content item type. The subject RepoClass's build zip is stored
    at its ordinary ``content_path``
    (``<class>/<version>/<class>_<version>_build.zip``, item type
    ``build``) and every companion at its own. The subject's zip already
    carries ``neuronsphere.lock`` at its root (``NERD017`` SPEC006); where
    the OCI form carries the *pinned* lock -- digests filled -- as the
    config blob, the librarian form carries it **inside the subject zip**:
    when ``nsctl`` builds the librarian form it replaces the tree's
    ``neuronsphere.lock`` entry with the pinned lock, deterministically, so
    that the subject's own digest is stable for a given set of inputs.

    Companion zips are the same bytes in both forms, so the digests
    ``NERD017`` SPEC007 records agree across transports, and a companion
    already published to one store need not be rebuilt for the other.

.. spec:: References
    :id: HMD_CLI_NEURONSPHERE_NERD020_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD020
    :status: proposed

    A stack in a librarian is named ``librarian://<class>@<version>``
    (the version is optional on push, where it defaults as today), beside
    ``oci://<host>/<path>:<version>``. The scheme, not a flag, is what
    makes the ``NERD017`` SPEC009 stack record (``ref:``) and
    ``stack list``'s ``FROM`` column self-describing; the record also
    keeps ``url:``, the tenant librarian it came from, so that
    ``stack versions`` and a later upgrade ask the same place. Which
    librarian the scheme resolves to is the profile's ``--url``, exactly
    as ``artifact pull`` resolves it. An anonymous reference is refused:
    the librarian has no public namespace, and the refusal names
    ``nsctl login``.

.. spec:: nsctl stack push to a librarian
    :id: HMD_CLI_NEURONSPHERE_NERD020_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD020
    :status: proposed

    ::

        nsctl stack push [<repo-dir>] librarian://<class>[@<version>] [--artifacts <dir>] [--update-lock]

    Builds as ``NERD017`` SPEC006 does -- the subject from the tree, each
    companion from the ``NERD019`` SPEC002 tiers -- then writes the
    librarian form: ``Put`` the subject zip (pinned lock inside) at its
    ``content_path``, and ``Put`` every companion **not already** at its
    ``content_path``. A companion already there is fetched and verified
    against the lock's digest and never overwritten; a disagreement is
    refused, since the lock is the publisher's statement of what they
    tested. A subject already there is refused: a published version is
    immutable, as the OCI tag rule says. ``--update-lock`` writes the
    digests back into the repository's lock as today, which is what makes
    the next ``stack add`` verifiable (SPEC004).

.. spec:: nsctl stack add from a librarian
    :id: HMD_CLI_NEURONSPHERE_NERD020_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD020
    :status: proposed

    ::

        nsctl stack add librarian://<class>@<version> [--profile <tenant>] [--url <librarian>]

    Fetches the subject by ``content_path``, reads ``neuronsphere.lock``
    out of the zip, and **requires every entry to carry a digest**: the
    librarian is not content-addressed, nothing but the lock can vouch for
    the bytes, and a lock without digests is refused naming
    ``stack push --update-lock`` and ``nsctl lock --resolve``. Each entry
    is then fetched by its ``content_path`` and verified, and the existing
    install tail runs unchanged: ``artifact.Store`` for every class, the
    lock written into the subject's tree, a best-effort ``Put`` into the
    local librarian, the instances declared as ``NERD017`` SPEC003. The
    record is SPEC002's.

    The implementation shape is a ``stack.Source`` with two
    implementations -- the OCI bundle, and the librarian subject zip plus
    its lock -- with ``Install`` taking that in place of ``oci.Fetcher``.
    Everything after "here are the zips and the lock" is shared.

.. spec:: stack versions and stack pull for a librarian reference
    :id: HMD_CLI_NEURONSPHERE_NERD020_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD020
    :status: proposed

    ``stack versions librarian://<class>`` lists the versions of the
    subject's content items in the tenant's librarian (the repository's
    content item ids, parsed back to specs), sorted and filtered as the
    OCI form is; ``stack pull`` caches the librarian form as it caches the
    OCI form. The versions cache key is ``stack~librarian~<class>`` beside
    ``NERD017`` SPEC005's ``stack~<host>~<path>``.

.. spec:: One lock, two transports
    :id: HMD_CLI_NEURONSPHERE_NERD020_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD020
    :status: proposed

    Lock entries keep both ``content_path`` and ``source``; ``NERD019``
    SPEC002's tiers are unchanged. A stack published to both stores is the
    same zips under two names, and a consumer of either sees the same
    ``Licences:`` line (``NERD017`` SPEC011), read from the manifests
    inside the zips rather than from where they were found. Images are
    ``NERD017`` SPEC008's unchanged limitation: a private customer's
    images reach an environment through
    ``HMD_LOCAL_IMAGE_PULL_REGISTRIES``.

Testing
-------

The fake librarian in ``cmd/artifact_test.go`` already serves the read path
and the three-leg write path, so ``stack push librarian://...`` followed by
``stack add librarian://...`` round-trips in a unit test, byte for byte,
with the digests in the pushed subject's lock. Unit tests for the librarian
``stack.Source`` prove the pinned lock is what the subject zip carries, a
lock without digests is refused with both remedies named, a companion
already present is verified and not re-put, and a subject already present
is refused. ``test/nsctl_cli.robot`` gains ``Stack Add Refuses Anonymous
Librarian Ref``, offline.

Alternatives considered
-----------------------

* **A ``stack`` content item type holding the lock.** Two items to keep
  consistent -- the lock and the subject zip that already contains a lock
  -- with nothing to gain: the subject zip is the artifact, and putting
  the pinned lock inside it is one write.
* **OCI only, with a customer-run registry.** Existing customers run an
  Artifact Librarian, not a registry, and the librarian is the paid
  contract; asking them to stand up a registry to use a feature they pay
  for would invert the funnel ``NERD017`` set up.
* **Mirroring librarian to OCI at push.** Useful later for a customer who
  wants a public subset of what they run privately; it is a separate verb
  over the same two forms and does not change either.
