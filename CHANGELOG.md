# Changelog

All notable changes to this project will be documented in this file.

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
