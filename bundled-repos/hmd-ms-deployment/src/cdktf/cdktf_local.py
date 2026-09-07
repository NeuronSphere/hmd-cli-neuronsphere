import copy
import json
import os
from collections import defaultdict
from shutil import copyfile
from typing import List, Tuple, Dict
from constructs import Construct
from cdktf import Fn
from hmd_lib_okta.groups import DEFAULT_LAMBDA_GROUPS as DEFAULT_LAMBDA_OKTA_GROUPS
from hmd_lib_okta.groups import group_rule_name

from hmd_cli_tools.cdktf_tools import (
    DeploymentConfig,
    db_secret_name_from_dependencies,
    get_neptune_endpoint,
)
from hmd_cli_tools.hmd_cli_tools import (
    get_cloud_region,
    get_account_number,
    get_deployer_target_session,
    get_session,
    get_secret,
    get_neuronsphere_domain,
)
from hmd_cli_tools.k8s_tools import create_kubeconfig
from hmd_lib_cdktf.terraform_imports.time.provider import TimeProvider
from hmd_lib_cdktf.terraform_imports.time.sleep import Sleep
from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_lib_cdktf.terraform_imports.kubernetes.cluster_role import (
    ClusterRole,
    ClusterRoleMetadata,
    ClusterRoleRule,
)
from hmd_lib_cdktf.terraform_imports.kubernetes.cluster_role_binding import (
    ClusterRoleBinding,
    ClusterRoleBindingMetadata,
    ClusterRoleBindingRoleRef,
    ClusterRoleBindingSubject,
)
from hmd_lib_cdktf.terraform_imports.kubernetes.data_kubernetes_secret import (
    DataKubernetesSecret,
    DataKubernetesSecretMetadata,
)
from hmd_lib_cdktf.terraform_imports.kubernetes.provider import (
    KubernetesProvider as Kubernetes,
    KubernetesProviderExperiments,
)
from hmd_lib_cdktf.terraform_imports.kubernetes.persistent_volume import (
    PersistentVolume,
    PersistentVolumeSpec,
    PersistentVolumeMetadata,
    PersistentVolumeSpecPersistentVolumeSource,
    PersistentVolumeSpecPersistentVolumeSourceCsi,
)
from hmd_lib_cdktf.terraform_imports.kubernetes.persistent_volume_claim import (
    PersistentVolumeClaim,
    PersistentVolumeClaimSpec,
    PersistentVolumeClaimSpecSelector,
    PersistentVolumeClaimMetadata,
    PersistentVolumeClaimSpecResources,
)
from hmd_lib_cdktf.terraform_imports.kubernetes.role import Role, RoleMetadata, RoleRule
from hmd_lib_cdktf.terraform_imports.kubernetes.role_binding import (
    RoleBinding,
    RoleBindingMetadata,
    RoleBindingRoleRef,
    RoleBindingSubject,
)
from hmd_lib_cdktf.terraform_imports.kubernetes.secret import Secret, SecretMetadata
from hmd_lib_cdktf.terraform_imports.kubernetes.service_account import (
    ServiceAccount,
    ServiceAccountMetadata,
)
from hmd_lib_cdktf.terraform_imports.kubernetes.storage_class import (
    StorageClass,
    StorageClassMetadata,
)
from hmd_lib_cdktf.terraform_imports.kubernetes.manifest import Manifest

from hmd_lib_cdktf_factories import (
    Lambda,
    ApiGateway,
    Iam,
    Okta,
    Cloudwatch,
    SecretsManager,
)
from hmd_lib_cdktf_factories.efs import Efs
from hmd_lib_cdktf_factories.scheduled_operation import ScheduledOperation

KUBECONFIG_FILE = ".kubeconfig"

DEPLOY_SECRET_EX = {
    "apiVersion": "external-secrets.io/v1",
    "kind": "ExternalSecret",
    "metadata": {"name": "deploy-secrets", "namespace": "argo-aaa"},
    "spec": {
        "secretStoreRef": {
            "name": "",
            "kind": "ClusterSecretStore",
        },
        "target": {
            "name": "deploy-secrets",
            "creationPolicy": "Owner",
        },
        "data": [],
    },
}


