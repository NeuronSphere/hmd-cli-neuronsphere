import logging

from hmd_lang_naming.hmd_lang_naming_client import HmdLangNamingClient

from hmd_graphql_client.hmd_db_engine_client import DbEngineClient
from hmd_ms_base.crud_operations import get_first_db_engine
from .service_manager import ServiceManager

LOGGER = logging.getLogger(f"HMD.{__name__}")


def create_service_manager(service):
    return ServiceManager(
        services_client=(
            HmdLangNamingClient(
                DbEngineClient(
                    db_engine=(get_first_db_engine({}, service.context)),
                    loader=service.context["loaders"]["naming"],
                )
            )
        ),
        deployment_loader=service.context["loaders"]["deployment"],
    )


def setup(service):
    global mgr
    mgr = create_service_manager(service)

    @service.operation(
        rest_path="/apiop/service/<args_service>/<args_environment>",
        rest_methods=["PUT"],
        args={"payload": "json", "service": "string", "environment": "string"},
    )
    def upsert_service(evt, ctx):
        payload = evt["args"].get("payload", {})
        service = evt["args"].get("service", None)
        environment = evt["args"].get("environment", None)

        if service is None:
            raise Exception("Missing service name")

        if environment is None:
            raise Exception("Missing environment name")

        return mgr.upsert_service(service, environment, payload).serialize()

    @service.operation(
        rest_path="/apiop/resolve/<args_service>",
        rest_methods=["POST"],
        args={"payload": "json", "service": "string"},
    )
    def resolve_service(evt, ctx):
        payload = evt["args"].get("payload", {})
        service = evt["args"].get("service", None)
        environment = payload.get("environment", "production")

        if service is None:
            raise Exception("Missing service name")

        return mgr.resolve_service(service, environment).serialize()

    @service.operation(
        rest_path="/apiop/list_services",
        rest_methods=["POST"],
        args={
            "payload": "json",
        },
    )
    def list_services(evt, ctx):
        payload = evt["args"].get("payload", {})
        environment = payload.get("environment", None)

        if service is None:
            raise Exception("Missing service name")

        return mgr.list_services(environment)
