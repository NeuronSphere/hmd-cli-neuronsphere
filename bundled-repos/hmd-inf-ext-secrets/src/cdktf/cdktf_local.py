import base64
from json import dumps

from constructs import Construct

from hmd_cli_tools.cdktf_tools import DeploymentConfig
from hmd_cli_tools.hmd_cli_tools import get_deployer_target_session, get_cloud_region
from hmd_cli_tools.k8s_tools import get_cluster_info
from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_lib_cdktf_factories import Iam
from hmd_lib_secrets_backend import create_secret

class CdkTfStack(HmdCdkTfStack):
    def __init__(
        self,
        scope: Construct,
        ns: str,
        instance_name: str,
        repo_name: str,
        deployment_id: str,
        environment: str,
        region: str,
        customer_code,
        repo_version: str,
        account_number: str,
        profile: str,
        config: dict,
    ):
        super().__init__(
            scope,
            ns,
            instance_name,
            repo_name,
            deployment_id,
            environment,
            region,
            customer_code,
            repo_version,
            account_number,
            profile,
            config,
        )

        dp_config = DeploymentConfig(config)
        eks_cluster_config = dp_config["eks-cluster"]
        session = get_deployer_target_session(
            self.hmd_region, self.profile, self.account
        )

        eks_cluster_info = get_cluster_info(
            eks_cluster_config["instance_name"],
            eks_cluster_config["repo_name"],
            session,
        )
        oidc_provider_id = eks_cluster_info["identity"]["oidc"]["issuer"].rsplit(
            "/", maxsplit=1
        )[1]

        external_secrets_role_name = (
            f"{self.instance_name}-{self.deployment_id}-ext-secret"
        )
        external_secrets_role_arn = f"arn:aws:iam::{self.account}:role/{external_secrets_role_name}"

        assume_policy = [
            {
                "effect": "Allow",
                "principals": [
                    {
                        "identifiers": [
                            f"arn:aws:iam::{self.account}:oidc-provider/oidc.eks.{get_cloud_region(self.hmd_region)}.amazonaws.com/id/{oidc_provider_id}"
                        ],
                        "type": "Federated",
                    }
                ],
                "actions": ["sts:AssumeRoleWithWebIdentity"],
                "condition": [
                    {
                        "test": "StringEquals",
                        "values": [
                            f"system:serviceaccount:{instance_name}-{deployment_id}:{instance_name}-{deployment_id}"
                        ],
                        "variable": f"oidc.eks.{get_cloud_region(self.hmd_region)}.amazonaws.com/id/{oidc_provider_id}:sub",
                    }
                ],
            },
            {
                "effect": "Allow",
                "principals": [
                    {
                        "identifiers": [external_secrets_role_arn],
                        "type": "AWS",
                    }
                ],
                "actions": ["sts:AssumeRole"],
            },
        ]
        iam = Iam(self)
        policy = iam.policy(
            name=f"{external_secrets_role_name}-policy",
            statements=[
                {
                    "actions": [
                        "secretsmanager:GetResourcePolicy",
                        "secretsmanager:GetSecretValue",
                        "secretsmanager:DescribeSecret",
                        "secretsmanager:ListSecretVersionIds"
                    ],
                    "effect": "Allow",
                    "resources": [
                        "*",
                    ],
                },
                {
                    "actions": [
                        "ssm:GetParameter",
                        "ssm:GetParameters",
                        "ssm:GetParametersByPath",
                        "ssm:DescribeParameters",
                    ],
                    "effect": "Allow",
                    "resources": [
                        "*",
                    ],
                },
                {
                    "actions": [
                        "kms:Decrypt",
                    ],
                    "effect": "Allow",
                    "resources": [
                        "*",
                    ],
                },
                {
                    "actions": [
                        "sts:AssumeRole"
                    ],
                    "effect": "Allow",
                    "resources": [
                        external_secrets_role_arn,
                    ],
                }
            ],
        )

        iam.role(
            name=external_secrets_role_name,
            assume_role_statement=assume_policy,
            managed_policies=[f"{policy.arn}"]
        )

        # create image access secrets...
        docker_username = dp_config["docker_username"]
        docker_password = dp_config["docker_password"]
        secret_data = base64.b64encode(f"{docker_username}:{docker_password}".encode())
        secret_data = {
            "auths": {
                "ghcr.io": {
                    "username": docker_username,
                    "password": docker_password,
                    "email": "admin@hmdlabs.io",
                    "auth": secret_data.decode(),
                }
            }
        }
        secret_data = dumps(secret_data)

        docker_secret = {
            "DOCKER_SECRET": secret_data
        }
        create_secret(
            session,
            "hmd-docker-repo-secret",
            docker_secret,
            kms_key_id=dp_config.get("kms_key_id"),
            exists_ok=True,
        )
