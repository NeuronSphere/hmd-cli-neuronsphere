package nsrunner

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// DefaultParallelism is how many nodes deploy at once when a submission does
// not say otherwise.
//
// Four, because that is what the cloud path already runs at: the Argo Workflow
// hmd-ms-deployment builds carries spec.parallelism: 4, and the point of this
// runner is to be the local half of the same behaviour rather than a second
// one.
const DefaultParallelism = 4

// execute runs one workflow to completion.
//
// The nodes arrive from traverse_deployment_dag in *a* valid dependency order,
// but each also carries its own `dependencies` -- the instance's real
// RepoInstanceReqRepoInstance edges, already transitively reduced and already
// filtered to the instances in this changeset. Scheduling on an in-degree map
// over that field, rather than walking the list, is the whole of SPEC011: the
// API, the store and node execution are untouched.
//
// Two things about the fail-fast are deliberate.
//
// A failure stops *dispatch*, and does not cancel the nodes already running.
// SPEC011 says to cancel the context so no unstarted node begins, and half of
// that is right -- but internal/container runs `docker run` under
// exec.CommandContext, so cancelling the context the nodes execute under would
// kill them mid-deploy. A node killed halfway through `terraform apply` leaves
// state worse than one allowed to finish, so the gate is a flag on the
// scheduler and the node context is left alone. The SPEC records this.
//
// And an in-flight node that succeeds after another has failed still counts as
// landed. Succeeded is what the reconcile snapshot is built from, and it must
// name exactly what is deployed: no more, or a failed entry is skipped next
// run, and no less, or a succeeded one redeploys forever.
func (s *Server) execute(ctx context.Context, rec *record, run *runner.Runner, nodes []msdeploy.DeploymentNode, log *logBuffer) {
	defer log.Close()

	wf := rec.snapshot()
	run.SetCSDStatus(ctx, wf.CSDNid, msdeploy.CSDStatusStarted)

	success, verb := runner.StatusDeployed, "deployed"
	final := runner.CSDStatusCompleted
	if wf.Destroy {
		// Destroy manifests arrive with their edges already reversed by
		// build_deployment_dag, so the same in-degree map schedules them.
		success, verb = msdeploy.StatusDestroyed, "destroyed"
		final = msdeploy.CSDStatusDestroyed
	}
	limit := parallelism(wf.Parallelism, len(nodes))

	// base is the template each node's runner is copied from. Runner holds no
	// lock and RunNode mutates none of its fields, so a per-node copy that
	// differs only in where it writes is safe -- and copying it once here,
	// before any goroutine starts, is what keeps the shared value read-only
	// for the rest of the run.
	base := *run
	base.Succeeded, base.LastFailure = nil, nil

	sched := newSchedule(nodes)
	done := make(chan completion)

	var (
		succeeded []string
		failure   *runner.Result
		stopped   string // why dispatch stopped; empty while it has not
		started   int
		inflight  int
	)

	for {
		for stopped == "" && inflight < limit {
			if ctx.Err() != nil {
				stopped = "the runner is shutting down"
				break
			}
			i, ok := sched.take()
			if !ok {
				break
			}
			started++
			inflight++
			s.setNode(rec, i, NodeRunning, "")
			go func(i int, node msdeploy.DeploymentNode) {
				done <- completion{index: i, result: s.runNode(ctx, base, node, success, limit > 1, log)}
			}(i, nodes[i])
		}
		// Nothing running and nothing dispatchable: either the DAG is done, or
		// it stopped, or what is left is unreachable. All three end the loop.
		if inflight == 0 {
			break
		}

		c := <-done
		inflight--
		sched.finish(c.index)
		node := nodes[c.index]
		if c.result.Failed {
			s.setNode(rec, c.index, NodeFailed, failureMessage(c.result))
			// The first failure is the one that stopped the run and the one
			// reported; a second, from a node already in flight, is in the log
			// and in that node's phase but does not rewrite the verdict.
			if failure == nil {
				res := c.result
				failure = &res
				stopped = fmt.Sprintf("deploying %s failed", node.InstanceName)
			}
			continue
		}
		succeeded = append(succeeded, node.InstanceName)
		s.setNode(rec, c.index, NodeSucceeded, "")
		fmt.Fprintf(log, "  %s %s (%d/%d)\n", node.InstanceName, verb, len(succeeded), len(nodes))
		sched.release(c.index)
	}

	// Nodes are written back on this goroutine only, once every worker is
	// done, so the copies above never raced a write to them.
	run.Succeeded, run.LastFailure = succeeded, failure

	if failure == nil && stopped == "" && started < len(nodes) {
		// Every remaining node is waiting on something that will never run.
		// Reporting it beats a scheduler that quietly returns having deployed
		// part of a changeset.
		stopped = fmt.Sprintf("%d node(s) never became runnable (%s); the dependency graph has a cycle",
			len(nodes)-started, strings.Join(sched.blocked(nodes), ", "))
	}
	if failure != nil || stopped != "" {
		// Not attempted during a shutdown: the context is already cancelled, so
		// the call could only add a warning line to a log nobody is reading.
		if ctx.Err() == nil {
			run.SetCSDStatus(ctx, wf.CSDNid, runner.CSDStatusFailed)
		}
		s.finish(rec, run, PhaseFailed, stopped)
		return
	}

	run.SetCSDStatus(ctx, wf.CSDNid, final)
	s.finish(rec, run, PhaseSucceeded, "")
}

