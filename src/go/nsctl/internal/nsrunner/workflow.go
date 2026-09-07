// Package nsrunner is the DAG-runner service: the half of a deployment that
// used to live inside the CLI process.
//
// The CLI used to pull. It asked hmd-ms-deployment for a node list and then
// executed that list itself, so a deployment lived and died with the command
// that started it -- a closed terminal killed a half-finished deploy, a second
// `env start` raced the first, and the control plane had no idea a workflow
// engine existed. In the cloud the same changeset becomes an Argo Workflow and
// the CLI is not involved in running it at all.
//
// This package inverts the local path to match. Node execution itself is not
// reimplemented: internal/runner already does it, and is used here verbatim.
// What changes is who calls it, and that status flows over REST already
// (set_deployment_status, set_change_set_deployment_status) is what makes the
// move as small as it is.
package nsrunner

import (
	"sync"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// Phase is where a workflow has got to. The vocabulary is Argo's, because the
// consumer -- hmd-ms-deployment -- already speaks it.
type Phase string

const (
	PhaseRunning   Phase = "Running"
	PhaseSucceeded Phase = "Succeeded"
	PhaseFailed    Phase = "Failed"
)

// Terminal reports whether the phase can still change.
func (p Phase) Terminal() bool { return p == PhaseSucceeded || p == PhaseFailed }

// NodePhase is one node's outcome. Pending nodes after a failure stay Pending
// rather than becoming Failed: the DAG stops at the first failure, so they were
// never attempted, and calling them failed would be a lie the reconcile
// snapshot then acts on.
type NodePhase string

const (
	NodePending   NodePhase = "Pending"
	NodeRunning   NodePhase = "Running"
	NodeSucceeded NodePhase = "Succeeded"
	NodeFailed    NodePhase = "Failed"
	NodeSkipped   NodePhase = "Skipped"
)

// NodeStatus is what the API reports for one node.
type NodeStatus struct {
	InstanceName  string    `json:"instance_name"`
	RepoClassName string    `json:"repo_class_name"`
	Version       string    `json:"version"`
	Phase         NodePhase `json:"phase"`
	Message       string    `json:"message,omitempty"`
	StartedAt     string    `json:"started_at,omitempty"`
	FinishedAt    string    `json:"finished_at,omitempty"`
}

// Submission is the body of a submit. The envelope is Argo's; the contents are
// local -- `workflow` carries generate_local_deployment's node manifest rather
// than an argoproj.io Workflow, because that manifest is what the local path
// actually has and translating it into a CRD nothing consumes would be
// ceremony. See SPEC010.
type Submission struct {
	Namespace string   `json:"namespace"`
	Workflow  Manifest `json:"workflow"`
}

// Manifest is one changeset's worth of work.
type Manifest struct {
	CSDNid string `json:"csd_nid"`
	// Environment names the environment whose substrate this deploys against.
	// It is what lets a submitter that knows nothing about Docker networks,
	// Floci endpoints or kubeconfigs -- hmd-ms-deployment, for one -- submit
	// anyway: the runner resolves the rest from HMD_HOME itself.
	Environment string                    `json:"environment"`
	Destroy     bool                      `json:"destroy"`
	Nodes       []msdeploy.DeploymentNode `json:"nodes"`
	// Config, when set, overrides what the runner would resolve. The CLI sends
	// it because it has already computed exactly this to reach the same
	// cluster; anything it leaves empty is still filled in from HMD_HOME.
	Config *ConfigOverride `json:"config,omitempty"`
}

// ConfigOverride mirrors the fields of runner.Config a submitter may pin.
type ConfigOverride struct {
	Network              string            `json:"network,omitempty"`
	Image                string            `json:"image,omitempty"`
	FlociEndpoint        string            `json:"floci_endpoint,omitempty"`
	AccountID            string            `json:"account_id,omitempty"`
	DeploymentServiceURL string            `json:"deployment_service_url,omitempty"`
	LocalProxy           string            `json:"local_proxy,omitempty"`
	K3sCluster           string            `json:"k3s_cluster,omitempty"`
	Kubeconfig           string            `json:"kubeconfig,omitempty"`
	RepoHome             string            `json:"repo_home,omitempty"`
	RepoPaths            map[string]string `json:"repo_paths,omitempty"`
	DeploymentID         string            `json:"deployment_id,omitempty"`
	Region               string            `json:"region,omitempty"`
	CustomerCode         string            `json:"customer_code,omitempty"`
	Extra                map[string]string `json:"extra,omitempty"`
}

// Workflow is one submitted deployment, and is what the store persists.
type Workflow struct {
	Name        string       `json:"name"`
	Namespace   string       `json:"namespace"`
	Environment string       `json:"environment"`
	CSDNid      string       `json:"csd_nid"`
	Destroy     bool         `json:"destroy"`
	Phase       Phase        `json:"phase"`
	Message     string       `json:"message,omitempty"`
	Nodes       []NodeStatus `json:"nodes"`
	// Succeeded is the instance names that landed, in order. The CLI writes
	// exactly this into its reconcile snapshot, so a deploy that failed partway
	// still records what got through -- no more, so a failed entry stays
	// eligible for retry, and no less, so a succeeded one is not redeployed
	// every time.
	Succeeded   []string `json:"succeeded"`
	SubmittedAt string   `json:"submitted_at"`
	FinishedAt  string   `json:"finished_at,omitempty"`
	// Failure is the node that stopped the run, carried structurally so an
	// attaching caller can reprint the detail block without scraping the log.
	Failure *Failure `json:"failure,omitempty"`
}

// record is a workflow plus the lock guarding it.
//
// The lock lives here rather than on Workflow so that Workflow stays a plain
// value: it is marshalled, snapshotted and ranged over constantly, and a mutex
// embedded in it would be copied by every one of those.
type record struct {
	mu sync.Mutex
	wf Workflow
}

// snapshot copies the parts callers read, under the lock.
func (r *record) snapshot() Workflow {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.wf
	out.Nodes = append([]NodeStatus(nil), r.wf.Nodes...)
	out.Succeeded = append([]string(nil), r.wf.Succeeded...)
	return out
}

func (r *record) phase() Phase {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wf.Phase
}

func (r *record) name() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wf.Name
}

// Failure is the detail of the node that stopped a workflow.
type Failure struct {
	InstanceName string `json:"instance_name"`
	Message      string `json:"message"`
	Stdout       string `json:"stdout,omitempty"`
	Stderr       string `json:"stderr,omitempty"`
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
