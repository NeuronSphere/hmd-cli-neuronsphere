import os

from cement import Controller, ex
from importlib.metadata import version
from hmd_cli_tools import get_version

VERSION_BANNER = """
hmd  version: {}
"""

VERSION = version("hmd_cli_neuronsphere")


class LocalController(Controller):
    class Meta:
        label = ""

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

        self.app.args.print_help()

    @ex(
        help="build <...>",
        arguments=[
            (["-n", "--name"], {"action": "store", "dest": "name", "required": False})
        ],
    )
    def build(self):
        args = {}
        # build the args values...

        from .hmd_cli_neuronsphere import build as do_build

        result = do_build(**args)
