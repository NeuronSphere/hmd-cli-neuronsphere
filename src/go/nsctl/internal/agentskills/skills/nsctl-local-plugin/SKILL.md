---
name: nsctl-local-plugin
description: Adapt a service for local NeuronSphere development and Floci-safe deployment. Use when a user asks to make a service, plugin, or cloud-oriented deployment run locally.
---

# Adapt local plugin behavior

Inspect the repository and its RepoClass metadata before proposing a local
substitute. Explain whether the service needs a local dependency, a Floci-safe
overlay, or a deliberate no-op for cloud-only infrastructure. Preserve the
repository's existing layout and deployment command wherever possible.

The legacy Python `init-ns-local` workflow is not an `nsctl` command. Do not
present it as one. Use current `nsctl repoclass` inspection and validation
commands, then ask before creating or changing local plugin files.

# nsctl-command: repoclass
# nsctl-command: repoclass validate
