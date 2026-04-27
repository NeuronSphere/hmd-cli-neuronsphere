"""
Floci deployer module.

Handles provisioning AWS resources, deploying Lambda functions, and
configuring API Gateway routes in Floci for the local NeuronSphere environment.
"""

import base64
import json
import os
import subprocess
import time
from pathlib import Path
from typing import Any, Dict, List, Optional

import boto3
import requests
from botocore.exceptions import ClientError
from cement import minimal_logger

logger = minimal_logger("floci_deployer")

FLOCI_ENDPOINT = os.environ.get(
    "FLOCI_ENDPOINT", os.environ.get("MINISTACK_ENDPOINT", "http://localhost:4566")
)
FLOCI_INTERNAL_ENDPOINT = "http://floci:4566"

# Workload Floci (separate account for infrastructure/service deployments)
FLOCI_WORKLOAD_ENDPOINT = os.environ.get(
    "FLOCI_WORKLOAD_ENDPOINT", "http://localhost:4567"
)
FLOCI_WORKLOAD_INTERNAL_ENDPOINT = "http://floci-workload:4566"

REGION = os.environ.get("AWS_REGION", "us-west-2")
ACCOUNT_ID = "000000000000"
WORKLOAD_ACCOUNT_ID = "654654218804"


def _get_client(service: str):
    return boto3.client(
        service,
        endpoint_url=FLOCI_ENDPOINT,
        aws_access_key_id=os.environ.get("AWS_ACCESS_KEY_ID", "dummykey"),
        aws_secret_access_key=os.environ.get("AWS_SECRET_ACCESS_KEY", "dummykey"),
        region_name=REGION,
    )


def wait_for_floci(timeout: int = 300, endpoint: str = None):
    """Poll Floci health endpoint until all services are available.

    :param timeout: Max seconds to wait
    :param endpoint: Base URL to check (defaults to FLOCI_ENDPOINT)
    """
    ep = endpoint or FLOCI_ENDPOINT
    start = time.time()
    while time.time() - start < timeout:
        try:
            r = requests.get(f"{ep}/_floci/health", timeout=5)
            if r.status_code == 200:
                logger.info(f"Floci healthy at {ep}: {r.json()}")
                return
        except requests.RequestException:
            pass
        logger.debug(
            f"Waiting for Floci at {ep} ({int(time.time() - start)}s/{timeout}s)..."
        )
        time.sleep(3)
    raise RuntimeError(f"Floci at {ep} not ready after {timeout}s")


# ---------------------------------------------------------------------------
# EKS / k3s cluster management
# ---------------------------------------------------------------------------

K3S_CLUSTER_NAME = os.environ.get("HMD_LOCAL_K3S_CLUSTER_NAME", "neuronsphere")
K3S_KUBECONFIG_PATH = Path(
    os.environ.get(
        "HMD_LOCAL_K3S_KUBECONFIG",
        os.path.join(os.environ.get("HMD_HOME", "/tmp"), ".cache", "k3s", "kubeconfig"),
    )
)

K3S_WRAPPER_IMAGE = os.environ.get(
    "HMD_LOCAL_K3S_WRAPPER_IMAGE", "hmd-img-k3s-floci:0.1"
)


def ensure_k3s_wrapper_image(image: str = K3S_WRAPPER_IMAGE) -> str:
    """Verify the configured k3s wrapper image is available locally.

    Floci 1.5.8 hardcodes ``--kube-apiserver-arg=storage-backend=sqlite3`` when
    spawning k3s, which the kube-apiserver rejects. We work around this by
    pointing Floci at a wrapper image whose entrypoint drops the bad flag
    before calling the real k3s binary. The image lives in ``hmd-img-k3s-floci``.

    Set ``HMD_LOCAL_K3S_WRAPPER_IMAGE`` to point at a different tag (e.g. a
    published registry image, or a locally-built dev tag).
    """
    inspect = subprocess.run(["docker", "image", "inspect", image], capture_output=True)
    if inspect.returncode == 0:
        return image
    raise RuntimeError(
        f"k3s wrapper image '{image}' not found locally. Build it with "
        f"`hmd build` in the hmd-img-k3s-floci repo, pull a published tag, "
        f"or override via HMD_LOCAL_K3S_WRAPPER_IMAGE."
    )


