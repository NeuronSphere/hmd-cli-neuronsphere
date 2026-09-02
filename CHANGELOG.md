# Changelog

## 2026-09-02

- fix: Create a DB subnet group per account, working around a Floci bug

  `CreateDBInstance` failed for every environment with
  "InvalidVPCNetworkStateFault: No subnets available for DB subnet group
  default". Floci's `Ec2Service.ensureDefaultResources` seeds a region's default
  VPC and subnets, but guards on a `Set<String> seededRegions` keyed by *region
  alone* while writing into storage namespaced per *account* -- so the first
  account to touch EC2 in a region marks it seeded and every other account is
  skipped, left with no default VPC. Phase 1 made that the common case by
  putting every environment in one Floci.

  Creating our own VPC cannot fix the implicit path, since Floci resolves the
  default VPC by a fixed id (`vpc-default-<region>`) we cannot assign. So
  `provision_resources` now creates a VPC, two subnets and a named DB subnet
  group per account, and both RDS deploys -- the control plane's bootstrap node
  and each environment's BOM entry -- place their instance in it explicitly.

  Best-effort: if the group cannot be ensured the deploy still runs and fails
  with Floci's own error, which says more than one invented here would.

- fix: Stop an orphaned RDS volume from permanently blocking `up`

  The PostgreSQL-compatibility check scanned every `floci-rds-*` volume,
  including ones no instance references. That made the error a dead end: its own
  suggested remedy, `down --purge`, discards Floci's instance records and so
  *orphans* the volume by definition, leaving `up` refusing to start over data
  nothing would ever mount again. Reported from a real install stuck on a
  PostgreSQL 12 volume left over from an old experiment.

  Two fixes. The check now ignores volumes no recorded instance would mount,
  read from Floci's persisted `rds-instances.json` before it starts; an
  unreadable state file treats everything as live, since a wrong "orphan" would
  skip a real incompatibility. And `down --purge` now sweeps `floci-rds-*`
  volumes by name rather than only deleting instances it can derive an
  identifier for -- the volumes most needing removal are precisely those whose
  instance record is already gone.

  The error message also names the volume outright (`docker volume rm <name>`),
  because both remedies it offered can fail to apply: `db upgrade` is wasted
  work on data nobody wants, and `down --purge` may already have run.

- fix: Supply every required dependency role on the environment-db BOM entry

  ms-deployment validates required *roles* when a changeset is applied, not when
  the deploy runs, so `hmd-postgres-rds`'s cloud dependencies had to be declared
  even though the local overlay references none of them. Without them `up`
  failed at Phase A with "For RepoInstance, environment-db, required role,
  rds-loggroup, not provided."

  `base-vpc` is resource-typed, so presence in the map is not enough -- the
  supplied instance is validated against what it actually produces. The core
  RepoClass already declared producing `network.neuronsphere.io/network` for
  repos like hmd-inf-hive-metastore, but hmd-postgres-rds names the `vpc`
  subtype specifically, and producing a parent type does not satisfy a
  requirement for its child. The core now declares both; Docker networking
  substitutes for a VPC locally either way.

  `datadog-lambda` and `rds-loggroup` are name-only roles that nothing validates
  beyond presence -- there is no CloudWatch or Datadog locally.

  Two regression tests read the real manifest: one asserts every required role
  is supplied, the other that any resource-typed role pointed at the core
  instance names a type the core declares producing.

- fix: Create the CDKTF state bucket before the DAG's first CDKTF deploy

  `provision_resources` creates the `hmd.<account>.<region>.tfstate` bucket the
  CDKTF S3 backend uses, and it had been folded into the DAG node that runs
  *after* the Postgres deploy. `tofu init` fails outright against a bucket that
  does not exist, so the control plane's first real deploy could never succeed:
  "Failed to get existing workspaces: S3 bucket does not exist."

  It now runs before the DAG. Nothing in it needs the database: the admin secret
  records `hmd_db` as the host, which is the alias the instance is given once it
  is up.

  A regression test pins the bucket name, which is derived twice -- here and by
  `hmd_lib_cdktf`'s S3Backend inside the projectbuilder container. A divergence
  produces the same "bucket does not exist" error, which reads like a
  provisioning failure rather than a naming one.

