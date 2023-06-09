import pathlib

from setuptools import find_packages, setup

repo_dir = pathlib.Path(__file__).absolute().parent.parent.parent
version_file = repo_dir / "meta-data" / "VERSION"
readme = (repo_dir / "README.md").read_text()

with open(version_file, "r") as vfl:
    version = vfl.read().strip()

setup(
    name="hmd-cli-neuronsphere",
    version=version,
    description="Local NeuronSphere Control CLI",
    long_description=readme,
    long_description_content_type="text/markdown",
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
        ]
    },
    install_requires=[],
)
