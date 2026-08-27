"""Resolving a repo class's deployed version, artifact-first.

``resolve_repo_version`` decides every ``repo_class_version`` in the local
deployment graph: the registered RepoClassVersion, the changeset definition,
the reconcile-drift key, and the ``<repo>_<ver>_build.zip`` lookup. The rule is
that a *published* artifact wins over a working tree, because a published
version is reproducible and resolvable while a tree is a work in progress --
and that a developer can always ask for their tree back, per repo or globally.

Both halves are pinned here: the precedence tiers, and the two override forms.

Every test patches ``_artifact_roots``. Without it this repo's own
``external/`` -- populated by ``pre_build_artifacts`` before the test run, so
non-empty during a build and empty in a fresh checkout -- would make these
assertions depend on how the tree was prepared.

Run directly: ``python -m pytest src/python/tests/test_repo_version.py``
"""

import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from hmd_cli_neuronsphere import bom_seeder as b


class _WarningAssertion:
    """Context manager: `bom_seeder.logger.warning` fired, naming each fragment."""

    def __init__(self, test, fragments):
        self._test = test
        self._fragments = fragments
        self._patch = mock.patch.object(b.logger, "warning")

    def __enter__(self):
        self._mock = self._patch.start()
        return self._mock

    def __exit__(self, exc_type, exc, tb):
        self._patch.stop()
        if exc_type is not None:
            return False
        self._test.assertTrue(
            self._mock.called, "expected a warning, but none was logged"
        )
        emitted = "\n".join(str(call.args[0]) for call in self._mock.call_args_list)
        for fragment in self._fragments:
            self._test.assertIn(fragment, emitted)
        return False


class _VersionTest(unittest.TestCase):
    """An isolated HMD_REPO_HOME plus a fake bundled-artifact root.

    Warnings are asserted against a mocked ``bom_seeder.logger`` rather than
    ``assertLogs``: cement's ``minimal_logger`` is not a stdlib logger, so
    whether its records reach the root handler depends on cement's own config.
    """

    #: Subclasses exercising `_artifact_roots` itself set this False.
    PATCH_ROOTS = True

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.repo_home = self.root / "repos"
        self.artifacts = self.root / "external"
        self.artifacts.mkdir(parents=True)

        self._env = mock.patch.dict(
            os.environ, {"HMD_REPO_HOME": str(self.repo_home)}, clear=False
        )
        self._env.start()
        for var in (
            b.PREFER_LOCAL_VERSIONS_ENV,
            b.ARTIFACT_ROOTS_ENV,
            b._local_version_env_var("hmd-ms-myapi"),
        ):
            os.environ.pop(var, None)

        self._roots = None
        if self.PATCH_ROOTS:
            self._roots = mock.patch.object(
                b, "_artifact_roots", return_value=[str(self.artifacts)]
            )
            self._roots.start()
        b._reset_artifact_version_index()

    def tearDown(self):
        if self._roots is not None:
            self._roots.stop()
        self._env.stop()
        b._reset_artifact_version_index()
        self._tmp.cleanup()

    def assertWarned(self, *fragments):
        """Assert `bom_seeder.logger.warning` fired, mentioning each fragment."""
        return _WarningAssertion(self, fragments)

    def _make_tree(self, repo_class_name, version, root=None):
        """A working tree with a meta-data/VERSION."""
        meta = Path(root or self.repo_home) / repo_class_name / "meta-data"
        meta.mkdir(parents=True, exist_ok=True)
        (meta / "VERSION").write_text(f"{version}\n")
        return meta.parent

    def _make_artifact(self, dir_name, version, manifest_name=None, root=None):
        """A bundled artifact: the unpacked build output of some repo."""
        meta = Path(root or self.artifacts) / dir_name / "meta-data"
        meta.mkdir(parents=True, exist_ok=True)
        (meta / "VERSION").write_text(f"{version}\n")
        if manifest_name is not None:
            (meta / "manifest.json").write_text(json.dumps({"name": manifest_name}))
        return meta.parent


