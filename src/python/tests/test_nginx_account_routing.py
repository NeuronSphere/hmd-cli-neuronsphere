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

The scope is set *unconditionally*. It used to fill only an empty
``Authorization``, which made the two uses of that one header mutually
exclusive: a caller presenting a real bearer token kept it, left Floci nothing
to resolve the account from, and 404d -- so only the environment whose account
happens to be Floci's default could ever serve an authenticated request. The
caller's token is relocated to ``X-NS-Authorization`` instead, where
``hmd-lib-auth``'s ``auth_token()`` reads it back.
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

    def test_credential_scope_names_the_account(self):
        out = self._render(_env())
        self.assertIn(
            'proxy_set_header Authorization "AWS4-HMAC-SHA256 '
            "Credential=000000000001/",
            out,
        )

    def test_each_environment_names_its_own_account(self):
        self.assertIn(
            "Credential=000000000002/",
            self._render(_env(slug="dev2", account_id="000000000002")),
        )

    def test_the_callers_token_is_relocated_not_dropped(self):
        """The scope displaces whatever the caller sent, so the token has to
        travel beside it or an authenticated request arrives anonymous."""
        out = self._render(_env())
        self.assertIn("proxy_set_header X-NS-Authorization $http_authorization;", out)

    def test_authorization_never_defers_to_the_caller(self):
        """The defect this replaced: `map`-ing an already-present Authorization
        through unchanged left the invocation with no account scope at all."""
        out = self._render(_env())
        self.assertNotIn("proxy_set_header Authorization $http_authorization;", out)
        self.assertNotIn("$ns_auth", out)
        self.assertNotIn("$ns_account", out)

    def test_no_account_means_no_header(self):
        """The control plane owns the default account; an unsigned call resolves
        there already, so its routes are left exactly as they were -- and a
        bearer token reaches the service in Authorization, untouched."""
        out = self._render(_env(account_id=None))
        self.assertNotIn("Authorization", out)


class BaseConfigNoLongerCarriesTheMap(unittest.TestCase):
    def setUp(self):
        self.tmp = self.enterContext(
            mock.patch.dict("os.environ", {"HMD_HOME": self.enterContext(_tmpdir())})
        )

    def test_the_map_and_its_variable_are_gone(self):
        """Both existed only to let a caller's Authorization win, which is the
        behaviour being reversed. A leftover `$ns_account` declaration would be
        dead config, and a leftover map would silently keep working."""
        config = nr.render_base_config().read_text()
        self.assertNotIn("$ns_auth", config)
        self.assertNotIn("$ns_account", config)


import contextlib
import tempfile


@contextlib.contextmanager
def _tmpdir():
    with tempfile.TemporaryDirectory() as d:
        yield d


if __name__ == "__main__":
    unittest.main()
