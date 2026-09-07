"""Floci-safe local CDKTF stack for hmd-inf-ext-secrets.

The real ``src/cdktf/cdktf_local.py`` provisions AWS-only infrastructure for the
external-secrets operator: it calls EKS ``DescribeCluster`` to derive the cluster
OIDC provider, then creates an IAM (IRSA) role the operator's ServiceAccount assumes,
plus a Secrets Manager image-pull secret (``hmd-docker-repo-secret``, built from
``docker_username``/``docker_password`` deploy config).

Most of that doesn't apply to the local k3s cluster:
- IRSA / ``AssumeRoleWithWebIdentity`` is an EKS-OIDC feature k3s doesn't provide.
- ``EKS DescribeCluster`` against a local k3s cluster isn't emulated by Floci.
- The operator's own image (``oci.external-secrets.io/...``) is public, so *it*
  needs no image-pull secret.

The ``hmd-docker-repo-secret`` itself, though, **is** still needed locally: it's
consumed by other BOM entries (``clickhouse``, ``otel-collector``) pulling private
``ghcr.io/hmdlabs/*`` images through real k3s/containerd -- Extend mode's local
cluster hits the same anonymous-pull 401 cloud would if this secret didn't exist.
So this overlay creates just that secret (skipping the IRSA role entirely), using
credentials harvested from the *developer's own host* Docker credential store
(``hmd-cli-neuronsphere``'s ``docker_credentials.local_docker_config_json``,
threaded through the local BOM's ``instance_configuration`` as
``docker_config_json`` -- see ``bom_seeder._inject_docker_credentials``) rather than
requiring separate local-only credentials to be configured.

Applied by LocalWorkflowRunner (``src/local/cdktf`` overlays ``src/cdktf``) only in
local mode; the cloud deploy (``src/cdktf/cdktf_local.py``) is unaffected.
"""

from json import dumps

from constructs import Construct

from hmd_cli_tools.hmd_cli_tools import get_deployer_target_session
from hmd_lib_cdktf.hmd_lib_cdktf import HmdCdkTfStack
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
        # Intentionally no IAM/IRSA resources: EKS OIDC doesn't apply on local k3s.

        docker_config_json = config.get("docker_config_json")
        if not docker_config_json:
            print(
                "No 'docker_config_json' in local deploy config -- skipping "
                "hmd-docker-repo-secret. Private image pulls (e.g. "
                "ghcr.io/hmdlabs/*) from local k3s will fail until the developer "
                "host has Docker credentials `hmd neuronsphere up` can harvest "
                "(run `docker login <registry>`)."
            )
            return

        session = get_deployer_target_session(self.hmd_region, self.profile, self.account)
        create_secret(
            session,
            "hmd-docker-repo-secret",
            {"DOCKER_SECRET": docker_config_json},
            exists_ok=True,
        )
