package bom

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// ProducedDefinition is one resource type the core RepoClass declares it
// produces.
type ProducedDefinition struct {
	Namespace string
	Name      string
	Version   string
	Role      string
}

// CoreProducedDefinitions are the resource types the core instance provides
// locally, so a resource-typed dependency resolves against it.
//
// kubernetes-cluster is deliberately absent: it is produced by the eks-cluster
// instance, deployed as a real DAG node so the same repo produces that Resource
// in the cloud and locally. compute-node stays core-produced, because in the
// cloud it comes from hmd-inf-eks-node-group -- a repo distinct from
// hmd-inf-eks-cluster -- and there is no local equivalent of that split.
var CoreProducedDefinitions = []ProducedDefinition{
	{"compute.neuronsphere.io", "compute-node", "0.1.0", "compute"},
	{"network.neuronsphere.io", "docker-network", "0.1.0", "network"},
	{
		// k3s ships Traefik enabled, so the local cluster already serves
		// Ingress. The abstract type is declared rather than the AWS-specific
		// aws-load-balancer-controller subtype, so a cloud repo depending on an
		// ingress-controller resolves locally.
		"kubernetes.neuronsphere.io", "ingress-controller", "0.1.0", "ingress-controller",
	},
	{
		// The foundation services are Lambdas nsctl owns directly and never
		// registers as RepoInstances -- their own manifests' required
		// dependencies would never resolve locally. What they provide is
		// represented purely as concrete microservice Resources, produced
		// type-level by the core instance, so a resource-typed dependency on
		// one resolves without those services existing as RepoInstances.
		"application.neuronsphere.io", "microservice", "0.1.0", "microservice",
	},
}

// network and vpc are deliberately absent. The core instance used to declare
// both, on the grounds that Docker networking substitutes for a VPC locally and
// an hmd-vpc deploy "is never applicable". That was true when written and is
// not any more: Floci 2.0.1's EC2 creates and persists real VPCs and subnets,
// verified by deploying hmd-vpc against an account that had none. The base-vpc
// instance produces both types now -- aws.neuronsphere.io/vpc, parented on
// network.neuronsphere.io/network -- so declaring them here as well would make
// two producers compete for the same dependency.

// BaseVPCProducedDefinitions are the network types the base-vpc instance
// provides.
//
// Both the abstract network and the concrete vpc subtype, because producing a
// parent type does not satisfy a requirement for a child and consumers name
// each: hmd-ms-transform's base-vpc dependency asks for network.neuronsphere.io/network,
// hmd-postgres-rds's asks for network.neuronsphere.io/vpc. These are exactly
// the two the core instance used to declare, moved to the repo that now
// actually creates them.
var BaseVPCProducedDefinitions = []ProducedDefinition{
	{"network.neuronsphere.io", "network", "0.1.0", "network"},
	{"network.neuronsphere.io", "vpc", "0.1.0", "network"},
}

// fallbackProduces is used only when a repo's working tree is not on this
// machine, so there is no meta-data/resources to read. A repo that is present
// always wins, since its own declaration is the one the cloud deploy path
// reads.
var fallbackProduces = map[string][]ProducedDefinition{
	BaseVPCRepoClass: BaseVPCProducedDefinitions,
}

// FromDeclarations turns a repo's meta-data/resources/*.yaml documents into
// the definitions to declare for it.
//
// Each produced declaration contributes itself *and* its parent. Resolution
// matches a requirement against a produced type and its ancestors, but the
// declare_produces_resource_definition operation takes no parent link, so
// whether ms-deployment already knows the hierarchy decides whether a
// requirement for the parent type resolves. Declaring both makes it resolve
// either way, and it is exactly what the hardcoded list it replaces did:
// network/vpc together with network/network, which is hmd-vpc's declaration
// and its parent.
func FromDeclarations(declarations []repoclass.ResourceDeclaration) []ProducedDefinition {
	var defs []ProducedDefinition
	seen := map[string]bool{}
	add := func(namespace, name, version, role string) {
		if namespace == "" || name == "" {
			return
		}
		key := namespace + "/" + name
		if seen[key] {
			return
		}
		seen[key] = true
		defs = append(defs, ProducedDefinition{namespace, name, version, role})
	}
	for _, d := range repoclass.Produced(declarations) {
		add(d.Namespace, d.Name, d.Version, d.Role)
		if d.Parent != nil {
			add(d.Parent.Namespace, d.Parent.Name, d.Parent.Version, d.Role)
		}
	}
	return defs
}

// DeclareCoreProduces records what the core RepoClass produces.
func (s *Seeder) DeclareCoreProduces(ctx context.Context) error {
	return s.DeclareProduces(ctx, CoreRepoClass, CoreProducedDefinitions)
}

// DeclareProduces records that a RepoClass produces a set of resource types.
//
// It runs after the RepoClassVersion is registered and *before*
// apply_changeset, so a resource-typed dependency validates against the
// producing instance rather than failing with "supplied instance satisfies
// neither the required resource type nor a suggested repo_class". Idempotent --
// declare_produces is deduped server-side.
func (s *Seeder) DeclareProduces(ctx context.Context, repoClass string, defs []ProducedDefinition) error {
	rcvID, err := s.repoClassVersionID(ctx, repoClass)
	if err != nil || rcvID == "" {
		return fmt.Errorf("no RepoClassVersion for %s, so its produced resource types cannot be declared", repoClass)
	}
	for _, d := range defs {
		_, err := s.Client.APIOp(ctx, "declare_produces_resource_definition", map[string]any{
			"repo_class_version_id": rcvID,
			"resource_definition": map[string]any{
				"resource_namespace":       d.Namespace,
				"resource_definition_name": d.Name,
				"version":                  d.Version,
			},
			"role": d.Role,
		})
		if err != nil {
			return fmt.Errorf("declaring %s/%s: %w", d.Namespace, d.Name, err)
		}
	}
	return nil
}

// repoClassVersionID resolves a RepoClassVersion identifier.
//
// find_repo_class_versions answers with the versions ascending, so the highest
// is last.
func (s *Seeder) repoClassVersionID(ctx context.Context, repoClass string) (string, error) {
	body, err := s.Client.APIOpRaw(ctx, "find_repo_class_versions/"+repoClass, nil)
	if err != nil {
		return "", err
	}
	var versions []struct {
		Identifier string `json:"identifier"`
		Version    string `json:"version"`
	}
	if err := json.Unmarshal(body, &versions); err != nil || len(versions) == 0 {
		return "", nil
	}
	return versions[len(versions)-1].Identifier, nil
}
