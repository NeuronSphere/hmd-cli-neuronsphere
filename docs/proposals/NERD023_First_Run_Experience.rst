.. NERD023 First Run Experience

NERD023 First Run Experience
============================

.. req:: Get a newcomer from download to a working environment
    :id: HMD_CLI_NEURONSPHERE_NERD023
    :status: partial

    ``nsctl`` shall carry a guided first run that establishes what is present,
    creates an environment, and offers to adopt the user's own repository --
    without requiring the tutorials to have been read first.

    It shall report the services an environment actually has rather than the
    ports it reserved, and it shall give a RepoClass a way to declare where its
    front door and its credentials are, so that a deployed user interface is
    reachable and loggable-into without reading a Helm chart.

    Nothing here invents a second control plane, a second manifest format, or a
    second source of truth. Every step is an existing ``nsctl`` command the
    guided run names before it runs it.

Motivation
----------

The CLI is written for someone who has already decided they want a local
NeuronSphere. Three things are measurable today on a fresh install.

**Bare ``nsctl`` does not say what to run.** The root command prints sixteen
top-level commands in one alphabetical block (``cmd/root.go``), among them
``authd`` -- a mock identity provider -- as a peer of ``env``. The root ``Long``
introduces the control plane, ``HMD_HOME``, the substrate and RepoClasses, and
names no first command. ``RequireHome`` refuses with ``HMD_HOME is not set.
Export it, or pass --home <path>`` and points nowhere. ``nsctl doctor`` checks
the container engine and the host names; it does not check whether a home is
set, whether an environment exists, or whether the user is signed in, and on a
clean pass it says nothing about what to do next. The whole binary contains two
interactive prompts: the OAuth issuer in ``nsctl login`` and the ``env purge``
confirmation.

The knowledge is not missing -- ``docs/tutorials/first-environment.rst`` is
accurate and complete. It is simply in prose the CLI never surfaces.

**The environment summary advertises rather than reports.** ``readySummary``
prints a Trino endpoint whenever the substrate is ``full``:

.. code-block:: text

    Ready.
      services   http://localhost/local/<service>/
      trino      localhost:19001
      k3s        localhost:19065

The ``trino`` line is port arithmetic from the environment's slot
(``internal/registry/registry.go``), printed on an environment that has never
deployed Trino and never will. ``nsctl env status`` repeats it from the same
condition. A newcomer's first act is to connect to an advertised endpoint that
nothing is listening on.

This is not for want of the answer. ``startCluster`` already calls
``FindTrinoCoordinator`` and takes a different branch when Trino is absent;
the fact is computed and then discarded before the summary is written. Two more
discoveries are thrown away in the same function: the deployed service routes
from Floci's API Gateway are reduced to ``routed N deployed service(s)``, and
the Ingress hostnames of every deployed user interface are read only to warn
about ``/etc/hosts``.

**A deployed user interface cannot be logged into.** Superset's local
administrator is the user ``admin``, with a password in the emulated Secrets
Manager under
``<instance>-<deployment_id>-<environment>-admin-credentials``, property
``password``. That is discoverable only by reading
``hmd-inf-superset/src/helm/templates/admin-password-secret.yaml`` and the
``awsAdminPasswordSecretName`` helper beside it. Airflow's is in a different
place again. Nothing in BACON lets the RepoClass say so, nothing in ``nsctl``
prints it, and no post-deploy notes mechanism exists anywhere in the platform to
hang it on.

Scope and terminology
---------------------

* The **guided first run** is one interactive command. It is a front end over
  existing verbs, not a new code path into the platform.
* An **access declaration** is a BACON block naming a RepoClass's front door:
  a URL, who logs in, and *where* the credential lives. It never holds a
  credential.
* **Revealing** a credential is resolving a declaration against the
  environment's secret store and printing the value. It is always explicit.

.. spec:: ``quickstart``: one command from download to an environment
    :id: HMD_CLI_NEURONSPHERE_NERD023_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD023
    :status: implemented

    ``nsctl quickstart`` shall walk an interactive terminal through, in order:

    1. **Host readiness.** The same ``doctor`` gate ``control-plane start``
       runs, rendered the same way, so the diagnostic and the gate cannot
       drift. A failed check stops the run and carries its existing remedy.

       **Amended by** :doc:`NERD025_Port_First_Addressing` **SPEC004
       (2026-09-24).** Name resolution is no longer part of that gate. It is
       reported by ``doctor`` and does not stop a quickstart, because after
       NERD025 nothing a first run does requires it -- which is what makes
       this command honest about being one command.
    2. **A home.** When ``HMD_HOME`` is unset, propose a path and **print the
       ``export`` line for the user to run**. It shall not write, append to, or
       create a shell profile, an ``rc`` file, or any file outside the chosen
       home. It may offer to continue this run with ``--home``.
    3. **An environment.** Through ``EnsureFirstEnvironment`` and the ordinary
       ``env start`` path -- the first-run auto-registration that already
       exists -- never a second registration mechanism.
    4. **Something deployed.** A stack, *only when a reference resolves*. The
       command shall not carry a hardcoded stack name: it shall resolve a
       candidate and skip the step silently when nothing answers.
    5. **The user's own repository**, through ``repoclass detect`` (SPEC008).
    6. **Agent guidance**, through ``agent skills install``, for the judgement
       the CLI deliberately refuses to make.

    Each step shall print the equivalent command before running it, so that the
    session doubles as a transcript the user can repeat by hand. Each step is
    declinable and declining one shall not abort the run.

    On a non-interactive stdin it shall not prompt, guess, or half-run. It shall
    print the ordered list of equivalent commands and exit as a usage refusal,
    the way ``login`` refuses without a profile.

    ``quickstart`` shall create nothing the named commands would not create, and
    shall never purge, delete, reset or redeploy. It is additive by
    construction.

