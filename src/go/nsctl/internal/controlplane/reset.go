package controlplane

import (
	"context"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hosturl"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// Reset redeploys a subset of the foundation Lambdas -- hmd-ms-naming,
// hmd-ms-artifact-lib, hmd-ms-deployment -- on a control plane that has
// already bootstrapped, re-resolving each one's version the same way
// Bootstrap did the first time.
//
// It exists because those three are only ever deployed from inside Bootstrap
// (bootstrap.go), which runs exactly once per HMD_HOME (guarded by
// reg.ControlPlane.Bootstrapped). `control-plane stop && start` does not
// redeploy them -- it never has, since a warm start's whole point is to
// recover the existing Lambdas rather than pay for that again. Reset is the
// missing "no, really, redeploy this one" path, scoped narrowly: it never
// touches the VPC, the control-plane database, the graph, or any
// environment's k3s/Postgres/graph state, none of which need a version bump.
func Reset(ctx context.Context, opts *Options, repoClasses ...string) error {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if !reg.ControlPlane.Bootstrapped {
		return nserr.New(nserr.Usage,
			"the control plane at %s has not bootstrapped yet; run `nsctl control-plane start` first", opts.Home)
	}

	targets, err := resolveResetTargets(repoClasses)
	if err != nil {
		return err
	}

	docker := container.New()
	if err := docker.Available(ctx); err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}

	target := floci.ControlPlane(opts.Lookup)
	names := floci.NamesFrom(opts.Lookup, "", "local")
	resolver := repoclass.NewWithHome(opts.lookup("HMD_REPO_HOME"), opts.Home, opts.Lookup)

	services, err := floci.NewServices(ctx, target)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	prov, err := floci.NewProvisioner(ctx, target, opts.Out, opts.Err)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	for _, repoClass := range targets {
		if _, err := deployFoundationService(ctx, opts, docker, services, resolver, repoClass, names, prov.EnsureBucket); err != nil {
			return err
		}
		removeIdleServiceContainers(ctx, opts, docker, reg, repoClass)
	}

	// SetupService recreates each service's REST API on every call
	// (EnsureRestAPI's recreate=true in lambda.go), rotating its API Gateway
	// id even when nothing about that service changed. So the map handed to
	// WriteControlPlaneRoutes has to be rediscovered in full rather than
	// assembled from just the services this loop touched -- otherwise the
	// untouched foundation services' routes are dropped, not preserved. This
	// mirrors Start's own post-bootstrap block exactly.
	opts.step("Configuring control-plane routes...")
	gateways, err := floci.NewGateways(ctx, target)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	routed, err := gateways.ServiceGateways(ctx)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	for _, id := range uniqueValues(routed) {
		if err := gateways.DeployStage(ctx, id, floci.DefaultStage); err != nil {
			opts.warn("%v", err)
		}
	}
	r := router.New(opts.Home, opts.Lookup)
	if err := r.WriteControlPlaneRoutes(routed, floci.DefaultStage, "", nil); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	// SetupService's recreated API Gateway id is worthless to a caller until
	// nginx actually reloads: the route file just written is not what the
	// running proxy serves until it does. Mirrors Start's own post-bootstrap
	// reload (controlplane.go:527) -- a proxy that fails to reload still
	// serves its old routes, which beats aborting a reset that already
	// redeployed the services.
	if err := r.Reload(ctx, docker.Exec); err != nil {
		opts.warn("%v", err)
	}

	// SetupService's freshly created API Gateway id, and the container Floci
	// spawns for its first cold invocation, both take a moment to actually
	// answer -- an immediate caller (this acceptance script included) can hit
	// Floci's "Invalid API id specified" for a few seconds after Reload
	// returns even though everything above succeeded. Every reset hits this,
	// not just an unlucky one: removeIdleServiceContainers guarantees the next
	// invocation is a cold start.
	for _, repoClass := range targets {
		if err := waitForServiceRoute(ctx, floci.LambdaName(repoClass), 30*time.Second); err != nil {
			opts.warn("%v", err)
		}
	}

	opts.step("  reset %v", targets)
	return nil
}

// waitForServiceRoute polls a foundation service's control-plane route until
// it stops answering Floci's "Invalid API id specified" -- the signal that
// the API Gateway this reset just recreated has not finished propagating
// through nginx and Floci yet. Any other response, including one the service
// itself considers an error, means the route resolved and the wait is done.
func waitForServiceRoute(ctx context.Context, lambdaName string, timeout time.Duration) error {
	url := hosturl.Route(lambdaName) + "/"
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)

	for {
		ready, err := serviceRouteReady(ctx, client, url)
		if err == nil && ready {
			return nil
		}
		if time.Now().After(deadline) {
			return nserr.New(nserr.Fail, "%s is not routed after %s", url, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func serviceRouteReady(ctx context.Context, client *http.Client, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return !strings.Contains(string(body), "Invalid API id specified"), nil
}

// resolveResetTargets validates repoClasses against bootstrapServices,
// defaulting to all three when none are given. Reset only knows the three
// names Bootstrap deploys, not arbitrary BOM entries.
func resolveResetTargets(repoClasses []string) ([]string, error) {
	if len(repoClasses) == 0 {
		return bootstrapServices, nil
	}
	known := map[string]bool{}
	for _, s := range bootstrapServices {
		known[s] = true
	}
	for _, rc := range repoClasses {
		if !known[rc] {
			return nil, nserr.New(nserr.Usage,
				"%q is not a foundation service; reset knows %v", rc, bootstrapServices)
		}
	}
	return repoClasses, nil
}

// removeIdleServiceContainers force-removes whatever container Floci spawned
// for a foundation service's last invocation, scoped to this platform's
// network. SetupService updates the Lambda's code and configuration in
// place, but does nothing about a container Floci already spawned and left
// running warm -- without this, the next request just reuses it and keeps
// serving the old image.
func removeIdleServiceContainers(ctx context.Context, opts *Options, docker *container.Docker, reg *registry.Registry, repoClass string) {
	name := floci.LambdaName(repoClass)
	for _, c := range docker.ContainersWithLabelOnNetwork(ctx, container.LabelFlociService, name, reg.ControlPlane.Network) {
		if err := docker.RemoveContainer(ctx, c); err != nil {
			opts.warn("removing idle %s container %s: %v", repoClass, c, err)
			continue
		}
		opts.step("  removed idle %s container %s", repoClass, c)
	}
}
