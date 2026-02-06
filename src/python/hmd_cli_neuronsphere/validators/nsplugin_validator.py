"""
Validator for nsplugin.json local plugin configuration files.

Validates:
- JSON syntax
- Required fields
- Field types
- Referenced files existence
- Docker Compose file validity
"""

import json
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional

import yaml


@dataclass
class ValidationResult:
    """Result of validating an nsplugin.json file."""

    valid: bool
    errors: List[str] = field(default_factory=list)
    warnings: List[str] = field(default_factory=list)

    def add_error(self, message: str) -> None:
        self.errors.append(message)
        self.valid = False

    def add_warning(self, message: str) -> None:
        self.warnings.append(message)


# Required fields in nsplugin.json
REQUIRED_FIELDS = ["plugin_name", "compose_file"]

# Optional fields with expected types
OPTIONAL_FIELDS = {
    "resources": dict,
    "required_dirs": list,
    "config_mappings": list,
    "templates": list,
    "postgres_scripts": list,
    "minio_scripts": list,
    "dynamodb_scripts": list,
    "dependencies": dict,
    "env_var_override": str,
    "config": dict,
}

# Resource field types
RESOURCE_FIELDS = {
    "services": list,
    "databases": list,
    "endpoints": list,
    "buckets": list,
}


def validate_nsplugin(path: Path, check_files: bool = True) -> ValidationResult:
    """
    Validate an nsplugin.json file.

    Args:
        path: Path to the nsplugin.json file or directory containing it
        check_files: Whether to check that referenced files exist

    Returns:
        ValidationResult with errors and warnings
    """
    result = ValidationResult(valid=True)

    # Resolve path
    if path.is_dir():
        nsplugin_path = path / "nsplugin.json"
        if not nsplugin_path.exists():
            nsplugin_path = path / "src" / "local" / "nsplugin.json"
    else:
        nsplugin_path = path

    if not nsplugin_path.exists():
        result.add_error(f"File not found: {nsplugin_path}")
        return result

    local_dir = nsplugin_path.parent

    # Load and validate JSON
    try:
        with open(nsplugin_path, "r") as f:
            config = json.load(f)
    except json.JSONDecodeError as e:
        result.add_error(f"Invalid JSON: {e}")
        return result

    # Validate required fields
    for field_name in REQUIRED_FIELDS:
        if field_name not in config:
            result.add_error(f"Missing required field: {field_name}")

    if not result.valid:
        return result

    # Validate plugin_name
    plugin_name = config.get("plugin_name", "")
    if not isinstance(plugin_name, str) or not plugin_name:
        result.add_error("plugin_name must be a non-empty string")
    elif not plugin_name.replace("_", "").replace("-", "").isalnum():
        result.add_warning(f"plugin_name '{plugin_name}' contains special characters")

    # Validate compose_file
    compose_file = config.get("compose_file", "")
    if not isinstance(compose_file, str) or not compose_file:
        result.add_error("compose_file must be a non-empty string")
    elif check_files:
        compose_path = local_dir / compose_file
        if not compose_path.exists():
            result.add_error(f"Compose file not found: {compose_path}")
        else:
            _validate_compose_file(compose_path, result)

    # Validate optional fields
    for field_name, expected_type in OPTIONAL_FIELDS.items():
        if field_name in config:
            value = config[field_name]
            if not isinstance(value, expected_type):
                result.add_error(
                    f"Field '{field_name}' must be {expected_type.__name__}, "
                    f"got {type(value).__name__}"
                )

    # Validate resources
    if "resources" in config:
        _validate_resources(config["resources"], result)

    # Validate config_mappings
    if "config_mappings" in config and check_files:
        _validate_config_mappings(config["config_mappings"], local_dir, result)

    # Validate templates
    if "templates" in config and check_files:
        _validate_templates(config["templates"], local_dir, result)

    # Validate postgres_scripts
    if "postgres_scripts" in config and check_files:
        _validate_scripts(
            config["postgres_scripts"], local_dir, "postgres_scripts", result
        )

    # Validate minio_scripts
    if "minio_scripts" in config and check_files:
        _validate_scripts(config["minio_scripts"], local_dir, "minio_scripts", result)

    # Validate dynamodb_scripts
    if "dynamodb_scripts" in config and check_files:
        _validate_scripts(
            config["dynamodb_scripts"], local_dir, "dynamodb_scripts", result
        )

    # Validate dependencies
    if "dependencies" in config:
        _validate_dependencies(config["dependencies"], result)

    # Validate env_var_override
    if "env_var_override" in config:
        env_var = config["env_var_override"]
        if not env_var.startswith("HMD_LOCAL_NEURONSPHERE_ENABLE_"):
            result.add_warning(
                f"env_var_override should start with "
                f"'HMD_LOCAL_NEURONSPHERE_ENABLE_', got '{env_var}'"
            )

    # Validate config section
    if "config" in config:
        _validate_config_section(config["config"], result)

    return result


