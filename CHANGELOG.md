# Changelog

All notable changes to this project will be documented in this file.

## 2026-07-21

- refactor: replace the strategy-based `local_overrides.json` mechanism with a two-phase BOM/changeset bootstrap for the local control plane. `hmd neuronsphere up` now applies **Phase A** (just the core producer instance, so its Resources — Docker network, k3s cluster/compute/ingress-controller, shared Postgres, JanusGraph, and a `microservice` Resource per bootstrapped-before-ms-deployment-exists HMDMS Lambda) submit before **Phase B** (ext-secrets, built-in/custom BOM, plugin-contributed entries) validates its changeset — letting Phase-B dependencies use a real `tag_selector` against a specific producer instead of accepting any producer of the shared type. Deletes `hmdms_seeder.py` and `local_overrides.json`; the removed per-instance `strategy`/`skip` bookkeeping is superseded by the phased changeset split.
- refactor: rename the core local producer instance `local-k3s` → `local-neuronsphere`, and give it a hashed, per-`HMD_HOME` name so multiple local environments on the same host no longer collide on a single shared instance identity.
- feat: detect k3s cluster recreation (vs. a plain restart) and re-run the affected bootstrap phase instead of assuming an existing environment is still valid.
- feat: add new resource-definition types backing the expanded Phase A core-producer output (see `docs/modes.rst`).
- Updated `bom_seeder.py`, `floci_deployer.py`, `hmd_cli_neuronsphere.py`, `k3s_operators.py`, `local_workflow_runner.py`, and `docs/modes.rst`/`docs/helm_chart_dev_loop.rst` accordingly; test coverage updated in `test_bom_seeder.py`/`test_k3s_operators.py`/`03__extend_mode_tests.robot`.

## 2026-07-17

- feat: enable the External Secrets local dev-deploy loop (`hmd-inf-ext-secrets-crds` + `hmd-inf-ext-secrets`, deployed through the real ms-deployment DAG) by default, per the "intended to move into the default bootstrap once proven" note in `docs/modes.rst`. `HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS` flips from opt-in to opt-out (`=false`/`0`/`no` disables it) — added `bom_seeder._is_falsy` for the new default-on/opt-out check, mirroring the pattern `k3s_operators._enabled()` already uses for `HMD_LOCAL_NEURONSPHERE_ENABLE_K3S_OPERATORS`. Updated `local_overrides.json` reasons and `docs/modes.rst` accordingly.
- refactor: remove KEDA from the hardcoded `k3s_operators._OPERATORS` core-operator install path and the `hmd-inf-keda` `pre_build_artifacts` bundle. KEDA now deploys through the real ms-deployment DAG as a BOM entry contributed by the optional `hmd-cli-plugin-ns-telemetry` package (mirroring the earlier ClickHouse-operator/cert-manager migration), so it's only installed when that plugin is present instead of unconditionally. `local_overrides.json`'s `hmd-inf-keda` entry switches from `strategy: "skip"` to `"default"` to let the DAG deploy it.