- fix: Generate a deploy command `hmd deploy` actually accepts

  The bootstrap DAG's Postgres node invented a positional tool name
  (`hmd deploy --instance-name ... cdktf`). `hmd deploy` has no such positional
  -- its only one is `status`, and it reads `manifest.json`'s `deploy.commands`
  to know which tool to run -- so `up` failed inside projectbuilder with
  "argument command: invalid choice: 'cdktf'".

  The command now mirrors what ms-deployment's `deploy_base.deploy_node`
  emits: repo identity on *global* `hmd` flags ahead of the subcommand
  (`--repo-name`, `--repo-version`, `--hmd-region`), and the instance
  configuration on stdin as a quoted heredoc. Two omissions are deliberate,
  both because ms-deployment does not exist yet while this runs: no
  `--register`, and no `HMD_REPO_INSTANCE_DEPLOYMENT_ID` export. The runner
  records both itself, buffered until the replay.

  The configuration must be passed explicitly rather than left to the manifest:
  its `default_configuration` describes Aurora (`engine_version: 17.9`,
  `instance_type: db.r7g.large`) and would otherwise override the local
  overlay's defaults with values a plain `aws_db_instance` cannot use.
  `engine_version` is read from the image Floci will actually spawn, so the
  declared version cannot drift from the binary that initialises the data
  directory.

  New tests check the generated command against `hmd deploy`'s real argument
  surface -- every flag it uses, and that it contains no positional -- since
  the original bug was inventing a CLI rather than reading it.

- feat: Detect a PostgreSQL major-version bump before it breaks start-up

  Floci recreates an RDS instance's container from the *current* postgres image
  on every start while reusing the instance's volume, so bumping the major
  version in `hmd-postgres-base` leaves the old data directory behind and the
  new binary refuses it. Nothing reported that at the point of the change --
  Floci still calls the instance `available` -- so the first symptom was
  whatever connected next failing, a layer removed from the cause.

  `up` now compares the image's `PG_MAJOR` against the `PG_VERSION` file
  postgres writes into its own data directory. Both are readable without
  starting anything, so the check is cheap and runs before Floci can spawn a
  container that crash-loops. On a mismatch it names the volumes and offers both
  ways out. Anything indeterminate (image not pulled, Docker unavailable, volume
  not yet initialised) yields no mismatch rather than a false alarm, because
  this gates `up`.

  `hmd neuronsphere db upgrade` migrates in place -- Floci looks for the volume
  by the name it derived when it spawned the container, so a differently-named
  copy would be ignored. It dumps with a stock `postgres:<old>-alpine` (the
  configured image cannot read that data directory, which is the whole problem),
  clears the volume so the new image runs `initdb`, and restores. The old data
  is copied to `hmd-pgbackup-<volume>-pg<major>` first and never deleted.

  That backup name deliberately sits outside Floci's `floci-rds-` namespace: a
  backup inside it would be rescanned by the check and, holding the old data
  directory by definition, reported as a mismatch forever -- leaving `up`
  blocked after a migration that had already succeeded.

- feat: Deploy each environment's Postgres as a Floci RDS instance too

  Completes the migration the control plane started. `hmd-postgres-rds` is now
  the first real node of every environment's core changeset, so a
  `database-instance` dependency -- `hmd-database-account`'s above all --
  resolves against a Resource a real deploy produced.

  The hand-seeded `hmd_db` Resource and the `database.neuronsphere.io/postgres`
  entry in `CORE_PRODUCED_DEFINITIONS` are therefore **deleted**, not
  re-pointed. Keeping them would give an environment two producers of the same
  type, and a dependency could resolve to the hand-written record describing a
  container that no longer exists.

  The container is aliased `hmd_db-<slug>` between the two changeset phases --
  after Phase A creates it, before any Phase-B entry addresses it -- and CoreDNS
  maps plain `hmd_db` to it inside the environment's cluster, so cloud charts
  still run unmodified. `down --purge` now deletes the RDS instance and its
  volume in both scopes: the volume deliberately survives a plain restart
  (`FLOCI_STORAGE_PRUNE_VOLUMES_ON_DELETE` is pinned false), which is exactly
  what a purge has to undo.

  The `db` service is gone from the environment compose file, and the pre-DAG
  database wait with it: there is nothing to wait for before the changeset that
  creates the database has run.

- feat: Bootstrap the control plane with a DAG whose last node is ms-deployment

  The control plane's Postgres is now deployed by the real `hmd-postgres-rds`
  RepoClass as a Floci RDS instance, through the same projectbuilder path every
  other deploy takes -- so its `database.neuronsphere.io/postgres` Resource is
  genuinely produced and read from `meta-data/resources_output/`, rather than a
  record hand-seeded to describe a container compose happened to start.

  That is possible because `LocalWorkflowRunner.run` takes a plain ordered list
  of node dicts and never asks the deployment service for them. Its only
  coupling is three callbacks, so a new `tracking=False` buffers them and
  `replay_into()` flushes them once ms-deployment is serving. The control plane
  therefore ends up recorded in the graph it just deployed, which it never was
  before.

  A node may now carry a `handler` callable instead of a deploy script,
  generalising the existing `CORE_REPO_CLASS` no-op special case. The nodes
  after the database (core databases, ms-naming, artifact-lib, ms-deployment)
  use it: they provision what the deployment service needs and so cannot be
  deployed *through* it. Each closure is the step that used to run inline in
  `ensure_control_plane`, unchanged. Moving one onto the projectbuilder path
  later is a per-node change, and the ordering it participates in is already
  right.

