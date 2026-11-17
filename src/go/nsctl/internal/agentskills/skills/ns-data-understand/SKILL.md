---
name: ns-data-understand
description: Work out with a person what their tables mean and how they join, by asking nsctl data understand's questions one at a time, then render a data dictionary, relationship diagram, views and example queries with nsctl data docs. Use when a data workspace is queryable and the user wants to document their data, name things properly, find how tables relate, or share what the data is with someone else.
---

# Name what the data means

`nsctl data understand` turns the profiles into yes/no questions; your job is
to ask them in the person's terms, record their answers, and show them the
result. Speak about tables, rows, columns, keys and joins. Do not say
"concept", "entity", "model", ".hms", "NEP051" or "perspective" unless the
person asks how it works underneath.

1. Run `nsctl data status --json`. Without a `catalog`, use
   `ns-data-register` first.
2. Explain, then run `nsctl data profile --relationships --json`. It measures
   which columns refer to other tables' keys and writes only evidence in the
   workspace. Mention any join it found that surprises you, with its numbers.
3. Run `nsctl data understand --json`. Read every `notes` entry first: a
   table with no profile is not proposed until it is profiled.
4. Ask the `proposals` one at a time, tables before joins, using each one's
   `question` and its `evidence`. While asking about a table:
   - Suggest a name the person would use for one row (`order` for a table of
     orders) and a one-sentence description from what the columns show, and
     let them correct both.
   - If the proposed key is wrong, ask which column identifies a row.
5. Record each answer:
   - Yes: `nsctl data approve <id> --name <name> --description "<sentence>"`,
     with `--key <column>` if they chose another key (`-` for none).
   - No: `nsctl data reject <id>`. It is not asked again.
   - Not sure: leave it. It stays unconfirmed and is marked so in the
     outputs.
   A join can only be approved after both of its tables; approve those first.
6. Report every entry in `disagreements` plainly, for example a key the data
   no longer supports. Never approve or reject something to make one go
   away; ask the person which is right.
7. Ask where the outputs should go, then explain and run
   `nsctl data docs --out <folder>` (default `./ns-data-out`). It writes
   `DATA.md`, `relationships.md`, `relationships.puml`, `views.sql` and
   `questions.md`, and creates the named views in the catalog. If it refuses
   because a file was edited, ask before passing `--force`; never delete
   anything in that folder yourself.
8. Show the result: a short excerpt of `DATA.md`, then rewrite one of the
   person's earlier queries against the named views (quote names that are SQL
   words, such as `"order"`) and run it with `nsctl data sql`. Point out that
   `views.sql` recreates everything in any DuckDB.

Run understand again after new files are scanned or tables change; it asks
only about what is new. If the person wants the data kept somewhere other
than their folder, hand off to `ns-data-store`; for anything else
afterwards, `ns-data-next`.

# nsctl-command: data status
# nsctl-command: data profile
# nsctl-command: data understand
# nsctl-command: data approve
# nsctl-command: data reject
# nsctl-command: data docs
# nsctl-command: data sql