- feat: harvest the developer host's Docker registry credentials into the local `ext-secrets` BOM entry, so local k3s can pull private images (e.g. `ghcr.io/hmdlabs/*`) the same way cloud does, instead of hitting `ErrImagePull` (401, anonymous pull). New `docker_credentials.local_docker_config_json()` reads `~/.docker/config.json` (or `$DOCKER_CONFIG`), resolving each registry host's credentials from either a literal `auths[host].auth` entry or its credential helper (`credHelpers[host]` / the top-level `credsStore` — the common case on macOS/Docker Desktop), covering every registry the developer is already authenticated to, not just `ghcr.io`. `bom_seeder._inject_docker_credentials` threads the result into any `hmd-inf-ext-secrets` BOM entry's `instance_configuration.docker_config_json`, consumed by that repo's local CDKTF overlay to seed `hmd-docker-repo-secret`. Best-effort throughout — a missing Docker config or failed helper lookup for one registry never blocks the rest of `up`.
- feat: enable the `dockerRepoSecret` `ClusterExternalSecret` locally in `k3s_operators._ext_secrets_passes()`'s second pass, pointed at a new `aws-parameter-store` `ClusterSecretStore` (added alongside the existing `aws-secrets-manager` one) — this is the same mechanism cloud already uses to sync `hmd-docker-repo-secret` into every namespace; it was simply switched off for local. Parameter Store specifically because `hmd-inf-credentials`' local CDKTF overlay writes secret values there (via `hmd_lib_secrets_backend`), not Secrets Manager.
- fix: reap `NotReady` ghost k3s Node registrations and their node-affine local-path PVs on every `hmd neuronsphere up` (`k3s_operators._clean_stale_nodes`, called from `provision_k3s_operators`). Floci reuses the k3s data volume across cluster delete/recreate, so a respawned container — whose hostname defaults to a fresh, random container ID — registers as a brand-new Node while the previous one lingers forever as `NotReady`. Those ghosts broke `EndpointSlice` reconciliation for every Service (`FailedToUpdateEndpointSlices ... Node <id> Not Found`) and pinned PVCs to a dead node (`didn't match PersistentVolume's node affinity`), which was silently corrupting ClickHouse's StatefulSet across restarts.
- fix: also drop the k3s container's persistent `/var/lib/rancher/k3s` volume in `floci_deployer._wait_for_cluster_gone` when force-recreating a stale cluster, so a full recreate doesn't carry forward the same accumulating ghost-node garbage `_clean_stale_nodes` has to clean up.
- fix: label the single local k3s node with `topology.kubernetes.io/{zone,region}=local` (`k3s_operators._ensure_node_topology_labels`, called from `provision_k3s_operators`). Cloud EKS nodes carry these labels; the bare local node had none, so any chart's zone-keyed `topologySpreadConstraints` (e.g. ClickHouse's Keeper StatefulSet) found "0/1 nodes match" and left every replica beyond the first stuck `Pending` forever. A single zone value trivially satisfies max-skew for any single-node cluster.

## 2026-07-16

