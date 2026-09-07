from hmd_ms_deployment.deploy_logic import DeploymentDag, GraphNode
from hmd_ms_deployment.deployment_dag_traversal import traverse_deployment_dag


class FakeInstance:
    def __init__(self, identifier):
        self.identifier = identifier


def make_node(name):
    return GraphNode(FakeInstance(name))


def make_dag(*nodes):
    return DeploymentDag({node.instance: node for node in nodes})


def record_visitor(node, context):
    context["order"].append(node.instance.identifier)


def assert_children_before_parents(dag, order):
    position = {name: i for i, name in enumerate(order)}
    for node in dag.nodes.values():
        for child in node.children:
            assert position[child.instance.identifier] < position[node.instance.identifier], (
                f"{child.instance.identifier} should be visited before "
                f"{node.instance.identifier}"
            )


def test_visits_every_node_exactly_once():
    a, b, c = make_node("a"), make_node("b"), make_node("c")
    a.children.add(b)
    b.children.add(c)
    dag = make_dag(a, b, c)

    context = {"order": []}
    traverse_deployment_dag(dag, record_visitor, context)

    assert sorted(context["order"]) == ["a", "b", "c"]
    assert context["order"] == ["c", "b", "a"]


def test_diamond_dependency_visits_shared_child_once():
    a, b, c, d = make_node("a"), make_node("b"), make_node("c"), make_node("d")
    a.children.add(b)
    a.children.add(c)
    b.children.add(d)
    c.children.add(d)
    dag = make_dag(a, b, c, d)

    context = {"order": []}
    traverse_deployment_dag(dag, record_visitor, context)

    assert context["order"].count("d") == 1
    assert_children_before_parents(dag, context["order"])


def test_multiple_independent_roots():
    a, b, c, d = make_node("a"), make_node("b"), make_node("c"), make_node("d")
    a.children.add(b)
    c.children.add(d)
    dag = make_dag(a, b, c, d)

    context = {"order": []}
    traverse_deployment_dag(dag, record_visitor, context)

    assert sorted(context["order"]) == ["a", "b", "c", "d"]
    assert_children_before_parents(dag, context["order"])


def test_large_linear_chain_completes_without_error():
    n = 250
    nodes = [make_node(str(i)) for i in range(n)]
    for parent, child in zip(nodes, nodes[1:]):
        parent.children.add(child)
    dag = make_dag(*nodes)

    context = {"order": []}
    traverse_deployment_dag(dag, record_visitor, context)

    assert context["order"] == [str(i) for i in range(n - 1, -1, -1)]
