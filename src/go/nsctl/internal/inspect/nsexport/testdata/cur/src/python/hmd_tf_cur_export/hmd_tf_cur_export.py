import datetime
import logging
import sys
import os
from typing import Dict

import boto3
import pandas as pd
from hmd_lib_librarian_client.hmd_lib_librarian_client import HmdLibrarianClient
from hmd_cli_tools.hmd_cli_tools import get_session, get_cloud_region

logging.basicConfig(
    stream=sys.stdout,
    format="%(levelname)s %(asctime)s - %(message)s",
    level=logging.ERROR,
)

logger = logging.getLogger(__name__)
logger.setLevel(logging.DEBUG)


def push_content_item(
    client: HmdLibrarianClient,
    content_item_path: str,
    file_path: str,
    content_item_type: str,
):
    client.put_file(
        content_path=content_item_path,
        file_name=file_path,
        content_item_type=content_item_type,
    )
    logger.info(f"Uploaded {file_path} to {content_item_path}")


def get_s3_client():
    """Get an S3 client, using hmd_cli_tools session in cloud or default creds locally."""
    if os.environ.get("HMD_ENVIRONMENT") == "local":
        return boto3.client("s3")
    else:
        session = get_session(get_cloud_region(os.environ.get("HMD_REGION")))
        return session.client("s3")


def list_parquet_files(s3_client, bucket: str, prefix: str) -> list:
    """List all .parquet files under the given S3 prefix."""
    paginator = s3_client.get_paginator("list_objects_v2")
    parquet_keys = []
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get("Contents", []):
            if obj["Key"].endswith(".parquet"):
                parquet_keys.append(obj["Key"])
    logger.info(f"Found {len(parquet_keys)} parquet files under s3://{bucket}/{prefix}")
    return parquet_keys


def do_transform(
    input_content_path: str,
    output_content_path: str,
    transform_nid: str,
    transform_instance_context: Dict,
) -> int:
    """Function to do the actual Transform work

    Args:
        input_content_path (str): filepath for input files
        output_content_path (str): filepath for output files
        transform_nid (str): NID of running TransformInstance
        transform_instance_context (Dict): context dictionary for the running TransformInstance

    Returns:
        int: exit code
    """
    logger.info(f"Transform_nid: {transform_nid}")
    logger.info(f"Transform_instance_context: {transform_instance_context}")

    billing_month = transform_instance_context.get("billing_month")
    if billing_month is None:
        raise ValueError("billing_month is required in transform_instance_context")

    cur_s3_prefix = transform_instance_context.get("cur_s3_prefix", "")
    cur_s3_prefix = cur_s3_prefix.replace("{billing_month}", billing_month)

    export_name = transform_instance_context.get("export_name", "cur_export")

    bucket = os.environ.get("BILLING_BUCKET")
    if not bucket:
        raise ValueError("BILLING_BUCKET environment variable is required")

    logger.info(f"Billing month: {billing_month}")
    logger.info(f"S3 prefix: s3://{bucket}/{cur_s3_prefix}")

    s3_client = get_s3_client()

    parquet_keys = list_parquet_files(s3_client, bucket, cur_s3_prefix)

    if not parquet_keys:
        logger.warning(f"No parquet files found under s3://{bucket}/{cur_s3_prefix}")
        return 1

    # Download and concatenate all parquet files
    dataframes = []
    for key in parquet_keys:
        logger.info(f"Downloading s3://{bucket}/{key}")
        local_path = os.path.join(str(input_content_path), os.path.basename(key))
        s3_client.download_file(bucket, key, local_path)
        df = pd.read_parquet(local_path)
        dataframes.append(df)

    combined_df = pd.concat(dataframes, ignore_index=True)
    logger.info(
        f"Combined {len(dataframes)} files into DataFrame with {len(combined_df)} rows"
    )

    # Write combined parquet to output
    os.makedirs(str(output_content_path), exist_ok=True)

    output_file_path = os.path.join(
        str(output_content_path), f"{export_name}_{billing_month}.parquet"
    )
    combined_df.to_parquet(output_file_path, index=False)
    logger.info(f"Parquet file written to {output_file_path}")

    # Upload to librarian
    target_librarian = os.environ.get("TARGET_LIBRARIAN")
    target_lib_url = os.environ.get(f"hmd_{target_librarian}_librarian_url")
    auth_token = os.environ.get("HMD_AUTH_TOKEN")

    year = billing_month.split("-")[0]
    month = billing_month.split("-")[1]

    content_item_path = f"cur:/year={year}/month={month}/{export_name}.parquet"
    content_item_type = "cur_export_parquet"

    lib_client = HmdLibrarianClient(base_url=target_lib_url, auth_token=auth_token)
    push_content_item(
        lib_client, content_item_path, output_file_path, content_item_type
    )

    return 0
