from cdktf import TerraformResourceLifecycle
from constructs import Construct

from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_lib_cdktf.terraform_imports.aws.neptune_cluster import (
    NeptuneCluster,
)
from hmd_lib_cdktf.terraform_imports.aws.neptune_cluster_instance import (
    NeptuneClusterInstance,
)
from hmd_lib_cdktf.terraform_imports.aws.neptune_cluster_parameter_group import (
    NeptuneClusterParameterGroup,
    NeptuneClusterParameterGroupParameter,
)
from hmd_lib_cdktf.terraform_imports.aws.neptune_parameter_group import (
    NeptuneParameterGroup,
    NeptuneParameterGroupParameter,
)
from hmd_lib_cdktf.terraform_imports.aws.data_aws_vpc import (
    DataAwsVpc,
)
from hmd_lib_cdktf.terraform_imports.aws.data_aws_security_groups import (
    DataAwsSecurityGroups,
    DataAwsSecurityGroupsFilter,
)
import random
import string


class CdkTfStack(HmdCdkTfStack):
    def __init__(
        self,
        scope: Construct,
        ns: str,
        instance_name: str,
        repo_name: str,
        deployment_id: str,
        environment: str,
        hmd_region: str,
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
            hmd_region,
            customer_code,
            repo_version,
            account_number,
            profile,
            config,
        )

        base_vpc = config["dependencies"]["base-vpc"]
        base_vpc_data = DataAwsVpc(
            self,
            "base-vpc",
            tags={
                "instance_name": base_vpc["instance_name"],
                "repo_name": base_vpc["repo_name"],
            },
        )

        neptune_subnet_group = f"{base_vpc['instance_name']}_{base_vpc['repo_name']}_{deployment_id}_{environment}_{hmd_region}_{customer_code}"

        security_groups = DataAwsSecurityGroups(
            self,
            "security_groups",
            filter=[
                DataAwsSecurityGroupsFilter(name="group-name", values=["default"]),
                DataAwsSecurityGroupsFilter(name="vpc-id", values=[base_vpc_data.id]),
            ],
        )

        engine = config.get("engine", "neptune")
        engine_version = config.get("engine_version", "1.0.4.1")
        cluster_param_group = config["cluster_parameter_group"]
        db_param_group = config["db_parameter_group"]

        # `create_before_destroy` lets the new family-tagged param group come up
        # and the cluster/instance pointers swing before the old one is destroyed
        # during a major-version upgrade.
        param_group_lifecycle = TerraformResourceLifecycle(create_before_destroy=True)

        cluster_pg = NeptuneClusterParameterGroup(
            self,
            "neptune-clst-pg",
            name_prefix=f"{self.base_name.replace('_', '-')}-clst-pg",
            family=cluster_param_group.get("family", "neptune1"),
            lifecycle=param_group_lifecycle,
            parameter=[
                NeptuneClusterParameterGroupParameter(
                    name=value["name"],
                    value=value["value"],
                    apply_method=value.get("apply_method", "immediate"),
                )
                for value in cluster_param_group.get("parameter_values", [])
            ],
        )

        db_pg = NeptuneParameterGroup(
            self,
            "neptune-db-pg",
            name_prefix=f"{self.base_name.replace('_', '-')}-db-pg",
            family=db_param_group.get("family", "neptune1"),
            lifecycle=param_group_lifecycle,
            parameter=[
                NeptuneParameterGroupParameter(
                    name=value["name"],
                    value=value["value"],
                    apply_method=value.get("apply_method", "immediate"),
                )
                for value in db_param_group.get("parameter_values", [])
            ],
        )
        cluster_kwargs = dict(
            allow_major_version_upgrade=True,
            apply_immediately=True,
            engine=engine,
            engine_version=engine_version,
            cluster_identifier=f"{self.base_name.replace('_', '-')}-cluster",
            iam_database_authentication_enabled=config.get(
                "iam_database_authentication_enabled", False
            ),
            neptune_cluster_parameter_group_name=cluster_pg.id,
            neptune_instance_parameter_group_name=db_pg.id,
            neptune_subnet_group_name=neptune_subnet_group,
            skip_final_snapshot=False,
            storage_encrypted=True,
            tags=self.global_tags,
            vpc_security_group_ids=security_groups.ids,
        )

        backup_retention_period = config.get("backup_retention_period")
        if backup_retention_period is not None:
            cluster_kwargs["backup_retention_period"] = backup_retention_period

        preferred_backup_window = config.get("preferred_backup_window")
        if preferred_backup_window:
            cluster_kwargs["preferred_backup_window"] = preferred_backup_window

        preferred_maintenance_window = config.get("preferred_maintenance_window")
        if preferred_maintenance_window:
            cluster_kwargs["preferred_maintenance_window"] = preferred_maintenance_window

        enable_cloudwatch_logs_exports = config.get("enable_cloudwatch_logs_exports")
        if enable_cloudwatch_logs_exports:
            cluster_kwargs["enable_cloudwatch_logs_exports"] = list(
                enable_cloudwatch_logs_exports
            )

        unique_id = "".join(random.choices(string.ascii_lowercase + string.digits, k=8))
        cluster_kwargs["final_snapshot_identifier"] = (
            f"{self.base_name.replace('_', '-')}-{unique_id}"
        )

        cluster = NeptuneCluster(self, "neptune-cluster", **cluster_kwargs)

        cluster_instance = NeptuneClusterInstance(
            self,
            "neptune-cluster-instance",
            apply_immediately=True,
            count=config.get("instance_count", 1),
            cluster_identifier=cluster.id,
            engine=engine,
            instance_class=config.get("instance_size", "db.t3.medium"),
            neptune_parameter_group_name=db_pg.id,
            neptune_subnet_group_name=neptune_subnet_group,
            tags=self.global_tags,
        )
