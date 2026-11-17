---
name: ns-data-inventory
description: Group a person's data files into tables with nsctl data scan, and agree the groups and names with them. Use when a data workspace has sources but no tables, when new files have been added, or when the user asks which files belong together.
---

# Group files into tables

`nsctl data scan` decides formats and groups; your job is to put its proposal
to the person in their terms and record their answer.

1. Run `nsctl data status --json`. If no workspace exists, use `ns-data-start`.
2. Run `nsctl data scan --json`. It writes nothing. A folder path argument
   adds that folder as a source.
3. Present each proposal in one plain sentence: how many files, which format,
   and what they appear to be. "These 14 CSV files look like one *orders*
   table, one per month; the month becomes a column." Mention each Excel sheet
   as its own table. Do not show the glob unless asked.
4. List the `unsupported` files with their reasons. A zip archive or a
   database dump is not read in this release; offer to proceed without it, or
   suggest the person extract the archive into a folder and scan again.
5. Ask whether the groups and names are right. Prefer short, plural-free,
   lower_snake names the person would say (`orders`, `customers`).
6. Explain, then run `nsctl data scan --apply`, adding
   `--name proposed=new` once per rename. It writes one table definition per
   proposal in the workspace's `tables/` folder.
7. If the person says two proposals are one table, or one proposal is two,
   there is no verb for that yet. Edit `paths` in the table definitions under
   `<root>/tables/` (`root` is in `nsctl data status --json`), say exactly what
   you changed, and run `nsctl data scan --json` again to confirm no file is
   left uncovered.

Then hand off to `ns-data-profile`.

# nsctl-command: data status
# nsctl-command: data scan
