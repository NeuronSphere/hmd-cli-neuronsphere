import logging
import time

from cdktf import TerraformOutput, Token
from constructs import Construct
from kubernetes import config, client
from kubernetes.client import ApiException
from kubernetes.client.models import V1ConfigMap
from yaml import safe_dump, safe_load

from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_cli_tools.hmd_cli_tools import (
    get_cloud_region,
    get_deployer_target_session,
    get_account_number,
    get_session,
)
from hmd_cli_tools.k8s_tools import (
    create_kubeconfig,
)
from hmd_cli_tools.cdktf_tools import get_neptune_security_group, DeploymentConfig
from hmd_lib_cdktf.terraform_imports.terraform_aws_modules.aws.eks import (
    TerraformAwsModulesAwsEks as Eks,
)
from hmd_lib_cdktf.terraform_imports.aws.eks_addon import EksAddon
from hmd_lib_cdktf_factories import Iam, Vpc
from hmd_lib_cdktf_factories.cloudwatch import Cloudwatch
from hmd_lib_cdktf_factories.datadog import Datadog

HMD_NEURONSPHERE_DEPLOY = "hmd.neuronsphere.deploy"

logger = logging.getLogger(__name__)


def build_addon_config(name: str, version_overrides: dict, **extra) -> dict:
    # PRESERVE on update keeps in-cluster customizations during K8s minor bumps;
    # OVERWRITE on create still wins on first apply.
    cfg = {
        "resolve_conflicts_on_create": "OVERWRITE",
        "resolve_conflicts_on_update": "PRESERVE",
        "preserve": True,
    }
    pinned = (version_overrides or {}).get(name)
    if pinned:
        cfg["addon_version"] = pinned
    else:
        cfg["most_recent"] = True
    cfg.update(extra)
    return cfg

BASE_AWS_AUTH = V1ConfigMap(
    api_version="v1",
    kind="ConfigMap",
    data={"mapRoles": ""},
    metadata={"name": "aws-auth", "namespace": "kube-system"},
)

