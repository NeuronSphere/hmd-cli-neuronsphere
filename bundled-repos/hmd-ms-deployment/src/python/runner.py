from hmd_schema_loader import DefaultLoader
from hmd_graphql_client import RestClient
from hmd_ms_deployment.deploy_logic import build_deployment_tree
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
if __name__ == "__main__":
    local = "http://localhost/hmd_ms_deployment"
    jim_eng = "https://wss3gl6050.execute-api.us-west-2.amazonaws.com/env_model_vocab-hmd-vocab-server_api_stage"
    hmd_dev = "https://96zbe8954h.execute-api.us-west-2.amazonaws.com/deployment_ms-hmd-ms-deployment_api_stage/"
    hmd_demo = "https://wss3gl6050.execute-api.us-west-2.amazonaws.com/env_model_vocab-hmd-vocab-server_api_stage"
    base_url = local
    loader = DefaultLoader(
        base="/Users/jimmajure/code/hmd-lang-deployment/src/schemas"
    )
    base_client = RestClient(base_url=base_url, loader=loader)
    client = HmdLangDeploymentClient(base_client)

    print(build_deployment_tree(client))