.. spec:: Help that names a first command
    :id: HMD_CLI_NEURONSPHERE_NERD023_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD023
    :status: implemented

    The root command tree shall be grouped, the way plugin commands already
    are, so that ``nsctl --help`` presents the commands a first run needs
    before authoring, artifact, plugin and authentication verbs. Grouping
    changes presentation only: no command is renamed, moved, or removed, and
    every existing invocation stays valid.

    The root ``Long`` and ``RequireHome``'s refusal shall each name
    ``nsctl quickstart``. A refusal that states a precondition without naming
    the command that satisfies it is incomplete.

.. spec:: Report the services an environment has
    :id: HMD_CLI_NEURONSPHERE_NERD023_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD023
    :status: implemented

    An environment summary and ``env status`` shall report an endpoint only for
    something that is there.

    The Trino endpoint shall be reported only when Trino is routed. The routing
    decision is already recorded: an environment's stream fragment carries a
    Trino listener if and only if a coordinator was found, so the fragment --
    not the substrate mode, and not the port slot -- is the condition. Reading
    it costs no cluster call, which matters because ``status`` must stay cheap
    and must answer on a stopped environment.

    The deployed service paths and the Ingress hostnames that ``env start``
    already discovers shall be reported rather than counted, capped at a
    readable number with the remainder stated as a count.

    Port-slot output shall remain -- an environment's reserved slot is a real
    fact worth printing -- but shall read as a reservation and not as a claim
    that a service answers there.

.. spec:: The ``access`` declaration
    :id: HMD_CLI_NEURONSPHERE_NERD023_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD023
    :status: implemented

    BACON shall gain a top-level ``access`` array. Each entry names one way in
    to the deployed RepoClass:

    .. code-block:: json

       "access": [
         {
           "name": "superset",
           "url": "http://{ingress_host}/",
           "username": "admin",
           "secret": {
             "store": "secrets-manager",
             "key": "{instance_name}-{deployment_id}-{environment}-admin-credentials",
             "property": "password"
           },
           "notes": "Self-registration is off; admin is the only account."
         }
       ]

    It is top-level rather than inside ``local`` because a front door is not a
    local-development fixture: the same declaration describes the cloud
    deployment, with different values substituted.

    ``url`` and ``secret.key`` are templates over placeholders ``nsctl`` already
    holds for a deployed instance: ``{instance_name}``, ``{deployment_id}``,
    ``{environment}``, and ``{ingress_host}`` -- the Ingress hostname
    ``hmd-cli-helm`` gives an instance's chart. An unknown placeholder is a
    validation error, not an empty substitution.

    ``secret.store`` is required when ``secret`` is present and selects the
    store explicitly. This is not decoration. ``create_secret()`` writes SSM
    Parameter Store whatever its name suggests, while Superset's chart reads
    through the Secrets Manager ClusterSecretStore, and the emulator keeps the
    two namespaces separate -- so a reader that infers the store reports "does
    not exist" against a secret that is present in the other one.

    Where the producing RepoClass already publishes the pointer -- a
    ``resources_output`` entry's ``output.secret_name`` is exactly this, and
    reaches consumers as ``hmd_resources`` today -- an entry may name that
    output key instead of restating a template. Resolution order shall be:
    a named output key, then the producer's emitted output, then the manifest's
    template. ``nsctl`` shall not re-derive a name its producer published.

    ``access`` shall be authorable through ``nsctl repoclass access`` verbs,
    readable through ``repoclass describe``, and checked by
    ``repoclass validate``.

.. spec:: What an access declaration never carries
    :id: HMD_CLI_NEURONSPHERE_NERD023_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD023
    :status: implemented

    ``validate`` shall **refuse** an entry carrying a literal ``password``, or
    any literal secret value, and say why: a manifest names where a credential
    lives and never holds one, because a manifest is a file a developer edits
    and may commit, and a secret in one is a secret in a git history. This is
    the rule the control-plane extension credentials block already enforces;
    the same words should be used, because it is the same mistake.

    An ``access`` entry shall not be a mechanism for *creating* a credential.
    It describes one some other actor wrote. A declaration whose secret does
    not resolve is reported as unresolved -- never silently omitted, and never
    replaced with a guess.

