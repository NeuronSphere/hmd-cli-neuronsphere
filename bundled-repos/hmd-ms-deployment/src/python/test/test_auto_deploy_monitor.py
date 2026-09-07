from datetime import datetime
from unittest.mock import patch, MagicMock, call, ANY

from hmd_lang_deployment.deployment_set import DeploymentSet
from hmd_lang_deployment.environment import Environment
from hmd_ms_deployment.auto_deploy_monitor import AutoDeployMonitor


def test_process_new_content_items_1():
    mock_librarian_client = MagicMock()
    mock_librarian_client.invoke_custom_operation.return_value = ["1", "2"]
    mock_deployment_client = MagicMock()
    mock_get_deloyment_set = MagicMock()
    mock_deploy_change_set_to_environment = MagicMock()

    am = AutoDeployMonitor(
        mock_librarian_client, mock_deployment_client, "a_query", "auto_deploy"
    )
    result1 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.3/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }

    deployment_set1 = DeploymentSet(
        name="ds1",
        auto_deploy="true",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {"transforms": [], "approval": False},
            }
        ],
    )

    def get_deployment_set_side_effect(result, ts1, ts2):
        if result == result1:
            return deployment_set1

    mock_get_deloyment_set.side_effect = get_deployment_set_side_effect

    with patch.multiple(
        am,
        get_deployment_set=mock_get_deloyment_set,
        deploy_change_set_to_deployment_set=mock_deploy_change_set_to_environment,
    ) as a_dict:
        am._do_process_new_content_items(
            [result1],
            datetime.utcnow().isoformat(),
            datetime.utcnow().isoformat(),
            {"Authorization": "abc"},
        )
        mock_deploy_change_set_to_environment.assert_called_once_with(
            deployment_set1, {"hmd-ms-test": ("1.2.3", result1)}, "abc"
        )


def test_process_new_content_items_2():
    mock_librarian_client = MagicMock()
    mock_librarian_client.invoke_custom_operation.return_value = ["1", "2"]
    mock_deployment_client = MagicMock()
    mock_get_deloyment_set = MagicMock()
    mock_deploy_change_set_to_environment = MagicMock()

    am = AutoDeployMonitor(
        mock_librarian_client, mock_deployment_client, "a_query", "auto_deploy"
    )
    result1 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.3/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }
    result2 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.4/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }

    deployment_set1 = DeploymentSet(
        name="ds1",
        auto_deploy="true",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {"transforms": [], "approval": False},
            }
        ],
    )

    def get_deployment_set_side_effect(result, ts1, ts2):
        if result == result1:
            return deployment_set1
        if result == result2:
            return deployment_set1

    mock_get_deloyment_set.side_effect = get_deployment_set_side_effect

    with patch.multiple(
        am,
        get_deployment_set=mock_get_deloyment_set,
        deploy_change_set_to_deployment_set=mock_deploy_change_set_to_environment,
    ) as a_dict:
        am._do_process_new_content_items(
            [result1, result2],
            datetime.utcnow().isoformat(),
            datetime.utcnow().isoformat(),
            {"Authorization": "abc"},
        )
        mock_deploy_change_set_to_environment.assert_called_once_with(
            deployment_set1, {"hmd-ms-test": ("1.2.4", result2)}, "abc"
        )


def test_process_new_content_items_3():
    mock_librarian_client = MagicMock()
    mock_librarian_client.invoke_custom_operation.return_value = ["1", "2"]
    mock_deployment_client = MagicMock()
    mock_get_deloyment_set = MagicMock()
    mock_deploy_change_set_to_environment = MagicMock()

    am = AutoDeployMonitor(
        mock_librarian_client, mock_deployment_client, "a_query", "auto_deploy"
    )
    result1 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.3/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }
    result2 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.4/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }

    deployment_set1 = DeploymentSet(
        name="ds1",
        auto_deploy="true",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {"transforms": [], "approval": False},
            }
        ],
    )

    deployment_set2 = DeploymentSet(
        name="ds2",
        auto_deploy="true",
        definition=[
            {
                "environment": "test",
                "deployment_gate": {"transforms": [], "approval": False},
            }
        ],
    )

    def get_deployment_set_side_effect(result, ts1, ts2):
        if result == result1:
            return deployment_set1
        if result == result2:
            return deployment_set2

    mock_get_deloyment_set.side_effect = get_deployment_set_side_effect

    with patch.multiple(
        am,
        get_deployment_set=mock_get_deloyment_set,
        deploy_change_set_to_deployment_set=mock_deploy_change_set_to_environment,
    ) as a_dict:
        am._do_process_new_content_items(
            [result1, result2],
            datetime.utcnow().isoformat(),
            datetime.utcnow().isoformat(),
            {"Authorization": "abc"},
        )
        calls = [
            call(deployment_set1, {"hmd-ms-test": ("1.2.3", result1)}, "abc"),
            call(deployment_set2, {"hmd-ms-test": ("1.2.4", result2)}, "abc"),
        ]
        mock_deploy_change_set_to_environment.assert_has_calls(calls)