VPC_RESOURCE_CONTROLLER = V1ConfigMap(
    api_version="v1",
    kind="ConfigMap",
    data={"enable-windows-ipam": "true"},
    metadata={"name": "amazon-vpc-cni", "namespace": "kube-system"},
)


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
        dp_config = DeploymentConfig(config)

        self._validate_cluster_version_step(config)

        # create eks iam role
        iam = Iam(self)
        eks_role_name = f"{self.instance_name}-{self.repo_name}"
        eks_role = iam.role(
            eks_role_name,
            {
                "actions": ["sts:AssumeRole"],
                "effect": "Allow",
                "principals": [
                    {"identifiers": ["eks.amazonaws.com"], "type": "Service"}
                ],
            },
            managed_policies=[
                "AmazonEKSClusterPolicy",
                "AmazonEKSVPCResourceController",
            ],
        )

        cluster_name = f"{self.instance_name}_{self.repo_name}"
        self.cluster_name = cluster_name
        efs_policy = iam.policy(
            f"{cluster_name}-node-efs",
            [
                {
                    "actions": [
                        "elasticfilesystem:DescribeMountTargets",
                        "elasticfilesystem:DescribeAccessPoints",
                        "elasticfilesystem:DescribeFileSystems",
                        "ec2:DescribeAvailabilityZones",
                    ],
                    "resources": ["*"],
                    "effect": "Allow",
                }
            ],
        )
        # get vpc_id and subnet_ids from dependent vpc tags
        vpc = Vpc(self)
        base_vpc_config = config["dependencies"]["base-vpc"]
        base_vpc_data = vpc.get(base_vpc_config)
        subnet_ids = vpc.get_subnet_ids(base_vpc_data.id).ids

        all_vpc_traffic_sg = vpc.allow_all_vpc_traffic_security_group(
            f"{cluster_name}_sg", base_vpc_data
        )

        # marshal configuration variables
        vpc_id = base_vpc_data.id
        enable_irsa = config.get("enable_irsa", True)
        enable_private_access = config.get("enable_private_access", True)
        enable_public_access = config.get("enable_public_access", True)

        # enable EKS auto mode
        if not config.get("enable_auto_mode", False):
            compute_config = {"enabled": False}
            node_role = iam.role(
                f"{cluster_name}-node-group-role",
                {
                    "actions": ["sts:AssumeRole"],
                    "effect": "Allow",
                    "principals": [
                        {"identifiers": ["ec2.amazonaws.com"], "type": "Service"}
                    ],
                },
                managed_policies=[
                    "AmazonEKS_CNI_Policy",
                    "AmazonEKSWorkerNodePolicy",
                    "AmazonEC2ContainerRegistryPullOnly",
                    "AmazonSSMManagedInstanceCore",
                ],
            )
            access_entries = {
                "nodes": {
                    "type": "EC2_LINUX",
                    "username": "system:node:{{EC2PrivateDNSName}}",
                    "principal_arn": f"arn:aws:iam::{self.account}:role/{cluster_name}-node-group-role",
                },
            }
            addon_overrides = config.get("addon_versions", {})
            addons = {
                "kube-proxy": build_addon_config("kube-proxy", addon_overrides),
                "vpc-cni": build_addon_config("vpc-cni", addon_overrides),
                "eks-pod-identity-agent": build_addon_config(
                    "eks-pod-identity-agent", addon_overrides
                ),
                "eks-node-monitoring-agent": build_addon_config(
                    "eks-node-monitoring-agent", addon_overrides
                ),
            }
        else:
            compute_config = {"enabled": True}
            access_entries = {
                "nodes": {
                    "type": "EC2",
                    "principal_arn": f"arn:aws:iam::{self.account}:role/{cluster_name}-node-group-role",
                    "policy_associations": {
                        "NodePolicy": {
                            "policy_arn": "arn:aws:eks::aws:cluster-access-policy/AmazonEKSAutoNodePolicy",
                            "access_scope": {"type": "cluster"},
                        },
                    },
                },
            }
            addons = {}

        # create eks cluster
        my_eks = Eks(
            self,
            "my_eks",
            name=cluster_name,
            kubernetes_version=config["cluster_version"],
            addons=addons,
            access_entries=access_entries,
            create_node_iam_role=True,
            node_iam_role_name=f"{cluster_name}-node-group-role",
            node_iam_role_use_name_prefix=False,
            node_iam_role_additional_policies={
                "SSM": "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
                "EFS": efs_policy.arn,
            },
            compute_config=compute_config,
            vpc_id=vpc_id,
            subnet_ids=subnet_ids,
            authentication_mode="API_AND_CONFIG_MAP",
            enable_irsa=enable_irsa,
            enabled_log_types=config["cluster_log_types"],
            cloudwatch_log_group_retention_in_days=config["log_retention"],
            endpoint_private_access=enable_private_access,
            endpoint_public_access=enable_public_access,
            create_security_group=False,
            security_group_id=all_vpc_traffic_sg.id,
            enable_cluster_creator_admin_permissions=True,
            iam_role_additional_policies={
                "AmazonEKSClusterPolicy": "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy",
                "AmazonEKSVPCResourceController": "arn:aws:iam::aws:policy/AmazonEKSVPCResourceController",
                "AmazonEBSCSIDriverPolicy": "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy",
            },
            tags=self.global_tags,
        )

        if config["dependencies"].get("neptune-db"):
            session = get_deployer_target_session(
                self.hmd_region, self.profile, self.account
            )
            neptune_sg = get_neptune_security_group(
                session,
                dp_config["neptune-db.instance_name"],
                dp_config["neptune-db.repo_name"],
                dp_config["neptune-db.deployment_id"],
                self.environment,
                self.hmd_region,
                self.customer_code,
            )
            sg_rule = vpc.security_group_rule(
                name=f"{self.instance_name}_{self.repo_name}_{dp_config['neptune-db.instance_name']}-ingress",
                rule_type="ingress",
                security_group=neptune_sg,
                source_sg=my_eks.cluster_primary_security_group_id_output,
            )

        # output cluster endpoint
        TerraformOutput(self, "cluster_endpoint", value=my_eks.cluster_endpoint_output)
        TerraformOutput(
            self,
            "cluster_kubernetes_version",
            value=my_eks.cluster_version_output,
        )

        # NERD0004: report this cluster as the concrete aws.neuronsphere.io/eks-cluster
        # subtype (declared in meta-data/resources/eks-cluster.yaml, parent
        # kubernetes.neuronsphere.io/kubernetes-cluster) so a resource-typed
        # `kubernetes-cluster` dependency resolves against real cloud EKS the same way
        # it already resolves against the local k3s producer.
        if hasattr(self, "add_resource_output"):
            self.add_resource_output(
                cluster_name,
                {
                    "resource_namespace": "aws.neuronsphere.io",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                },
                {
                    "cluster_name": my_eks.cluster_name_output,
                    "endpoint": my_eks.cluster_endpoint_output,
                    "ca_certificate": my_eks.cluster_certificate_authority_data_output,
                    "oidc_provider_arn": my_eks.oidc_provider_arn_output,
                    "cluster_arn": my_eks.cluster_arn_output,
                    "oidc_issuer_url": my_eks.cluster_oidc_issuer_url_output,
                    "platform_version": my_eks.cluster_platform_version_output,
                },
            )
        else:
            logger.warning(
                "hmd-lib-cdktf does not provide HmdCdkTfStack.add_resource_output "
                "(older version installed) — skipping NERD0004 Resource output emission."
            )

        clw = Cloudwatch(self)
        cloudwatch_log_group = clw.get_log_group(
            f"/aws/eks/{cluster_name}/cluster", [my_eks]
        )

    def _validate_cluster_version_step(self, config: dict):
        # EKS only supports one minor at a time forward; downgrades are unsupported.
        # Fail before any AWS resource is constructed so a multi-minor config edit
        # cannot apply unrelated IAM/SG diffs before the EKS API rejects it.
        target_str = config["cluster_version"]
        try:
            target_major, target_minor = (int(x) for x in target_str.split("."))
        except (ValueError, AttributeError):
            raise ValueError(
                f"cluster_version must be 'MAJOR.MINOR' (got {target_str!r})"
            )

        if config.get("skip_version_step_check", False):
            return

        session = get_deployer_target_session(
            self.hmd_region, self.profile, self.account
        )
        eks = session.client("eks", region_name=get_cloud_region(self.hmd_region))
        cluster_name = f"{self.instance_name}_{self.repo_name}"
        try:
            current = eks.describe_cluster(name=cluster_name)["cluster"]["version"]
        except eks.exceptions.ResourceNotFoundException:
            return
        cur_major, cur_minor = (int(x) for x in current.split("."))
        if (target_major, target_minor) == (cur_major, cur_minor):
            return
        if target_major != cur_major:
            raise ValueError(
                f"EKS major version change not supported "
                f"(current {current} -> target {target_str})"
            )
        if target_minor < cur_minor:
            raise ValueError(
                f"EKS downgrade not supported by AWS "
                f"(current {current} -> target {target_str}). "
                f"Use blue/green via a new instance_name to roll back."
            )
        if target_minor - cur_minor > 1:
            raise ValueError(
                f"EKS supports one minor at a time. "
                f"Current {current}, target {target_str}. "
                f"Upgrade to {cur_major}.{cur_minor + 1} first, verify, then "
                f"re-deploy with cluster_version={cur_major}.{cur_minor + 2}."
            )

    def _retry_k8s(self, fn, *args, attempts=10, backoff=6, **kwargs):
        # Retry across control-plane flapping during a minor bump (1-5 min of
        # intermittent 5xx as the API server is replaced). 404 is semantic to
        # callers and must propagate immediately.
        last = None
        for _ in range(attempts):
            try:
                return fn(*args, **kwargs)
            except ApiException as e:
                if e.status == 404:
                    raise
                last = e
                if e.status is None or e.status >= 500:
                    time.sleep(backoff)
                    continue
                raise
        raise last

    # adds hmd deployer user to eks config map
    def _do_update_aws_auth(self, k8s_client, role_to_map, hmd_deployer):
        do_patch = True
        try:
            aws_auth = self._retry_k8s(
                k8s_client.read_namespaced_config_map, "aws-auth", "kube-system"
            )
            mapped_roles = safe_load(aws_auth.data["mapRoles"])
        except ApiException as apie:
            if apie.status != 404:
                raise apie
            do_patch = False
            aws_auth = BASE_AWS_AUTH
            mapped_roles = []

        # Define expected roles with their exact configuration
        expected_roles = [
            {
                "groups": ["system:bootstrappers", "system:nodes"],
                "username": "system:node:{{EC2PrivateDNSName}}",
                "rolearn": f"arn:aws:iam::{self.account}:role/{self.cluster_name}-node-group-role",
            },
            {
                "groups": ["system:masters"],
                "rolearn": role_to_map,
                "username": f"{hmd_deployer}",
            },
        ]

        # Collect rolearns that we expect to manage
        managed_rolearns = {role["rolearn"] for role in expected_roles}

        # Remove any existing roles with managed rolearns (these will be replaced with expected versions)
        filtered_roles = [
            role
            for role in mapped_roles
            if role.get("rolearn", "") not in managed_rolearns
        ]

        # Add all expected roles
        filtered_roles.extend(expected_roles)

        aws_auth.data["mapRoles"] = safe_dump(filtered_roles)

        if do_patch:
            self._retry_k8s(
                k8s_client.patch_namespaced_config_map,
                "aws-auth",
                "kube-system",
                aws_auth,
            )
        else:
            self._retry_k8s(
                k8s_client.create_namespaced_config_map, "kube-system", aws_auth
            )

    # execute post deployment tasks
    def do_post_deploy(self):
        session = get_deployer_target_session(
            self.hmd_region, self.profile, self.account
        )
        role_to_map = f"arn:aws:iam::{self.account}:role/{HMD_NEURONSPHERE_DEPLOY}"
        deployer_account = get_account_number(
            get_session(get_cloud_region(self.hmd_region), self.profile)
        )
        with create_kubeconfig(
            self.instance_name,
            session,
            self.profile,
            get_cloud_region(self.hmd_region),
            self.repo_name,
            assume_role=deployer_account != self.account,
        ) as kubeconfig:
            config.load_kube_config(kubeconfig)
        self._do_update_aws_auth(
            client.CoreV1Api(), role_to_map, HMD_NEURONSPHERE_DEPLOY
        )
        # adds resource controller configmap to enable windows ipam (if not already created)
        ks_config_maps = self._retry_k8s(
            client.CoreV1Api().list_namespaced_config_map, "kube-system"
        ).items
        vpc_resource_controller_cm = [
            cm for cm in ks_config_maps if cm.metadata.name == "amazon-vpc-cni"
        ]
        if len(vpc_resource_controller_cm) == 0:
            self._retry_k8s(
                client.CoreV1Api().create_namespaced_config_map,
                "kube-system",
                VPC_RESOURCE_CONTROLLER,
            )