class PrecedenceTests(_VersionTest):
    def test_bundled_artifact_beats_declared_and_local(self):
        self._make_tree("hmd-ms-myapi", "2.3.4")
        self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        resolution = b.resolve_repo_version("hmd-ms-myapi", bom_version="0.0.1")
        self.assertEqual(resolution.version, "1.0.0")
        self.assertEqual(resolution.source, "bundled")

    def test_declared_beats_local_when_there_is_no_bundle(self):
        self._make_tree("hmd-ms-myapi", "2.3.4")
        resolution = b.resolve_repo_version("hmd-ms-myapi", bom_version="0.0.1")
        self.assertEqual(resolution.version, "0.0.1")
        self.assertEqual(resolution.source, "declared")

    def test_local_tree_is_the_last_resort_before_the_sentinel(self):
        # No bundle, no declared version: "0.1.0" would be a worse label than
        # the tree's own number, so the tree is used -- loudly.
        self._make_tree("hmd-ms-myapi", "2.3.4")
        with self.assertWarned("no bundled artifact and no declared version"):
            resolution = b.resolve_repo_version("hmd-ms-myapi")
        self.assertEqual(resolution.version, "2.3.4")
        self.assertEqual(resolution.source, "local-fallback")

    def test_sentinel_when_nothing_resolves(self):
        with self.assertWarned("using 0.1.0"):
            resolution = b.resolve_repo_version("hmd-ms-nowhere")
        self.assertEqual(resolution.version, "0.1.0")
        self.assertEqual(resolution.source, "default")

    def test_repo_path_selects_which_tree_the_fallback_reads(self):
        self._make_tree("hmd-ms-myapi", "2.3.4")
        elsewhere = self._make_tree("hmd-ms-myapi", "7.0.0", root=self.root / "other")
        resolution = b.resolve_repo_version("hmd-ms-myapi", repo_path=str(elsewhere))
        self.assertEqual(resolution.version, "7.0.0")

    def test_a_shadowed_working_tree_is_warned_about(self):
        self._make_tree("hmd-ms-myapi", "2.3.4")
        self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        with self.assertWarned("2.3.4", b._local_version_env_var("hmd-ms-myapi")):
            b.resolve_repo_version("hmd-ms-myapi")

    def test_a_matching_working_tree_is_not_warned_about(self):
        self._make_tree("hmd-ms-myapi", "1.0.0")
        self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        with mock.patch.object(b.logger, "warning") as warn:
            b.resolve_repo_version("hmd-ms-myapi")
        warn.assert_not_called()