- feat: Keep `hmd_db` as the canonical hostname now that Floci owns the container

  Floci names the RDS backend it spawns opaquely
  (`floci-rds-db-<HEX>-<suffix>`) and finds it by label, not by name. Rather
  than rewrite every consumer -- compose peers (the Deployment GUI, Hive
  metastore, Trino, Airflow, Superset), `_psql`, and cloud Helm charts running
  unmodified in k3s -- `ensure_rds_network_alias` gives the container the
  `hmd_db` alias on the NeuronSphere network. Docker refuses to add an alias to
  an existing endpoint, so it disconnects and reconnects; that is safe because
  it runs immediately after creation, before anything has connected, and is
  skipped when the alias is already present.

  This also keeps the port at 5432 rather than routing through Floci's
  7001-7099 RDS proxy -- which matters because that proxy is not re-established
  after a Floci restart.

  The `db` service is gone from the control-plane compose file. The Deployment
  GUI loses its `depends_on`, which it did not really need: its own database
  was created after `compose up` returned even when `db` was a service, so its
  migrate retry loop was always what waited.

  Note the environment compose file still runs a `db` service. The
  per-environment migration to RDS needs its own BOM entry and is not done yet;
  removing the service first would leave an environment with no database.

- feat: Collapse the per-environment Flocis into one multi-account Floci

  An environment used to run its own Floci container alongside its Postgres,
  JanusGraph and k3s cluster -- roughly 1.5-2.5 GB each, the footprint
  limitation `docs/environments.rst` already called out. Floci isolates accounts
  within a single container, resolving which one a request belongs to from the
  SigV4 access key id it is signed with (a 12-digit AKID *is* the account) and
  namespacing every storage-backed service beneath it. So the `floci` service is
  gone from `docker-compose.environment.yml`, and an environment is now an
  account inside the control plane's Floci.

  `FlociTarget` gains `access_key_id`, and that field alone selects the account:
  `env_target()` returns the same endpoint, container and alias as the control
  plane, differing only in the account. Because every caller already went
  through `_get_client`, the existing call sites became account-correct without
  being touched.

  `_get_client` now signs with `target.access_key_id` rather than
  `$AWS_ACCESS_KEY_ID`. An ambient key was harmless while accounts were
  separated by endpoint; now it would silently route an environment's calls into
  whichever account that key names. The same fix is threaded into the two places
  that build credentials by hand: the projectbuilder containers running deploy
  nodes, and the External Secrets operator's chart values (a new
  `_inject_floci_account`, mirroring `_inject_docker_credentials`) -- otherwise
  every environment's operator authenticates as one account and resolves the
  control plane's secrets instead of its own, silently, since the secret names
  are identical across environments.

  Also removed as a consequence: `floci_container`/`floci_alias` as persisted
  registry fields (now derived constants, so stale per-environment values in an
  existing registry fall away on load), the per-environment Floci stream
  listener in nginx, the per-environment Floci health wait, the now-dead
  `_wait_for_container_health`, and the `FLOCI` column in `env list`, which
  pointed at a port nothing listens on. The port-slot layout is deliberately
  unchanged -- `trino_port`/`graph_port`/`spare_port` are offsets from the slot
  base, and slot 0's spare port is the Deployment GUI's published 19003, so
  reclaiming one unused port would move every environment's Trino and the GUI.

- feat: Refuse to start over pre-collapse per-environment Floci state

  That state cannot be migrated: Floci keys persisted records by an account
  prefix whose on-disk format it does not document. Ignoring it would be worse
  than failing -- an environment's Lambdas, gateways, buckets and secrets would
  appear to have vanished while `up` reported success. `up` now names the
  environments involved and points at `down --purge`. Legacy-layout
  environments are exempt: their "own" Floci data dir *is* the control plane's.

- fix: Upgrade Floci 1.7.0 -> 2.0.1

  The only breaking change across the 2.0 boundary is a Step Functions JSONata
  fix, and `stepfunctions` is not in `FLOCI_SERVICES`. 2.0.0 lands several
  things the local platform wants directly: EKS cluster restoration after
  restart, Lambda ARN/function-URL invocations resolving in the owning account,
  API Gateway v2 routing requests to the API-owning account (all three
  load-bearing for multi-account), and RDS matching AWS's real defaults so a
  second `terraform plan` reports no changes.

