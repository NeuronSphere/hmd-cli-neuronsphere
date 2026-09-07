import base64
import json
import logging
import os
import re
import uuid
from copy import deepcopy
from typing import List, Dict, Tuple, Optional, Any

from hmd_lang_deployment.change_set import ChangeSet
from hmd_lang_deployment.change_set_deployment import ChangeSetDeployment
from hmd_lang_deployment.change_set_env_deployment import ChangeSetEnvDeployment
from hmd_lang_deployment.environment import Environment
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_lib_auth.hmd_lib_auth import okta_service_account_token_by_service
from hmd_ms_deployment.deploy_logic import DeploymentDag
from . import DESTROYED, COMPLETED
from .deploy_base import DeployBase, EFS_MOUNT_POINT
from .deployment_dag_traversal import traverse_deployment_dag
from .environment_information import EnvironmentInformation
from .instance_config_collector import InstanceConfigCollector

logger = logging.getLogger(f"HMD.{__name__}")


DEPLOY_BASE_DIR = "/mnt/deploy_base/"


def get_base_workflow(
    preamble: str,
    customer_code: str,
    hmd_region: str,
    deployment_set_name: str,
    deployment_lock_name: Optional[str] = None,
):
    script = (
        preamble
        + f"\nhmd deployment set-change-set-deployment-status --neuronsphere-id {{{{inputs.parameters.nid}}}} --status {{{{inputs.parameters.status}}}}\n"
    )

    script_env = (
        preamble
        + f"\nhmd deployment set-change-set-env-deployment-status --environment {{{{inputs.parameters.environment}}}} --neuronsphere-id {{{{inputs.parameters.nid}}}} --status {{{{inputs.parameters.status}}}}\n"
    )

    # Get node selector for workers if available
    node_selector = {"kubernetes.io/os": "linux"}
    if "WORKERS_INSTANCE_NAME" in os.environ:
        node_selector["hmdlabs.io/repo-instance-name"] = os.environ["WORKERS_INSTANCE_NAME"]

    secret_name = f"as-{uuid.uuid1()}"
    k8s_secret_spec = {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {"name": secret_name},
        "type": "Opaque",
        "data": {
            "HMD_AUTH_TOKEN": base64.b64encode(
                okta_service_account_token_by_service(
                    instance_name=os.environ["HMD_INSTANCE_NAME"],
                    repo_name=os.environ["HMD_REPO_NAME"],
                    deployment_id=os.environ["HMD_DID"],
                ).encode()
            ).decode()
        },
    }
    k8s_delete_secret_spec = {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {"name": secret_name},
    }
    mutex_name = (
        deployment_set_name
        if deployment_lock_name is None
        else f"{deployment_lock_name}-{deployment_set_name}"
    )
    hmd_app_image = os.environ['HMD_APP_IMAGE']
    result = {
        "apiVersion": "argoproj.io/v1alpha1",
        "kind": "Workflow",
        "metadata": {"generateName": f"hmd-{mutex_name}-"},
        "spec": {
            "synchronization": {"mutex": {"name": mutex_name}},
            "imagePullSecrets": [{"name": "hmd-docker-repo-secret"}],
            "serviceAccountName": "argo",
            "entrypoint": "deploy-change-set-to-deployment-set",
            "onExit": "on-exit",
            "nodeSelector": node_selector,
            "parallelism": 4,
            "ttlStrategy": {"secondsAfterCompletion": 129600},  # 36 hours
            "volumeClaimGC": {"strategy": "onWorkflowCompletion"},
            "volumeClaimTemplates": [
                {
                    "metadata": {"name": "gate-volume"},
                    "spec": {
                        "storageClassName": os.environ["DEPLOYMENT_STORAGE_CLASS_NAME"],
                        "accessModes": ["ReadWriteOnce"],
                        "resources": {"requests": {"storage": "5Gi"}},
                    },
                }
            ],
            "volumes": [
                {
                    "name": "docker-sock",
                    "hostPath": {"path": "/run/containerd/containerd.sock"},
                },
                {
                    "name": "security",
                    "persistentVolumeClaim": {
                        "claimName": os.environ["DEPLOYMENT_PVC_NAME"]
                    },
                },
            ],
            "templates": [
                {
                    "name": "create-auth-secret",
                    "resource": {
                        "action": "create",
                        "set_owner_reference": True,
                        "manifest": json.dumps(k8s_secret_spec),
                    },
                },
                {
                    "name": "destroy-auth-secret",
                    "resource": {
                        "action": "delete",
                        "manifest": json.dumps(k8s_delete_secret_spec),
                    },
                },
                {
                    "name": "remove-working-dir",
                    "inputs": {"parameters": [{"name": "config-dir"}]},
                    "metadata": {
                        "annotations": {}
                    },
                    "container": {
                        "image": hmd_app_image,
                        "command": ["rm"],
                        "args": ["-rf", DEPLOY_BASE_DIR + "{{ config-dir }}"],
                        "envFrom": [{"secretRef": {"name": "deploy-secrets"}}],
                        "volumeMounts": [
                            {
                                "name": "docker-sock",
                                "mountPath": "/run/containerd/containerd.sock",
                            },
                            {"name": "security", "mountPath": "/mnt"},
                        ],
                    },
                },
                {
                    "name": "run-script",
                    "retryStrategy": {
                        "limit": "2",
                        "backoff": {
                            "duration": "30",
                            "factor": "1",
                            "maxDuration": "20m",
                        },
                    },
                    "inputs": {"parameters": [{"name": "script"}, {"name": "hmd_app_image"}]},
                    "metadata": {
                        "annotations": {"karpenter.sh/do-not-disrupt": "true"}
                    },
                    "script": {
                        "image": "{{inputs.parameters.hmd_app_image}}",
                        "command": ["bash"],
                        "source": "{{inputs.parameters.script}}",
                        "envFrom": [
                            {"secretRef": {"name": "deploy-secrets"}},
                            {"secretRef": {"name": secret_name}},
                        ],
                        "volumeMounts": [
                            {
                                "name": "docker-sock",
                                "mountPath": "/run/containerd/containerd.sock",
                            },
                            {"name": "security", "mountPath": "/mnt"},
                        ],
                    },
                    "initContainers": [
                        {
                            "name": "make-opa-dir",
                            "image": hmd_app_image,
                            "command": ["mkdir"],
                            "args": ["-p", "/mnt/opa-efs"],
                            "mirrorVolumeMounts": False,
                            "volumeMounts": [{"name": "security", "mountPath": "/mnt"}],
                        }
                    ],
                },
                {
                    "name": "update-change-set-env-deployment-status",
                    "retryStrategy": {
                        "limit": "3",
                        "backoff": {
                            "duration": "30",
                            "factor": "1",
                            "maxDuration": "20m",
                        },
                    },
                    "inputs": {
                        "parameters": [
                            {"name": "status"},
                            {"name": "nid"},
                            {"name": "environment"}
                        ]
                    },
                    "metadata": {
                        "annotations": {}
                    },
                    "script": {
                        "image": hmd_app_image,
                        "command": ["bash"],
                        "source": script_env,
                        "envFrom": [
                            {"secretRef": {"name": "deploy-secrets"}},
                            {"secretRef": {"name": secret_name}},
                        ],
                        "volumeMounts": [
                            {
                                "name": "docker-sock",
                                "mountPath": "/run/containerd/containerd.sock",
                            }
                        ],
                    },
                },
                {
                    "name": "update-change-set-deployment-status",
                    "retryStrategy": {
                        "limit": "3",
                        "backoff": {
                            "duration": "30",
                            "factor": "1",
                            "maxDuration": "20m",
                        },
                    },
                    "inputs": {"parameters": [{"name": "status"}, {"name": "nid"}]},
                    "metadata": {
                        "annotations": {}
                    },
                    "script": {
                        "image": hmd_app_image,
                        "command": ["bash"],
                        "source": script,
                        "envFrom": [
                            {"secretRef": {"name": "deploy-secrets"}},
                            {"secretRef": {"name": secret_name}},
                        ],
                        "volumeMounts": [
                            {
                                "name": "docker-sock",
                                "mountPath": "/run/containerd/containerd.sock",
                            }
                        ],
                    },
                },
                {
                    "name": "do-transform",
                    "inputs": {
                        "parameters": [
                            {"name": "image"},
                            {"name": "context"},
                            {"name": "input_path"},
                            {"name": "output_path"},
                            {"name": "env_type"},
                            {"name": "account"},
                            {"name": "custom_env", "default": "{}"},
                            {"name": "otel_endpoint", "default": ""},
                            {"name": "bender_mode", "default": ""},
                            {"name": "bender_remote_url", "default": ""},
                            {"name": "bender_session_token", "default": ""},
                            {"name": "bender_remote_libraries", "default": ""},
                        ]
                    },
                    "metadata": {
                        "annotations": {"karpenter.sh/do-not-disrupt": "true"}
                    },
                    "container": {
                        "image": "{{ inputs.parameters.image }}",
                        "command": ["python"],
                        "args": ["entrypoint.py"],
                        "volumeMounts": [
                            {
                                "name": "gate-volume",
                                "mountPath": "/hmd_transform/input",
                                "subPath": "{{ inputs.parameters.input_path }}",
                            },
                            {
                                "name": "gate-volume",
                                "mountPath": "/hmd_transform/output",
                                "subPath": "{{ inputs.parameters.output_path }}",
                            },
                            {"name": "security", "mountPath": "/mnt"},
                        ],
                        "envFrom": [
                            {"secretRef": {"name": "deploy-secrets"}},
                            {"secretRef": {"name": secret_name}},
                        ],
                        "env": [
                            {
                                "name": "TRANSFORM_INSTANCE_CONTEXT",
                                "value": "{{ inputs.parameters.context }}",
                            },
                            {"name": "HMD_CUSTOMER_CODE", "value": customer_code},
                            {"name": "HMD_REGION", "value": hmd_region},
                            {
                                "name": "HMD_ENVIRONMENT",
                                "value": "{{ inputs.parameters.env_type }}",
                            },
                            {
                                "name": "HMD_ACCOUNT",
                                "value": "{{ inputs.parameters.account }}",
                            },
                            {
                                "name": "CUSTOM_ENV",
                                "value": "{{ inputs.parameters.custom_env }}",
                            },
                            # Lets the Bender run emit a span per suite/test/keyword,
                            # so a gate failure can be pulled up as one trace with the
                            # service spans it triggered. Empty when the environment
                            # has no collector, which disables tracing in the listener.
                            {
                                "name": "HMD_OTEL_ENDPOINT",
                                "value": "{{ inputs.parameters.otel_endpoint }}",
                            },
                            {
                                "name": "BENDER_MODE",
                                "value": "{{ inputs.parameters.bender_mode }}",
                            },
                            {
                                "name": "BENDER_REMOTE_URL",
                                "value": "{{ inputs.parameters.bender_remote_url }}",
                            },
                            {
                                "name": "BENDER_SESSION_TOKEN",
                                "value": "{{ inputs.parameters.bender_session_token }}",
                            },
                            {
                                "name": "BENDER_REMOTE_LIBRARIES",
                                "value": "{{ inputs.parameters.bender_remote_libraries }}",
                            },
                        ],
                    },
                },
                {
                    "name": "do-bender-session-call",
                    "inputs": {
                        "parameters": [
                            {"name": "method"},
                            {"name": "url"},
                            {"name": "body", "default": ""},
                        ]
                    },
                    "outputs": {
                        "parameters": [
                            {
                                "name": "session_id",
                                "valueFrom": {"default": "", "path": "/tmp/session_id"},
                            },
                            {
                                "name": "session_url",
                                "valueFrom": {"default": "", "path": "/tmp/session_url"},
                            },
                            {
                                "name": "session_token",
                                "valueFrom": {"default": "", "path": "/tmp/session_token"},
                            },
                        ]
                    },
                    "metadata": {
                        "annotations": {"karpenter.sh/do-not-disrupt": "true"}
                    },
                    "container": {
                        "image": os.environ.get(
                            "HMD_DEPLOYMENT_IMAGE",
                            f"ghcr.io/hmdlabs/hmd-ms-deployment:{os.environ['HMD_REPO_VERSION']}",
                        ),
                        "command": ["python"],
                        "args": ["/bender_session_call.py"],
                        "env": [
                            {"name": "METHOD", "value": "{{ inputs.parameters.method }}"},
                            {"name": "URL", "value": "{{ inputs.parameters.url }}"},
                            {"name": "BODY", "value": "{{ inputs.parameters.body }}"},
                        ],
                    },
                },
                {
                    "name": "prep-gate",
                    "inputs": {"parameters": [{"name": "config"}]},
                    "metadata": {
                        "annotations": {"karpenter.sh/do-not-disrupt": "true"}
                    },
                    "container": {
                        "image": os.environ.get('HMD_DEPLOYMENT_IMAGE', f"ghcr.io/hmdlabs/hmd-ms-deployment:{os.environ['HMD_REPO_VERSION']}"),
                        "command": ["python"],
                        "args": ["/prepare_workflow_gate.py"],
                        "volumeMounts": [
                            {"name": "gate-volume", "mountPath": "/prep_root"},
                            {"name": "security", "mountPath": "/mnt"},
                        ],
                        "envFrom": [
                            {"secretRef": {"name": "deploy-secrets"}},
                            {"secretRef": {"name": secret_name}},
                        ],
                        "env": [
                            {"name": "HMD_CUSTOMER_CODE", "value": customer_code},
                            {"name": "HMD_REGION", "value": hmd_region},
                            {
                                "name": "WORKFLOW_GATE_CONFIG",
                                "value": "{{ inputs.parameters.config }}",
                            },
                        ],
                    },
                },
                {
                    "name": "gather-gate",
                    "inputs": {
                        "parameters": [
                            {"name": "change_set_deployment"},
                            {"name": "change_set"},
                        ]
                    },
                    "metadata": {
                        "annotations": {"karpenter.sh/do-not-disrupt": "true"}
                    },
                    "container": {
                        "image": os.environ.get('HMD_DEPLOYMENT_IMAGE', f"ghcr.io/hmdlabs/hmd-ms-deployment:{os.environ['HMD_REPO_VERSION']}"),
                        "command": ["python"],
                        "args": ["/collect_gate_results.py"],
                        "volumeMounts": [
                            {"name": "gate-volume", "mountPath": "/prep_root"}
                        ],
                        "envFrom": [
                            {"secretRef": {"name": "deploy-secrets"}},
                            {"secretRef": {"name": secret_name}},
                        ],
                        "env": [
                            {"name": "HMD_CUSTOMER_CODE", "value": customer_code},
                            {"name": "HMD_REGION", "value": hmd_region},
                            {
                                "name": "CHANGE_SET_DEPLOYMENT",
                                "value": "{{ inputs.parameters.change_set_deployment }}",
                            },
                            {
                                "name": "CHANGE_SET",
                                "value": "{{ inputs.parameters.change_set }}",
                            },
                        ],
                    },
                },
                {"name": "do-wait", "suspend": {}},
            ],
        },
    }
    return result


