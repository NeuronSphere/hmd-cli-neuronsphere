import os
from importlib.metadata import version

from cement import Controller, ex
from hmd_cli_tools import get_version
from hmd_cli_tools.hmd_cli_tools import load_hmd_env, set_hmd_env
from hmd_cli_tools.prompt_tools import prompt_for_values

VERSION_BANNER = """
hmd neuronsphere version: {}
"""

VERSION = version("hmd_cli_neuronsphere")


CONFIG_VALUES = {
    "HMD_LOCAL_NS_CONTAINER_REGISTRY": {
        "hidden": True,
        "default": "ghcr.io/neuronsphere",
    }
}


class LocalController(Controller):
    class Meta:
        label = "neuronsphere"

        stacked_type = "nested"
        stacked_on = "base"

        # text displayed at the top of --help output
        description = "Local NeuronSphere Control CLI"

        arguments = (
            (
                ["-v", "--version"],
                {
                    "help": "Display the version of the  command.",
                    "action": "version",
                    "version": VERSION_BANNER.format(VERSION),
                },
            ),
        )

    def _default(self):
        """Default action if no sub-command is passed."""
        self._parser.print_help()

    @ex(help="Start the local NeuronSphere")
    def up(self):
        from .hmd_cli_neuronsphere import start_neuronsphere

        start_neuronsphere()

    @ex(help="Stop the local NeuronSphere")
    def down(self):
        from .hmd_cli_neuronsphere import stop_neuronsphere

        stop_neuronsphere()

    @ex(
        help="Restart the local NeuronSphere",
        arguments=[
            (
                ["-s", "--service_name"],
                {
                    "help": "name of service to restart",
                    "action": "store",
                    "dest": "service_name",
                    "nargs": "*",
                },
            )
        ],
    )
    def restart(self):
        from .hmd_cli_neuronsphere import restart_service

        restart_service(self.app.pargs.service_name)

    @ex(
        help="Run a NeuronSphere microservice locally",
        arguments=[
            (
                ["instance_name"],
                {
                    "help": "name to assign service",
                    "action": "store",
                },
            ),
            (
                ["-mnt", "--mount"],
                {
                    "help": "local Python packages to mount into the container",
                    "action": "store",
                    "dest": "mounts",
                    "required": False,
                    "nargs": "*",
                    "default": [],
                },
            ),
        ],
    )
    def run(self):
        from .hmd_cli_neuronsphere import run_local_service

        run_local_service(
            self.app.pargs.repo_name,
            self.app.pargs.repo_version,
            self.app.pargs.instance_name,
            mount_packages=self.app.pargs.mounts,
        )

    @ex(help="pulls latest versions of required images")
    def update_images(self):
        from .hmd_cli_neuronsphere import update_images

        update_images()

    @ex(
        help="configures HMD environment variables",
        arguments=[],
    )
    def configure(self):
        load_hmd_env()

        results = prompt_for_values(CONFIG_VALUES)

        for k, v in results.items():
            set_hmd_env(k, str(v))

    @ex(
        help="Validate a local plugin configuration (nsplugin.json)",
        arguments=[
            (
                ["path"],
                {
                    "help": "Path to nsplugin.json file or directory containing it",
                    "action": "store",
                    "nargs": "?",
                    "default": ".",
                },
            ),
            (
                ["--no-file-check"],
                {
                    "help": "Skip checking if referenced files exist",
                    "action": "store_true",
                    "dest": "no_file_check",
                },
            ),
        ],
    )
    def validate_plugin(self):
        from pathlib import Path

        from .validators import validate_nsplugin

        path = Path(self.app.pargs.path)
        check_files = not self.app.pargs.no_file_check

        result = validate_nsplugin(path, check_files=check_files)

        if result.errors:
            print("Errors:")
            for error in result.errors:
                print(f"  - {error}")

        if result.warnings:
            print("Warnings:")
            for warning in result.warnings:
                print(f"  - {warning}")

        if result.valid:
            if not result.warnings:
                print("Plugin configuration is valid.")
            else:
                print("\nPlugin configuration is valid with warnings.")
            return

        print("\nPlugin configuration is INVALID.")
        raise SystemExit(1)
