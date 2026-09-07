import json

from constructs import Construct

from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_cli_tools.cdktf_tools import db_secret_name_from_dependencies, DeploymentConfig
from hmd_cli_tools.hmd_cli_tools import get_cloud_region, get_session
from hmd_lib_cdktf_factories import Lambda, ApiGateway, Iam, Okta
from hmd_lib_okta.groups import DEFAULT_LAMBDA_GROUPS as DEFAULT_LAMBDA_OKTA_GROUPS
from hmd_lib_okta.groups import group_rule_name

from hmd_lib_cdktf_factories.service_base import ServiceCdkTfStack


class CdkTfStack(ServiceCdkTfStack):
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

        iam = Iam(self)

        session = get_session(get_cloud_region(self.hmd_region), self.profile)

        organization_id = (
            session.client("organizations")
            .describe_organization()
            .get("Organization", {})
            .get("Id")
        )
        assert organization_id is not None, f"No organization id found."

        invoke_naming_service_role = iam.role(
            name="naming_role",
            assume_role_statement={
                "effect": "Allow",
                "actions": ["sts:AssumeRole"],
                "principals": [
                    {
                        "identifiers": ["*"],
                        "type": "AWS",
                    }
                ],
                "condition": [
                    {
                        "test": "StringEquals",
                        "values": [organization_id],
                        "variable": f"aws:PrincipalOrgID",
                    }
                ],
            },
        )
        iam.role_policy(
            name="naming_policy",
            policy_statements=[
                {
                    "effect": "Allow",
                    "actions": ["lambda:InvokeFunction"],
                    "resources": [self.service_lambda_function.arn],
                }
            ],
            role_id=invoke_naming_service_role.id,
        )
