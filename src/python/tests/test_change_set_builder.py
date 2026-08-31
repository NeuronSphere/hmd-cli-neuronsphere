"""Compiling a manifest into a hmd_lang_deployment.change_set definition.

The change_set ``definition`` schema is a three-way ``oneOf`` with
``additionalProperties: false`` on every branch, and the branch this CLI emits
(variant C) requires all six of ``deployment_id``, ``repo_instance_name``,
``repo_class_name``, ``repo_class_version``, ``instance_configuration`` and
``dependencies``. One stray key, or a missing ``dependencies``, fails all three
branches with an error that does not name the offending entry -- so the exact
key set is pinned here rather than left to be discovered against a live server.

Also pinned: the merge precedence that decides what an environment contains.
A manifest that enables two plugins must not pick up a third just because it
happens to be pip-installed, and a declared repo must win over a built-in of
the same instance name.

Every test patches ``_collect_plugin_bom_entries``; without it an installed
``hmd-cli-plugin-ns-*`` package would contribute entries and mask assertions.

Run directly: ``python -m pytest src/python/tests/test_change_set_builder.py``
"""

import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from hmd_cli_neuronsphere import bom_seeder as b
from hmd_cli_neuronsphere import change_set_builder as csb
from hmd_cli_neuronsphere import env_manifest as em


class _Env:
    """Stand-in for env_registry.LocalEnvironment (no HMD_HOME needed)."""

    def __init__(self, slug="dev2"):
        self.slug = slug
        self.name = slug
        self.deployment_id = slug
        self.account_id = "000000000002"
        self.core_instance_name = "local-neuronsphere"
        self.legacy_layout = False
        # Fourth of the four host ports each environment reserves; the Deployment
        # GUI is served here (see bom_seeder.gui_port).
        self.spare_port = 19003

    @property
    def is_default(self):
        return self.slug == "local"


class _BuilderTest(unittest.TestCase):
    """Base: an isolated HMD_REPO_HOME and no plugin contributions.

    Subclasses that exercise the collector itself set ``PATCH_PLUGINS = False``
    and stub ``entry_points`` instead.
    """

    PATCH_PLUGINS = True

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.repo_home = Path(self._tmp.name) / "repos"
        self._make_repo("hmd-ms-myapi", "2.3.4")
        self._env_patch = mock.patch.dict(
            os.environ, {"HMD_REPO_HOME": str(self.repo_home)}, clear=False
        )
        self._env_patch.start()
        for var in (
            "HMD_LOCAL_BOM_FILE",
            "HMD_LOCAL_NEURONSPHERE_ENABLE_EXT_SECRETS",
            "HMD_LOCAL_NEURONSPHERE_ENABLE_GUI",
            b.PREFER_LOCAL_VERSIONS_ENV,
            b.ARTIFACT_ROOTS_ENV,
            b._local_version_env_var("hmd-ms-myapi"),
        ):
            os.environ.pop(var, None)
        # The index is cached and its roots are partly env-derived, so a stale
        # one would carry another test's fake artifacts into this one.
        b._reset_artifact_version_index()
        # Installed plugin packages must never leak into these assertions.
        self._plugins = None
        if self.PATCH_PLUGINS:
            self._plugins = mock.patch.object(
                b, "_collect_plugin_bom_entries", return_value=[]
            )
            self._plugins.start()
        # Keep the host's docker config out of ext-secrets' instance_configuration.
        self._docker = mock.patch.object(
            b, "local_docker_config_json", return_value=None
        )
        self._docker.start()

    def tearDown(self):
        self._docker.stop()
        if self._plugins is not None:
            self._plugins.stop()
        self._env_patch.stop()
        b._reset_artifact_version_index()
        self._tmp.cleanup()

    def _make_repo(self, name, version, root=None):
        base = Path(root or self.repo_home) / name / "meta-data"
        base.mkdir(parents=True, exist_ok=True)
        (base / "VERSION").write_text(f"{version}\n")
        return base.parent

    def _manifest(self, **kwargs):
        doc = {"version": 1, "name": "dev2"}
        doc.update(kwargs)
        return em.parse_manifest(doc)


