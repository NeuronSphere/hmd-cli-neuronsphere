"""Resource name parsing and generation utilities for NERD0002.

This module provides support for deployment resource names in multiple formats:
- Full: ns:<customer_code>:<environment>:<repo_class_name>:<repo_class_version>:<instance_name>:<deployment_id>
- Shorthand: ns:<instance_name>:<deployment_id>
- Plain: <instance_name> (backwards compatible)
"""

import logging
from dataclasses import dataclass
from typing import Optional, Union

logger = logging.getLogger(f"HMD.{__name__}")


@dataclass
class ResourceName:
    """Parsed resource name components."""

    instance_name: str
    deployment_id: Optional[str] = None
    customer_code: Optional[str] = None
    environment: Optional[str] = None
    repo_class_name: Optional[str] = None
    repo_class_version: Optional[str] = None

    def is_full(self) -> bool:
        """Check if this is a full resource name."""
        return all(
            [
                self.customer_code,
                self.environment,
                self.repo_class_name,
                self.repo_class_version,
                self.deployment_id,
            ]
        )

    def is_shorthand(self) -> bool:
        """Check if this is a shorthand resource name."""
        return (
            self.deployment_id is not None
            and not self.customer_code
            and not self.environment
            and not self.repo_class_name
            and not self.repo_class_version
        )

    def is_plain(self) -> bool:
        """Check if this is a plain instance name."""
        return self.deployment_id is None


def parse_resource_name(resource_name: str) -> ResourceName:
    """Parse a resource name in any supported format.

    Supported formats:
    - Full: ns:<customer_code>:<environment>:<repo_class_name>:<repo_class_version>:<instance_name>:<deployment_id>
    - Shorthand: ns:<instance_name>:<deployment_id>
    - Plain: <instance_name> (backwards compatible)

    Args:
        resource_name: The resource name string to parse

    Returns:
        ResourceName object with parsed components

    Raises:
        ValueError: If the resource name format is invalid
    """
    if not resource_name:
        raise ValueError("Resource name cannot be empty")

    # Plain instance name (backwards compatible)
    if not resource_name.startswith("ns:"):
        return ResourceName(instance_name=resource_name)

    # Remove "ns:" prefix
    parts = resource_name[3:].split(":")

    # Shorthand: ns:<instance_name>:<deployment_id>
    if len(parts) == 2:
        return ResourceName(instance_name=parts[0], deployment_id=parts[1])

    # Full: ns:<customer_code>:<environment>:<repo_class_name>:<repo_class_version>:<instance_name>:<deployment_id>
    if len(parts) == 6:
        return ResourceName(
            customer_code=parts[0],
            environment=parts[1],
            repo_class_name=parts[2],
            repo_class_version=parts[3],
            instance_name=parts[4],
            deployment_id=parts[5],
        )

    raise ValueError(
        f"Invalid resource name format: {resource_name}. "
        f"Expected 'ns:<instance_name>:<deployment_id>' or "
        f"'ns:<customer_code>:<environment>:<repo_class_name>:<repo_class_version>:<instance_name>:<deployment_id>' "
        f"or plain '<instance_name>'"
    )


def generate_resource_name(
    instance_name: str,
    deployment_id: str,
    customer_code: Optional[str] = None,
    environment: Optional[str] = None,
    repo_class_name: Optional[str] = None,
    repo_class_version: Optional[str] = None,
    shorthand: bool = False,
) -> str:
    """Generate a resource name for a RepoInstanceDeployment.

    Args:
        instance_name: The instance name
        deployment_id: The deployment ID
        customer_code: Customer code (required for full format)
        environment: Environment type (required for full format)
        repo_class_name: RepoClass name (required for full format)
        repo_class_version: RepoClass version (required for full format)
        shorthand: If True, generate shorthand format even if full info available

    Returns:
        Resource name string in the requested format

    Raises:
        ValueError: If required components are missing for full format
    """
    if not instance_name or not deployment_id:
        raise ValueError("instance_name and deployment_id are required")

    # Shorthand format
    if shorthand or not all(
        [customer_code, environment, repo_class_name, repo_class_version]
    ):
        return f"ns:{instance_name}:{deployment_id}"

    # Full format
    return f"ns:{customer_code}:{environment}:{repo_class_name}:{repo_class_version}:{instance_name}:{deployment_id}"


def generate_resource_name_from_deployment(
    repo_instance_deployment,
    repo_instance,
    repo_class_version,
    environment,
    customer_code: str,
    shorthand: bool = False,
) -> str:
    """Generate a resource name from deployment entities.

    Args:
        repo_instance_deployment: RepoInstanceDeployment entity
        repo_instance: RepoInstance entity
        repo_class_version: RepoClassVersion entity
        environment: Environment entity
        customer_code: Customer code
        shorthand: If True, generate shorthand format

    Returns:
        Resource name string
    """
    from hmd_lang_deployment.repo_class import RepoClass

    return generate_resource_name(
        instance_name=repo_instance.name,
        deployment_id=repo_instance_deployment.deployment_id,
        customer_code=customer_code,
        environment=environment.type,
        repo_class_name=repo_class_version.repo_class_name,
        repo_class_version=repo_class_version.repo_class_version,
        shorthand=shorthand,
    )
