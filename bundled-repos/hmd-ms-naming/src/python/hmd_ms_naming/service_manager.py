import os
import logging
from functools import lru_cache
from typing import Dict, List, Optional
from hmd_graphql_client.hmd_base_client import BaseClient

from hmd_graphql_client.hmd_db_engine_client import DbEngineClient
from hmd_graphql_client.hmd_lambda_client import LambdaClient
from hmd_graphql_client.hmd_rest_client import RestClient
from hmd_lang_naming.hmd_lang_naming_client import HmdLangNamingClient
from hmd_lang_naming.service import Service
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lang_deployment.environment import Environment
from hmd_lang_naming.service_is_in_environment import ServiceIsInEnvironment
from hmd_schema_loader.hmd_schema_loader import DefaultLoader
from hmd_lib_auth.hmd_lib_auth import okta_service_account_token_by_service


logger = logging.getLogger()


def get_deployment_client(
    client: HmdLangNamingClient, deployment_loader: DefaultLoader
) -> Optional[HmdLangDeploymentClient]:
    def get_auth_token():
        service_auth_token = (
            okta_service_account_token_by_service(
                os.environ.get("HMD_INSTANCE_NAME"),
                os.environ.get("HMD_REPO_NAME"),
                os.environ.get("HMD_DID"),
            )
            if os.environ.get("AUTHORIZATION") == "CUSTOM"
            else None
        )
        return service_auth_token

    deploy_svc = client.search_service_hmd_lang_naming(
        {"attribute": "name", "operator": "=", "value": "deployment"}
    )

    if len(deploy_svc) == 0:
        return None

    auth_token = get_auth_token()
    if os.environ.get("HMD_LOCAL_ENV", "1") == "1":
        base_client = RestClient(
            base_url=deploy_svc[0].httpEndpoint,
            loader=deployment_loader,
            auth_token=auth_token,
            expired_auth_token_callback=get_auth_token,
        )
    else:
        base_client = LambdaClient(
            deploy_svc[0].arnEndpoint,
            deployment_loader,
            auth_token=auth_token,
            expired_auth_token_callback=get_auth_token(),
        )

    deployment_client = HmdLangDeploymentClient(base_client)
    return deployment_client


@lru_cache
def get_environment(
    env: str, deployment_client: HmdLangDeploymentClient
) -> Optional[Environment]:
    result = None
    if deployment_client:
        envs = deployment_client.search_environment_hmd_lang_deployment(
            {"attribute": "type", "operator": "=", "value": env}
        )
        result = envs[0] if envs else None

    return result


@lru_cache
def get_environment_by_id(id_: str, deployment_client: HmdLangDeploymentClient) -> str:
    env = None
    if deployment_client:
        try:
            env = deployment_client.get_environment_hmd_lang_deployment(id_)
        except Exception as e:
            logger.exception(f"Error getting environment: {id_}")
            env = None

    return env.type if env is not None else id_


class ServiceManager:
    def __init__(
        self,
        services_client: HmdLangNamingClient,
        deployment_loader: DefaultLoader,
    ) -> None:
        self.services_client = services_client
        self.deployment_loader = deployment_loader
        self._deployment_client: Optional[HmdLangDeploymentClient] = None

    def _get_deployment_client(self) -> Optional[HmdLangDeploymentClient]:
        if self._deployment_client is None:
            self._deployment_client = get_deployment_client(
                self.services_client, self.deployment_loader
            )
        return self._deployment_client

    def find_service_in_environment(
        self, service_name: str, environment: Environment
    ) -> Service:
        existing_svcs = self.services_client.search_service_hmd_lang_naming(
            {"attribute": "name", "operator": "=", "value": service_name}
        )

        for svc in existing_svcs:
            env_rels = (
                self.services_client.get_from_service_is_in_environment_hmd_lang_naming(
                    svc
                )
            )

            existing_envs = list(
                filter(
                    lambda rel: rel.ref_to == environment.identifier
                    or (rel.ref_to == environment.type),
                    env_rels,
                )
            )
            if len(existing_envs) > 0:
                logger.info(f"Found service {svc.name} already in {environment.type}")
                return svc
        else:
            logger.info("Existing service not found")

    def upsert_service(
        self, service_name: str, environment: str, payload: Dict[str, str]
    ) -> Service:
        if environment is None:
            raise Exception(f"Missing environment in creating service: {service_name}")

        if not "name" in payload:
            payload["name"] = service_name

        deployment_client = self._get_deployment_client()

        env = get_environment(environment, deployment_client)

        if env is None:
            env = Environment(
                **{
                    "type": environment,
                    "identifier": environment,
                    "account_number": "placeholder",
                    "hmd_region": os.environ["HMD_REGION"],
                }
            )
        existing_svc = self.find_service_in_environment(service_name, env)

        if existing_svc is not None:
            payload["identifier"] = existing_svc.identifier

        svc = self.services_client.upsert_service_hmd_lang_naming(Service(**payload))
        svc_rels = (
            self.services_client.get_from_service_is_in_environment_hmd_lang_naming(svc)
        )
        svc_rel = list(
            filter(
                lambda rel: rel.ref_to == env.identifier,
                svc_rels,
            )
        )

        if not svc_rel:
            self.services_client.upsert_service_is_in_environment_hmd_lang_naming(
                ServiceIsInEnvironment(ref_from=svc.identifier, ref_to=env.identifier)
            )

        return svc

    def resolve_service(self, service_name: str, env: str) -> Service:
        deployment_client = self._get_deployment_client()
        environment = get_environment(env, deployment_client)

        if environment is None:
            environment = Environment(
                **{
                    "type": env,
                    "identifier": env,
                    "account_number": "placeholder",
                    "hmd_region": os.environ["HMD_REGION"],
                }
            )
        existing_svc = self.find_service_in_environment(service_name, environment)

        if existing_svc is None:
            raise Exception(
                f"Cannot find {service_name} service in {environment.type}."
            )

        return existing_svc

    def list_services(self, env: str = None) -> Dict[str, List]:
        services = self.services_client.search_service_hmd_lang_naming(filter_={})
        deployment_client = self._get_deployment_client()

        svc_map = {}
        svc_rels: List[ServiceIsInEnvironment] = []
        for svc in services:
            svc_rel = (
                self.services_client.get_from_service_is_in_environment_hmd_lang_naming(
                    svc
                )
            )
            svc_rels.extend(svc_rel)

        envs = set(
            map(
                lambda r: r.ref_to,
                svc_rels,
            )
        )

        for env in envs:
            env_name = get_environment_by_id(env, deployment_client)
            env_rels = list(
                map(
                    lambda r: r.ref_from
                    if isinstance(r.ref_from, str)
                    else r.ref_from.identifier,
                    filter(lambda r: r.ref_to == env, svc_rels),
                )
            )
            env_svcs = list(
                map(
                    lambda s: s.serialize(),
                    filter(lambda s: s.identifier in env_rels, services),
                )
            )
            if env_name in svc_map:
                svc_map[env_name].extend(env_svcs)
            else:
                svc_map[env_name] = env_svcs

        return svc_map
