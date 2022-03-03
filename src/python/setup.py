import pathlib

from setuptools import find_packages, setup

repo_dir = pathlib.Path(__file__).absolute().parent.parent.parent
version_file = repo_dir / "meta-data" / "VERSION"

with open(version_file, "r") as vfl:
    version = vfl.read().strip()

setup(
    name="hmd-cli-neuronsphere",
    version=version,
    description="Local NeuronSphere Control CLI",
    author="Adam Stortz",
    author_email="adam.stortz@hmdlabs.io",
    license="unlicensed",
    packages=find_packages(),
    include_package_data=True,
    package_data={
        "": [
            "services/*",
            "services/superset/*",
            "services/superset/.env*",
            "services/superset/pythonpath_dev/*",
        ]
    },
    install_requires=[],
)
