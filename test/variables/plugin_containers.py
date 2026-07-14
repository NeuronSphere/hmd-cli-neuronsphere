"""
Plugin-to-container name mappings for NeuronSphere integration tests.

Each plugin defines its long-running containers and one-shot init containers separately,
since init containers exit after completing and should not be checked with
'Container Should Be Running'.
"""

# --- Main (always enabled) ---
# hmd_gateway removed — all routing goes through Floci API Gateway
MAIN_RUNNING_CONTAINERS = ["hmd_proxy", "hmd_db"]
MAIN_RUNNING_CONTAINERS_LEGACY = ["hmd_proxy", "hmd_db"]
MAIN_INIT_CONTAINERS = ["hmd-ms-naming_db_init"]

# --- Telemetry ---
TELEMETRY_RUNNING_CONTAINERS = ["otel-collector", "prometheus", "jaeger-all-in-one"]
TELEMETRY_INIT_CONTAINERS = []

# --- Graph ---
GRAPH_RUNNING_CONTAINERS = ["global-graph"]
GRAPH_INIT_CONTAINERS = []

# --- Floci (replaces minio + dynamodb via AWS emulation) ---
FLOCI_RUNNING_CONTAINERS = ["floci"]
FLOCI_INIT_CONTAINERS = []

# --- DynamoDB (legacy, disabled when MiniStack is enabled) ---
DYNAMODB_RUNNING_CONTAINERS = ["dynamodb-local"]
DYNAMODB_INIT_CONTAINERS = []

# --- Jupyter ---
JUPYTER_RUNNING_CONTAINERS = ["jupyter-local"]
JUPYTER_INIT_CONTAINERS = []

# --- MinIO ---
MINIO_RUNNING_CONTAINERS = ["minio"]
MINIO_INIT_CONTAINERS = []

# --- Apache Superset ---
APACHE_SUPERSET_RUNNING_CONTAINERS = [
    "superset_app",
    "superset_cache",
    "superset_worker",
    "superset_worker_beat",
]
APACHE_SUPERSET_INIT_CONTAINERS = ["superset_init"]

# --- Airflow ---
AIRFLOW_RUNNING_CONTAINERS = [
    "hmd_airflow_scheduler",
    "hmd_airflow_webserver",
    "socket-proxy",
]
AIRFLOW_INIT_CONTAINERS = []

# --- Transform ---
# hmd_ms_transform, query_dispatcher, and inst_dispatcher deploy as Lambdas via Floci
# queues container replaced by Floci SQS
TRANSFORM_RUNNING_CONTAINERS = [
    "queue_poll",
]
TRANSFORM_RUNNING_CONTAINERS_LEGACY = [
    "hmd_ms_transform",
    "queues",
    "query_dispatcher",
    "inst_dispatcher",
    "queue_poll",
]
TRANSFORM_INIT_CONTAINERS = []

# --- Trino ---
TRINO_RUNNING_CONTAINERS = ["trino", "namenode", "datanode"]
TRINO_INIT_CONTAINERS = []

# --- ClickHouse ---
CLICKHOUSE_RUNNING_CONTAINERS = ["clickhouse"]
CLICKHOUSE_INIT_CONTAINERS = ["clickhouse-init"]

# --- Hive Metastore ---
HIVE_METASTORE_RUNNING_CONTAINERS = ["metastore"]
HIVE_METASTORE_INIT_CONTAINERS = []


# --- Extend Mode (admin control plane) ---
# ms-deployment and ms-naming run as Lambda functions in Floci, not as Docker containers.
# They are verified via HTTP response, not container status.
EXTEND_ADMIN_RUNNING_CONTAINERS = ["hmd_proxy", "hmd_db", "floci"]
EXTEND_ADMIN_INIT_CONTAINERS = ["hmd-ms-naming_db_init", "hmd-ms-deployment_db_init"]
EXTEND_SUBSTITUTE_CONTAINERS = ["global-graph"]


