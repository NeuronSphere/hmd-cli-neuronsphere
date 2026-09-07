package environment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsrunner"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// DefaultRunnerURL is the control-plane route to the DAG runner, from the host.
// The container publishes no port of its own, so this is an hmd_proxy
// passthrough rather than a direct address.
const DefaultRunnerURL = "http://localhost/nsrunner"

// RunnerURL is where to reach the DAG runner.
func RunnerURL(lookup func(string) string) string {
	if lookup != nil {
		if v := lookup("HMD_LOCAL_RUNNER_URL"); v != "" {
			return v
		}
	}
	return DefaultRunnerURL
}

// deployer runs a changeset's nodes, either by submitting them to the control
// plane's DAG runner or, when there is none, in this process.
//
// Both paths exist on purpose and neither is transitional. Submitting is what
// makes a deployment outlive the CLI and lets a second `env start` be refused
// rather than race. But the runner is itself a control-plane container, so it
// cannot be the thing that brings the control plane up, and an installation
// that has not enabled it must still be able to deploy. The CLI therefore asks
// whether a runner is answering and falls back when it is not.
type deployer struct {
	opts   *Options
	run    *runner.Runner
	env    string
	client *nsrunner.Client

	// submitted records what the remote run settled, so the reconcile snapshot
	// reads the same field whichever path executed.
	remoteSucceeded []string
	remoteFailure   *runner.Result
}

func newDeployer(opts *Options, run *runner.Runner, envSlug string) *deployer {
	d := &deployer{opts: opts, run: run, env: envSlug}
	c := nsrunner.NewClient(RunnerURL(opts.lookup))
	if c.Reachable(context.Background()) {
		d.client = c
	}
	return d
}

func (d *deployer) deploy(ctx context.Context, csdNid string, nodes []msdeploy.DeploymentNode) error {
	if d.client == nil {
		return d.run.Run(ctx, csdNid, nodes)
	}
	return d.submit(ctx, csdNid, nodes)
}

// submit hands the nodes to the runner and streams the result back.
//
// The CLI pins the whole platform context it already computed rather than
// letting the runner resolve it: both would reach the same cluster, but a
// difference between them would surface as a deploy against the wrong
// environment, which is not a failure mode worth leaving open.
func (d *deployer) submit(ctx context.Context, csdNid string, nodes []msdeploy.DeploymentNode) error {
	cfg := d.run.Config
	m := nsrunner.Manifest{
		CSDNid:      csdNid,
		Environment: d.env,
		Nodes:       nodes,
		Config: &nsrunner.ConfigOverride{
			Network: cfg.Network, Image: cfg.Image,
			FlociEndpoint: cfg.FlociEndpoint, AccountID: cfg.AccountID,
			DeploymentServiceURL: cfg.DeploymentServiceURL, LocalProxy: cfg.LocalProxy,
			K3sCluster: cfg.K3sCluster, Kubeconfig: cfg.Kubeconfig,
			RepoHome: cfg.RepoHome, RepoPaths: cfg.RepoPaths,
			DeploymentID: cfg.DeploymentID, Region: cfg.Region,
			CustomerCode: cfg.CustomerCode, Extra: cfg.Extra,
		},
	}

	name, err := d.client.Submit(ctx, d.env, m)
	if err != nil {
		var se *nsrunner.SubmitError
		if errors.As(err, &se) && se.Busy() {
			// Refused, not raced. Distinct exit code, because the platform is
			// fine and the caller simply asked at the wrong moment.
			return nserr.Wrap(nserr.InUse, fmt.Errorf(
				"%s is already being deployed; wait for it, or watch it with `nsctl env attach %s`", d.env, d.env))
		}
		return err
	}
	d.opts.step("  submitted as %s", name)

	// Streaming is presentation only. The deployment is the runner's now, so a
	// broken stream is not a failed deploy -- the status below is what decides.
	if err := d.client.Stream(ctx, d.env, name, d.opts.Out); err != nil {
		d.opts.warn("lost the log stream: %v", err)
	}

	wf, err := d.client.Get(ctx, d.env, name)
	if err != nil {
		return fmt.Errorf("reading the outcome of %s: %w", name, err)
	}
	return d.record(wf)
}

