# Changelog

All notable changes to this project will be documented in this file.

## 2026-02-05

### Added
- AI skills support with SkillsLoader for skill discovery and the `init-ns-local` skill that guides users through creating src/local/ directories for local NeuronSphere plugin development
- Support for external Docker Compose artifacts from other repos, allowing each service to manage its own local development configuration via pre-build artifacts with nsplugin.json schema and Jinja2 templating
- Local filesystem plugin support allowing plugins from HMD_REPO_HOME to override installed plugins when explicitly enabled via environment variables
- Handler registration with `hmd_cli.controllers` entry point for the new handler discovery mechanism
- `validate-plugin` command to validate nsplugin.json configuration files with checks for JSON syntax, required fields, Docker Compose validity, and file existence
- `init-plugin` command to scaffold src/local/ directory structure with template nsplugin.json and docker-compose files
- Pre-build artifact configuration for hmd-ms-transform
