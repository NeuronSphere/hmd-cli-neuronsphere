---
name: init-ns-local
description: Initialize src/local/ directory for local NeuronSphere development plugin support
version: "2.0"
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

The local NeuronSphere plugin system allows services to define their own Docker Compose configurations and supporting files that integrate with the `hmd neuronsphere up` command. This skill guides you through creating the necessary structure by analyzing your repository's manifest.json to auto-detect dependencies.

## Instructions

### Step 1: Analyze the Repository

First, understand what type of repository you're working with:

1. **Read `meta-data/manifest.json`** to determine:
   - Repository name (`name` field)
   - Repository type (infer from prefix: `hmd-ms-*` = microservice, `hmd-inf-*` = infrastructure, etc.)
   - Existing build commands
   - **Dependencies** in `deploy.dependencies` section
   - **Database engines** in `deploy.default_configuration.service_config.hmd_db_engines`

2. **Identify infrastructure dependencies from manifest.json**:

   | Dependency Pattern | Local Equivalent | Action Required |
   |-------------------|------------------|-----------------|
   | `hmd-database-account` | PostgreSQL | Add postgres init script |
   | `hmd_db_engines.postgres` | PostgreSQL | Add postgres init script |
   | `hmd_db_engines.dynamo` | DynamoDB Local | Add DynamoDB table creation |
   | `hmd_db_engines.gremlin` | JanusGraph | Depends on graph-db service |
   | `hmd-inf-s3bucket` | MinIO | Add MinIO bucket creation script |
   | `hmd-inf-neptune` | JanusGraph | Depends on graph-db service |
   | `hmd-inf-redis` | Redis | Add redis service dependency |

3. **Check for existing Docker files**:
   - Look for `docker-compose*.yml` or `docker-compose*.yaml` files
   - Check `src/docker/` for Dockerfiles
   - Look for existing configuration files

4. **Identify the plugin name**:
   - For `hmd-ms-transform` → plugin name is `transform`
   - For `hmd-inf-trino` → plugin name is `trino`
   - Extract the meaningful suffix after the repo type prefix

### Step 2: Create Directory Structure

Use the `init-plugin` command to scaffold the directory structure:

```bash
hmd neuronsphere init-plugin --plugin-name <plugin_name>
```

This creates:
```
src/local/
    nsplugin.json              # Plugin configuration (required) - template created
    docker-compose.<plugin>.yml # Docker Compose file (required) - template created
    config/                    # Configuration files (optional)
    templates/                 # Jinja2 templates (optional)
    scripts/
        postgres/              # PostgreSQL init scripts (optional)
        minio/                 # MinIO bucket creation scripts (optional)
        dynamodb/              # DynamoDB table creation scripts (optional)
```

The command auto-detects the plugin name from `meta-data/manifest.json` if not provided.

Alternatively, create the structure manually:
```bash
mkdir -p src/local/config
mkdir -p src/local/templates
mkdir -p src/local/scripts/postgres
mkdir -p src/local/scripts/minio
mkdir -p src/local/scripts/dynamodb
```

### Step 3: Analyze Manifest Dependencies

Read `meta-data/manifest.json` and extract dependency information:

```json
{
  "deploy": {
    "dependencies": {
      "db-credentials": {
        "repo_class_name": "hmd-database-account",
        "required": "true"
      },
      "buckets": {
        "repo_class_name": "hmd-inf-s3bucket",
        "required": "false"
      },
      "neptune-db": {
        "repo_class_name": "hmd-inf-neptune",
        "required": "true"
      }
    },
    "default_configuration": {
      "service_config": {
        "hmd_db_engines": {
          "postgres": {
            "engine_type": "postgres",
            "engine_config": {
              "db_name": "my_service"
            }
          },
          "dynamo": {
            "engine_type": "dynamo"
          },
          "gremlin": {
            "engine_type": "gremlin"
          }
        }
      }
    }
  }
}
```

For each dependency type found, follow the corresponding section below.

### Step 4: Create nsplugin.json

Create `src/local/nsplugin.json` with the following structure. Use manifest.json to populate dependencies:

```json
{
  "plugin_name": "<plugin_name>",
  "compose_file": "docker-compose.<plugin_name>.yml",
  "resources": {
    "services": [],
    "databases": [],
    "endpoints": [],
    "buckets": []
  },
  "required_dirs": [],
  "config_mappings": [],
  "templates": [],
  "postgres_scripts": [],
  "minio_scripts": [],
  "dynamodb_scripts": [],
  "dependencies": {
    "requires_plugins": [],
    "requires_services": []
  },
  "config": {
    "SERVICE_CONFIG": {
      "default": {},
      "env_var": "SERVICE_CONFIG",
      "type": "json"
    }
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
| `resources.buckets` | S3/MinIO buckets this plugin needs | `[{"name": "transform-data", "region": "us-west-2"}]` |
| `required_dirs` | Directories to create in HMD_HOME | `["transform", "transform/queries"]` |
| `config_mappings` | Files to copy to HMD_HOME | See below |
| `templates` | Jinja2 templates to render | See below |
| `postgres_scripts` | PostgreSQL init scripts | `["scripts/postgres/init.sh"]` |
| `minio_scripts` | MinIO bucket creation scripts | `["scripts/minio/create-buckets.sh"]` |
| `dynamodb_scripts` | DynamoDB table creation scripts | `["scripts/dynamodb/create-tables.sh"]` |
| `dependencies.requires_plugins` | Other plugins that must be enabled | `["graph", "airflow"]` |
| `dependencies.requires_services` | Docker services that must be running | `["db", "graph-db"]` |
| `config` | Configurable environment variables with defaults | See below |
| `env_var_override` | Environment variable to enable/disable | `"HMD_LOCAL_NEURONSPHERE_ENABLE_TRANSFORM"` |

#### Config Section Format:

The `config` section defines environment variables that can be configured via `meta-data/config_local.json`:

```json
"config": {
  "SERVICE_CONFIG": {
    "default": {"operations_modules": ["my_service.my_service"]},
    "env_var": "SERVICE_CONFIG",
    "type": "json"
  },
  "LOG_LEVEL": {
    "default": "INFO",
    "env_var": "MY_SERVICE_LOG_LEVEL",
    "type": "string"
  }
}
```

Schema options:
- `default`: Default value if not overridden
- `env_var`: Environment variable name to set
- `type`: Value type (`string`, `json`, `int`, `bool`)

To override values, create `meta-data/config_local.json` in your repo:

```json
{
  "SERVICE_CONFIG": {
    "operations_modules": ["my_service.my_service", "my_service.custom_ops"],
    "custom_setting": true
  },
  "LOG_LEVEL": "DEBUG"
}
```

The values from `config_local.json` are merged with defaults and injected as environment variables when the plugin starts.

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

### Step 5: Create Docker Compose File

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
      # Configurable via meta-data/config_local.json
      SERVICE_CONFIG: ${SERVICE_CONFIG:-'{}'}
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

---

## Dependency-Specific Configuration

### PostgreSQL Database (hmd-database-account or hmd_db_engines.postgres)

If manifest.json contains `hmd-database-account` dependency or `hmd_db_engines.postgres`, create a PostgreSQL init script.

**1. Create `src/local/scripts/postgres/<db_name>.sh`:**

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

**2. Add to nsplugin.json:**

```json
{
  "resources": {
    "databases": [
      {"username": "hmd_ms_<plugin>", "password": "hmd_ms_<plugin>", "database": "hmd_ms_<plugin>"}
    ]
  },
  "postgres_scripts": ["scripts/postgres/hmd_ms_<plugin>.sh"],
  "dependencies": {
    "requires_services": ["db"]
  }
}
```

**3. Add to docker-compose:**

```yaml
services:
  <service>:
    depends_on:
      db:
        condition: service_healthy
    environment:
      HMD_DB_HOST: db
      HMD_DB_PORT: 5432
      HMD_DB_NAME: hmd_ms_<plugin>
      HMD_DB_USER: hmd_ms_<plugin>
      HMD_DB_PASSWORD: hmd_ms_<plugin>
```

### S3 Buckets (hmd-inf-s3bucket)

If manifest.json contains `hmd-inf-s3bucket` dependency, create MinIO bucket initialization.

**1. Create `src/local/scripts/minio/create-buckets.sh`:**

```bash
#!/bin/bash
set -e

# Wait for MinIO to be ready
until mc alias set myminio http://minio:9000 minioadmin minioadmin; do
  echo "Waiting for MinIO..."
  sleep 2
done

# Create buckets
mc mb --ignore-existing myminio/<bucket-name>

# Set bucket policy if needed (public read)
# mc anonymous set download myminio/<bucket-name>

echo "Buckets created successfully"
```

**2. Add to nsplugin.json:**

```json
{
  "resources": {
    "buckets": [
      {"name": "<bucket-name>", "region": "us-west-2"}
    ]
  },
  "minio_scripts": ["scripts/minio/create-buckets.sh"],
  "dependencies": {
    "requires_services": ["minio"]
  }
}
```

**3. Add MinIO client to docker-compose (if not using shared init):**

```yaml
services:
  <plugin>-minio-init:
    image: minio/mc:latest
    container_name: <plugin>-minio-init
    depends_on:
      minio:
        condition: service_healthy
    volumes:
      - ./scripts/minio:/scripts
    entrypoint: /scripts/create-buckets.sh
    networks:
      - neuronsphere_default

  <service>:
    environment:
      AWS_ENDPOINT_URL: http://minio:9000
      AWS_ACCESS_KEY_ID: minioadmin
      AWS_SECRET_ACCESS_KEY: minioadmin
      S3_BUCKET_NAME: <bucket-name>