## 2026-09-01

- fix: Pin Floci at 1.7.0 so a plain `down` preserves the k3s cluster's volume

  Every prior Floci version's `ContainerLifecycleManager` removed the spawned
  `floci-eks-<cluster>` container **and its named volume** whenever the parent
  `floci`/`floci-<env>` container stopped -- so even though a plain `down`
  only stops containers (never removes them) and `stop_k3s_cluster` explicitly
  preserves the k3s cluster's own container, the cluster's datastore was still
  destroyed as a side effect, and every `up` got a new `kube-system` UID.
  `_bootstrap_environment` read that as "the cluster was replaced" and
  redeployed every k8s-backed instance, even on an otherwise-unchanged
  restart.

  Floci 1.7.0 re-adopts a still-recorded cluster's container/volume on
  restart instead of tearing it down, controlled by two new env vars now
  pinned explicitly in all three Floci compose files:
  `FLOCI_SERVICES_EKS_KEEP_RUNNING_ON_SHUTDOWN=false` (the CLI still stops the
  k3s container itself) and `FLOCI_STORAGE_PRUNE_VOLUMES_ON_DELETE=false` (its
  volume is no longer pruned as a side effect). The image tag is now
  `floci/floci:${HMD_LOCAL_FLOCI_VERSION:-1.7.0}`, matching every other
  bundled image's override-var convention, where it was previously hardcoded.

  A `down` then `up` cycle keeps the same `kube-system` UID end to end, so
  `up` now takes the true restart fast-path with no k8s-instance redeploy.
  The Helm-release cross-check that narrows a redeploy to just the
  k8s-backed instances remains as a safety net for the cases where the
  cluster genuinely is replaced (a Docker daemon restart evicting the
  container, a wrapper-image mismatch forcing a recreate, or an environment
  bootstrapped before release tracking existed). `down --purge` is
  unaffected -- it already force-removes the container and volume directly.

- feat: Mint and print an MCP API key when the control plane starts

  The Deployment GUI serves a read-only MCP endpoint at `/mcp/`, and the control
  plane already ran it with `MCP_API_KEYS_ENABLED` -- Okta is not emulated
  locally, so a platform API key is the only credential it accepts. Nothing ever
  created one. `up` therefore brought up an MCP server that answered 401 to every
  caller, and the only way in was knowing to run a management command inside the
  container by hand.

  `environments.ensure_mcp_api_key` now runs at the end of `ensure_control_plane`,
  just before the GUI URL is printed. It waits for the GUI's `/health/` to answer
  200 -- a stronger signal than the container's healthcheck, whose interval is
  30s, and the point at which the container's `until migrate` loop has finished --
  reads back whether MCP came up at all, and execs
  `create_mcp_api_key --if-not-exists` for the local superuser. Only the SHA-256
  of a key is stored, so the plaintext exists for exactly one moment; it is
  printed there, padded and unindented so it does not read as one more status
  line.

  `--if-not-exists` keys off the key's name, so a second `up` prints nothing and
  rotates nothing, and a client configured once keeps working. Every failure mode
  -- an unready GUI, an image predating the command, docker unavailable, MCP
  switched off -- is a warning and never a failed `up`. `MCP_ENABLED` in the
  compose file becomes `${HMD_LOCAL_GUI_MCP_ENABLED:-true}`, one switch for both
  the server and the minting step, and `hmd neuronsphere env status` gains a
  `deployment_gui_mcp` route. The key itself is deliberately not recoverable
  there: `docs/modes.rst` documents minting a replacement under a different name.

- chore: Pin the Deployment GUI at 0.1.74 and ms-deployment at 0.1.848

  `MS_DEPLOYMENT_VERSION` is new: the control plane fell back to the floating
  `stable` tag on a machine with no `$HMD_REPO_HOME/hmd-ms-deployment` checkout,
  so `up` ran whatever was published last. A checked-out working tree and
  `HMD_MS_DEPLOYMENT_VERSION` still win — only the fallback changed.

