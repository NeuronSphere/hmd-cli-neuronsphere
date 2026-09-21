.. NERD018 CLI Plugins

NERD018 CLI Plugins
===================

.. req:: Run a declared executable as one new top-level noun
    :id: HMD_CLI_NEURONSPHERE_NERD018
    :status: proposed

    ``nsctl`` shall run an out-of-tree executable as one new top-level noun --
    ``nsctl <name> <args...>`` -- with every argument after the noun passed
    through verbatim, the exit status returned unchanged, and enough of
    ``nsctl``'s resolved state in the environment for the plugin to find the
    same ``HMD_HOME`` and configuration.

    A plugin exists because ``$HMD_HOME/.config/nsctl.toml`` names it, and
    for no other reason. **No directory and no ``PATH`` is scanned.** A plugin
    is installed from an OCI artifact (``NERD016``) by version, recorded with
    its digest, and removed by name.

Motivation
----------

The second kind of plugin is code: a verb set that ``nsctl`` does not have and
should not grow in-tree -- a customer's deploy wrapper, a partner's
diagnostics, an experiment someone wants to ship without an ``nsctl``
release. ``NERD005`` SPEC008 already put the principle in writing for
extensions: "if shipping a new extension requires a new ``nsctl`` release, the
extension surface is decorative". The same holds for commands.

The platform's established answer to "how does an extension get found" is
that it does not. ``NERD004`` SPEC013 says nothing is scanned, enumerated or
auto-registered; an extension runs because a manifest names it. The Python
CLI's entry-point discovery is the thing that was withdrawn. So this NERD does
not adopt the ``kubectl``/``git`` convention of running any ``nsctl-<name>``
found on ``PATH``, attractive as its zero machinery is: it is discovery, it
records no version and no digest, and it turns a stray binary into a command.
Instead the declaration is explicit, in the file that already holds
``nsctl``'s per-installation configuration, and the install verb is what
writes it.

Go's ``plugin`` package is not considered: it needs CGO, a matching
toolchain and matching dependency versions, and cannot load on a binary built
with ``CGO_ENABLED=0`` as this one is. An executable with a documented
argument and environment contract is the portable form, and it lets a plugin
be written in any language.

Scope and terminology
---------------------

* A **plugin** is a declared executable; its **noun** is the name it is
  declared under; its **binary** is ``nsctl-<noun>``.
* A **declaration** is a ``[plugin.<noun>]`` table in ``nsctl.toml``.
* A **published plugin** is an OCI artifact (SPEC002) in some namespace; the
  canonical one is ``ghcr.io/hmdlabs/plugins``, a default expansion for a
  bare name and nothing more.
* A **dev build** is a declaration with a ``path`` and no ``source``.

Out of scope: a plugin adding a verb *under* an existing noun (``nsctl env
snapshot``) -- that needs a descriptor-driven grafting protocol, per-verb
conflict rules and changes to the generated reference and to ``NERD015``'s
skill validation, and top-level nouns cover the cases that motivated this;
sandboxing or permissioning a plugin beyond what the operating system does;
and plugin-to-plugin dependencies.

.. spec:: Declaration
    :id: HMD_CLI_NEURONSPHERE_NERD018_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD018
    :status: proposed

    .. code-block:: toml

        [plugin.hello]
        source  = "oci://ghcr.io/hmdlabs/plugins/hello"
        version = "1.2.0"
        digest  = "sha256:..."        # manifest digest that was installed

        [plugin.scratch]
        path    = "/Users/me/src/nsctl-scratch/build/nsctl-scratch"   # a dev build

    ``nsctl.toml`` stays strictly parsed (``DisallowUnknownFields``), so the
    table is modelled on ``Config`` and an unknown key inside it is still an
    error. A noun matches ``^[a-z][a-z0-9-]*$``. A table must have ``source``
    or ``path``; with both, ``path`` wins and ``list`` says so. ``version`` and
    ``digest`` are required with ``source`` and ignored with ``path``.

    A missing ``nsctl.toml`` means no plugins. A malformed one is a warning
    at startup and no plugins, never a failure: ``nsctl version`` must keep
    working on an installation whose configuration has one bad line, as
    ``hmd.env`` already does.

