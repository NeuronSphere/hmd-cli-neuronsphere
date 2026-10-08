Give each coding session its own environment
============================================

Several Claude Code sessions -- or a session and a person -- deploying into one
environment undo each other's work. A **session lease** (NERD035) gives each
session an environment of its own for as long as it works: shaped from a
template and the repositories it edits, placed in the closest free pool
environment, stopped when the session ends and reused warm by the next one.

This page wires that up with Claude Code hooks, so every session gets its
environment without asking.

Before you start
----------------

- A template for the kind of work (:doc:`../environments`, "Templates"):

  .. code-block:: bash

     nsctl template add telemetry --stack observability

- A pool big enough for the sessions you run at once, and a cap on how many may
  run -- each is a cluster and a database:

  .. code-block:: toml

     # $HMD_HOME/.config/nsctl.toml
     [pool]
     size = 4
     members = ["local"]
     max_running = 2

- The skill, so the agent knows how to work inside its environment:

  .. code-block:: bash

     nsctl agent skills install nsctl-session-environment

The hooks
---------

Add to the project's ``.claude/settings.json`` (or ``~/.claude/settings.json``
for every project):

.. code-block:: json

   {
     "hooks": {
       "SessionStart": [
         {
           "matcher": "startup|resume|clear|compact",
           "hooks": [
             {
               "type": "command",
               "command": "nsctl env lease acquire --session --pool --shell --no-start --template telemetry --repo \"$CLAUDE_PROJECT_DIR\" >> \"$CLAUDE_ENV_FILE\" && nsctl env lease whoami",
               "timeout": 120
             }
           ]
         }
       ],
       "UserPromptSubmit": [
         {
           "hooks": [
             { "type": "command", "command": "nsctl env lease heartbeat >/dev/null 2>&1 || true" }
           ]
         }
       ]
     }
   }

What each part does:

- **SessionStart** takes the lease and appends ``HMD_LOCAL_ENV`` and
  ``NSCTL_LEASE_TOKEN`` to ``$CLAUDE_ENV_FILE``, which Claude Code applies to the
  session's commands. ``whoami``'s output lands in the session's context, so the
  agent knows which environment is its own. ``--no-start`` keeps session start
  fast: the lease and the composed manifest are written at once, and the agent
  starts the environment (``nsctl env start``) when it first needs it.
- It is safe on every SessionStart source. A session holds one environment: an
  ``acquire --session`` from a session that already holds one returns that
  lease, so ``/clear`` or a compaction never takes a second environment.
- The lease watches the ``claude`` process itself (the nearest ``claude``
  ancestor of the hook), not the short-lived shell the hook runs in. Check it
  once: the ``pid`` that ``whoami`` prints should be your ``claude`` process
  (``ps -p <pid>``). If it is not, the hook ran outside Claude Code's process
  tree and the lease would end with the hook. Pass ``--pid`` with the process
  to watch instead.
- **UserPromptSubmit** heartbeats the lease, so a session longer than
  ``[pool] session_ttl`` (8 hours by default) keeps it. Like ``whoami`` and
  ``release``, ``heartbeat`` finds the session's lease by its process when the
  hook does not see the token.

Without ``--wait`` a session that finds the pool full, or ``max_running``
reached, starts without an environment and the hook says why; add ``--wait`` to
queue instead, and raise ``timeout`` to match.

When a session ends
-------------------

No SessionEnd hook is needed, and none is recommended: Claude Code gives
SessionEnd hooks 1.5 seconds, and stopping an environment takes longer. When the
``claude`` process exits, its lease's process is gone; the next ``nsctl``
command anyone runs -- ``env lease list``, another session's ``acquire`` --
notices, stops the environment (keeping its state) and frees it. To end a
session's environment deliberately, the agent or you run:

.. code-block:: bash

   nsctl env lease release --session

``--keep-running`` leaves it running for a quick return.

Cleaning up
-----------

Stopped pool environments stay, warm, for the next session of the same shape.
Nothing removes them for you; see ``env lease list`` for how long each has been
idle, and ``env purge --idle 72h`` (:doc:`../environments`, "Cleaning up the
pool") when you decide to reclaim the space.
