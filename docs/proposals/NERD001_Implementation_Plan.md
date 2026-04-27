# NERD001 Floci Local Architecture — Implementation Plan

**Status**: IN PROGRESS (Phase 0 done, Phase 1A–1F code complete pending live validation)  
**Created**: 2026-04-22  
**Last Updated**: 2026-04-27  
**Architecture**: See `NERD001_Floci_Local_Architecture.rst`

## Summary

NERD001 provides two operating modes for local NeuronSphere:

- **Platform mode** (default): Cloud-parity user experience. Services as Floci Lambdas, Argo on k3s for `image_sequence` transforms, Airflow for `provider`/`dbt`. End users deploy transforms and services through the same APIs as cloud.
- **Extend mode**: Adds `ms-deployment` for NeuronSphere engineers who need `DeploymentConfig` to validate CDKTF/Helm infrastructure code.

---

## Phase 0: Floci Drop-In Replacement — DONE

| Task | Repo | Status |
|------|------|--------|
| Replace MiniStack image with Floci in Docker Compose | hmd-cli-neuronsphere | DONE |
| Rename `ministack_deployer.py` to `floci_deployer.py` | hmd-cli-neuronsphere | DONE |
| Update health check endpoint | hmd-cli-neuronsphere | DONE |
| Add `HMD_LOCAL_NEURONSPHERE_ENABLE_FLOCI` env var with backwards compat | hmd-cli-neuronsphere | DONE |
| Test S3, DynamoDB, SQS, Lambda, API Gateway on Floci | hmd-cli-neuronsphere | DONE |

---

## Phase 1: Platform Mode + Argo nsplugin

### 1A: Mode Renaming

| Task | Repo | Status |
|------|------|--------|
| Rename `legacy` to `platform` in mode switching (backwards compat) | hmd-cli-neuronsphere | DONE |
| Rename `deploy` to `extend` in mode switching (backwards compat) | hmd-cli-neuronsphere | DONE |
| Rename `start_neuronsphere_legacy()` to `start_neuronsphere_platform()` | hmd-cli-neuronsphere | DONE |
| Rename `start_neuronsphere_deploy()` to `start_neuronsphere_extend()` | hmd-cli-neuronsphere | DONE |
| Update help text and documentation | hmd-cli-neuronsphere | DONE |

### 1B: k3s Cluster Auto-Creation

| Task | Repo | Status |
|------|------|--------|
| Add Floci EKS cluster creation to `hmd neuronsphere up` startup | hmd-cli-neuronsphere | DONE |
| Wait for k3s cluster readiness | hmd-cli-neuronsphere | DONE |
| Validate Floci EKS API for k3s cluster creation | hmd-cli-neuronsphere | DONE (live runtime check pending) |
| Add k3s teardown to `hmd neuronsphere down` | hmd-cli-neuronsphere | DONE |

### 1C: Argo nsplugin

| Task | Repo | Status |
|------|------|--------|
| Create `src/local/nsplugin.json` for Argo plugin | hmd-app-argo | DONE |
| Implement Argo Workflows installation on k3s (Helm or kubectl apply) | hmd-app-argo | DONE |
| Create namespace, service account, RBAC for ms-transform | hmd-app-argo | DONE |
| Register `argo-server` with ms-naming | hmd-app-argo | DONE |
| Expose Argo UI via nginx proxy at `/argo/` | hmd-cli-neuronsphere | DONE |
| Create `plugins/argo.py` wrapper in hmd-cli-neuronsphere (if needed) | hmd-cli-neuronsphere | DONE |

### 1D: Transform Service Argo Integration

| Task | Repo | Status |
|------|------|--------|
| Add `ARGO_HOST` env var override to `ArgoTransformEngine.py` | hmd-ms-transform | DONE (in `hmd_ms_transform.py:configure_tf_engines`) |
| Update `src/local/nsplugin.json` — add `argo` to `requires_plugins` | hmd-ms-transform | DONE |
| Add `TF_ENGINES` and `ARGO_HOST` to nsplugin config section | hmd-ms-transform | DONE |
| Validate engine selection: `image_sequence` → Argo, `provider`/`dbt` → Airflow | hmd-ms-transform | DONE (live runtime check pending) |
| Test Argo fallback when `HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO=false` | hmd-ms-transform | DONE (env var supported; live check pending) |

