from constructs import Construct

from hmd_cli_tools.hmd_cli_tools import get_deployer_target_session, get_secret
from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_lib_cdktf.terraform_imports.aws.route53_resolver_query_log_config import (
    Route53ResolverQueryLogConfig,
)
from hmd_lib_cdktf.terraform_imports.aws.route53_resolver_query_log_config_association import (
    Route53ResolverQueryLogConfigAssociation,
)
from hmd_lib_cdktf.terraform_imports.terraform_aws_modules.aws.vpc import (
    TerraformAwsModulesAwsVpc as Vpc,
)


ORG_METADATA_NAME = "organization-metadata"


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

        org_metadata = get_secret(
            get_deployer_target_session(
                hmd_region=self.hmd_region, profile=self.profile, account=self.account
            ),
            ORG_METADATA_NAME,
        )
        grc_log_bucket_arn = f"arn:aws:s3:::{self.customer_code}.{org_metadata.get('grc_account', self.account)}.grc.{self.hmd_region}.logs"

        expose_db = config.get("expose_db", False)
        expose_db = expose_db and expose_db.lower() == "true"
        vpc = Vpc(
            self,
            "base-vpc",
            name=self.base_name,
            cidr=config["cidr"],
            azs=config["azs"],
            public_subnets=config["public_subnets"],
            public_subnet_tags={"visibility": "public", "kubernetes.io/role/elb": "1"},
            private_subnets=config["private_subnets"],
            private_subnet_tags={
                "visibility": "private",
                "kubernetes.io/role/elb": "1",
            },
            database_subnets=config["database_subnets"],
            database_subnet_tags={"visibility": "db"},
            enable_nat_gateway=True,
            single_nat_gateway=True,
            tags=self.global_tags,
            create_database_subnet_group=True,
            create_database_subnet_route_table=expose_db,
            create_database_internet_gateway_route=expose_db,
            enable_dns_hostnames=True,
            enable_dns_support=True,
            enable_flow_log=True,
            flow_log_destination_arn=grc_log_bucket_arn,
            flow_log_destination_type="s3",
            flow_log_traffic_type="ALL",
        )

        route53_query_log_config = Route53ResolverQueryLogConfig(
            self,
            "route53-query-log-config",
            name=self.base_name,
            destination_arn=grc_log_bucket_arn,
            tags=self.global_tags,
        )

        Route53ResolverQueryLogConfigAssociation(
            self,
            "route53-query-log-config-association",
            resolver_query_log_config_id=route53_query_log_config.id,
            resource_id=vpc.vpc_id_output,
        )

    def do_migrate_state(self):
        """Reconcile Terraform state with the current synth so module-ID
        changes don't trigger destroy-and-recreate plans. Runs between
        `terraform init` and `terraform apply` via hmd-cli-cdktf's hook."""
        from hmd_cli_tools.cdktf_tools import TerraformStateMigrator

        migrator = TerraformStateMigrator()
        migrator.auto_detect_module_migrations()

        # Fallback for construct-hierarchy refactors that change the
        # module's base name (built-in auto-detect only matches when the
        # post-suffix-strip base names line up). If exactly one module is
        # unmatched on each side after pass 1, assume they correspond.
        state_resources = migrator._get_state_resources()
        state_modules = {
            m
            for r in state_resources
            for m in [migrator._extract_module_name(r)]
            if m
        }
        state_modules = {
            m
            for m in state_modules
            if any(r.startswith(m + ".") for r in state_resources)
        }
        config_modules = set(migrator._get_config_modules())

        covered_old = {
            x["old"] for x in migrator.migrations if x["type"] == "module"
        }
        covered_new = {
            x["new"] for x in migrator.migrations if x["type"] == "module"
        }
        unchanged = state_modules & config_modules
        unmatched_old = state_modules - covered_old - unchanged
        unmatched_new = config_modules - covered_new - unchanged

        if len(unmatched_old) == 1 and len(unmatched_new) == 1:
            old = next(iter(unmatched_old))
            new = next(iter(unmatched_new))
            print(f"  ↪ Fallback 1:1 module migration: {old} -> {new}")
            migrator.add_module_migration(old, new)

        migrator.execute()

    def do_pre_destroy(self):
        session = get_deployer_target_session(
            self.hmd_region, self.profile, self.account
        )
        ec2_client = session.client("ec2")

        # Find the VPC created by this stack using the base_name tag
        vpcs = ec2_client.describe_vpcs(
            Filters=[{"Name": "tag:Name", "Values": [self.base_name]}]
        )["Vpcs"]

        if not vpcs:
            return

        vpc_id = vpcs[0]["VpcId"]

        # Only delete security groups belonging to this VPC
        security_groups = ec2_client.describe_security_groups(
            Filters=[{"Name": "vpc-id", "Values": [vpc_id]}]
        )["SecurityGroups"]

        for sg in security_groups:
            if sg["GroupName"] != "default":
                ec2_client.delete_security_group(GroupId=sg["GroupId"])
