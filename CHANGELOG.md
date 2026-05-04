# Changelog

All notable changes to this project will be documented in this file.

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
