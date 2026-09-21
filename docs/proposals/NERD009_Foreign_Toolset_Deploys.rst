.. NERD009 Foreign Toolset Deploys

NERD009 Foreign Toolset Deploys
===============================

.. include:: ../_includes/paid-cloud.txt

.. req:: Deploy a repo that was never a NeuronSphere repo, using its own toolset
    :id: HMD_CLI_NEURONSPHERE_NERD009
    :status: proposed

    ``nsctl`` shall be able to take a repository that has no BACON metadata and
    no NeuronSphere source layout, generate the metadata the deployment graph
    needs, and deploy it -- **by running that repository's own deploy command,
    in an image that repository names**.

    It shall not require the repository to be reshaped. No file shall be moved
    into ``src/helm/``, ``src/docker/`` or ``src/cdktf/``, and no deploy script
    shall be authored on the author's behalf.

    It shall not require ``hmd-img-projectbuilder``. A repo class may name the
    OCI image its deploy node runs in, and ``nsctl`` shall assume nothing of
    that image beyond its being an OCI image.

    ``nsctl`` shall bundle no language model. The metadata that describes a
    codebase is authored by whatever AI surface the user already has, driving
    deterministic commands that own correctness.

.. note::

    ``exec`` is already a **spec-mandatory** BACON tool identifier --
    ``hmd-docs-bacon/docs/spec/toolsets.rst:75`` says *"every conforming tool
    set MUST implement it"*, and its own worked example is
    ``["exec", "make", "deploy"]``. No tool set implements it. This document
    does not widen the BACON spec so much as it finally reads it.

Motivation
----------

NERD002 withdrew the original port of all forty ``hmd-cli-*`` packages on one
argument: *"Adoption is gated by the local platform -- the thing a newcomer runs
first."* That argument is right, and ``nsctl`` has now delivered on it. A
newcomer installs one binary, has Docker, and gets a control plane and an
environment substrate.

Then they try to deploy their own repository, which is the reason they came, and
nothing in ``nsctl`` helps. ``nsctl repo add`` declares an *instance* of a
RepoClass; it cannot create the RepoClass. Their repo has no
``meta-data/VERSION``, no ``meta-data/manifest.json``, and nothing ``hmd
deploy`` knows how to dispatch. The motion ends one command short of its own
goal.

**The tempting fix is the wrong one.** It is easy to describe: move the
Dockerfile to ``src/docker/``, move the chart to ``src/helm/``, write a
``src/local/deploy_local.sh``, and the repo becomes a well-formed repo class.
Every part of that is an imposition of NeuronSphere's shape on a stranger's
repository, at the exact moment the goal was to lower the cost of trying the
platform. A tool that rewrites your repo layout on first contact is not a tool
you try.

It also does not work, for a reason worth stating precisely: **the BACON tool
set hard-codes the NeuronSphere source layout.** ``hmd helm`` reads and writes
``./src/helm`` in six places
(``hmd-cli-helm/src/python/hmd_cli_helm/hmd_cli_helm.py:179``, ``:181``,
``:185``, ``:219``, ``:240``, ``:286``). ``hmd docker`` sets ``dockerfile =
"./src/docker/Dockerfile"`` unconditionally, overwriting whatever the build
config carried
(``hmd-cli-docker/src/python/hmd_cli_docker/hmd_cli_docker.py:261``). ``hmd
cdktf`` uses ``src/cdktf``
(``hmd-cli-cdktf/src/python/hmd_cli_cdktf/hmd_cli_cdktf.py:96``, ``:146``). So
``deploy.commands: [["helm"]]`` against a repo with its chart in ``chart/``
names a path that does not exist. Symlinking around it fails too: ``copyTree``
deliberately skips symlinks rather than following them
(``internal/runner/workspace.go:161-166``), so the overlay workspace arrives
without the file.

**The real user arrives with a toolset, and it is not ours.** They deploy with
kustomize, or skaffold, or Tilt, or pulumi, or ``terraform apply``, or a
``make deploy`` target, or a shell script their CI runs. That toolset is not
BACON-compatible and never will be, and asking them to port it is asking for
the work this binary exists to remove. But they have two things that make
adapting to it cheap: **a command that already deploys their software**, and
**an image their CI already proves has the tools to run it in**.

So the direction inverts. Do not translate their repo into BACON's vocabulary.
Record their command, record their image, and run it. BACON metadata becomes the
*envelope* -- the minimum ``hmd-ms-deployment`` needs to schedule the node and
wire its dependencies -- rather than an attempt to re-describe their build.

Three things already in the tree make this far cheaper than it looks.

