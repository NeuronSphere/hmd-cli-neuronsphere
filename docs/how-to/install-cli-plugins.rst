Install and write CLI plugins
=============================

A **CLI plugin** adds one top-level noun to ``nsctl``: ``nsctl <name> ...``
runs an executable called ``nsctl-<name>`` with everything after the noun
passed through verbatim. A plugin runs because
``$HMD_HOME/.config/nsctl.toml`` declares it under ``[plugin.<name>]`` and
for no other reason -- nothing on ``PATH`` or under the cache is scanned.

Install a published plugin
--------------------------

::

   nsctl plugin install hello
   nsctl plugin install ghcr.io/acme/plugins/deploy:1.4.0
   nsctl plugin install hello --spec "~= 1.2"

A bare name expands to ``ghcr.io/neuronsphere/plugins/<name>`` and the expansion
is printed. Without a version the newest published one is chosen and
printed. The binary for this platform is unpacked under
``$HMD_HOME/.cache/neuronsphere/plugins/<name>@<version>/`` and the
declaration is written::

   [plugin.hello]
   source  = "oci://ghcr.io/neuronsphere/plugins/hello"
   version = "1.2.0"
   digest  = "sha256:..."

Then::

   nsctl hello --help
   nsctl plugin list
   nsctl plugin update hello      # newest published version
   nsctl plugin remove hello

Public namespaces need no credential. For a private one, set
``HMD_REGISTRY_TOKEN`` or give a profile a ``registry_url`` and
``nsctl login``, as for :doc:`stacks <use-stacks>`.

A plugin needs an ``HMD_HOME`` to be found: with none set and no ``--home``
before the noun, no plugins are attached and ``nsctl hello`` is an unknown
command.

Run a local build
-----------------

While developing a plugin, declare its path by hand::

   [plugin.scratch]
   path = "/Users/me/src/nsctl-scratch/build/nsctl-scratch"

``nsctl scratch ...`` runs it as ``dev``. A ``path`` wins over a ``source``
when both are set, and ``plugin list`` says so.

Write one
---------

A plugin is any executable. ``nsctl`` runs it with this contract:

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - What
     - Contract
   * - Arguments
     - Every token after the noun, verbatim. ``--help`` is yours to answer.
       A ``--home`` *after* the noun is yours too; the one ``nsctl`` honours
       is before it.
   * - stdin, stdout, stderr, cwd
     - Inherited. ``nsctl`` writes nothing around the call.
   * - Exit status
     - Yours, unchanged, with no ``Error:`` line from ``nsctl``. A signal
       death is ``128+n``.
   * - Environment
     - The process environment, then every ``hmd.env`` key the process did
       not set, then the variables below.

.. list-table::
   :header-rows: 1
   :widths: 34 66

   * - Variable
     - Value
   * - ``HMD_HOME`` / ``NSCTL_HOME``
     - The resolved home, even when it came from ``--home``.
   * - ``NSCTL_VERSION``
     - The running ``nsctl`` version.
   * - ``NSCTL_BINARY``
     - Path of the running ``nsctl``.
   * - ``NSCTL_PLUGIN_NAME``
     - The noun.
   * - ``NSCTL_PLUGIN_VERSION``
     - The declared version, or ``dev`` for a ``path``.
   * - ``NSCTL_PLUGIN_DIR``
     - The directory holding the binary.

No profile and no credential are passed. A plugin that needs the login token
reads ``$HMD_HOME/.cache/tokens.yaml`` as ``nsctl`` does; a plugin that needs
a tenant reads ``nsctl.toml`` for itself.

Publish one
-----------

The artifact is one OCI manifest per version: a descriptor as the config
blob and one gzipped tarball per platform, each holding ``nsctl-<name>`` at
its root. That is exactly the archive GoReleaser produces, so a Go author's
release job is GoReleaser followed by ``nsctl plugin push``.

In a directory with ``plugin.json``::

   {
     "name": "hello",
     "version": "1.2.0",
     "summary": "Say hello from a plugin",
     "min_nsctl_version": "1.0"
   }

and the archives ``nsctl-hello_1.2.0_darwin_arm64.tar.gz``,
``nsctl-hello_1.2.0_linux_amd64.tar.gz`` and so on::

   nsctl plugin push dist/ ghcr.io/acme/plugins/hello --token $GHCR_PAT

The ``platforms`` table of the descriptor is filled in from the archives'
digests; the tag is the descriptor's version. On ``ghcr.io`` the new package
is private by default -- make it public before anyone installs it
anonymously.

Reserved names
--------------

A plugin may not be called after a built-in noun (``env``, ``repo``,
``stack``, ...) or ``help`` or ``completion``. ``plugin install`` refuses
such a descriptor; a hand-written declaration with a reserved name is
warned about at startup and the built-in wins.
