from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment.deploy_logic import build_deployment_dag
from hmd_ms_deployment.environment_information import EnvironmentInformation

from hmd_schema_loader import DefaultLoader


def make_client(data):
    loader = DefaultLoader("schemas/local")
    mem_client = MemoryClient(loader, data)

    client = HmdLangDeploymentClient(mem_client)
    return client


def test_build_deployment_tree(test_simple):
    client = make_client(test_simple)
    out = client._base_client.get_data()
    environ = EnvironmentInformation(client.search_environment_hmd_lang_deployment({})[0], client)

    result = build_deployment_dag(environ, [])

    assert 3 == len(result.nodes)
    children = [node for node in result.nodes.values() if len(node.children) == 0]
    parents = [node for node in result.nodes.values() if len(node.children) > 0]

    assert len(children) == 2
    assert len(parents) == 1

    assert all(
        child in parents[0].children for child in children
    ), "Children are in parent collection."

    print(result)

def test_build_destroy_tree(test_simple):
    client = make_client(test_simple)
    out = client._base_client.get_data()
    environ = EnvironmentInformation(client.search_environment_hmd_lang_deployment({})[0], client)

    result = build_deployment_dag(environ, [], True)

    assert 3 == len(result.nodes)
    children = [node for node in result.nodes.values() if len(node.children) == 0]
    parents = [node for node in result.nodes.values() if len(node.children) > 0]

    assert len(children) == 1
    assert len(parents) == 2

    assert all(
        child in parents[0].children for child in children
    ), "Children are in parent collection."

    assert all(
        child in parents[1].children for child in children
    ), "Children are in parent collection."

    print(result)

