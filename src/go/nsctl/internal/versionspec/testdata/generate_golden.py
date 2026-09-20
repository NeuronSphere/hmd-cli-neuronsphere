"""Generate golden.json: the contract internal/versionspec is held to.

NERD011 SPEC002 says the satisfaction test shall be ported from
hmd_ms_deployment.version.VersionSpecifier *by observed behaviour* rather than by
reading its source, because a disagreement between the two is a version nsctl
picks and the control plane then rejects. This script is how the behaviour is
observed: it runs every specifier against every version and records what the
Python evaluator did.

The table is the contract and the prose is commentary, so when the two disagree,
regenerate rather than edit.

Run it from anywhere:

    python3 generate_golden.py [path/to/hmd-ms-deployment/src/python]

The default is the sibling checkout. Nothing but hmd_ms_deployment.version is
imported, and that module needs only logging, re, abc and typing -- no nsenv, no
FastAPI, no service construction.

Four outcomes are recorded, because validate() is not a boolean predicate: it
returns None on success and raises on failure, in three distinguishable ways.

    accept         validate() returned
    reject         VersionSpecifierException -- the version does not satisfy it
    version_error  the version itself was refused: AssertionError for a
                   non-numeric component, IndexError for a version with fewer
                   components than the spec compares (`~= 0.1.7` against `0.1`)
    spec_error     AssertionError constructing the VersionSpecifier, which is
                   what every ordered specifier written in the wild does

Go treats reject and version_error alike -- neither satisfies -- which is
conservative in the direction that matters: nsctl never selects a version the
control plane cannot even validate.
"""

import json
import os
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent
DEFAULT_SOURCE = HERE.parents[6] / "hmd-ms-deployment" / "src" / "python"

# Every specifier shape the platform writes, plus the ones it must refuse.
SPECS = [
    "~= 0.1",
    "~=0.1",
    "~= 0.1.7",
    "~= 0.1.0",
    "~= 1.0",
    "~= 0.1.2.3",
    "== 0.1",
    "== 0.1.*",
    "== 0.1.5",
    "!= 0.1.5",
    "~= 0.1,!= 0.1.5",
    "~= 0.1, != 0.1.5",
    "== 0.1.*,!= 0.1.5",
    # Ordered specifiers. All five real-world uses are written like the first
    # two and do not parse; the last three parse and compare component-wise.
    ">= 0.3",
    ">=0.1",
    ">= 0.3.0",
    "> 0.1.0",
    "< 0.3.0",
    "<= 0.2.0",
    # Malformed.
    "~= 1",
    "== *",
    "0.1",
    "~= a.b",
    "",
]

VERSIONS = [
    "0",
    "0.0.9",
    "0.1",
    "0.1.0",
    "0.1.5",
    "0.1.6",
    "0.1.7",
    "0.1.9",
    "0.1.100",
    "0.1.225",
    "0.1.2.3",
    "0.2.0",
    "0.2.5",
    "0.2.9",
    "0.3.0",
    "0.3.1",
    "0.9.99",
    "1.0.0",
    "1.0.3",
    "2.0.1",
    "abc",
    "0.1.x",
    "",
]


def outcome(VersionSpecifier, VersionSpecifierException, spec, version):
    try:
        specifier = VersionSpecifier(spec)
    except Exception:
        # Every construction failure is an AssertionError today, but the
        # distinction Go cares about is "cannot be parsed", not which exception
        # said so.
        return "spec_error"
    try:
        specifier.validate(version)
    except VersionSpecifierException:
        return "reject"
    except Exception:
        return "version_error"
    return "accept"


def main():
    source = pathlib.Path(sys.argv[1]) if len(sys.argv) > 1 else DEFAULT_SOURCE
    source = pathlib.Path(os.environ.get("HMD_MS_DEPLOYMENT_SRC", source))
    if not (source / "hmd_ms_deployment" / "version.py").exists():
        sys.exit(f"no hmd_ms_deployment/version.py under {source}")
    sys.path.insert(0, str(source))
    from hmd_ms_deployment.version import (  # noqa: E402
        VersionSpecifier,
        VersionSpecifierException,
    )

    cases = [
        {
            "spec": spec,
            "version": version,
            "outcome": outcome(
                VersionSpecifier, VersionSpecifierException, spec, version
            ),
        }
        for spec in SPECS
        for version in VERSIONS
    ]

    table = {
        "source": "hmd_ms_deployment.version.VersionSpecifier",
        "generator": "internal/versionspec/testdata/generate_golden.py",
        "cases": cases,
    }
    out = HERE / "golden.json"
    out.write_text(json.dumps(table, indent=2) + "\n")

    tally = {}
    for case in cases:
        tally[case["outcome"]] = tally.get(case["outcome"], 0) + 1
    print(f"{out}: {len(cases)} cases {tally}", file=sys.stderr)


if __name__ == "__main__":
    main()