class EntryShapeTests(_BuilderTest):
    def test_entry_has_exactly_the_variant_c_keys(self):
        definition = csb.build_definition(
            env=_Env(),
            manifest=self._manifest(
                plugins=[],
                repos=[{"instance_name": "my-api", "repo_class_name": "hmd-ms-myapi"}],
            ),
        )
        entry = next(e for e in definition if e["repo_instance_name"] == "my-api")
        self.assertEqual(set(entry), set(csb.CHANGE_SET_ENTRY_KEYS))

    def test_extra_keys_are_dropped(self):
        entry = csb.normalize_entry(
            {
                "repo_instance_name": "x",
                "repo_class_name": "hmd-ms-myapi",
                "image_only": True,
                "auto_deploy": "true",
                "something_else": 1,
            }
        )
        self.assertEqual(set(entry), set(csb.CHANGE_SET_ENTRY_KEYS))

    def test_required_mappings_default_to_empty(self):
        entry = csb.normalize_entry(
            {"repo_instance_name": "x", "repo_class_name": "hmd-ms-myapi"}
        )
        self.assertEqual(entry["instance_configuration"], {})
        self.assertEqual(entry["dependencies"], {})

    def test_local_working_tree_is_ignored_without_an_override(self):
        # The tree is at 2.3.4, but a declared version is a published one.
        entry = csb.normalize_entry(
            {
                "repo_instance_name": "my-api",
                "repo_class_name": "hmd-ms-myapi",
                "repo_class_version": "0.0.1",
            }
        )
        self.assertEqual(entry["repo_class_version"], "0.0.1")

    def test_local_working_tree_wins_with_the_global_override(self):
        with mock.patch.dict(
            os.environ, {b.PREFER_LOCAL_VERSIONS_ENV: "true"}, clear=False
        ):
            entry = csb.normalize_entry(
                {
                    "repo_instance_name": "my-api",
                    "repo_class_name": "hmd-ms-myapi",
                    "repo_class_version": "0.0.1",
                }
            )
        self.assertEqual(entry["repo_class_version"], "2.3.4")

    def test_local_working_tree_wins_with_the_per_repo_override(self):
        with mock.patch.dict(
            os.environ,
            {b._local_version_env_var("hmd-ms-myapi"): "local"},
            clear=False,
        ):
            entry = csb.normalize_entry(
                {
                    "repo_instance_name": "my-api",
                    "repo_class_name": "hmd-ms-myapi",
                    "repo_class_version": "0.0.1",
                }
            )
        self.assertEqual(entry["repo_class_version"], "2.3.4")

    def test_declared_version_is_the_fallback(self):
        entry = csb.normalize_entry(
            {
                "repo_instance_name": "other",
                "repo_class_name": "hmd-ms-not-checked-out",
                "repo_class_version": "9.9.9",
            }
        )
        self.assertEqual(entry["repo_class_version"], "9.9.9")

    def test_source_path_repo_resolves_its_own_version(self):
        elsewhere = Path(self._tmp.name) / "elsewhere"
        repo = self._make_repo("hmd-ms-myapi", "7.0.0", root=elsewhere)
        entry = csb.normalize_entry(
            {"repo_instance_name": "my-api", "repo_class_name": "hmd-ms-myapi"},
            repo_path=str(repo),
        )
        # With no bundled artifact and no declared version the working tree is
        # the last resort before "0.1.0" -- and `repo_path` picks which tree,
        # so it is 7.0.0, not 2.3.4 from HMD_REPO_HOME.
        self.assertEqual(entry["repo_class_version"], "7.0.0")

    def test_source_path_does_not_override_a_declared_version(self):
        elsewhere = Path(self._tmp.name) / "elsewhere"
        repo = self._make_repo("hmd-ms-myapi", "7.0.0", root=elsewhere)
        entry = csb.normalize_entry(
            {
                "repo_instance_name": "my-api",
                "repo_class_name": "hmd-ms-myapi",
                "repo_class_version": "9.9.9",
            },
            repo_path=str(repo),
        )
        # `source.path` says where the tree is, not that the tree wins.
        self.assertEqual(entry["repo_class_version"], "9.9.9")


class PrecedenceTests(_BuilderTest):
    def test_declared_repo_overrides_a_builtin_of_the_same_name(self):
        definition = csb.build_definition(
            env=_Env(),
            manifest=self._manifest(
                plugins=[],
                repos=[
                    {
                        "instance_name": "project-bucket",
                        "repo_class_name": "hmd-ms-myapi",
                    }
                ],
            ),
        )
        entries = [e for e in definition if e["repo_instance_name"] == "project-bucket"]
        self.assertEqual(len(entries), 1)
        self.assertEqual(entries[0]["repo_class_name"], "hmd-ms-myapi")

    def test_builtins_the_manifest_omits_are_still_included(self):
        definition = csb.build_definition(
            env=_Env(), manifest=self._manifest(plugins=[], repos=[])
        )
        names = {e["repo_instance_name"] for e in definition}
        self.assertIn("project-bucket", names)

    def test_deployment_id_is_stamped_from_the_environment(self):
        definition = csb.build_definition(
            env=_Env("dev2"), manifest=self._manifest(plugins=[])
        )
        self.assertTrue(all(e["deployment_id"] == "dev2" for e in definition))

    def test_dependencies_are_topologically_ordered(self):
        manifest = self._manifest(
            plugins=[],
            repos=[
                {
                    "instance_name": "consumer",
                    "repo_class_name": "hmd-ms-myapi",
                    "dependencies": {"upstream": "producer"},
                },
                {"instance_name": "producer", "repo_class_name": "hmd-ms-myapi"},
            ],
        )
        names = [
            e["repo_instance_name"]
            for e in csb.build_definition(env=_Env(), manifest=manifest)
        ]
        self.assertLess(names.index("producer"), names.index("consumer"))

    def test_core_instance_only_appears_in_full_definition(self):
        manifest = self._manifest(plugins=[])
        phase_b = csb.build_definition(env=_Env(), manifest=manifest)
        full = csb.full_definition(env=_Env(), manifest=manifest)
        self.assertNotIn(
            b.CORE_INSTANCE_NAME, {e["repo_instance_name"] for e in phase_b}
        )
        self.assertIn(b.CORE_INSTANCE_NAME, {e["repo_instance_name"] for e in full})


