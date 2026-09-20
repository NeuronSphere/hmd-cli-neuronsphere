package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// DefaultParallelism is how many nodes deploy at once when nothing says
// otherwise.
//
// Four, because that is what the cloud path runs at: the Argo Workflow
// hmd-ms-deployment builds carries spec.parallelism: 4, and the local runner
// is the local half of the same behaviour rather than a second one.
const DefaultParallelism = 4

// Run executes the nodes, independent ones concurrently, stopping dispatch at
// the first failure.
//
// Each node carries its own `dependencies` -- the entry's role targets that are
// in this batch -- and an in-degree map over that field is what decides order,
// not the list order (which Seed already made valid).
//
// Two things about the fail-fast are deliberate. A failure stops *dispatch* and
// does not cancel nodes already running: a node killed halfway through
// `terraform apply` leaves state worse than one allowed to finish. And an
// in-flight node that succeeds after another has failed still counts as landed:
// Succeeded is what the reconcile snapshot is built from, and it must name
// exactly what is deployed.
func (r *Runner) Run(ctx context.Context, nodes []msdeploy.DeploymentNode) error {
	r.Succeeded = nil
	r.LastFailure = nil
	limit := parallelism(r.Parallelism, len(nodes))

	// base is the template each node's runner is copied from. Runner holds no
	// lock and RunNode mutates none of its fields, so a per-node copy that
	// differs only in where it writes is safe -- and copying it once here,
	// before any goroutine starts, keeps the shared value read-only.
	base := *r
	base.Succeeded, base.LastFailure = nil, nil
	var outMu sync.Mutex
	out := &lockedWriter{w: r.Out, mu: &outMu}
	errw := &lockedWriter{w: r.Err, mu: &outMu}

	sched := newSchedule(nodes)
	done := make(chan completion)

	var (
		succeeded []string
		failure   *Result
		stopped   string // why dispatch stopped; empty while it has not
		started   int
		inflight  int
	)

	for {
		for stopped == "" && inflight < limit {
			if ctx.Err() != nil {
				stopped = "interrupted"
				break
			}
			i, ok := sched.take()
			if !ok {
				break
			}
			started++
			inflight++
			go func(i int, node msdeploy.DeploymentNode) {
				done <- completion{index: i, result: runOne(ctx, base, node, out, errw, limit > 1)}
			}(i, nodes[i])
		}
		// Nothing running and nothing dispatchable: the DAG is done, or it
		// stopped, or what is left is unreachable. All three end the loop.
		if inflight == 0 {
			break
		}

		c := <-done
		inflight--
		sched.finish(c.index)
		node := nodes[c.index]
		if c.result.Failed {
			// The first failure is the one that stopped the run and the one
			// reported; a second, from a node already in flight, is printed
			// but does not rewrite the verdict.
			if failure == nil {
				res := c.result
				failure = &res
				stopped = fmt.Sprintf("deploying %s failed", node.InstanceName)
			}
			continue
		}
		succeeded = append(succeeded, node.InstanceName)
		if limit > 1 {
			r.step("  %s deployed (%d/%d)", node.InstanceName, len(succeeded), len(nodes))
		}
		sched.release(c.index)
	}

	r.Succeeded, r.LastFailure = succeeded, failure

	if failure == nil && stopped == "" && started < len(nodes) {
		// Every remaining node is waiting on something that will never run.
		return fmt.Errorf("%d node(s) never became runnable (%s); the dependency graph has a cycle",
			len(nodes)-started, strings.Join(sched.blocked(nodes), ", "))
	}
	if failure != nil {
		return fmt.Errorf("%s", stopped)
	}
	if stopped != "" {
		return fmt.Errorf("deployment stopped: %s", stopped)
	}
	return nil
}

// completion is one worker reporting back to the scheduler.
type completion struct {
	index  int
	result Result
}

// runOne executes one node, prints under its own name when others run beside
// it, and reports its status.
//
// The status POST happens here rather than on the scheduling goroutine on
// purpose: SetNodeStatus has a 30-second budget, and doing it inline would let
// one slow reply hold up the dispatch of every other node.
func runOne(ctx context.Context, base Runner, node msdeploy.DeploymentNode, out, errw io.Writer, prefix bool) Result {
	if prefix {
		po := newPrefixWriter(out, node.InstanceName)
		pe := newPrefixWriter(errw, node.InstanceName)
		defer po.Close()
		defer pe.Close()
		base.Out, base.Err = po, pe
	} else {
		base.Out, base.Err = out, errw
	}
	res := base.RunNode(ctx, node)
	if res.Failed {
		base.SetNodeStatus(ctx, node, StatusFailed)
		base.printFailure(res)
		return res
	}
	base.SetNodeStatus(ctx, node, StatusDeployed)
	return res
}

// parallelism resolves the concurrency: what was asked for, the default when
// nothing was, and never more workers than there are nodes.
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
	unmet      []int
	successors [][]int
	ready      []int
	// Two nodes of one repo class do not deploy at once: `hmd deploy` for a
	// class unpacks into the class's shared tree, and two instances of the
	// same class racing that unpack is a real failure (a shared resource, not
	// a dependency, so the graph does not model it).
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
			// A dependency naming something outside this batch is already
			// met. A duplicate edge, or a self-edge, would otherwise leave a
			// node waiting forever.
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
// to reproducible as a concurrent one can be; a node whose class is busy is
// skipped, not waited on, so unrelated repos still overlap.
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
// failed: a failed node frees its repo class just the same.
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

// lockedWriter serialises writes from concurrent nodes onto one stream.
type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	if l.w == nil {
		return len(b), nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// prefixWriter tags every line of a node's output with its instance name and
// emits whole lines at a time, so two nodes' output interleave by line rather
// than by fragment.
type prefixWriter struct {
	w      io.Writer
	prefix string
	buf    []byte
}

func newPrefixWriter(w io.Writer, instance string) *prefixWriter {
	return &prefixWriter{w: w, prefix: "[" + instance + "] "}
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	cut := bytes.LastIndexByte(p.buf, '\n')
	if cut < 0 {
		return len(b), nil
	}
	p.emit(p.buf[:cut+1])
	p.buf = append(p.buf[:0], p.buf[cut+1:]...)
	return len(b), nil
}

// Close flushes a trailing line that never got its newline.
func (p *prefixWriter) Close() {
	if len(p.buf) == 0 {
		return
	}
	p.emit(append(p.buf, '\n'))
	p.buf = nil
}

func (p *prefixWriter) emit(chunk []byte) {
	var out bytes.Buffer
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			i = len(chunk) - 1
		}
		out.WriteString(p.prefix)
		out.Write(chunk[:i+1])
		chunk = chunk[i+1:]
	}
	_, _ = p.w.Write(out.Bytes())
}
