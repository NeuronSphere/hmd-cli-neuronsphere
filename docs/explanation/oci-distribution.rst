Why stacks and plugins come from an OCI registry
================================================

Stacks (:doc:`../how-to/use-stacks`) and CLI plugins
(:doc:`../how-to/install-cli-plugins`) are both distributed as **OCI
artifacts** -- the same registry protocol container images use, carrying
arbitrary bytes instead of an image. This page is about why, and what that
decides for a free user, a paid tenant, and anyone publishing their own.

The gap this closes
-------------------

Before stacks, a RepoClass that was not a checkout reached a local
environment one way: ``nsctl artifact pull`` against the cloud Artifact
Librarian, which needs a tenant credential and is a paid feature. A free
user had no download path at all for a third-party RepoClass.

The gap was narrow. ``source: {type: artifact}`` resolution reads nothing but
the unpacked cache under ``$HMD_HOME/.cache/neuronsphere/artifacts/``, and
the deploy runner bind-mounts that tree. Put bytes in the cache and
everything above it -- the lock, the planner, ``env apply`` -- already
works, offline, with no credential. What was missing was a *source* that did
not require a tenant.

Why a registry, and why this one
--------------------------------

Every NeuronSphere container image already lives at ``ghcr.io/hmdlabs``,
which is public, anonymous, and the host a local environment must reach to
run anything at all. An OCI registry stores any artifact under the same
tags, digests and token flow as images. So a stack's zips live where its
images live, and "is this stack usable for free" is one visibility decision
on one package.

Three properties of the protocol carry the whole design:

**Content addressing.** Every manifest and blob is named by its sha256.
``nsctl`` verifies every byte it receives against the descriptor before a
consumer sees it, and a stack's lock carries a ``digest`` per pinned zip so
what you install is what the publisher tested. No checksum file to invent,
no signing key to manage for integrity alone.

**Anonymous and authenticated on one code path.** A registry challenges;
``nsctl`` presents what it has -- nothing, a token, or a login -- and the
registry decides. That is the licensing posture applied to distribution:
``nsctl`` has no notion of free or paid and checks nothing. A public
namespace is a free product; a namespace that needs a login is a paid one;
the code path is identical. A tenant's profile gains one optional key,
``registry_url``, and a reference whose host matches it is fetched with the
login token.

**Tags are versions.** ``tags/list`` is the version enumeration, filtered to
version-shaped tags and ordered the way every other version list in
``nsctl`` is. ``latest`` is never pulled: everything downstream is keyed by
a version that does not move.

What is *not* consulted
-----------------------

``nsctl`` never guesses a registry host. A bare name expands to a namespace
compiled into the verb -- a build identity -- and the expansion is printed.
It never reads ``~/.docker/config.json``: that is another tool's credential
store, holding credentials for hosts the user never meant ``nsctl`` to
speak to. A credential resolved for one host is sent to that host and to
nothing else.

Nothing here is on the resolve path. The registry client is imported only by
the verbs that fetch or push, never by resolution or ``env apply``, and a
test fails the build if that changes. After the one fetch an environment is
as offline as any other.

What a publisher does
---------------------

A stack publisher runs ``nsctl stack push`` from a repository with a lock;
a plugin publisher runs ``nsctl plugin push`` from a GoReleaser release
directory. Both need a credential for the registry (a PAT with
``write:packages`` on ``ghcr.io``) and neither needs any tool but ``nsctl``.
A stack's companion zips come from ``--artifacts`` or from the cloud
librarian, so publishing a stack of platform RepoClasses is a paid user's
act and installing it is not -- that asymmetry is the point.

What is deferred
----------------

A ``github.com/<owner>/<repo>`` source -- GitHub Releases, the way ``nsctl``
itself ships -- is recognised and refused with a pointer to the plan. It has
no answer for a private namespace and its rate limits bite anonymous users,
and a publisher who can run GoReleaser can run ``nsctl plugin push`` in the
same job. Signing and attestation are out of scope for now, as they are for
the ``nsctl`` binary. The requirements are ``NERD016`` (the mechanism),
``NERD017`` (stacks) and ``NERD018`` (plugins) under :doc:`../proposals/index`.
