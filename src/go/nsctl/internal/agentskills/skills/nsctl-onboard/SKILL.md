---
name: nsctl-onboard
description: Orient a newcomer to the local NeuronSphere and identify the next safe nsctl command. Use when a user asks what NeuronSphere is, how to begin locally, or why a local setup is needed.
---

# Local NeuronSphere orientation

Treat NeuronSphere as a local deployment substrate: `nsctl` manages a control
plane and local environments; a RepoClass describes software deployed into an
environment. Do not imply that it is a Docker Compose replacement or a cloud
account.

1. Start with the user's goal. Explain only the terms needed for that goal.
2. Inspect rather than assume. Run `nsctl version`; if a local home is needed,
   ask for or locate `HMD_HOME` before commands that require it.
3. Use help and read-only environment views to identify the next step. Explain
   what a mutating command would change before running it.
4. For a genuinely fresh installation, `nsctl quickstart` is the guided path: it
   checks the host, settles `HMD_HOME`, starts the first environment and names
   every command it runs. It needs a terminal, so offer it to the user to run
   rather than running it yourself in a non-interactive session.
5. If the task is to adopt an existing repository, use
   `nsctl-repoclass-adopt`, which starts from `nsctl repoclass detect`. If the
   repository already has a manifest, use `nsctl-repoclass-author`. Do not guess
   a manifest layout in either case.
6. `nsctl env credentials <env>` answers "how do I sign in to this" without
   reading a chart. It withholds values unless `--reveal` is passed; do not pass
   it unless the user asked for the value.

# nsctl-command: version
# nsctl-command: env
# nsctl-command: env credentials
# nsctl-command: quickstart
