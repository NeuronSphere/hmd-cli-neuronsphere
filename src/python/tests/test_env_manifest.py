"""Declarative environment manifests: parsing and validation.

A manifest is the *declared* desired state of a local environment, and
``up --prune`` destroys whatever the declaration leaves out. That makes the
parser's and validator's failure modes unusually consequential, so what is
pinned here is mostly the things that must never pass silently:

1. YAML and JSON are the same format -- a manifest must not mean two different
   things depending on the extension it was saved under.
2. Omitting ``plugins`` is not the same as ``plugins: []``. The first keeps the
   historical "every installed plugin contributes" behaviour; the second is a
   deliberate "none". Collapsing them would silently empty an environment.
3. A declared instance may not take a core instance's name, and a local repo
   must actually exist on disk -- otherwise the failure surfaces deep inside a
   projectbuilder container, long after `up` started changing things.

Run directly: ``python -m pytest src/python/tests/test_env_manifest.py``
"""

import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import yaml

from hmd_cli_neuronsphere import env_manifest as em
from hmd_cli_neuronsphere.validators import env_manifest_validator as v

MANIFEST = {
    "version": 1,
    "name": "dev2",
    "plugins": ["ns-telemetry"],
    "plugin_config": {"ns-telemetry": {"profile": "minimal"}},
    "repos": [
        {
            "instance_name": "my-api",
            "repo_class_name": "hmd-ms-myapi",
            "source": {"type": "local"},
            "instance_configuration": {"replicas": 1},
            "dependencies": {"eks-cluster": "local-neuronsphere"},
        }
    ],
}


