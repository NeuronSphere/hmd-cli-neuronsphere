import os
from datetime import datetime
from json import dumps
from pathlib import Path
from unittest.mock import patch, MagicMock, call, ANY, mock_open

import pytest
import yaml

from hmd_graphql_client.hmd_memory_client import MemoryClient
from hmd_lang_deployment.hmd_lang_deployment_client import HmdLangDeploymentClient
from hmd_ms_deployment.artifact_monitor import ArtifactMonitor
from hmd_ms_deployment.class_information import ClassInformation
from hmd_ms_deployment.resource_information import ResourceInformation
from hmd_schema_loader import DefaultLoader


def test_do_process_new_content_items():
    mock_librarian_client = MagicMock()
    mock_librarian_client.invoke_custom_operation.return_value = ["1", "2"]
    mock_class_information = MagicMock()
    mock_deployment_client = MagicMock()
    mock_download = MagicMock()

    test_manifest = {
        "deploy": {
            "dependencies": {"test": "test"},
            "default_configuration": {"default": "config"},
        },
    }

    am = ArtifactMonitor(
        mock_librarian_client,
        mock_class_information,
        mock_deployment_client,
        "query_name",
        "timestamp_name",
    )
    with patch("builtins.open", mock_open(read_data=dumps(test_manifest))):
        with patch.multiple(
            am,
            _download_deployment_artifact=mock_download,
        ) as a_dict:
            am._do_process_new_content_items(
                [
                    {
                        "content_item": {
                            "content_item_path": "repository:/hmd-ms-test/1.2.3/abc.zip"
                        },
                        "download_url": "a_url",
                    }
                ],
                datetime.utcnow(),
                datetime.utcnow(),
            )
            mock_download.assert_called_once_with("a_url", ANY)
            mock_class_information.add_repo_version_by_name.assert_called_once_with(
                "hmd-ms-test",
                "1.2.3",
                test_manifest["deploy"]["dependencies"],
                test_manifest["deploy"]["default_configuration"],
                None,
                discovery={},
            )

            with pytest.raises(Exception):
                am._process_new_deployable(
                    {
                        "content_item": {
                            "content_item_path": "bad_prefix:/hmd-ms-test/1.2.3/abc.zip"
                        },
                        "download_url": "a_url",
                    }
                )


def _write_artifact(root: Path, manifest: dict, resource_defs: dict):
    """Lay out a fake unpacked artifact under ``root``."""
    meta = root / "meta-data"
    (meta / "resources").mkdir(parents=True, exist_ok=True)
    with open(meta / "manifest.json", "w") as fl:
        fl.write(dumps(manifest))
    for name, definition in resource_defs.items():
        with open(meta / "resources" / name, "w") as fl:
            yaml.safe_dump(definition, fl)


