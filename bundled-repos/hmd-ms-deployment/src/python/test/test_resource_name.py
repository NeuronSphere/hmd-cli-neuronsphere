"""Tests for resource_name module (NERD0002)."""

import pytest
from hmd_ms_deployment.resource_name import (
    parse_resource_name,
    generate_resource_name,
    ResourceName,
)


class TestParseResourceName:
    """Tests for parse_resource_name function."""

    def test_parse_plain_instance_name(self):
        """Test parsing a plain instance name (backwards compatible)."""
        result = parse_resource_name("my-instance")
        assert result.instance_name == "my-instance"
        assert result.deployment_id is None
        assert result.customer_code is None
        assert result.environment is None
        assert result.repo_class_name is None
        assert result.repo_class_version is None
        assert result.is_plain()
        assert not result.is_shorthand()
        assert not result.is_full()

    def test_parse_shorthand_resource_name(self):
        """Test parsing shorthand resource name: ns:<instance_name>:<deployment_id>."""
        result = parse_resource_name("ns:my-instance:aaa")
        assert result.instance_name == "my-instance"
        assert result.deployment_id == "aaa"
        assert result.customer_code is None
        assert result.environment is None
        assert result.repo_class_name is None
        assert result.repo_class_version is None
        assert not result.is_plain()
        assert result.is_shorthand()
        assert not result.is_full()

    def test_parse_full_resource_name(self):
        """Test parsing full resource name."""
        result = parse_resource_name(
            "ns:cust01:dev:hmd-vpc:0.1.20:base-vpc:aaa"
        )
        assert result.customer_code == "cust01"
        assert result.environment == "dev"
        assert result.repo_class_name == "hmd-vpc"
        assert result.repo_class_version == "0.1.20"
        assert result.instance_name == "base-vpc"
        assert result.deployment_id == "aaa"
        assert not result.is_plain()
        assert not result.is_shorthand()
        assert result.is_full()

    def test_parse_empty_resource_name(self):
        """Test parsing empty resource name raises ValueError."""
        with pytest.raises(ValueError, match="Resource name cannot be empty"):
            parse_resource_name("")

    def test_parse_invalid_ns_format(self):
        """Test parsing invalid ns: format raises ValueError."""
        with pytest.raises(ValueError, match="Invalid resource name format"):
            parse_resource_name("ns:invalid")

    def test_parse_invalid_ns_too_many_parts(self):
        """Test parsing ns: with wrong number of parts raises ValueError."""
        with pytest.raises(ValueError, match="Invalid resource name format"):
            parse_resource_name("ns:a:b:c:d:e")

    def test_parse_instance_name_with_hyphens(self):
        """Test parsing instance names with hyphens and numbers."""
        result = parse_resource_name("my-instance-123")
        assert result.instance_name == "my-instance-123"
        assert result.is_plain()

    def test_parse_shorthand_with_deployment_id_bbb(self):
        """Test parsing shorthand with different deployment_id."""
        result = parse_resource_name("ns:cluster-01:bbb")
        assert result.instance_name == "cluster-01"
        assert result.deployment_id == "bbb"
        assert result.is_shorthand()


class TestGenerateResourceName:
    """Tests for generate_resource_name function."""

    def test_generate_shorthand_resource_name(self):
        """Test generating shorthand resource name."""
        result = generate_resource_name(
            instance_name="my-instance",
            deployment_id="aaa",
            shorthand=True,
        )
        assert result == "ns:my-instance:aaa"

    def test_generate_shorthand_when_missing_info(self):
        """Test generating shorthand when full info not provided."""
        result = generate_resource_name(
            instance_name="my-instance",
            deployment_id="aaa",
            customer_code="cust01",
            # Missing other required fields
        )
        assert result == "ns:my-instance:aaa"

    def test_generate_full_resource_name(self):
        """Test generating full resource name."""
        result = generate_resource_name(
            instance_name="base-vpc",
            deployment_id="aaa",
            customer_code="cust01",
            environment="dev",
            repo_class_name="hmd-vpc",
            repo_class_version="0.1.20",
            shorthand=False,
        )
        assert result == "ns:cust01:dev:hmd-vpc:0.1.20:base-vpc:aaa"

    def test_generate_full_resource_name_production(self):
        """Test generating full resource name for production environment."""
        result = generate_resource_name(
            instance_name="db-credentials",
            deployment_id="aaa",
            customer_code="acme",
            environment="prod",
            repo_class_name="hmd-database-account",
            repo_class_version="0.1.15",
        )
        assert result == "ns:acme:prod:hmd-database-account:0.1.15:db-credentials:aaa"

    def test_generate_missing_instance_name(self):
        """Test generating without instance_name raises ValueError."""
        with pytest.raises(ValueError, match="instance_name and deployment_id are required"):
            generate_resource_name(
                instance_name="",
                deployment_id="aaa",
            )

    def test_generate_missing_deployment_id(self):
        """Test generating without deployment_id raises ValueError."""
        with pytest.raises(ValueError, match="instance_name and deployment_id are required"):
            generate_resource_name(
                instance_name="my-instance",
                deployment_id="",
            )

    def test_generate_shorthand_overrides_full_info(self):
        """Test that shorthand=True generates shorthand even with full info."""
        result = generate_resource_name(
            instance_name="base-vpc",
            deployment_id="aaa",
            customer_code="cust01",
            environment="dev",
            repo_class_name="hmd-vpc",
            repo_class_version="0.1.20",
            shorthand=True,
        )
        assert result == "ns:base-vpc:aaa"


class TestResourceNameRoundTrip:
    """Tests for parsing and generating resource names (round-trip tests)."""

    def test_round_trip_shorthand(self):
        """Test generating and parsing shorthand resource name."""
        original = "ns:my-instance:aaa"
        parsed = parse_resource_name(original)
        generated = generate_resource_name(
            instance_name=parsed.instance_name,
            deployment_id=parsed.deployment_id,
            shorthand=True,
        )
        assert original == generated

    def test_round_trip_full(self):
        """Test generating and parsing full resource name."""
        original = "ns:cust01:dev:hmd-vpc:0.1.20:base-vpc:aaa"
        parsed = parse_resource_name(original)
        generated = generate_resource_name(
            instance_name=parsed.instance_name,
            deployment_id=parsed.deployment_id,
            customer_code=parsed.customer_code,
            environment=parsed.environment,
            repo_class_name=parsed.repo_class_name,
            repo_class_version=parsed.repo_class_version,
        )
        assert original == generated

    def test_parse_plain_does_not_generate_ns_prefix(self):
        """Test that plain instance names stay plain."""
        parsed = parse_resource_name("my-instance")
        # Plain names don't have deployment_id, so can't generate ns: format
        assert parsed.is_plain()
        assert parsed.instance_name == "my-instance"