class OverrideTests(_VersionTest):
    def test_env_var_name_is_derived_from_the_repo_class(self):
        self.assertEqual(
            b._local_version_env_var("hmd-inf-ext-secrets"),
            "HMD_LOCAL_VERSION_HMD_INF_EXT_SECRETS",
        )

    def test_global_override_makes_the_local_tree_win(self):
        self._make_tree("hmd-ms-myapi", "2.3.4")
        self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        with mock.patch.dict(
            os.environ, {b.PREFER_LOCAL_VERSIONS_ENV: "true"}, clear=False
        ):
            resolution = b.resolve_repo_version("hmd-ms-myapi")
        self.assertEqual(resolution.version, "2.3.4")
        self.assertEqual(resolution.source, "local")

    def test_global_override_without_a_tree_falls_through_to_the_bundle(self):
        self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        with mock.patch.dict(
            os.environ, {b.PREFER_LOCAL_VERSIONS_ENV: "true"}, clear=False
        ):
            with self.assertWarned("local version override is set"):
                resolution = b.resolve_repo_version("hmd-ms-myapi")
        self.assertEqual(resolution.version, "1.0.0")
        self.assertEqual(resolution.source, "bundled")

    def test_per_repo_override_makes_the_local_tree_win(self):
        self._make_tree("hmd-ms-myapi", "2.3.4")
        self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        with mock.patch.dict(
            os.environ,
            {b._local_version_env_var("hmd-ms-myapi"): "local"},
            clear=False,
        ):
            resolution = b.resolve_repo_version("hmd-ms-myapi")
        self.assertEqual(resolution.version, "2.3.4")

    def test_per_repo_override_only_affects_its_own_repo(self):
        self._make_tree("hmd-ms-other", "5.5.5")
        self._make_artifact("other", "1.0.0", manifest_name="hmd-ms-other")
        with mock.patch.dict(
            os.environ,
            {b._local_version_env_var("hmd-ms-myapi"): "local"},
            clear=False,
        ):
            resolution = b.resolve_repo_version("hmd-ms-other")
        self.assertEqual(resolution.version, "1.0.0")

    def test_a_literal_pin_wins_over_everything(self):
        self._make_tree("hmd-ms-myapi", "2.3.4")
        self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        with mock.patch.dict(
            os.environ,
            {
                b._local_version_env_var("hmd-ms-myapi"): "9.9.9",
                b.PREFER_LOCAL_VERSIONS_ENV: "true",
            },
            clear=False,
        ):
            resolution = b.resolve_repo_version("hmd-ms-myapi", bom_version="0.0.1")
        self.assertEqual(resolution.version, "9.9.9")
        self.assertEqual(resolution.source, "pin")

    def test_per_repo_falsy_opts_out_of_a_global_prefer_local(self):
        # "prefer local everywhere except this one repo" -- otherwise
        # unexpressible, since the per-repo var doubles as a version pin.
        self._make_tree("hmd-ms-myapi", "2.3.4")
        self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        for value in ("false", "bundled", "artifact"):
            with self.subTest(value=value):
                with mock.patch.dict(
                    os.environ,
                    {
                        b._local_version_env_var("hmd-ms-myapi"): value,
                        b.PREFER_LOCAL_VERSIONS_ENV: "true",
                    },
                    clear=False,
                ):
                    resolution = b.resolve_repo_version("hmd-ms-myapi")
                self.assertEqual(resolution.version, "1.0.0")