- feat: deploy newly-available plugin BOM entries on `hmd neuronsphere up --upgrade` without a purge. Since `d08bc16`/`802ee24` moved ClickHouse/OTEL-collector out of this repo into the optional `hmd-cli-plugin-ns-telemetry` package (contributed via the `hmd_cli_neuronsphere.get_local_bom_entries` entry point), the restart fast-path had no way to notice a plugin installed, or an `HMD_LOCAL_NEURONSPHERE_ENABLE_*` flag flipped on, after an environment was already bootstrapped — the only way to pick it up was a destructive `down --purge` + `up`. `bom_seeder.compute_new_bom_entries` now diffs the resolved BOM against the RepoInstances already registered in ms-deployment; on a restart, the fast-path calls it and, under `--upgrade`, applies a delta-scoped changeset + DAG run for just the new instances via `seed_bom(bom=new_entries)` — already-deployed instances (core, ext-secrets) are left untouched, since the server only emits deploy scripts for instances attached to the changeset just applied. A plain `up` restart stays fast and only prints a one-line hint when new entries are detected.
- fix: make `seed_bom` safe to call more than once against the same environment, which the delta-apply above depends on. The `deployment_set` "local" write is now find-first (matching `ensure_local_environment`'s existing pattern) instead of an unconditional PUT, and the changeset name is minted uniquely per invocation (`_new_change_set_name`) instead of the hardcoded `"local-changeset"` — a second call previously created a duplicate `local-changeset` row, which made the server's `apply_changeset` assert `Found 2` and crash.
- fix: `compute_new_bom_entries` no longer treats a "skip"-strategy instance (e.g. the core `local-k3s` producer) as permanently retry-eligible just because it's stuck at ms-deployment's un-visited default status ("SKIPPED"). `LocalWorkflowRunner` never actually writes literal "SKIPPED" — it only writes `DEPLOYED`/`FAILED`; "SKIPPED" is what an instance shows when a fail-fast run never reached it. A "default"-strategy instance stuck there genuinely needs a retry, but a "skip"-strategy one (never runs a real script, marks itself `DEPLOYED` instantly if ever visited) does not — the delta computation now consults `local_overrides.json`'s strategy (mirroring `local_workflow_runner.SKIP_STRATEGIES`) so it stops re-offering already-terminal no-op instances on every future `--upgrade`.
- fix: mount a rewritten kubeconfig into the projectbuilder container instead of the raw one. `write_kubeconfig` always produces a host-published `https://localhost:<port>` server (needed for host-side `kubectl`), which is unreachable from inside a container on the `neuronsphere_default` network — Helm-strategy deploy nodes for repos with no `kubernetes-cluster` resource dependency (e.g. `hmd-inf-clickhouse-operator`, which has none) got `Connection refused`. `LocalWorkflowRunner._kubeconfig_for_container` now rewrites the mounted copy's `server` to the in-network Floci EKS alias (`https://floci-eks-<cluster>:6443`) unconditionally for every node, rather than relying on hmd-cli-helm's own NERD0006 dependency-gated substitution (which only fires for repos that happen to declare a `kubernetes-cluster` resource dependency).
- feat: wire the new `hmd-inf-cert-manager` (cluster-wide, deployed once per cluster) into `local_overrides.json` as `strategy: "default"`, so `hmd neuronsphere up` deploys it locally the same way it deploys `hmd-inf-clickhouse-operator` and other Helm-only cluster add-ons.

## 2026-07-15

- feat: install Traefik as the local k3s ingress controller during operator provisioning. This NeuronSphere k3s image ships no bundled ingress controller (no `traefik.yaml` addon, no IngressClass), so the seeded `kubernetes.neuronsphere.io/ingress-controller` Resource was aspirational and Ingress objects went unserved. `k3s_operators._ensure_ingress_controller` now `helm upgrade --install`s Traefik into `kube-system` (public chart, pinned version; overridable via `HMD_LOCAL_TRAEFIK_REPO`/`_VERSION`, gated by `HMD_LOCAL_NEURONSPHERE_ENABLE_INGRESS`), binding the node's `:80`/`:443` via `hostPort` so it is reachable at the k3s node container's name on the Floci docker network — no servicelb needed. Best-effort and idempotent; runs on every `hmd neuronsphere up`.
- feat: register `hmd_proxy` and `hmd_db` in the k3s `coredns-custom` config so pods resolve them by name. `k3s_operators._ensure_coredns_floci_entry` now emits per-host server blocks for the core docker-network services alongside `neuronsphere`/`neuronsphere-workload`, so charts can use stable hostnames (e.g. `http://hmd_proxy/…`, DB host `hmd_db`) instead of ephemeral container IPs baked into config.
- feat: idempotently re-sync bootstrapped core Resources on restart, plus a `hmd neuronsphere up --upgrade` flag. Once an env is bootstrapped, `up` takes the restart fast-path and skips BOM seeding — so it never re-submitted the core NERD Resources, and a changed core Resource set (e.g. the new Traefik ingress-controller) could only be applied via a destructive `down --purge`. `bom_seeder.resync_local_resources` now re-runs only the idempotent subset — `seed_base_resource_definitions`, `declare_core_produces`, and `build_local_core_resources`/`submit_local_resources` against the **existing** `local-k3s` deployment (looked up by `find_core_deployment_node` via the `repo_instance` → `repo_instance_has_repo_instance_deployment` edge, avoiding a duplicate changeset). The restart fast-path calls it on every `up`, so restarts self-heal; `--upgrade` additionally repulls images via the existing `update_images()`. No DAG re-run, no redeploy.
- feat: advertise a local ingress controller as a core Resource. The `local-k3s` core instance now produces a `kubernetes.neuronsphere.io/ingress-controller` Resource (typed by the abstract base definition; `output` = `{name: traefik, namespace: kube-system, ingress_class: traefik}` — `name`/`namespace` satisfy the effective schema inherited from the `deployment` base type), backed by the Traefik that k3s already runs — no new repo or operator install. `bom_seeder.declare_core_produces` declares the fourth core type and `build_local_core_resources` submits a `<cluster>-traefik` Resource alongside the k3s cluster, so a cloud repo whose manifest declares a SPEC0008 `resource` dependency on an ingress-controller resolves against `local-k3s`. `hmd-inf-eks-alb` (the AWS Load Balancer Controller) is unchanged — it is AWS-only and cannot run against k3s.

## 2026-07-14

- feat: complete the local `hmd deploy` DAG loop for cdktf+helm repos onto k3s, with the cluster addressed via its Resource (cloud parity). The `local-k3s` `kubernetes-cluster` Resource now carries the **in-network** `endpoint` (`https://floci-eks-<cluster>:6443`), and core Resources are submitted **before** the DAG runs so a dependent's `hmd deploy` resolves that endpoint (NERD0006) — hmd-cli-helm then connects there instead of the host-scoped kubeconfig server. `LocalWorkflowRunner` also passes `HMD_HOME` and dummy `DOCKER_USERNAME`/`DOCKER_PASSWORD` (for `hmd cdktf deploy`'s credential pre-flight) into the projectbuilder container, and `floci_deployer.provision_resources` creates the CDKTF tfstate bucket (`hmd.<account>.<region>.tfstate`) so `tofu init` works locally (path-style backend lives in `hmd-lib-cdktf`).
- fix: reach the local Deployment Service from the projectbuilder container via the `hmd_proxy` nginx alias, not `neuronsphere` (which is the Floci alias on :4566). This lets the in-container `hmd deploy` resolve dependency resource outputs and submit produced Resources.
- fix: when the external-secrets stack is opted into the ms-deployment DAG, `provision_k3s_operators` skips the operators-path install of `ext-secrets`/`ext-secrets-crds` so the DAG is the sole installer (no CRD ownership collision).
- feat: run the local `hmd deploy` from a **local source** instead of the Artifact Librarian (absent locally). `LocalWorkflowRunner` now injects `--local` into each generated deploy node command (`_localize_deploy_script`) so hmd-cli-deploy deploys from the mounted `/workspace` (primary). Alternatively, set `HMD_LOCAL_DEPLOY_ARTIFACT_ROOT=<dir>` holding `<repo>_<ver>_build.zip` and the runner mounts it and points hmd-cli-deploy at it via `HMD_ARTIFACT_ROOT` (secondary). Fixes the `hmd deploy` DAG nodes failing on `HMD_ARTIFACT_LIBRARIAN_API_KEY`; uses the stock projectbuilder (no rebuild).
- feat: own all local default/core Resources under a single `hmd-cli-neuronsphere` RepoClass. The local core is now BOM entry #0 — a `local-k3s` instance of `hmd-cli-neuronsphere` (`skip` strategy in `local_overrides.json`) that `apply_changeset` creates as a proper environment producer. `bom_seeder.declare_core_produces` declares it produces `kubernetes.neuronsphere.io/kubernetes-cluster`, `compute.neuronsphere.io/compute-node`, and `network.neuronsphere.io/docker-network` before the changeset applies, so SPEC0008 resource-type dependencies resolve against it. `build_local_core_resources`/`submit_local_resources` now attach the concrete core Resources (docker-network, k3s cluster, compute pool) to that instance's RepoInstanceDeployment (looked up from the deployed nodes) instead of hand-creating bare instances.
- feat: opt-in local deploy of the external-secrets stack via `HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS`. When set, `bom_seeder` appends `hmd-inf-ext-secrets-crds` then `hmd-inf-ext-secrets` to the resolved BOM so `hmd neuronsphere up` deploys them through projectbuilder onto the local k3s cluster and tracks their produced Resources in `hmd-ms-deployment` (NERD0004/0006). Their manifests' `eks-cluster`/`compute` deps resolve against the local `local-k3s` producer.
- feat: register a BOM repo's declared ResourceDefinitions locally. `bom_seeder.upsert_repo_resource_definitions` reads each repo's `meta-data/resources/*.yaml` and upserts them (mirroring the cloud ArtifactMonitor) so a produced Resource can be typed at deploy time.
- feat: close the local resource-tracking loop (NERD0006). After a node deploys, `LocalWorkflowRunner` reads the Resources hmd-cli-helm renders under `meta-data/resources_output/` in the mounted workspace and POSTs them to the local Deployment Service against the node's RepoInstanceDeployment (`_submit_produced_resources`) — no projectbuilder rebuild required. It also passes a container-reachable `HMD_DEPLOYMENT_SERVICE_URL` (localhost→`neuronsphere`) and, opt-in via `HMD_LOCAL_INCONTAINER_RESOURCE_SUBMIT`, `HMD_REPO_INSTANCE_DEPLOYMENT_ID` for a local-aware in-container submit.