def get_deploy_gate_env(
    env: str, nid: str, status: str, has_gate_transforms: bool, destroy: bool
) -> Dict:
    result = {
        "name": f"deploy-gate-{env}",
        "steps": [
            [
                {
                    "name": f"update-status-{env}-started",
                    "template": "update-change-set-env-deployment-status",
                    "arguments": {
                        "parameters": [
                            {"name": "status", "value": "STARTED"},
                            {"name": "nid", "value": f"{nid}"},
                            {"name": "environment", "value": f"{env}"},
                        ]
                    },
                }
            ],
            [{"name": f"deploy-{env}-step", "template": f"deploy-{env}"}],
        ],
    }
    if not destroy and has_gate_transforms:
        result["steps"].append(
            [{"name": f"gate-{env}-step", "template": f"gate-{env}"}]
        )
    result["steps"].append(
        [
            {
                "name": f"update-status-{env}-completed",
                "template": "update-change-set-env-deployment-status",
                "arguments": {
                    "parameters": [
                        {"name": "status", "value": status},
                        {"name": "nid", "value": f"{nid}"},
                        {"name": "environment", "value": f"{env}"},
                    ]
                },
            }
        ]
    )
    return result


def _rewrite_ghcr_to_ecr(image_url: str) -> str:
    prefix = os.environ.get("HMD_ECR_CACHE_PREFIX")
    if prefix and image_url.startswith("ghcr.io/"):
        return f"{prefix}{image_url[len('ghcr.io/'):]}"
    return image_url


