"""No AWS credential in this package may be a placeholder or ambient value.

One Floci serves every account and resolves which one a caller means from the
12-digit access key id -- the key *is* the account. Two habits break that, and
both fail silently:

* A placeholder (``test``, ``dummykey``, ``localstack``) is not a 12-digit id,
  so Floci resolves it to the **default** account. An environment's caller then
  reads the control plane's buckets, secrets and tables. Because the resource
  names are identical across accounts, nothing looks wrong until something is
  missing -- which is how a present secret was reported as
  ``Secret does not exist``.
* Reading an ambient ``$AWS_ACCESS_KEY_ID`` is worse: a developer with real AWS
  credentials exported redirects local calls into a third account entirely.

This test is a guard, not a design statement: the same mistake was made
independently in the ext-secrets BOM values, the ext-secrets *operator* values,
the k3s chart-plugin Floci client and every environment Lambda's environment.
Finding them took four failed `up` runs. A new one should fail here instead.

Use ``floci_deployer.account_access_key(env)`` (or a ``FlociTarget``'s
``access_key_id``) for anything that reaches Floci.
"""

import ast
import pathlib
import unittest

PACKAGE = pathlib.Path(__file__).resolve().parent.parent / "hmd_cli_neuronsphere"

# Values that do not name an account.
PLACEHOLDERS = {"test", "dummykey", "localstack", "dummy", "foo", "changeme"}

# Keys whose value selects a Floci account.
CREDENTIAL_KEYS = {
    "AWS_ACCESS_KEY_ID",
    "AWS_SECRET_ACCESS_KEY",
    "aws_access_key_id",
    "aws_secret_access_key",
    "localAccessKeyId",
    "localSecretAccessKey",
}

# (file, reason) -- deliberate, reviewed exceptions.
ALLOWED = {
    # Injects the developer's *real* AWS credentials from a named profile so a
    # notebook can reach real AWS. Not a Floci account selector.
    "plugins/jupyter.py",
}


def _sources():
    for path in sorted(PACKAGE.rglob("*.py")):
        rel = path.relative_to(PACKAGE).as_posix()
        if rel in ALLOWED or "__pycache__" in rel:
            continue
        yield rel, path.read_text()


def _string_value(node):
    """The literal string a node evaluates to, or None."""
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    return None


def _is_ambient_env_read(node) -> bool:
    """``os.environ.get("AWS_ACCESS_KEY_ID", ...)`` or ``os.environ[...]``."""
    if isinstance(node, ast.Call):
        func = node.func
        if (
            isinstance(func, ast.Attribute)
            and func.attr == "get"
            and node.args
            and _string_value(node.args[0]) in CREDENTIAL_KEYS
        ):
            return True
    if isinstance(node, ast.Subscript):
        return _string_value(node.slice) in CREDENTIAL_KEYS
    return False


class NoPlaceholderOrAmbientCredentials(unittest.TestCase):
    def test_no_credential_is_a_placeholder(self):
        bad = []
        for rel, src in _sources():
            tree = ast.parse(src)
            for node in ast.walk(tree):
                # {"AWS_ACCESS_KEY_ID": "test"} and keyword aws_access_key_id="test"
                if isinstance(node, ast.Dict):
                    for key, value in zip(node.keys, node.values):
                        if _string_value(key) in CREDENTIAL_KEYS:
                            v = _string_value(value)
                            if v in PLACEHOLDERS:
                                bad.append(f"{rel}:{node.lineno} -> {v!r}")
                if isinstance(node, ast.Call):
                    for kw in node.keywords:
                        if kw.arg in CREDENTIAL_KEYS:
                            v = _string_value(kw.value)
                            if v in PLACEHOLDERS:
                                bad.append(f"{rel}:{node.lineno} -> {v!r}")
        self.assertEqual(
            bad,
            [],
            "placeholder AWS credentials resolve to the DEFAULT Floci account, "
            "silently: use floci_deployer.account_access_key(env). Offenders:\n"
            + "\n".join(bad),
        )

    def test_no_credential_is_read_from_the_ambient_environment(self):
        bad = []
        for rel, src in _sources():
            tree = ast.parse(src)
            for node in ast.walk(tree):
                if isinstance(node, ast.Dict):
                    for key, value in zip(node.keys, node.values):
                        if _string_value(
                            key
                        ) in CREDENTIAL_KEYS and _is_ambient_env_read(value):
                            bad.append(f"{rel}:{node.lineno}")
                if isinstance(node, ast.Call):
                    for kw in node.keywords:
                        if kw.arg in CREDENTIAL_KEYS and _is_ambient_env_read(kw.value):
                            bad.append(f"{rel}:{node.lineno}")
        self.assertEqual(
            bad,
            [],
            "an ambient $AWS_ACCESS_KEY_ID redirects local calls into whatever "
            "account a developer's real credentials name: use "
            "floci_deployer.account_access_key(env). Offenders:\n" + "\n".join(bad),
        )


class TheGuardActuallyCatchesThings(unittest.TestCase):
    """A guard that cannot fail is worse than none: it reads as coverage."""

    def test_a_placeholder_would_be_caught(self):
        tree = ast.parse('x = {"AWS_ACCESS_KEY_ID": "test"}')
        found = [
            _string_value(v)
            for n in ast.walk(tree)
            if isinstance(n, ast.Dict)
            for k, v in zip(n.keys, n.values)
            if _string_value(k) in CREDENTIAL_KEYS
        ]
        self.assertIn("test", found)

    def test_an_ambient_read_would_be_caught(self):
        tree = ast.parse(
            'x = {"AWS_ACCESS_KEY_ID": os.environ.get("AWS_ACCESS_KEY_ID")}'
        )
        hits = [
            _is_ambient_env_read(v)
            for n in ast.walk(tree)
            if isinstance(n, ast.Dict)
            for k, v in zip(n.keys, n.values)
            if _string_value(k) in CREDENTIAL_KEYS
        ]
        self.assertEqual(hits, [True])


if __name__ == "__main__":
    unittest.main()
