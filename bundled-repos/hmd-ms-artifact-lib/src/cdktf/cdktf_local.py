from constructs import Construct

from hmd_lib_cdktf_factories.librarian_base import LibrarianHmdCdkTfStack


class CdkTfStack(LibrarianHmdCdkTfStack):
    def __init__(
        self,
        scope: Construct,
        ns: str,
        instance_name,
        repo_name,
        deployment_id,
        environment,
        hmd_region,
        customer_code,
        repo_version,
        account_number,
        profile,
        config,
    ):
        super().__init__(
            scope,
            ns,
            instance_name,
            repo_name,
            deployment_id,
            environment,
            hmd_region,
            customer_code,
            repo_version,
            account_number,
            profile,
            config,
        )