def _get_hmd_app_image(environment: Environment):
    hmd_app_image = os.environ.get("HMD_APP_IMAGE")
    if not hmd_app_image:
        raise Exception("Missing HMD_APP_IMAGE configuration")
    
    hmd_app_image_map = os.environ.get("HMD_APP_IMAGE_MAP", "{}")
    hmd_app_image_map = json.loads(hmd_app_image_map)

    if isinstance(hmd_app_image_map, dict):
        hmd_app_image = hmd_app_image_map.get(environment.type, hmd_app_image)
        
    assert isinstance(hmd_app_image, str), f"HMD_APP_IMAGE be str, got {type(hmd_app_image)}"

    return hmd_app_image

deploy_env_task = {
    "name": "name",
    "dependencies": [],
    "template": "run-script",
    "arguments": {"parameters": []},
}


def get_preamble(hmd_region: str, customer_code: str):
    terraform_binary_name = os.environ.get("TERRAFORM_BINARY_NAME", "tofu")
    preamble = f"""
export HMD_REGION={hmd_region}
export HMD_CUSTOMER_CODE={customer_code}
export HMD_ARTIFACT_LIBRARIAN_URL=https://artifact-aaa-{hmd_region}.{customer_code}-admin-neuronsphere.io
export TERRAFORM_BINARY_NAME={terraform_binary_name}
"""
    return preamble


