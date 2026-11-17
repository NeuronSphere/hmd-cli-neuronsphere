---
name: ns-data-profile
description: Profile each table in a data workspace with nsctl data profile, put the decisions it flags to the person, and record their fixes. Use when tables exist but have not been profiled, when query results look wrong (dates as text, lost leading zeros, odd rows), or when files have changed shape.
---

# See what needs a decision

`nsctl data profile` computes everything; you explain it and record what the
person decides. Never compute statistics yourself from file contents.

1. Run `nsctl data status --json`, then `nsctl data profile <table> --json`
   for each table not yet profiled. `--full` reads every row instead of a
   100,000-row sample; offer it only when a decision depends on rare values.
2. Lead with a one-line summary per table: rows, columns, and how many issues.
3. Put each `issues` entry as a question about their data, with the evidence
   and the `suggest` fix:
   - `text_as_date`, `text_as_number`: "`order_date` holds dates but was read
     as text. Treat it as a date?"
   - `leading_zeros`: "Some `zip` values start with 0. Keep it as text so
     they are not lost?" Recommend yes for identifiers and codes.
   - `header_rows_skipped`, `no_header`: lines above the header, such as a
     report title, or no header at all.
   - `schema_drift`: a column that differs between files. Ask whether that
     is expected (a column added later) or a mistake in one file.
   - `personal_data`: say the samples were redacted and that nothing left the
     machine. Do not ask to see the values.
   Candidate keys are not issues; mention them briefly as likely identifiers.
4. Record each accepted fix by merging its `suggest` into the table definition
   (`<root>/tables/<table>.table.yaml`; `root` is in `status`): `casts` maps a
   column to a type, `reader_options` holds read settings. Change nothing the
   person declined. Then profile the table again and show that the issue is
   gone. If the table was already pinned, its pinned schema now differs from
   the fix; say so, and pin again in `ns-data-register`.
5. Ask whether to pin. Pinning (`nsctl data register --pin`, run in
   `ns-data-register`) records today's columns and types, so a file that
   later changes shape is reported as drift instead of silently changing
   answers. Recommend it when the files are exports that will be refreshed.

Share only the profile's samples and statistics in the conversation, never
whole rows from the files. Then hand off to `ns-data-register`.

# nsctl-command: data status
# nsctl-command: data profile
# nsctl-command: data register