def test_process_new_content_items_4():
    mock_librarian_client = MagicMock()
    mock_librarian_client.invoke_custom_operation.return_value = ["1", "2"]
    mock_deployment_client = MagicMock()
    mock_get_deloyment_set = MagicMock()
    mock_deploy_change_set_to_environment = MagicMock()

    am = AutoDeployMonitor(
        mock_librarian_client, mock_deployment_client, "a_query", "auto_deploy"
    )
    result1 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.3/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }

    result2 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test2/1.2.4/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }

    deployment_set1 = DeploymentSet(
        name="ds1",
        auto_deploy="true",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {"transforms": [], "approval": False},
            }
        ],
    )

    def get_deployment_set_side_effect(result, ts1, ts2):
        return deployment_set1

    mock_get_deloyment_set.side_effect = get_deployment_set_side_effect

    with patch.multiple(
        am,
        get_deployment_set=mock_get_deloyment_set,
        deploy_change_set_to_deployment_set=mock_deploy_change_set_to_environment,
    ) as a_dict:
        am._do_process_new_content_items(
            [result1, result2],
            datetime.utcnow().isoformat(),
            datetime.utcnow().isoformat(),
            {"Authorization": "abc"},
        )
        mock_deploy_change_set_to_environment.assert_called_once_with(
            deployment_set1,
            {"hmd-ms-test": ("1.2.3", result1), "hmd-ms-test2": ("1.2.4", result2)},
            "abc",
        )


def test_process_new_content_items_5():
    mock_librarian_client = MagicMock()
    mock_deployment_client = MagicMock()
    mock_get_deloyment_set = MagicMock()
    mock_deploy_change_set_to_environment = MagicMock()

    am = AutoDeployMonitor(
        mock_librarian_client, mock_deployment_client, "a_query", "auto_deploy"
    )
    result1 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.3/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }

    result2 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test2/1.2.4/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }

    deployment_set1 = DeploymentSet(
        name="ds1",
        auto_deploy="false",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {"transforms": [], "approval": False},
            }
        ],
    )

    def get_deployment_set_side_effect(result, ts1, ts2):
        return deployment_set1

    mock_get_deloyment_set.side_effect = get_deployment_set_side_effect

    with patch.multiple(
        am,
        get_deployment_set=mock_get_deloyment_set,
        deploy_change_set_to_deployment_set=mock_deploy_change_set_to_environment,
    ) as a_dict:
        am._do_process_new_content_items(
            [result1, result2],
            datetime.utcnow().isoformat(),
            datetime.utcnow().isoformat(),
            {"Authorization": "abc"},
        )
        mock_deploy_change_set_to_environment.assert_not_called()


def test_process_new_content_items_6():
    mock_librarian_client = MagicMock()
    mock_librarian_client.invoke_custom_operation.return_value = ["1", "2"]
    mock_deployment_client = MagicMock()
    mock_get_deloyment_set = MagicMock()
    mock_deploy_change_set_to_environment = MagicMock()

    am = AutoDeployMonitor(
        mock_librarian_client, mock_deployment_client, "a_query", "auto_deploy"
    )
    result1 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.3/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }
    result2 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.4/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }

    deployment_set1 = DeploymentSet(
        name="ds1",
        auto_deploy="true",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {"transforms": [], "approval": False},
            }
        ],
    )

    deployment_set2 = DeploymentSet(
        name="ds2",
        definition=[
            {
                "environment": "test",
                "deployment_gate": {"transforms": [], "approval": False},
            }
        ],
    )

    def get_deployment_set_side_effect(result, ts1, ts2):
        if result == result1:
            return deployment_set1
        if result == result2:
            return deployment_set2

    mock_get_deloyment_set.side_effect = get_deployment_set_side_effect

    with patch.multiple(
        am,
        get_deployment_set=mock_get_deloyment_set,
        deploy_change_set_to_deployment_set=mock_deploy_change_set_to_environment,
    ) as a_dict:
        am._do_process_new_content_items(
            [result1, result2],
            datetime.utcnow().isoformat(),
            datetime.utcnow().isoformat(),
            {"Authorization": "abc"},
        )
        calls = [
            call(deployment_set1, {"hmd-ms-test": ("1.2.3", result1)}, "abc"),
        ]
        mock_deploy_change_set_to_environment.assert_has_calls(calls)


def test_deploy_change_set_to_deployment_set():
    mock_librarian_client = MagicMock()
    mock_deployment_client = MagicMock()
    mock_create_deploy_specs_for_artifact = MagicMock()
    mock_deployment_manager = MagicMock()

    am = AutoDeployMonitor(
        mock_librarian_client, mock_deployment_client, "a_query", "auto_deploy"
    )
    result1 = {
        "content_item": {
            "content_item_path": "repository:/hmd-ms-test/1.2.3/abc.zip",
            "identifier": "1",
        },
        "content_item_type": "build",
        "download_url": "a_url",
    }

    deployment_set1 = DeploymentSet(
        name="ds1",
        auto_deploy="true",
        definition=[
            {
                "environment": "dev",
                "deployment_gate": {"transforms": [], "approval": False},
            }
        ],
    )

    mock_create_deploy_specs_for_artifact.return_value = [
        {
            "repo_instance_name": "core-acm",
            "repo_class_name": "hmd-inf-acm",
            "repo_class_version": "0.1.6",
            "deployment_id": "aaa",
            "instance_configuration": {},
            "dependencies": {},
        },
    ]

    with patch.multiple(
        am,
        create_deploy_specs_for_artifact=mock_create_deploy_specs_for_artifact,
        deployment_manager=mock_deployment_manager,
    ) as a_dict:
        am.deploy_change_set_to_deployment_set(
            deployment_set1, {"hmd-ms-test": ("1.2.3", result1)}, "abc"
        )
        mock_deployment_client.search_entity.assert_called_once_with(
            Environment.get_namespace_name(),
            {"attribute": "type", "operator": "=", "value": "dev"},
        )
        mock_create_deploy_specs_for_artifact.assert_called_once_with(
            "hmd-ms-test", "1.2.3", ANY, "build"
        )
        mock_deployment_client.upsert_entity.assert_called_once()
        mock_deployment_manager.apply_changeset.assert_called_once()
