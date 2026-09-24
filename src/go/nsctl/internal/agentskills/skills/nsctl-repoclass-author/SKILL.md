---
name: nsctl-repoclass-author
description: Author and validate a RepoClass using nsctl repoclass commands. Use when a user asks to create, edit, inspect, or validate NeuronSphere repository metadata.
---

# Author a RepoClass through nsctl

Work in the repository selected by `--path` (the current directory by default).
Use `nsctl repoclass describe --json` to inspect normalized metadata before and
after edits. Use the `init`, `build`, `deploy`, `test`, and `discovery` verbs;
do not hand-edit manifest fields that those commands own.

Run `nsctl repoclass validate` after a coherent change. Explain dependencies
before adding them and obtain confirmation if the user did not explicitly ask
to change dependency topology.

This skill is for a repository that already has a manifest. For one that has
none, use `nsctl-repoclass-adopt`, which starts from `nsctl repoclass detect`.

# nsctl-command: repoclass
# nsctl-command: repoclass describe
# nsctl-command: repoclass validate