class ArtifactIndexTests(_VersionTest):
    def test_the_key_is_the_manifest_name_not_the_directory_name(self):
        # The dirs are named after the plugin (`ext-secrets`), the repo classes
        # are not (`hmd-inf-ext-secrets`).
        self._make_artifact(
            "ext-secrets", "0.2.50", manifest_name="hmd-inf-ext-secrets"
        )
        index = b._artifact_version_index()
        self.assertIn("hmd-inf-ext-secrets", index)
        self.assertNotIn("ext-secrets", index)
        self.assertEqual(index["hmd-inf-ext-secrets"][0], "0.2.50")

    def test_a_dir_without_a_manifest_is_indexed_by_its_directory_name(self):
        self._make_artifact("hmd-inf-trino", "0.1.202")
        self.assertIn("hmd-inf-trino", b._artifact_version_index())

    def test_the_resolution_root_points_at_the_artifact(self):
        artifact = self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        resolution = b.resolve_repo_version("hmd-ms-myapi")
        self.assertEqual(Path(resolution.root), artifact)

    def test_a_missing_root_is_not_an_error(self):
        # The state of any source checkout: external/ is only populated by a build.
        with mock.patch.object(
            b, "_artifact_roots", return_value=[str(self.root / "nope")]
        ):
            b._reset_artifact_version_index()
            self.assertEqual(b._artifact_version_index(), {})

    def test_a_blank_version_file_is_skipped(self):
        meta = self.artifacts / "myapi" / "meta-data"
        meta.mkdir(parents=True)
        (meta / "VERSION").write_text("   \n")
        (meta / "manifest.json").write_text(json.dumps({"name": "hmd-ms-myapi"}))
        self.assertEqual(b._artifact_version_index(), {})

    def test_a_dir_without_meta_data_is_skipped(self):
        (self.artifacts / "junk").mkdir()
        self.assertEqual(b._artifact_version_index(), {})

    def test_unreadable_manifest_falls_back_to_the_directory_name(self):
        meta = self.artifacts / "hmd-ms-myapi" / "meta-data"
        meta.mkdir(parents=True)
        (meta / "VERSION").write_text("1.0.0\n")
        (meta / "manifest.json").write_text("{not json")
        self.assertIn("hmd-ms-myapi", b._artifact_version_index())

    def _duplicate_across_two_roots(self, first_version, second_version):
        """Index the same repo class bundled at two versions in two roots."""
        second = self.root / "external2"
        second.mkdir(exist_ok=True)
        self._make_artifact("myapi", first_version, manifest_name="hmd-ms-myapi")
        self._make_artifact(
            "myapi", second_version, manifest_name="hmd-ms-myapi", root=second
        )
        return second, mock.patch.object(
            b, "_artifact_roots", return_value=[str(self.artifacts), str(second)]
        )

    def test_the_highest_version_wins_a_duplicate_and_warns(self):
        # Ownership of a repo class moves between packages. Whichever root a
        # stale copy sits in, it must not shadow a newer one: its manifest's
        # dependencies would be registered against a version nobody built.
        second, roots = self._duplicate_across_two_roots("1.0.0", "2.0.0")
        with roots:
            b._reset_artifact_version_index()
            with self.assertWarned("found twice", "2.0.0", "1.0.0"):
                index = b._artifact_version_index()
        self.assertEqual(index["hmd-ms-myapi"], ("2.0.0", str(second / "myapi")))

    def test_the_highest_version_wins_from_the_first_root_too(self):
        # The assertion is about the version, not the root order.
        _, roots = self._duplicate_across_two_roots("2.0.0", "1.0.0")
        with roots:
            b._reset_artifact_version_index()
            with self.assertWarned("found twice"):
                index = b._artifact_version_index()
        self.assertEqual(
            index["hmd-ms-myapi"], ("2.0.0", str(self.artifacts / "myapi"))
        )

    def test_build_numbers_compare_numerically_not_lexically(self):
        # "0.2.9" > "0.2.70" as strings; these are build numbers, not text.
        second, roots = self._duplicate_across_two_roots("0.2.9", "0.2.70")
        with roots:
            b._reset_artifact_version_index()
            index = b._artifact_version_index()
        self.assertEqual(index["hmd-ms-myapi"], ("0.2.70", str(second / "myapi")))

    def test_the_same_version_in_two_roots_is_not_a_conflict(self):
        _, roots = self._duplicate_across_two_roots("1.0.0", "1.0.0")
        with roots:
            b._reset_artifact_version_index()
            with mock.patch.object(b.logger, "warning") as warned:
                index = b._artifact_version_index()
        self.assertFalse(warned.called)
        self.assertEqual(
            index["hmd-ms-myapi"], ("1.0.0", str(self.artifacts / "myapi"))
        )

    def test_a_non_numeric_component_does_not_raise(self):
        # A version nobody can order is not a reason to fail `up`; it just
        # loses to any numbered one.
        _, roots = self._duplicate_across_two_roots("1.0.0", "1.0.0rc1")
        with roots:
            b._reset_artifact_version_index()
            with self.assertWarned("found twice"):
                index = b._artifact_version_index()
        self.assertEqual(index["hmd-ms-myapi"][0], "1.0.0")

    def test_the_index_is_cached_until_reset(self):
        self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        b._artifact_version_index()
        self._make_artifact("other", "2.0.0", manifest_name="hmd-ms-other")
        self.assertNotIn("hmd-ms-other", b._artifact_version_index())
        b._reset_artifact_version_index()
        self.assertIn("hmd-ms-other", b._artifact_version_index())