def ensure_k3s_cluster(name: str = K3S_CLUSTER_NAME) -> Dict[str, Any]:
    """Create a Floci EKS k3s cluster (idempotent).

    Floci's EKS service in real mode (FLOCI_SERVICES_EKS_MOCK=false) starts a
    privileged k3s container per cluster on the configured Docker network,
    binding the API server to a host port from 6500-6599.

    Returns the describe_cluster response payload.
    """
    ensure_k3s_wrapper_image()
    eks = _get_client("eks")
    try:
        eks.create_cluster(
            name=name,
            roleArn=f"arn:aws:iam::{ACCOUNT_ID}:role/eks-role",
            resourcesVpcConfig={"subnetIds": [], "securityGroupIds": []},
            version="1.29",
        )
        logger.info(f"Created k3s cluster: {name}")
    except ClientError as e:
        if e.response["Error"]["Code"] in (
            "ResourceInUseException",
            "ConflictException",
        ):
            logger.debug(f"k3s cluster already exists: {name}")
        else:
            raise
    return eks.describe_cluster(name=name)["cluster"]


def _k3s_host_port(name: str) -> str:
    """Return the host port that maps to the k3s API server (6443) on the
    Floci-spawned container, or an empty string if it can't be discovered.
    """
    try:
        result = subprocess.run(
            [
                "docker",
                "inspect",
                f"floci-eks-{name}",
                "--format",
                '{{(index (index .NetworkSettings.Ports "6443/tcp") 0).HostPort}}',
            ],
            capture_output=True,
            text=True,
            timeout=5,
        )
        return result.stdout.strip() if result.returncode == 0 else ""
    except (subprocess.SubprocessError, OSError):
        return ""


