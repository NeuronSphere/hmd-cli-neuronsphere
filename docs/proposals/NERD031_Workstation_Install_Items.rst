.. NERD031 Workstation Install Items

NERD031 Workstation Install Items
=================================

.. req:: An artifact declares what installing it puts on a workstation
    :id: HMD_CLI_NEURONSPHERE_NERD031
    :status: partial

    A RepoClass's published artifact shall be able to declare, in its own
    BACON manifest, what installing it onto a person's workstation does: the
    commands it adds under ``nsctl``, the agent skills it installs, and the
    documents it places for a local knowledge base. ``nsctl plugin install``
    shall fetch such an artifact from the Artifact Librarian and carry out
    that declaration. ``nsctl plugin remove`` shall undo it.

    The first consumer is the Python ``hmd`` toolset. An artifact of the
    ``hmd-cli-toolchain`` RepoClass declares a ``uv``-managed Python
    environment, pinned by a hashed lock it carries and installed from a
    package index the user configured beforehand. After ``nsctl plugin
    install librarian:hmd-cli-toolchain``, ``nsctl hmd <args...>`` works, and
    no ``hmd`` executable exists anywhere on the machine.

    Installing shall never run code the artifact carries. ``nsctl`` shall not
    read, store or discover a package-index or git credential.

Motivation
----------

``nsctl`` is the front door to NeuronSphere. The Python front end it replaced
is gone (:doc:`/explanation/python-compatibility`), but the other ``hmd-cli-*``
packages are not: ``hmd build``, ``hmd deploy``, ``hmd bender``, ``hmd
bartleby`` and ``hmd repo`` are still Python and still reached only through
the ``hmd`` umbrella. Today a person gets them by installing packages by hand
from a private index, at whatever versions the resolver picked that day, into
whatever environment they had open. Nothing in ``nsctl`` says any of this.

NERD002's withdrawal note refused to port them, and that argument still
holds. The fix is not a port. What is needed is a way for a *published,
versioned artifact* to say "install me like this" and for ``nsctl`` to do it.

The platform already has most of the pieces:

* **The artifact.** A RepoClass version in the Artifact Librarian is a zip of
  its repository tree (NERD005, NERD010). ``nsctl`` fetches it, resolves
  version specifications (NERD011) and caches it under ``HMD_HOME``.
* **The noun.** A declared plugin runs as ``nsctl <noun>`` with argv passed
  through, ``HMD_HOME`` resolved, and ``hmd.env`` layered into the child
  environment (NERD018).
* **The skill installer.** NERD015 already knows where Claude Code and Codex
  look for skills, per user and per project, and how to detect a
  hand-modified install.

What is missing is the declaration that ties them together, and the
declaration belongs to the artifact. The ``hmd-cli-toolchain`` artifact knows
its lock, its entry point, and that it needs ``uv``. ``nsctl`` should not.

The same declaration serves more than Python. A team's runbook scripts, the
authoring skills of ``hmd-ms-transform``, or a knowledge base of design notes
are the same shape: a versioned artifact that puts something on a workstation.
Today skills ride inside pip packages and are copied out by ``hmd ai
install``. With this declaration, the artifact a RepoClass already deploys
from can carry them itself.

Scope and terminology
---------------------

* The **install section** is the optional top-level ``install`` object in a
  BACON manifest. Its canonical definition lives in ``hmd-docs-bacon``
  (``docs/spec/install.rst``). This document specifies how ``nsctl`` carries
  it out.
* An **item** is one entry in ``install.items``. It has a **kind**
  (``command``, ``agent-skills`` or ``docs``) and a **source** (``artifact``
  or ``git``).
* A **command item** has a **runtime** (``python``, ``scripts`` or
  ``binary``) and a **noun**.
* An **installed plugin** is a RepoClass whose install section ``nsctl`` has
  carried out, recorded in ``nsctl.toml`` under the class name (SPEC003).
  NERD018 is amended so that "plugin" means this.

