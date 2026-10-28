import datetime
import logging
import sys
import os
import json
from pathlib import Path
import shutil
from typing import Dict
from hmd_entity_storage.gremlin_query_support import GremlinQuerySupport
import pandas as pd
from hmd_lib_librarian_client.hmd_lib_librarian_client import HmdLibrarianClient
from hmd_cli_tools.hmd_cli_tools import (
    get_session,
    get_cloud_region,
    make_standard_name,
)
from hmd_cli_tools.cdktf_tools import get_neptune_endpoint

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


def get_graph_conn_info(graph_db: str) -> Dict:
    """Get the graph connection information based on the graph_db name

    Args:
        graph_db (str): Name of the graph database

    Returns:
        Dict: Graph connection information
    """
    if os.environ.get("HMD_ENVIRONMENT") == "local":
        return {
            "host": graph_db,
            "port": 8182,
            "protocol": "ws",
        }
    else:
        session = get_session(get_cloud_region(os.environ.get("HMD_REGION")))
        neptune_client = session.client("neptune")
        host = get_neptune_endpoint(
            session,
            graph_db,
            "hmd-inf-neptune",
            "aaa",
            os.getenv("HMD_ENVIRONMENT"),
            os.getenv("HMD_REGION"),
            os.getenv("HMD_CUSTOMER_CODE"),
        )

        return {
            "host": host,
            "port": 8182,
            "protocol": "wss",
        }


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

    graph_conn_info = get_graph_conn_info(transform_instance_context.get("graph_db"))

    if graph_conn_info is None:
        raise ValueError("graph_conn is required in transform_instance_context")

    # Get the graph data
    graph_query_support = GremlinQuerySupport(
        host=graph_conn_info.get("host"),
        port=graph_conn_info.get("port", 8182),
        query_definitions={},
        protocol=graph_conn_info.get("protocol", "wss"),
    )

    start_date = transform_instance_context.get("start_date")
    if isinstance(start_date, list):
        if len(start_date) > 1:
            raise ValueError("start_date should be a single value, not a list")
        start_date = start_date[0]
    start_date = start_date.replace("T", " ")
    duration = transform_instance_context.get("duration", "24 hours")

    if start_date is None:
        raise ValueError("start_date is required in transform_instance_context")

    duration_amt = int(duration.split(" ")[0])
    duration_unit = duration.split(" ")[1]
    if duration_unit not in ["hours", "days", "weeks", "months"]:
        raise ValueError(
            "Invalid duration unit. Must be one of: hours, days, weeks, months"
        )

    end_date = pd.to_datetime(start_date) + pd.Timedelta(
        **{duration_unit: duration_amt}
    )
    end_date = end_date.strftime("%Y-%m-%d %H:%M:%S.%f")[:-3]

    logger.info(f"Start date: {start_date}")
    logger.info(f"End date: {end_date}")

    graph_query = f"""
g.V().hasLabel("hmd_lang_transform.transform_instance").as("ti").has("_created", gte("{start_date}")).has("_created", lt("{end_date}")).select("ti").project("nid", "transform_name", "transform_version","created_at", "scheduled_at", "started_at", "completed_at", "status", "_created", "_updated")
.by(id())
.by(coalesce(__.in("hmd_lang_transform.transform_version_has_transform_instance").in("hmd_lang_transform.transform_has_transform_version").values("name"), constant("null")))
.by(coalesce(__.in("hmd_lang_transform.transform_version_has_transform_instance").values("version"), constant("null")))
.by(coalesce(values("created_at"),values("_created")))
.by(coalesce(__.values("scheduled_at"), constant("null")))
.by(coalesce(__.values("started_at"), constant("null")))
.by(coalesce(__.values("completed_at"), constant("null")))
.by(values("status"))
.by(values("_created"))
.by(values("_updated"))
    """

    result = graph_query_support._do_execute_query(graph_query)

    # Convert the result into a Parquet file

    if not result:
        logger.warning("No data retrieved from the graph query.")
        return 1

    # Convert the result to a DataFrame
    df = pd.DataFrame(result)

    # Ensure the output directory exists
    os.makedirs(output_content_path, exist_ok=True)

    export_name = transform_instance_context.get(
        "export_name", "transform_instance_export"
    )

    # Define the output Parquet file path
    output_file_path = os.path.join(
        output_content_path, f"{export_name}_{start_date.replace(' ', 'T')}.parquet"
    )

    # Write the DataFrame to a Parquet file
    df.to_parquet(output_file_path, index=False)

    logger.info(f"Parquet file written to {output_file_path}")
    target_librarian = os.environ.get("TARGET_LIBRARIAN")
    target_lib_url = os.environ.get(f"hmd_{target_librarian}_librarian_url")

    auth_token = os.environ.get("HMD_AUTH_TOKEN")
    environment = os.environ.get("HMD_ENVIRONMENT", "local")

    content_item_path = f"ntc:/date={datetime.datetime.fromisoformat(start_date).strftime('%Y-%m-%d')}/env={environment}/{export_name}_{datetime.datetime.fromisoformat(start_date).strftime('%Y_%m_%d')}.parquet"
    content_item_item = "ntc_export_parquet"
    lib_client = HmdLibrarianClient(base_url=target_lib_url, auth_token=auth_token)

    push_content_item(
        lib_client, content_item_path, output_file_path, content_item_item
    )
    return 0