**``exec`` is already the answer, and it is already mandatory.**
``toolsets.rst:74-91`` makes ``exec`` spec-mandatory, specifies the contract
(*"MUST execute that command/script in the repo class root, passing through the
same standard environment variables it passes to other tools, and propagate its
exit code"*), and gives ``["exec", "make", "deploy"]`` as its example. Nothing
implements it. Declaring a foreign toolset needs no new BACON concept -- it
needs an existing MUST to become true.

**``nsctl`` already replaces the generated deploy command, so it can honour
``exec`` itself.** ``prepareWorkspace`` returns the literal string ``"bash
src/local/deploy_local.sh"`` and ``node.Script`` -- the ``hmd ... deploy``
command ``hmd-ms-deployment`` generated -- is never executed
(``internal/runner/workspace.go:61-69``). The same seam can run a repo's
declared ``exec`` argv. **The local path therefore requires no change in any
Python package**, which is what makes this a proposal rather than a programme.

**A per-instance image carrier already exists, half-built.**
``hmd_lang_deployment.repo_instance_deployment`` has a ``deployment_image``
attribute
(``hmd-lang-deployment/src/python/hmd_lang_deployment/repo_instance_deployment.py:11``,
``:102-107``). It is written from ``HMD_APP_IMAGE``
(``hmd-ms-deployment/src/python/hmd_ms_deployment/environment_information.py:614``)
and read by nothing: the cloud resolves one image per environment type through
``_get_hmd_app_image``
(``hmd-ms-deployment/src/python/hmd_ms_deployment/deploy_workflow_creator.py:509``),
``nsctl`` uses a single process-wide ``Config.Image``
(``internal/runner/runner.go:316``), and ``msdeploy.DeploymentNode`` has no
image field at all (``internal/msdeploy/entities.go:164-172``). The data model
has been waiting for a reader.

Why projectbuilder cannot be the deploy environment
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

``hmd-img-projectbuilder`` is the right image for a NeuronSphere repo class and
the wrong one for a foreign repo, and not marginally. Its Dockerfile installs
helm, tofu/terraform, node, awscli, opa, java and the
nerdctl/containerd/buildkit set. It contains **no ``kubectl``** -- zero
references in the whole Dockerfile -- and **no ``docker`` CLI**. No kustomize,
no skaffold, no pulumi, no ko.

``hmd-cli-helm`` already documents the consequence of the missing docker CLI, in
code, because it hit it: it guards its own k3s image import with
``if shutil.which("docker") is None`` and prints *"docker CLI not available;
skipping local image import into k3s (images are pulled from the registry
instead)"*
(``hmd-cli-helm/src/python/hmd_cli_helm/hmd_cli_helm.py:99-104``). For a
locally-built image there is no registry that has it, so "pulled from the
registry instead" means ``ImagePullBackOff``.

Adding tools to projectbuilder is not the fix either. The set of tools a
stranger's repo needs is unbounded, and a base image that tries to contain it
becomes a distribution nobody asked for. Their CI image already contains
exactly the right set, by construction -- it is the image in which that command
already works.

Scope
-----

**In scope.** Generating BACON metadata for a repo that has none, through
deterministic commands an agent can drive; detecting how the repo already
deploys and in what image; honouring ``deploy.commands: [["exec", ...]]`` in
``nsctl``; running a deploy node in a repo-declared image under a contract that
assumes nothing but an OCI image; validating the result against both the schema
and the rules that actually predict failure; and emitting an instruction pack
that teaches the user's own AI surface to do the part that needs judgement.

**Out of scope, deliberately.** Relocating any file in the user's repository.
Authoring a ``src/local/deploy_local.sh`` on their behalf -- that mechanism
stays exactly as it is and this proposal does not use it. Bundling, embedding or
calling a language model. An MCP server. Cloud parity, which is referred out by
SPEC012. Making ``hmd build`` able to build a foreign repo: this is a deploy
proposal, and a repo whose CI already builds it needs nothing from ``hmd
build``.

.. spec:: The command group is ``nsctl repoclass``
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC001
    :status: implemented

    BACON authoring shall live under ``nsctl repoclass``, not ``nsctl
    manifest``.

    Three different files in this ecosystem are called "manifest", and ``nsctl``
    reads all three:

    .. list-table::
       :header-rows: 1

       * - Noun
         - File
         - Read by
       * - repo class manifest
         - ``<repo>/meta-data/manifest.{json,toml}``
         - ``internal/repoclass``, ``hmd-lib-manifest``
       * - environment manifest
         - ``$HMD_HOME/environments/<slug>.yaml``
         - ``internal/manifest``
       * - control-plane manifest
         - ``$HMD_HOME/.config/control-plane.yaml``
         - ``internal/manifest``, ``internal/cpext``

    The Go packages resolved this collision already, and the CLI shall agree
    with them: ``internal/repoclass`` owns the BACON one -- its type is
    ``repoclass.Manifest`` (``internal/repoclass/repoclass.go:205-212``) -- and
    ``internal/manifest`` owns the other two. The command tree already
    distinguishes the same two levels, ``nsctl repo add`` declaring an
    *instance* and ``nsctl control-plane repo add`` the same for the other
    scope. ``repoclass`` is the platform's own word for the class:
    ``repo_class_name`` is the key in every BOM entry
    (``internal/bom/bom.go:69``) and in ``add_repo_class_version``.

    ``nsctl manifest init`` would read as "create an environment manifest",
    which is the one thing it does not do -- and ``nsctl repo add``'s own help
    text says in so many words that it *"edits the environment manifest"*
    (``cmd/repo.go:22``). No ``nsctl manifest`` alias shall be offered: an alias
    would reintroduce the ambiguity the name exists to remove.

    Documentation and help text shall use the three unqualified-free phrases
    above. The word "manifest" shall not appear unqualified in any message this
    proposal adds.

.. spec:: A foreign toolset is declared as ``exec`` plus a declared image
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC002
    :status: proposed

    A repo class whose deploy is its own shall declare it as a BACON ``exec``
    command and an image to run it in:

    .. code-block:: json

       {
         "name": "acme-api",
         "description": "The Acme public API",
         "build": { "mechanism": "external",
                    "external": { "system": "github_actions",
                                  "entry_point": ".github/workflows/ci.yml" } },
         "deploy": {
           "commands": [["exec", "make", "deploy"]],
           "image": "ghcr.io/acme/ci:3.2",
           "dependencies": {},
           "default_configuration": {}
         }
       }

    ``deploy.commands`` is the existing key, carrying the existing
    spec-mandatory identifier, with the argument list the spec already
    specifies. ``deploy.image`` is the one addition, and SPEC004 defines it.

    Nothing about this declaration is ``nsctl``-private. That is the point:
    contrast ``src/local/deploy_local.sh``, which works only because ``nsctl``
    substitutes it (``internal/runner/workspace.go:61-69``) and which Python
    ``hmd deploy`` cannot see at all. A manifest written this way is a manifest
    the ordinary deploy path will run on the day SPEC012's referred work lands,
    with no edit.

    ``build.mechanism: "external"`` shall be written whenever the repo's build
    is not a BACON build, because it is the schema's own way of saying so
    (``toolsets.rst:61-68``: a conforming tool set *"MUST NOT invoke that
    phase's commands -- which MAY be absent entirely"*). SPEC008 requires
    ``validate`` to report that this key is currently advisory, since
    ``grep -rn mechanism`` across ``hmd-cli-deploy``, ``hmd-cli-build`` and
    ``hmd-lib-manifest`` finds no reader.

.. spec:: ``nsctl`` honours ``exec`` itself
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC003
    :status: implemented

    ``nsctl`` shall read ``deploy.commands`` -- which it ignores entirely today
    -- and when the entry is ``["exec", ...args]`` shall run that argv as the
    deploy node, in the repo class root, propagating its exit code, per
    ``toolsets.rst:76-82``.

    This is implemented at the seam that already replaces the generated command.
    ``prepareWorkspace`` (``internal/runner/workspace.go:47-79``) returns the
    command a node runs; today it returns either ``Localize(node.Script)`` or
    the literal ``"bash src/local/deploy_local.sh"``. It gains a third answer,
    and ``node.Script`` is discarded for such a node exactly as it already is
    for a ``deploy_local.sh`` one. **No Python package changes for the local
    path.**

    ``repoclass.Manifest`` (``internal/repoclass/repoclass.go:205-212``) gains
    the two fields this needs and no others. It currently types ``name``,
    ``deploy.dependencies`` and ``deploy.default_configuration``, because those
    are what it forwards to ``add_repo_class_version``; it grows
    ``deploy.commands`` and ``deploy.image`` because those are what it will now
    act on. The package doc's rule holds -- read what you use.

    Precedence, stated because a repo may declare both: an ``exec`` entry in
    ``deploy.commands`` outranks ``src/local/deploy_local.sh``. A repo that has
    written down what its deploy command is has said something more specific
    than a conventional filename.

    Exactly one ``exec`` entry per phase is supported. Two is refused by
    ``validate`` (SPEC008) rather than half-run, because the ordering and
    failure semantics of a multi-command foreign deploy are the author's
    business and a guess about them is worse than a refusal.

    **Implemented 2026-09-16.** ``repoclass.Manifest.ExecCommand``
    (``internal/repoclass/repoclass.go``) applies the rule, and refuses one
    more shape: an ``exec`` beside a native tool. ``prepareWorkspace``
    reads the manifest of the tree it is about to mount and returns the
    argv as the node's command (``internal/runner/workspace.go``);
    ``validate`` does not exist yet, so the runner refuses at deploy time
    with the manifest path and the reason. ``deploy.commands`` is typed
    ``[][]any`` rather than ``[][]string`` because real manifests carry
    object arguments in their command lists
    (``["docker", "build", {"is_windows": true}]``), and a stricter reader
    would refuse repos it deploys today.

.. spec:: ``deploy.image``: the deploy environment is the repo's
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC004
    :status: partial

    A repo class may name the OCI image its deploy node runs in. Absent the
    key, the node runs in ``hmd-img-projectbuilder`` exactly as it does today,
    so no existing repo class changes behaviour.

    Two ways to populate it, both supported, because the two populations differ:

    - **A ref**, which for most repos is the image their CI already uses --
      ``ghcr.io/acme/ci:3.2``. Their pipeline is the proof that this image can
      run this command.
    - **A repo-designated Dockerfile**, for a repo whose CI installs its tools
      inline rather than using a prebuilt image. ``nsctl`` builds it on the host
      and tags it deterministically from the repo class name and version. This
      is the only build ``nsctl`` performs, and it builds a *tools* image, never
      the application image.

    ``nsctl`` shall assume nothing about the image's contents. In particular it
    shall not assume ``hmd``, ``bash``, ``python3``, ``helm``, ``kubectl`` or a
    root user. SPEC005 is the contract that makes that true.

    **Partial as of 2026-09-16.** The reference form is implemented:
    ``deploy.image`` is read from the manifest and passed to ``docker run``
    as it is, pulled by the daemon if absent (the acceptance probe ran in
    ``alpine:3.20``). The Dockerfile form -- ``nsctl`` building a tools
    image on the host -- is not built, and neither is the
    ``repo_instance_deployment.deployment_image`` carrier below.

    The eventual cross-path carrier is
    ``repo_instance_deployment.deployment_image``, which already exists with a
    writer and no reader (see Motivation). Wiring it requires
    ``apply_changeset`` to accept it, ``generate_local_deployment`` to emit it,
    and a field on ``msdeploy.DeploymentNode``
    (``internal/msdeploy/entities.go:164-172``). That is a change to
    ``hmd-ms-deployment`` and belongs with SPEC012's referred work; until then
    ``nsctl`` reads the manifest key directly, which needs nobody's agreement.

.. spec:: The foreign-node contract assumes an OCI image and nothing else
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC005
    :status: implemented

    This is the specification that removes ``hmd-img-projectbuilder`` from a
    foreign repo's path, and it is best written as what
    ``internal/runner/runner.go``'s ``dockerArgs`` (``:243-317``) must stop
    assuming.

    **Dropped for a foreign node:**

    - **The entrypoint override.** ``--entrypoint bash``
      (``internal/runner/runner.go:290``) is not set; the command is passed as
      argv (``docker run --rm <image> make deploy``). A foreign image may have
      no ``bash``, and may have an entrypoint of its own that matters.
    - **The mounted script.** The command is not written to a temp file and
      mounted at ``/tmp/hmd-deploy-node.sh``
      (``internal/runner/runner.go:315-316``). That indirection exists because
      the *generated* script inlines a node's resolved dependencies and can
      exceed ``ARG_MAX``; an ``exec`` argv is a handful of words and carries its
      configuration in the environment instead.
    - **``/root`` paths.** The kubeconfig is not mounted at
      ``/root/.kube/config`` (``internal/runner/runner.go:305-306``). It is
      mounted at a neutral path and named by ``KUBECONFIG``, which is the
      variable every Kubernetes tool reads, so the image's user need not be
      root.
    - **Everything present only to satisfy ``hmd-cli-*``.**
      ``HMD_HOME=/root/hmd`` (``:258``), and the dummy
      ``DOCKER_USERNAME``/``DOCKER_PASSWORD`` that exist because
      ``hmd cdktf deploy`` unconditionally resolves registry credentials
      (``:262-264``), are not set. There is no ``hmd`` in this container to
      assert on them.

    **Injected, and this is the whole contract:**

    - ``/workspace`` -- the repo class root, mounted read-write, and the working
      directory.
    - The platform Docker network, and the two labels ``env purge`` uses to find
      a container an interrupted deploy left behind
      (``internal/runner/runner.go:296-297``).
    - ``AWS_ENDPOINT_URL``, ``AWS_ACCESS_KEY_ID``, ``AWS_SECRET_ACCESS_KEY``,
      ``AWS_DEFAULT_REGION`` -- pointed at Floci, with the access key acting as
      the account selector.
    - ``KUBECONFIG``, when the environment has a cluster.
    - ``HMD_INSTANCE_NAME``, ``HMD_REPO_NAME``, ``HMD_REPO_VERSION``,
      ``HMD_INSTANCE_CONFIG`` (the resolved configuration as JSON), ``HMD_DID``,
      ``HMD_ENVIRONMENT=local``, ``HMD_CUSTOMER_CODE``,
      ``HMD_LOCAL_K3S_CLUSTER_NAME``.
    - ``/var/run/docker.sock``, so a toolset that builds images can. Note that
      the daemon is a *sibling*: every bind mount it is asked for resolves on
      the host, not inside this container.

    ``HMD_REPO_VERSION`` is new. ``dockerArgs`` sets fifteen ``HMD_*`` variables
    and not that one, although ``node.Version`` is in hand at
    ``internal/runner/runner.go:229`` and every ``hmd`` invocation defaults
    ``--repo-version`` from it. Without it a foreign command has no version to
    tag with, and reading ``meta-data/VERSION`` itself would be a second answer
    to a question the resolver already answers.

    Produced Resources are unchanged: the runner collects every
    ``meta-data/resources_output/*.json`` from the workspace after the node
    exits and submits it, which requires no tool in the image at all.

    **Withdrawn 2026-09-16 -- the same-ChangeSet caveat.** This section
    originally said that ``hmd deploy`` resolves same-changeset
    ``hmd_resource_ref`` pointers into ``hmd_resources`` immediately before
    dispatching its tools
    (``hmd-cli-deploy/src/python/hmd_cli_deploy/controller.py:446-508``),
    that replacing the command replaced that step, and that a foreign node
    must therefore not depend on an instance deployed in the same ChangeSet.
    It also assumed ``HMD_INSTANCE_CONFIG`` would carry the resolved
    configuration at all. Neither held:

    - ``generate_local_deployment`` emits no ``instance_configuration``
      (``hmd-ms-deployment/src/python/hmd_ms_deployment/local_deployment.py:137-144``),
      so ``dockerArgs`` marshalled ``{}`` for every service-generated node.
      The resolved configuration -- the merged defaults, the instance's
      values and every dependency role with its NERD0006 ``hmd_resources``
      -- exists only inside the generated script, as the
      ``--config-file STDIN <<'EOF'`` heredoc ``hmd deploy`` reads
      (``deploy_base.py:322-360``). A node that runs the script gets it for
      free; a foreign node does not run the script.
    - So ``nsctl`` lifts the configuration out of the script
      (``runner.ExtractConfig``) and resolves every ``hmd_resource_ref`` in
      it through ``get_deployment_resources`` exactly as ``hmd deploy`` does
      -- every role in the tree, each deployment fetched once, outputs
      appended to ``hmd_resources``, best effort
      (``runner.ResolveResourceRefs``, ``internal/runner/config.go``). The
      caveat is withdrawn: a foreign node may depend on anything, and
      SPEC011's corresponding warning item is moot. The durable fix is for
      ``local_deployment.py`` to emit ``instance_configuration`` per node,
      at which point the extraction becomes a fallback.

    Two more corrections from the live acceptance (2026-09-16, a repo class
    with ``[["exec", "sh", "-c", ...]]`` in ``alpine:3.20`` depending on the
    substrate's ``database.neuronsphere.io/postgres``):

    - ``meta-data/resources_output/`` did not exist. ``hmd-cli-helm`` creates
      it on the native path, and "requires no tool in the image at all" was
      true only after the runner started creating the directory before the
      node runs. It does now.
    - ``HMD_REPO_VERSION`` is the version the resolver registered, which for
      a working tree is ``meta-data/VERSION`` verbatim (``0.1``), not the
      three-part form the graph's ``version`` field reports elsewhere.

    The contract is otherwise as written: ``docs/nsctl.rst`` "Deploying a
    repo with its own toolset" documents it, ``foreignEnv`` builds it, and
    ``TestForeignEnvIsExactlySPEC005`` pins the injected set against the
    map the command line is built from, with and without a cluster.

    This contract shall be documented in ``docs/``, not only here. It is the
    product: a user whose ``make deploy`` expects a git identity, a registry
    credential or a ``kubectl`` context gets a failure ``nsctl`` cannot explain,
    and the only cure is that the contract was written down where they looked.

.. spec:: The manifest store: one writer, two axes
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC006
    :status: partial

    .. note::

        Partial as of 2026-09-17. The store is ``internal/bacon``: an ordered
        document, read through the three-tier precedence below, written back
        to the file it was read from. Writes are implemented for
        ``meta-data/manifest.json`` only. A TOML tier is read -- ``describe``
        and ``validate`` work on it -- and every write verb refuses it with
        the file named, because a surgical key-path TOML edit that leaves
        untouched text byte-identical has no library behind it in this module
        and was judged the riskiest code in the slice for a format none of the
        motivating repositories use. ``init --format toml`` and ``--at root``
        are refused as not implemented for the same reason.

    Reading and writing a repo class manifest shall go through one store,
    parameterised by *location* and *format*, resolved by a single precedence
    list: ``meta-data/manifest.toml``, then ``meta-data/manifest.json``, then
    repo-root ``neuronsphere.toml``.

    The first two are ``hmd_lib_manifest.read_manifest``'s own order, and being
    second to TOML is not incidental -- ``hmd-lib-manifest`` prefers TOML when
    both exist. The third is the direction the platform intends: a single
    BACON-compatible ``neuronsphere.toml`` at the repo root, and nothing under
    ``meta-data/`` at all. Because BACON already supports TOML, that is a change
    of *location*, not of format, and the day it becomes the default one
    ordered list reorders and ``init``'s default changes. Nothing else moves.

    A write returns to the file it was read from, in the format it was read in
    -- the behaviour ``write_manifest`` gets from remembering its source. No
    command shall silently migrate a repo between formats or locations.

    The in-memory document shall be an ordered map, not a Go struct, so that
    every key ``nsctl`` does not model survives a rewrite. This is NERD008
    SPEC004's rule for ``tokens.yaml`` applied to the other shared file, and for
    the same reason: forty Python repositories write this format, and a Go
    front end that drops what it does not understand is a front end nobody can
    afford to run twice.

    Two constraints stated here rather than discovered later:

    - ``nsctl`` cannot preserve TOML comments the way ``tomlkit`` does. TOML
      writes shall therefore be surgical key-path edits that leave untouched
      text byte-identical, and ``validate`` shall not rewrite a file at all.
    - ``hmd_lib_manifest``'s path list does not look at the repo root. A repo
      carrying *only* ``neuronsphere.toml`` cannot be read by ``hmd deploy``
      until that changes, so the root location is the lowest tier today and the
      Python-side change is a prerequisite of the flip, not of this proposal.

.. spec:: ``init``, and the minimum viable repo class
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC007
    :status: implemented

    ``nsctl repoclass init <name>`` shall write ``name``, ``description`` and
    ``build: {}`` -- the three members the BACON schema requires -- and
    ``meta-data/VERSION`` containing ``0.1`` when there is none.

    ``VERSION`` is not optional in practice. Without it every tier of
    ``ResolveVersion`` falls through to the sentinel
    (``internal/repoclass/repoclass.go:20-21``, ``:188-192``), so the class
    registers under ``0.1.0`` -- a version it never declared, which
    ``nsctl repo list`` and the deployment graph then report as fact. A missing
    version is a question; ``0.1.0`` is a wrong answer.

    ``init`` shall refuse when a manifest already exists, naming ``describe``
    and ``validate`` as what the caller probably wanted. The absent-versus-wrong
    distinction NERD008 SPEC002 draws about ``nsctl.toml`` applies with more
    force here, because this file is very often the user's own work.

    No verb under ``nsctl repoclass`` shall call ``Options.RequireHome``
    (``cmd/root.go:40-48``). A user pointing this binary at their repository
    before they have installed or started anything is the entire motion, and
    ``test/nsctl_cli.robot`` already runs with ``HMD_HOME`` empty, so the
    requirement is directly testable.

.. spec:: The write verbs, and the read verb the loop turns on
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC008
    :status: implemented

    .. note::

        Implemented as of 2026-09-17 (``cmd/repoclass*.go``,
        ``internal/bacon``), with these deltas from the grammar below:
        ``detect`` and ``agent init`` are not built (SPEC009/010, SPEC015);
        ``init --format`` and ``--at`` accept only ``json`` and ``meta-data``
        and refuse the rest as not implemented (SPEC006); ``deploy set-image
        --from-dockerfile`` is not built (SPEC004). Two verbs the grammar does
        not list were needed to author a foreign-toolset class entirely
        through the verbs: ``build set-mechanism <tool_set|external>`` and
        ``test set-command exec <argv...>``; ``deploy`` also takes
        ``set-mechanism``, ``add-command`` and ``remove-command``. Every
        ``add-*`` is keyed and idempotent; the fourteen-verb authoring of the
        motivating product manifest reproduces the hand-written file
        byte-for-byte (``TestVerbsReproduceTheAcmeManifest``), and
        ``hmd_lib_manifest.read_manifest`` parses what the verbs write.
        ``describe --json`` is the Python's document plus each dependency's
        ``resource`` block and the ``test`` section.

    ``nsctl repoclass`` shall port the ``hmd manifest`` verb set and ``hmd
    describe`` to Go with identical semantics, under a persistent ``--path``
    defaulting to ``.``:

    .. code-block:: text

       nsctl repoclass [--path DIR]
         init <name> [--description T] [--format json|toml] [--at meta-data|root]
         describe [--json]
         detect [--json] [--apply] [--yes]
         validate [--strict] [--json]
         deploy set-command exec <argv...>
         deploy set-image <ref> | --from-dockerfile <path>
         deploy add-dependency <role> [--repo-class-name X] [--required|--optional]
                [--version-spec S] [--resource-namespace NS]
                [--resource-definition-name N] [--resource-version V] [--tag K=V]
         deploy remove-dependency <role>
         deploy add-resource <name> ... | deploy remove-resource <name>
         deploy set-config <dotted.key> <value> | deploy unset-config <dotted.key>
         build add-command <tool> [args...] | build remove-command <tool>
         discovery set-summary | add-entry-point | add-capability | add-related-doc
         agent init [--flavor claude|agents|both] [--force]

    Every ``add-*`` is key-addressed and idempotent: re-adding a dependency
    replaces the entry with the same role rather than duplicating it, so an
    agent that re-runs a command has not broken anything. ``required`` is
    serialised as the **string** ``"true"``/``"false"`` the schema's enum
    demands, and ``--required`` remains the default, because two front ends with
    the same verb and opposite defaults is worse than the footgun it would
    close. ``validate`` closes it instead.

    Verbs nest properly. The Python spells these ``hmd manifest
    build-add-command``, not ``build add-command``, because Cement derives the
    subcommand from the method name and rewrites ``_`` to ``-``
    (``hmd-cli-app/src/python/hmd/controllers/base.py:97``). Cobra has no such
    constraint. SPEC011 requires the emitted instruction pack to be generated
    from the live command tree partly because of this divergence.

    ``describe --json`` shall emit the same normalized summary ``hmd describe``
    does. It is the document SPEC011 tells an agent to read *instead of* the
    manifest file: an agent that reads the raw file re-derives what one command
    already answers, and an agent that verifies a write by re-reading the file
    cannot tell success from a no-op.

    Three verbs have no Python equivalent and exist because detection needs
    them: ``deploy set-command``, ``deploy set-image`` and ``deploy
    set-config``/``unset-config``, the last writing a dotted key path into
    ``deploy.default_configuration`` with JSON-typed values -- the typing rule
    ``parseConfig`` already applies to ``nsctl repo add --config``.

    Writes print one line naming the file and the key they changed and exit
    ``0``. Reads print a table, or a JSON document under ``--json``: a new
    convention in this CLI, justified by the primary consumer being an agent
    parsing stdout. Exit codes are ``internal/nserr``'s.

.. spec:: ``detect``: find how they already deploy, and in what
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC009
    :status: proposed

    ``nsctl repoclass detect`` shall inspect the repository and print a
    *mechanism classification* -- what deploys this repo, what image it runs in,
    what could not be decided, and what will not be guessed -- with the file and
    line that produced each conclusion. With ``--apply`` it writes what is
    unambiguous, through SPEC008's verbs rather than by serialising a document,
    so one code path writes manifests. Without ``--apply`` it writes nothing.

    The questions are asked in this order, because the later ones are only
    interesting when the earlier ones fail: *is there an author-maintained
    deploy entry point?* then *what image does their pipeline run it in?* then
    *what deployable artifacts exist?*

    .. list-table::
       :header-rows: 1

       * - Signal
         - Conclusion
       * - ``.github/workflows/*.y?ml`` ``run:`` steps containing
           ``helm upgrade``, ``kubectl apply``, ``kustomize build``,
           ``skaffold run``, ``terraform|tofu apply``, ``pulumi up``,
           ``docker build|push``, ``argocd app sync``
         - the authoritative statement of how this repo deploys; also recorded
           as ``build.external.entry_point``
       * - that job's ``container:``, or the image it builds and pushes
         - ``deploy.image``
       * - ``Makefile`` target ``deploy``/``release``/``publish``,
           ``Taskfile.yml``, ``justfile``
         - the best ``exec`` candidate: an entry point the author maintains and
           tests
       * - ``skaffold.yaml`` -- ``deploy.helm.releases[].chartPath``,
           ``manifests.rawYaml``, ``build.artifacts[].image``
         - the clearest machine-readable deploy description available; take the
           paths directly
       * - ``Tiltfile``, ``kustomization.yaml``, ``k8s/``/``manifests/`` raw
           YAML, ``chart/Chart.yaml``, root ``Dockerfile`` + ``EXPOSE``,
           ``docker-compose.yml``, ``*.tf``
         - evidence, and the argv to propose
       * - ``src/helm``/``src/docker``/``src/cdktf`` present
         - this repo is already NeuronSphere-shaped; propose the native tool,
           not ``exec``
       * - ``Procfile``, ``fly.toml``, ``render.yaml``, ``vercel.json``,
           ``app.yaml``
         - **report and stop.** There is no local equivalent of a PaaS deploy;
           a chart or a command has to be authored, and saying so is the honest
           answer
       * - directory name, git remote basename, ``package.json``/``Chart.yaml``
           name
         - the repo class name, slugified, checked against ``manifest.Reserved``
           and the bundled set. The ``hmd-`` prefix is **not** prepended: this
           is the user's repo
       * - ``README`` first sentence, ``package.json``/``pyproject.toml``
           description
         - a provisional ``description``, flagged for a human to improve
       * - ``meta-data/VERSION`` absent
         - write ``0.1`` (SPEC007)
       * - ``meta-data/manifest.*`` present
         - already a repo class; ``init`` refuses, ``validate`` is named

.. spec:: What detection refuses to infer
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC010
    :status: proposed

    ``detect --apply`` shall write **no ``deploy.dependencies`` entry, no
    ``deploy.resources`` entry, and no ``meta-data/resources/*.yaml``** -- not a
    provisional one, not a commented one. This is the boundary between the
    deterministic layer and the judgement layer, and it is drawn here because
    the cost of crossing it is asymmetric.

    **A dependency role cannot be derived from a repo's files.** The vocabulary
    lives in this environment's catalogue, not in the user's source. A wrong
    role marked ``required: "true"`` does not fail that node -- it fails the
    *entire ChangeSet* server-side, with a message naming only the role
    (``internal/bom/bom.go:96-105``). A newcomer's first ``env apply`` returning
    ``required role, X, not provided`` for a role they never chose is the worst
    available outcome, and it is produced by guessing helpfully.

    **A resource declaration needs a namespace, a definition name and a version
    from a catalogue the repo has never referenced**
    (``internal/repoclass/resources.go:12-43``), and a malformed one is silently
    skipped rather than reported.

    Also refused: which of several ``EXPOSE``\ d ports, charts or compose
    services is *the* one; which keys in a compose ``environment:`` block the
    platform should own and which are the app's own constants; ``description``
    beyond a provisional first sentence; anything under ``discovery``; and
    whether this repo should ever deploy anywhere but locally.

    A compose file's ``environment:`` and ``depends_on:`` blocks shall be
    reported as *evidence for the agent* and never written. SPEC011's pack is
    what crosses this boundary, and its rule is that a required dependency is
    added only after the user has named the instance that will fill it.

.. spec:: ``validate``: the schema, the rules, and three severities
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC011
    :status: implemented

    .. note::

        Implemented as of 2026-09-17. The structural pass is hand-coded from
        ``hmd-docs-bacon/docs/reference/schema.rst`` (``internal/bacon/validate.go``)
        rather than run against a schema file, because SPEC013's file does not
        exist yet; the two must be reconciled when it does. The withdrawn
        "any required dependency" warning is not emitted. Findings print as
        ``<severity>: <key path> <message>`` and the closing line is
        ``validate: ok|failed -- N errors, N warnings, N notes``; ``--json``
        emits the counts and the findings.

    ``nsctl repoclass validate`` shall run a structural pass against the BACON
    JSON Schema (SPEC013) and a semantic pass against the rules that actually
    predict failure, reporting findings at three severities.

    **Error -- this will fail a deploy.** A ``deploy`` section with neither
    ``commands`` nor a resolvable command, which is a bare ``KeyError`` at
    ``hmd-cli-deploy/src/python/hmd_cli_deploy/controller.py:293``. An ``exec``
    entry with empty argv. More than one ``exec`` entry in a phase (SPEC003). A
    native tool named without the source directory its CLI hard-codes --
    ``helm`` without ``src/helm/Chart.yaml``
    (``hmd_cli_helm.py:179``, ``:240``), ``docker`` without
    ``src/docker/Dockerfile`` (``hmd_cli_docker.py:261``), ``cdktf`` without
    ``src/cdktf`` (``hmd_cli_cdktf.py:96``). A dependency with ``required``
    truthy and neither ``repo_class_name`` nor ``resource``, which is
    unresolvable by construction.

    **Warning -- this will surprise you.** Any ``required`` dependency at all,
    because the environment manifest must wire it or the ChangeSet fails
    (SPEC010). ``required`` written as a JSON boolean where the schema's enum is
    the strings. *(Withdrawn with SPEC005's caveat: a same-ChangeSet dependency is
    resolved by ``nsctl`` itself, so there is nothing to warn about.)*
    ``meta-data/VERSION`` absent or not ``MAJOR.MINOR``. A
    ``meta-data/resources/*.yaml`` missing ``resource_namespace`` or
    ``resource_definition_name``, which ``Produces`` silently skips. A ``name``
    colliding with a bundled repo class or a reserved instance name.

    **Note -- this is inert.** ``deploy.mechanism`` is read by no installed tool
    set. ``exec`` is not implemented by ``hmd-cli-deploy``, so this repo deploys
    under ``nsctl`` and fails under ``hmd deploy`` with
    ``CLI command, exec, not found.`` That note shall name SPEC012's referred
    work, and it shall disappear on the day that work lands -- a failing test is
    how anyone finds out.

    **Advisory by default.** ``--strict`` promotes warnings to errors, and
    nothing wires ``validate`` into ``env apply`` at error severity.
    Strict-by-default is not available: thirteen manifests in this workspace
    already violate the schema's own ``required: ["name","description","build"]``
    -- including ``hmd-inf-eks-cluster``, which has no ``description`` and is
    one of the ten repos ``make generate`` embeds, so it is deployed by every
    environment this binary starts. Three others have neither ``name`` nor
    ``description``, and ``hmd-img-jupyter-server``'s entire manifest is
    ``{"build":{"commands":[["docker"]]}}``. A validator that refused those
    would refuse to bring up the substrate.

    ``validate`` shall never rewrite the file it checks (SPEC006).

.. spec:: Cloud parity is referred out, and costs this proposal nothing
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC012
    :status: proposed

    An adopted repo deploys locally under ``nsctl`` and does not deploy through
    Python ``hmd deploy``. This proposal shall say so plainly rather than imply
    parity -- and shall leave nothing to redo when parity arrives.

    Two things are needed, both in other repositories:

    - **``exec`` conformance in ``hmd-cli-deploy``.**
      ``_run_deploy_commands`` binds ``command_name = deploy_command[0]`` and
      discards the rest
      (``hmd-cli-deploy/src/python/hmd_cli_deploy/controller.py:596-606``);
      ``_execute_deploy_command`` resolves that name to a Cement controller and
      calls ``.deploy()`` with no arguments (``:515-521``); ``_find_controller``
      raises on an unknown name (``:195-203``). The build path's ``exec`` is
      also non-conformant: ``Tool`` is a fixed three-tuple
      (``hmd-cli-build/src/python/hmd_cli_build/controller.py:39``, built at
      ``:221-224``), so ``["exec","make","deploy"]`` becomes
      ``sub_command="make"``, ``arguments="deploy"``, and the exec branch runs
      ``subprocess.run([tool.sub_command], shell=True)`` (``:250-263``) -- it
      runs ``make`` and silently drops ``deploy``. Only
      ``["exec", "make deploy"]`` works, by accident of ``shell=True``.
    - **A reader for the deploy image**, per SPEC004.

    That work changes the behaviour of every build and deploy everywhere,
    including cloud CI, so it belongs in its own proposal in ``hmd-cli-deploy``
    rather than in a local-platform NERD -- the line NERD005 SPEC006 already
    draws about ``hmd build``'s resolution order. It is also worth more than
    this proposal: ``exec`` conformance makes every foreign repo deployable
    through the ordinary path, and subordinating it to a section of the feature
    that happened to notice it would undersell it.

    What this proposal owes it is cheap and shall be paid: the manifest SPEC002
    writes is already the conformant declaration, so on the day ``exec`` lands
    an adopted repo deploys through Python with **no manifest edit**.

.. spec:: Where the schema artifact comes from
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC013
    :status: proposed

    There is no BACON JSON Schema file anywhere in the ecosystem. The schema
    exists only as an RST ``code-block`` inside
    ``hmd-docs-bacon/docs/reference/schema.rst``, carrying
    ``"$id": "https://neuronsphere.io/bacon/manifest.schema.json"``, which is
    unpublished -- while the same document's usage section tells readers to
    point their editor at it. ``hmd-lib-manifest`` performs no validation at
    all.

    **The artifact shall be authored in ``hmd-docs-bacon``, not here.**
    ``hmd-docs-bacon/meta-data/schema/manifest.schema.json`` becomes the source
    of truth and ``schema.rst``'s block becomes a ``literalinclude`` of it, so
    the document cannot drift from the thing tools check. ``nsctl`` obtains it
    the way it already obtains ten repo trees: published as a librarian content
    item of type ``schema``, staged by ``make generate``, and ``//go:embed``\ ed
    (``internal/bundled/bundled.go:26``). The embedded copy carries the version
    it came from and ``validate`` prints it, so a finding can be attributed to a
    schema revision. Offline is the default case and needs no special path: the
    embedded copy is the only one consulted at runtime.

    Two things stated plainly. The ``$id`` resolves to nothing, which is
    harmless for offline validation because nothing dereferences it and
    misleading in the ``$schema`` key that document tells readers to paste. And
    the schema is permissive -- ``additionalProperties: true`` at the root,
    nothing required inside ``build`` or ``deploy`` -- so the structural pass
    catches little that matters. It exists so that one artifact, two CLIs and an
    editor can agree; SPEC011's semantic rules are where the value is.

.. spec:: Images the toolset builds must still reach the cluster
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC014
    :status: proposed

    A foreign image containing a docker CLI can build against the mounted
    socket, but the result lands in the **host** daemon, not in the k3s node's
    containerd, so a pod referencing it fails with ``ImagePullBackOff``. This is
    not hypothetical: ``hmd-cli-helm`` performs exactly this import and skips it
    with a printed warning when it cannot
    (``hmd-cli-helm/src/python/hmd_cli_helm/hmd_cli_helm.py:99-104``).

    ``nsctl`` shall stage named images into the cluster from the host, before or
    after the node as the toolset requires: ``docker save``, ``docker cp``, then
    ``docker exec <k3s container> ctr -n k8s.io images import``, expressed as
    discrete calls through ``internal/container``'s Docker surface rather than a
    shell pipe, so it is testable, and addressing the container name
    ``internal/floci`` already computes.

    A generated deploy that omits this ships a command whose pods never start,
    which is why it is a specification and not an implementation detail.

    **Open alternative**, recorded rather than chosen: a local image registry
    the k3s node pulls from. That is the cleaner answer -- it removes the
    host-side step entirely and matches what a user's toolset already expects to
    push to -- and it is a larger piece of work with an obvious relationship to
    NERD006.

.. spec:: ``agent init``: the emitted instruction pack
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC015
    :status: proposed

    ``nsctl repoclass agent init`` shall write an instruction pack into the
    *target* repository -- ``AGENTS.md`` at the root, and/or
    ``.claude/skills/neuronsphere-adopt/SKILL.md``, from one template selected
    by ``--flavor`` -- following the frontmatter shape the ``hmd-cli-*``
    ``src/skills/`` and ``src/agents/`` packs already use.

    This is how ``nsctl`` ships no model and still gets the judgement work done.
    The pack instructs an agent to: run ``detect --json`` first and read its
    evidence rather than re-deriving it; run ``describe --json`` before and after
    every edit; apply changes only through the verbs, never by writing the
    manifest file; and -- the rule that matters -- **confirm with the human
    before writing any dependency, and never write a required one unprompted**,
    for SPEC010's reason.

    **The pack shall be generated from the live cobra command tree at emit
    time**, never from a committed table. The cautionary tale is in this
    ecosystem already: ``hmd-cli-manifest/src/skills/author-manifest/SKILL.md``
    documents ``hmd manifest build add-command``, with a space, in every row of
    its command table, while Cement renders the real verb as
    ``build-add-command``
    (``hmd-cli-app/src/python/hmd/controllers/base.py:97``). An instruction pack
    that hallucinates verbs is worse than none, because the agent runs them
    confidently and reads the failure as its own mistake.

    Re-emitting into a repository that already has an ``AGENTS.md`` shall
    rewrite only the region between ``<!-- nsctl:begin -->`` and
    ``<!-- nsctl:end -->``. That file is more likely to be the user's than ours.

.. spec:: What this is not
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC016
    :status: proposed

    **Not an MCP server.** The agent shells out to a binary it already has. A
    server adds a process, a transport and a lifecycle to a problem whose whole
    content is "write these files correctly".

    **Not a language model in ``nsctl``.** No model, no API key, no prompt, and
    no network call to one. Every judgement the binary cannot make
    deterministically it declines and names (SPEC010).

    **Not a relocation tool.** Nothing moves a file in the user's repository.

    **Not a shell generator.** No ``deploy_local.sh`` is authored on anyone's
    behalf. That mechanism (``internal/runner/workspace.go:61-69``) stays
    exactly as it is for the NeuronSphere repos that use it, and this proposal
    does not use it.

    **Not a claim of cloud parity.** See SPEC012.

    **Not a second manifest editor.** ``hmd manifest`` remains; both front ends
    write the same file; NERD002 SPEC012's coexistence rule applies unchanged.

    **Not a plugin system.** ``src/local/nsplugin.json`` stays as deprecated as
    ``internal/repoclass``'s package doc already says it is. An extension runs
    because a manifest names it and for no other reason.

.. spec:: Verification
    :id: HMD_CLI_NEURONSPHERE_NERD009_SPEC017
    :status: proposed

    Nothing in this proposal's test suite may require Docker, a control plane or
    an ``HMD_HOME``, because the population it serves has none of the three at
    the moment they run it. Everything runs under ``make check`` and
    ``make test-cli``, with one named exception at the end.

    - **``detect`` against table-driven fixtures** built in ``t.TempDir()``: a
      ``make deploy`` repo, a skaffold repo, a kustomize repo, a chart-at-root
      repo, a Dockerfile-only repo, a workflow-only repo, a Procfile repo, and
      an already-NeuronSphere-shaped repo. Each asserts the classification, the
      detected image, **and the refusals**. The refusals are the assertions most
      likely to rot, and SPEC010 is only real as an executable assertion.
    - **A golden round trip through every write verb** leaving a file
      ``hmd_lib_manifest.read_manifest`` still parses. That is the contract with
      forty Python repositories and it should fail loudly. Additionally: a TOML
      manifest's comments and untouched lines byte-identical after an edit, TOML
      staying TOML and JSON staying JSON, and unknown top-level and unknown
      ``deploy.*`` keys surviving a rewrite.
    - **The foreign-node invocation asserted against a fake Docker runner**: no
      ``--entrypoint``, no mounted script, no ``/root`` path, no ``HMD_HOME``,
      no dummy registry credentials, and the injected set exactly SPEC005's
      list -- asserted against the same map ``dockerArgs`` builds, not a copy of
      it, so adding a variable to one and not the other fails.
    - **``validate`` asserting each severity** on a fixture built to trip it;
      ``validate`` on the real ``hmd-inf-eks-cluster`` and
      ``hmd-img-jupyter-server`` manifests producing warnings and exit ``0``,
      which is the regression that proves advisory-by-default was not quietly
      reverted; and the "``exec`` is unimplemented by ``hmd-cli-deploy``" note
      being emitted, so that it fails when SPEC012's work lands.
    - **Every ``nsctl`` command string in the emitted pack resolving against the
      live cobra tree**, asserted by walking cobra rather than against a
      fixture. This is the test that would have caught ``author-manifest``'s
      twelve wrong rows.
    - **``test/nsctl_cli.robot`` additions** -- ``repoclass --help``, ``init``,
      ``describe``, ``detect``, ``validate``, ``agent init`` -- all succeeding
      against a scratch directory **with ``HMD_HOME`` empty**, and ``init``
      refusing on an existing manifest with exit ``2`` without touching the
      file.
    - **Everything under ``t.Parallel()``**, which is why no package here reads
      ``os.Getenv``: configuration arrives as an ``hmdenv.Lookup`` and a path
      argument, per NERD002 SPEC004.
    - **Named as outside ``make check``**: one live acceptance. A repository with
      a root ``Dockerfile``, a ``chart/``, a ``make deploy`` and a CI image is
      adopted, deployed, and shows ``Running`` in k3s -- with no file in it
      moved and no shell script written into it. Everything above tests a file
      writer; only this proves the file writer wrote something that deploys.