class _TempHome(unittest.TestCase):
    """Isolate HMD_HOME/HMD_REPO_HOME from the developer's real environment."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.home = Path(self._tmp.name) / "hmd_home"
        self.repo_home = Path(self._tmp.name) / "repos"
        (self.repo_home / "hmd-ms-myapi" / "meta-data").mkdir(parents=True)
        (self.repo_home / "hmd-ms-myapi" / "meta-data" / "VERSION").write_text(
            "2.3.4\n"
        )
        self.home.mkdir(parents=True)
        self._env = mock.patch.dict(
            os.environ,
            {"HMD_HOME": str(self.home), "HMD_REPO_HOME": str(self.repo_home)},
            clear=False,
        )
        self._env.start()
        os.environ.pop("HMD_LOCAL_ENV_MANIFEST", None)

    def tearDown(self):
        self._env.stop()
        self._tmp.cleanup()

    def write(self, name, doc, as_json=False):
        path = self.home / "environments" / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(doc) if as_json else yaml.safe_dump(doc))
        return path


class ParseTests(_TempHome):
    def test_yaml_and_json_parse_identically(self):
        from_yaml = em.load_manifest_file(self.write("a.yaml", MANIFEST))
        from_json = em.load_manifest_file(self.write("b.json", MANIFEST, as_json=True))
        self.assertEqual(from_yaml.name, from_json.name)
        self.assertEqual(from_yaml.plugins, from_json.plugins)
        self.assertEqual(
            [r.instance_name for r in from_yaml.repos],
            [r.instance_name for r in from_json.repos],
        )
        self.assertEqual(
            from_yaml.repos[0].dependencies, from_json.repos[0].dependencies
        )

    def test_fields_round_trip(self):
        m = em.load_manifest_file(self.write("dev2.yaml", MANIFEST))
        repo = m.repos[0]
        self.assertEqual(m.plugin_config, {"ns-telemetry": {"profile": "minimal"}})
        self.assertEqual(repo.repo_class_name, "hmd-ms-myapi")
        self.assertEqual(repo.source_type, "local")
        self.assertEqual(repo.instance_configuration, {"replicas": 1})

    def test_absent_plugins_is_not_the_same_as_empty(self):
        without = dict(MANIFEST)
        without.pop("plugins")
        without.pop("plugin_config")
        m = em.load_manifest_file(self.write("a.yaml", without))
        self.assertIsNone(m.plugins)
        self.assertFalse(m.strict_plugins)
        self.assertIsNone(m.enabled_plugins())

        empty = dict(without, plugins=[])
        m2 = em.load_manifest_file(self.write("b.yaml", empty))
        self.assertEqual(m2.plugins, [])
        self.assertTrue(m2.strict_plugins)
        self.assertEqual(m2.enabled_plugins(), set())

    def test_repo_path_defaults_to_repo_home(self):
        m = em.load_manifest_file(self.write("dev2.yaml", MANIFEST))
        self.assertEqual(
            m.repos[0].resolved_repo_path(),
            os.path.join(str(self.repo_home), "hmd-ms-myapi"),
        )

    def test_explicit_source_path_wins(self):
        doc = json.loads(json.dumps(MANIFEST))
        doc["repos"][0]["source"]["path"] = str(self.repo_home / "hmd-ms-myapi")
        m = em.load_manifest_file(self.write("dev2.yaml", doc))
        self.assertEqual(
            m.repos[0].resolved_repo_path(), str(self.repo_home / "hmd-ms-myapi")
        )

    def test_a_list_document_is_rejected(self):
        path = self.home / "environments" / "bad.yaml"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(yaml.safe_dump([{"repo_instance_name": "x"}]))
        with self.assertRaises(em.EnvManifestError):
            em.load_manifest_file(path)


class DiscoveryTests(_TempHome):
    def test_missing_manifest_is_none_not_an_error(self):
        self.assertIsNone(em.load_manifest("dev2"))

    def test_extension_precedence_prefers_yaml(self):
        self.write("dev2.json", dict(MANIFEST, name="from-json"), as_json=True)
        self.write("dev2.yaml", dict(MANIFEST, name="from-yaml"))
        self.assertEqual(em.load_manifest("dev2").name, "from-yaml")

    def test_env_var_override(self):
        path = self.write("elsewhere.yaml", dict(MANIFEST, name="override"))
        with mock.patch.dict(
            os.environ, {"HMD_LOCAL_ENV_MANIFEST": str(path)}, clear=False
        ):
            self.assertEqual(em.load_manifest("dev2").name, "override")

    def test_install_copies_into_hmd_home(self):
        src = Path(self._tmp.name) / "authored.yaml"
        src.write_text(yaml.safe_dump(MANIFEST))
        dest = em.install_manifest("dev2", str(src))
        self.assertEqual(dest, em.manifests_root() / "dev2.yaml")
        self.assertEqual(em.load_manifest("dev2").name, "dev2")

    def test_install_rejects_an_invalid_manifest(self):
        src = Path(self._tmp.name) / "authored.yaml"
        src.write_text(yaml.safe_dump({"version": 1, "repos": []}))  # no name
        with self.assertRaises(em.EnvManifestError):
            em.install_manifest("dev2", str(src))
        self.assertFalse((em.manifests_root() / "dev2.yaml").exists())


class ValidationTests(_TempHome):
    def _manifest(self, **overrides):
        return em.parse_manifest(dict(MANIFEST, **overrides))

    def test_valid_manifest_has_no_errors(self):
        self.assertEqual(v.validate_manifest(self._manifest()), [])

    def test_name_is_required(self):
        errors = v.validate_manifest(self._manifest(name=None))
        self.assertTrue(any("'name' is required" in e for e in errors))

    def test_unsupported_version(self):
        errors = v.validate_manifest(self._manifest(version=99))
        self.assertTrue(any("unsupported manifest version" in e for e in errors))

    def test_reserved_instance_name_is_rejected(self):
        errors = v.validate_manifest(
            self._manifest(
                repos=[
                    {
                        "instance_name": "local-neuronsphere",
                        "repo_class_name": "hmd-ms-myapi",
                    }
                ]
            )
        )
        self.assertTrue(any("reserved for the local NeuronSphere" in e for e in errors))

    def test_db_owner_instance_is_reserved_too(self):
        errors = v.validate_manifest(
            self._manifest(
                repos=[
                    {
                        "instance_name": "local-databases",
                        "repo_class_name": "hmd-ms-myapi",
                    }
                ]
            )
        )
        self.assertTrue(any("reserved" in e for e in errors))

    def test_duplicate_instance_names(self):
        repo = MANIFEST["repos"][0]
        errors = v.validate_manifest(self._manifest(repos=[repo, dict(repo)]))
        self.assertTrue(any("duplicate instance_name" in e for e in errors))

    def test_missing_local_repo_is_caught_before_deploy(self):
        errors = v.validate_manifest(
            self._manifest(
                repos=[
                    {"instance_name": "ghost", "repo_class_name": "hmd-ms-nonexistent"}
                ]
            )
        )
        self.assertTrue(any("local repo path does not exist" in e for e in errors))

    def test_artifact_source_reports_not_yet_supported(self):
        errors = v.validate_manifest(
            self._manifest(
                repos=[
                    {
                        "instance_name": "my-api",
                        "repo_class_name": "hmd-ms-myapi",
                        "source": {"type": "artifact"},
                    }
                ]
            )
        )
        self.assertTrue(any("not supported yet" in e for e in errors))

    def test_unknown_source_type(self):
        errors = v.validate_manifest(
            self._manifest(
                repos=[
                    {
                        "instance_name": "my-api",
                        "repo_class_name": "hmd-ms-myapi",
                        "source": {"type": "carrier-pigeon"},
                    }
                ]
            )
        )
        self.assertTrue(any("unknown source type" in e for e in errors))

    def test_dependency_values_must_be_names(self):
        errors = v.validate_manifest(
            self._manifest(
                repos=[
                    {
                        "instance_name": "my-api",
                        "repo_class_name": "hmd-ms-myapi",
                        "dependencies": {"eks-cluster": {"nested": "object"}},
                    }
                ]
            )
        )
        self.assertTrue(any("must be an instance name" in e for e in errors))

    def test_dependency_list_of_names_is_allowed(self):
        errors = v.validate_manifest(
            self._manifest(
                repos=[
                    {
                        "instance_name": "my-api",
                        "repo_class_name": "hmd-ms-myapi",
                        "dependencies": {"peers": ["a", "b"]},
                    }
                ]
            )
        )
        self.assertEqual(errors, [])

    def test_plugin_config_for_a_disabled_plugin_is_flagged(self):
        errors = v.validate_manifest(
            self._manifest(plugin_config={"ns-nope": {"a": 1}})
        )
        self.assertTrue(any("not in the 'plugins' allow-list" in e for e in errors))


class LegacyBomFileTests(_TempHome):
    def test_flat_bom_array_becomes_a_non_strict_manifest(self):
        bom = [
            {
                "repo_instance_name": "project-bucket",
                "repo_class_name": "hmd-inf-s3bucket",
                "instance_configuration": {},
                "dependencies": {},
            }
        ]
        path = Path(self._tmp.name) / "bom.json"
        path.write_text(json.dumps(bom))
        m = em.manifest_from_bom_file("dev2", str(path))
        # plugins stays None -- a legacy BOM file never restricted plugins, and
        # turning it into an allow-list would silently drop every installed one.
        self.assertIsNone(m.plugins)
        self.assertEqual([r.instance_name for r in m.repos], ["project-bucket"])


if __name__ == "__main__":
    unittest.main()