class ArtifactRootTests(_VersionTest):
    """`_artifact_roots` itself -- the one thing the other tests patch out."""

    PATCH_ROOTS = False

    def test_this_package_is_always_the_first_root(self):
        own = os.path.join(os.path.dirname(b.__file__), "external")
        self.assertEqual(
            os.path.realpath(b._artifact_roots()[0]), os.path.realpath(own)
        )

    def test_the_env_var_appends_extra_roots(self):
        extra = [str(self.root / "a"), str(self.root / "bee")]
        with mock.patch.dict(
            os.environ, {b.ARTIFACT_ROOTS_ENV: os.pathsep.join(extra)}, clear=False
        ):
            roots = b._artifact_roots()
        self.assertEqual(roots[-2:], extra)

    def test_a_plugin_packages_external_dir_is_discovered(self):
        ep = mock.Mock()
        ep.value = "hmd_cli_plugin_ns_fake.plugin:enabled"
        spec = mock.Mock()
        spec.submodule_search_locations = ["/somewhere/hmd_cli_plugin_ns_fake"]
        with mock.patch.object(b, "entry_points", return_value=[ep]):
            with mock.patch.object(b.importlib.util, "find_spec", return_value=spec):
                roots = b._artifact_roots()
        self.assertIn(
            os.path.join("/somewhere/hmd_cli_plugin_ns_fake", "external"), roots
        )

    def test_a_package_that_cannot_be_located_is_skipped(self):
        ep = mock.Mock()
        ep.value = "hmd_cli_plugin_ns_broken.plugin:enabled"
        with mock.patch.object(b, "entry_points", return_value=[ep]):
            with mock.patch.object(
                b.importlib.util, "find_spec", side_effect=ImportError("boom")
            ):
                roots = b._artifact_roots()  # must not raise
        self.assertTrue(all("broken" not in r for r in roots))


class CoreRepoTests(_VersionTest):
    """The CLI's own repo class has no `external/` entry -- it *is* the package."""

    def test_the_installed_distribution_version_is_used(self):
        self._make_tree(b.CORE_REPO_CLASS, "2.3.4")
        with mock.patch.object(b, "_core_distribution_version", return_value="1.2.3"):
            resolution = b.resolve_repo_version(b.CORE_REPO_CLASS)
        self.assertEqual(resolution.version, "1.2.3")
        self.assertEqual(resolution.source, "bundled")

    def test_an_uninstalled_distribution_falls_through(self):
        self._make_tree(b.CORE_REPO_CLASS, "2.3.4")
        with mock.patch.object(b, "_core_distribution_version", return_value=None):
            with self.assertWarned("no bundled artifact and no declared version"):
                resolution = b.resolve_repo_version(b.CORE_REPO_CLASS)
        self.assertEqual(resolution.version, "2.3.4")

    def test_an_override_still_wins_for_the_core_repo(self):
        self._make_tree(b.CORE_REPO_CLASS, "2.3.4")
        with mock.patch.object(b, "_core_distribution_version", return_value="1.2.3"):
            with mock.patch.dict(
                os.environ, {b.PREFER_LOCAL_VERSIONS_ENV: "true"}, clear=False
            ):
                resolution = b.resolve_repo_version(b.CORE_REPO_CLASS)
        self.assertEqual(resolution.version, "2.3.4")