def get_one_subnet_per_az(session, instance_name: str, repo_name: str) -> List[str]:
    ec2 = session.client("ec2")
    vpcs = ec2.describe_vpcs(
        Filters=[
            {"Name": "tag:instance_name", "Values": [instance_name]},
            {"Name": "tag:repo_name", "Values": [repo_name]},
        ]
    )
    assert len(vpcs["Vpcs"]) == 1, f"Expected 1 VPC, got {len(vpcs['Vpcs'])}"
    vpc_id = vpcs["Vpcs"][0]["VpcId"]

    subnets = ec2.describe_subnets(Filters=[{"Name": "vpc-id", "Values": [vpc_id]}])
    subnets_az = defaultdict(list)
    for subnet in subnets["Subnets"]:
        subnets_az[subnet["AvailabilityZone"]].append(subnet["SubnetId"])

    return [snids[0] for snids in subnets_az.values()]


def get_artifact_api_key(session, hmd_region, customer_code):
    apigw = session.client("apigateway")
    results = apigw.get_api_keys(
        nameQuery=f"artifact_hmd-ms-artifact-lib_aaa_admin_{hmd_region}_{customer_code}",
        includeValues=True,
    )
    api_key = results["items"][0]["value"] if len(results["items"]) > 0 else None
    return api_key


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

        dp_config = DeploymentConfig(config)
        service_config = dp_config["service_config"]
        hmd_db_engines = service_config.get("hmd_db_engines", {})
        for engine in hmd_db_engines.values():
            if engine["engine_type"] == "postgres":
                if "db_secret_name" in engine["engine_config"]:
                    if engine["engine_config"]["db_secret_name"].startswith(
                        "dependency:"
                    ):
                        dependency_name = engine["engine_config"][
                            "db_secret_name"
                        ].split(":", maxsplit=1)[1]
                        engine["engine_config"]["db_secret_name"] = (
                            db_secret_name_from_dependencies(
                                dp_config[dependency_name],
                                environment,
                                hmd_region,
                                customer_code,
                            )
                        )
                        engine["engine_config"]["db_name"] = dp_config[
                            f"{dependency_name}.db_name"
                        ]

            if engine["engine_type"] == "gremlin":
                if "db_host" in engine["engine_config"] and engine["engine_config"][
                    "db_host"
                ].startswith("dependency:"):
                    dependency_name = engine["engine_config"]["db_host"].split(
                        ":", maxsplit=1
                    )[1]
                    engine["engine_config"]["db_host"] = get_neptune_endpoint(
                        get_deployer_target_session(
                            self.hmd_region, self.profile, self.account
                        ),
                        dp_config[f"{dependency_name}.instance_name"],
                        dp_config[f"{dependency_name}.repo_name"],
                        deployment_id,
                        environment,
                        hmd_region,
                        customer_code,
                    )

        hmd_log_level = dp_config.get("hmd_log_level")
        if hmd_log_level:
            hmd_log_level = json.dumps(hmd_log_level)
        else:
            hmd_log_level = "INFO"

        lambda_vars = {
            "DD_LAMBDA_HANDLER": "hmd_ms_base.hmd_ms_base.handler",
            "HMD_LOG_LEVEL": hmd_log_level,
            "SERVICE_CONFIG": json.dumps(service_config),
        }
        lambda_vars.update(self.get_lambda_vars(dp_config))

        iam = Iam(self)

        lambda_role = iam.standard_lambda_role(
            additional_policy_statements=self.get_lambda_policy(dp_config)
        )

        lambda_fn = Lambda(self)

        storage_class_name, pvc_name, pv_name = self.get_efs_names()
        session = get_deployer_target_session(hmd_region, profile, self.account)

        base_vpc = dp_config["base-vpc"]
        subnet_ids = get_one_subnet_per_az(
            session,
            base_vpc["instance_name"],
            base_vpc["repo_name"],
        )

        efs = Efs(self)
        efs_fs, mount_targets = efs.efs_filesystem(
            f"{instance_name}-{repo_name}-{deployment_id}", base_vpc, subnet_ids
        )
        time_provider = TimeProvider(self, "time_provider")
        sleeper = Sleep(
            self,
            "sleepy",
            create_duration="90s",
            depends_on=mount_targets,
            provider=time_provider,
        )

        hmd_authorizer = None
        if dp_config.get("authorizer"):
            hmd_authorizer = f"{self.make_standard_name(instance_name=dp_config['authorizer.instance_name'], repo_name=dp_config['authorizer.repo_name'])}-lambda"
        if not lambda_vars.get("AUTHORIZATION"):
            lambda_vars["AUTHORIZATION"] = "CUSTOM" if hmd_authorizer else "NONE"

        okta = Okta(self)
        for group in DEFAULT_LAMBDA_OKTA_GROUPS:
            okta.group(
                group,
                full_name=group_rule_name(
                    group,
                    self.instance_name,
                    self.environment,
                    self.customer_code,
                ),
            )

        if dp_config.get("create_okta_identity", False):
            service_app = okta.oauth_service_app(True)
            self.service_oauth_app = service_app

        lf = lambda_fn.lambda_function(
            vpc_config=dp_config["base-vpc"],
            lambda_execution_role=lambda_role,
            instance_name=instance_name,
            repo_name=repo_name,
            repo_version=repo_version,
            efs_access_point=efs.lambda_access_point(efs_fs, "/deploy_base"),
            vars=lambda_vars,
            memory_size=dp_config.get("memory_size", 256),
            image_repo_name=dp_config.get("service-image.repo_name"),
            image_repo_version=dp_config.get("service-image.version"),
            depends_on=[sleeper],
        )
        self.service_lambda_function = lf

        api_gateway = ApiGateway(self)
        ag = api_gateway.lambda_api(
            lf,
            authorization=lambda_vars["AUTHORIZATION"],
            authorizer_name=hmd_authorizer,
        )
        self.service_api = ag

        if hasattr(self, "add_resource_output"):
            self.add_resource_output(
                self.instance_name,
                {
                    "resource_namespace": "application.neuronsphere.io",
                    "resource_definition_name": "microservice",
                    "version": "0.1.0",
                },
                {
                    "api_base_url": f"https://{self.instance_name}-{self.deployment_id}-{self.hmd_region}.{self.root_neuronsphere_domain}",
                },
                tags=[{"key": "service", "value": "deployment"}],
            )
        assume_role = (
            get_account_number(get_session(get_cloud_region(hmd_region), self.profile))
            != self.account
        )
        session = get_deployer_target_session(hmd_region, self.profile, self.account)
        eks_config = dp_config["argo.eks-alb.eks-cluster"]

        with create_kubeconfig(
            eks_config["instance_name"],
            session,
            self.profile,
            get_cloud_region(self.hmd_region),
            eks_config["repo_name"],
            assume_role,
        ) as kubeconfig:
            if os.path.exists(KUBECONFIG_FILE):
                os.remove(KUBECONFIG_FILE)
            copyfile(kubeconfig, KUBECONFIG_FILE)

        # print(exec_command)
        k8s_provider = Kubernetes(
            self,
            "Kubernetes",
            config_path=f"{os.getcwd()}/{KUBECONFIG_FILE}",
            experiments=[KubernetesProviderExperiments(manifest_resource=True)],
        )
        storage_class = StorageClass(
            self,
            "deploy_storage_class",
            provider=k8s_provider,
            metadata=StorageClassMetadata(name=storage_class_name),
            storage_provisioner="efs.csi.aws.com",
            parameters={
                "provisioningMode": "efs-ap",
                "fileSystemId": efs_fs.id,
                "directoryPerms": "700",
            },
        )

        pv = PersistentVolume(
            self,
            "deploy_pv",
            provider=k8s_provider,
            metadata=PersistentVolumeMetadata(
                name=pv_name, labels={"hmdlabs.io/pv": "deployment"}
            ),
            spec=[
                PersistentVolumeSpec(
                    access_modes=["ReadWriteMany"],
                    capacity={"storage": "5Gi"},
                    persistent_volume_reclaim_policy="Retain",
                    storage_class_name=storage_class_name,
                    persistent_volume_source=PersistentVolumeSpecPersistentVolumeSource(
                        csi=PersistentVolumeSpecPersistentVolumeSourceCsi(
                            driver="efs.csi.aws.com",
                            volume_handle=efs_fs.id,
                        )
                    ),
                )
            ],
        )

        argo_k8s_namespace = (
            f'{dp_config["argo.instance_name"]}-{dp_config["argo.deployment_id"]}'
        )

        pvc = PersistentVolumeClaim(
            self,
            "deploy_pvc",
            provider=k8s_provider,
            metadata=PersistentVolumeClaimMetadata(
                name=pvc_name,
                namespace=argo_k8s_namespace,
            ),
            spec=PersistentVolumeClaimSpec(
                access_modes=["ReadWriteMany"],
                resources=PersistentVolumeClaimSpecResources(
                    requests={"storage": "5Gi"}
                ),
                selector=PersistentVolumeClaimSpecSelector(
                    match_labels={"hmdlabs.io/pv": "deployment"}
                ),
                storage_class_name=storage_class_name,
            ),
        )
        deploy_secrets = get_secret(
            get_session(get_cloud_region(self.hmd_region), profile), "deploy-secrets"
        )

        if dp_config.get("ext-secrets"):
            ds_manifest = copy.deepcopy(DEPLOY_SECRET_EX)
            ds_manifest["metadata"][
                "namespace"
            ] = f'{dp_config["argo.instance_name"]}-{dp_config["argo.deployment_id"]}'

            # Get ClusterSecretStore name from ext-secrets dependency
            ext_secrets_instance_name = dp_config["ext-secrets.instance_name"]
            ext_secrets_repo_name = dp_config["ext-secrets.repo_name"]
            ext_secrets_deployment_id = dp_config["ext-secrets.deployment_id"]
            cluster_secret_store_name = dp_config.get("ext-secrets.clusterSecretStore.name", "aws-secrets-manager")
            ds_manifest["spec"]["secretStoreRef"]["name"] = cluster_secret_store_name

            for key in deploy_secrets:
                ds_manifest["spec"]["data"].append(
                    {"secretKey": key, "remoteRef": {"key": "deploy-secrets", "property": key}}
                )

            print(json.dumps(ds_manifest, indent=2))
            Manifest(
                self,
                "deploy_secrets",
                provider=k8s_provider,
                manifest=ds_manifest,
            )

        sa_name = f"{self.instance_name}-{self.repo_name}-{self.deployment_id}"
        k8s_role_name = f"{self.instance_name}-{self.deployment_id}-role"
        k8s_cluster_role_name = (
            f"{self.instance_name}-{self.deployment_id}-cluster-role"
        )
        service_account = ServiceAccount(
            self,
            "deployment-argo-sa",
            metadata=ServiceAccountMetadata(
                name=sa_name,
                namespace=argo_k8s_namespace,
            ),
            provider=k8s_provider,
        )
        k8s_role_rules = [
            RoleRule(
                api_groups=["coordination.k8s.io"],
                resources=["leases"],
                verbs=["create", "get", "update"],
            ),
            RoleRule(
                api_groups=[""],
                resources=["secrets"],
                verbs=["get"],
            ),
        ]
        k8s_role = Role(
            self,
            "deployment-argo-role",
            metadata=RoleMetadata(name=k8s_role_name),
            rule=k8s_role_rules,
            provider=k8s_provider,
        )
        k8s_cluster_role_rules = [
            ClusterRoleRule(
                api_groups=["argoproj.io"],
                resources=[
                    "workflows",
                    "workflow/finalizers",
                    "workflowtasksets",
                    "workflowtasksets/finalizers",
                ],
                verbs=["get", "list", "watch", "update", "patch", "delete", "create"],
            ),
            ClusterRoleRule(
                api_groups=["argoproj.io"],
                resources=[
                    "workflowtemplates",
                    "workflowtemplates/finalizers",
                    "clusterworkflowtemplates",
                    "clusterworkflowtemplates/finalizers",
                ],
                verbs=["get", "list", "watch"],
            ),
            ClusterRoleRule(
                api_groups=["argoproj.io"],
                resources=["cronworkflows", "cronworkflows/finalizers"],
                verbs=["get", "list", "watch", "update", "patch", "delete"],
            ),
        ]

        k8s_cluster_role = ClusterRole(
            self,
            "deployment-argo-cluster-role",
            metadata=ClusterRoleMetadata(name=k8s_cluster_role_name),
            rule=k8s_cluster_role_rules,
        )
        RoleBinding(
            self,
            "deployment-argo-role-binding",
            metadata=RoleBindingMetadata(name=f"{self.instance_name}-binding"),
            role_ref=RoleBindingRoleRef(
                api_group="rbac.authorization.k8s.io", kind="Role", name=k8s_role_name
            ),
            subject=[
                RoleBindingSubject(
                    kind="ServiceAccount", name=sa_name, namespace=argo_k8s_namespace
                )
            ],
        )
        ClusterRoleBinding(
            self,
            "deployment-argo-cluster-role-binding",
            metadata=ClusterRoleBindingMetadata(name=f"{self.instance_name}-binding"),
            role_ref=ClusterRoleBindingRoleRef(
                api_group="rbac.authorization.k8s.io",
                kind="ClusterRole",
                name=k8s_cluster_role_name,
            ),
            subject=[
                ClusterRoleBindingSubject(
                    kind="ServiceAccount", name=sa_name, namespace=argo_k8s_namespace
                )
            ],
        )

        sa_k8s_secret = Secret(
            self,
            "deployment-argo-sa-secret",
            metadata=SecretMetadata(
                name=f"{sa_name}-token",
                annotations={
                    "kubernetes.io/service-account.name": sa_name,
                },
                namespace=argo_k8s_namespace,
            ),
            type="kubernetes.io/service-account-token",
            depends_on=[service_account],
        )

        sa_data_k8s_secret = DataKubernetesSecret(
            self,
            "deployment-argo-sa-data-secret",
            metadata=DataKubernetesSecretMetadata(
                name=f"{sa_name}-token",
                namespace=argo_k8s_namespace,
            ),
            binary_data={"token": ""},
            depends_on=[sa_k8s_secret],
        )

        sm = SecretsManager(self)
        sa_token_secret = sm.secret(
            name=f"{sa_name}-sa-token",
            description="API access token to Argo",
        )
        sm.secret_version(
            name="argo-sa-token",
            secret=sa_token_secret,
            secret_string=Fn.base64decode(
                Fn.lookup(sa_data_k8s_secret.binary_data, "token", "")
            ),
        )

        if "artifact_librarian" in dp_config:
            cw = Cloudwatch(self)
            okta_conn = cw.okta_event_connection(
                client_id=self.service_oauth_app.client_id,
                client_secret=self.service_oauth_app.client_secret,
            )

            # schedule a call to check for new deployables
            sch_op = ScheduledOperation(self)
            sch_op.scheduled_custom_op_api(
                schedule_expression=f"rate({dp_config['artifact_librarian'].get('new_deployables_call_rate', '1 minute')})",
                custom_op="check_for_new_deployables",
                oauth_connection=okta_conn,
            )
            sch_op.scheduled_custom_op_api(
                schedule_expression=f"rate({dp_config['artifact_librarian'].get('auto_deployment_call_rate', '1 minute')})",
                custom_op="check_for_auto_deployments",
                oauth_connection=okta_conn,
            )

    def has_artifact_librarian(self, dp_config: DeploymentConfig):
        return "artifact_librarian" in dp_config

    def get_efs_names(self) -> Tuple[str, str, str]:
        return (
            f"{self.instance_name}-{self.repo_name}-{self.deployment_id}",
            f"{self.instance_name}-{self.deployment_id}-deploy-pvc",
            f"{self.instance_name}-{self.deployment_id}-deploy-pv",
        )

    def get_lambda_vars(self, dp_config: DeploymentConfig):
        session = get_deployer_target_session(
            self.hmd_region, self.profile, self.account
        )
        storage_class_name, pvc_name, pv_name = self.get_efs_names()
        argo_logs_bucket = dp_config.get("argo.logs")
        if argo_logs_bucket:
            logs_bucket_name = self.make_standard_name(instance_name=argo_logs_bucket['instance_name'], repo_name=argo_logs_bucket['repo_name']).replace('_', '-')
        hmd_app_image = dp_config['hmd_app_image']

        hmd_app_image_map = dp_config.get('hmd_app_image_map', {})

        aws_region = get_cloud_region(self.hmd_region)
        ecr_cache_prefix = f"{self.account}.dkr.ecr.{aws_region}.amazonaws.com/ghcr/"

        def rewrite_ghcr_to_ecr(image_url: str) -> str:
            if image_url.startswith("ghcr.io/"):
                return f"{ecr_cache_prefix}{image_url[len('ghcr.io/'):]}"
            return image_url

        if dp_config.get("ecr-cache"):
            hmd_app_image = rewrite_ghcr_to_ecr(hmd_app_image)
            if isinstance(hmd_app_image_map, dict):
                hmd_app_image_map = {k: rewrite_ghcr_to_ecr(v) for k, v in hmd_app_image_map.items()}

        if isinstance(hmd_app_image_map, dict):
            hmd_app_image_map = json.dumps(hmd_app_image_map)

        assert isinstance(hmd_app_image, str), f"hmd_app_image configuration must be str or dict, got {type(hmd_app_image)}"

        hmd_deployment_image = f"ghcr.io/hmdlabs/hmd-ms-deployment:{self.repo_version}"
        if dp_config.get("ecr-cache"):
            hmd_deployment_image = rewrite_ghcr_to_ecr(hmd_deployment_image)

        vars = {
            "HMD_APP_IMAGE": hmd_app_image,
            "HMD_APP_IMAGE_MAP": hmd_app_image_map,
            "HMD_DEPLOYMENT_IMAGE": hmd_deployment_image,
            "DEPLOYMENT_STORAGE_CLASS_NAME": storage_class_name,
            "DEPLOYMENT_PVC_NAME": pvc_name,
            "DEPLOYMENT_PV_NAME": pv_name,
            "ARGO_INSTANCE_NAME": dp_config["argo.instance_name"],
            "ARGO_DEPLOYMENT_ID": dp_config["argo.deployment_id"],
            "ARGO_NAMESPACE": dp_config.get("argo.namespace", "argo-aaa"),
            "HMD_USE_FASTAPI": dp_config.get("use_fastapi", "true"),
            "TERRAFORM_BINARY_NAME": dp_config.get("terraform_binary_name", "tofu"),
        }
        if dp_config.get("ecr-cache"):
            vars["HMD_ECR_CACHE_PREFIX"] = ecr_cache_prefix
        argo_logs_bucket = dp_config.get("argo.logs")
        if argo_logs_bucket:
            logs_bucket_name = self.make_standard_name(instance_name=argo_logs_bucket['instance_name'], repo_name=argo_logs_bucket['repo_name']).replace('_', '-')
        else:
            print("Missing Argo Logs Bucket")

        vars['ARGO_LOGS_BUCKET'] = logs_bucket_name
        artifact_librarian_config = dp_config.get("artifact_librarian")
        if artifact_librarian_config:
            vars["ARTIFACT_LIBRARIAN_NAME"] = artifact_librarian_config["instance_name"]
            artifact_api_key = get_artifact_api_key(
                session, self.hmd_region, self.customer_code
            )
            if artifact_api_key:
                vars["HMD_ARTIFACT_LIBRARIAN_API_KEY"] = artifact_api_key

        if dp_config.get("otel-collector"):
            vars["HMD_OTEL_ENDPOINT"] = (
                f"https://{dp_config.get('otel-collector.instance_name')}.{get_neuronsphere_domain(self.customer_code, self.environment)}"
            )
            vars["OTEL_INSTRUMENTATION_AWS_LAMBDA_FLUSH_TIMEOUT"] = str(
                dp_config.get("otel_flush_timeout_ms", 3000)
            )
            vars["HMD_OTEL_HTTP_TIMEOUT_S"] = str(
                dp_config.get("otel_http_timeout_s", 1)
            )

        if dp_config.get("workers"):
            vars["WORKERS_INSTANCE_NAME"] = dp_config["workers.instance_name"]

        # NERD0005: reach the shared hmd-inf-redis deployment for the environment
        # graph cache. Mirror hmd-ms-transform: from the Lambda runtime we use the
        # external neuronsphere domain host + credentials from Secrets Manager
        # (the in-cluster ClusterIP is not reachable from Lambda).
        vars["HMD_ENV_CACHE_ENABLED"] = str(
            dp_config.get("hmd_env_cache_enabled", False)
        ).lower()
        redis = dp_config.get("redis")
        if redis:
            redis_secret_name = self.make_standard_name(
                redis["instance_name"],
                redis["repo_name"],
                redis["deployment_id"],
                environment=self.environment,
                hmd_region=self.hmd_region,
                customer_code=self.customer_code,
            )
            redis_host = f"{redis['instance_name']}.{get_neuronsphere_domain(self.customer_code, self.environment)}"
            vars.update(
                {
                    "REDIS_HOST": redis_host,
                    "REDIS_PORT": f"{redis.get('port', 6379)}",
                    "REDIS_DB": f"{redis.get('db', 0)}",
                    "REDIS_SECRET": redis_secret_name,
                }
            )

        return vars

    def get_lambda_policy(self, dp_config: DeploymentConfig) -> List[Dict]:
        policy: List[Dict] = [
            {
                "actions": [
                    "secretsmanager:GetSecretValue",
                    "secretsmanager:DescribeSecret",
                ],
                "effect": "Allow",
                "resources": ["*"],
            },
            {
                "effect": "Allow",
                "actions": ["lambda:InvokeFunction"],
                "resources": ["*"],
            },
            {
                "effect": "Allow",
                "actions": ["ssm:GetParameter"],
                "resources": [
                    f"arn:aws:ssm:{get_cloud_region(self.hmd_region)}:{self.account}:parameter/{self.base_name}*"
                ],
            },
        ]

        service_config = dp_config["service_config"]
        hmd_db_engines = service_config.get("hmd_db_engines", {})
        for engine in hmd_db_engines.values():
            if engine["engine_type"] == "postgres":
                # db_secret_name is already resolved to its concrete value in
                # __init__ (e.g. core-rds_hmd-postgres-rds_...) and lives
                # outside this service's base_name prefix, so it needs its
                # own explicit statement.
                db_secret_name = engine.get("engine_config", {}).get(
                    "db_secret_name"
                )
                if db_secret_name:
                    policy.append(
                        {
                            "effect": "Allow",
                            "actions": ["ssm:GetParameter"],
                            "resources": [
                                f"arn:aws:ssm:{get_cloud_region(self.hmd_region)}:{self.account}:parameter/{db_secret_name}"
                            ],
                        }
                    )

        argo_logs_bucket = dp_config.get("argo.logs")
        if argo_logs_bucket:
            logs_bucket_name = self.make_standard_name(
                instance_name=argo_logs_bucket["instance_name"],
                repo_name=argo_logs_bucket["repo_name"],
            ).replace("_", "-")
            policy.extend([
                {
                    "effect": "Allow",
                    "actions": ["s3:ListBucket"],
                    "resources": [f"arn:aws:s3:::{logs_bucket_name}"],
                },
                {
                    "effect": "Allow",
                    "actions": ["s3:GetObject"],
                    "resources": [f"arn:aws:s3:::{logs_bucket_name}/*"],
                },
            ])

        return policy