### 1E: Networking + Default Service Plugins

| Task | Repo | Status |
|------|------|--------|
| Add NodePort + nginx /argo/ proxy for Lambda → k3s networking | hmd-cli-neuronsphere | DONE |
| Validate Argo workflow container image access from k3s | hmd-cli-neuronsphere | TODO (live runtime check pending) |
| Add `hmd-ms-cluster/src/local/nsplugin.json` (default-on, mocked deploy) | hmd-ms-cluster | DONE |
| Add `hmd-ms-artifact-lib/src/local/nsplugin.json` (default-on) | hmd-ms-artifact-lib | DONE |
| Add `hmd neuronsphere pull-artifact` CLI command | hmd-cli-neuronsphere | DONE |

### 1F: Tests

| Task | Repo | Status |
|------|------|--------|
| Test: Platform mode starts k3s cluster | hmd-cli-neuronsphere | DONE (`test/05__argo_tests.robot`) |
| Test: Argo plugin installs Argo on k3s | hmd-cli-neuronsphere | DONE (`test/05__argo_tests.robot`) |
| Test: Argo UI accessible at `/argo/` | hmd-cli-neuronsphere | DONE (`test/05__argo_tests.robot`) |
| Test: artifact-lib Lambda + bucket | hmd-cli-neuronsphere | DONE (`test/06__artifact_lib_tests.robot`) |
| Test: pull-artifact CLI is registered | hmd-cli-neuronsphere | DONE (`test/06__artifact_lib_tests.robot`) |
| Test: ms-transform routes `image_sequence` to Argo (E2E) | hmd-cli-neuronsphere | DEFERRED to hmd-intg-transform suite |
| Test: ms-transform routes `provider` to Airflow (E2E) | hmd-cli-neuronsphere | DEFERRED to hmd-intg-transform suite |

---

## Phase 2: Extend Mode for Engineers

### 2A: ms-deployment as Floci Lambda (revised)

| Task | Repo | Status |
|------|------|--------|
| Deploy ms-deployment as Floci Lambda in **both** Platform and Extend modes | hmd-cli-neuronsphere | DONE |
| ms-deployment DB init lifted into `docker-compose.main.yml` | hmd-cli-neuronsphere | DONE |
| Add `HMD_LOCAL_NEURONSPHERE_DISABLE_MS_DEPLOYMENT` escape hatch | hmd-cli-neuronsphere | DONE |
| Retain seeding for HMDMS services + `hmd-inf-s3bucket` baseline (only the 28-repo platform BOM is removed) | hmd-cli-neuronsphere | DONE |
| Keep dual-Floci config (workload Floci still needed for `LocalWorkflowRunner`) | hmd-cli-neuronsphere | KEEP |
| Remove automatic DAG execution from platform startup | hmd-cli-neuronsphere | TODO |

### 2B: Engineer CLI Commands

| Task | Repo | Status |
|------|------|--------|
| Implement `hmd neuronsphere register-repo` | hmd-cli-neuronsphere | TODO |
| Implement `hmd neuronsphere get-config` | hmd-cli-neuronsphere | TODO |
| Implement `hmd neuronsphere deploy-repo` (single-repo via LocalWorkflowRunner) | hmd-cli-neuronsphere | TODO |

### 2D: HMDMS service plugin support

Lets users add HMDMS-based microservices (especially librarians) to local NeuronSphere by dropping `src/local/nsplugin.json` with an optional `hmdms_service` block. Plumbing is shared between Platform and Extend modes — see `/Users/aburg/.claude/plans/so-reconsider-how-both-agile-alpaca.md` for the full plan.