.. spec:: The plugin artifact
    :id: HMD_CLI_NEURONSPHERE_NERD018_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD018
    :status: proposed

    One OCI image manifest per version:

    ================================ ====================================================
    Field                            Value
    ================================ ====================================================
    ``artifactType``                 ``application/vnd.neuronsphere.plugin.v1+json``
    ``config``                       the descriptor below,
                                     ``mediaType: application/vnd.neuronsphere.plugin.v1+json``
    ``layers[]``                     one per platform,
                                     ``mediaType: application/vnd.neuronsphere.plugin.binary.v1.tar+gzip``
    ================================ ====================================================

    .. code-block:: json

        {
          "name": "hello",
          "version": "1.2.0",
          "summary": "Say hello from a plugin",
          "min_nsctl_version": "1.0",
          "platforms": {
            "darwin_arm64": {"digest": "sha256:...", "size": 1234567},
            "linux_amd64":  {"digest": "sha256:...", "size": 1234567}
          }
        }

    Each layer is annotated ``io.neuronsphere.plugin.os``,
    ``io.neuronsphere.plugin.arch`` and ``org.opencontainers.image.title``
    (``nsctl-<name>_<version>_<os>_<arch>.tar.gz``), and holds ``nsctl-<name>``
    at the tarball root with mode ``0755``, plus optional ``LICENSE*`` and
    ``NOTICE`` files and nothing else. The platform keys are Go's
    ``GOOS_GOARCH``; the same four ``nsctl`` itself ships are expected but
    not required.

    This is exactly the archive GoReleaser produces for ``nsctl``, so a Go
    author's release job is: GoReleaser, then ``nsctl plugin push``.