Out of scope, deliberately:

* **Porting any** ``hmd-cli-*`` **package.** NERD002's withdrawal stands.
* **Running artifact code at install time.** There are no install hooks and
  no ``exec`` in the install section (SPEC004).
* **A package manager.** ``nsctl`` installs one declared, locked set per
  Python item. It does no resolution of its own, keeps no second index and has
  no upgrade policy beyond ``plugin update``.
* **A search engine over docs.** The docs kind places files and indexes them
  (SPEC010). Searching them is a consumer's job.
* **Keeping a git clone current.** A cloned item belongs to the user after
  the first clone (SPEC011).

.. spec:: The install section
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD031
    :status: implemented

    .. code-block:: json

        "install": {
          "requires": [
            {"binary": "uv",
             "install_hint": "brew install uv  (or: curl -LsSf https://astral.sh/uv/install.sh | sh)"}
          ],
          "items": [
            {"kind": "command", "noun": "hmd",
             "summary": "The Python hmd toolset: repo, build, deploy, bender, bartleby",
             "runtime": {"kind": "python", "python": "3.12",
                         "lock": "requirements.lock", "entry_point": "hmd.main:main"},
             "index_hint": "Configure the hmd-cli-* index in $HMD_HOME/.config/uv.toml"},
            {"kind": "command", "noun": "ops", "summary": "Team runbooks",
             "runtime": {"kind": "scripts",
                         "scripts": {"drain": "scripts/drain.sh", "rotate": "scripts/rotate.py"}}},
            {"kind": "agent-skills", "dir": "skills/"},
            {"kind": "docs", "dir": "docs/", "format": "rst", "title": "Transform authoring"},
            {"kind": "docs", "title": "Design notes",
             "source": {"git": "git@github.com:acme/design-notes.git", "ref": "main"}}
          ]
        }

    Every path is relative to the artifact root and must stay inside it. A
    path that resolves outside the root, or through a symlink, is a
    validation error. ``nsctl repoclass validate`` (``internal/bacon``) knows
    the section and reports these errors, so an author finds them before a
    user does.

    ``requires`` lists host binaries the items need, each with a
    ``install_hint`` written by the author. ``nsctl`` looks each one up on
    ``PATH`` and does nothing else with it: it never downloads, installs or
    upgrades a prerequisite. A git-sourced item implies ``git``, with a
    default hint when the author gave none.

    ``kind``, ``runtime.kind`` and ``source`` are closed enumerations. An
    unknown value is refused with the list of known ones, so an artifact
    written for a newer ``nsctl`` fails clearly and never half-installs.

.. spec:: The librarian plugin source
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD031
    :status: implemented

    .. code-block:: text

        nsctl plugin install librarian:<class>[@<version-spec>]

    The reference is parsed as ``librarian.Spec`` already parses
    ``<class>@<ver>:<type>``, and the type defaults to ``build``. The version
    specification resolves through NERD011, so the newest match wins and an
    omitted spec means the newest version. The fetch uses the existing
    ``librarian.Client``, with its existing URL, API-key and login-token
    precedence. The zip is stored through ``artifact.Store`` into the same
    ``artifacts/<class>@<version>/`` cache the environment resolver uses,
    rather than a second copy.

    An NERD016 class artifact (``oci://`` whose ``artifactType`` is the class
    artifact type) is accepted by the same path, because it carries the same
    zip. An ``oci://`` reference whose ``artifactType`` is the NERD018 plugin
    type keeps today's behaviour exactly (SPEC008).

    An artifact with no install section is refused, with a message naming the
    section: "``<class>@<version>`` declares nothing to install". Installing
    a deployable RepoClass as a plugin is an error, not a no-op.

