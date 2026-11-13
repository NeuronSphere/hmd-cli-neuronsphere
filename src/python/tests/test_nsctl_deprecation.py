"""`hmd neuronsphere` commands that nsctl now covers warn on stderr and still run."""

import pytest

from hmd_cli_neuronsphere.controller import EnvController, LocalController, _deprecated

# method name -> the nsctl verb that replaces it
LOCAL_COVERED = {
    "up": "nsctl env start",
    "down": "nsctl env stop",
    "restart": "nsctl env apply",
    "status": "nsctl env status",
    "update_images": "nsctl env apply --pull",
    "pull_artifact": "nsctl artifact pull",
    "push_artifact": "nsctl artifact register",
    "db_upgrade": "nsctl db upgrade",
}
ENV_COVERED = {
    "create": "nsctl env add",
    "plan": "nsctl env plan",
    "list": "nsctl env list",
    "delete": "nsctl env delete",
    "status": "nsctl env status",
}
# No nsctl equivalent yet: must keep working without a deprecation warning.
NOT_COVERED = {
    LocalController: [
        "run",
        "configure",
        "validate_plugin",
        "init_plugin",
        "list_local_plugins",
        "db_provision",
        "db_register",
        "gen_dev_config",
        "route_service",
    ],
    EnvController: ["show", "use"],
}


def test_decorator_warns_on_stderr_and_still_runs(capsys):
    @_deprecated("hmd old", "nsctl new")
    def fn(self, x):
        print("ran", x)
        return 7

    assert fn(None, 1) == 7
    out, err = capsys.readouterr()
    assert out == "ran 1\n"
    assert "deprecated" in err
    assert "`hmd old`" in err
    assert "`nsctl new`" in err


@pytest.mark.parametrize("name,new", LOCAL_COVERED.items())
def test_local_commands_name_their_nsctl_replacement(name, new):
    assert getattr(LocalController, name).__deprecated_by__ == new


@pytest.mark.parametrize("name,new", ENV_COVERED.items())
def test_env_commands_name_their_nsctl_replacement(name, new):
    assert getattr(EnvController, name).__deprecated_by__ == new


@pytest.mark.parametrize(
    "cls,name",
    [(c, n) for c, names in NOT_COVERED.items() for n in names],
)
def test_commands_without_an_nsctl_equivalent_do_not_warn(cls, name):
    assert not hasattr(getattr(cls, name), "__deprecated_by__")
