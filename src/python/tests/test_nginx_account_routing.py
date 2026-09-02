"""An environment's API routes must name the account they belong to.

One Floci serves every account and resolves which one a call belongs to from
the SigV4 credential scope -- the 12-digit access key *is* the account. nginx's
``proxy_pass``, though, issues an **unsigned** request, so there is nothing to
resolve from and the invocation lands in the default account. An environment's
REST API is not there, and Floci answers 404 -- indistinguishable from a route
that was never wired, which is why this surfaced as

    404 for http://hmd_proxy/local/hmd_ms_dbaccount/api/create_db_account

with the route present in the config the whole time.

Floci parses the account out of the credential scope without verifying the
signature, so a static header is enough. The control plane owns the default
account, so its routes need no header and deliberately do not get one.
"""

import types
import unittest
from unittest import mock

from hmd_cli_neuronsphere import nginx_router as nr


def _env(slug="local", account_id="000000000001"):
    return types.SimpleNamespace(
        slug=slug,
        account_id=account_id,
        floci_alias="neuronsphere",
        floci_container="floci",
    )


class EnvRoutesCarryTheirAccount(unittest.TestCase):
    def _render(self, env):
        with mock.patch.object(
            nr, "_write_fragment", lambda path, blocks, desc: blocks
        ):
            return "\n".join(
                nr.write_env_routes(env, {"hmd_ms_dbaccount": "be915fa884"})
            )

    def test_account_is_set_and_authorization_injected(self):
        out = self._render(_env())
        self.assertIn('set $ns_account "000000000001";', out)
        self.assertIn("proxy_set_header Authorization $ns_auth;", out)

    def test_each_environment_names_its_own_account(self):
        self.assertIn(
            'set $ns_account "000000000002";',
            self._render(_env(slug="dev2", account_id="000000000002")),
        )

    def test_no_account_means_no_header(self):
        """The control plane owns the default account; an unsigned call resolves
        there already, so its routes are left exactly as they were."""
        out = self._render(_env(account_id=None))
        self.assertNotIn("ns_account", out)
        self.assertNotIn("Authorization", out)


class BaseConfigDefinesTheMap(unittest.TestCase):
    def setUp(self):
        self.tmp = self.enterContext(
            mock.patch.dict("os.environ", {"HMD_HOME": self.enterContext(_tmpdir())})
        )

    def test_map_preserves_a_caller_supplied_authorization(self):
        config = nr.render_base_config().read_text()
        self.assertIn("map $http_authorization $ns_auth {", config)
        # Only an *empty* Authorization is filled in; anything else passes through,
        # so a bearer-token caller is not silently rewritten into an AWS one.
        self.assertIn("default $http_authorization;", config)
        self.assertIn("Credential=$ns_account/", config)

    def test_variable_is_declared_even_with_no_environment(self):
        """`$ns_auth`'s map references `$ns_account`; nginx refuses to load a
        config whose variables are never declared, so the server block declares
        it and each environment's locations override it."""
        config = nr.render_base_config().read_text()
        self.assertIn('set $ns_account "";', config)


import contextlib
import tempfile


@contextlib.contextmanager
def _tmpdir():
    with tempfile.TemporaryDirectory() as d:
        yield d


if __name__ == "__main__":
    unittest.main()