.. spec:: The declaration records what was installed
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD018_SPEC001
    :status: implemented

    .. code-block:: toml

        [plugin.hmd-cli-toolchain]
        source  = "librarian:hmd-cli-toolchain"
        version = "1.4.12"
        digest  = "sha256:..."            # of the fetched zip

          [[plugin.hmd-cli-toolchain.item]]
          kind    = "command"
          noun    = "hmd"
          summary = "The Python hmd toolset: repo, build, deploy, bender, bartleby"
          path    = ".cache/neuronsphere/installs/hmd-cli-toolchain@1.4.12/hmd"

          [[plugin.hmd-cli-toolchain.item]]
          kind  = "agent-skills"
          paths = ["/Users/me/.claude/skills/transform-author"]

    The table key is the class name. ``nsctl`` writes the item records as the
    ledger of what it placed, and ``list``, ``remove`` and dispatch read them.
    It never re-reads the artifact to find out what it did. The strict decoder
    gains these fields and nothing looser.

    A NERD018 declaration with no ``item`` table is read as a single
    ``command``/``binary`` item whose noun is the table key. Every
    installation that exists today therefore keeps working with no migration.

    **Nouns are unique across plugins.** ``install`` refuses a noun that is
    reserved (NERD018 SPEC004), or already claimed by an item of another
    plugin, before anything is fetched beyond the manifest.

.. spec:: The install transaction
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD031
    :status: implemented

    ``install`` proceeds in a fixed order, and nothing is written outside
    ``nsctl``'s artifact cache until every check has passed:

    #. Fetch and store the artifact (SPEC002). Validate the install section
       (SPEC001).
    #. Check every ``requires`` binary. Check that ``HMD_REPO_HOME`` is set if
       any item is git-sourced. Check nouns (SPEC003). **All** failures are
       reported together, each with its hint.
    #. Place every item into one staging directory,
       ``$HMD_HOME/.cache/neuronsphere/installs/<class>@<version>.installing/``,
       with one subdirectory per item. Clones and skills are placed where they
       belong (SPEC009, SPEC011), and skills are placed last, because they are
       the only writes outside ``HMD_HOME`` and the repository folder.
    #. Update the docs index, then rename the staging directory into place.
       This is ``internal/plugin``'s stage-and-rename, applied to the whole
       version.
    #. Write the declaration last.

    If any item fails, the items already placed by this run are removed, the
    declaration is not written, and the previous version, if one was
    installed, is untouched. An upgrade removes the previous version's items
    only after the new declaration is written.

    Every precondition or placement failure exits ``2``. The installer's own
    exit code is never returned, so it cannot be mistaken for a command's.

    **Installing never runs artifact code.** ``nsctl`` copies files, unpacks
    the zip, runs ``uv`` with arguments it composes itself (SPEC005) and runs
    ``git clone`` (SPEC011). The one exception is the smoke test of a python
    item, which imports the declared entry point. That is the code the user
    is installing in order to run, and running it once is how ``nsctl`` knows
    the environment works. Script and binary items are never executed until
    the user types their noun.

.. spec:: command + python
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD031_SPEC004
    :status: implemented

    The item's placed directory is a ``uv`` virtual environment:

    .. code-block:: text

        uv venv --python <python> <stage>/env
        uv pip sync --python <stage>/env --require-hashes <artifact>/<lock>

    ``uv`` is the one taken from ``PATH``. ``uv`` can install the requested
    CPython itself, so the host needs no Python. The lock must be a hashed
    requirements file (``uv pip compile --generate-hashes``). A lock without
    hashes is a validation error, because an unhashed lock pins names, not
    content.

    **The environment is created** ``--relocatable``, so the staged
    directory can be renamed into place. Its placed directory is
    ``installs/<class>@<version>/<noun>``, and the declaration records the
    lock's sha256. *Not built:* reusing the previous environment on an
    upgrade whose lock hash is unchanged. Every upgrade re-provisions today,
    which is correct but slower. The recorded hash is what that
    optimisation will compare against.

    **Success is not "uv exited zero".** Before the rename, ``nsctl`` runs the
    environment's interpreter to import ``module`` and resolve ``function``
    from ``entry_point``. If that fails, the provision has failed.

    ``nsctl`` then **deletes the console script** named by the entry point
    from ``env/bin``, if one exists, and launches the item as:

    .. code-block:: text

        <env>/bin/python -c "import sys; from <module> import <function> as m; sys.argv[0] = '<noun>'; sys.exit(m())" <args...>

    This is what SPEC013 relies on.