| Task | Repo | Status |
|------|------|--------|
| Extend nsplugin.json schema with `hmdms_service` block (validator) | hmd-cli-neuronsphere | DONE |
| `LocalPluginLoader` getters for HMDMS services + lambda spec derivation | hmd-cli-neuronsphere | DONE |
| Synthesize Lambda + S3 bucket registrations on startup (both modes) | hmd-cli-neuronsphere | DONE |
| Wire `*_BUCKET` env vars into transform plugin | hmd-cli-neuronsphere | DONE |
| `hmdms_seeder.py` — seed RepoClass + RepoInstance + Deployment with SKIPPED mocks for unmet deps | hmd-cli-neuronsphere | DONE |
| `hmd neuronsphere status` command (text + JSON) | hmd-cli-neuronsphere | DONE |
| Robot test suite `test/04__hmdms_service_tests.robot` | hmd-cli-neuronsphere | DONE |
| Document `hmdms_service` block in `init-ns-local` SKILL | hmd-cli-neuronsphere | DONE |
| Customer / artifact-pull flow (deferred — designed-for, not built) | hmd-cli-neuronsphere | DEFERRED |

### 2C: Validation

| Task | Repo | Status |
|------|------|--------|
| Test: Extend mode starts ms-deployment Lambda | hmd-cli-neuronsphere | TODO |
| Test: `register-repo` registers a RepoClass | hmd-cli-neuronsphere | TODO |
| Test: `get-config` returns a valid DeploymentConfig | hmd-cli-neuronsphere | TODO |
| Test: `deploy-repo` executes single repo in projectbuilder | hmd-cli-neuronsphere | TODO |
| Test with real RepoClass: hmd-inf-redis | hmd-cli-neuronsphere | TODO |
| Test with real RepoClass: hmd-ms-transform | hmd-cli-neuronsphere | TODO |

---

## Phase 3: CLI Tool Convergence

| Task | Repo | Status |
|------|------|--------|
| Add centralized boto3 session factory with Floci endpoint override | hmd-cli-tools | TODO |
| Migrate individual repos to use shared factory (incremental) | various | TODO |
| Add Floci-specific resource provisioning (Secrets Manager, IAM) | hmd-cli-tools | TODO |

---

## Components Deprecated from Previous Plan

These components were part of the original deploy mode design and are no longer
needed for platform startup:

| Component | Previous Role | New Role |
|-----------|--------------|----------|
| `local_overrides.json` (60+ entries) | Strategy overrides for all repos in DAG | Retained only as fallback for `deploy-repo`; not loaded at startup |
| Dual Floci instances (admin + workload) | Multi-account emulation | **Kept** — workload Floci still needed for `LocalWorkflowRunner` |
| Platform BOM (`platform_bom.json`) | Full 28-repo dependency snapshot | Eliminated; not needed when services start via Docker Compose |
| Automatic BOM seeding at startup | Seed deployment graph on every `up` | **Partially retained**: HMDMS services and `hmd-inf-s3bucket` baseline are auto-seeded in both modes; the 28-repo platform BOM is removed. |
| Full DAG execution at startup | Deploy all 28 repos via LocalWorkflowRunner | Eliminated; `deploy-repo` for single-repo on demand |
| `docker-compose.admin.yml` (full) | Admin control plane with dual Floci | Simplified to extend-mode overlay (ms-deployment DB init now in main.yml) |

---

## Execution Order

```
Phase 0 (DONE)
    |
    v
Phase 1A (mode rename) ──> Phase 1B (k3s) ──> Phase 1C (Argo plugin) ──> Phase 1D (transform) ──> Phase 1E/1F (validation/tests)
                                                                                                            |
                                                                                                            v
                                                                                                    Phase 2A (ms-deployment) ──> Phase 2B (CLI) ──> Phase 2C (validation)
                                                                                                                                                            |
                                                                                                                                                            v
                                                                                                                                                    Phase 3 (CLI convergence)
```

Phase 1 tasks are mostly sequential (each depends on the prior). Phase 2 depends on Phase 1 completion.
Phase 3 is independent and can proceed in parallel with Phase 2.
