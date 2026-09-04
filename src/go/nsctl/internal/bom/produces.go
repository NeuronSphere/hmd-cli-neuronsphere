package bom

import (
	"context"
	"encoding/json"
	"fmt"
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
	{
		// A generic stand-in for "some network the instance lives in",
		// satisfying a resource-typed base-vpc dependency without an hmd-vpc
		// deploy, which is never applicable locally -- Docker networking
		// replaces it.
		"network.neuronsphere.io", "network", "0.1.0", "network",
	},
	{
		// And the concrete vpc subtype, for a repo whose base-vpc dependency
		// names it specifically rather than the abstract network above.
		// Producing a parent type does not satisfy a requirement for a child,
		// so both have to be declared.
		"network.neuronsphere.io", "vpc", "0.1.0", "network",
	},
}

// DeclareCoreProduces records that the core RepoClass produces the local core
// resource types.
//
// It runs after the core RepoClassVersion is registered and *before*
// apply_changeset, so a resource-typed dependency validates against the
// producing instance. Idempotent -- declare_produces is deduped server-side --
// and best effort: a failure here surfaces as a dependency that will not
// resolve, which the apply reports with its own, better message.
func (s *Seeder) DeclareCoreProduces(ctx context.Context) error {
	rcvID, err := s.repoClassVersionID(ctx, CoreRepoClass)
	if err != nil || rcvID == "" {
		return fmt.Errorf("no RepoClassVersion for %s, so its produced resource types cannot be declared", CoreRepoClass)
	}
	for _, d := range CoreProducedDefinitions {
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