### Fixed
- fix: wire `FLOCI_SERVICES_EKS_DEFAULT_IMAGE` (the `hmd-img-k3s-floci` wrapper) into the admin/extend `floci` service (`docker-compose.admin.yml`). Without it Floci spawned stock `rancher/k3s:latest`, whose apiserver crash-loops on the `--storage-backend=sqlite3` / `etcd-servers=unix:///tmp/kine.sock` overrides Floci injects; the wrapper's entrypoint strips them so the local k3s cluster reaches ACTIVE.
- fix: override the projectbuilder container ENTRYPOINT (`hmd`) with `bash` in `LocalWorkflowRunner`, so a node's deploy script runs as a shell command instead of being parsed as `hmd bash -c ...` ("invalid choice: 'bash'").
- fix: pass `HMD_HOME` into the projectbuilder container (hmd-cli-* asserts it is set).
- fix: base64-encode the `collection` `definition` attribute when creating the `deployment_set` and `change_set` via CRUD PUT. hmd_ms_base types these as base64-encoded JSON strings; sending a native list is rejected 422 and a plain JSON string fails base64 decoding (500).

## 2026-07-13

- fix: self-heal a stale Floci k3s cluster in `ensure_k3s_cluster`. Floci pins the k3s node image into its **persistent** cluster record at creation time, so a cluster first created before `FLOCI_SERVICES_EKS_DEFAULT_IMAGE` pointed at the `hmd-img-k3s-floci` wrapper (or against any stale image) keeps respawning `rancher/k3s:latest` and crash-loops on `--kube-apiserver-arg=storage-backend=sqlite3` (`--storage-backend invalid, allowed values: etcd3`). `create_cluster`'s `ResourceInUseException` was blindly trusted, so the cluster never recovered. The deployer now inspects the spawned `floci-eks-<name>` container and, when it is missing, stopped, or not the expected wrapper image, deletes and recreates the cluster so Floci respawns it from the current wrapper image (`_k3s_container_image`/`_k3s_container_running`/`_wait_for_cluster_gone` helpers; unit tests in `tests/test_k3s_self_heal.py`).
- feat: make the default `hmd neuronsphere up` a **minimal core** — Docker network + Floci + core databases + k3s + the deployment control plane (`hmd-ms-deployment`, `hmd-ms-naming`, `hmd-ms-dbaccount`) + the graph database (Neptune/JanusGraph). Every other app/infra service (Airflow, Trino, Superset, transform, Jupyter, ClickHouse, Hive Metastore, telemetry, MinIO, DynamoDB-standalone) is now **opt-in, off by default** — enable per-user with `HMD_LOCAL_NEURONSPHERE_ENABLE_<NAME>=true` or `hmd neuronsphere configure`. No plugins were removed. A `CORE_PLUGINS` set (`floci`, `main`, `graph`) short-circuits `LocalPluginLoader.is_plugin_enabled`; graph is brought up in the core compose set (opt out with `HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH=false`).
- feat: on first bootstrap, submit the concrete locally-deployed **NERD0004 Resources** into `hmd-ms-deployment` — the Docker network (`network.neuronsphere.io/docker-network`) and the k3s cluster (`kubernetes.neuronsphere.io/kubernetes-cluster`), tagged `environment=local` — so cloud repos whose `manifest.json` declares a resource dependency resolve against the local environment (`bom_seeder.build_local_core_resources` / `submit_local_resources`). Pairs with `seed_base_resource_definitions`, which seeds the base catalog before the BOM.
- refactor: `hmd neuronsphere configure` shows the always-on local core separately and only prompts for the optional plugins (which now default off).