def _k3s_container_logs(name: str) -> str:
    """Best-effort fetch of the spawned k3s container's recent logs.

    Floci names the per-cluster k3s container ``floci-eks-<cluster>``. Returns
    an empty string if the container is missing or docker isn't reachable.
    """
    try:
        result = subprocess.run(
            ["docker", "logs", "--tail", "30", f"floci-eks-{name}"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        output = (result.stdout + result.stderr).strip()
        return output
    except (subprocess.SubprocessError, OSError):
        return ""


def wait_for_k3s_ready(
    name: str = K3S_CLUSTER_NAME, timeout: int = 300
) -> Dict[str, Any]:
    """Poll describe_cluster until status == ACTIVE."""
    eks = _get_client("eks")
    start = time.time()
    last_status = None
    while time.time() - start < timeout:
        try:
            cluster = eks.describe_cluster(name=name)["cluster"]
            status = cluster.get("status")
            if status != last_status:
                logger.info(f"k3s cluster {name} status: {status}")
                last_status = status
            if status == "ACTIVE":
                return cluster
            if status == "FAILED":
                logs = _k3s_container_logs(name)
                detail = f"\nfloci-eks-{name} logs:\n{logs}" if logs else ""
                raise RuntimeError(f"k3s cluster {name} entered FAILED status{detail}")
        except ClientError as e:
            logger.debug(f"describe_cluster failed: {e}")
        time.sleep(3)
    logs = _k3s_container_logs(name)
    detail = f"\nfloci-eks-{name} logs:\n{logs}" if logs else ""
    raise RuntimeError(f"k3s cluster {name} not ACTIVE after {timeout}s{detail}")


def write_kubeconfig(name: str = K3S_CLUSTER_NAME, path: Path = None) -> Path:
    """Fetch the k3s cluster's kubeconfig and write it to disk.

    Preference order:
      1. Floci's custom kubeconfig endpoint (if exposed).
      2. ``/etc/rancher/k3s/k3s.yaml`` from inside the spawned k3s container,
         with the server URL rewritten to point at the host-published port.
         This carries the real client cert/key needed to actually authenticate.
      3. Synthesized minimal kubeconfig from the EKS describe payload (last
         resort — uses a placeholder token that won't authenticate).
    """
    out_path = path or K3S_KUBECONFIG_PATH
    out_path.parent.mkdir(parents=True, exist_ok=True)

    # Try Floci's custom kubeconfig endpoint first.
    for url in [
        f"{FLOCI_ENDPOINT}/_floci/eks/{name}/kubeconfig",
        f"{FLOCI_ENDPOINT}/_floci/services/eks/clusters/{name}/kubeconfig",
    ]:
        try:
            r = requests.get(url, timeout=10)
            if r.status_code == 200 and r.text.strip().startswith("apiVersion"):
                out_path.write_text(r.text)
                logger.info(f"Wrote kubeconfig from {url} to {out_path}")
                return out_path
        except requests.RequestException:
            continue

    # Pull the real kubeconfig out of the k3s container.
    host_port = _k3s_host_port(name)
    try:
        result = subprocess.run(
            ["docker", "exec", f"floci-eks-{name}", "cat", "/etc/rancher/k3s/k3s.yaml"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode == 0 and result.stdout.strip().startswith("apiVersion"):
            kubeconfig = result.stdout
            if host_port:
                # k3s writes server: https://127.0.0.1:6443 — rewrite to the host-published port.
                kubeconfig = kubeconfig.replace(
                    "https://127.0.0.1:6443", f"https://localhost:{host_port}"
                )
            out_path.write_text(kubeconfig)
            logger.info(f"Wrote kubeconfig from k3s container to {out_path}")
            return out_path
    except (subprocess.SubprocessError, OSError) as e:
        logger.debug(f"Failed to read kubeconfig from container: {e}")

    # Fallback: synthesize a minimal kubeconfig pointing at the cluster endpoint.
    # Floci returns a server URL using the in-Docker-network hostname
    # (e.g. https://floci-eks-<name>:6443). For host-side kubectl use, swap
    # in the host-published port from the spawned k3s container.
    eks = _get_client("eks")
    cluster = eks.describe_cluster(name=name)["cluster"]
    endpoint = cluster.get("endpoint")
    if not endpoint:
        raise RuntimeError(
            f"Cannot retrieve kubeconfig for {name}: no endpoint and no /_floci/eks/.../kubeconfig endpoint"
        )
    host_port = _k3s_host_port(name)
    if host_port:
        endpoint = f"https://localhost:{host_port}"
    cert = cluster.get("certificateAuthority", {}).get("data", "")
    kubeconfig = f"""apiVersion: v1
kind: Config
clusters:
- name: {name}
  cluster:
    server: {endpoint}
    insecure-skip-tls-verify: true
contexts:
- name: {name}
  context:
    cluster: {name}
    user: {name}
current-context: {name}
users:
- name: {name}
  user:
    token: floci-local
"""
    out_path.write_text(kubeconfig)
    logger.info(f"Wrote synthesized kubeconfig to {out_path}")
    return out_path


def delete_k3s_cluster(name: str = K3S_CLUSTER_NAME) -> None:
    """Delete the k3s cluster (best-effort, used by teardown)."""
    eks = _get_client("eks")
    try:
        eks.delete_cluster(name=name)
        logger.info(f"Deleted k3s cluster: {name}")
    except ClientError as e:
        code = e.response["Error"]["Code"]
        if code in ("ResourceNotFoundException", "NotFoundException"):
            logger.debug(f"k3s cluster already gone: {name}")
        else:
            logger.warning(f"Failed to delete k3s cluster {name}: {e}")


def provision_resources(resources: Dict[str, List[Dict[str, Any]]]):
    """Provision AWS resources declared by plugins.

    Creates SQS queues, S3 buckets, and DynamoDB tables from the aggregated
    resource dictionary. Each call is idempotent.
    """
    # Create SQS queues
    sqs = _get_client("sqs")
    for queue in resources.get("sqs_queues", []):
        name = queue["name"]
        try:
            sqs.create_queue(QueueName=name)
            logger.info(f"Created SQS queue: {name}")
        except ClientError as e:
            if e.response["Error"]["Code"] == "QueueAlreadyExists":
                logger.debug(f"SQS queue already exists: {name}")
            else:
                raise

    # Create S3 buckets
    s3 = _get_client("s3")
    for bucket in resources.get("s3_buckets", []):
        name = bucket["name"]
        try:
            s3.create_bucket(
                Bucket=name,
                CreateBucketConfiguration={"LocationConstraint": REGION},
            )
            logger.info(f"Created S3 bucket: {name}")
        except ClientError as e:
            code = e.response["Error"]["Code"]
            if code in ("BucketAlreadyOwnedByYou", "BucketAlreadyExists"):
                logger.debug(f"S3 bucket already exists: {name}")
            else:
                raise

    # Create DynamoDB tables
    dynamodb = _get_client("dynamodb")
    for table in resources.get("dynamodb_tables", []):
        name = table["name"]
        try:
            dynamodb.create_table(
                TableName=name,
                KeySchema=table.get(
                    "key_schema", [{"AttributeName": "id", "KeyType": "HASH"}]
                ),
                AttributeDefinitions=table.get(
                    "attribute_definitions",
                    [{"AttributeName": "id", "AttributeType": "S"}],
                ),
                BillingMode="PAY_PER_REQUEST",
            )
            logger.info(f"Created DynamoDB table: {name}")
        except ClientError as e:
            if e.response["Error"]["Code"] == "ResourceInUseException":
                logger.debug(f"DynamoDB table already exists: {name}")
            else:
                raise


def _ensure_local_image(image_uri: str) -> str:
    """Resolve a Docker image URI for use by Floci's Lambda executor.

    Floci spawns Lambda containers through the mounted host ``docker.sock``,
    so any image already present in the host's image cache is reusable
    directly — no push to Floci's bundled ECR sidecar is required. The
    sidecar isn't reliably reachable from the host CLI on macOS Docker
    Desktop anyway (bridge network, port unpublished), so we skip it.

    For images not in the cache, return the URI unchanged so Floci's
    Lambda runner can pull from the original registry.
    """
    result = subprocess.run(
        ["docker", "image", "inspect", image_uri],
        capture_output=True,
    )
    if result.returncode == 0:
        logger.debug(f"Image {image_uri} cached locally; Floci will reuse it")
    else:
        logger.debug(f"Image {image_uri} not cached; Floci will pull from registry")
    return image_uri


def _image_cached(image_uri: str) -> bool:
    """Return True if the image is present in the local docker cache."""
    result = subprocess.run(
        ["docker", "image", "inspect", image_uri],
        capture_output=True,
    )
    return result.returncode == 0


def resolve_image_uri(repo_name: str, version: str) -> Optional[str]:
    """Find a locally-cached Docker image URI for a repo/version.

    Tries each candidate prefix in order and returns the first that
    `docker image inspect` finds. Returns None if none are cached.

    Candidates (in priority):
      1. ``$HMD_CONTAINER_REGISTRY/<repo>:<version>`` (matches `hmd build`)
      2. ``$HMD_LOCAL_NS_CONTAINER_REGISTRY/<repo>:<version>``
      3. ``ghcr.io/neuronsphere/<repo>:<version>`` (default registry)
      4. ``<repo>:<version>`` (bare tag)

    Steps 1 and 2 are skipped when the corresponding env var is unset.
    """
    candidates: List[str] = []
    seen: set = set()

    def _add(uri: str) -> None:
        if uri and uri not in seen:
            candidates.append(uri)
            seen.add(uri)

    build_registry = os.environ.get("HMD_CONTAINER_REGISTRY")
    if build_registry:
        _add(f"{build_registry}/{repo_name}:{version}")

    local_registry = os.environ.get("HMD_LOCAL_NS_CONTAINER_REGISTRY")
    if local_registry:
        _add(f"{local_registry}/{repo_name}:{version}")

    _add(f"ghcr.io/neuronsphere/{repo_name}:{version}")
    _add(f"{repo_name}:{version}")

    for uri in candidates:
        if _image_cached(uri):
            logger.debug(f"Resolved image for {repo_name}:{version} → {uri}")
            return uri

    logger.debug(
        f"No locally-cached image for {repo_name}:{version}; tried {candidates}"
    )
    return None


def deploy_lambda_function(
    function_name: str,
    image_uri: str,
    env_vars: Dict[str, str],
    timeout: int = 300,
    memory_size: int = 512,
) -> str:
    """Deploy a Docker image as a Lambda function in Floci.

    Returns the function ARN.
    """
    image_uri = _ensure_local_image(image_uri)
    client = _get_client("lambda")

    function_config = {
        "FunctionName": function_name,
        "PackageType": "Image",
        "Code": {"ImageUri": image_uri},
        "Role": f"arn:aws:iam::{ACCOUNT_ID}:role/lambda-role",
        "Timeout": timeout,
        "MemorySize": memory_size,
        "Environment": {"Variables": env_vars},
    }

    try:
        resp = client.create_function(**function_config)
        logger.info(f"Created Lambda function: {function_name}")
        return resp["FunctionArn"]
    except ClientError as e:
        if e.response["Error"]["Code"] == "ResourceConflictException":
            # Function exists -- update it
            client.update_function_code(FunctionName=function_name, ImageUri=image_uri)
            client.update_function_configuration(
                FunctionName=function_name,
                Timeout=timeout,
                MemorySize=memory_size,
                Environment={"Variables": env_vars},
            )
            resp = client.get_function(FunctionName=function_name)
            logger.info(f"Updated Lambda function: {function_name}")
            return resp["Configuration"]["FunctionArn"]
        raise


# ---------------------------------------------------------------------------
# API Gateway management
# ---------------------------------------------------------------------------


def create_api_gateway(
    api_name: str = "neuronsphere-local", recreate: bool = False
) -> str:
    """Create or get a REST API Gateway in Floci.

    When ``recreate`` is True, any existing API Gateway with the same name
    is deleted first so we start with a clean resource tree. This is
    important because Floci's resource matcher appears to prefer the
    earliest-created `{proxy+}` resource on a tie, so accumulated stale
    routes from previous runs can hijack traffic for newly-registered
    services.

    Returns the REST API ID.
    """
    client = _get_client("apigateway")

    apis = client.get_rest_apis()
    for api in apis.get("items", []):
        if api["name"] == api_name:
            if recreate:
                client.delete_rest_api(restApiId=api["id"])
                logger.info(f"Deleted existing API Gateway: {api['id']}")
            else:
                logger.info(f"Found existing API Gateway: {api['id']}")
                return api["id"]

    resp = client.create_rest_api(
        name=api_name,
        description="NeuronSphere local API Gateway",
    )
    logger.info(f"Created API Gateway: {resp['id']}")
    return resp["id"]


def add_api_gateway_route(
    api_id: str,
    service_name: str,
    function_name: str,
) -> None:
    """Add routes at the API Gateway root that proxy to a Lambda function.

    Creates `/` (root) and `/{proxy+}` integrations for every HTTP method.
    Designed for one-Lambda-per-gateway: the gateway has no service-name
    prefix in its path, so nginx must strip the service prefix before
    proxying to Floci. The Lambda then receives clean `/api/...` paths
    (rather than `/{service_name}/api/...`), which FastAPI/hmd-ms-base
    routes natively.
    """
    client = _get_client("apigateway")
    lambda_client = _get_client("lambda")

    # Get root resource ID
    resources = client.get_resources(restApiId=api_id)
    root_id = None
    existing_paths = {}
    for r in resources["items"]:
        existing_paths[r["path"]] = r["id"]
        if r["path"] == "/":
            root_id = r["id"]

    # Get Lambda function ARN
    func = lambda_client.get_function(FunctionName=function_name)
    function_arn = func["Configuration"]["FunctionArn"]
    integration_uri = (
        f"arn:aws:apigateway:{REGION}:lambda:path"
        f"/2015-03-31/functions/{function_arn}/invocations"
    )

    # Use root resource directly for `/`; create `/{proxy+}` as its child
    svc_resource_id = root_id

    proxy_path = "/{proxy+}"
    if proxy_path in existing_paths:
        proxy_resource_id = existing_paths[proxy_path]
    else:
        proxy_resource = client.create_resource(
            restApiId=api_id,
            parentId=root_id,
            pathPart="{proxy+}",
        )
        proxy_resource_id = proxy_resource["id"]

    # Add methods with Lambda proxy integration on both resources.
    # Floci does not support the ANY catch-all method, so register each
    # HTTP method individually. Delete any pre-existing method first so a
    # partial prior registration (e.g. POST set but GET missing) can't leave
    # gaps that cause Floci's matcher to fall through to a sibling
    # `{proxy+}` resource.
    http_methods = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"]
    for resource_id in [svc_resource_id, proxy_resource_id]:
        for method in http_methods:
            try:
                client.delete_method(
                    restApiId=api_id,
                    resourceId=resource_id,
                    httpMethod=method,
                )
            except ClientError as e:
                if e.response["Error"]["Code"] != "NotFoundException":
                    raise

            client.put_method(
                restApiId=api_id,
                resourceId=resource_id,
                httpMethod=method,
                authorizationType="NONE",
            )

            client.put_integration(
                restApiId=api_id,
                resourceId=resource_id,
                httpMethod=method,
                type="AWS_PROXY",
                integrationHttpMethod="POST",
                uri=integration_uri,
            )

    logger.info(
        f"Added API Gateway route: /{service_name}/{{proxy+}} -> {function_name}"
    )


def deploy_api_gateway(api_id: str, stage_name: str = "local") -> str:
    """Deploy the API Gateway to a stage.

    Returns the invoke URL for use within the Docker network.
    """
    client = _get_client("apigateway")
    resp = client.create_deployment(restApiId=api_id)
    deployment_id = resp["id"]

    # Delete any existing stage first — Floci does not support
    # update_stage patch operations reliably.
    try:
        client.delete_stage(restApiId=api_id, stageName=stage_name)
    except ClientError:
        pass  # Stage doesn't exist yet, that's fine

    # Create the stage linked to the new deployment.
    # Floci does not auto-create stages from create_deployment's stageName.
    client.create_stage(
        restApiId=api_id,
        stageName=stage_name,
        deploymentId=deployment_id,
    )

    invoke_url = (
        f"{FLOCI_INTERNAL_ENDPOINT}/restapis/{api_id}/{stage_name}/_user_request_"
    )
    logger.info(f"API Gateway deployed: {invoke_url}")
    return invoke_url


def get_api_gateway_url(api_id: str, stage: str = "local") -> str:
    """Get the invoke URL for an API Gateway stage.

    Returns the URL usable within the Docker network.
    """
    return f"{FLOCI_INTERNAL_ENDPOINT}/restapis/{api_id}/{stage}/_user_request_"


def setup_service(
    service_name: str,
    image_uri: str,
    env_vars: Dict[str, str],
    api_id: str = None,
) -> str:
    """Deploy a service as a Lambda function and add an API Gateway route.

    If ``api_id`` is provided, adds a route to that existing API Gateway.
    Otherwise creates a fresh API Gateway named ``neuronsphere-<service_name>``.
    Per-service gateways are the default because Floci's matcher treats every
    ``{proxy+}`` resource as a global wildcard; a single shared gateway with
    multiple services would have every service catching every other service's
    traffic.

    Returns the API Gateway ID. Caller should call ``deploy_api_gateway()``
    once per returned api_id after all services are registered.
    """
    # Deploy Lambda
    deploy_lambda_function(service_name, image_uri, env_vars)

    # Create or reuse API Gateway
    if api_id is None:
        api_id = create_api_gateway(
            api_name=f"neuronsphere-{service_name}", recreate=True
        )

    # Add route for this service
    add_api_gateway_route(api_id, service_name, service_name)

    logger.info(f"Service {service_name} routed via API Gateway {api_id}")
    return api_id


def write_nginx_config(
    services: Dict[str, str],
    config_path: Path,
    api_id: str = None,
    stage: str = "local",
    extra_locations: Dict[str, str] = None,
):
    """Write nginx config that proxies each service via API Gateway.

    Args:
        services: Mapping of service_name to api_id.
        config_path: Path to write the nginx config file.
        api_id: Shared API Gateway ID (overrides per-service values).
        stage: API Gateway stage name.
        extra_locations: Optional mapping of {path: upstream_url} for routes
            that bypass API Gateway (e.g., /argo/ → k3s NodePort).
    """
    location_blocks = []
    for service_name in services:
        gw_id = api_id or services[service_name]
        # Strip the `/{service_name}` prefix before proxying so the Lambda
        # receives clean paths like `/api/foo` instead of
        # `/{service_name}/api/foo` (which FastAPI would 404).
        location_blocks.append(
            f"""        location /{service_name}/ {{
            proxy_pass http://floci:4566/restapis/{gw_id}/{stage}/_user_request_/;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_read_timeout 300s;
            proxy_connect_timeout 75s;
        }}"""
        )

    # Argo is enabled by default; expose its UI/API at /argo/.
    if extra_locations is None and os.environ.get(
        "HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO", "true"
    ).lower() not in ("false", "0", "no"):
        argo_upstream = os.environ.get(
            "HMD_LOCAL_ARGO_UPSTREAM", "http://host.docker.internal:30246"
        )
        extra_locations = {"argo": argo_upstream}

    for path, upstream in (extra_locations or {}).items():
        path_clean = path.strip("/")
        # Preserve trailing slash semantics: location /argo/ → upstream/
        upstream_with_slash = upstream.rstrip("/") + "/"
        location_blocks.append(
            f"""        location /{path_clean}/ {{
            proxy_pass {upstream_with_slash};
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_set_header Host $host;
            proxy_read_timeout 300s;
            proxy_connect_timeout 75s;
        }}"""
        )

    locations = "\n".join(location_blocks)
    config = f"""events {{
    worker_connections 1024;
}}
http {{
    server {{
        listen 80 default_server;
        server_name _;
{locations}
        location / {{
            return 404 '{{"error": "no route defined"}}';
        }}
    }}
}}
"""
    os.makedirs(config_path.parent, exist_ok=True)
    with open(config_path, "w") as f:
        f.write(config)
    logger.info(f"Wrote nginx config to {config_path}")
