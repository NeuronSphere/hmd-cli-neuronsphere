---
name: init-ns-local
description: Initialize src/local/ directory for local NeuronSphere development plugin support
version: "1.0"
author: HMD Labs
requires:
  - hmd-cli-neuronsphere
tags:
  - local-development
  - docker-compose
  - neuronsphere
  - plugin
---

# Initialize Local NeuronSphere Plugin

Convert an existing NeuronSphere repository to support local development by creating a `src/local/` directory with all necessary plugin configuration files.

## Overview

The local NeuronSphere plugin system allows services to define their own Docker Compose configurations and supporting files that integrate with the `hmd neuronsphere up` command. This skill guides you through creating the necessary structure.

## Instructions

### Step 1: Analyze the Repository

First, understand what type of repository you're working with:

1. **Read `meta-data/manifest.json`** to determine:
   - Repository name (`name` field)
   - Repository type (infer from prefix: `hmd-ms-*` = microservice, `hmd-inf-*` = infrastructure, etc.)
   - Existing build commands

2. **Check for existing Docker files**:
   - Look for `docker-compose*.yml` or `docker-compose*.yaml` files
   - Check `src/docker/` for Dockerfiles
   - Look for existing configuration files

3. **Identify the plugin name**:
   - For `hmd-ms-transform` → plugin name is `transform`
   - For `hmd-inf-trino` → plugin name is `trino`
   - Extract the meaningful suffix after the repo type prefix

### Step 2: Create Directory Structure

Create the `src/local/` directory with the following structure:

```
src/local/
    nsplugin.json              # Plugin configuration (required)
    docker-compose.<plugin>.yml # Docker Compose file (required)
    config/                    # Configuration files (optional)
    templates/                 # Jinja2 templates (optional)
    scripts/
        postgres/              # PostgreSQL init scripts (optional)
```

Commands to create structure:
```bash
mkdir -p src/local/config
mkdir -p src/local/templates
mkdir -p src/local/scripts/postgres
```

### Step 3: Create nsplugin.json

Create `src/local/nsplugin.json` with the following structure:

```json
{
  "plugin_name": "<plugin_name>",
  "compose_file": "docker-compose.<plugin_name>.yml",
  "resources": {
    "services": [],
    "databases": [],
    "endpoints": []
  },
  "required_dirs": [],
  "config_mappings": [],
  "templates": [],
  "postgres_scripts": [],
  "dependencies": {
    "requires_plugins": [],
    "requires_services": []
  },
  "env_var_override": "HMD_LOCAL_NEURONSPHERE_ENABLE_<PLUGIN_NAME_UPPER>"
}
```

#### Field Descriptions:

| Field | Description | Example |
|-------|-------------|---------|
| `plugin_name` | Short identifier for the plugin | `"transform"` |
| `compose_file` | Docker Compose filename | `"docker-compose.transform.yml"` |
| `resources.services` | Services this plugin provides | `[{"name": "ms-transform", "url": "http://hmd_gateway/hmd_ms_transform/"}]` |
| `resources.databases` | Databases this plugin needs | `[{"username": "hmd_ms_transform", "password": "hmd_ms_transform", "database": "hmd_ms_transform"}]` |
| `resources.endpoints` | Endpoints this plugin exposes | `["transform:localhost:8080"]` |
| `required_dirs` | Directories to create in HMD_HOME | `["transform", "transform/queries"]` |
| `config_mappings` | Files to copy to HMD_HOME | See below |
| `templates` | Jinja2 templates to render | See below |
| `postgres_scripts` | PostgreSQL init scripts | `["scripts/postgres/init.sh"]` |
| `dependencies.requires_plugins` | Other plugins that must be enabled | `["graph", "airflow"]` |
| `dependencies.requires_services` | Docker services that must be running | `["db", "graph-db"]` |
| `env_var_override` | Environment variable to enable/disable | `"HMD_LOCAL_NEURONSPHERE_ENABLE_TRANSFORM"` |

#### Config Mappings Format:

```json
"config_mappings": [
  {
    "source": "config/query_config.json",
    "dest": "transform/queries/query_config.json",
    "merge": true
  },
  {
    "source": "config/settings/",
    "dest": "transform/settings/",
    "if_empty": true
  }
]
```

Options:
- `merge`: For JSON files, merge with existing file instead of overwriting
- `if_empty`: Only copy if destination directory is empty

#### Templates Format:

```json
"templates": [
  {
    "source": "templates/catalog.properties.j2",
    "dest": "trino/config/catalog/plugin.properties",
    "variables": ["graph_host", "graph_port"]
  }
]
```

Templates receive context including:
- `resources`: Aggregated resources from all plugins
- `configs`: Plugin enabled/disabled states
- `env`: All HMD_* environment variables

### Step 4: Create Docker Compose File

Create `src/local/docker-compose.<plugin_name>.yml`:

```yaml
services:
  <service_name>:
    image: ${HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/neuronsphere}/<image_name>:${HMD_IMG_<NAME>_VERSION:-stable}
    container_name: <container_name>
    networks:
      - neuronsphere_default
    environment:
      HMD_CUSTOMER_CODE: ${HMD_CUSTOMER_CODE}
      HMD_DID: ${HMD_DID:-aaa}
      HMD_ENVIRONMENT: ${HMD_ENVIRONMENT:-local}
      HMD_REGION: ${HMD_REGION:-us-west-2}
      # Add service-specific environment variables
    volumes:
      - ${HMD_HOME}/data:/data
      # Add service-specific volumes
    depends_on:
      db:
        condition: service_healthy
      # Add other dependencies
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/health"]
      interval: 30s
      timeout: 10s
      retries: 5

networks:
  neuronsphere_default:
    external: true
```

