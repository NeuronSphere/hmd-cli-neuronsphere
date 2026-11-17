---
name: ns-data-next
description: Suggest what a person can do next with a data workspace they have explored -- refresh it with new files, keep the work in a git repository with nsctl data export, share it through local storage and Trino, or grow into the rest of NeuronSphere -- and point at the skill or command for each. Use when the person asks "what now", "how do I share this", "how do I keep this", "how do I update it", or has finished answering their first questions.
---

# What next

Offer a short menu chosen from what the person has said they want, not all of
it. Each item names the one command or skill that starts it. Run nothing until
they pick one, and explain and confirm any command that writes.

1. Run `nsctl data status --json` to see where the workspace is: tables,
   storage, and whether it has been exported.
2. Offer what fits:
   - **New or changed files.** `nsctl data status` lists source files changed
     since a snapshot or landing. New files: `ns-data-inventory` (scan again;
     files an existing table reads are not proposed twice), then
     `ns-data-profile`, `ns-data-register`, and `nsctl data understand` to be
     asked only about what is new. Landed tables: `nsctl data land` copies
     only what changed. Refresh the documents with `nsctl data docs`.
   - **Keep the work in git, or hand it to a team.** Explain, then run
     `nsctl data export --repo <new folder>`. It writes a repository with the
     model, table definitions, saved queries and documents, and a manifest
     that `nsctl repoclass validate` accepts. Run it again later to refresh;
     files the person edits there are never replaced without `--force`.
     Committing and pushing it is the person's call.
   - **Generate other forms (dbt, a catalog, a warehouse model).** These are
     made from the exported repository by generators that live outside nsctl;
     the export's `src/model/export.json` and the NEP051 files are what they
     read. Say so plainly rather than promising a command.
   - **Share it, or put it where other NeuronSphere services can read it.**
     `ns-data-store` lands the tables in a local environment's S3.
   - **Query it with Trino or Superset.** In that environment,
     `nsctl bundle add analytics --env <name>` and `nsctl env apply <name>`
     bring up Trino, Superset and Airflow. Trino does not read the landed
     tables on its own: each needs a table defined over its
     `s3://<bucket>/.../raw/<table>/` folder. Offer to help write that SQL
     from `DATA.md`'s columns.
   - **Learn what NeuronSphere is.** `nsctl-onboard`.
3. When they choose, hand off to that skill or walk them through that command.

Never present a step that needs work nsctl does not do (a Trino table, a
generator, a published repository) as if one command finishes it.

# nsctl-command: data status
# nsctl-command: data export
# nsctl-command: data docs
# nsctl-command: data land
# nsctl-command: data understand
# nsctl-command: bundle add
# nsctl-command: env apply
# nsctl-command: repoclass validate
