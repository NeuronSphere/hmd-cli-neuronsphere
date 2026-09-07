import json
import os
from hmd_ms_deployment.worflow_gate_prep import prepare_workflow_gate


with open(os.environ["WORKFLOW_GATE_CONFIG"], "r") as config:
    prep_gate_config = json.load(config)

prepare_workflow_gate(prep_gate_config)
