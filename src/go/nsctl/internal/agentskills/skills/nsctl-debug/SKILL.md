---
name: nsctl-debug
description: Diagnose a local NeuronSphere problem from status, plans, and logs without destructive repair. Use when a local environment, control plane, RepoClass, or deployment has failed or behaves unexpectedly.
---

# Diagnose before changing state

Gather the smallest useful evidence: the user's error, `nsctl` version,
environment state, deployment plan or status, and relevant logs. Separate
observed facts from likely causes and name the command or evidence behind each
conclusion.

Do not purge an environment, tear down the control plane, change credentials,
or modify manifests merely to test a hypothesis. Present those actions as
explicit options with their effect and obtain confirmation before running them.

# nsctl-command: version
# nsctl-command: env
# nsctl-command: control-plane
# nsctl-command: repoclass validate
