package nsrunner

import (
	"context"
	"fmt"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// execute runs one workflow to completion.
//
// The order and the fail-fast semantics are exactly internal/runner's: the
// nodes arrive in dependency order from traverse_deployment_dag, and everything
// after a failure depends on it, so it would fail in a way that says less.
//
// The loop is here rather than in runner.Run only because the service reports
// per-node transitions as they happen. That is also what makes SPEC011 a change
// to this function alone: the node payload already carries `dependencies`, so
// scheduling on an in-degree map replaces the range below without touching the
// API, the store, or node execution.
func (s *Server) execute(ctx context.Context, rec *record, run *runner.Runner, nodes []msdeploy.DeploymentNode, log *logBuffer) {
	defer log.Close()

	wf := rec.snapshot()
	run.SetCSDStatus(ctx, wf.CSDNid, msdeploy.CSDStatusStarted)

	success := runner.StatusDeployed
	if wf.Destroy {
		success = msdeploy.StatusDestroyed
	}

	for i, node := range nodes {
		if err := ctx.Err(); err != nil {
			s.finish(rec, run, PhaseFailed, "the runner is shutting down")
			return
		}
		s.setNode(rec, i, NodeRunning, "")

		res := run.RunNode(ctx, node)
		if res.Failed {
			failure := res
			run.LastFailure = &failure
			run.SetNodeStatus(ctx, node, runner.StatusFailed)
			run.SetCSDStatus(ctx, wf.CSDNid, runner.CSDStatusFailed)
			run.ReportFailure(res)
			s.setNode(rec, i, NodeFailed, failureMessage(res))
			s.finish(rec, run, PhaseFailed, fmt.Sprintf("deploying %s failed", node.InstanceName))
			return
		}

		run.Succeeded = append(run.Succeeded, node.InstanceName)
		run.SetNodeStatus(ctx, node, success)
		s.setNode(rec, i, NodeSucceeded, "")
	}

	final := runner.CSDStatusCompleted
	if wf.Destroy {
		final = msdeploy.CSDStatusDestroyed
	}
	run.SetCSDStatus(ctx, wf.CSDNid, final)
	s.finish(rec, run, PhaseSucceeded, "")
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