- refactor: Run the Deployment GUI as a control-plane container instead of a k3s workload

  `hmd-app-neuronsphere` was in the local BOM, so every `up` deployed it through
  the full DAG — CDKTF, Helm into the environment's k3s, the Traefik-as-`alb`
  patch, an ext-secrets dependency, a NodePort, an Ingress-host rewrite, an
  ms-dbaccount round trip and a private-registry image import into containerd —
  before the GUI could serve a page. That chain was blocking `up`.

  The GUI is the control plane's own management surface, not a platform
  workload, so it now runs as the `deployment-gui` service in
  `docker-compose.control-plane.yml` beside `hmd_db` and `floci`, and its
  database is one of `floci_deployer.CORE_DATABASES` (created directly by
  `psql`, like `hmd_ms_naming` and `hmd_ms_deployment`). `hmd_proxy` serves it at
  the same `http://localhost:19003/` as before, now proxying straight to the
  container — no Ingress, so no Host rewrite or `proxy_redirect` pair.

  Nothing of `hmd-app-neuronsphere` is bundled into this CLI any more: its
  `pre_build_artifacts` entry is dropped, since the container runs from the app's
  published image rather than from its Helm chart. The version is a pin
  (`environments.GUI_IMAGE_VERSION`), still overridable with
  `HMD_LOCAL_VERSION_HMD_APP_NEURONSPHERE`.

  `bom_seeder.gui_bom` and both of its BOM appends are gone;
  `HMD_LOCAL_NEURONSPHERE_ENABLE_GUI=false` still opts out, now via a compose
  profile. `gui_port()` lost its `env` argument — the GUI is one control-plane
  singleton serving every environment rather than one instance per environment —
  and `HMD_LOCAL_GUI_HOST_PORT` overrides it. An existing install still carrying
  the old Helm release sees the removed entries as "declared no longer; destroy"
  on the next reconcile.

## 2026-08-31

- fix: Make a non-purge `down` preserve the k3s cluster so `up` takes the restart fast-path

  `stop_environment` deleted the environment's k3s cluster on every `down`, not
  just under `--purge`. Floci's `delete_cluster` drops the cluster's
  `floci-eks-<name>` volume along with it, so the next `up` got a brand-new
  cluster with a new `kube-system` UID — which `_bootstrap_environment` reads as
  "the cluster was replaced since the last bootstrap" and answers by redeploying
  the entire BOM. The documented restart fast-path was therefore never taken.

  A plain `down` now stops the k3s container, stops (rather than removes) the
  containers, and leaves the Docker network in place; `ensure_k3s_cluster`
  restarts a stopped container on the expected image instead of recreating it,
  falling back to a recreate only when the start fails. `--purge` keeps the old
  destructive behavior.

  Floci's `ContainerLifecycleManager` still removes the `floci-eks-<cluster>`
  container and its volume when the environment's `floci-<env>` container stops,
  so the cluster's datastore does not survive a `down` today. What did change is
  that the cluster *record* now persists (`eks-clusters.json` reloads it rather
  than coming back empty), and the redeploy is no longer all-or-nothing:

- fix: Redeploy only the k8s instances when the k3s cluster is replaced

  A mismatched `kube-system` UID forced `_run_full_bootstrap` — the entire BOM —
  even though only the instances deployed onto k3s were actually lost. S3
  buckets, cdktf-to-Floci stacks and Lambdas live in Floci, whose state persists
  across the restart. `up` now reconciles instead, and the release cross-check
  turns exactly the k8s-backed entries into additions. With no recorded release
  information it still falls back to the full bootstrap, since narrowing without
  that record would be a guess.

- fix: Cross-check Helm releases against the deployment graph during reconcile

  ms-deployment records intent and cannot see the cluster, so an instance whose
  release was uninstalled stayed `DEPLOYED` and `up` reported "matches its
  declared state" over a missing workload. The applied-changeset snapshot now
  records the Helm release each entry installed, and `compute_plan` proposes a
  redeploy when that release is gone. Entries that install no release are
  unaffected, and an unreadable cluster is never mistaken for an empty one.

- feat: Add a `get_post_deploy_notices` plugin entry point

  There was no way for an installed local-BOM plugin to surface anything in
  `up`'s "Ready" summary short of hardcoding plugin-specific knowledge into
  this repo — the summary's per-environment block was a fixed list of core
  URLs. `hmd_cli_neuronsphere.get_post_deploy_notices` is a new entry-point
  group, collected the same best-effort way as the existing
  `get_local_bom_entries`/`get_resources` hooks: each installed contributor is
  a callable `(env) -> List[str]`, and a broken contributor logs a warning and
  is skipped rather than breaking `up` for everyone else. `start_neuronsphere_
  extend` prints whatever lines every contributor returns after the
  environment's URL block. First consumer:
  `hmd-cli-plugin-ns-visualization` reports its Superset admin login this way
  instead of requiring a manual `aws secretsmanager get-secret-value` call.

