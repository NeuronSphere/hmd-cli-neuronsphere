from collections import deque, defaultdict
from typing import Dict, Callable

from hmd_ms_deployment.deploy_logic import DeploymentDag


def traverse_deployment_dag(deployment_dag: DeploymentDag, visitor: Callable, context: Dict):
    nodes = list(deployment_dag.nodes.values())
    remaining = {node: len(node.children) for node in nodes}
    dependents = defaultdict(list)
    for node in nodes:
        for child in node.children:
            dependents[child].append(node)

    queue = deque(node for node in nodes if remaining[node] == 0)
    while queue:
        node = queue.popleft()
        visitor(node, context)
        for parent in dependents[node]:
            remaining[parent] -= 1
            if remaining[parent] == 0:
                queue.append(parent)
