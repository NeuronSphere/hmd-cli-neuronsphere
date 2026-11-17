---
name: ns-data-register
description: Make a data workspace's tables queryable with nsctl data register, optionally pinning schemas or snapshotting changing files, and check the row counts. Use when tables are profiled but not yet queryable, after a table definition changes, or when files are still being edited and answers should stay put.
---

# Make the tables queryable

1. Run `nsctl data status --json`. If a table is not profiled yet, use
   `ns-data-profile` first unless the person wants to skip ahead.
2. Explain what register does: it builds the workspace catalog, a DuckDB
   database holding one view per table over the files where they are. No data
   is copied. Then run `nsctl data register --json`, with `--pin` if the
   person chose to pin in `ns-data-profile`.
3. Read back each table's row and column counts. Ask whether the counts look
   right for files they know ("about 1,200 orders a month?"). A count that is
   far off usually means a wrong group or junk lines; go back to
   `ns-data-inventory` or `ns-data-profile` rather than patching around it.
4. Report every `drift` and `error` entry plainly. A table whose definition
   declares its schema refuses files that do not match; that is the pin doing
   its job, not a failure to hide. Ask whether the file or the pin is right.
5. If the files are still being edited or replaced and the person wants
   answers to stay put, explain, then run `nsctl data snapshot <table>` and
   `nsctl data register` again. A snapshot is a Parquet copy inside the
   workspace. From then on `nsctl data status` lists source files that
   changed since the snapshot; snapshot again to refresh.

Then hand off to `ns-data-query`.

# nsctl-command: data status
# nsctl-command: data register
# nsctl-command: data snapshot