def _validate_compose_file(path: Path, result: ValidationResult) -> None:
    """Validate a Docker Compose file."""
    try:
        with open(path, "r") as f:
            compose = yaml.safe_load(f)

        if not isinstance(compose, dict):
            result.add_error(f"Compose file {path.name} is not a valid YAML dict")
            return

        if "services" not in compose:
            result.add_warning(f"Compose file {path.name} has no 'services' section")

    except yaml.YAMLError as e:
        result.add_error(f"Invalid YAML in {path.name}: {e}")


def _validate_resources(resources: Dict[str, Any], result: ValidationResult) -> None:
    """Validate resources section."""
    for field_name, expected_type in RESOURCE_FIELDS.items():
        if field_name in resources:
            value = resources[field_name]
            if not isinstance(value, expected_type):
                result.add_error(
                    f"resources.{field_name} must be {expected_type.__name__}"
                )

    # Validate service entries
    for svc in resources.get("services", []):
        if isinstance(svc, dict):
            if "name" not in svc:
                result.add_warning("Service entry missing 'name' field")
            if "url" not in svc:
                result.add_warning("Service entry missing 'url' field")

    # Validate database entries
    for db in resources.get("databases", []):
        if isinstance(db, dict):
            for req_field in ["username", "password", "database"]:
                if req_field not in db:
                    result.add_warning(f"Database entry missing '{req_field}' field")


def _validate_config_mappings(
    mappings: List[Dict], local_dir: Path, result: ValidationResult
) -> None:
    """Validate config_mappings entries."""
    for i, mapping in enumerate(mappings):
        if not isinstance(mapping, dict):
            result.add_error(f"config_mappings[{i}] must be a dict")
            continue

        if "source" not in mapping:
            result.add_error(f"config_mappings[{i}] missing 'source' field")
        else:
            source_path = local_dir / mapping["source"]
            if not source_path.exists():
                result.add_error(
                    f"config_mappings[{i}] source not found: {mapping['source']}"
                )

        if "dest" not in mapping:
            result.add_error(f"config_mappings[{i}] missing 'dest' field")


def _validate_templates(
    templates: List[Dict], local_dir: Path, result: ValidationResult
) -> None:
    """Validate templates entries."""
    templates_dir = local_dir / "templates"

    for i, template in enumerate(templates):
        if not isinstance(template, dict):
            result.add_error(f"templates[{i}] must be a dict")
            continue

        if "source" not in template:
            result.add_error(f"templates[{i}] missing 'source' field")
        else:
            source = template["source"]
            # Handle both full path and just filename
            if "/" in source:
                source = os.path.basename(source)
            template_path = templates_dir / source
            if not template_path.exists():
                result.add_error(f"templates[{i}] source not found: {source}")

        if "dest" not in template:
            result.add_error(f"templates[{i}] missing 'dest' field")


def _validate_scripts(
    scripts: List[str], local_dir: Path, field_name: str, result: ValidationResult
) -> None:
    """Validate script entries (postgres_scripts, minio_scripts, dynamodb_scripts)."""
    for i, script in enumerate(scripts):
        if not isinstance(script, str):
            result.add_error(f"{field_name}[{i}] must be a string")
            continue

        script_path = local_dir / script
        if not script_path.exists():
            result.add_error(f"{field_name}[{i}] not found: {script}")


def _validate_dependencies(deps: Dict[str, Any], result: ValidationResult) -> None:
    """Validate dependencies section."""
    if "requires_plugins" in deps:
        if not isinstance(deps["requires_plugins"], list):
            result.add_error("dependencies.requires_plugins must be a list")

    if "requires_services" in deps:
        if not isinstance(deps["requires_services"], list):
            result.add_error("dependencies.requires_services must be a list")


def _validate_config_section(
    config_section: Dict[str, Any], result: ValidationResult
) -> None:
    """Validate config section for configurable environment variables.

    The config section defines configurable values that can be overridden
    via meta-data/config_local.json. Each entry can be:
    - A simple value (used as default)
    - A dict with: default, env_var, type

    Example:
        "config": {
            "SERVICE_CONFIG": {
                "default": {},
                "env_var": "SERVICE_CONFIG",
                "type": "json"
            },
            "LOG_LEVEL": {
                "default": "INFO",
                "env_var": "MY_SERVICE_LOG_LEVEL",
                "type": "string"
            }
        }
    """
    valid_types = ["string", "json", "int", "bool"]

    for key, schema in config_section.items():
        if not isinstance(key, str):
            result.add_error(f"config key must be a string, got {type(key).__name__}")
            continue

        if isinstance(schema, dict):
            # Validate schema structure
            if "type" in schema:
                if schema["type"] not in valid_types:
                    result.add_warning(
                        f"config.{key}.type '{schema['type']}' not in {valid_types}"
                    )

            if "env_var" in schema:
                if not isinstance(schema["env_var"], str):
                    result.add_error(f"config.{key}.env_var must be a string")
        # Simple values are allowed as defaults
