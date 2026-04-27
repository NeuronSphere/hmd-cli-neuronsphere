"""HMDMS-service seeder for ms-deployment.

For each HMDMS-service plugin discovered at startup:

1. Registers the repo class + version (with manifest dependencies and
   default_configuration) via the ``add_repo_class_version`` apiop.
2. Creates a ``hmd_lang_deployment.repo_instance`` named after the Lambda.
3. Creates a ``hmd_lang_deployment.repo_instance_deployment`` with
   ``status="DEPLOYED"`` and ``deployment_image=<repo>:<version>``.
4. Seeds an ``hmd-inf-s3bucket`` instance per declared bucket as DEPLOYED
   (Floci really did create them).
5. For each manifest dependency that is NOT locally deployed (no matching
   HMDMS service plugin and not a pre-known local instance), creates a
   SKIPPED deployment record so dependency resolution succeeds.

Idempotent — safe to re-run on every ``hmd ns up``.
"""

from typing import Dict, Iterable, List, Optional, Set

from cement import minimal_logger

from . import _rest

logger = minimal_logger("hmdms_seeder")

LOCAL_DEPLOYMENT_ID = "local"

# Repo classes that are present locally even though no HMDMS plugin declares them.
# Buckets are also added dynamically below.
ALWAYS_LOCAL_REPO_CLASSES: Set[str] = {"hmd-inf-s3bucket"}


def _instance_config_for(spec: Dict) -> Dict:
    """Build instance_configuration for a librarian's deployment record.

    Includes ``repo_class_name`` so callers/tests can match by it without
    needing to traverse relationships.
    """
    manifest = spec.get("manifest") or {}
    default_config = manifest.get("deploy", {}).get("default_configuration", {})
    merged = {
        **default_config,
        "service_config": spec.get("merged_service_config", {}),
        "repo_class_name": spec.get("repo_class_name"),
        "lambda_name": spec.get("function_name"),
    }
    return merged


def _seed_repo_class_version(
    base_url: str,
    repo_class_name: str,
    version: str,
    dependencies: Optional[Dict] = None,
    default_configuration: Optional[Dict] = None,
) -> None:
    """Register a repo_class with its version via add_repo_class_version apiop."""
    _rest.post_apiop_idempotent(
        base_url,
        "add_repo_class_version",
        {
            "repo_class_name": repo_class_name,
            "version": version,
            "dependencies": dependencies or {},
            "default_configuration": default_configuration or {},
        },
    )


def _seed_instance_and_deployment(
    base_url: str,
    instance_name: str,
    instance_configuration: Dict,
    status: str,
    deployment_image: Optional[str] = None,
) -> None:
    """Create repo_instance and repo_instance_deployment with the given status."""
    _rest.put_entity_idempotent(
        base_url,
        "hmd_lang_deployment.repo_instance",
        {"name": instance_name, "auto_deploy": "false"},
    )
    deployment_data = {
        "deployment_id": LOCAL_DEPLOYMENT_ID,
        "instance_configuration": instance_configuration,
        "status": status,
    }
    if deployment_image:
        deployment_data["deployment_image"] = deployment_image
    _rest.put_entity_idempotent(
        base_url,
        "hmd_lang_deployment.repo_instance_deployment",
        deployment_data,
    )


def seed_hmdms_services(base_url: str, deployed_specs: Iterable[Dict]) -> None:
    """Seed ms-deployment with each HMDMS service and its (possibly-mocked) deps.

    :param base_url: ms-deployment base URL.
    :param deployed_specs: Iterable of specs as returned by
        :func:`hmd_cli_neuronsphere._deploy_hmdms_service_lambdas`.
    """
    deployed_specs = list(deployed_specs)
    if not deployed_specs:
        return

    locally_deployed: Set[str] = set(ALWAYS_LOCAL_REPO_CLASSES)
    for spec in deployed_specs:
        locally_deployed.add(spec["repo_class_name"])

    for spec in deployed_specs:
        repo_class_name = spec["repo_class_name"]
        version = spec["repo_class_version"]
        manifest = spec.get("manifest") or {}
        deploy_block = manifest.get("deploy", {})
        manifest_deps = deploy_block.get("dependencies", {}) or {}
        default_config = deploy_block.get("default_configuration", {}) or {}

        logger.info(f"Seeding HMDMS service {repo_class_name}@{version}")

        _seed_repo_class_version(
            base_url,
            repo_class_name,
            version,
            dependencies=manifest_deps,
            default_configuration=default_config,
        )

        _seed_instance_and_deployment(
            base_url,
            instance_name=spec["function_name"],
            instance_configuration=_instance_config_for(spec),
            status="DEPLOYED",
            deployment_image=spec.get("image"),
        )

        # Each declared bucket is a real, deployed hmd-inf-s3bucket instance.
        for bucket in (
            spec.get("buckets")
            or manifest.get("hmdms_service", {}).get("buckets")
            or []
        ):
            bucket_name = bucket.get("name") if isinstance(bucket, dict) else None
            if not bucket_name:
                continue
            _seed_repo_class_version(base_url, "hmd-inf-s3bucket", "0.1")
            _seed_instance_and_deployment(
                base_url,
                instance_name=bucket_name,
                instance_configuration={
                    "repo_class_name": "hmd-inf-s3bucket",
                    "bucket_name": bucket_name,
                },
                status="DEPLOYED",
            )

        # Mock manifest dependencies that aren't deployed locally.
        for dep_role, dep_info in manifest_deps.items():
            if not isinstance(dep_info, dict):
                continue
            dep_class = dep_info.get("repo_class_name")
            if not dep_class or dep_class in locally_deployed:
                continue
            mock_instance_name = dep_class
            logger.info(f"Mocking dependency '{dep_role}' -> {dep_class} as SKIPPED")
            _seed_repo_class_version(
                base_url, dep_class, dep_info.get("version_spec", "0.1")
            )
            _seed_instance_and_deployment(
                base_url,
                instance_name=mock_instance_name,
                instance_configuration={
                    "repo_class_name": dep_class,
                    "mocked": True,
                    "for": repo_class_name,
                    "role": dep_role,
                },
                status="SKIPPED",
            )

    logger.info(f"Finished seeding {len(deployed_specs)} HMDMS service(s)")
