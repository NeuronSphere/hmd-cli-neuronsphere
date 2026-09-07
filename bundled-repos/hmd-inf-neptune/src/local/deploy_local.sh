#!/usr/bin/env bash
#
# Floci-safe local deploy for hmd-inf-neptune.
#
# Why a script and not a `src/local/cdktf` overlay, which is the pattern every
# other repo here uses: `aws_neptune_cluster` cannot be applied against Floci at
# all. The cluster is created and lands in state, but the provider then calls
# ListTagsForResource and Floci fails it --
#
#     InvalidParameterValue: Invalid resource name:
#     arn:aws:neptune:us-west-2:000000000000:cluster:<name>
#
# Floci hands back `DBClusterArn = arn:aws:neptune:...` while its own tagging API
# accepts only `arn:aws:rds:...`, so its ARN is rejected by its own API. That
# poisons `plan` as well as `apply`, so it is not a create-only wrinkle and no
# amount of trimming the resource avoids it. Revisit the CDKTF overlay once that
# is fixed upstream; `deploy_local.sh` is the highest-precedence overlay
# LocalWorkflowRunner supports, so the node still runs through the DAG in a
# projectbuilder container -- only the tool changes.
#
# Idempotent: re-running is a no-op once the cluster exists, which is what makes
# a repeat `up` fast.
set -euo pipefail

python3 - <<'PY'
import json
import os
import socket
import sys
import time

import boto3
from botocore.exceptions import ClientError

NOT_FOUND = ("DBClusterNotFoundFault", "ResourceNotFoundException")


def standard_name() -> str:
    """The identifier `make_standard_name` produces, which the CLI looks up by.

    Built from the environment rather than imported so this script stays
    runnable in a projectbuilder image without hmd-cli-tools on the path.
    """
    parts = [
        os.environ["HMD_INSTANCE_NAME"],
        os.environ["HMD_REPO_NAME"],
        os.environ.get("HMD_DID", "aaa"),
        os.environ.get("HMD_ENVIRONMENT", "local"),
        os.environ.get("HMD_REGION", "reg1"),
        os.environ.get("HMD_CUSTOMER_CODE", "none"),
    ]
    return "-".join(parts).replace("_", "-").lower()


config = json.loads(os.environ.get("HMD_INSTANCE_CONFIG") or "{}")
identifier = standard_name()
client = boto3.client("neptune", endpoint_url=os.environ.get("AWS_ENDPOINT_URL"))

try:
    client.describe_db_clusters(DBClusterIdentifier=identifier)
    print(f"Neptune cluster {identifier} already exists; nothing to do.")
except ClientError as e:
    if e.response["Error"]["Code"] not in NOT_FOUND:
        raise
    print(f"Creating Neptune cluster {identifier}...")
    client.create_db_cluster(
        DBClusterIdentifier=identifier,
        Engine="neptune",
        # Only the fields Floci models. The cloud stack's subnet group, security
        # groups, both parameter groups, storage_encrypted and
        # final_snapshot_identifier have nothing to bind to locally.
        EngineVersion=config.get("engine_version", "1.4.7.0"),
    )

deadline = time.time() + int(config.get("wait_timeout", 300))
status = None
while time.time() < deadline:
    clusters = client.describe_db_clusters(DBClusterIdentifier=identifier)["DBClusters"]
    status = clusters[0].get("Status")
    if status == "available":
        break
    time.sleep(2)
else:
    print(f"Neptune cluster {identifier} is {status}, not available", file=sys.stderr)
    sys.exit(1)

# The endpoint consumers get. NOT `DBCluster["Endpoint"]`, which is Floci's own
# Gremlin proxy: that proxy is not restored after a Floci restart -- connections
# are reset while the backend container keeps serving normally. The CLI attaches
# a DNS alias to the backend container and passes it in here, so the recorded
# endpoint stays true across restarts. (`describe_db_cluster_endpoints` is
# unsupported by Floci's Neptune outright.)
host = config.get("graph_host")
port = config.get("graph_port", 8182)
if not host:
    host = clusters[0].get("Endpoint")
    port = clusters[0].get("Port", port)
    print(
        "WARNING: no graph_host in the instance configuration; recording Floci's "
        "proxy endpoint, which will stop answering after a Floci restart.",
        file=sys.stderr,
    )

# Floci reporting the cluster `available` only means it recorded the create --
# it says nothing about whether the JVM behind it has finished booting Gremlin
# Server. Checked against Floci's own proxy endpoint (`clusters[0]["Endpoint"]`),
# never the alias recorded above: that alias points at the backend container the
# CLI has not attached yet at this point in the DAG (it does so only after this
# node succeeds), so checking it here would just time out on every fresh
# deploy. The proxy is Floci's front door and is reachable the moment the
# cluster exists, so it is what confirms "deployed" also means "the JVM
# answers" before anything downstream tries to connect. Best-effort like the
# rest of this script's warnings: a timeout here is logged, not fatal, since
# blocking the whole DAG on it would trade one failure mode for a worse one.
proxy_host = clusters[0].get("Endpoint")
proxy_port = clusters[0].get("Port", 8182)
if proxy_host:
    ready_deadline = time.time() + int(config.get("ready_timeout", 60))
    while time.time() < ready_deadline:
        try:
            with socket.create_connection((proxy_host, proxy_port), timeout=2):
                break
        except OSError:
            time.sleep(2)
    else:
        print(
            f"WARNING: {proxy_host}:{proxy_port} did not accept connections "
            f"within {config.get('ready_timeout', 60)}s; recording it anyway.",
            file=sys.stderr,
        )

out_dir = os.path.join("meta-data", "resources_output")
os.makedirs(out_dir, exist_ok=True)
resource = {
    "resource_name": os.environ["HMD_INSTANCE_NAME"],
    "resource_definition": {
        "resource_namespace": "database.neuronsphere.io",
        "resource_definition_name": "graph-database",
        "version": "0.1.0",
    },
    "output": {
        "endpoint": f"ws://{host}:{port}/gremlin",
        "host": host,
        "port": port,
        "cluster_identifier": identifier,
    },
}
path = os.path.join(out_dir, f"{os.environ['HMD_INSTANCE_NAME']}.json")
with open(path, "w") as fh:
    json.dump(resource, fh, indent=2)
print(f"Wrote {path}: {resource['output']['endpoint']}")
PY