.. spec:: ``env credentials``: ask for it, and it is yours
    :id: HMD_CLI_NEURONSPHERE_NERD023_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD023
    :status: implemented

    ``nsctl env credentials [<env>] [--instance <name>] [--reveal] [--json]``
    shall resolve every ``access`` entry of every instance declared in the
    environment and report instance, name, URL and username.

    By default it shall **not** print the secret. It reports where each one
    resolved from, or that it did not resolve, the way the extension credential
    report already says which references resolved and never what they resolved
    to. ``--reveal`` prints the values; it is the only thing that does.

    The default is withholding because the alternative is not a trade-off in
    the user's favour. A summary printed on every ``env apply`` lands in
    terminal scrollback, in CI logs, and in screen shares, for a value the user
    needed once. One extra command is a smaller cost than a password that
    cannot be un-printed.

    Commands that finish a deploy -- an environment summary, ``env apply``,
    ``stack add --apply``, ``stack list`` -- shall print one pointer line
    naming ``env credentials``, and only when some declared instance actually
    has an ``access`` entry. A pointer to an empty table is noise.

.. spec:: A stack declares access the same way
    :id: HMD_CLI_NEURONSPHERE_NERD023_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD023
    :status: proposed

    A stack is a RepoClass, so it declares ``access`` with the same block and
    no stack-specific mechanism. A stack's reported access is its own entries
    together with those of the instances it declared, which the environment
    manifest's stack record already identifies.

    Because the declaration belongs to the workload's own RepoClass, it applies
    however that instance arrived -- ``stack add``, ``env add --from-repo``, or
    ``repo add`` -- and a stack authored later inherits it without restating it.
    NERD017 is amended to say so rather than to define anything new.

.. spec:: ``detect``, and the adoption skill it unblocks
    :id: HMD_CLI_NEURONSPHERE_NERD023_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD023
    :status: implemented

    ``nsctl repoclass detect`` shall be implemented as NERD009 SPEC009 and
    SPEC010 already specify it, with no change to that contract. SPEC010's
    refusals are the load-bearing half: no dependency entry, no resource entry,
    no resource declaration, not even a provisional or commented one.

    With ``detect`` present, NERD015 SPEC001's stated condition for bundling
    ``nsctl-repoclass-adopt`` is met and that skill shall be bundled. The
    ``nsctl-onboard`` skill shall name ``quickstart`` and ``detect``.

    The division of labour is the point of this specification. ``detect``
    gathers evidence and writes only what is unambiguous; the skill proposes
    the manifest and asks a human about anything that is a judgement. Neither
    half guesses a dependency role, because a wrong role marked required fails
    the entire ChangeSet with a message naming only the role -- and a
    newcomer's first ``env apply`` failing on a role they never chose is the
    worst outcome available, produced by guessing helpfully.

Testing
-------

Tests shall run against the CLI contract suite, which executes the binary with
no ``HMD_HOME``, no container engine and no credential:

* Grouped help presents the first-run group first, and every pre-existing
  command path still resolves.
* ``quickstart`` on a non-interactive stdin prompts for nothing, writes
  nothing, and prints the ordered equivalent commands.
* ``repoclass access add`` then ``describe --json`` round-trips, preserving
  keys the CLI does not model.
* A manifest with a literal ``password`` fails ``validate``.
* An unknown placeholder in ``url`` or ``secret.key`` fails ``validate``.
* ``repoclass detect --json`` against table-driven fixtures reports its
  classification **and its refusals**; the refusals are the assertions that
  matter.
* Every verb named in a bundled skill resolves from that release's command
  tree.

An environment-level test shall prove SPEC003 by control: an environment on a
``full`` substrate with no Trino reports no Trino endpoint, and the same
environment reports one once Trino is routed. Asserting only the second half
would pass against the present defect.

Alternatives considered
-----------------------

**A richer ``doctor`` instead of a new command.** ``doctor`` is a read-only
diagnostic and is the gate ``control-plane start`` runs. Making it mutate, or
prompt, would either break that reuse or make a preflight interactive.

**A terminal UI.** The binary links no TUI library and needs none: the flow is
a short ordered sequence of yes/no and one-line answers, which the two prompts
already in the tree handle. A full-screen interface would also be the first
thing in ``nsctl`` that cannot be driven from a script.

**Writing the ``export HMD_HOME`` line into the user's shell profile.** It is
their file, the correct file is unknowable, and an appended line is a change
they did not review. Printing it costs one paste and leaves the decision where
it belongs.

**Printing credentials in the deploy summary.** Most discoverable, and
irreversible: the value reaches scrollback, CI logs and screen shares for
everyone who ever runs ``env apply``. A named command is discoverable enough
once the summary names it.

**Inferring the front door instead of declaring it.** ``nsctl`` can see an
Ingress host and a Secret name in the cluster, and could guess a login form. It
cannot know which of several hosts is the one a human uses, which secret backs
it, or that self-registration is off -- and a confident wrong answer about a
credential is worse than no answer. The RepoClass author knows; the declaration
is where they say so.