```

### DynamoDB (hmd_db_engines.dynamo)

If manifest.json contains `hmd_db_engines.dynamo`, create DynamoDB Local table initialization.

**1. Create `src/local/scripts/dynamodb/create-tables.sh`:**

```bash
#!/bin/bash
set -e

ENDPOINT_URL="http://dynamodb-local:8000"

# Wait for DynamoDB to be ready
until aws dynamodb list-tables --endpoint-url $ENDPOINT_URL > /dev/null 2>&1; do
  echo "Waiting for DynamoDB Local..."
  sleep 2
done

# Create table (example - adjust AttributeDefinitions and KeySchema as needed)
aws dynamodb create-table \
  --endpoint-url $ENDPOINT_URL \
  --table-name <table-name> \
  --attribute-definitions \
    AttributeName=pk,AttributeType=S \
    AttributeName=sk,AttributeType=S \
  --key-schema \
    AttributeName=pk,KeyType=HASH \
    AttributeName=sk,KeyType=RANGE \
  --billing-mode PAY_PER_REQUEST \
  2>/dev/null || echo "Table may already exist"

echo "DynamoDB tables created successfully"
```

**2. Add to nsplugin.json:**

```json
{
  "dynamodb_scripts": ["scripts/dynamodb/create-tables.sh"],
  "dependencies": {
    "requires_services": ["dynamodb-local"]
  }
}
```

**3. Add to docker-compose:**

```yaml
services:
  <plugin>-dynamodb-init:
    image: amazon/aws-cli:latest
    container_name: <plugin>-dynamodb-init
    depends_on:
      dynamodb-local:
        condition: service_healthy
    volumes:
      - ./scripts/dynamodb:/scripts
    environment:
      AWS_ACCESS_KEY_ID: local
      AWS_SECRET_ACCESS_KEY: local
      AWS_DEFAULT_REGION: us-west-2
    entrypoint: /scripts/create-tables.sh
    networks:
      - neuronsphere_default

  <service>:
    depends_on:
      <plugin>-dynamodb-init:
        condition: service_completed_successfully
    environment:
      AWS_DYNAMODB_ENDPOINT: http://dynamodb-local:8000
      AWS_ACCESS_KEY_ID: local
      AWS_SECRET_ACCESS_KEY: local
      AWS_DEFAULT_REGION: us-west-2
```

### Graph Database (hmd-inf-neptune or hmd_db_engines.gremlin)

If manifest.json contains `hmd-inf-neptune` dependency or `hmd_db_engines.gremlin`, configure JanusGraph dependency.

**1. Add to nsplugin.json:**

```json
{
  "dependencies": {
    "requires_plugins": ["graph"],
    "requires_services": ["graph-db"]
  }
}
```

**2. Add to docker-compose:**

```yaml
services:
  <service>:
    depends_on:
      graph-db:
        condition: service_healthy
    environment:
      HMD_GRAPH_HOST: graph-db
      HMD_GRAPH_PORT: 8182
```

### Redis (hmd-inf-redis)

If manifest.json contains `hmd-inf-redis` dependency, configure Redis.

**1. Add to nsplugin.json:**

```json
{
  "dependencies": {
    "requires_services": ["redis"]
  }
}
```

**2. Add to docker-compose:**

```yaml
services:
  <service>:
    depends_on:
      redis:
        condition: service_healthy
    environment:
      REDIS_HOST: redis
      REDIS_PORT: 6379
```

---

## Examples

### Microservice Example (hmd-ms-transform)

Based on a manifest.json with postgres, gremlin, redis, and s3 dependencies:

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
    ],
    "buckets": [
      {"name": "transform-data", "region": "us-west-2"}
    ]
  },
  "required_dirs": ["transform", "transform/queries", "data/local_transforms", "queues"],
  "config_mappings": [
    {"source": "config/query_config.json", "dest": "transform/queries/query_config.json", "merge": true},
    {"source": "queues/", "dest": "queues/", "if_empty": true}
  ],
  "postgres_scripts": ["scripts/postgres/hmd_ms_transform.sh"],
  "minio_scripts": ["scripts/minio/create-buckets.sh"],
  "dependencies": {
    "requires_plugins": ["graph"],
    "requires_services": ["db", "graph-db", "redis", "minio"]
  },
  "config": {
    "SERVICE_CONFIG": {
      "default": {
        "operations_modules": ["hmd_ms_transform.hmd_ms_transform"]
      },
      "env_var": "SERVICE_CONFIG",
      "type": "json"
    }
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

---

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

---

## Validation

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
- [ ] All minio scripts referenced exist (if applicable)
- [ ] All dynamodb scripts referenced exist (if applicable)
- [ ] All template files referenced exist
- [ ] Dependencies are correctly documented based on manifest.json
- [ ] Environment variable name follows convention: `HMD_LOCAL_NEURONSPHERE_ENABLE_<NAME>`
