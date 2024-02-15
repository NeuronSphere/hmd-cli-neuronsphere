import os
from importlib.metadata import version

from cement import Controller, ex
from hmd_cli_tools import get_version

VERSION_BANNER = """
hmd neuronsphere version: {}
"""

VERSION = version("hmd_cli_neuronsphere")


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