class RepoRootCandidateTests(_VersionTest):
    """The code deployed follows the same rule as the version registered."""

    def test_the_bundled_artifact_comes_first(self):
        tree = self._make_tree("hmd-ms-myapi", "2.3.4")
        bundle = self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        self.assertEqual(
            b.repo_root_candidates("hmd-ms-myapi"), [str(bundle), str(tree)]
        )

    def test_an_override_puts_the_working_tree_first(self):
        tree = self._make_tree("hmd-ms-myapi", "2.3.4")
        bundle = self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        with mock.patch.dict(
            os.environ, {b.PREFER_LOCAL_VERSIONS_ENV: "true"}, clear=False
        ):
            self.assertEqual(
                b.repo_root_candidates("hmd-ms-myapi"), [str(tree), str(bundle)]
            )

    def test_a_version_pin_does_not_move_the_working_tree_up(self):
        # A pin says which version to register, not where the code lives.
        tree = self._make_tree("hmd-ms-myapi", "2.3.4")
        bundle = self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        with mock.patch.dict(
            os.environ,
            {b._local_version_env_var("hmd-ms-myapi"): "9.9.9"},
            clear=False,
        ):
            self.assertEqual(
                b.repo_root_candidates("hmd-ms-myapi"), [str(bundle), str(tree)]
            )

    def test_only_directories_that_exist_are_offered(self):
        bundle = self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        self.assertEqual(b.repo_root_candidates("hmd-ms-myapi"), [str(bundle)])

    def test_a_repo_with_no_artifact_falls_back_to_its_tree(self):
        tree = self._make_tree("hmd-ms-myapi", "2.3.4")
        self.assertEqual(b.repo_root_candidates("hmd-ms-myapi"), [str(tree)])

    def test_nothing_to_deploy_from_is_an_empty_list(self):
        self.assertEqual(b.repo_root_candidates("hmd-ms-nowhere"), [])

    def test_repo_path_selects_which_tree_is_offered(self):
        self._make_tree("hmd-ms-myapi", "2.3.4")
        elsewhere = self._make_tree("hmd-ms-myapi", "7.0.0", root=self.root / "other")
        self.assertEqual(
            b.repo_root_candidates("hmd-ms-myapi", repo_path=str(elsewhere)),
            [str(elsewhere)],
        )


class MetadataFollowsTheVersionTests(_VersionTest):
    """A registered RepoClassVersion must describe one build, not two."""

    def test_manifest_is_read_from_the_artifact_the_version_came_from(self):
        artifact = self._make_artifact("myapi", "1.0.0", manifest_name="hmd-ms-myapi")
        (artifact / "meta-data" / "manifest.json").write_text(
            json.dumps(
                {
                    "name": "hmd-ms-myapi",
                    "deploy": {
                        "dependencies": {"eks-cluster": "local-neuronsphere"},
                        "default_configuration": {"replicas": 1},
                    },
                }
            )
        )
        tree = self._make_tree("hmd-ms-myapi", "2.3.4")
        (tree / "meta-data" / "manifest.json").write_text(
            json.dumps(
                {
                    "name": "hmd-ms-myapi",
                    "deploy": {
                        "dependencies": {"eks-cluster": "in-progress"},
                        "default_configuration": {"replicas": 99},
                    },
                }
            )
        )
        resolution = b.resolve_repo_version("hmd-ms-myapi")
        self.assertEqual(resolution.source, "bundled")
        self.assertEqual(
            b._get_repo_dependencies("hmd-ms-myapi", None, resolution.root),
            {"eks-cluster": "local-neuronsphere"},
        )
        self.assertEqual(
            b._get_repo_deploy_config("hmd-ms-myapi", None, resolution.root),
            {"replicas": 1},
        )

    def test_the_working_tree_manifest_is_used_when_the_version_came_from_it(self):
        tree = self._make_tree("hmd-ms-myapi", "2.3.4")
        (tree / "meta-data" / "manifest.json").write_text(
            json.dumps({"deploy": {"dependencies": {"eks-cluster": "local"}}})
        )
        resolution = b.resolve_repo_version("hmd-ms-myapi")
        self.assertNotEqual(resolution.source, "bundled")
        self.assertEqual(
            b._get_repo_dependencies("hmd-ms-myapi", None, None),
            {"eks-cluster": "local"},
        )


if __name__ == "__main__":
    unittest.main()