- feat: Expose `floci_deployer.get_client` as a public Floci client factory

  Plugins implementing `get_post_deploy_notices` (or any future entry point)
  need to read their own secrets/resources back from an environment's Floci,
  but the existing `_get_client` is private and has ~20 internal call sites
  not worth touching. `get_client(service, env)` is a thin public wrapper
  around it for exactly this kind of cross-package use.

## 2026-08-28

- fix: Address Floci by its network alias, never the `floci` compose service key

  Docker Compose registers every *service key* as a network alias, so the `floci`
  key shared by the control-plane and environment compose files resolved
  round-robin to both containers. Control-plane API Gateway and Lambda calls
  landed in environment accounts at random, and `hmd neuronsphere up --upgrade`
  on a running platform failed with `Invalid API id specified`. The same
  collision on `db` pointed the Hive metastore at whichever Postgres won the
  coin flip.

  **On upgrade:** the control-plane Floci container is recreated on the next `up`
  (~30s) so its gateway table starts clean. An environment that was bootstrapped
  before this fix also ran its BOM/DAG through the ambiguous route, so its
  deployment records may be inconsistent; `hmd neuronsphere env delete <slug>
  --purge && hmd neuronsphere up` is the clean path. That also destroys the
  environment's Postgres and JanusGraph data, so it is not done automatically.
- fix: Resolve container CLI dynamically instead of hardcoding; improve image caching logic
- feat: Implement host-side staging for local Lambda images and enhance image resolution logic
- feat: Update ingress routing for local environments
- fix: Update version of ext-secrets dependency in manifest.json
- fix: Scope `up --upgrade` image pulls to Extend mode's own compose files

## 2026-08-27

- feat: Add support for additional cdktf file paths in setup configuration
- feat: Add support for cdktf files in external local directory
- fix: Resolve artifact version conflicts and improve error reporting in `up` command
- feat: Add comprehensive tests for nginx router, port validation, and repo version resolution

## 2026-07-28

- test: update local runner fixture to quoted heredoc delimiter

## 2026-07-24

- feat: expose local k3s Trino on host :18080 via nginx stream (no port-forward)
- fix: drop the k3s container and volume on down --purge
- fix: source local DB-secret customer_code from hmd.env, default none
- feat: resolve global-graph via k3s CoreDNS for Trino graph catalog

## 2026-07-22

- feat: local-deploy fixes for analytics-engines bring-up

## 2026-07-21

- refactor: replace strategy-based local-overrides with two-phase BOM/changeset bootstrap

## 2026-07-17

- feat: Enable ext-secrets by default in local NeuronSphere
- refactor: Remove KEDA from hardcoded k3s core-operator install path
- fix: resolve local ClickHouse/otel-collector deploy failures on k3s

## 2026-07-16

- refactor: Update NeuronSphere CLI with plugin architecture and remove deprecated components
- refactor: Remove ClickHouse and Telemetry plugins along with related configurations
- feat: Add local Postgres DB provisioning and registration commands

## 2026-07-14

- feat: complete the local hmd deploy DAG loop (Resource-driven cluster, cdktf-local)
- feat: opt-in local ext-secrets dev-deploy loop (single-Floci branch checkpoint)

## 2026-07-06

- feat: Enhance local k3s deployment for NeuronSphere

## 2026-05-04

- feat: Enhance Docker Compose configurations and introduce customer-derived librarians

## 2026-04-27

- feat: Enhance Floci deployment with local image resolution and API Gateway improvements
- feat: Add Argo plugin and extend mode tests

## 2026-04-22

- feat: replace MiniStack with Floci and add legacy/deploy mode switching

## 2026-04-15

- feat: add MiniStack integration replacing MinIO and DynamoDB plugins

## 2026-03-13

- fix: skip port-in-use warnings for existing NeuronSphere containers

## 2026-03-03

- fix: update version numbers for pre_build_artifacts in manifest.json

## 2026-02-27

- feat: add enabled_by_default to nsplugin.json spec for plugin default state

## 2026-02-25

- fix: update version numbers for airflow, clickhouse, and otel-collector in manifest

## 2026-02-24

- fix: ensure .env-non-dev is packaged and available for superset startup
- feat: add aws-secretsmanager-caching to requirements
- feat: update plugin configurations and add interactive selection for enabling local plugins
- feat: add clean startup/shutdown output with service URL summary
- feat: add port conflict detection for local NeuronSphere startup
- feat: add clickhouse, hive-metastore plugin wrappers and superset pre-build artifact

## 2026-02-20

- feat: update pre-build artifacts to latest versions for consistency
- feat: add telemetry profile seeding from local plugin nsplugin.json

## 2026-02-19

- fix: resolve network and db-init issues for local plugin containers

