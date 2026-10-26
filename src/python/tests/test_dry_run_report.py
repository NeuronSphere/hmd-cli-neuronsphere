"""`hmd neuronsphere plan --dry-run` output (hmd-ms-deployment NERD0011)."""

from hmd_cli_neuronsphere.environments import render_dry_run_report

REPORT = {
    "totals": {"create": 4, "update": 0, "delete": 4, "replace": 0},
    "instances": [
        {
            "repo_instance_name": "core-rds",
            "status": "PLANNED",
            "planned": True,
            "plan": {
                "fidelity": "approximate",
                "fidelity_reasons": ["depends on base-vpc, also in this dry run"],
                "unmigrated": True,
                "counts": {"create": 4, "update": 0, "delete": 4, "replace": 0},
                "deletes": [
                    {
                        "type": "aws_rds_cluster",
                        "address": "module.old.aws_rds_cluster.this[0]",
                    }
                ],
                "replaces": [],
                "violations": [
                    {
                        "rule": "protect type=aws_rds_cluster",
                        "address": "module.old.aws_rds_cluster.this[0]",
                    }
                ],
            },
        },
        {
            "repo_instance_name": "api",
            "status": "PLANNED",
            "planned": False,
            "plan": {},
        },
    ],
}


def test_lists_deletes_violations_fidelity_and_plan_files():
    lines = render_dry_run_report(REPORT, {"core-rds": "/state/plans/rid/core-rds"})
    text = "\n".join(lines)
    assert "core-rds: 4 create, 0 update, 4 delete, 0 replace (approximate)" in text
    assert "approximate: depends on base-vpc" in text
    assert "unmigrated" in text
    assert "DELETE   aws_rds_cluster  module.old.aws_rds_cluster.this[0]" in text
    assert "POLICY   protect type=aws_rds_cluster module.old" in text
    assert "plan files: /state/plans/rid/core-rds" in text
    assert "api: not planned (PLANNED)" in text
