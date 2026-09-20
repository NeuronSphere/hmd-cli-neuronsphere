.. NERD015 Agent Skills

NERD015 Agent Skills
====================

.. req:: Ship installable agent guidance with the local CLI
    :id: HMD_CLI_NEURONSPHERE_NERD015
    :status: proposed

    ``nsctl`` shall bundle a small set of versioned, task-focused Agent Skills
    that teach an AI coding agent how to operate the local NeuronSphere through
    the CLI.  It shall install a selected skill into the native project or user
    location of Codex, Claude Code, or both; it shall not require the user to
    copy a prompt from documentation or to know which directory an agent scans.

    A skill is guidance, not a second implementation of the platform.  It
    directs an agent to inspect evidence and invoke ``nsctl``; the CLI remains
    the owner of validation, planning, mutation, credentials, and deployment.
    No skill ships a model, makes a network call, or grants a command new
    authority.

Motivation
----------

The local NeuronSphere is deliberately a real substrate rather than a mock:
one control plane per ``HMD_HOME``, with a cluster, database and other services
needed to run RepoClasses locally.  That is valuable context but unfamiliar to
a person encountering the product through an AI coding agent.  They should be
able to ask *"run this project locally"* and have the agent first establish
what is present, explain the next safe action, and use the supported commands.

The same installation must serve a power user who asks the agent to inspect a
deployment plan, author a RepoClass, or diagnose a failed local run.  A large
always-loaded instruction file serves neither audience: it consumes context,
does not describe when an instruction applies, and turns unrelated requests
into NeuronSphere work.  Agent Skills solve that routing problem.  They are
directory-based instruction packs with a ``SKILL.md`` entry point and optional
resources, a format understood by both Codex and Claude Code.

NERD009 already proposed ``nsctl repoclass agent init`` as a narrow instruction
pack for foreign RepoClass adoption.  It is not built.  The need has expanded
beyond RepoClasses and its target locations are not enough for Codex, so this
NERD supersedes NERD009 SPEC015.  The adoption skill remains one member of the
general pack; the safeguards in that requirement remain binding.

Scope and terminology
---------------------

* A **bundled skill** is a directory embedded in the released ``nsctl`` binary.
  Its entry point is ``SKILL.md`` with Agent Skills-compatible YAML frontmatter.
* A **host** is the agent product which discovers the installed skill: ``codex``
  or ``claude``.  ``all`` means both hosts, not every installed agent product.
* **Project scope** writes into a target repository and is reviewable and
  shareable through that repository's version control.  **User scope** writes
  into the invoking user's agent configuration and is available across their
  projects.
* Installing a skill only writes the skill directory.  It does not write or
  replace ``AGENTS.md``, ``CLAUDE.md``, a manifest, a profile, or an environment
  registry.

.. spec:: Bundled skills and their boundaries
    :id: HMD_CLI_NEURONSPHERE_NERD015_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD015
    :status: proposed

    The initial release shall bundle these named skills:

    ``nsctl-onboard``
        Explain the local NeuronSphere model, inspect local prerequisites and
        identify the next safe command.  It does not start or create anything
        without a user request.

    ``nsctl-local-environment``
        Create, start, inspect and stop local environments, explaining the
        control plane, substrate and RepoClass distinction before mutation.

    ``nsctl-repoclass-author``
        Author and validate a RepoClass through ``nsctl repoclass`` verbs and
        use ``describe --json`` to inspect the normalized result.

    ``nsctl-local-plugin``
        Guide a service developer through local plugin configuration and
        Floci-safe local deployment alternatives.  It may explain the legacy
        Python ``init-ns-local`` pack, but only invokes verbs actually present
        in the installed ``nsctl`` version.

    ``nsctl-debug``
        Gather status, logs, plans and release evidence; classify a failure and
        propose a remedy.  It never purges, tears down or changes configuration
        merely as a diagnostic action.

    ``nsctl-deploy-plan``
        Inspect BOM, repository, lock and environment state before a deploy;
        identify effects and require the user's confirmation immediately before
        a command that starts or applies work.

    ``nsctl-auth-and-profiles``
        Guide login, profile selection, credential verification and logout.
        It does not print, persist in generated output, or ask a user to paste
        a token or secret.

    The initial release shall not bundle ``nsctl-repoclass-adopt``.  Its useful
    workflow depends on the evidence contract of ``nsctl repoclass detect``,
    which NERD009 specifies but the CLI has not implemented.  When ``detect``
    exists, that skill shall run ``detect --json`` before proposing changes,
    run ``describe --json`` before and after each edit, write only through CLI
    verbs, and obtain explicit human confirmation before adding a dependency.

    Each skill shall have a narrow description that names the requests that
    should activate it.  It shall use instructions rather than scripts unless
    deterministic local processing is necessary.  A bundled skill shall name
    only commands provided by the same release's Cobra command tree; generating
    or validating packs against that tree is required so a released skill never
    documents a removed or imagined verb.

.. spec:: List and inspect bundled skills
    :id: HMD_CLI_NEURONSPHERE_NERD015_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD015
    :status: proposed

    ``nsctl agent skills list`` shall list the bundled skill name, a one-line
    description and its installed state for each selected host and scope.  It
    takes ``--host codex|claude|all`` (default ``all``), ``--scope
    project|user`` (default ``project``), and ``--path DIR`` for project scope
    (default the current directory).  ``--json`` shall emit the same data with
    concrete target paths.

    Listing is read-only.  A missing target repository, a missing agent
    directory, or a host the user has not otherwise installed is not an error:
    these are locations ``nsctl`` can prepare.  An unknown skill, host or scope
    is a usage error.