// completion is one worker reporting back to the scheduler.
type completion struct {
	index  int
	result runner.Result
}

// runNode executes one node, prints under its own name, and reports its status.
//
// The status POSTs happen here rather than on the scheduling goroutine on
// purpose: SetNodeStatus has a 30-second budget, and doing it inline would
// let one slow reply hold up the dispatch of every other node.
func (s *Server) runNode(ctx context.Context, base runner.Runner, node msdeploy.DeploymentNode, success string, prefix bool, log *logBuffer) runner.Result {
	out := io.Writer(log)
	if prefix {
		pw := newPrefixWriter(log, node.InstanceName)
		defer pw.Close()
		out = pw
	}
	base.Out, base.Err = out, out

	// The core repo class executes nothing -- RunNode returns a bare success
	// for it -- but it is still a node: its status flips and its successors
	// are released by exactly the path every other node takes.
	res := base.RunNode(ctx, node)
	if res.Failed {
		base.SetNodeStatus(ctx, node, runner.StatusFailed)
		base.ReportFailure(res)
		return res
	}
	base.SetNodeStatus(ctx, node, success)
	return res
}

// parallelism resolves a submission's concurrency: what it asked for, the cloud
// path's default when it asked for nothing, and never more workers than there
// are nodes to give them.
func parallelism(requested, nodes int) int {
	n := requested
	if n < 1 {
		n = DefaultParallelism
	}
	if nodes > 0 && n > nodes {
		n = nodes
	}
	if n < 1 {
		n = 1
	}
	return n
}

// schedule is the in-degree map over the node payload's `dependencies`.
type schedule struct {
	unmet      []int   // how many dependencies each node is still waiting on
	successors [][]int // node -> the nodes waiting on it
	ready      []int
	// repoClass per node, and the classes currently deploying. Two instances of
	// one repo class must not deploy at once: they share a working tree, and so
	// share its cdktf.out. Six hmd-inf-s3bucket instances dispatched three at a
	// time is how that was found -- one node's rmtree("cdktf.out") deleted the
	// provider directory another was walking, and the deploy died with
	// `FileNotFoundError: 'linux_arm64'`. The dependency graph does not model
	// this, because it is not a dependency: it is a shared resource.
	repoClass []string
	busy      map[string]bool
}

