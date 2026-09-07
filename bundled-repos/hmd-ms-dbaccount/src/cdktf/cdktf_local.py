from constructs import Construct

from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_lib_cdktf_factories import Iam, Lambda, ApiGateway


class CdkTfStack(HmdCdkTfStack):
    def __init__(
        self,
        scope: Construct,
        ns: str,
        instance_name,
        repo_name,
        deployment_id,
        environment,
        hmd_region,
        customer_code,
        repo_version,
        account_number,
        profile,
        config,
    ):
        super().__init__(
            scope,
            ns,
            instance_name,
            repo_name,
            deployment_id,
            environment,
            hmd_region,
            customer_code,
            repo_version,
            account_number,
            profile,
            config,
        )

        iam = Iam(self)
        secret_access_policy = [
            {
                "actions": [
                    "secretsmanager:CreateSecret",
                    "secretsmanager:GetSecretValue",
                    "secretsmanager:DescribeSecret",
                    "secretsmanager:UpdateSecret",
                    "secretsmanager:TagResource",
                    "kms:DescribeKey",
                    "kms:ListAliases",
                    "kms:ListKeys",
                ],
                "effect": "Allow",
                "resources": ["*"],
            },
        ]
        lambda_role = iam.standard_lambda_role(
            additional_policy_statements=secret_access_policy
        )

        # A lambda function in the base-vpc with the image from the current repository
        lambda_function = Lambda(self)
        lf = lambda_function.lambda_function(
            config["dependencies"]["base-vpc"],
            lambda_execution_role=lambda_role,
            instance_name=instance_name,
            repo_name=repo_name,
            repo_version=repo_version,
            vars={},
        )

        # An API gateway that is a passthru to a lambda function...
        api_gateway = ApiGateway(self)
        ag = api_gateway.lambda_api(lf)