## 2026-02-18

- fix: telemetry plugin now uses base.py helpers for local plugin support

## 2026-02-17

- feat: add pre-build artifacts for airflow, clickhouse, and otel-collector plugins

## 2026-02-10

- fix: local plugin support for neuronsphere down/restart commands

## 2026-02-06

- feat: auto-generate postgres init containers from nsplugin.json
- fix: use cement minimal_logger to fix namespace field error
- feat: make local plugin discovery explicit via HMD_LOCAL_PLUGINS
- fix: include directory-based skills in package_data
- fix: support directory-based skills in SkillsLoader
- feat: update init-ns-local skill to directory format with manifest analysis
- feat: add configurable plugin support via config_local.json

## 2026-02-05

- feat: add pre_build_artifact for hmd-ms-transform in manifest.json
- feat: add init-plugin command to scaffold local plugin structure
- feat: add validate-plugin command for nsplugin.json validation
- feat: register handler with hmd_cli.controllers entry point
- feat: add local filesystem plugin support from HMD_REPO_HOME
- feat: add AI skills support with init-ns-local skill
- feat: add support for external Docker Compose artifacts from other repos

## 2025-12-16

- fix: add AWS credentials to Jupyter docker-compose configuration

## 2025-10-24

- fix: update python-dotenv version to 1.1.1 in requirements.in

## 2025-08-01

- fix: update DynamoDB local image version and remove unnecessary startup option

## 2025-07-30

- fix: ensure cache directory is created if it doesn't exist and handle missing databases gracefully

## 2025-07-14

- fix: update docker-compose file path and environment variables for transform service

## 2025-05-07

- fix: update bucket name retrieval in start_neuronsphere function

## 2025-04-21

- fix: remove deprecated requirements.txt file

## 2025-03-17

- fix: adds HMD_REPO_HOME and updates docker-compose configurations

## 2025-03-03

- fix: bumps hmd-cli-app version
- fix: fixes some minor initial config bugs

## 2025-02-17

- fix: fixes restarting local cached services

## 2025-01-03

- fix: fixes local encryption key

## 2025-01-02

- fix: fixes quotes

## 2024-12-21

- fix: fixes error on missing query config
- fix: made compose cmd configurable

## 2024-12-19

- feat: adds gozer

## 2024-11-14

- fix: pins PyYAML

## 2024-08-21

- fix: fixes update images

## 2024-08-07

- feat: adds Jaeger to telemetry plugin

## 2024-07-19

- feat: registers services w/ ms-naming on up

## 2024-07-18

- feat: adds restart command

## 2024-07-01

- fix: fixes update-images cmd

## 2024-04-03

- fix: fixes minio plugin

## 2024-03-12

- fix: fixes SECRET_KEY
- fix: temporarily removes prev secret key

## 2024-03-05

- fix: creates missing neuronsphere_default network

## 2024-02-28

- fix: adds missing hadoop env
- fix: adds trino plugin

## 2024-02-16

- feat: adds instance name to local svc yaml
- fix: adds missing service files

## 2024-02-15

- feat: adds telemetry containers

## 2024-02-14

- feat: adds otel collector

## 2024-02-13

- feat:  implements plugin architecture
- feat: adds naming service

## 2024-02-07

- fix: fixes running local services

## 2023-11-14

- fix: removes dependencies on nginx

## 2023-09-08

- feat: configures for airflow img seq tf

## 2023-08-29

- fix: adds correct gremlin config

## 2023-08-15

- fix: adds healthchecks to depends on

## 2023-08-14

- fix: removes print statement

## 2023-08-11

- fix: fixes overwrite conn for airflow scheduler
- fix: adds correct session properties to trino overwrite

## 2023-08-09

- feat: saves addtl local services for restart

## 2023-08-04

- fix: adds missing data files
- feat: connects transform svc to graph and queues

## 2023-08-03

- fix: uses HMD_LOCAL_NS registry

## 2023-07-12

- fix: changes env var for container registry

## 2023-06-09

- fix: adds nginx conf to setup.py

## 2023-06-07

- feat: adds Nginx reverse proxy

## 2023-06-06

- fix: makes superset more configurable

## 2023-06-02

- feat: makes db conns configurable

## 2023-05-10

- feat: adds MinIO container

## 2023-04-25

- fix: fixes missing db_init key
- feat: adds override arguments for starting services
- feat: adds local graph db

## 2023-04-11

- fix: fixes defaulting postgres version
- fix: add quotes to file paths
- fix: typo

## 2023-04-05

- fix: fixes versioned export on superset

## 2023-04-04

- fix: adds back init scripts mount

## 2023-03-07