.. spec:: The package index is the user's setup
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD031_SPEC005
    :status: implemented

    ``hmd-cli-*`` distributions live on a private index. ``nsctl`` does not
    resolve, store or discover its credential. The environment it composes
    for ``uv`` is the NERD018 SPEC005 environment (the process environment,
    then ``hmd.env`` keys the process did not set, then ``HMD_HOME``), plus
    one default:

    * ``UV_CONFIG_FILE=$HMD_HOME/.config/uv.toml``, **only** when that file
      exists and ``UV_CONFIG_FILE`` is not already set. This is the file
      ``hmd python login`` writes its index configuration into today.

    ``uv`` reads its own variables: ``UV_INDEX_URL``, ``UV_EXTRA_INDEX_URL``
    and ``UV_CONFIG_FILE``. It does **not** read ``PIP_INDEX_URL`` or
    ``PIP_EXTRA_INDEX_URL``. A user whose index is configured only for
    ``pip`` has not configured it for this. The how-to says so, and so does
    the failure message.

    Failure classification. The tail of ``uv``'s stderr is always shown:

    * **HTTP 401/403 from an index.** Show the item's ``index_hint`` and the
      variable names above.
    * **Unresolvable or hash mismatch.** Name the lock and the distribution.
    * **No network.** Say so.

    **Redact.** Index URLs commonly embed a token
    (``https://user:token@host/simple``). ``nsctl`` removes URL userinfo from
    every byte it prints, including the ``uv`` tail it forwards. The host may
    be shown.

    **No silent retry.** A rejected credential fails once, loudly.

.. spec:: command + scripts
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD031_SPEC004
    :status: implemented

    ``runtime.scripts`` maps a subcommand name to a file. Install copies the
    files into the placed directory and sets the executable bit. Each file is
    run by its shebang, or by ``runtime.interpreter`` when one is given, which
    then must be in ``requires``.

    ``nsctl <noun> <name> <args...>`` runs that file under the NERD018
    SPEC005 exec protocol. ``nsctl <noun>`` alone, or ``--help``, lists the
    names from the declaration.

    The map is explicit on purpose. ``nsctl`` does not list a directory to
    find scripts, because that would be NERD018 SPEC006 discovery with an
    extra step. A file the author did not name is not a command.

.. spec:: command + binary
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD018_SPEC002
    :status: implemented

    ``runtime.binaries`` maps ``GOOS_GOARCH`` to a path inside the artifact.
    Install copies the one for the running platform, or refuses and names the
    platforms that are present.

    This is today's NERD018 plugin expressed as an item. The OCI plugin
    artifact (NERD018 SPEC002) remains a supported transport for exactly this
    item and nothing else. It is the better one for a large per-platform
    binary, because a librarian zip carries every platform.

.. spec:: agent-skills
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD015
    :status: implemented

    ``dir`` holds skill directories, each with a ``SKILL.md``. Install hands
    them to the NERD015 installer, whose source is generalised from the
    embedded set to any ``fs.FS``. Destinations, frontmatter checks and
    modification detection are therefore NERD015's, unchanged.

    ``plugin install`` takes NERD015's ``--host codex|claude|all`` and
    ``--scope project|user``, and the item's recorded ``paths`` are the
    destinations written. The default is ``--scope user``, unlike ``nsctl
    agent skills install``, because a plugin is installed per machine, not
    per repository.

    ``remove`` removes those paths through NERD015's ``Remove``. A skill the
    user has edited is kept and reported, never deleted, which is NERD015's
    rule.

    A skill from an artifact whose name matches a bundled ``nsctl`` skill is
    refused. The bundled one belongs to ``nsctl``.

.. spec:: docs
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC010
    :links: HMD_CLI_NEURONSPHERE_NERD031
    :status: implemented

    An artifact-sourced docs item is copied into its installed version's
    directory, ``installs/<class>@<version>/docs-<n>/``, so that an upgrade
    replaces it and ``remove`` deletes it with everything else. A git-sourced
    one stays where it was cloned (SPEC011). Consumers find either through the
    index, not through the path.

    Either way, ``$HMD_HOME/knowledge/index.json`` gains an entry with the
    class, version, ``title``, ``format`` and absolute path. ``format``
    (``rst``, ``markdown``, ``html`` or ``text``) is descriptive. ``nsctl``
    never renders anything. An author who wants HTML builds it at ``hmd
    build`` time and points ``dir`` at the output.

    ``NSCTL_KNOWLEDGE`` (the ``knowledge`` directory) joins the NERD018
    SPEC005 environment, so a skill or command can find the index without
    knowing ``HMD_HOME``'s layout. ``nsctl plugin list`` shows docs items with
    their paths. There is no ``nsctl knowledge`` noun, and none is proposed
    until something needs more than the index.

.. spec:: The git source
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC011
    :links: HMD_CLI_NEURONSPHERE_NERD031_SPEC004
    :status: implemented

    ``"source": {"git": "<url>", "ref": "<branch|tag>"}`` places an item by
    cloning, not by copying from the artifact. It is allowed for ``docs``,
    ``agent-skills`` and ``command`` + ``scripts``, and refused for
    ``command`` + ``python``, whose lock must be a published pin.

    * **Where.** ``$HMD_REPO_HOME/<repo-name>``, where ``<repo-name>`` is the
      last path segment of the URL without ``.git``, or the item's
      ``checkout`` name when given. This is the directory the user already
      keeps repositories in, and a clone there is the working tree
      RepoClass resolution already prefers. If ``HMD_REPO_HOME`` is unset,
      install exits ``2`` naming ``hmd.env`` and ``--repo-home``.
    * **How.** The ``git`` CLI (``git clone --branch <ref>``), with the
      user's environment. The user's credential helper, SSH agent and
      ``insteadOf`` rules apply, and ``nsctl`` never sees a git credential.
      This is the same reasoning that delegates registry credentials to the
      ``docker`` CLI.
    * **Already there.** If the directory is a clone whose ``origin`` matches
      the URL, it is used as it is and reported as "already present".
      ``nsctl`` never fetches, pulls, resets or checks out in it. If it is
      anything else (another origin, not a repository, a file), install
      refuses and names the path.
    * **After that it is the user's.** ``ref`` applies to the first clone
      only. ``plugin update`` does not touch a clone.
    * **Remove never deletes a clone.** It removes the item's other effects
      (skill destinations, index entry, noun) and prints the clone's path. A
      clone is a working tree, and it may hold the only copy of someone's
      work.

    Skills from a git item are **copied** from the clone into their
    destinations, not linked, so NERD015's modification detection still means
    something. ``plugin update <class>`` re-copies them from the clone as it
    now stands, which is how a team's edited skills reach the agent.

.. spec:: Dispatch, and what a command noun adds
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC012
    :links: HMD_CLI_NEURONSPHERE_NERD018_SPEC005
    :status: implemented

    Each command item attaches one node under NERD018 SPEC004, in the plugin
    help group, with the item's ``summary`` as its short help. The exec
    protocol is NERD018 SPEC005 unchanged, with ``NSCTL_PLUGIN_NAME`` set to
    the noun, ``NSCTL_PLUGIN_VERSION`` to the class version and
    ``NSCTL_KNOWLEDGE`` added (SPEC010).

    Three consequences are written down here so they are not filed as bugs:

    * **``nsctl hmd X`` is not byte-identical to running ``hmd X``.** It runs
      with ``nsctl``'s resolved home and ``hmd.env``. ``nsctl --home /alt hmd
      repo configure`` retargets the whole Python toolset for one invocation,
      and ``HMD_REPO_HOME`` set only in ``hmd.env`` reaches ``hmd repo
      pull-all``. That is the point.
    * **Exit codes are the target's.** Cement exits ``1`` for any exception,
      so ``nsctl``'s exit-code vocabulary does not hold under a Python noun.
      Only the plugin's own preconditions use ``2``.
    * **Interrupting a Cement prompt exits 0.** The terminal delivers
      ``SIGINT`` to the child, and Cement maps ``KeyboardInterrupt`` to exit
      code ``0``. This is true of bare ``hmd`` today. The fix belongs in
      ``hmd-cli-app``.

    Standard streams are never wrapped. The dispatch node's streams are the
    process's own files, so InquirerPy, ``rich`` and cookiecutter prompts see
    a TTY.

.. spec:: Reachable only through nsctl
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC013
    :links: HMD_CLI_NEURONSPHERE_NERD031_SPEC005
    :status: implemented

    A python item leaves **no executable named for its noun** anywhere:

    * the environment lives under ``HMD_HOME``'s cache and is never added to
      ``PATH``;
    * ``nsctl`` writes no shim, alias or symlink;
    * the console script is deleted (SPEC005).

    This is **obscurity, not enforcement**, and the documentation says so. A
    person who finds the environment can still run its interpreter. What it
    buys is that the supported way is the only obvious way, and that nothing
    installed by this path collides with, or shadows, an ``hmd`` the user
    installed themselves.

    It also removes a hazard. Because the target is never looked up by name,
    ``alias hmd='nsctl hmd'`` cannot recurse.

.. spec:: list, update, remove
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC014
    :links: HMD_CLI_NEURONSPHERE_NERD018_SPEC003
    :status: implemented

    * ``list`` prints one row per plugin and one indented row per item: kind,
      noun or title, path, and a state (``installed``, ``missing``, or
      ``clone`` for a git item). ``--json`` carries the same data.
    * ``update [<class>]`` is ``install`` with the newest version matching the
      declared source.
    * ``remove <class>`` removes every item this plugin placed, under the
      rules of SPEC009 and SPEC011, then the ``installs/<class>@*``
      directories, then the declaration. ``remove <noun>`` for a NERD018
      single-binary plugin keeps working, because its class key and its noun
      are the same.

.. spec:: hmd-cli-toolchain
    :id: HMD_CLI_NEURONSPHERE_NERD031_SPEC015
    :links: HMD_CLI_NEURONSPHERE_NERD031
    :status: proposed

    The first consumer is a new RepoClass, ``hmd-cli-toolchain``, which holds
    only:

    * ``requirements.in``, the ``hmd-cli-*`` set, seeded from the
      ``neuronsphere`` meta-package's pins;
    * ``requirements.lock``, from ``uv pip compile --generate-hashes``,
      regenerated by ``make lock`` and committed;
    * a manifest whose install section has one python item (noun ``hmd``,
      entry point ``hmd.main:main``) and requires ``uv``.

    It is published with ``nsctl artifact push``, so the zip is the repository
    tree and carries the lock. It does not depend on what ``hmd build`` leaves
    in ``./build``, which has never included a wheel or a lock. Its version is
    the lock's version: a changed lock is a new release, and ``nsctl plugin
    update`` delivers it.

    It establishes nothing about per-tool nouns. ``nsctl bender`` and ``nsctl
    build`` are not proposed. One noun that ``nsctl`` will never take is the
    reason for choosing ``hmd``.

Testing
-------

``internal/installitems`` is tested against a temporary ``HMD_HOME``, a
fake librarian and a fake ``uv`` on ``PATH``.

* **Redaction, written first.** A token embedded in an index URL appears in
  no byte of either stream on any failure path.
* **Requirements.** A missing ``uv`` prints the manifest's
  ``install_hint``, writes nothing outside the artifact cache and exits ``2``.
  Several missing requirements are reported together.
* **Python items.**

  * A ``uv`` that exits ``0`` without a working entry point is still a
    failure, and it leaves no ``installs/`` directory. A killed install
    retries cleanly.
  * A stub index that returns ``401`` prints ``index_hint`` and names
    ``UV_INDEX_URL``.
  * A lock without hashes is refused at validation.
  * After install, ``env/bin`` has no console script for the noun.
* **Scripts items.** A listed script dispatches with argv verbatim, an
  unlisted file in the same directory is not a command, and ``nsctl <noun>``
  alone lists the names.
* **Skills items.** Skills land in the Claude and Codex locations. An edited
  skill survives ``remove``. A name that collides with a bundled skill is
  refused.
* **Docs items.** Docs land under ``knowledge/``, ``index.json`` is updated
  and cleaned on ``remove``, and ``NSCTL_KNOWLEDGE`` reaches a command's
  child.
* **Git items.** These use a local bare repository as the remote.

  * The clone lands at ``$HMD_REPO_HOME/<name>``.
  * A second install over the same origin is a no-op, and an edit made in the
    clone survives it.
  * Another origin is refused.
  * An unset ``HMD_REPO_HOME`` exits ``2``.
  * ``remove`` leaves the clone and prints its path.
* **The transaction.** A failure in the third item removes the first two.
  An upgrade that fails leaves the previous version working.
* **Compatibility.** An existing NERD018 declaration with no items still
  dispatches, lists and removes unchanged.

The acceptance run is performed in a scratch ``HMD_HOME``, beside no live
platform.

#. Push ``hmd-cli-toolchain`` to the local librarian.
#. ``nsctl plugin install librarian:hmd-cli-toolchain``.
#. See ``hmd`` in ``nsctl --help`` with its summary.
#. Run ``nsctl hmd --version`` and ``nsctl hmd repo --help``.
#. Confirm that ``nsctl --home <alt> hmd ...`` makes the child see
   ``<alt>``.
#. Confirm that ``which hmd`` finds nothing new.
#. ``nsctl plugin remove hmd-cli-toolchain``, and see everything gone.

Alternatives considered
-----------------------

**The first draft of this NERD: an OCI Go shim, nsctl-hmd, with an embedded
lock.** It needed a Go release to change a Python pin, and it put the
knowledge of the lock in the wrong artifact. It also resolved ``hmd`` from
``PATH`` as a fallback, which is the executable this design removes.

**Port the** ``hmd-cli-*`` **packages to Go.** NERD002 withdrew this. It
costs the most and delivers the same commands.

**Freeze the Python with PyInstaller, or ship a virtual environment in the
artifact.** A frozen build is sealed against ``entry_points()`` from
packages a customer installs, and a virtual environment is not relocatable.

**Allow** ``exec`` **in the install section.** This is the most general
option, and it makes every install a script run with the user's rights on
the strength of a zip. A closed set of kinds that ``nsctl`` carries out
keeps install reviewable. A kind that is missing is a NERD, not a shell
line.

**A separate noun (** ``nsctl kit`` **).** This names the bundle better,
but it duplicates NERD018's dispatch, declaration and help. Broadening
"plugin" to mean "what an artifact put on this workstation" costs one
definition.

**Discover scripts or skills by listing a directory.** This is NERD018
SPEC006 discovery with an extra step. Scripts are named in the manifest, and
skills are named by their own ``SKILL.md`` inside a declared directory,
under NERD015's existing rule.

**Keep a git item current (pull on update).** This is convenient until it
rebases someone's uncommitted work. The clone is the user's working tree,
so ``nsctl`` clones it once and then leaves it alone.
