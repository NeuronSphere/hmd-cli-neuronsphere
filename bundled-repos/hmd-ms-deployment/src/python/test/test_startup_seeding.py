"""Tests for automatic base ResourceDefinition catalog seeding at service startup.

Covers `resource_information.seed_base_catalog_best_effort`, called from
`deployment_ops.setup()` so cloud environments -- which never run
`hmd neuronsphere up` -- self-seed the base catalog (NERD0004) on every service cold
start / app init. Seeding logic itself (`seed_base_resource_definitions`) is already
covered end-to-end in test_base_resources.py; these tests exercise only the
best-effort wrapper: happy path, idempotency, and failure containment.
"""

from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment.resource_information import seed_base_catalog_best_effort
from hmd_schema_loader import DefaultLoader

EXPECTED_BASE_COUNT = 21


def make_client():
    loader = DefaultLoader("schemas/local")
    mem_client = MemoryClient(loader, {})
    return HmdLangDeploymentClient(mem_client)


def test_seeds_full_catalog_on_first_call():
    client = make_client()
    seeded = seed_base_catalog_best_effort(client)
    assert seeded is not None and len(seeded) == EXPECTED_BASE_COUNT
    assert (
        len(client.search_resource_definition_hmd_lang_deployment({}))
        == EXPECTED_BASE_COUNT
    )


def test_idempotent_across_repeated_cold_starts():
    client = make_client()
    seed_base_catalog_best_effort(client)
    second = seed_base_catalog_best_effort(client)
    assert len(second) == EXPECTED_BASE_COUNT
    assert (
        len(client.search_resource_definition_hmd_lang_deployment({}))
        == EXPECTED_BASE_COUNT
    )


def test_underlying_client_failure_is_swallowed(monkeypatch):
    client = make_client()

    def _boom(*args, **kwargs):
        raise RuntimeError("graph db unavailable")

    monkeypatch.setattr(client, "search_resource_definition_hmd_lang_deployment", _boom)

    result = seed_base_catalog_best_effort(client)  # must not raise

    assert result is None