## 2026-07-02

- feat: framework to run bundled Helm-chart plugins on the Floci k3s cluster instead of as Docker Compose containers (`k3s_chart_plugins.py`), opt-in via `HMD_LOCAL_NEURONSPHERE_K3S_CHARTS` and only when `hmd_cli_helm` is available. `provision_k3s_chart_plugins` (run in `up` after the operators) seeds each chart's Floci fixtures (S3 buckets + Secrets Manager secrets in region `local`) and deploys it via `hmd helm deploy --local`; the compose render loop suppresses the container for any converted plugin. Includes robustness for the persistent Floci k3s datastore: delete `NotReady` ghost nodes + node-affine orphaned PVs before install (`k3s_operators._clean_stale_nodes`), and a surgical pre-delete of operator-owned CRs (ExternalSecret/ScaledObject) before re-deploy to avoid helm-4 server-side-apply field conflicts.
- feat: install NeuronSphere cluster operators onto the local Floci k3s cluster at `hmd neuronsphere up` time (`k3s_operators.provision_k3s_operators`), so chart repos deployed with `hmd helm deploy --local` render and apply their `ExternalSecret`/`ScaledObject`/ClickHouse operator resources with cloud parity. The operators are the same `hmd-inf-*` charts used in the cloud — `hmd-inf-ext-secrets-crds`, `hmd-inf-ext-secrets`, `hmd-inf-clickhouse-operator`, `hmd-inf-keda` — bundled as `pre_build_artifacts` and installed in dependency order (best-effort, gated by `HMD_LOCAL_NEURONSPHERE_ENABLE_K3S_OPERATORS`). The External Secrets `aws-secrets-manager` ClusterSecretStore is pointed at Floci Secrets Manager so `ExternalSecret`s sync for real.
- refactor: `local_overrides.json` now records that the External Secrets / KEDA / ClickHouse operators are provided by the up-time operator step rather than the extend-mode DAG.
- fix: the Floci k3s cluster now tracks the cloud EKS Kubernetes version (default `1.34` via `HMD_LOCAL_K3S_VERSION`) instead of a hardcoded `1.29`. Operator CRDs target the cloud API (the External Secrets CRDs use `selectableFields`, requiring k8s >= 1.30) and would not install on the old cluster. Requires the matching `hmd-img-k3s-floci` wrapper image.
- feat: `provision_k3s_operators` installs a `coredns-custom` record so k3s pods resolve `neuronsphere`/`neuronsphere-workload` to the Floci container IP. Operators and workload charts then reach Floci via the same in-network hostname as the cloud (`http://neuronsphere:4566`) — no per-chart endpoint rewriting.
- fix: harden operator provisioning against cluster-warmup and stale state — wait for a k3s node to report `Ready` before installing (EKS-API ACTIVE != node ready), and wait out any `Terminating` target namespace (Floci's k3s datastore persists across cluster delete/recreate, so a prior teardown can leave a namespace mid-termination).
- test: document the local Helm chart dev loop (`docs/helm_chart_dev_loop.rst`).

## 2026-04-30

- fix: rebrand the in-network Floci hostname from `floci`/`floci-workload` to `neuronsphere`/`neuronsphere-workload`. The new names are registered as Docker network aliases on the compose services so in-network DNS resolves them automatically. Lambdas (`AWS_ENDPOINT_URL`), the projectbuilder workflow runner, the API-gateway invoke URL, and the nginx upstream now use the rebranded names. Resolves the host-side DNS failure on the S3 PUT step of `push-artifact`/publish flows by having presigned URLs use a single hostname that's resolvable from both the host (via a one-time `/etc/hosts` entry) and from inside Floci's network (via Docker network aliases) - so transform-manager and other in-network consumers of presigned URLs keep working.
- feat: `hmd neuronsphere up` now runs a pre-flight check that verifies `neuronsphere`/`neuronsphere-workload` resolve to a loopback address on the host, and prints a clear one-line setup instruction (`sudo sh -c 'echo "127.0.0.1 neuronsphere neuronsphere-workload" >> /etc/hosts'`) when missing. Replaces the previous failure mode where missing hostname resolution would surface deep in the build/publish flow as a confusing DNS error.

## 2026-04-29

- fix: `LocalPluginLoader` now resolves gremlin engine configs to local `global-graph` container values (`db_host=global-graph`, `db_protocol=ws`, `with_strategies=False`) for any HMDMS plugin, mirroring `hmd-lib-cdktf-factories`' cloud-deploy `dependency:neptune-db` resolution. Resolves `aiohttp.client_exceptions.InvalidUrlClientError: wss://dependency:neptune-db:8182/gremlin` from `push-artifact` against the artifact-librarian Lambda. `setdefault` preserves any explicit override in the plugin's `meta-data/config_local.json`.
- fix: stop pre-creating DynamoDB tables for HMDMS plugins in `floci_deployer.provision_resources`; `hmd-entity-storage`'s `DynamoDbEngine` now creates the table on first service invocation with the correct attributes, key schema, and GSIs (`FromIndex`, `ToIndex`, `EntityNameIndex`). Resolves `ValidationException: The table does not have the specified index: EntityNameIndex` from `push-artifact` and other entity-storage queries. Users with an existing local stack should drop the broken table from Floci's LocalStack endpoint before re-running `hmd neuronsphere up`:

  ```
  aws --endpoint-url http://localhost:4566 dynamodb list-tables
  aws --endpoint-url http://localhost:4566 dynamodb delete-table --table-name <table-from-list>
  ```

## 2026-04-28

- feat: add `hmd neuronsphere push-artifact` to register a local repo build artifact in the local artifact librarian, with auto-build (`HMD_BUILD_OUTPUT_DIR` capture, no Docker/PyPI publish) and pre-built (`--build-path`) modes
- test: round-trip + help test for push-artifact in `06__artifact_lib_tests.robot`
- fix: `LocalPluginLoader.is_plugin_enabled` now honors `enabled_by_default` and `env_var_override` from nsplugin.json so discovered plugins (e.g. `artifact-lib`) start and register without requiring an explicit env var
- fix: foundation-load `hmd-ms-artifact-lib` from `HMD_REPO_HOME` during `hmd neuronsphere up` so `push-artifact`/`pull-artifact` no longer fail with `"no route defined"` when the user hasn't set `HMD_LOCAL_PLUGINS`
- fix: append a trailing slash to the default and user-supplied `--local-url` for `push-artifact`/`pull-artifact` so `urljoin` no longer strips the `/hmd_ms_artifact_lib` path segment when constructing `apiop/*` requests
- fix: `LocalPluginLoader` now auto-populates `dynamo_table` for any dynamo engine in an HMDMS plugin's `service_config` using `make_standard_name(function_name, repo_name, did, "local", region, customer)`, mirroring `ServiceCdkTfStack`'s cloud-deploy behavior, and emits a matching `dynamodb_tables` resource so Floci provisions the table at startup
- fix: `LocalPluginLoader` now injects `CONTENT_PATH_CONFIGS` and `GRAPH_QUERY_CONFIG` env vars on librarian-style HMDMS plugins from `manifest.deploy.default_configuration` (overridable via `config_local.json`), mirroring `LibrarianBase.get_lambda_vars` so artifact-lib boots locally without missing-config errors
- fix: `LocalPluginLoader` also injects `BUCKET_NAME` (bare bucket name, no `s3://` prefix) on librarian-style HMDMS plugins so `hmd_ms_librarian.get_service_parameter("BUCKET_NAME")` resolves locally; gated on `content_path_configs` presence and the first declared bucket, mirroring cloud's `LibrarianBase.get_full_bucket_name`

## 2026-04-22

- feat: replace MiniStack with Floci as local AWS emulator (NERD001 Phase 0)
- feat: add mode-switching infrastructure for legacy/deploy operating modes (SPEC008)
- refactor: rename ministack_deployer to floci_deployer with backwards-compatible env var fallback
- refactor: rename ministack plugin to floci plugin across entry points and tests

## 2026-03-10

- feat: add MiniStack integration replacing MinIO and DynamoDB plugins
- fix: skip port-in-use warnings for existing NeuronSphere containers
- fix: update version numbers for pre_build_artifacts in manifest.json
- feat: add enabled_by_default to nsplugin.json spec for plugin default state
- fix: update version numbers for airflow, clickhouse, and otel-collector in manifest

## 2026-02-24

- feat: add clean startup/shutdown output with service URL summary
- feat: add port conflict detection for local NeuronSphere startup

## 2026-02-23

- feat: add clickhouse and hive-metastore plugin wrappers with entry points
- feat: add superset pre-build artifact to manifest
- fix: correct hive-metastore external artifact path (underscore to hyphen)

## 2026-02-20

- feat: add telemetry profile seeding from local plugin nsplugin.json

## 2026-02-17

- feat: add pre-build artifacts for airflow, clickhouse, and otel-collector plugins
- fix: resolve network and db-init issues for local plugin containers
- fix: telemetry plugin now uses base.py helpers for local plugin support

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

### Added
- AI skills support with SkillsLoader for skill discovery and the `init-ns-local` skill that guides users through creating src/local/ directories for local NeuronSphere plugin development
- Support for external Docker Compose artifacts from other repos, allowing each service to manage its own local development configuration via pre-build artifacts with nsplugin.json schema and Jinja2 templating
- Local filesystem plugin support allowing plugins from HMD_REPO_HOME to override installed plugins when explicitly enabled via environment variables
- Handler registration with `hmd_cli.controllers` entry point for the new handler discovery mechanism
- `validate-plugin` command to validate nsplugin.json configuration files with checks for JSON syntax, required fields, Docker Compose validity, and file existence
- `init-plugin` command to scaffold src/local/ directory structure with template nsplugin.json and docker-compose files
- Pre-build artifact configuration for hmd-ms-transform