def get_deploy_change_set_to_deployment_set(
    change_set_deployment: ChangeSetDeployment, change_set: ChangeSet, destroy: bool
) -> Any:
    deployment_set_definition = change_set_deployment.deployment_set_definition
    nid = change_set_deployment.identifier

    result = {"name": "deploy-change-set-to-deployment-set", "steps": []}
    result["steps"].append(
        [{"name": "do-create-auth-secret", "template": "create-auth-secret"}]
    )
    result["steps"].append(
        [
            {
                "name": f"update-status-started",
                "template": "update-change-set-deployment-status",
                "arguments": {
                    "parameters": [
                        {"name": "status", "value": "STARTED"},
                        {"name": "nid", "value": f"{nid}"},
                    ]
                },
            }
        ]
    )
    for i, env_config in enumerate(deployment_set_definition):
        result["steps"].append(
            [
                {
                    "name": f"deploy-gate-{env_config['environment']}",
                    "template": f"deploy-gate-{env_config['environment']}",
                }
            ]
        )
        if (
            env_config["deployment_gate"]["approval"]
            and i < len(deployment_set_definition) - 1
        ):
            result["steps"].append(
                [
                    {
                        "name": f"suspend-{env_config['environment']}",
                        "template": "do-wait",
                    }
                ]
            )

    return result


