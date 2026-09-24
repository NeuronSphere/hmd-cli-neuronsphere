package bom

import (
	"context"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hosturl"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// The base resource types the local core's concrete Resources are typed by.
// All four come from ms-deployment's own bundled catalog
// (seed_base_resource_definitions), which is why nothing here declares a
// parent: these *are* the parents.
const (
	nsNetwork     = "network.neuronsphere.io"
	nsCompute     = "compute.neuronsphere.io"
	nsKubernetes  = "kubernetes.neuronsphere.io"
	nsApplication = "application.neuronsphere.io"

	defVersion = "0.1.0"
)

// Tag is one selector-visible label on a Resource.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Resource is one concrete NERD0004 Resource the local core provides.
//
// Concrete, as opposed to the produced *types* DeclareProduces records. The
// difference is what made this necessary: a dependency carrying a tag_selector
// -- hmd-database-account's create-service asks for an
// application.neuronsphere.io/microservice tagged repo_class=hmd-ms-dbaccount --
// is matched against Resources, not against produced types, so declaring the
// type is not enough and apply_changeset answers "supplied instance,
// local-neuronsphere, satisfies neither the required resource type ... nor a
// suggested repo_class".
type Resource struct {
	Name       string
	Namespace  string
	Definition string
	Output     map[string]any
	Tags       []Tag
}

// ServiceSpec is one foundation service to advertise as a microservice
// Resource: the Lambdas nsctl owns directly and never registers as
// RepoInstances, whose own manifests' required dependencies would never
// resolve locally.
type ServiceSpec struct {
	// Name is the function name, which is also the route segment.
	Name string
	// RepoClass is what a consumer's tag_selector matches on.
	RepoClass string
	// APIBaseURL is where the service answers.
	APIBaseURL string
}

// LocalCoreResources is the registry of concrete Resources the local core
// actually provides.
//
// Each is owned by the single hmd-cli-neuronsphere / local-neuronsphere
// instance and typed by a base definition, which is what gives local-cloud
// parity: because docker-network isa network, a cloud repo declaring a
// dependency on network.neuronsphere.io/network is satisfied locally.
//
// kubernetes.neuronsphere.io/kubernetes-cluster is deliberately absent. The
// eks-cluster instance produces it directly, so the same repo is the producer
// in both places; the cluster's *other* core Resources are built here because
// those stay core-produced.
//
// clusterName empty means no cluster, and its two Resources are skipped rather
// than submitted describing something that does not exist.
func LocalCoreResources(env Environment, networkName, clusterName string, services []ServiceSpec) []Resource {
	// environment carries the slug -- the Environment.type a consumer's deploy
	// is invoked with. Resource queries scope by environment through
	// find_resources_by_selector's own argument, so this tag is descriptive;
	// it is kept accurate so a hand-written selector reads the same locally as
	// in the cloud.
	common := []Tag{{"environment", env.Slug}}
	if env.DeploymentID != "" {
		common = append(common, Tag{"deployment_id", env.DeploymentID})
	}
	with := func(extra ...Tag) []Tag {
		tags := make([]Tag, 0, len(common)+len(extra))
		tags = append(tags, common...)
		return append(tags, extra...)
	}

	resources := []Resource{{
		Name:       networkName,
		Namespace:  nsNetwork,
		Definition: "docker-network",
		Output:     map[string]any{"network_name": networkName, "driver": "bridge"},
		Tags:       with(Tag{"platform", "local"}),
	}}

	if clusterName != "" {
		// Not required for dependency validation -- a selector-less dep is
		// satisfied by the produces edge alone -- but submitted so discovery
		// finds it.
		resources = append(resources, Resource{
			Name:       clusterName + "-compute",
			Namespace:  nsCompute,
			Definition: "compute-node",
			Output:     map[string]any{"node_group_name": clusterName + "-compute"},
			Tags:       with(Tag{"cluster_type", "k3s"}),
		})
		resources = append(resources, Resource{
			Name:       clusterName + "-traefik",
			Namespace:  nsKubernetes,
			Definition: "ingress-controller",
			// name/namespace satisfy the schema inherited from the deployment
			// base type; ingress_class is the ingress-controller's own field.
			// The class is alb, not traefik: local Traefik is configured to
			// answer to the cloud's ALB class so charts render the same Ingress
			// in both places. Advertising traefik would hand consumers a class
			// nothing serves.
			Output: map[string]any{
				"name":          "traefik",
				"namespace":     "kube-system",
				"ingress_class": "alb",
			},
			Tags: with(Tag{"cluster_type", "k3s"}),
		})
	}

	for _, svc := range services {
		if svc.Name == "" {
			continue
		}
		url := svc.APIBaseURL
		if url == "" {
			url = hosturl.Route(svc.Name)
		}
		tags := with()
		if svc.RepoClass != "" {
			// The tag every tag_selector dependency on a service matches.
			tags = append(tags, Tag{"repo_class", svc.RepoClass})
		}
		resources = append(resources, Resource{
			Name:       "local-service-" + svc.Name,
			Namespace:  nsApplication,
			Definition: "microservice",
			Output:     map[string]any{"api_base_url": url},
			Tags:       tags,
		})
	}
	return resources
}

// SubmitResources attaches the concrete Resources to the core instance's
// deployment.
//
// coreDeploymentID is the local-neuronsphere RepoInstanceDeployment, which only
// exists once that instance has been through a changeset -- which is why the
// substrate is applied as its own phase before anything that might select one
// of these.
//
// Per-resource best-effort: one that fails is a warning, because a missing
// discovery Resource degrades a later deploy and a failed submission loop would
// abort the environment outright.
func (s *Seeder) SubmitResources(ctx context.Context, coreDeploymentID string, resources []Resource) int {
	if coreDeploymentID == "" {
		s.warn("no deployed %s instance, so the local core Resources were not submitted; "+
			"a dependency selecting one will not resolve", CoreInstanceName)
		return 0
	}
	submitted := 0
	for _, r := range resources {
		payload := map[string]any{
			"repo_instance_deployment_id": coreDeploymentID,
			"resources": []map[string]any{{
				"resource_name": r.Name,
				"resource_definition": map[string]any{
					"resource_namespace":       r.Namespace,
					"resource_definition_name": r.Definition,
					"version":                  defVersion,
				},
				"output": r.Output,
				"tags":   r.Tags,
			}},
		}
		if _, err := s.Client.APIOp(ctx, "submit_resources", payload); err != nil {
			s.warn("could not submit the %s/%s Resource %q: %v", r.Namespace, r.Definition, r.Name, err)
			continue
		}
		submitted++
	}
	return submitted
}

// CoreDeploymentID is the local-neuronsphere RepoInstanceDeployment id.
//
// Taken from the nodes a seed just returned when there are any. There are none
// on a warm start -- nothing was deployed -- so it falls back to the graph,
// which still holds the deployment from whenever the environment was
// bootstrapped. Without that fallback a restart could never refresh these
// Resources.
func (s *Seeder) CoreDeploymentID(ctx context.Context, env Environment, nodes []msdeploy.DeploymentNode) string {
	for _, n := range nodes {
		if n.InstanceName == CoreInstanceName && n.RIDNid != "" {
			return n.RIDNid
		}
	}
	id, err := s.Client.CoreInstanceDeployment(ctx, CoreInstanceName, env.Slug)
	if err != nil {
		s.warn("could not resolve the %s deployment: %v", CoreInstanceName, err)
		return ""
	}
	return id
}
