"""Floci-safe local CDKTF stack for hmd-inf-eks-cluster.

The real ``src/cdktf/cdktf_local.py`` wraps ``terraform-aws-modules/eks`` (IAM
control-plane and node-group roles, IRSA + an ``aws_iam_openid_connect_provider``
backed by a live TLS dial to the cluster's OIDC issuer, four ``aws_eks_addon``s,
EKS access entries, a CloudWatch log group) and finishes with ``do_post_deploy``
patching the ``aws-auth`` ConfigMap via ``aws eks get-token`` exec-plugin auth.

None of that applies against Floci's k3s-backed EKS emulation:

- IRSA / OIDC discovery, EKS access entries, and ``aws eks get-token`` exec auth
  are all EKS-API features a plain k3s API server doesn't implement (or, for the
  first two, that Floci doesn't emulate even against its own EKS facade).
- k3s ships its own kube-proxy/flannel/CoreDNS; the ``vpc-cni``/`eks-pod-identity-
  agent`/etc. addons are meaningless and imply a live ``DescribeAddonVersions``
  call this environment can't answer usefully.
- The ``aws-auth`` ConfigMap and the ``amazon-vpc-cni`` ConfigMap patched in
  ``do_post_deploy`` are EKS/VPC-CNI-specific; k3s auth and networking don't
  read them.

So this overlay creates a single bare ``aws_eks_cluster`` -- confirmed live
against a running Floci 2.0.1 to round-trip cleanly (clean create, no-op
re-apply, and a correct recreate-plan after an out-of-band delete) -- which is
exactly the ``eks:CreateCluster`` call that makes Floci spawn the real k3s
container this environment's Kubernetes workloads actually run on, identically
to how ``floci_deployer.ensure_k3s_cluster`` did it directly via boto3 before
this repo was deployed as a DAG node. `hmd-cli-neuronsphere` keeps everything
about that container's Docker lifecycle (stopped/restart, stale-image
recreation, purge) -- none of it is visible to, or expressible as, a Terraform
resource; only cluster *creation* moves here.

One thing this DID surface that the boto3 call got away with: Floci now
validates ``vpc_config.subnet_ids`` against real EC2 state, and the AWS
provider's schema separately requires at least one subnet id (the empty-list
``resourcesVpcConfig`` the CLI's boto3 call used is rejected by the provider
before a request is even made). This overlay looks up the account's
already-seeded subnets via boto3 -- safe because the ``environment-db`` BOM
entry (``hmd-postgres-rds``, via ``floci_deployer.ensure_rds_subnet_group``)
always runs earlier in the same Phase A changeset and seeds them first.
"""

import logging

from constructs import Construct

from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_lib_cdktf.terraform_imports.aws.eks_cluster import (
    EksCluster,
    EksClusterVpcConfig,
)
from hmd_cli_tools.hmd_cli_tools import get_cloud_region, get_session

logger = logging.getLogger(__name__)


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

        # `cluster_name`/`cluster_endpoint` are injected into
        # instance_configuration by bom_seeder._inject_eks_cluster_endpoint --
        # the account-qualified container-naming logic
        # (floci_deployer.k3s_container_name) lives once, in the CLI, and this
        # overlay only reads what it computed. Falls back to a standard name so
        # a standalone `hmd deploy --local` (no environment context) still synths.
        cluster_name = config.get("cluster_name") or f"{instance_name}-{repo_name}"
        self.cluster_name = cluster_name

        session = get_session(get_cloud_region(hmd_region), profile)
        ec2 = session.client("ec2", region_name=get_cloud_region(hmd_region))
        subnet_ids = [
            sn["SubnetId"] for sn in ec2.describe_subnets().get("Subnets", [])
        ]
        if not subnet_ids:
            raise RuntimeError(
                f"No subnets available in account {self.account} to place the "
                f"local EKS cluster in -- expected environment-db's "
                f"ensure_rds_subnet_group to have seeded the account's VPC "
                f"before this Phase A node runs."
            )

        cluster = EksCluster(
            self,
            "eks_cluster",
            name=cluster_name,
            role_arn=f"arn:aws:iam::{self.account}:role/eks-role",
            version=config["cluster_version"],
            vpc_config=EksClusterVpcConfig(subnet_ids=subnet_ids),
        )

        # Floci does not implement ``UpdateClusterConfig`` -- the request is
        # routed to S3 and comes back as an S3 XML error, which the SDK then
        # fails to deserialize ("invalid character '<' looking for beginning of
        # value"). So any diff on ``vpc_config`` is unappliable, and the cluster
        # cannot be repaired except by deleting and recreating it, which throws
        # away the k3s datastore and every Helm release on it.
        #
        # A diff is easy to produce: the subnet ids above are *discovered*, so
        # an account that gains or loses a subnet after the cluster was created
        # plans an update. That happens for real when an environment created
        # before ``hmd-vpc`` owned the network is migrated onto it -- the new
        # VPC's subnets are not the ones the existing cluster recorded.
        #
        # Ignoring the change is right rather than merely convenient: Floci
        # backs this cluster with a k3s container on the Docker network, which
        # does not route through the subnets either way, so the recorded ids
        # have no effect on anything the cluster actually does.
        cluster.add_override("lifecycle", {"ignore_changes": ["vpc_config"]})

        endpoint = config.get("cluster_endpoint") or cluster.endpoint

        if hasattr(self, "add_resource_output"):
            self.add_resource_output(
                cluster_name,
                {
                    "resource_namespace": "kubernetes.neuronsphere.io",
                    "resource_definition_name": "kubernetes-cluster",
                    "version": "0.1.0",
                },
                {
                    "cluster_name": cluster_name,
                    "endpoint": endpoint,
                },
            )
        else:
            logger.warning(
                "hmd-lib-cdktf does not provide HmdCdkTfStack.add_resource_output "
                "(older version installed) — skipping NERD0004 Resource output emission."
            )
