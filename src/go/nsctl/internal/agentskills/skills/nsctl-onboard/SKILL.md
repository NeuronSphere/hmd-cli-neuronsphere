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
4. If the task is to author a repository, use `nsctl-repoclass-author` rather
   than guessing a manifest layout.

# nsctl-command: version
# nsctl-command: env
