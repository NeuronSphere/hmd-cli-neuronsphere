from contextlib import contextmanager

import pytest

from hmd_ms_deployment.version import (
    Compatible,
    VersionSpecifierException,
    VersionSpecifier,
    sort_versions,
)


@contextmanager
def does_not_raise():
    yield

@pytest.mark.parametrize(
    "spec,version,expectation",
    [
        ("~= 1.2","1.1.1", pytest.raises(VersionSpecifierException)),
        ("~= 1.2","2.1.1", pytest.raises(VersionSpecifierException)),
        ("~= 1.2.3","1.2.1", pytest.raises(VersionSpecifierException)),
        ("~= 1.2","1.2.1", does_not_raise()),
        ("~= 1.2.3","1.2.5", does_not_raise()),

        ("== 1.2.3","1.2.3", does_not_raise()),
        ("== 1.2","1.2.3", pytest.raises(VersionSpecifierException)),
        ("== 1.2.*","1.2.5", does_not_raise()),
        ("== 1.2.*","1.2.1", does_not_raise()),
        ("== 1.*","1.2.1", does_not_raise()),
        ("== 1.2.*","2.2.1", pytest.raises(VersionSpecifierException)),
        ("== 1.2.*","1.1.1", pytest.raises(VersionSpecifierException)),

        ("!= 1.2","1.2.3", does_not_raise()),
        ("!= 1.2.3","1.2.3", pytest.raises(VersionSpecifierException)),
        ("!= 1.2.*","1.2.5", pytest.raises(VersionSpecifierException)),
        ("!= 1.2.*","1.2.1", pytest.raises(VersionSpecifierException)),
        ("!= 1.*","1.2.1", pytest.raises(VersionSpecifierException)),
        ("!= 1.2.*","2.2.1", does_not_raise()),
        ("!= 1.2.*","1.1.1", does_not_raise()),

        ("> 1.2.3","1.1.1", pytest.raises(VersionSpecifierException)),
        ("> 1.2.3","1.2.3", pytest.raises(VersionSpecifierException)),
        (">= 1.2.3","1.2.3", does_not_raise()),
        (">= 1.2.0","1.2.3", does_not_raise()),
        (">1.2.3","1.2.4", does_not_raise()),

        ("< 1.2.3","1.1.1", does_not_raise()),
        ("< 1.2.3","1.2.3", pytest.raises(VersionSpecifierException)),
        ("<= 1.2.3","1.2.3", does_not_raise()),
        ("<= 1.2.0","1.2.3", pytest.raises(VersionSpecifierException)),
        ("<1.2.3","1.2.4", pytest.raises(VersionSpecifierException)),

        ("== 1.2.*,>=1.2.4", "1.1.1", pytest.raises(VersionSpecifierException)),
        ("== 1.2.*,>=1.2.4", "1.2.4", does_not_raise()),
        ("~= 1.2.4", "1.2.4", does_not_raise()),
        ("~= 1.2.4", "1.2.4", does_not_raise()),

    ]
)
def test_version_validation(spec, version, expectation):
    vs = VersionSpecifier(spec)
    with expectation:
        vs.validate(version)

@pytest.mark.parametrize(
    "spec,expectation",
    [
        (">= 1.2", pytest.raises(AssertionError)),
        ("> 1.2", pytest.raises(AssertionError)),
        ("<= 1.2", pytest.raises(AssertionError)),
        ("> 1.2", pytest.raises(AssertionError)),
        ("1.2", pytest.raises(AssertionError)),

    ]
)
def test_specs(spec, expectation):
    with expectation:
        vs = VersionSpecifier(spec)


def _versions(*vs):
    return [{"version": v} for v in vs]


def _keys(result):
    return [r["version"] if r is not None else None for r in result]


def test_sort_versions_sorts_numeric_descending():
    result = sort_versions(_versions("1.0.0", "2.1.3", "1.2.0"), lambda v: v["version"])
    assert _keys(result) == ["2.1.3", "1.2.0", "1.0.0"]


def test_sort_versions_appends_non_numeric_at_end():
    result = sort_versions(
        _versions("1.0.0", "0.1", "2.0.0", "1.0.0-rc1"),
        lambda v: v["version"],
    )
    sortable, unsortable = _keys(result)[:2], sorted(_keys(result)[2:])
    assert sortable == ["2.0.0", "1.0.0"]
    assert unsortable == ["0.1", "1.0.0-rc1"]


def test_sort_versions_skips_none_entries():
    result = sort_versions(
        [{"version": "1.0.0"}, None, {"version": "2.0.0"}],
        lambda v: v["version"],
    )
    assert _keys(result) == ["2.0.0", "1.0.0"]


def test_sort_versions_empty_list():
    assert sort_versions([], lambda v: v["version"]) == []


def test_sort_versions_all_unsortable():
    result = sort_versions(
        _versions("foo", "bar"),
        lambda v: v["version"],
    )
    assert sorted(_keys(result)) == ["bar", "foo"]