func newSchedule(nodes []msdeploy.DeploymentNode) *schedule {
	index := make(map[string]int, len(nodes))
	for i, n := range nodes {
		index[n.InstanceName] = i
	}
	s := &schedule{
		unmet:      make([]int, len(nodes)),
		successors: make([][]int, len(nodes)),
		repoClass:  make([]string, len(nodes)),
		busy:       map[string]bool{},
	}
	for i, n := range nodes {
		s.repoClass[i] = n.RepoClassName
	}
	for i, n := range nodes {
		seen := make(map[int]bool, len(n.Dependencies))
		for _, dep := range n.Dependencies {
			j, ok := index[dep]
			// A dependency naming something outside this changeset is already
			// met: build_deployment_dag filtered the field to the instances
			// present here, so an edge that survived to something absent points
			// at an instance that is already deployed. A duplicate edge, or a
			// self-edge, would otherwise leave a node waiting forever.
			if !ok || j == i || seen[j] {
				continue
			}
			seen[j] = true
			s.unmet[i]++
			s.successors[j] = append(s.successors[j], i)
		}
	}
	for i := range nodes {
		if s.unmet[i] == 0 {
			s.ready = append(s.ready, i)
		}
	}
	return s
}

// take removes the next runnable node whose repo class is not already
// deploying. Ready nodes are considered in payload order, so a run is as close
// to reproducible as a concurrent one can be.
//
// Skipping over a node rather than stopping at it matters: with six
// hmd-inf-s3bucket instances ready, stopping would idle every worker behind the
// first one, and the whole point is that unrelated repos still overlap.
func (s *schedule) take() (int, bool) {
	for at, i := range s.ready {
		if class := s.repoClass[i]; class != "" && s.busy[class] {
			continue
		}
		s.ready = append(s.ready[:at:at], s.ready[at+1:]...)
		if class := s.repoClass[i]; class != "" {
			s.busy[class] = true
		}
		return i, true
	}
	return 0, false
}

// finish records that a node is no longer running, whether it succeeded or
// failed. Separate from release, which only advances the dependency graph: a
// failed node frees its repo class just the same, and an in-flight sibling
// still has to be able to start.
func (s *schedule) finish(i int) {
	if class := s.repoClass[i]; class != "" {
		delete(s.busy, class)
	}
}

// release decrements the nodes waiting on i, queueing those that just became
// runnable.
func (s *schedule) release(i int) {
	for _, j := range s.successors[i] {
		s.unmet[j]--
		if s.unmet[j] == 0 {
			s.ready = append(s.ready, j)
		}
	}
}

// blocked names the nodes still waiting on a dependency that never landed.
func (s *schedule) blocked(nodes []msdeploy.DeploymentNode) []string {
	var out []string
	for i := range nodes {
		if s.unmet[i] > 0 {
			out = append(out, nodes[i].InstanceName)
		}
	}
	return out
}

// setNode records one node's transition and mirrors it to disk, so a caller
// polling status sees progress rather than a single jump at the end.
func (s *Server) setNode(rec *record, i int, phase NodePhase, message string) {
	rec.mu.Lock()
	if i < len(rec.wf.Nodes) {
		rec.wf.Nodes[i].Phase = phase
		rec.wf.Nodes[i].Message = message
		switch phase {
		case NodeRunning:
			rec.wf.Nodes[i].StartedAt = now()
		case NodeSucceeded, NodeFailed, NodeSkipped:
			rec.wf.Nodes[i].FinishedAt = now()
		}
	}
	rec.mu.Unlock()
	s.Store.Persist(rec)
}

// finish settles the workflow and releases its environment.
func (s *Server) finish(rec *record, run *runner.Runner, phase Phase, message string) {
	rec.mu.Lock()
	rec.wf.Phase = phase
	rec.wf.Message = message
	rec.wf.FinishedAt = now()
	rec.wf.Succeeded = append([]string(nil), run.Succeeded...)
	if run.LastFailure != nil {
		rec.wf.Failure = failureDetail(*run.LastFailure)
	}
	rec.mu.Unlock()
	s.Store.Persist(rec)
}

func failureMessage(res runner.Result) string {
	if res.Err != nil {
		return res.Err.Error()
	}
	return "the deploy script failed"
}

// failureDetail is what the CLI reprints for a failed node. The runner already
// prints it into the log; carrying it structurally as well means an attaching
// caller does not have to scrape the stream to say which node failed.
func failureDetail(res runner.Result) *Failure {
	f := &Failure{InstanceName: res.Node.InstanceName, Message: failureMessage(res)}
	f.Stdout = tailString(string(res.Stdout), 8000)
	f.Stderr = tailString(string(res.Stderr), 8000)
	return f
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
