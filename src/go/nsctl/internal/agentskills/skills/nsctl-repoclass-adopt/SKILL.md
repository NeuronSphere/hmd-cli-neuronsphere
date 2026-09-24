---
name: nsctl-repoclass-adopt
description: Adopt an existing repository that has no NeuronSphere metadata, using nsctl repoclass detect as evidence. Use when a user asks to run their own project on the local NeuronSphere, to add their repo to an environment, or to write a BACON manifest for a repository that has none.
---

# Adopt an existing repository

Your job is the judgement half. `nsctl repoclass detect` is the deterministic
half, and it deliberately refuses to guess the parts that need a person; do not
do its job by hand, and do not do its refusing for it.

1. Run `nsctl repoclass detect --path <dir> --json` **first** and read all of it.
   Treat the `refused` rows as instructions to you, not as gaps to fill silently.
2. If it reports `already_a_class`, stop adopting. Use
   `nsctl repoclass describe --json` and change fields with the authoring verbs.
3. Apply what it decided: `nsctl repoclass detect --path <dir> --apply`. Supply
   `--description` only when detect could not read one; prefer asking the user
   for that line over inventing it.
4. Run `nsctl repoclass describe --json` before and after every further edit, and
   write only through `nsctl repoclass` verbs. Never hand-edit a field a verb
   owns.
5. Then, and only then, discuss dependencies. **Never add one without the user's
   explicit confirmation of that specific role.** A required role that is wrong
   does not fail its own node; it fails the entire ChangeSet, and the message
   names only the role. Ask what this repository needs at runtime, in their
   words, and map it to roles the environment actually provides.
6. Run `nsctl repoclass validate` after each coherent change and read every
   finding, including notes.
7. A `Procfile`, `fly.toml`, `render.yaml`, `vercel.json` or `app.yaml` means
   there is no local equivalent of how this repository currently deploys. Say so
   plainly; a chart or a deploy command has to be authored, and you should offer
   to help write one rather than pretending a manifest alone will run it.

Do not add resources or `meta-data/resources/*.yaml`, and do not write a
`discovery` section as part of adoption. Do not claim a deploy works because the
manifest validates: `nsctl repoclass validate` checks metadata shape, not
execution.

# nsctl-command: repoclass
# nsctl-command: repoclass detect
# nsctl-command: repoclass describe
# nsctl-command: repoclass validate