def test_ingest_resource_definitions_from_artifact(tmp_path):
    loader = DefaultLoader("schemas/local")
    client = HmdLangDeploymentClient(MemoryClient(loader, {}))
    class_information = ClassInformation(client)

    manifest = {"deploy": {"dependencies": {}, "default_configuration": {}}}
    resource_def = {
        "resource_namespace": "aws",
        "resource_definition_name": "eks-cluster",
        "version": "0.1.0",
        "description": "An EKS cluster",
        "output_schema": {"type": "object"},
        "produces": True,
        "role": "primary",
    }
    _write_artifact(tmp_path, manifest, {"eks.yaml": resource_def})

    am = ArtifactMonitor(
        MagicMock(), class_information, client, "query_name", "timestamp_name"
    )

    def fake_download(download_url, path):
        # copy the pre-laid-out artifact into the monitor's tmpdir
        import shutil

        shutil.copytree(tmp_path / "meta-data", Path(path) / "meta-data")

    with patch.multiple(am, _download_deployment_artifact=fake_download):
        am._do_process_new_content_items(
            [
                {
                    "content_item": {
                        "content_item_path": "repository:/hmd-vpc/0.1.0/abc.zip"
                    },
                    "download_url": "a_url",
                }
            ],
            datetime.utcnow(),
            datetime.utcnow(),
        )

    ri = ResourceInformation(client)
    rd = ri.find_resource_definition("aws", "eks-cluster", "0.1.0")
    assert rd is not None
    assert rd.description == "An EKS cluster"

    # provenance (has) recorded against the ingested RepoClassVersion
    rcv = class_information.get_repo_class_version("hmd-vpc", "0.1.0")
    has_rels = client.get_from_repo_class_version_has_resource_definition_hmd_lang_deployment(
        rcv
    )
    assert len(has_rels) == 1
    produces_rels = client.get_from_repo_class_version_produces_resource_definition_hmd_lang_deployment(
        rcv
    )
    assert len(produces_rels) == 1

    # re-ingest is idempotent (no duplicate definitions or edges)
    with patch.multiple(am, _download_deployment_artifact=fake_download):
        am._do_process_new_content_items(
            [
                {
                    "content_item": {
                        "content_item_path": "repository:/hmd-vpc/0.1.0/abc.zip"
                    },
                    "download_url": "a_url",
                }
            ],
            datetime.utcnow(),
            datetime.utcnow(),
        )
    assert len(client.search_resource_definition_hmd_lang_deployment({})) == 1


def _ingest_from_manifest(tmp_path, manifest, resource_defs=None, repo_name="hmd-vpc"):
    """Lay out a fake artifact and run ingestion, returning the (client, class_information)."""
    loader = DefaultLoader("schemas/local")
    client = HmdLangDeploymentClient(MemoryClient(loader, {}))
    class_information = ClassInformation(client)
    _write_artifact(tmp_path, manifest, resource_defs or {})

    am = ArtifactMonitor(
        MagicMock(), class_information, client, "query_name", "timestamp_name"
    )

    def fake_download(download_url, path):
        import shutil

        shutil.copytree(tmp_path / "meta-data", Path(path) / "meta-data")

    with patch.multiple(am, _download_deployment_artifact=fake_download):
        am._do_process_new_content_items(
            [
                {
                    "content_item": {
                        "content_item_path": f"repository:/{repo_name}/0.1.0/abc.zip"
                    },
                    "download_url": "a_url",
                }
            ],
            datetime.utcnow(),
            datetime.utcnow(),
        )

    return client, class_information


def test_manifest_deploy_resources_declares_produces_for_yaml_shipped_type(tmp_path):
    """meta-data/resources/*.yaml still defines the ResourceDefinition (schema,
    parent/isa); deploy.resources only *references* it by identity to declare
    that this RepoClassVersion produces it -- the two are complementary, not
    alternatives.
    """
    manifest = {
        "deploy": {
            "dependencies": {},
            "default_configuration": {},
            "resources": {
                "cluster": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                    "role": "primary",
                }
            },
        }
    }
    yaml_resource_def = {
        "resource_namespace": "aws",
        "resource_definition_name": "eks-cluster",
        "version": "0.1.0",
        "description": "An EKS cluster",
        "output_schema": {"type": "object"},
        # note: no `produces` here -- production is declared via deploy.resources
    }
    client, class_information = _ingest_from_manifest(
        tmp_path, manifest, {"eks.yaml": yaml_resource_def}
    )

    ri = ResourceInformation(client)
    rd = ri.find_resource_definition("aws", "eks-cluster", "0.1.0")
    assert rd is not None
    assert rd.description == "An EKS cluster"  # defined by the YAML, not the manifest

    rcv = class_information.get_repo_class_version("hmd-vpc", "0.1.0")
    produces_rels = client.get_from_repo_class_version_produces_resource_definition_hmd_lang_deployment(
        rcv
    )
    assert len(produces_rels) == 1
    assert produces_rels[0].role == "primary"


