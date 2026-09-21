Use agent skills
================

The CLI bundles task guidance that you can install for Codex or Claude Code.
Install project skills when the guidance should travel with a repository;
use user scope when you want it available across your own projects.

Inspect the bundled pack
------------------------

From a repository root::

   nsctl agent skills list --path .

The bundled skills cover these tasks:

.. list-table::
   :header-rows: 1

   * - Skill
     - Use it for
   * - ``nsctl-onboard``
     - Getting oriented in nsctl and a repository
   * - ``nsctl-local-environment``
     - Creating and operating local environments
   * - ``nsctl-repoclass-author``
     - Authoring BACON repository metadata
   * - ``nsctl-local-plugin``
     - Adding local extensions
   * - ``nsctl-debug``
     - Investigating local failures
   * - ``nsctl-deploy-plan``
     - Reviewing deployment intent and plans
   * - ``nsctl-auth-and-profiles``
     - Configuring identities and tenant profiles

Preview and install
-------------------

Preview the destinations before installing into both supported hosts::

   nsctl agent skills install nsctl-onboard nsctl-local-environment \
       --host all --path . --dry-run

Install the selected skills::

   nsctl agent skills install nsctl-onboard nsctl-local-environment \
       --host all --path .

For project scope, Codex receives
``.agents/skills/<skill>/SKILL.md`` and Claude Code receives
``.claude/skills/<skill>/SKILL.md``. Use ``--host codex`` or ``--host claude``
to target only one host. Review the installed files and decide whether to
commit them as project guidance.

The operation installs guidance files. It does not configure an AI model,
an MCP server, or the host agent application.

User scope
----------

To make debugging guidance available across your own repositories::

   nsctl agent skills install nsctl-debug --host codex --scope user --dry-run
   nsctl agent skills install nsctl-debug --host codex --scope user

User scope uses the corresponding agent skills directory under the invoking
user's home. Do not combine ``--scope user`` with ``--path``; that path is a
project-scope option.

Verify and maintain
-------------------

Inspect installation health and ownership::

   nsctl agent skills doctor --path .
   nsctl agent skills list --path . --json

If a skill already exists, install refuses to overwrite it unless you pass
``--force``. Read any local modifications before replacing it. Upgrading the
CLI updates the bundled pack; replacing previously installed copies is an
explicit install operation.

To remove an installed skill::

   nsctl agent skills remove nsctl-local-environment --host all --path . --dry-run
   nsctl agent skills remove nsctl-local-environment --host all --path .

Removal normally accepts only an unmodified skill installed by nsctl.
``--force`` permits removing a locally changed selected skill. It does not
remove the enclosing skills directory.

If your agent does not see a skill, check the host and scope with ``doctor``
and confirm that the agent is working in the project where it was installed.
Installation itself does not start environments or apply deployments.