class DeploymentGuiTests(_BuilderTest):
    """The GUI must reach the manifest-driven path too.

    ``build_definition`` also produces the snapshot ``env_reconcile`` diffs against,
    so a GUI present in ``resolve_plugin_bom`` but absent here would read as drift
    and every reconcile would propose destroying an instance the other path keeps
    re-creating.
    """

    def test_gui_is_included_by_default(self):
        definition = csb.build_definition(
            env=_Env(), manifest=self._manifest(plugins=[], repos=[])
        )
        names = {e["repo_instance_name"] for e in definition}
        self.assertIn(b.GUI_INSTANCE_NAME, names)
        self.assertIn(b.GUI_DB_INSTANCE_NAME, names)

    def test_gui_can_be_opted_out(self):
        os.environ["HMD_LOCAL_NEURONSPHERE_ENABLE_GUI"] = "false"
        definition = csb.build_definition(
            env=_Env(), manifest=self._manifest(plugins=[], repos=[])
        )
        names = {e["repo_instance_name"] for e in definition}
        self.assertNotIn(b.GUI_INSTANCE_NAME, names)
        self.assertNotIn(b.GUI_DB_INSTANCE_NAME, names)

    def test_a_declared_repo_overrides_the_builtin_gui_entry(self):
        """Keep-first de-dup: an explicit manifest entry wins on instance name."""
        definition = csb.build_definition(
            env=_Env(),
            manifest=self._manifest(
                plugins=[],
                repos=[
                    {
                        "instance_name": b.GUI_INSTANCE_NAME,
                        "repo_class_name": "hmd-app-neuronsphere",
                        "version": "0.1.70",
                        "instance_configuration": {"replicaCount": 3},
                    }
                ],
            ),
        )
        entry = next(
            e for e in definition if e["repo_instance_name"] == b.GUI_INSTANCE_NAME
        )
        self.assertEqual(entry["instance_configuration"], {"replicaCount": 3})

    def test_gui_csrf_origins_track_the_environment_port(self):
        env = _Env()
        env.spare_port = 19011
        definition = csb.build_definition(
            env=env, manifest=self._manifest(plugins=[], repos=[])
        )
        entry = next(
            e for e in definition if e["repo_instance_name"] == b.GUI_INSTANCE_NAME
        )
        origins = entry["instance_configuration"]["config"]["extraCsrfOrigins"]
        self.assertIn("http://localhost:19011", origins)