# --- Minimal-core default (extend mode) ---
# What `hmd neuronsphere up` brings up by default: the Docker network, Floci,
# PostgreSQL, the nginx proxy, and the graph database (Neptune/JanusGraph, part
# of the core). The deployment control plane (ms-deployment / ms-naming /
# dbaccount) runs as Floci Lambdas — verified via HTTP, not container status —
# and k3s runs inside Floci's EKS emulation.
CORE_RUNNING_CONTAINERS = ["hmd_proxy", "hmd_db", "floci", "global-graph"]

# App/infra plugins that are OFF by default. Each is opt-in per user via
# HMD_LOCAL_NEURONSPHERE_ENABLE_<NAME>=true or `hmd neuronsphere configure`.
# None of these containers should be present in a default `up`.
OPTIONAL_PLUGINS = [
    "telemetry",
    "jupyter",
    "apache_superset",
    "airflow",
    "transform",
    "trino",
    "clickhouse",
    "hive_metastore",
    "minio",
    "dynamodb",
]


# --- Aggregate lookup ---
PLUGIN_RUNNING_CONTAINERS = {
    "main": MAIN_RUNNING_CONTAINERS,
    "telemetry": TELEMETRY_RUNNING_CONTAINERS,
    "graph": GRAPH_RUNNING_CONTAINERS,
    "floci": FLOCI_RUNNING_CONTAINERS,
    "dynamodb": DYNAMODB_RUNNING_CONTAINERS,
    "jupyter": JUPYTER_RUNNING_CONTAINERS,
    "minio": MINIO_RUNNING_CONTAINERS,
    "apache_superset": APACHE_SUPERSET_RUNNING_CONTAINERS,
    "airflow": AIRFLOW_RUNNING_CONTAINERS,
    "transform": TRANSFORM_RUNNING_CONTAINERS,
    "trino": TRINO_RUNNING_CONTAINERS,
    "clickhouse": CLICKHOUSE_RUNNING_CONTAINERS,
    "hive_metastore": HIVE_METASTORE_RUNNING_CONTAINERS,
}

PLUGIN_INIT_CONTAINERS = {
    "main": MAIN_INIT_CONTAINERS,
    "telemetry": TELEMETRY_INIT_CONTAINERS,
    "graph": GRAPH_INIT_CONTAINERS,
    "floci": FLOCI_INIT_CONTAINERS,
    "dynamodb": DYNAMODB_INIT_CONTAINERS,
    "jupyter": JUPYTER_INIT_CONTAINERS,
    "minio": MINIO_INIT_CONTAINERS,
    "apache_superset": APACHE_SUPERSET_INIT_CONTAINERS,
    "airflow": AIRFLOW_INIT_CONTAINERS,
    "transform": TRANSFORM_INIT_CONTAINERS,
    "trino": TRINO_INIT_CONTAINERS,
    "clickhouse": CLICKHOUSE_INIT_CONTAINERS,
    "hive_metastore": HIVE_METASTORE_INIT_CONTAINERS,
}

# All bundled plugin names (excluding "main" which is always on)
ALL_BUNDLED_PLUGINS = [
    "telemetry",
    "graph",
    "floci",
    "jupyter",
    "apache_superset",
    "airflow",
    "transform",
    "trino",
    "clickhouse",
    "hive_metastore",
]


def get_expected_running_containers(plugin_list):
    """Return flat list of running container names for a list of plugins."""
    containers = []
    for plugin in plugin_list:
        containers.extend(PLUGIN_RUNNING_CONTAINERS.get(plugin, []))
    return containers


def get_expected_init_containers(plugin_list):
    """Return flat list of init container names for a list of plugins."""
    containers = []
    for plugin in plugin_list:
        containers.extend(PLUGIN_INIT_CONTAINERS.get(plugin, []))
    return containers


# Flat list of every optional-plugin long-running container. None of these
# should be present in a default `hmd neuronsphere up`. (Exposed as a Variables
# value because Robot can't call the helper functions above as keywords.)
OPTIONAL_RUNNING_CONTAINERS = get_expected_running_containers(OPTIONAL_PLUGINS)
