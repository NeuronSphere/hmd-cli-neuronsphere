package environment

import (
	"context"
	"fmt"
	"sort"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// DoctorChecks reports whether each environment's substrate services are
// current and answering (NERD024 SPEC006).
//
// It names repairs and performs none, which is doctor's existing contract and
// the reason this is a separate reader rather than a call to ReconcileDBAccount:
// a diagnostic that deploys is one people stop running before they need it.
//
// Silence is the default. An unreadable registry, an environment that routes
// nothing, a Floci that does not answer and a proxy that is down are each
// "nothing to check" rather than a finding: doctor's engine and control-plane
// checks already own those, and a second opinion on them from here would be a
// cascade of failures with one cause.
func DoctorChecks(opts *Options) func(context.Context) []doctor.Check {
	return func(ctx context.Context) []doctor.Check {
		reg, err := registry.Load(opts.Home, opts.Lookup)
		if err != nil {
			return nil
		}
		r := router.New(opts.Home, opts.Lookup)
		resolver := repoclass.NewWithHome(opts.lookup("HMD_REPO_HOME"), opts.Home, opts.Lookup)
		want := resolver.ResolveVersion(DBAccountRepoClass, "").Version

		// Sorted, because doctor's output is read as a list and a map's order
		// would shuffle it between runs of the same command.
		slugs := make([]string, 0, len(reg.Environments))
		for slug := range reg.Environments {
			slugs = append(slugs, slug)
		}
		sort.Strings(slugs)

		var checks []doctor.Check
		name := floci.LambdaName(DBAccountRepoClass)
		for _, slug := range slugs {
			e := reg.Environments[slug]
			env := &e
			// The router's record is the cheap gate. An environment that routes
			// no dbaccount either never needed one (NERD024 SPEC001) or has
			// never started, and neither is a finding.
			if !r.RoutesService(slug, name) {
				continue
			}
			if c, ok := dbAccountCheck(ctx, opts, env, name, want); ok {
				checks = append(checks, c)
			}
		}
		return checks
	}
}

// dbAccountCheck is one environment's finding, and whether there is one.
func dbAccountCheck(ctx context.Context, opts *Options, env *registry.Environment,
	name, want string) (doctor.Check, bool) {

	target := floci.ForAccount(opts.Lookup, env.AccountID, env.LegacyLayout)
	services, err := floci.NewServices(ctx, target)
	if err != nil {
		return doctor.Check{}, false
	}
	deployed := deployedVersion(ctx, services, DBAccountRepoClass)
	if deployed == "" {
		// Routed but not deployed. Floci is up -- NewServices answered -- so
		// this is a real inconsistency, and one `env start` resolves.
		return doctor.Check{
			Name:   checkName(env.Slug),
			Status: doctor.StatusWarn,
			Detail: fmt.Sprintf("%q routes %s but Floci is serving no such function", env.Slug, name),
			Remedy: "nsctl env start " + env.Slug,
		}, true
	}

	// A 5xx is the failure this whole mechanism exists for, so it is reported
	// ahead of a version difference: when both are true the 5xx is the one
	// that explains what the user is seeing.
	//
	// Only a 5xx. Nothing answering at all is far more likely to be a stopped
	// proxy or a stopped environment than a broken service, and reporting that
	// as a broken image would send the reader after the wrong thing.
	if code, routed := probeRouteN(ctx, opts.Lookup, env.Slug, name, 1); routed && code >= 500 {
		return doctor.Check{
			Name:   checkName(env.Slug),
			Status: doctor.StatusFail,
			Detail: brokenRoute(opts.Lookup, env.Slug, name, deployed, want, code),
		}, true
	}

	if deployed != want {
		return doctor.Check{
			Name:   checkName(env.Slug),
			Status: doctor.StatusWarn,
			Detail: fmt.Sprintf("%q is serving %s %s; this nsctl resolves %s", env.Slug, name, deployed, want),
			Remedy: "nsctl env start " + env.Slug,
		}, true
	}
	return doctor.Check{
		Name:   checkName(env.Slug),
		Status: doctor.StatusOK,
		Detail: fmt.Sprintf("%s %s, answering", name, deployed),
	}, true
}

func checkName(slug string) string { return "substrate (" + slug + ")" }
