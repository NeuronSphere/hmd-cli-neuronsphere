"""Floci-safe local CDKTF stack for hmd-vpc.

The real ``src/cdktf/cdktf_local.py`` wraps ``terraform-aws-modules/aws/vpc``
(public, private and database subnet tiers, a NAT gateway and its EIP, route
tables and internet gateway routes, VPC flow logs to a GRC S3 bucket) and pairs
it with a Route53 Resolver query-log config and association.

Most of that has no meaning against Floci, and one part of it cannot run at all:

- ``Route53Resolver`` is **not implemented** by Floci 2.0.1 --
  ``ListResolverQueryLogConfigs`` answers ``UnknownOperationException`` -- so the
  query-log config and its association are dropped rather than stubbed.
- Flow logs and the query-log config both write to
  ``<customer>.<grc account>.grc.<region>.logs``, an S3 bucket in a GRC account
  that does not exist locally. The ``organization-metadata`` secret the bucket
  name is derived from does not exist either.
- NAT gateways and internet-gateway routes model egress from private subnets to
  the internet. Locally every workload is a container on one Docker network and
  reaches the outside through the host, so a NAT gateway would be an emulated
  object nothing routes through. Floci does implement the API -- verified live,
  including rejecting a bogus allocation id -- so this is dropped as meaningless
  rather than as unsupported.

What remains is what the rest of the local platform actually consumes: a VPC,
subnets across two availability zones, and a DB subnet group. Bare resources
rather than the module, following ``hmd-inf-eks-cluster``'s local overlay --
matching that repo's judgement that reproducing a large community module against
an emulator is a worse bet than expressing the handful of resources directly.

This replaces a workaround rather than adding a deploy. Until now the VPC,
subnets and DB subnet group were created imperatively by the CLI
(``floci_deployer.ensure_rds_subnet_group``), working around a Floci bug where
``Ec2Service.ensureDefaultResources`` guards on region alone while its VPC
storage is per-account -- so the first account to touch EC2 marks the region
seeded and every other account is left with no default VPC, and RDS then fails
``CreateDBInstance`` with ``InvalidVPCNetworkStateFault: No subnets available for
DB subnet group default``. Creating them here instead means:

- ``base-vpc`` resolves to a real VPC-producing RepoInstance rather than to the
  core instance declaring a type it does not create;
- ``hmd-inf-eks-cluster``'s overlay keeps finding seeded subnets by boto3, which
  its own docstring notes it depends on -- now ordered by a declared BOM
  dependency rather than by which entry happens to run first;
- the CLI needs no EC2 client at all.
"""

import logging

from constructs import Construct

from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
from hmd_lib_cdktf.terraform_imports.aws.vpc import Vpc
from hmd_lib_cdktf.terraform_imports.aws.subnet import Subnet
from hmd_lib_cdktf.terraform_imports.aws.db_subnet_group import DbSubnetGroup

logger = logging.getLogger(__name__)

# The CIDR the local VPC is created with. Deliberately the same 172.31.0.0/16 the
# CLI's imperative workaround used, so an environment created before this repo
# was deployed keeps the addresses its existing subnets already carry.
LOCAL_CIDR = "172.31.0.0/16"

# Two zones. A single-AZ group is enough for the instances deployed here, but a
# multi-AZ one costs nothing and keeps `multi_az` usable later -- the same reason
# the CLI's workaround created two.
LOCAL_SUBNETS = [
    ("a", "172.31.0.0/20"),
    ("b", "172.31.16.0/20"),
]

# The named group the local overlays place instances in. RDS's implicit
# "default" group is built by listing the subnets of ``vpc-default-<region>`` in
# the calling account, which Floci never seeds outside the first account -- so a
# named group is the only one that resolves.
LOCAL_DB_SUBNET_GROUP = "hmd-local-db-subnets"


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

        cidr = config.get("cidr", LOCAL_CIDR)
        # The AWS region the zones are named from. Floci's own default, not the
        # HMD region (`reg1`), which is a NeuronSphere concept with no zones.
        region = config.get("aws_region", "us-west-2")

        vpc = Vpc(
            self,
            "base-vpc",
            cidr_block=cidr,
            enable_dns_hostnames=True,
            enable_dns_support=True,
            tags={**self.global_tags, "Name": self.base_name},
        )

        subnet_ids = []
        for suffix, subnet_cidr in LOCAL_SUBNETS:
            subnet = Subnet(
                self,
                f"base-subnet-{suffix}",
                vpc_id=vpc.id,
                cidr_block=subnet_cidr,
                availability_zone=f"{region}{suffix}",
                tags={**self.global_tags, "Name": f"{self.base_name}-{suffix}"},
            )
            subnet_ids.append(subnet.id)

        DbSubnetGroup(
            self,
            "db-subnet-group",
            name=LOCAL_DB_SUBNET_GROUP,
            description="Local NeuronSphere DB subnet group",
            subnet_ids=subnet_ids,
            tags=self.global_tags,
        )
