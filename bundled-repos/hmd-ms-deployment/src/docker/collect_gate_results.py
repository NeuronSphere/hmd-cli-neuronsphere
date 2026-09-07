import json
import os

from hmd_ms_deployment.worflow_gate_prep import (
    gather_bender_results,
)

gather_bender_results(
    json.loads(os.environ["CHANGE_SET_DEPLOYMENT"]),
    json.loads(os.environ["CHANGE_SET"]),
)