// record translates a finished workflow into what the caller expects: the
// instances that settled, and the failure detail the subnet-group migration
// reads.
func (d *deployer) record(wf nsrunner.Workflow) error {
	d.remoteSucceeded = wf.Succeeded
	if wf.Failure != nil {
		d.remoteFailure = &runner.Result{
			Node:   msdeploy.DeploymentNode{InstanceName: wf.Failure.InstanceName},
			Failed: true,
			Stdout: []byte(wf.Failure.Stdout),
			Stderr: []byte(wf.Failure.Stderr),
			Err:    errors.New(wf.Failure.Message),
		}
	}
	switch wf.Phase {
	case nsrunner.PhaseSucceeded:
		return nil
	case nsrunner.PhaseFailed:
		if wf.Failure != nil {
			d.opts.printRemoteFailure(wf.Failure)
			return fmt.Errorf("deploying %s failed", wf.Failure.InstanceName)
		}
		return errors.New(strings.TrimSpace(wf.Message))
	default:
		return fmt.Errorf("the deployment is still %s", wf.Phase)
	}
}

func (d *deployer) succeeded() []string {
	if d.client == nil {
		return d.run.Succeeded
	}
	return d.remoteSucceeded
}

func (d *deployer) lastFailure() *runner.Result {
	if d.client == nil {
		return d.run.LastFailure
	}
	return d.remoteFailure
}

// printRemoteFailure reprints a failed node's detail. The runner already
// printed it into the log this CLI streamed, but a stream that dropped, or a
// caller attaching after the fact, must still see why the deploy stopped.
func (o *Options) printRemoteFailure(f *nsrunner.Failure) {
	o.warn("%s failed: %s", f.InstanceName, f.Message)
	if s := strings.TrimSpace(f.Stderr); s != "" {
		fmt.Fprintf(o.Err, "--- %s stderr ---\n%s\n", f.InstanceName, s)
	}
}

// Attach joins a deployment already in flight and streams it to the end.
//
// This is what the inversion buys a user: a deploy is no longer owned by the
// terminal that started it, so it can be watched from another one, or picked
// back up after the first was closed.
func Attach(ctx context.Context, opts *Options, name string) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	env, err := reg.Environment(name, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}

	c := nsrunner.NewClient(RunnerURL(opts.lookup))
	if !c.Reachable(ctx) {
		return nserr.New(nserr.Usage,
			"no DAG runner is answering at %s; deployments run inside the CLI, so there is nothing to attach to",
			RunnerURL(opts.lookup))
	}

	wf, active, err := c.Active(ctx, env.Slug, env.Slug)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if !active {
		opts.step("Nothing is deploying in %s", env.Slug)
		return nil
	}

	opts.step("Attached to %s", wf.Name)
	if err := c.Stream(ctx, env.Slug, wf.Name, opts.Out); err != nil {
		opts.warn("lost the log stream: %v", err)
	}

	final, err := c.Get(ctx, env.Slug, wf.Name)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	switch final.Phase {
	case nsrunner.PhaseSucceeded:
		opts.step("  deployed %d instance(s)", len(final.Succeeded))
		return nil
	case nsrunner.PhaseFailed:
		if final.Failure != nil {
			opts.printRemoteFailure(final.Failure)
		}
		// The same code a failed deploy exits with when this CLI ran it: the
		// platform is up and one entry did not deploy.
		return nserr.New(nserr.DeployFailed, "the deployment of %s failed", env.Slug)
	default:
		return nserr.New(nserr.Fail, "the deployment is still %s", final.Phase)
	}
}