- fix: removes warnings about unset vars

## 2023-03-06

- fix: adds full hdfs config for metastore

## 2023-03-01

- fix: bumps deps
- fix: fixes postgres init scripts
- fix: adds hive files to package

## 2023-02-28

- fix: switches jupyter img to env var
- feat: updates Trino images to HMD builds

## 2023-02-27

- feat: converts trino img to HMD built one
- fix: removes mounting superset scripts
- fix: removes NB_USER env var
- fix: fixes postgres init scripts dir

## 2023-02-26

- fix: fixes missing HMD_DID
- fix: removes booleans
- fix: fixes remaining file versions
- fix: updates docker-compose version
- fix: fixes docker-compose version of superset

## 2023-02-24

- feat: adds TRINO_BUCKET envvar to transform

## 2023-02-22

- fix: fixes projects mount on jupyter

## 2023-02-21

- fix: fixes default for aws region
- fix: removes --quiet-pull from run

## 2023-02-17

- fix: adds defaults to env vars
- fix: :bug: fixes bug with missing .aws folder

## 2023-02-15

- fix: conditionally copies trino config
- fix: :bug: adds missing package data
- fix: adds trino config
- fix: :bug: fixes missing required dirs

## 2023-02-14

- fix: enables all techs by default
- feat: adds update-images command

## 2023-02-10

- fix: fixes mounting projects

## 2023-02-06

- feat: updates docker-compose ymls

## 2023-01-31

- fix: bumps app and tool versions

## 2023-01-30

- feat: allows specifying local config in meta-data

## 2023-01-27

- fix: :bug: fixes default compose for running ms

## 2023-01-25

- feat: adds mount pkgs opt to run command

## 2022-11-15

- feat: adds run cmd for local svc dev

## 2022-11-14

- fix: bumps superset version

## 2022-10-13

- fix: fixes local transform issue

## 2022-08-18

- feat: adds support for disabling local project service

## 2022-08-16

- fix: fixes diagram location

## 2022-07-21

- feat: adds dag_generators mount and new trino conn

## 2022-07-15

- fix: fixes trino data location

## 2022-07-13

- fix: removes volumes for trino
- fix: makes volumes more portable

## 2022-07-08

- fix: removes unused api key refs

## 2022-07-07

- feat: adds required directory on start

## 2022-07-06

- feat: update transform service
- feat: update transform service

## 2022-07-05

- feat: adds core hmd home files

## 2022-07-01

- feat: added mount for project

## 2022-06-29

- fix: use build_repo
- feat: adds local_transform_projects path to transform compose

## 2022-06-06

- fix: fixes mount target
- feat: adds aws mount to jupyter

## 2022-05-17

- feat: adds git env vars

## 2022-03-31

- feat: tweak mount path
- feat: tweak mount path
- feat: tweak mount path

## 2022-03-30

- fix: fixes env vars
- fix: adds necessary package data

## 2022-03-29

- feat: adds postgres startup scripts to ensure dbs and users are created

## 2022-03-17

- feat: finishes airflow and ms-transform implementation

## 2022-03-15

- feat: updates to new image and config
- fix: fixes datadog config
- feat: adds ns env file

## 2022-03-07

- feat: adds build commands

## 2022-03-04

- feat: runs jupyter server as root to alleviate some permissions issues
- feat: makes airflow dags persistent

## 2022-03-03

- fix: fixes volume generation
- feat: add all dotfiles
- feat: adds env files
- fix: fixes package data paths
- fix: fixes package data to include other resources
- feat: adds superset to edge

## 2022-03-02

- feat: nest airflow folders under transform folder
- feat: adds full transform service to edge

## 2022-03-01

- feat: adds airflow support

## 2022-02-23

- feat: adds project mount

## 2022-02-15

- fix: adds repo version back in
- fix: adds hostname and removes unused variable

## 2022-02-04

- feat: streamlines folder creation

## 2022-02-02

- feat: moves image versions to env vars

## 2022-01-27

- feat: bumps project version

## 2022-01-12

- feat: bumps local project version
- feat: bumps local projects version

## 2021-12-20

- fix: disables xray globally
- fix: fixes datadog issues

## 2021-12-07

- feat: adds hmd_home env var to proxy for future local ns checks

## 2021-12-03

- fix: bump project service version

## 2021-12-01

- feat: bumping image versions to latest

## 2021-11-29

- feat: updates project and postgres image versions

## 2021-11-10

- fix: fixes docker-compose reference
- feat: tuning order of operations
- feat: adds support for local repos not existing
- feat: initial commit of cli

## 2021-11-09

- feat: :tada: generate initial repo structure