.. spec:: Install and remove selected skills
    :id: HMD_CLI_NEURONSPHERE_NERD015_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD015
    :status: proposed

    The command grammar shall be:

    .. code-block:: text

       nsctl agent skills list [--host codex|claude|all]
                              [--scope project|user] [--path DIR] [--json]
       nsctl agent skills install <skill> [<skill>...]
                                 [--host codex|claude|all]
                                 [--scope project|user] [--path DIR]
                                 [--force] [--dry-run]
       nsctl agent skills remove <skill> [<skill>...]
                                [--host codex|claude|all]
                                [--scope project|user] [--path DIR]
                                [--force] [--dry-run]
       nsctl agent skills doctor [--host codex|claude|all]
                                  [--scope project|user] [--path DIR] [--json]

    ``install`` shall create only the missing parent directories and copy the
    complete bundled skill directory.  By default it refuses to replace a
    target that already exists, even if its name matches a bundled skill.
    ``--force`` replaces only that exact skill directory, after the full set of
    destinations has been resolved and checked.  ``--dry-run`` reports the
    operations without writing.  A multi-skill, multi-host install reports each
    completed destination and returns failure if any requested destination
    could not be installed; it must not silently omit one host.

    ``remove`` refuses by default unless the existing directory is recognizably
    an unmodified ``nsctl`` installation of the named bundled skill.  ``--force``
    removes the exact selected skill directory despite local changes.  It never
    removes a host's enclosing skills directory or any parent directory.
    ``--dry-run`` writes nothing.  A hash or version marker sufficient to make
    this ownership decision may be stored inside each installed skill directory;
    it must not become an input that an agent loads as instructions.

    The CLI shall use atomic file replacement within a destination where the
    platform supports it, and must leave a pre-existing skill intact if staging
    the replacement fails.  It shall print every changed concrete path.

.. spec:: Host locations and project ownership
    :id: HMD_CLI_NEURONSPHERE_NERD015_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD015
    :status: proposed

    The installer shall use these paths, where ``<skill>`` is the bundled skill
    name:

    ====================== ========================================== =============================================
    Host                   Project scope                              User scope
    ====================== ========================================== =============================================
    ``codex``              ``<path>/.agents/skills/<skill>/SKILL.md`` ``~/.agents/skills/<skill>/SKILL.md``
    ``claude``             ``<path>/.claude/skills/<skill>/SKILL.md`` ``~/.claude/skills/<skill>/SKILL.md``
    ====================== ========================================== =============================================

    ``--path`` is invalid with user scope.  Project scope resolves ``--path``
    to an absolute directory and shall refuse a file or nonexistent target;
    creating a repository is outside this command's authority.  User scope
    resolves the invoking user's home through the operating system, not a
    shell expansion or a guessed home directory.

    The default is project scope because the installed guidance then belongs to
    the project, can be code-reviewed, and does not affect unrelated work.  A
    successful ``--host all`` installation writes the same Agent
    Skills-compatible content into each native location.  Host-specific
    frontmatter may be generated only when that host requires it; shared
    instructions must remain semantically equivalent.

.. spec:: Diagnose, do not overreach
    :id: HMD_CLI_NEURONSPHERE_NERD015_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD015
    :status: proposed

    ``doctor`` shall report whether the requested target locations are usable,
    whether each installed skill has the expected entry point and ownership
    marker, and whether its bundled version differs from the installed version.
    It is read-only and does not require Codex or Claude Code itself to be
    installed: skill discovery is owned by those hosts, while path and file
    validation are deterministic work ``nsctl`` can perform.

    The command shall not configure a model, API key, MCP server, plugin,
    shell permission or automatic agent invocation policy.  It shall not
    mutate any file beyond selected skill directories, and shall never edit
    user-authored ``AGENTS.md`` or ``CLAUDE.md``.  The skills themselves shall
    retain normal host approval semantics; installing a skill is not consent to
    execute its destructive operations.

Testing
-------

Tests shall exercise the command tree against temporary project and user-home
directories.  They shall prove the target mapping for both hosts and scopes,
the emitted ``SKILL.md`` frontmatter and contents, idempotent refusal without
``--force``, unchanged user content on a refused replacement, forced
replacement, dry-run non-mutation, unmodified-only removal, and the guarantee
that no parent skills directory is removed.  A release test shall load every
bundled pack and verify that every documented ``nsctl`` command path resolves
from that release's live Cobra tree.

Alternatives considered
-----------------------

**One global ``AGENTS.md``.**  It is always loaded, has no task routing, and
cannot give Claude Code a native slash/invocable skill.  It also makes
preserving a user's instructions needlessly difficult.

**An MCP server.**  The agent already has a local binary.  Adding a transport,
server lifecycle and permission surface to expose the same CLI commands would
not make the operation safer or more capable.

**Install every third-party agent format.**  Support the two named hosts first,
where the directory model is established and testable.  Additional hosts need
their own explicit target mapping and compatibility contract rather than an
unreviewed best-effort copy.
