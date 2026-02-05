# Changelog

All notable changes to this project will be documented in this file.

## 2026-02-05

### Added
- AI skills support with SkillsLoader for skill discovery and the `init-ns-local` skill that guides users through creating src/local/ directories for local NeuronSphere plugin development
- Support for external Docker Compose artifacts from other repos, allowing each service to manage its own local development configuration via pre-build artifacts with nsplugin.json schema and Jinja2 templating
