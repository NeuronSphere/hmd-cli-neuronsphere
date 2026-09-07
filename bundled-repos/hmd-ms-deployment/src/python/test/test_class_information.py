from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_schema_loader import DefaultLoader

from hmd_ms_deployment.class_information import ClassInformation


def _new_client():
    loader = DefaultLoader("schemas/local")
    return HmdLangDeploymentClient(MemoryClient(loader, {}))


def test_add_repo_version_by_name_stores_discovery():
    client = _new_client()
    class_info = ClassInformation(client)

    discovery = {
        "summary": "A test repo class.",
        "entry_points": [{"path": "src/foo.py", "description": "Entry point."}],
        "capabilities": [
            {"name": "do_thing", "kind": "function", "description": "Does a thing."}
        ],
        "related_docs": [{"title": "Docs", "path": "docs/index.rst"}],
    }

    class_info.add_repo_version_by_name(
        "rc1", "0.1.0", {}, {"config": "value"}, discovery=discovery
    )

    rcv = class_info.get_repo_class_version("rc1", "0.1.0")
    assert rcv.discovery == discovery


def test_add_repo_version_by_name_discovery_defaults_to_none():
    client = _new_client()
    class_info = ClassInformation(client)

    class_info.add_repo_version_by_name("rc1", "0.1.0", {}, {"config": "value"})

    rcv = class_info.get_repo_class_version("rc1", "0.1.0")
    assert rcv.discovery is None