def test_manifest_deploy_resources_tolerates_not_yet_registered_type(tmp_path):
    """A deploy.resources entry may reference a type not shipped in this
    artifact at all (already registered elsewhere, or registered later) --
    mirrors the tolerant stub-then-reconcile pattern already used for
    consumer-side resource dependencies.
    """
    manifest = {
        "deploy": {
            "dependencies": {},
            "default_configuration": {},
            "resources": {
                "cluster": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                }
            },
        }
    }
    client, class_information = _ingest_from_manifest(tmp_path, manifest)

    ri = ResourceInformation(client)
    assert ri.find_resource_definition("aws", "eks-cluster", "0.1.0") is not None

    rcv = class_information.get_repo_class_version("hmd-vpc", "0.1.0")
    produces_rels = client.get_from_repo_class_version_produces_resource_definition_hmd_lang_deployment(
        rcv
    )
    assert len(produces_rels) == 1


def test_manifest_deploy_resources_produces_false_is_not_declared(tmp_path):
    manifest = {
        "deploy": {
            "dependencies": {},
            "default_configuration": {},
            "resources": {
                "cluster": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                    "produces": False,
                }
            },
        }
    }
    client, class_information = _ingest_from_manifest(tmp_path, manifest)

    rcv = class_information.get_repo_class_version("hmd-vpc", "0.1.0")
    produces_rels = client.get_from_repo_class_version_produces_resource_definition_hmd_lang_deployment(
        rcv
    )
    assert len(produces_rels) == 0


def test_manifest_deploy_resources_missing_required_field_is_skipped(tmp_path):
    manifest = {
        "deploy": {
            "dependencies": {},
            "default_configuration": {},
            "resources": {
                "bad": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "no-version",
                    # "version" intentionally omitted
                },
                "good": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "has-version",
                    "version": "0.1.0",
                },
            },
        }
    }
    client, _ = _ingest_from_manifest(tmp_path, manifest)

    ri = ResourceInformation(client)
    assert ri.find_resource_definition("aws", "no-version", "") is None
    assert ri.find_resource_definition("aws", "has-version", "0.1.0") is not None


def test_manifest_discovery_is_stored_on_repo_class_version(tmp_path):
    """A top-level `discovery` block in manifest.json (BACON) is copied onto
    the ingested RepoClassVersion, unmodified."""
    discovery = {
        "summary": "Deployment orchestration microservice.",
        "entry_points": [
            {"path": "src/python/hmd_ms_deployment/deployment_ops.py", "description": "API."}
        ],
        "capabilities": [
            {
                "name": "POST /apiop/add_repo_class_version",
                "kind": "endpoint",
                "description": "Registers a new RepoClassVersion.",
            }
        ],
        "related_docs": [{"title": "User Guide", "path": "docs/user_guide.rst"}],
    }
    manifest = {
        "discovery": discovery,
        "deploy": {"dependencies": {}, "default_configuration": {}},
    }
    client, class_information = _ingest_from_manifest(tmp_path, manifest)

    rcv = class_information.get_repo_class_version("hmd-vpc", "0.1.0")
    assert rcv.discovery == discovery


def test_manifest_without_discovery_stores_empty_dict(tmp_path):
    manifest = {"deploy": {"dependencies": {}, "default_configuration": {}}}
    client, class_information = _ingest_from_manifest(tmp_path, manifest)

    rcv = class_information.get_repo_class_version("hmd-vpc", "0.1.0")
    assert rcv.discovery == {}


def test_manifest_deploy_resources_produces_defaults_true(tmp_path):
    manifest = {
        "deploy": {
            "dependencies": {},
            "default_configuration": {},
            "resources": {
                "cluster": {
                    "resource_namespace": "aws",
                    "resource_definition_name": "eks-cluster",
                    "version": "0.1.0",
                    # "produces" intentionally omitted -- BACON default is true
                }
            },
        }
    }
    client, class_information = _ingest_from_manifest(tmp_path, manifest)

    rcv = class_information.get_repo_class_version("hmd-vpc", "0.1.0")
    produces_rels = client.get_from_repo_class_version_produces_resource_definition_hmd_lang_deployment(
        rcv
    )
    assert len(produces_rels) == 1
