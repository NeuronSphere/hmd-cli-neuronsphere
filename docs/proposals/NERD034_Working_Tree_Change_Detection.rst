.. NERD034 Working Tree Change Detection

NERD034 Working Tree Change Detection
=====================================

.. req:: An edit to a working tree reaches the environment on the next apply
    :id: HMD_CLI_NEURONSPHERE_NERD034
    :status: proposed

    When a RepoClass deploys from a developer's working tree, ``nsctl env
    apply`` shall deploy what that tree says *now*. That covers an edited
    chart, an edited CDKTF stack or local overlay, and an edited
    ``default_configuration`` in ``meta-data/manifest.json``. It shall do so
    without ``--force-full-redeploy`` and without a ``meta-data/VERSION``
    bump. An unchanged tree shall still be left alone.

Motivation
----------

``NERD005`` SPEC "The resolution tier, and precedence" says a working tree
wins when the user asked for it, "because the entire purpose of
``$HMD_REPO_HOME`` is to run uncommitted changes". Two gates undo that
promise after the first apply.

**The catalog keeps the first default.** ``Seeder.RegisterCatalog`` registers
each RepoClassVersion with ``add_repo_class_version``. The deployment service
refuses a version it already has ("already has version"), and nsctl treats
that refusal as success. The edited ``default_configuration`` is dropped
without a word. ``get_deployment_config`` then merges the *stored* default
into every deploy, so the stale value is what reaches the chart. Template
edits do land, because the runner mounts the live tree. That makes the
manifest default the one part of the tree that silently lags.

**The plan never looks at the tree.** ``reconcile.Compute`` skips an instance
that the graph calls ``DEPLOYED`` and whose ``EntryHash`` matches the last
apply's snapshot. ``EntryHash`` covers only the environment manifest's
declaration: instance name, class, version, instance configuration and
dependencies. Nothing about the tree is in it, so a chart edit reads as
"everything declared is already deployed and current". The only way out
today is ``--force-full-redeploy``, which redeploys the whole environment.

Testing the ClickHouse / OpenTelemetry collector architecture hit both
gates. The environment files ended up carrying workarounds: defaults copied
into ``instance_configuration``, and a habit of forcing full redeploys.

Relationship to editable mode
-----------------------------

A proposed "editable" mode (vault task ``nsctl-editable-repoclass-mode``)
would let a running instance execute the checkout's *source* with no
redeploy. That proposal leaves the CDKTF overlay, the manifest and chart
values to ``nsctl env apply``. This NERD is what makes that assumption true.
It is the config layer underneath editable mode. Editable mode itself is out
of scope here.

.. spec:: An existing version's catalog metadata follows its tree
    :id: HMD_CLI_NEURONSPHERE_NERD034_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD034
    :status: proposed

    ``RegisterCatalog`` keeps calling ``add_repo_class_version`` first. When
    the service answers "already exists", nsctl no longer stops there:

    1. It finds the ``hmd_lang_deployment.repo_class`` by
       ``repo_class_name``. It then finds the
       ``hmd_lang_deployment.repo_class_version`` reached from it through
       ``repo_class_has_repo_class_version`` whose ``version`` matches.
    2. It compares the stored ``default_configuration`` (and ``discovery``,
       when the tree supplied one) with what the tree says now, as
       normalised JSON.
    3. If they differ, it updates that row through the ms-base CRUD
       endpoint: ``PUT /api/hmd_lang_deployment.repo_class_version``
       carrying the row's ``identifier``, which ms-base treats as an update.
       It says so in one line: "updated <class> <version>'s default
       configuration from <tree>". If they agree, it writes nothing.

    It does this only when the default was read from a tree. When no tree
    was found, ``RegisterCatalog`` falls back to registering the instance's
    own ``instance_configuration`` as the class default. That fallback is
    not a statement about the class and shall never overwrite a stored
    default.

    No new service operation is needed, and nothing changes for a cloud
    environment: published versions are immutable artifacts whose manifest
    does not change under a fixed version.

    A failed update is a warning, not a failed apply. The version is
    registered, and the old default is what the environment already ran on.

.. spec:: A working tree's digest is part of what an apply recorded
    :id: HMD_CLI_NEURONSPHERE_NERD034_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD034
    :status: proposed

    **What is digested.** For an instance whose deploy mounts a developer's
    tree, nsctl computes a *tree digest*. A developer's tree is the
    runner's own choice of directory with ``shared == false``: an explicit
    source path, an ``=local`` pin, ``PREFER_LOCAL``, or the any-checkout
    tier. Bundled and artifact trees are versioned caches and get no digest.

    The digest is a SHA-256 over sorted relative paths and file contents of
    what a deploy reads:

    - ``meta-data/``, except ``meta-data/resources_output/``, which a deploy
      writes;
    - ``src/local/``;
    - ``src/<tool>/`` for each tool named in the manifest's
      ``deploy.commands``, or all of ``src/`` when the manifest names none
      that nsctl can read.

    It skips build and cache output: dot-directories, ``__pycache__``,
    ``node_modules``, ``imports``, ``cdktf.out``, ``.terraform``,
    ``build``, ``dist``, ``target``, ``*.egg-info``, ``*.pyc`` and
    ``*.log``.

    **Where it lives.** ``SnapshotEntry`` gains ``tree_digest``, next to
    ``k8s_release``. ``EntryHash`` is unchanged. It must stay byte-identical
    to the Python ``change_set_builder.entry_hash``, and folding the tree into
    it would turn every user's first apply after upgrading into a full
    redeploy.

    **How it decides.** After ``reconcile.Compute``, an instance in
    ``Unchanged`` whose recorded tree digest differs from the current one
    moves to ``Change``. ``Plan.Summary`` reports how many of the changes
    came from a local tree, and ``nsctl env plan`` lists them the same way.
    An instance with no recorded tree digest is left alone and gets one
    recorded. That is the same "no information is not drift" rule the
    snapshot already follows, so upgrading nsctl redeploys nothing.

    **When it is recorded.** Exactly where the entry hash is recorded: for
    entries this run settled, and for entries the plan found current. A
    failed deploy records nothing, so the next apply tries again.

    ``--force-full-redeploy`` keeps its meaning. After this NERD it is the
    escape hatch for a digest that disagrees with reality, not the way to
    ship a chart edit.

What this is not
----------------

- Not a rebuild trigger. An edited ``src/python`` in a service's tree does
  not rebuild its image. That stays ``hmd build``, or editable mode later.
- Not a change to cloud behaviour. Both SPECs act only on trees nsctl mounts
  from the workstation.
- Not a change to the Python front end. It is deprecated, and its snapshot
  reader ignores the new optional field.
