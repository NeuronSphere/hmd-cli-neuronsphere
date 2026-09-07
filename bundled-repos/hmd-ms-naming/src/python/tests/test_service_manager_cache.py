from unittest.mock import MagicMock

import pytest

from hmd_ms_naming.service_manager import ServiceManager


@pytest.fixture
def services_client():
    client = MagicMock()
    fake_deployment_service = MagicMock()
    fake_deployment_service.httpEndpoint = "http://deployment.local"
    fake_deployment_service.arnEndpoint = (
        "arn:aws:lambda:us-east-1:0:function:deployment"
    )
    client.search_service_hmd_lang_naming.return_value = [fake_deployment_service]
    return client


def test_deployment_client_cached_after_first_call(monkeypatch, services_client):
    monkeypatch.setenv("HMD_LOCAL_ENV", "1")
    monkeypatch.setenv("AUTHORIZATION", "NONE")

    sm = ServiceManager(services_client, deployment_loader=MagicMock())

    first = sm._get_deployment_client()
    second = sm._get_deployment_client()

    assert first is second
    assert services_client.search_service_hmd_lang_naming.call_count == 1


def test_deployment_client_retries_when_initial_lookup_returns_none(monkeypatch):
    monkeypatch.setenv("HMD_LOCAL_ENV", "1")
    monkeypatch.setenv("AUTHORIZATION", "NONE")

    fake_deployment_service = MagicMock()
    fake_deployment_service.httpEndpoint = "http://deployment.local"

    client = MagicMock()
    client.search_service_hmd_lang_naming.side_effect = [
        [],
        [fake_deployment_service],
    ]

    sm = ServiceManager(client, deployment_loader=MagicMock())

    assert sm._get_deployment_client() is None
    resolved = sm._get_deployment_client()

    assert resolved is not None
    assert client.search_service_hmd_lang_naming.call_count == 2
