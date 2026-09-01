# Changelog

## 2026-09-01

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
