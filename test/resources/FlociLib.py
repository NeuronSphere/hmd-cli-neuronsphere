"""
Custom Robot Framework library for testing Floci integration.

Provides keywords for:
- Waiting for Floci readiness
- S3 bucket and object operations
- Verifying data persistence
"""

import os
import shutil
import time

import boto3
import requests
from robot.api import logger
from robot.api.deco import keyword, library

DEFAULT_ENDPOINT = "http://localhost:4566"
REGION = "us-west-2"


@library
class FlociLib:
    """Robot Framework library for Floci AWS service assertions."""

    def _client(self, service):
        endpoint = os.environ.get(
            "FLOCI_ENDPOINT", os.environ.get("MINISTACK_ENDPOINT", DEFAULT_ENDPOINT)
        )
        return boto3.client(
            service,
            endpoint_url=endpoint,
            aws_access_key_id="dummykey",
            aws_secret_access_key="dummykey",
            region_name=REGION,
        )

    @keyword
    def wait_until_floci_is_ready(self, timeout=300):
        """Poll Floci health endpoint until all services are available.

        Args:
            timeout: Maximum wait time in seconds.
        """
        endpoint = os.environ.get(
            "FLOCI_ENDPOINT", os.environ.get("MINISTACK_ENDPOINT", DEFAULT_ENDPOINT)
        )
        start = time.time()
        while time.time() - start < int(timeout):
            try:
                r = requests.get(f"{endpoint}/_floci/health", timeout=5)
                if r.status_code == 200:
                    logger.info(f"Floci healthy: {r.json()}")
                    return
            except requests.RequestException:
                pass
            time.sleep(3)
        raise AssertionError(f"Floci not ready after {timeout}s")

    @keyword
    def clean_floci_state(self):
        """Remove Floci persisted data so the next start is clean.

        Clears ``floci/data`` (service state) while preserving the parent
        directories.  Safe to call when Floci is stopped.
        """
        hmd_home = os.environ.get("HMD_HOME", "")
        if not hmd_home:
            logger.warn("HMD_HOME not set, skipping Floci state cleanup")
            return
        for subdir in ["floci/data"]:
            path = os.path.join(hmd_home, subdir)
            if os.path.isdir(path):
                shutil.rmtree(path)
                os.makedirs(path, exist_ok=True)
                logger.info(f"Cleaned Floci state: {path}")

    @keyword
    def wait_until_s3_is_ready(self, timeout=600):
        """Poll Floci S3 endpoint until it responds to list-buckets.

        This is faster than waiting for all Floci services when only S3
        is needed (e.g. persistence tests).

        Args:
            timeout: Maximum wait time in seconds.
        """
        start = time.time()
        while time.time() - start < int(timeout):
            try:
                self._client("s3").list_buckets()
                logger.info("Floci S3 is ready")
                return
            except Exception:
                pass
            time.sleep(3)
        raise AssertionError(f"Floci S3 not ready after {timeout}s")

    @keyword
    def create_s3_bucket(self, bucket_name):
        """Create an S3 bucket in Floci.

        Args:
            bucket_name: Name of the bucket to create.
        """
        try:
            self._client("s3").create_bucket(
                Bucket=bucket_name,
                CreateBucketConfiguration={"LocationConstraint": REGION},
            )
            logger.info(f"Created S3 bucket: {bucket_name}")
        except self._client("s3").exceptions.BucketAlreadyOwnedByYou:
            logger.info(f"S3 bucket already exists: {bucket_name}")

    @keyword
    def upload_test_file_to_s3(self, bucket, key, content):
        """Upload a test file to an S3 bucket.

        Args:
            bucket: Bucket name.
            key: Object key.
            content: String content to upload.
        """
        self._client("s3").put_object(
            Bucket=bucket, Key=key, Body=content.encode()
        )
        logger.info(f"Uploaded s3://{bucket}/{key}")

    @keyword
    def verify_s3_object_exists(self, bucket, key):
        """Assert that an S3 object exists.

        Args:
            bucket: Bucket name.
            key: Object key.
        """
        self._client("s3").head_object(Bucket=bucket, Key=key)
        logger.info(f"Verified s3://{bucket}/{key} exists")

    @keyword
    def verify_s3_object_content(self, bucket, key, expected):
        """Assert that an S3 object has the expected content.

        Args:
            bucket: Bucket name.
            key: Object key.
            expected: Expected string content.
        """
        obj = self._client("s3").get_object(Bucket=bucket, Key=key)
        actual = obj["Body"].read().decode()
        if actual != expected:
            raise AssertionError(
                f"S3 object s3://{bucket}/{key} content mismatch: "
                f"expected '{expected}', got '{actual}'"
            )
        logger.info(f"Verified s3://{bucket}/{key} content matches")