def get_exit_handler(
    nid: str,
    config_directory: str,
    change_set: ChangeSet,
    change_set_deployment: ChangeSetDeployment,
) -> Any:
    result = {"name": "on-exit", "steps": []}
    if change_set and change_set_deployment:
        result["steps"].append(
            [
                {
                    "name": "gather-gate",
                    "template": "gather-gate",
                    "arguments": {
                        "parameters": [
                            {
                                "name": "change_set_deployment",
                                "value": json.dumps(
                                    change_set_deployment.serialize(encode_blobs=False)
                                ),
                            },
                            {
                                "name": "change_set",
                                "value": json.dumps(
                                    change_set.serialize(encode_blobs=False)
                                ),
                            },
                        ]
                    },
                }
            ]
        )
    result["steps"].append(
        [
            {
                "name": "remove-working-dir",
                "template": "remove-working-dir",
                "arguments": {
                    "parameters": [{"name": "config-dir", "value": config_directory}]
                },
            }
        ]
    )
    result["steps"].append(
        [
            {
                "name": f"update-status-success",
                "when": "{{workflow.status}} == Succeeded",
                "template": "update-change-set-deployment-status",
                "arguments": {
                    "parameters": [
                        {"name": "status", "value": "COMPLETED"},
                        {"name": "nid", "value": f"{nid}"},
                    ]
                },
            }
        ]
    )
    result["steps"].append(
        [
            {
                "name": f"update-status-failed",
                "when": "{{workflow.status}} != Succeeded",
                "template": "update-change-set-deployment-status",
                "arguments": {
                    "parameters": [
                        {"name": "status", "value": "FAILED"},
                        {"name": "nid", "value": f"{nid}"},
                    ]
                },
            }
        ]
    )
    result["steps"].append(
        [{"name": f"do-destroy-auth-secret", "template": "destroy-auth-secret"}]
    )
    return result


