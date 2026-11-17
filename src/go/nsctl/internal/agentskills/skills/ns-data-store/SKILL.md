---
name: ns-data-store
description: Optionally move a data workspace's tables into S3 in a local NeuronSphere environment, with a bucket the person creates themselves or one the storage bundle sets up, then point the tables there with nsctl data land. Use only when the person wants their data to outgrow the laptop folder, to share it, or to use it from other NeuronSphere services, and has a queryable data workspace.
---

# Put the tables in local storage (optional)

Nothing before this needed an environment, and most people do not need this
step. Offer it only when the person wants the data somewhere other than their
folder: to share it, to keep a copy that does not change when their files do,
or to use it with other NeuronSphere services. Storage is always the S3 of a
local NeuronSphere environment on this machine; nothing leaves it.

1. Run `nsctl data status --json`. If it shows `storage`, the workspace is
   already set up: run `nsctl data land` to land new or changed tables and
   stop.
2. Explain what an environment is in one sentence (a local NeuronSphere with
   its own S3 and services), then run `nsctl env list`. Reuse one the person
   names; otherwise explain and run `nsctl env add <name>` and
   `nsctl env start <name>`. Confirm before each, and say they take minutes.
3. Run `nsctl data storage --env <name>` and show the endpoint and the
   buckets that already exist. Do not pass `--reveal` unless the person asks
   for the secret.
4. Ask: "Do you want to create the bucket yourself, or should I set one up?"
   - **Themselves:** point them at the AWS CLI or Terraform snippet that
     `storage` printed (any S3 tool works), and wait until they say it exists.
     The target is then `s3://<bucket>[/<prefix>]`.
   - **Set one up:** explain, then run `nsctl bundle add storage --env <name>`
     and `nsctl env apply <name>`. The bundle deploys one bucket, an instance
     named `storage`. The target is then `instance:storage`; never type the
     bucket name, nsctl reads it from what the instance produced.
5. Explain, then run `nsctl data land --env <name> --to <target>`. It writes
   each table as Parquet under `<target>/raw/<table>/` and points its view
   there. Read back each table's row count and check it against the counts
   from `ns-data-register`.
6. Say plainly what changed: the views now read the bucket, so the environment
   must be running to query the workspace (`nsctl env start <name>`), and
   `nsctl data land` again copies only the tables whose files changed.
   `nsctl data connect` now also prints the secret a DuckDB on this machine
   needs to read them.

Never delete a bucket or its objects, and never land into a bucket the person
did not name or create for this.

# nsctl-command: data status
# nsctl-command: data storage
# nsctl-command: data land
# nsctl-command: data connect
# nsctl-command: env list
# nsctl-command: env add
# nsctl-command: env start
# nsctl-command: env apply
# nsctl-command: bundle add