.. spec:: nsctl plugin install, remove, list, update, push
    :id: HMD_CLI_NEURONSPHERE_NERD018_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD018
    :status: proposed

    .. code-block:: text

        nsctl plugin install <ref>              # bare name expands to the canonical namespace
        nsctl plugin remove  <noun>
        nsctl plugin list    [--json]
        nsctl plugin update  [<noun>]           # re-resolve newest; all declared when omitted
        nsctl plugin push    <dir> <ref> [--token <t>] [--profile <tenant>]

    ``install`` resolves the reference and version (``NERD016`` SPEC001 and
    SPEC004), fetches and verifies the descriptor, refuses when
    ``min_nsctl_version`` is newer than the running version under
    ``versionspec.Compare`` (a ``dev`` build warns and continues), selects the
    layer for the running ``GOOS_GOARCH`` and refuses naming the platforms
    that *are* published when there is none, extracts the tarball into
    ``$HMD_HOME/.cache/neuronsphere/plugins/<noun>@<version>/`` through a
    staging directory (path-traversal and size guards as ``artifact.Store``;
    the binary must exist and be executable), renames it into place, and
    writes the declaration. A noun that is reserved (SPEC004) is refused
    before any fetch. A noun already declared is upgraded in place, and the
    previous version's directory is removed once the new one is in place.

    ``remove`` deletes the declaration and the plugin's cache directories. It
    never touches a ``path`` target. ``list`` prints noun, version, source and
    a state: ``installed``, ``missing binary`` (declared, cache gone --
    remedy ``plugin install``), or ``path``. ``update`` is ``install`` with
    the newest version for a declared source.

    ``push`` builds SPEC002 from a directory holding the per-platform
    tarballs and a ``plugin.json`` descriptor (with ``platforms`` filled in by
    the verb from the tarballs' digests), and pushes with ``NERD016``
    SPEC005. It requires a credential.

.. spec:: Dispatch and reservation
    :id: HMD_CLI_NEURONSPHERE_NERD018_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD018
    :status: proposed

    At startup, ``nsctl`` reads the declarations for the ``HMD_HOME`` it
    resolves -- ``--home`` when it appears before the first non-flag
    argument, else the environment -- and attaches one command per
    declaration to the root, in a help group titled ``Plugin commands
    (declared in nsctl.toml)``, with flag parsing disabled so the plugin
    sees its arguments untouched.

    **Built-in nouns are reserved.** The reserved set is every command the
    root already has, plus ``help`` and ``completion``. A declaration whose
    noun is reserved is not attached; ``nsctl`` warns on stderr and the
    built-in wins. ``install`` refuses the same set up front so the warning
    is only ever reached by hand-editing.

    A declared plugin whose binary is absent fails with a usage error naming
    ``nsctl plugin install <noun>``. With no ``HMD_HOME`` at all, no plugins
    are attached and an unknown noun is the usage error it is today;
    ``nsctl version``, ``nsctl repoclass`` and ``nsctl --help`` keep working
    with nothing set, as ``NERD002`` requires.

    The exported ``NewRootCommand`` that ``tools/docref`` and the tests build
    the tree from **never attaches plugins**, so the generated
    :doc:`/reference/commands` cannot pick up a developer's declarations,
    and ``NERD015`` skill validation still resolves against the built-in tree
    only. A second constructor that takes the argument vector is what
    ``main`` calls.

.. spec:: The exec protocol
    :id: HMD_CLI_NEURONSPHERE_NERD018_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD018
    :status: proposed

    ``nsctl <noun> <args...>`` runs the binary with:

    * **argv**: every token after the noun, verbatim. ``--help`` is the
      plugin's to answer. A ``--home`` *after* the noun is the plugin's too;
      the one ``nsctl`` honours is before it.
    * **stdin, stdout, stderr**: inherited. ``nsctl`` writes nothing to
      either stream around the call.
    * **cwd**: inherited.
    * **environment**: the process environment, then every ``hmd.env`` key
      the process did not already set (so the plugin sees the same layered
      view ``nsctl`` does), then:

      ================================ ============================================
      Variable                         Value
      ================================ ============================================
      ``HMD_HOME``                     the resolved home (set even if it came from ``--home``)
      ``NSCTL_HOME``                   the same, for a plugin that wants to know it was resolved by ``nsctl``
      ``NSCTL_VERSION``                the running ``nsctl`` version
      ``NSCTL_BINARY``                 ``os.Executable()`` of the running ``nsctl``
      ``NSCTL_PLUGIN_NAME``            the noun
      ``NSCTL_PLUGIN_VERSION``         the declared version, or ``dev`` for a ``path``
      ``NSCTL_PLUGIN_DIR``             the directory holding the binary
      ================================ ============================================

    * **exit status**: the plugin's, unchanged, with **no** ``Error:`` line
      from ``nsctl``; a plugin that died of a signal exits ``128+n``.
      ``nsctl`` does not forward its own ``SIGINT`` to the child: the
      terminal delivers it to both, and forwarding would deliver it twice.

    No ``NSCTL_PROFILE`` is passed. Profiles are per-verb in ``nsctl``
    (``--profile`` binds on cloud-reaching verbs only), a plugin reads
    ``nsctl.toml`` for itself, and passing one would imply a default profile
    ``nsctl`` does not have. No credential of any kind is passed: a plugin
    that needs the login token reads ``tokens.yaml`` from ``HMD_HOME`` as
    ``nsctl`` does, and the user has consented to that by installing it.

.. spec:: No discovery
    :id: HMD_CLI_NEURONSPHERE_NERD018_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD018
    :status: proposed

    Nothing enumerates ``PATH``, the cache directory, ``HMD_REPO_HOME`` or a
    plugin directory. A binary that is present but not declared is not a
    command. A ``path`` declaration is the *only* way to run an unpublished
    build, and it is a declaration: the user typed the path into the file.
    ``plugin list`` lists declarations, not directories, so a stale cache
    directory is invisible until ``remove`` or an upgrade deletes it.

.. spec:: Help and the generated reference
    :id: HMD_CLI_NEURONSPHERE_NERD018_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD018
    :status: proposed

    ``nsctl --help`` lists attached plugins in their own group with the
    descriptor's ``summary`` (or ``plugin <noun>`` for a ``path`` build).
    ``nsctl help <noun>`` prints the summary, source, version and binary
    path and then says the rest is ``nsctl <noun> --help``. The reference
    excludes plugins by construction (SPEC004), and the how-to for writing
    one documents SPEC005 as the contract a plugin author codes against.

Testing
-------

Tests exercise ``internal/plugin`` and the verbs against a temporary
``HMD_HOME`` and the ``NERD016`` fake registry: descriptor decoding, platform
selection and its "not published for ``darwin_arm64``" message, tarball
guards, ``min_nsctl_version`` against a real and a ``dev`` version, ``path``
precedence, and ``Run`` for exit status, environment and signal death using
a shell-script fixture. Dispatch tests build the tree with the argument
vector and ``--home`` before the noun and prove ``--help`` reaches the
plugin, an exit of 7 comes back as 7 with no ``Error:`` line, a declared
plugin with no binary is a usage error naming ``plugin install``, a reserved
noun warns and the built-in wins, and no ``HMD_HOME`` leaves an unknown noun
a usage error. ``nsconfig`` tests prove the strict decode still accepts the
table, rejects a bad noun and a table with neither ``source`` nor ``path``,
and round-trips profiles and plugins together. A ``tools/docref`` test
asserts the reference contains ``nsctl plugin`` and never the plugin group.
``test/nsctl_cli.robot`` gains the no-Docker cases listed in ``NERD017``'s
Testing section for this NERD's tag.

The acceptance run publishes a toy ``nsctl-hello`` for two platforms to the
canonical namespace, and in a scratch ``HMD_HOME`` with no credential
installs it, runs it with arguments, sees the exit status propagate, sees it
in ``--help``, and removes it.

Alternatives considered
-----------------------

**``kubectl``-style ``PATH`` discovery.** Zero machinery, and the convention
users of ``kubectl`` and ``git`` know. Rejected as discovery: ``NERD004``
SPEC013 is the platform's stated rule, a stray binary on ``PATH`` becoming a
command is the failure mode the rule exists to prevent, and it records
nothing that ``list`` could show or ``update`` could act on.

**Go ``plugin`` package / shared objects.** CGO, toolchain and dependency
version lock-step, no cross-language plugins, and not loadable by a
``CGO_ENABLED=0`` binary.

**A gRPC/JSON-RPC plugin protocol (HashiCorp ``go-plugin``).** Right when the
host calls into the plugin repeatedly and needs typed results; here the host
calls once and the plugin owns the terminal. An exec contract is the whole
protocol.

**In-tree contribution.** The default, and still the right answer for
anything the platform should own. The point of this NERD is what it should
not.
