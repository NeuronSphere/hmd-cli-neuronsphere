package nsrunner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// Server is the DAG-runner service.
//
// Authentication is deliberately absent. Argo's submit path carries a bearer
// token because the Argo server is reachable across a cluster; this one is
// published on no host port at all -- the control-plane compose project permits
// only the proxy to publish -- so it is reachable from the platform network and
// nowhere else, which is the same trust boundary every other control-plane
// service already sits inside. Adding a token here would secure nothing and
// would need somewhere to keep it.
type Server struct {
	Home   string
	Lookup func(string) string
	Store  *Store
	Docker runner.Docker
	// MSDeploymentURL is the service the runner reports node status back to.
	MSDeploymentURL string
	// WorkDir holds overlay workspaces and generated deploy scripts.
	WorkDir string
	Out     io.Writer

	// ctx bounds every in-flight deployment, so a shutdown does not leave
	// containers running with nothing watching them.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// submitMu serialises the check-then-claim of an environment, so two
	// simultaneous submissions cannot both find it free.
	submitMu sync.Mutex
}

// NewServer prepares a runner rooted at an HMD_HOME.
func NewServer(home string, lookup func(string) string, out io.Writer) (*Server, error) {
	if home == "" {
		return nil, fmt.Errorf("HMD_HOME is not set; the runner has no environment registry to read")
	}
	if lookup == nil {
		lookup = os.Getenv
	}
	store, err := NewStore(WorkflowDir(home))
	if err != nil {
		return nil, err
	}
	workDir := DefaultWorkDir(home)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating the work directory %s: %w", workDir, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		Home: home, Lookup: lookup, Store: store,
		Docker:          container.New(),
		MSDeploymentURL: msDeploymentURL(lookup),
		WorkDir:         workDir,
		Out:             out,
		ctx:             ctx, cancel: cancel,
	}, nil
}

// msDeploymentURL is the container-side route to hmd-ms-deployment. The CLI's
// default is localhost, which is the host's nginx; from inside the platform
// network the same proxy is hmd_proxy.
func msDeploymentURL(lookup func(string) string) string {
	if v := lookup("HMD_LOCAL_MS_DEPLOYMENT_URL"); v != "" {
		return v
	}
	return "http://hmd_proxy/hmd_ms_deployment"
}

// Submit registers a workflow and starts executing it.
//
// It returns as soon as the workflow is recorded: the point of the service is
// that execution outlives the request, and the caller polls or streams.
func (s *Server) Submit(sub Submission) (Workflow, error) {
	m := sub.Workflow
	if m.Environment == "" {
		return Workflow{}, fmt.Errorf("the submission names no environment")
	}
	if m.CSDNid == "" {
		return Workflow{}, fmt.Errorf("the submission carries no csd_nid")
	}

	cfg, _, err := s.resolveConfig(m.Environment, m.Config)
	if err != nil {
		return Workflow{}, err
	}

	s.submitMu.Lock()
	defer s.submitMu.Unlock()
	if active, busy := s.Store.ActiveFor(m.Environment); busy {
		return Workflow{}, &BusyError{Environment: m.Environment, Workflow: active.name()}
	}

	wf := Workflow{
		Name:        workflowName(),
		Namespace:   sub.Namespace,
		Environment: m.Environment,
		CSDNid:      m.CSDNid,
		Destroy:     m.Destroy,
		Parallelism: parallelism(m.Parallelism, len(m.Nodes)),
		Phase:       PhaseRunning,
		SubmittedAt: now(),
		Nodes:       make([]NodeStatus, 0, len(m.Nodes)),
	}
	for _, n := range m.Nodes {
		wf.Nodes = append(wf.Nodes, NodeStatus{
			InstanceName: n.InstanceName, RepoClassName: n.RepoClassName,
			Version: n.Version, Phase: NodePending,
		})
	}
	rec := &record{wf: wf}
	s.Store.Put(rec)
	log := s.Store.Log(wf.Name)

	run := &runner.Runner{
		Docker: s.Docker,
		Client: msdeploy.New(s.MSDeploymentURL),
		Config: cfg,
		Out:    log,
		Err:    log,
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.execute(s.ctx, rec, run, m.Nodes, log)
	}()
	return rec.snapshot(), nil
}

// Shutdown stops accepting work and waits for in-flight deployments, so a
// control-plane restart does not orphan a half-finished changeset.
func (s *Server) Shutdown(ctx context.Context) {
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// BusyError is the refusal SPEC010 asks for: a second submission against an
// environment already deploying is rejected rather than allowed to interleave.
type BusyError struct {
	Environment string
	Workflow    string
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("environment %s is already deploying under workflow %s", e.Environment, e.Workflow)
}

// workflowName mirrors the shape Argo returns -- a generated, unique name the
// changeset deployment records verbatim.
func workflowName() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("nsrun-%d", time.Now().UnixNano())
	}
	return "nsrun-" + hex.EncodeToString(b[:])
}

// Handler is the HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("POST /api/v1/workflows/{namespace}", s.handleSubmit)
	mux.HandleFunc("GET /api/v1/workflows/{namespace}", s.handleList)
	mux.HandleFunc("GET /api/v1/workflows/{namespace}/{name}", s.handleGet)
	mux.HandleFunc("GET /api/v1/workflows/{namespace}/{name}/log", s.handleLog)
	return mux
}