class PluginEnablementTests(_BuilderTest):
    """The allow-list is what makes an environment's contents declared."""

    PATCH_PLUGINS = False

    class _EP:
        def __init__(self, name, entries, accepts_config=False):
            self.name = name
            self._entries = entries
            self._accepts_config = accepts_config
            self.called_with = None

        def load(self):
            def contributor(**kwargs):
                if kwargs and not self._accepts_config:
                    raise TypeError("unexpected keyword argument")
                self.called_with = kwargs
                return self._entries

            return contributor

    def _entry(self, name):
        return {
            "repo_instance_name": name,
            "repo_class_name": "hmd-ms-myapi",
            "instance_configuration": {},
            "dependencies": {},
        }

    def _with_eps(self, *eps):
        return mock.patch.object(b, "entry_points", return_value=list(eps))

    def test_only_enabled_plugins_contribute(self):
        a = self._EP("ns-a", [self._entry("from-a")])
        c = self._EP("ns-c", [self._entry("from-c")])
        with self._with_eps(a, c):
            entries = b._collect_plugin_bom_entries(enabled={"ns-a"})
        self.assertEqual([e["repo_instance_name"] for e in entries], ["from-a"])

    def test_no_allow_list_accepts_every_installed_plugin(self):
        a = self._EP("ns-a", [self._entry("from-a")])
        c = self._EP("ns-c", [self._entry("from-c")])
        with self._with_eps(a, c):
            entries = b._collect_plugin_bom_entries(enabled=None)
        self.assertEqual(
            sorted(e["repo_instance_name"] for e in entries), ["from-a", "from-c"]
        )

    def test_empty_allow_list_accepts_none(self):
        a = self._EP("ns-a", [self._entry("from-a")])
        with self._with_eps(a):
            self.assertEqual(b._collect_plugin_bom_entries(enabled=set()), [])

    def test_enabling_an_uninstalled_plugin_is_an_error(self):
        a = self._EP("ns-a", [self._entry("from-a")])
        with self._with_eps(a):
            with self.assertRaises(ValueError) as ctx:
                b._collect_plugin_bom_entries(enabled={"ns-a", "ns-typo"})
        self.assertIn("ns-typo", str(ctx.exception))

    def test_config_is_passed_to_a_contributor_that_accepts_it(self):
        a = self._EP("ns-a", [self._entry("from-a")], accepts_config=True)
        with self._with_eps(a):
            b._collect_plugin_bom_entries(
                enabled={"ns-a"}, config={"ns-a": {"profile": "minimal"}}
            )
        self.assertEqual(a.called_with, {"profile": "minimal"})

    def test_a_zero_arg_contributor_still_works_with_config_present(self):
        a = self._EP("ns-a", [self._entry("from-a")], accepts_config=False)
        with self._with_eps(a):
            entries = b._collect_plugin_bom_entries(
                enabled={"ns-a"}, config={"ns-a": {"profile": "minimal"}}
            )
        self.assertEqual([e["repo_instance_name"] for e in entries], ["from-a"])

    def test_a_broken_plugin_does_not_block_the_others(self):
        class _Broken:
            name = "ns-broken"

            def load(self):
                raise ImportError("boom")

        good = self._EP("ns-good", [self._entry("from-good")])
        with self._with_eps(_Broken(), good):
            entries = b._collect_plugin_bom_entries(enabled=None)
        self.assertEqual([e["repo_instance_name"] for e in entries], ["from-good"])


class HashTests(_BuilderTest):
    def test_configuration_change_changes_the_digest(self):
        base = csb.normalize_entry(
            {"repo_instance_name": "x", "repo_class_name": "hmd-ms-myapi"}
        )
        changed = csb.normalize_entry(
            {
                "repo_instance_name": "x",
                "repo_class_name": "hmd-ms-myapi",
                "instance_configuration": {"replicas": 2},
            }
        )
        self.assertNotEqual(csb.entry_hash(base), csb.entry_hash(changed))

    def test_deployment_id_is_not_part_of_the_digest(self):
        a = csb.normalize_entry(
            {
                "repo_instance_name": "x",
                "repo_class_name": "hmd-ms-myapi",
                "deployment_id": "local",
            }
        )
        c = csb.normalize_entry(
            {
                "repo_instance_name": "x",
                "repo_class_name": "hmd-ms-myapi",
                "deployment_id": "dev2",
            }
        )
        self.assertEqual(csb.entry_hash(a), csb.entry_hash(c))

    def test_definition_hash_is_order_independent(self):
        one = csb.normalize_entry(
            {"repo_instance_name": "a", "repo_class_name": "hmd-ms-myapi"}
        )
        two = csb.normalize_entry(
            {"repo_instance_name": "b", "repo_class_name": "hmd-ms-myapi"}
        )
        self.assertEqual(
            csb.definition_hash([one, two]), csb.definition_hash([two, one])
        )


class DeclaredRepoPathTests(_BuilderTest):
    def test_only_non_default_paths_are_reported(self):
        elsewhere = Path(self._tmp.name) / "elsewhere"
        self._make_repo("hmd-ms-other", "1.0.0", root=elsewhere)
        manifest = self._manifest(
            plugins=[],
            repos=[
                # Under HMD_REPO_HOME: the runner finds this one unaided.
                {"instance_name": "my-api", "repo_class_name": "hmd-ms-myapi"},
                {
                    "instance_name": "other",
                    "repo_class_name": "hmd-ms-other",
                    "source": {
                        "type": "local",
                        "path": str(elsewhere / "hmd-ms-other"),
                    },
                },
            ],
        )
        paths = csb.declared_repo_paths(manifest)
        self.assertEqual(paths, {"hmd-ms-other": str(elsewhere / "hmd-ms-other")})

    def test_no_manifest_means_no_overrides(self):
        self.assertEqual(csb.declared_repo_paths(None), {})


if __name__ == "__main__":
    unittest.main()
