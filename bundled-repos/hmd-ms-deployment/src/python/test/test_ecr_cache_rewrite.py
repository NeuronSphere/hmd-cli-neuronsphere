"""Tests for _rewrite_ghcr_to_ecr helper in deploy_workflow_creator."""

import os
from unittest.mock import patch

from hmd_ms_deployment.deploy_workflow_creator import _rewrite_ghcr_to_ecr


PREFIX = "123456789.dkr.ecr.us-east-1.amazonaws.com/ghcr/"


def test_rewrites_ghcr_image_when_prefix_set():
    with patch.dict(os.environ, {"HMD_ECR_CACHE_PREFIX": PREFIX}):
        result = _rewrite_ghcr_to_ecr("ghcr.io/hmdlabs/hmd-tf-bender:0.1.42")
    assert result == f"{PREFIX}hmdlabs/hmd-tf-bender:0.1.42"


def test_no_rewrite_when_prefix_unset():
    env = {k: v for k, v in os.environ.items() if k != "HMD_ECR_CACHE_PREFIX"}
    with patch.dict(os.environ, env, clear=True):
        result = _rewrite_ghcr_to_ecr("ghcr.io/hmdlabs/hmd-tf-bender:0.1.42")
    assert result == "ghcr.io/hmdlabs/hmd-tf-bender:0.1.42"


def test_no_rewrite_for_non_ghcr_image_when_prefix_set():
    with patch.dict(os.environ, {"HMD_ECR_CACHE_PREFIX": PREFIX}):
        result = _rewrite_ghcr_to_ecr(
            "123456789.dkr.ecr.us-east-1.amazonaws.com/some-image:1.2.3"
        )
    assert result == "123456789.dkr.ecr.us-east-1.amazonaws.com/some-image:1.2.3"
