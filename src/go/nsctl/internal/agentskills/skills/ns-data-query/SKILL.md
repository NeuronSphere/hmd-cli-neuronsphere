---
name: ns-data-query
description: Answer a person's questions about their own data with visible SQL over the nsctl data workspace catalog, save the queries worth keeping, and show how to open the catalog from their own tools. Use when a data workspace is registered and the user asks a question of their data, wants to run SQL, or wants to connect DBeaver, the DuckDB CLI or another tool.
---

# Answer questions with SQL

1. Run `nsctl data status --json`. Without a `catalog`, use
   `ns-data-register` first.
2. Start with the question the person asked in `ns-data-start`. Before
   writing SQL, check columns with `nsctl data sql "DESCRIBE <table>" --json`
   instead of guessing names. `SUMMARIZE <table>` gives per-column statistics.
3. Show the SQL, then run it with `nsctl data sql "<statement>" --json`, then
   state the answer in a sentence. The SQL is DuckDB's dialect; keep it plain
   so the person can reuse it elsewhere.
4. Never pass `--write`. The catalog is read-only by default so a query
   cannot change it; tables change through the table definitions and
   `nsctl data register`.
5. When an answer is worth keeping, offer `nsctl data sql --save <name>
   "<statement>"`. Saved queries stay in the workspace.
6. If a join gives odd counts, say so and show the evidence (for example a
   key that matches only some rows) rather than adjusting the query until it
   looks right.
7. When the person wants to explore on their own, run `nsctl data connect`
   and relay how to open the catalog from the DuckDB CLI or UI, DBeaver or an
   agent. Recommend opening it read-only. `nsctl data sql` with no statement
   opens an interactive shell; it needs a terminal, so offer it to the person
   to run rather than running it yourself.

Return only the rows needed to answer. Use `LIMIT` and aggregates; never dump
a whole table into the conversation.

# nsctl-command: data status
# nsctl-command: data sql
# nsctl-command: data connect