#### Key Patterns:

1. **Container Registry**: Use `${HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/neuronsphere}`
2. **Image Version**: Use `${HMD_IMG_<NAME>_VERSION:-stable}`
3. **Network**: Always use `neuronsphere_default` external network
4. **HMD Context**: Include standard HMD environment variables
5. **Dependencies**: Use `depends_on` with health checks for reliability
6. **Volumes**: Mount from `${HMD_HOME}` for persistence

### Step 5: Add PostgreSQL Init Script (if needed)

If your service needs a PostgreSQL database, create `src/local/scripts/postgres/<db_name>.sh`:

```bash
#!/bin/bash
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE USER <username> WITH PASSWORD '<password>';
    CREATE DATABASE <database_name>;
    GRANT ALL PRIVILEGES ON DATABASE <database_name> TO <username>;
    \c <database_name>
    GRANT ALL ON SCHEMA public TO <username>;
EOSQL
```

### Step 6: Add Configuration Files

Copy any configuration files your service needs to `src/local/config/`:

- Configuration files that should be copied to HMD_HOME
- Default settings that users can override
- Templates for dynamic configuration

### Step 7: Validate Configuration

After creating all files, run the validation command:

```bash
hmd neuronsphere validate-plugin src/local/
```

This validates:
1. **JSON validity**: Ensures nsplugin.json is valid JSON
2. **Required fields**: Checks for `plugin_name` and `compose_file`
3. **YAML validity**: Validates the Docker Compose file syntax
4. **File existence**: Verifies all referenced config files, templates, and scripts exist
5. **Schema compliance**: Checks field types and structure

You can also validate without file existence checks:
```bash
hmd neuronsphere validate-plugin src/local/ --no-file-check
```

## Examples

### Microservice Example (hmd-ms-transform)

```json
{
  "plugin_name": "transform",
  "compose_file": "docker-compose.transform.yml",
  "resources": {
    "services": [
      {"name": "ms-transform", "url": "http://hmd_gateway/hmd_ms_transform/"}
    ],
    "databases": [
      {"username": "hmd_ms_transform", "password": "hmd_ms_transform", "database": "hmd_ms_transform"}
    ]
  },
  "required_dirs": ["transform", "transform/queries", "data/local_transforms", "queues"],
  "config_mappings": [
    {"source": "config/query_config.json", "dest": "transform/queries/query_config.json", "merge": true},
    {"source": "queues/", "dest": "queues/", "if_empty": true}
  ],
  "postgres_scripts": ["scripts/postgres/hmd_ms_transform.sh"],
  "dependencies": {
    "requires_plugins": ["graph", "airflow"],
    "requires_services": ["db", "graph-db", "airflow-webserver"]
  },
  "env_var_override": "HMD_LOCAL_NEURONSPHERE_ENABLE_TRANSFORM"
}
```

### Infrastructure Example (hmd-inf-trino)

```json
{
  "plugin_name": "trino",
  "compose_file": "docker-compose.trino.yml",
  "resources": {
    "endpoints": ["trino:localhost:8081"]
  },
  "required_dirs": [
    "trino/data",
    "trino/config",
    "trino/hadoop/dfs/name",
    "trino/hadoop/dfs/data",
    "hive/config",
    "hadoop/config",
    ".cache/hadoop",
    "warehouse"
  ],
  "config_mappings": [
    {"source": "config/trino/", "dest": "trino/config/", "if_empty": true},
    {"source": "config/hive/", "dest": "hive/config/", "if_empty": true},
    {"source": "config/hadoop/", "dest": "hadoop/config/", "if_empty": true},
    {"source": "config/hadoop-hive.env", "dest": ".cache/hadoop/hadoop-hive.env"}
  ],
  "postgres_scripts": ["scripts/postgres/metastore.sh"],
  "dependencies": {
    "requires_plugins": [],
    "requires_services": ["db"]
  },
  "env_var_override": "HMD_LOCAL_NEURONSPHERE_ENABLE_TRINO"
}
```

## Common Patterns

### Service with Queue Dependencies

If your service needs message queues, include them in the compose file:

```yaml
services:
  myservice:
    depends_on:
      queues:
        condition: service_healthy

  queues:
    image: softwaremill/elasticmq:1.4.2
    container_name: local_queues
    volumes:
      - ${HMD_HOME}/queues:/data
```

### Service with Redis

```yaml
services:
  myservice:
    depends_on:
      redis:
        condition: service_healthy

  redis:
    image: redis:7-alpine
    container_name: local_redis
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
```

## Checklist

Before completing, run the validation command and verify it passes:

```bash
hmd neuronsphere validate-plugin src/local/
```

The validation checks:
- [ ] `src/local/nsplugin.json` exists and is valid JSON
- [ ] `src/local/docker-compose.<plugin>.yml` exists and is valid YAML
- [ ] All config files referenced in `config_mappings` exist
- [ ] All postgres scripts referenced exist
- [ ] All template files referenced exist
- [ ] Dependencies are correctly documented
- [ ] Environment variable name follows convention: `HMD_LOCAL_NEURONSPHERE_ENABLE_<NAME>`
