.. NERD041 System Membership

NERD041 System Membership
=========================

.. req:: nsctl shall author, report and resolve a repo class's system
    :id: HMD_CLI_NEURONSPHERE_NERD041
    :status: approved

    ``nsctl repoclass`` shall write and remove the BACON ``system`` key of a
    repo class, report it in ``describe``, and resolve the systems of a
    workspace: which repo classes each system includes, each system's
    definition, and any conflict between inline definitions.

    nsctl shall never infer membership from a name or a directory, and
    ``system`` shall never change what nsctl builds, deploys or resolves.

Source
------

``hmd-core-arch-docs`` NEP058 (Work Model: Systems, Needs and Teams of
Agents), section *Systems*. The key itself is specified in
``hmd-docs-bacon`` NERD0002 (System Membership): a system name, or
``{name, role, description, parent}``. ``hmd-lang-deployment`` NERD001
models it as the ``system`` noun with ``system_includes_repo_class`` (one
system per repo class) and ``system_has_parent``.

Problem
-------

Agents and people keep asking "what is the transform service made of?",
and nothing answers it. The question gets rediscovered by reading
directory names, and a plugin carries a hand-written list of systems as a
stopgap.

BACON NERD0002 gives each repo class a ``system`` key, but there is no
command to write it. The repository's rule is that a manifest is authored
with ``nsctl repoclass`` and never by hand, so a key without a verb won't
get used. ``describe``, which agents read instead of the manifest, does
not show it either.

NERD0002 SPEC0004 also requires a tool to resolve a system's definition
and to report conflicting inline definitions. A per-repo-class verb cannot
do that, because it sees one manifest.

Scope
-----

In scope:

- ``nsctl repoclass system set`` and ``clear``;
- ``system`` in ``nsctl repoclass describe``, both the table and ``--json``;
- ``nsctl repoclass systems``, which resolves the systems across a
  workspace.

Out of scope:

- a central systems catalog file or service, which NERD0002 leaves to the
  organisation (a later NERD may add ``--catalog``);
- writing ``system_*`` relationships into ``hmd-ms-deployment``'s catalog;
- any change to build, deploy, bundles or dependency resolution.

Specifications
--------------

.. spec:: nsctl repoclass system set writes the key
    :id: HMD_CLI_NEURONSPHERE_NERD041_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD041
    :status: approved

    ``nsctl repoclass system set <name> [--role R] [--description D]
    [--parent P]`` replaces the manifest's ``system`` key.

    - With no flags it writes the shorthand form, ``"system": "<name>"``.
      With any flag it writes the object form, which holds only the fields
      given.
    - ``<name>`` and ``--parent`` must match
      ``^[a-z][a-z0-9]*(-[a-z0-9]+)*$``. A name is never its own parent.
    - ``--role`` is one of ``core``, ``engine``, ``image``, ``library``,
      ``ui``, ``cli`` or ``docs``.
    - A bad value is a usage error (exit 2). It names the field and the
      pattern or the allowed values, and leaves the manifest unchanged.
    - The edit goes through the same store as ``license set``, so JSON and
      TOML manifests keep their formatting and key order, and the command
      reports the key it wrote.

    *Verification:* table tests in ``internal/bacon`` cover each form, each
    rejected value, TOML round-trip and an unchanged file on error. A CLI
    test runs ``set`` then ``describe --json``.

.. spec:: nsctl repoclass system clear removes it
    :id: HMD_CLI_NEURONSPHERE_NERD041_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD041
    :status: approved

    ``nsctl repoclass system clear`` removes the key. On a manifest without
    one it changes nothing, prints nothing and exits 0, as ``license clear``
    does.

    *Verification:* unit tests for removal and for the no-op.

.. spec:: describe reports the system
    :id: HMD_CLI_NEURONSPHERE_NERD041_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD041
    :status: approved

    ``nsctl repoclass describe --json`` carries ``system`` as an object,
    ``{name, role?, description?, parent?}``, whichever form the manifest
    uses. It holds only what the manifest says and is never resolved against
    other repo classes.

    The table form prints one line, ``system`` followed by the name, with
    ``(role)`` when a role is given. A manifest without the key prints no
    line and emits no field, so existing output is unchanged.

    *Verification:* golden tests for both forms, and for a manifest without
    the key, whose output is byte-identical to today's.

.. spec:: nsctl repoclass systems resolves a workspace
    :id: HMD_CLI_NEURONSPHERE_NERD041_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD041
    :status: approved

    ``nsctl repoclass systems [--root DIR]... [--json]`` reads the manifests
    of the repo classes under each root. Without ``--root`` it reads the
    current directory. It uses the same discovery as other workspace scans:
    repository roots one level down, skipping hidden directories and
    worktrees. For each system it reports:

    - its name, and its resolved ``description`` and ``parent``;
    - its members, each with its repo class name, role and manifest path;
    - its subsystems, the systems whose resolved parent is this one.

    Resolution follows ``hmd-docs-bacon`` NERD0002 SPEC0004, with no central
    catalog in this NERD:

    - a system's definition comes from the one manifest that gives inline
      ``description`` or ``parent``;
    - manifests that give the same inline values agree;
    - manifests that give different inline values are a **conflict**,
      reported with every manifest path involved. nsctl never picks one by
      order.

    A system named only as a parent, with no member and no definition, is
    still listed, so the tree is complete.

    A parent chain that loops is reported as a **cycle**, naming the
    systems in it.

    The exit status is 0 when there are no conflicts and no cycles, and 1
    otherwise. Every system is still reported either way, so the command
    can serve as a check.

    *Verification:* unit tests over a temporary workspace cover an
    agreement, a conflict, a cycle, a parent-only system and a repo class
    without the key. A CLI test checks the JSON shape and the exit status.

.. spec:: Membership is declared, never inferred
    :id: HMD_CLI_NEURONSPHERE_NERD041_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD041
    :status: approved

    No nsctl command derives a system from a repository name, prefix or
    directory. ``nsctl repoclass detect`` and ``init`` do not propose one.
    No build, deploy, bundle or resource-matching code path reads
    ``system``.

    *Verification:* a test asserts that ``init`` and ``detect`` output
    contain no ``system`` key. A grep-based test asserts that ``system`` is
    read only by the ``system`` verbs, ``describe`` and ``systems``.

Rollout
-------

1. ``internal/bacon``: ``SetSystem``, ``ClearSystem`` and the ``system``
   entry of ``Describe``.
2. The verbs ``system set``, ``system clear`` and ``systems``, plus the
   docs page for each, with CHANGELOG.
3. Declare the first systems with ``system set``: transform, deployment,
   librarian, and ``cli`` with its subsystems ``cli-go`` and ``cli-python``.
   ``nsctl repoclass systems`` over the workspace must exit 0.
