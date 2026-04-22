"""
Floci deployer module.

Handles provisioning AWS resources, deploying Lambda functions, and
configuring API Gateway in Floci for the local NeuronSphere environment.
"""

import json
import os
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
REGION = os.environ.get("AWS_REGION", "us-west-2")
ACCOUNT_ID = "000000000000"


def _get_client(service: str):
    return boto3.client(
        service,
        endpoint_url=FLOCI_ENDPOINT,
        aws_access_key_id=os.environ.get("AWS_ACCESS_KEY_ID", "dummykey"),
        aws_secret_access_key=os.environ.get("AWS_SECRET_ACCESS_KEY", "dummykey"),
        region_name=REGION,
    )


def wait_for_floci(timeout: int = 300):
    """Poll Floci health endpoint until all services are available."""
    start = time.time()
    while time.time() - start < timeout:
        try:
            r = requests.get(f"{FLOCI_ENDPOINT}/_floci/health", timeout=5)
            if r.status_code == 200:
                logger.info(f"Floci healthy: {r.json()}")
                return
        except requests.RequestException:
            pass
        logger.debug(f"Waiting for Floci ({int(time.time() - start)}s/{timeout}s)...")
        time.sleep(3)
    raise RuntimeError(f"Floci not ready after {timeout}s")


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


def get_or_create_api_gateway() -> str:
    """Get or create the shared 'neuronsphere-local' REST API. Returns api_id."""
    client = _get_client("apigateway")

    # Check for existing API
    apis = client.get_rest_apis()
    for api in apis.get("items", []):
        if api["name"] == "neuronsphere-local":
            logger.debug(f"Found existing API Gateway: {api['id']}")
            return api["id"]

    # Create new API
    resp = client.create_rest_api(
        name="neuronsphere-local",
        description="NeuronSphere local development API Gateway",
    )
    api_id = resp["id"]
    logger.info(f"Created API Gateway: {api_id}")
    return api_id


def _get_root_resource_id(client, api_id: str) -> str:
    """Get the root resource (/) ID for an API Gateway."""
    resources = client.get_resources(restApiId=api_id)
    for resource in resources["items"]:
        if resource["path"] == "/":
            return resource["id"]
    raise RuntimeError(f"No root resource found for API {api_id}")


def _find_resource(client, api_id: str, path: str) -> Optional[str]:
    """Find a resource by path, return its ID or None."""
    resources = client.get_resources(restApiId=api_id)
    for resource in resources["items"]:
        if resource["path"] == path:
            return resource["id"]
    return None


def create_api_gateway_route(api_id: str, service_name: str, lambda_arn: str):
    """Create API Gateway resources and integration for a service.

    Creates /{service_name} and /{service_name}/{proxy+} with ANY method
    and AWS_PROXY Lambda integration.
    """
    client = _get_client("apigateway")
    root_id = _get_root_resource_id(client, api_id)

    # Create /{service_name} resource
    svc_resource_id = _find_resource(client, api_id, f"/{service_name}")
    if not svc_resource_id:
        resp = client.create_resource(
            restApiId=api_id,
            parentId=root_id,
            pathPart=service_name,
        )
        svc_resource_id = resp["id"]

    # Create /{service_name}/{proxy+} resource
    proxy_path = f"/{service_name}/{{proxy+}}"
    proxy_resource_id = _find_resource(client, api_id, proxy_path)
    if not proxy_resource_id:
        resp = client.create_resource(
            restApiId=api_id,
            parentId=svc_resource_id,
            pathPart="{proxy+}",
        )
        proxy_resource_id = resp["id"]

    # Set up ANY method + Lambda integration on both resources
    lambda_uri = (
        f"arn:aws:apigateway:{REGION}:lambda:path"
        f"/2015-03-31/functions/{lambda_arn}/invocations"
    )

    for resource_id in [svc_resource_id, proxy_resource_id]:
        try:
            client.put_method(
                restApiId=api_id,
                resourceId=resource_id,
                httpMethod="ANY",
                authorizationType="NONE",
            )
        except ClientError as e:
            if e.response["Error"]["Code"] != "ConflictException":
                raise

        try:
            client.put_integration(
                restApiId=api_id,
                resourceId=resource_id,
                httpMethod="ANY",
                type="AWS_PROXY",
                integrationHttpMethod="POST",
                uri=lambda_uri,
            )
        except ClientError as e:
            if e.response["Error"]["Code"] != "ConflictException":
                raise

    logger.info(f"Created API Gateway route: /{service_name}/{{proxy+}}")


def deploy_api(api_id: str, stage: str = "local"):
    """Deploy the API Gateway to make routes active."""
    client = _get_client("apigateway")
    client.create_deployment(restApiId=api_id, stageName=stage)
    logger.info(f"Deployed API Gateway {api_id} to stage '{stage}'")


def get_api_gateway_url(api_id: str, stage: str = "local") -> str:
    """Return the internal Docker network URL for the API Gateway."""
    return f"{FLOCI_INTERNAL_ENDPOINT}/restapis/{api_id}/{stage}/_user_request_"


def setup_service(
    service_name: str,
    image_uri: str,
    env_vars: Dict[str, str],
) -> str:
    """Deploy a service as a Lambda function behind the API Gateway.

    Returns the service's invoke URL.
    """
    # Deploy Lambda
    lambda_arn = deploy_lambda_function(service_name, image_uri, env_vars)

    # Get/create API Gateway and route
    api_id = get_or_create_api_gateway()
    create_api_gateway_route(api_id, service_name, lambda_arn)
    deploy_api(api_id)

    url = get_api_gateway_url(api_id)
    service_url = f"{url}/{service_name}"
    logger.info(f"Service {service_name} available at: {service_url}")
    return service_url


def write_nginx_config(api_id: str, config_path: Path, stage: str = "local"):
    """Write nginx config that proxies to the Floci API Gateway.

    Args:
        api_id: The API Gateway REST API ID.
        config_path: Path to write the nginx config file.
        stage: API Gateway stage name.
    """
    config = f"""events {{
    worker_connections 1024;
}}
http {{
    server {{
        listen 80 default_server;
        server_name _;
        location / {{
            proxy_pass http://floci:4566/restapis/{api_id}/{stage}/_user_request_/;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_read_timeout 300s;
            proxy_connect_timeout 75s;
        }}
    }}
}}
"""
    os.makedirs(config_path.parent, exist_ok=True)
    with open(config_path, "w") as f:
        f.write(config)
    logger.info(f"Wrote nginx config to {config_path}")
