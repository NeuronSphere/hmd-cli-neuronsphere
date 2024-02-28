import pathlib

from setuptools import find_packages, setup

repo_dir = pathlib.Path(__file__).absolute().parent.parent.parent
version_file = repo_dir / "meta-data" / "VERSION"
readme = (repo_dir / "docs" / "readme.rst").read_text()

with open(version_file, "r") as vfl:
    version = vfl.read().strip()

setup(
    name="hmd-cli-neuronsphere",
    version=version,
    description="Local NeuronSphere Control CLI",
    long_description=readme,
    author="Adam Stortz",
    author_email="adam.stortz@hmdlabs.io",
    license="Apache 2.0",
    packages=find_packages(),
    include_package_data=True,
    package_data={
        "": [
            "services/*",
            "services/postgres/*",
            "services/postgres/always-initdb.d/*",
            "services/superset/*",
            "services/superset/.*",
            "services/superset/pythonpath_dev/*",
            "services/trino/config/*",
            "services/trino/config/catalog/*",
            "services/hive/*",
            "services/hadoop/*",
            "services/nginx/*",
            "services/queues/*",
            "services/transform/*",
            "services/naming/*",
            "services/telemetry/*",
        ]
    },
    entry_points={
        "hmd_cli_neuronsphere.enabled": [
            "main=hmd_cli_neuronsphere.plugins.main:enabled",
            "telemetry=hmd_cli_neuronsphere.plugins.telemetry:enabled",
            "graph=hmd_cli_neuronsphere.plugins.graph:enabled",
            "dynamodb=hmd_cli_neuronsphere.plugins.dynamodb:enabled",
            "jupyter=hmd_cli_neuronsphere.plugins.jupyter:enabled",
            "minio=hmd_cli_neuronsphere.plugins.airflow:enabled",
            "apache_superset=hmd_cli_neuronsphere.plugins.apache_superset:enabled",
            "airflow=hmd_cli_neuronsphere.plugins.airflow:enabled",
            "transform=hmd_cli_neuronsphere.plugins.transform:enabled",
            "trino=hmd_cli_neuronsphere.plugins.trino:enabled",
        ],
        "hmd_cli_neuronsphere.prepare_hmd_home": [
            "main=hmd_cli_neuronsphere.plugins.main:prepare_hmd_home",
            "telemetry=hmd_cli_neuronsphere.plugins.telemetry:prepare_hmd_home",
            "graph=hmd_cli_neuronsphere.plugins.graph:prepare_hmd_home",
            "dynamodb=hmd_cli_neuronsphere.plugins.dynamodb:prepare_hmd_home",
            "jupyter=hmd_cli_neuronsphere.plugins.jupyter:prepare_hmd_home",
            "minio=hmd_cli_neuronsphere.plugins.airflow:prepare_hmd_home",
            "apache_superset=hmd_cli_neuronsphere.plugins.apache_superset:prepare_hmd_home",
            "airflow=hmd_cli_neuronsphere.plugins.airflow:prepare_hmd_home",
            "transform=hmd_cli_neuronsphere.plugins.transform:prepare_hmd_home",
            "trino=hmd_cli_neuronsphere.plugins.trino:prepare_hmd_home",
        ],
        "hmd_cli_neuronsphere.get_resources": [
            "main=hmd_cli_neuronsphere.plugins.main:get_resources",
            "telemetry=hmd_cli_neuronsphere.plugins.telemetry:get_resources",
            "graph=hmd_cli_neuronsphere.plugins.graph:get_resources",
            "dynamodb=hmd_cli_neuronsphere.plugins.dynamodb:get_resources",
            "jupyter=hmd_cli_neuronsphere.plugins.jupyter:get_resources",
            "minio=hmd_cli_neuronsphere.plugins.airflow:get_resources",
            "apache_superset=hmd_cli_neuronsphere.plugins.apache_superset:get_resources",
            "airflow=hmd_cli_neuronsphere.plugins.airflow:get_resources",
            "transform=hmd_cli_neuronsphere.plugins.transform:get_resources",
            "trino=hmd_cli_neuronsphere.plugins.trino:get_resources",
        ],
        "hmd_cli_neuronsphere.render_compose_yaml": [
            "main=hmd_cli_neuronsphere.plugins.main:render_compose_yaml",
            "telemetry=hmd_cli_neuronsphere.plugins.telemetry:render_compose_yaml",
            "graph=hmd_cli_neuronsphere.plugins.graph:render_compose_yaml",
            "dynamodb=hmd_cli_neuronsphere.plugins.dynamodb:render_compose_yaml",
            "jupyter=hmd_cli_neuronsphere.plugins.jupyter:render_compose_yaml",
            "minio=hmd_cli_neuronsphere.plugins.airflow:render_compose_yaml",
            "apache_superset=hmd_cli_neuronsphere.plugins.apache_superset:render_compose_yaml",
            "airflow=hmd_cli_neuronsphere.plugins.airflow:render_compose_yaml",
            "transform=hmd_cli_neuronsphere.plugins.transform:render_compose_yaml",
            "trino=hmd_cli_neuronsphere.plugins.trino:render_compose_yaml",
        ],
    },
    install_requires=[],
)