class DeployWorkflowCreator(DeployBase):
    def __init__(self, client: HmdLangDeploymentClient):
        super().__init__(client)

    def get_deploy_env(
        self,
        env_info: EnvironmentInformation,
        customer_code: str,
        deployment_dag: DeploymentDag,
        instance_configs: Dict[str, Dict],
        repo_instance_deployment_ids: List[str],
        configuration_directory: str,
        destroy: bool = False,
        profile: str = None,
        secrets_to_expose: Optional[Dict] = None,
    ) -> Any:
        if not secrets_to_expose:
            secrets_to_expose = {}
        return {
            "name": f"deploy-{env_info.environment.type}",
            "dag": {
                "tasks": self.create_deploy_env_workflow_tasks(
                    env_info.environment,
                    customer_code,
                    deployment_dag,
                    instance_configs,
                    repo_instance_deployment_ids,
                    configuration_directory,
                    destroy,
                    profile,
                    secrets_to_expose,
                )
            },
        }

    def create_deploy_env_workflow_tasks(
        self,
        environment: Environment,
        customer_code: str,
        deployment_dag: DeploymentDag,
        instance_configs: Dict[str, Dict],
        repo_instance_deployment_ids: List[str],
        configuration_directory: str,
        destroy: bool = False,
        profile: str = None,
        secrets_to_expose=None,
    ) -> List[Dict]:
        hmd_app_image = _get_hmd_app_image(environment=environment)
        if secrets_to_expose is None:
            secrets_to_expose = {}

        logger.info(
            f"Building core workflow for env: {environment.type}, destroy: {destroy}"
        )
        command_list = []
        context = {
            "command_list": command_list,
            "destroy": destroy,
            "environment": environment.type,
            "instance_configs": instance_configs,
            "repo_instance_deployment_ids": repo_instance_deployment_ids,
            "configuration_directory": configuration_directory,
        }
        if profile:
            context["profile"] = profile
        if not profile:
            context["account"] = environment.account_number
        traverse_deployment_dag(deployment_dag, self.deploy_node, context)

        preamble = get_preamble(environment.hmd_region, customer_code)
        if secrets_to_expose:
            for k, v in secrets_to_expose.items():
                preamble += f"export {k}={v}\n"

        # When the environment has a telemetry-debug service available, export its
        # endpoint so each repo's deploy seeds its telemetry service profiles
        # (repos that ship meta-data/telemetry_profiles.json). Skipped silently
        # when no telemetry service is present in the environment.
        telemetry_debug_url = self.get_telemetry_debug_url(environment, customer_code)
        if telemetry_debug_url:
            logger.info(
                f"Telemetry profile seeding enabled for env {environment.type}: "
                f"{telemetry_debug_url}"
            )
            preamble += f"export HMD_DEBUG_URL={telemetry_debug_url}\n"

        tasks = []
        for command in command_list:
            my_task = deepcopy(deploy_env_task)
            my_task["name"] = command[1].instance.name
            my_task["dependencies"] = [name for name in command[2]]
            my_task["arguments"]["parameters"].append(
                {"name": "script", "value": preamble + command[0]}
            )
            my_task["arguments"]["parameters"].append(
                {"name": "hmd_app_image", "value": hmd_app_image}
            )

            tasks.append(my_task)

        return tasks

    def create_deploy_workflow(
        self,
        csd: ChangeSetDeployment,
        change_set: ChangeSet,
        env_infos: Dict[
            str, Tuple[EnvironmentInformation, ChangeSetEnvDeployment, DeploymentDag]
        ],
        customer_code: str,
        hmd_region: str,
        deployment_set_name: str,
        destroy: bool,
        profile: Optional[str],
        deployment_lock_name: Optional[str] = None,
    ):
        config_directory_name = f"dir_{uuid.uuid1()}"
        my_workflow = get_base_workflow(
            get_preamble(hmd_region, customer_code),
            customer_code,
            hmd_region,
            deployment_set_name,
            deployment_lock_name=deployment_lock_name,
        )
        my_workflow["spec"]["templates"].append(
            get_deploy_change_set_to_deployment_set(csd, change_set, destroy)
        )
        my_workflow["spec"]["templates"].append(
            get_exit_handler(csd.identifier, config_directory_name, change_set, csd)
        )
        for env_config in csd.deployment_set_definition:
            env_info: EnvironmentInformation
            csed: ChangeSetEnvDeployment
            has_gate_transforms = len(env_config["deployment_gate"]["transforms"]) > 0
            env_info, csed, deployment_dag = env_infos[env_config["environment"]]
            my_workflow["spec"]["templates"].append(
                get_deploy_gate_env(
                    env_config["environment"],
                    csed.identifier,
                    COMPLETED if not destroy else DESTROYED,
                    has_gate_transforms,
                    destroy,
                )
            )

            logger.debug(
                f"Gathering instance configurations: {env_info.environment.type}; destroy: {destroy}"
            )
            instance_configs_collector = InstanceConfigCollector(self.client, env_info.rel_support)
            instance_configs = instance_configs_collector.collect_instance_configs(
                deployment_dag, destroy
            )

            repo_instance_deployment_ids = [
                rel.ref_to
                for rel in self.client.get_from_change_set_env_deployment_has_repo_instance_deployment_hmd_lang_deployment(
                    csed
                )
            ]
            # Use the environment's RelationshipSupport so deploy_node resolves
            # entities from the same cache that load_environment() populated.
            self.rs = env_info.rel_support
            my_workflow["spec"]["templates"].append(
                self.get_deploy_env(
                    env_info,
                    customer_code,
                    deployment_dag,
                    instance_configs,
                    repo_instance_deployment_ids,
                    config_directory_name,
                    destroy,
                    profile,
                )
            )
            if not destroy and has_gate_transforms:
                my_workflow["spec"]["templates"].append(
                    self.get_gate_env(
                        env_info,
                        csd,
                        change_set,
                        instance_configs,
                        config_directory_name,
                        customer_code,
                    )
                )

        return my_workflow

    def get_transform_step(
        self, config, instance_config, config_directory: str, customer_code: str = None
    ):
        env_type = config["environment"]
        seq_no = config["seq_no"]
        repo_instance_name = config["repo_instance_name"]

        dir_path = f"{EFS_MOUNT_POINT}/{config_directory}"
        if not os.path.exists(dir_path):
            os.makedirs(dir_path, exist_ok=True)

        config_filename = f"transform_{seq_no}_{repo_instance_name}_context.json"
        config_full_path = f"{dir_path}/{config_filename}"
        with open(config_full_path, "w") as fl:
            json.dump(instance_config, fl, indent=2)

        result = {
            "name": f"transform-{seq_no}-{repo_instance_name}",
            "template": "do-transform",
            "arguments": {
                "parameters": [
                    {
                        "name": "image",
                        "value": _rewrite_ghcr_to_ecr(
                            f'{config["transform"]["image_name"]}:{config["transform"]["tag"]}'
                        ),
                    },
                    {
                        "name": "context",
                        "value": f"{DEPLOY_BASE_DIR}{config_directory}/{config_filename}",
                    },
                    {"name": "input_path", "value": config["input_path"]},
                    {"name": "output_path", "value": config["output_path"]},
                    {"name": "env_type", "value": env_type},
                    {"name": "account", "value": config["account"]},
                    {
                        "name": "custom_env",
                        "value": json.dumps(config.get("env", {})),
                    },
                    {
                        "name": "otel_endpoint",
                        "value": self._resolve_otel_endpoint(customer_code, env_type),
                    },
                ]
            },
        }
        return result

    def _resolve_session_manager_url(
        self, remote_test_server: dict, customer_code: str, env_type: str
    ) -> str:
        override = remote_test_server.get("session_manager_url")
        if override:
            return override.rstrip("/")
        from hmd_cli_tools.hmd_cli_tools import get_neuronsphere_domain

        domain = get_neuronsphere_domain(customer_code, env_type)
        if not domain:
            raise ValueError(
                "Could not resolve session_manager_url: no override in DeploymentSet and "
                f"get_neuronsphere_domain({customer_code!r}, {env_type!r}) returned no value."
            )
        return f"https://bender-session.{domain}"

    def _resolve_otel_endpoint(self, customer_code: str, env_type: str) -> str:
        """OTLP endpoint for the Bender gate run, or "" to leave tracing off.

        Same convention as _resolve_session_manager_url. Best-effort: a missing
        domain just means no tracing, never a failed deploy.
        """
        if not customer_code or not env_type:
            # get_neuronsphere_domain happily interpolates empties into a
            # syntactically valid but meaningless host, so screen them out here.
            return ""
        try:
            from hmd_cli_tools.hmd_cli_tools import get_neuronsphere_domain

            domain = get_neuronsphere_domain(customer_code, env_type)
            if not domain:
                return ""
            return f"https://otel-collector.{domain}"
        except Exception as e:
            logger.warning(f"Could not resolve OTEL endpoint for {env_type}: {e}")
            return ""

    def get_bender_session_steps(
        self,
        config: dict,
        instance_config: dict,
        config_directory: str,
        customer_code: str,
    ) -> list:
        """Return the 3 sequential steps for a remote bender test session:
        start-session (POST), transform (client mode), stop-session (DELETE).
        """
        env_type = config["environment"]
        seq_no = config["seq_no"]
        repo_instance_name = config["repo_instance_name"]
        rts = config["remote_test_server"]

        base_url = self._resolve_session_manager_url(rts, customer_code, env_type)
        idle_timeout = int(rts.get("idle_timeout_seconds", 1800))
        body = json.dumps(
            {
                "artifact_ref": config["artifact_ref"],
                "libraries": rts["libraries"],
                "ttl_seconds": idle_timeout,
                "image": _rewrite_ghcr_to_ecr(
                    f'{config["transform"]["image_name"]}:{rts.get("image_tag", config["transform"]["tag"])}'
                ),
                "transform_instance_context": instance_config,
                "hmd_environment": env_type,
                "hmd_region": os.environ.get("HMD_REGION", ""),
                "hmd_customer_code": customer_code,
                "hmd_account": config["account"],
            }
        )

        start_step = {
            "name": f"start-session-{seq_no}-{repo_instance_name}",
            "template": "do-bender-session-call",
            "arguments": {
                "parameters": [
                    {"name": "method", "value": "POST"},
                    {"name": "url", "value": f"{base_url}/sessions"},
                    {"name": "body", "value": body},
                ]
            },
        }

        transform_step = self.get_transform_step(
            config, instance_config, config_directory, customer_code
        )
        transform_step["arguments"]["parameters"].extend(
            [
                {"name": "bender_mode", "value": "client"},
                {
                    "name": "bender_remote_url",
                    "value": f"{{{{ steps.{start_step['name']}.outputs.parameters.session_url }}}}",
                },
                {
                    "name": "bender_session_token",
                    "value": f"{{{{ steps.{start_step['name']}.outputs.parameters.session_token }}}}",
                },
                {
                    "name": "bender_remote_libraries",
                    "value": ",".join(rts["libraries"]),
                },
            ]
        )

        stop_step = {
            "name": f"stop-session-{seq_no}-{repo_instance_name}",
            "template": "do-bender-session-call",
            "arguments": {
                "parameters": [
                    {"name": "method", "value": "DELETE"},
                    {
                        "name": "url",
                        "value": f"{base_url}/sessions/{{{{ steps.{start_step['name']}.outputs.parameters.session_id }}}}",
                    },
                ]
            },
        }

        return [start_step, transform_step, stop_step]

    def get_gate_env(
        self,
        env_info: EnvironmentInformation,
        csd: ChangeSetDeployment,
        change_set: ChangeSet,
        instance_configs: Dict[str, Dict],
        config_directory: str,
        customer_code: str,
    ) -> Any:
        deployment_set = csd.deployment_set_definition
        csd_env = [
            ds
            for ds in deployment_set
            if ds["environment"] == env_info.environment.type
        ]
        assert (
            len(csd_env) == 1
        ), f"Expected one environment with type {env_info.environment.type}, found {len(csd_env)}."
        csd_env = csd_env[0]
        result = {"name": f"gate-{env_info.environment.type}", "steps": []}

        transform_instances = []
        for i, transform_def in enumerate(csd_env["deployment_gate"]["transforms"]):
            artifact_ref = (
                transform_def["artifact_ref"]
                if "artifact_ref" in transform_def
                else None
            )
            apply_to_re = re.compile(transform_def["apply_to"])
            for change in change_set.definition:
                ri_name = change["repo_instance_name"]
                if apply_to_re.fullmatch(ri_name):
                    transform_instances.append(
                        {
                            "environment": env_info.environment.type,
                            "account": env_info.environment.account_number,
                            "seq_no": i,
                            "repo_instance_name": ri_name,
                            "transform": transform_def["transform"],
                            "instance_configuration": instance_configs[ri_name],
                            "artifact_ref": (
                                artifact_ref
                                if artifact_ref
                                else f'{change["repo_class_name"]}@{change["repo_class_version"]}:build'
                            ),
                            "input_path": f"{env_info.environment.type}/{ri_name}_{i}/input",
                            "output_path": f"{env_info.environment.type}/{ri_name}_{i}/output",
                            "env": transform_def.get("env", {}),
                            "remote_test_server": transform_def.get(
                                "remote_test_server"
                            ),
                        }
                    )

        result["steps"].append(
            [
                self.get_gate_prep_step(
                    transform_instances, config_directory, env_info.environment.type
                )
            ]
        )

        for ti in transform_instances:
            rts = ti.get("remote_test_server") or {}
            if rts.get("enabled"):
                for step in self.get_bender_session_steps(
                    ti,
                    instance_configs[ti["repo_instance_name"]],
                    config_directory,
                    customer_code,
                ):
                    result["steps"].append([step])
            else:
                result["steps"].append(
                    [
                        self.get_transform_step(
                            ti,
                            instance_configs[ti["repo_instance_name"]],
                            config_directory,
                            customer_code,
                        )
                    ]
                )

        return result if result["steps"] else None

    def get_gate_prep_step(
        self, transform_instances, config_directory: str, env_type: str
    ):
        dir_path = f"{EFS_MOUNT_POINT}/{config_directory}/{env_type}"
        if not os.path.exists(dir_path):
            os.makedirs(dir_path, exist_ok=False)

        config_file = f"{dir_path}/prep_gate_config.json"
        with open(config_file, "w") as fl:
            json.dump(transform_instances, fl, indent=2)

        result = {
            "name": "prep-gate",
            "template": "prep-gate",
            "arguments": {
                "parameters": [
                    {
                        "name": "config",
                        "value": f"{DEPLOY_BASE_DIR}{config_directory}/{env_type}/prep_gate_config.json",
                    }
                ]
            },
        }
        return result
