---
name: ns-data-start
description: Start exploring a person's own data files (CSV, Parquet, JSON, Excel exports) with SQL on their machine, and route to the next step. Use when a user says they have data, exports or spreadsheets they want to look at, query or understand, or asks how to load their files into NeuronSphere.
---

# Explore your own data: start here

The person wants answers from their files, not a platform. Measure progress by
how soon they see their own data answer a question. `nsctl data` needs only a
container engine: no environment, no bucket, no account, no repository.

Say these four things early, once, in plain words:

- Files are read where they are and are never changed or moved.
- Nothing leaves this machine. DuckDB runs in a container with no network.
- What nsctl learns is kept in a workspace under `$HMD_HOME/data/`.
- A first answer usually takes about ten minutes.

1. Run `nsctl data status --json` first. If a workspace already exists, resume
   from its `next` field and the matching skill below instead of starting over.
   `nsctl data list` shows every workspace. "No current workspace" means this
   is a fresh start; carry on below.
2. Ask two things before running anything: what question they want answered,
   and which folder holds the files. Keep their question; `ns-data-query`
   answers it at the end. Do not ask about formats, schemas or keys; the
   commands find those.
3. Run `nsctl doctor` if you are unsure the container engine is running. If
   `nsctl data` reports that the engine is not reachable, help with that and
   nothing else.
4. Explain, then run `nsctl data init <workspace> <folder>...`. Pick a short
   workspace name from their words (`sales`, `survey-2026`). It writes only
   under `$HMD_HOME/data/<workspace>/` and makes it the current workspace.
5. Hand off in order, one step at a time, each with its own skill:
   `ns-data-inventory` (group files into tables), `ns-data-profile` (what
   needs a decision), `ns-data-register` (make them queryable), `ns-data-query`
   (answer the question), then, if they want it documented or shared,
   `ns-data-understand`, and `ns-data-next` for what comes after. Skip ahead
   only when `status` says a step is done.

Do not start an environment, add a bucket, or mention repo classes, manifests,
Trino or the Librarian at this stage. They come later, and only if asked.
Never read a whole data file into the conversation; the commands report what
is in it.

# nsctl-command: data status
# nsctl-command: data list
# nsctl-command: data init
# nsctl-command: doctor
