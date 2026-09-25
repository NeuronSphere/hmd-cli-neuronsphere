package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/controlplane"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/status"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tty"
	"github.com/spf13/cobra"
)

// loadRegistry resolves HMD_HOME and reads the registry, turning both failure
// modes into errors that name the fix.
func loadRegistry(opts *Options) (*registry.Registry, string, error) {
	home, err := opts.RequireHome()
	if err != nil {
		return nil, "", err
	}
	reg, err := registry.Load(home, opts.Lookup)
	if err != nil {
		return nil, "", nserr.Wrap(nserr.Fail, err)
	}
	return reg, home, nil
}

// reporter builds a status Reporter bound to the real Docker and a real probe.
//
// reg carries the host ports this home publishes, which are chosen rather than
// fixed (NERD025 SPEC008) -- a report built without it names the defaults and so
// can name a port nothing answers on.
func reporter(opts *Options, reg *registry.Registry) *status.Reporter {
	r := router.New(opts.Home, opts.Lookup)
	rep := &status.Reporter{
		Docker:        container.New(),
		Probe:         status.HTTPProber(3 * time.Second),
		Lookup:        opts.Lookup,
		Substrate:     substrateOf(opts),
		Routed:        r.StreamsPort,
		RoutedService: r.RoutesService,
	}
	if reg != nil {
		rep.ControlPlane = reg.ControlPlane
	}
	return rep
}

// substrateOf answers an environment's recorded substrate by slug, full when
// the manifest is missing or unreadable -- the reading every consumer of the
// mode falls back to.
func substrateOf(opts *Options) func(string) manifest.Substrate {
	return func(slug string) manifest.Substrate {
		mode, err := manifest.LoadSubstrate(opts.Home, slug, opts.Lookup)
		if err != nil {
			return manifest.SubstrateFull
		}
		return mode
	}
}

func newEnvCommand(opts *Options) *cobra.Command {
	env := &cobra.Command{
		Use:           "env",
		Short:         "Work with local environments",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	env.AddCommand(
		newEnvListCommand(opts),
		newEnvStatusCommand(opts),
		newEnvCredentialsCommand(opts),
		newEnvStartCommand(opts),
		newEnvStopCommand(opts),
		newEnvApplyCommand(opts),
		newEnvPlanCommand(opts),
		newEnvAddCommand(opts),
		newEnvDeleteCommand(opts),
		newEnvPurgeCommand(opts),
	)
	return env
}

func newEnvStartCommand(opts *Options) *cobra.Command {
	var noDeploy, verbose, force bool
	var substrate string
	cmd := &cobra.Command{
		Use:   "start [name]",
		Short: "Start an environment's infrastructure",
		Long: `Starts one environment: its database, k3s cluster and routes.

The control plane is started first if it is down, and is left running
afterwards -- stopping it would take every other environment's emulated AWS
with it.

The graph is not in that list because it is provisioned on demand: it is
deployed the first time something declares a graph-db, neptune-db or neptune
dependency, and a default environment runs none. That keeps a gremlin JVM out
of every environment that never reads a graph. HMD_LOCAL_NEURONSPHERE_ENABLE_GRAPH=false
suppresses it even where something asks, leaving that dependency unresolved.

--substrate chooses how much of that infrastructure this environment runs and
records the choice in its manifest for every later start, apply and status:
  full   the database, k3s cluster and the core instances (the default)
  core   the database and the graph; no cluster
  none   nothing beyond the control plane -- for repo classes that deploy with
         their own toolset against infrastructure they already have

On an HMD_HOME with no environments registered this also registers the first
one, so a new install needs this command and nothing before it. Once any
environment exists a name that matches none of them is refused, because then it
is a typo rather than a first run; use ` + "`nsctl env add`" + ` to add another.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			// Which environment this is about, settled before anything starts:
			// a fresh HMD_HOME gets its first one registered here, and a name
			// that matches nothing is refused here rather than after the
			// control-plane bootstrap has run.
			slug, err := resolveStartTarget(cmd, opts, home, name)
			if err != nil {
				return err
			}
			// The mode, recorded before anything starts: a bad value costs a
			// line, and a good one is what every later command reads.
			if cmd.Flags().Changed("substrate") {
				mode, err := manifest.ParseSubstrate(substrate)
				if err != nil {
					return nserr.New(nserr.Usage, "--substrate: %v", err)
				}
				if err := recordSubstrate(opts, home, slug, mode); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Recorded substrate %s for %s\n", mode, slug)
			}
			// `env start` starts the control plane implicitly if it is down,
			// and then leaves it running.
			if err := controlplane.Start(cmd.Context(), &controlplane.Options{
				Home: home, Lookup: opts.Lookup, Version: opts.Version, Verbose: verbose,
				// This environment is about to be started, so the control
				// plane must not stop the containers Floci woke for it only
				// for the next call to start them again.
				StartingEnv: slug,
				Out:         cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}); err != nil {
				return err
			}
			return environment.Start(cmd.Context(), &environment.Options{
				Home: home, Lookup: opts.Lookup, NoDeploy: noDeploy, Verbose: verbose,
				ForceRedeploy: force,
				Out:           cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}, name)
		},
	}
	cmd.Flags().BoolVar(&noDeploy, "no-deploy", false, "Bring up the infrastructure without reconciling the BOM")
	cmd.Flags().BoolVar(&force, "force-full-redeploy", false,
		"Deploy everything declared, ignoring what the graph says is already deployed")
	cmd.Flags().BoolVarP(&verbose, "verbose", "V", false, "Show the underlying command output")
	cmd.Flags().StringVar(&substrate, "substrate", "",
		"How much substrate to run: none, core or full (default: what the environment recorded, else full)")
	return cmd
}

// recordSubstrate writes the mode into the environment manifest (NERD014
// SPEC002), creating the file with no instances when there is none -- the way
// the first `nsctl repo add` does.
func recordSubstrate(opts *Options, home, slug string, mode manifest.Substrate) error {
	m, err := openManifest(opts, home, slug)
	if err != nil {
		return err
	}
	if mode == manifest.SubstrateFull {
		// The default is recorded as absence, so a manifest that never chose
		// a mode and one that chose full read the same.
		m.Substrate = ""
	} else {
		m.Substrate = string(mode)
	}
	if err := m.Save(m.Path); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	return nil
}

func newEnvApplyCommand(opts *Options) *cobra.Command {
	var verbose, force, pull, prune bool
	var repo fromRepo
	var libs librarians

	cmd := &cobra.Command{
		Use:   "apply [name]",
		Short: "Reconcile an environment to its manifest",
		Long: `Deploys the environment substrate and everything the manifest declares.

The manifest at $HMD_HOME/environments/<name>.yaml is the desired state.
Editing it by hand and running this is the same operation as ` + "`nsctl repo add`" + `,
which edits that file and reconciles for you.

This is what ` + "`env start`" + ` runs at the end, so applying after an edit does not
need a restart. An environment with no manifest gets the substrate alone.

With --from-repo it re-reads a repository's lock first and reconciles the
environment to it. Unlike ` + "`env add`" + ` this is offline: an artifact nothing has
fetched fails naming ` + "`nsctl artifact pull`" + ` rather than reaching for the
network, because an apply that reaches the internet unasked is an apply that
behaves differently on an aeroplane. --pull fetches what is missing first, as a
declared step.

With no --profile it uses the profiles the environment recorded, and with no
--name the instance names it already bound -- so a re-apply never duplicates an
instance you renamed. Deactivating a profile leaves its instances declared
unless --prune; nothing is ever torn down on your behalf.`,
		Example: `  nsctl env apply
  nsctl env apply dev --from-repo .
  nsctl env apply dev --from-repo . --profile full --pull`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			if repo.requested() {
				if err := applyFromRepo(cmd, opts, &repo, &libs, home, name, pull, prune); err != nil {
					return err
				}
			}
			return environment.Apply(cmd.Context(), &environment.Options{
				Home: home, Lookup: opts.Lookup, Verbose: verbose, ForceRedeploy: force,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}, name)
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "V", false, "Show the underlying command output")
	cmd.Flags().BoolVar(&force, "force-full-redeploy", false,
		"Deploy everything declared, ignoring what the graph says is already deployed")
	cmd.Flags().BoolVar(&pull, "pull", false,
		"Fetch the artifacts the declaration names but this machine does not hold")
	cmd.Flags().BoolVar(&prune, "prune", false,
		"Undeclare instances this repository no longer asks for. Does not tear them down")
	repo.bind(cmd)
	libs.bind(cmd)
	return cmd
}

// applyFromRepo reconciles an environment manifest to what a repository's lock
// says, before the deploy that follows reads it.
//
// Offline unless asked otherwise. NERD005 SPEC004 refuses to let resolution
// reach the internet unasked, and this verb honours the same rule: the fetch is
// a step the user typed, never a side effect of applying.
func applyFromRepo(cmd *cobra.Command, opts *Options, repo *fromRepo, libs *librarians,
	home, name string, pull, prune bool) error {

	_, _, slug, err := resolveEnvSlug(opts, name)
	if err != nil {
		return err
	}
	existing, err := openManifest(opts, home, slug)
	if err != nil {
		return err
	}
	plan, err := planFromRepo(repo, existing)
	if err != nil {
		return err
	}
	if err := checkNamesAreUsed(repo, plan); err != nil {
		return err
	}

	if missing := plan.missingArtifacts(home); len(missing) > 0 {
		if !pull {
			return uncachedError(opts, home, missing, "--pull")
		}
		if err := pullMissing(cmd, opts, libs, home, missing); err != nil {
			return err
		}
	}

	added, kept := plan.apply(existing, prune)
	if problems := existing.Validate(opts.Lookup); len(problems) > 0 {
		return nserr.New(nserr.Usage,
			"the environment this repository describes is not valid:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	if err := existing.Save(existing.Path); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	plan.render(cmd, slug)
	if len(kept) > 0 {
		// These verbs edit a manifest; nothing tears an instance down on the
		// user's behalf, which is the promise `nsctl repo remove` already makes.
		fmt.Fprintf(cmd.ErrOrStderr(),
			"note: %s %s no longer asked for, left declared. Pass --prune to undeclare them\n",
			strings.Join(kept, ", "), plural(len(kept), "is", "are"))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Reconciled %s to %s (%d instance(s))\n\n",
		existing.Path, plan.Spec.Path, len(added))
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func newEnvStopCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "stop [name]",
		Short: "Stop an environment, leaving its state in place",
		Long: `Stops one environment. This is a stop, not a teardown: the k3s cluster is
stopped rather than deleted and its containers are stopped rather than removed,
so the next start restarts them in place and keeps the cluster's datastore.

The control plane keeps running.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			return environment.Stop(cmd.Context(), &environment.Options{
				Home: home, Lookup: opts.Lookup,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}, name)
		},
	}
}

func newEnvListCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:           "list",
		Short:         "List the registered environments",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, _, err := loadRegistry(opts)
			if err != nil {
				return err
			}
			renderEnvList(cmd.OutOrStdout(), cmd.ErrOrStderr(), reg, substrateOf(opts))
			return nil
		},
	}
}

func renderEnvList(out, errOut io.Writer, reg *registry.Registry, substrate func(string) manifest.Substrate) {
	names := reg.Names()
	if len(names) == 0 {
		// An empty result is worth a sentence, not a blank line.
		fmt.Fprintln(out, "No environments registered.")
		fmt.Fprintln(out, "Create one with `nsctl env add <name>`.")
		return
	}
	if reg.Synthesized {
		fmt.Fprintln(errOut, "note: no registry file yet; this view was derived from a pre-multi-environment install and has not been written")
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tACCOUNT\tSLOT\tCLUSTER\tSUBSTRATE\tBOOTSTRAPPED\tLAYOUT")
	for _, name := range names {
		e := reg.Environments[name]
		marker := name
		if name == reg.DefaultEnv {
			marker += " (default)"
		}
		layout := "current"
		if e.LegacyLayout {
			layout = "legacy"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			marker, e.AccountID, e.PortSlot, e.K3sCluster, substrate(e.Slug), yesNo(e.Bootstrapped()), layout)
	}
	w.Flush()
}

func newEnvStatusCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:           "status [name]",
		Short:         "Report one environment's containers and routes",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, _, err := loadRegistry(opts)
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			e, err := reg.Environment(name, opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			snap := reporter(opts, reg).EnvironmentStatus(cmd.Context(), e)
			renderEnvStatus(cmd.OutOrStdout(), snap)
			return nil
		},
	}
}

func renderEnvStatus(out io.Writer, s status.Environment) {
	fmt.Fprintf(out, "Environment: %s\n", s.Name)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  account\t%s\n", s.AccountID)
	fmt.Fprintf(w, "  deployment id\t%s\n", s.DeploymentID)
	// The mode is announced only when it is not the default, and the cluster
	// line only when the mode has one (NERD014 SPEC007).
	if s.Substrate == "" || s.Substrate == manifest.SubstrateFull {
		fmt.Fprintf(w, "  k3s cluster\t%s\n", s.K3sCluster)
	} else {
		fmt.Fprintf(w, "  substrate\t%s\n", s.Substrate)
	}
	fmt.Fprintf(w, "  compose project\t%s\n", s.ComposeProject)
	fmt.Fprintf(w, "  state dir\t%s\n", s.StateDir)
	fmt.Fprintf(w, "  bootstrapped\t%s\n", yesNo(s.Bootstrapped))
	if s.LegacyLayout {
		fmt.Fprintf(w, "  layout\tlegacy (shares the control plane's Postgres and graph)\n")
	}
	w.Flush()

	fmt.Fprintln(out, "\nContainers:")
	cw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, c := range s.Containers {
		fmt.Fprintf(cw, "  %s\t%s\t%s\n", c.Role, state(c), c.Name)
	}
	cw.Flush()

	fmt.Fprintln(out, "\nRoutes:")
	rw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, key := range s.RouteOrder {
		fmt.Fprintf(rw, "  %s\t%s\n", key, s.Routes[key])
	}
	rw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// state words a container's condition. "absent" is distinct from "stopped":
// a lazily-provisioned resource that was never created and one that `env stop`
// stopped need different words, or a default environment's missing graph reads
// as a failure.
func state(c status.Container) string {
	switch {
	case c.Running:
		return "running"
	case c.Absent:
		return "absent"
	default:
		return "stopped"
	}
}

func newEnvAddCommand(opts *Options) *cobra.Command {
	var makeDefault, noPull bool
	var repo fromRepo
	var libs librarians

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a new environment",
		Long: `Registers an environment and allocates its account, port slot and names.

This only writes the registry. The environment's database, cluster and routes
are created by ` + "`nsctl env start <name>`" + `, which is also what makes it usable.

A new environment is empty: it gets the substrate and nothing else. Declare
what should run on it with ` + "`nsctl repo add --env <name> <repo-class>`" + `.

With --from-repo it is not empty. The repository's ` + "`local`" + ` section and its
checked-in neuronsphere.lock say what to stand up alongside it, every activated
entry is declared at its pinned version, and the repository itself is declared
from its working tree -- which is what makes it the thing under test.

This is the one command that fetches an artifact without being asked, because
this is first start: there is no environment yet, so there is no offline
expectation to violate. --no-pull suppresses it.`,
		Example: `  nsctl env add dev
  nsctl env add dev --from-repo .
  nsctl env add dev --from-repo . --profile transforms
  nsctl env add dev --from-repo . --lean --name neptune-db=my-graph`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Read before the registry is written, so a broken declaration or a
			// missing lock costs nothing: an environment registered against a
			// repository that cannot be read is a slot allocated for nothing.
			var plan *repoPlan
			if repo.requested() {
				p, err := planFromRepo(&repo, nil)
				if err != nil {
					return err
				}
				if err := checkNamesAreUsed(&repo, p); err != nil {
					return err
				}
				plan = p
			}

			reg, home, err := loadRegistry(opts)
			if err != nil {
				return err
			}
			env, err := reg.NewEnvironment(home, args[0], opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if makeDefault {
				reg.DefaultEnv = env.Slug
			}
			if err := reg.Save(home); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}

			renderNewEnvironment(cmd, env)
			if plan == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "\nStart it with `nsctl env start %s`.\n", env.Slug)
				return nil
			}

			missing := plan.missingArtifacts(home)
			switch {
			case noPull && len(missing) > 0:
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: %d artifact(s) are not cached and --no-pull was given;"+
						" `nsctl env start %s` will refuse until they are\n", len(missing), env.Slug)
			case !noPull:
				if err := pullMissing(cmd, opts, &libs, home, missing); err != nil {
					return err
				}
			}

			m, err := openManifest(opts, home, env.Slug)
			if err != nil {
				return err
			}
			plan.apply(m, false)
			if problems := m.Validate(opts.Lookup); len(problems) > 0 {
				return nserr.New(nserr.Usage, "the environment this repository describes is not valid:\n  - %s",
					strings.Join(problems, "\n  - "))
			}
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}

			fmt.Fprintln(cmd.OutOrStdout())
			plan.render(cmd, env.Slug)
			fmt.Fprintf(cmd.OutOrStdout(), "\nDeclared in %s\n", m.Path)
			fmt.Fprintf(cmd.OutOrStdout(), "Start it with `nsctl env start %s`.\n", env.Slug)
			return nil
		},
	}
	cmd.Flags().BoolVar(&makeDefault, "default", false, "Make this the default environment")
	cmd.Flags().BoolVar(&noPull, "no-pull", false,
		"Do not fetch the artifacts the declaration names")
	repo.bind(cmd)
	libs.bind(cmd)
	return cmd
}

func newEnvDeleteCommand(opts *Options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Unregister an environment",
		Long: `Removes an environment from the registry, freeing its account and port slot.

This is a registry edit, not a teardown. Stop the environment first: an
unregistered environment whose containers are still running is worse than
either state alone, because nothing left knows how to address them.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, home, err := loadRegistry(opts)
			if err != nil {
				return err
			}
			env, err := reg.Environment(args[0], opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}

			// Refusing while it is running is the whole point of the check;
			// --yes does not override it, because the objection is not
			// "are you sure" but "this leaves containers nothing can address".
			if running := runningContainers(cmd.Context(), env); len(running) > 0 {
				return nserr.New(nserr.InUse,
					"environment %q still has %d running container(s): %s\nStop it first with `nsctl env stop %s`.",
					env.Slug, len(running), strings.Join(running, ", "), env.Slug)
			}
			if !yes {
				return nserr.New(nserr.Usage,
					"this unregisters %q and frees its account %s and port slot %d. Pass --yes to confirm.",
					env.Slug, env.AccountID, env.PortSlot)
			}

			if err := reg.RemoveEnvironment(env.Slug, opts.Lookup); err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if err := reg.Save(home); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Unregistered %s\n", env.Slug)
			fmt.Fprintf(cmd.ErrOrStderr(),
				"note: its state directory %s is left in place; remove it by hand if you want the disk back\n",
				env.StateDir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the removal")
	return cmd
}

// runningContainers is the environment's containers that are still up.
//
// Matched by the names the registry already records rather than by a pattern:
// a substring match on the slug would catch a container belonging to another
// environment whose name happens to contain it, and refusing to delete `dev`
// because `dev2` is running would be its own bug.
func runningContainers(ctx context.Context, env *registry.Environment) []string {
	d := container.New()
	candidates := []string{env.DBContainer, env.GraphContainer}
	for name := range d.ContainerNames(ctx) {
		// Floci names the k3s node after the cluster, with an account prefix
		// this does not need to reconstruct.
		if strings.HasSuffix(name, env.K3sCluster) {
			candidates = append(candidates, name)
		}
	}

	var running []string
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if up, err := d.Running(ctx, name); err == nil && up {
			running = append(running, name)
		}
	}
	sort.Strings(running)
	return running
}

// newEnvPurgeCommand is the teardown `env delete` refuses to be.
//
// SPEC001 made purge a verb rather than a flag on purpose: destroying an
// environment's cluster, database, graph and state deserves its own word, and
// `hmd neuronsphere down --purge` reads as a variation on `down` when it is
// nothing of the sort.
func newEnvPurgeCommand(opts *Options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "purge [name]",
		Short: "Destroy an environment: its cluster, database, graph and state",
		Long: `Tears an environment down and unregisters it. This is not a stop -- the k3s
cluster, the Postgres instance, the graph, their containers and volumes, the
nginx routes and the state directory all go, and none of it comes back.

With no name it purges every environment and the control plane with them,
leaving an HMD_HOME a fresh bootstrap can start from.

Resources Floci spawned are deleted through Floci before the control plane
stops, because a delete asked of a stopped Floci is a delete that did not
happen -- and its containers and volumes are then left behind with nothing
naming them.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			envOpts := &environment.Options{
				Home: home, Lookup: opts.Lookup,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}

			if len(args) == 1 {
				if !yes {
					return nserr.New(nserr.Usage,
						"this permanently destroys %q -- its cluster, database, graph and state. Pass --yes to confirm.", args[0])
				}
				return environment.Purge(cmd.Context(), envOpts, args[0])
			}

			if !yes && !confirmFullPurge(cmd, opts) {
				return nserr.New(nserr.Usage, "purge cancelled")
			}
			cpOpts := &controlplane.Options{
				Home: home, Lookup: opts.Lookup,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}
			names := floci.NamesFrom(opts.Lookup, "", "local")
			return environment.PurgeAll(cmd.Context(), envOpts, environment.ControlPlaneTeardown{
				// The control plane's graph, not an environment's: a different
				// instance under a different deployment id, and asking the
				// wrong helper finds nothing and reports no error.
				GraphIdentifier: controlplane.CPGraphIdentifier(names),
				Stop: func(ctx context.Context) error {
					return controlplane.Remove(ctx, cpOpts)
				},
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation")
	return cmd
}

// confirmFullPurge reproduces _confirm_full_purge: it names what is about to go
// before asking, because "are you sure" without a list is not informed consent.
func confirmFullPurge(cmd *cobra.Command, opts *Options) bool {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "\n  `nsctl env purge` with no name will permanently delete:")
	fmt.Fprintln(out, "    - the control plane's Floci, PostgreSQL and graph state")
	fmt.Fprintln(out, "    - ms-deployment's deployment graph for ALL environments")
	if reg, _, err := loadRegistry(opts); err == nil {
		if slugs := reg.Names(); len(slugs) > 0 {
			fmt.Fprintf(out, "    - all state for environment(s): %s\n", strings.Join(slugs, ", "))
		}
	}
	fmt.Fprintln(out, "\n  To purge a single environment instead, name it.")
	return tty.New(cmd.InOrStdin(), out).ConfirmWord("  Type 'yes' to continue", "yes")
}

// renderNewEnvironment reports an environment that has just been registered,
// with the derived names a later `docker ps` will show.
//
// Shared by `env add` and by `env start`'s first-run registration so the two
// read identically: the same act deserves the same words whichever verb
// performed it.
func renderNewEnvironment(cmd *cobra.Command, env *registry.Environment) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Registered environment %q\n", env.Slug)
	fmt.Fprintf(out, "  account:  %s\n", env.AccountID)
	// "reserves" rather than a bare list of ports: these are the slot's, held
	// for this environment whether or not anything ever listens on them, and the
	// summary at the end of a start is where a service that answers gets named
	// (NERD023 SPEC003).
	fmt.Fprintf(out, "  slot:     %d (reserves Floci %d, Trino %d)\n",
		env.PortSlot, env.FlociPort(), env.TrinoPort())
	fmt.Fprintf(out, "  cluster:  %s\n", env.K3sCluster)
	fmt.Fprintf(out, "  state:    %s\n", env.StateDir)
	// Before the choice is acted on, not after it fails.
	environment.WarnUndeployableSlug(func(format string, a ...any) {
		fmt.Fprintf(cmd.ErrOrStderr(), "\nwarning: "+format+"\n", a...)
	}, env.Slug)
}

// resolveStartTarget settles which environment `env start` is about, before
// anything is started.
//
// Two jobs, both of which have to happen ahead of the control plane. A fresh
// HMD_HOME has no environments at all, and registering the first one here is
// what makes `nsctl env start` a complete first run rather than a ten-minute
// bootstrap followed by "create one with `nsctl env add local`". And a name
// that does not match any registered environment is a typo whose cost should
// be a line of output, not that same ten minutes -- the refusal used to arrive
// after the bootstrap, because the registry was not consulted until the
// environment's own start began.
//
// It returns the environment's slug, which is what the manifest is filed under.
func resolveStartTarget(cmd *cobra.Command, opts *Options, home, name string) (string, error) {
	reg, err := registry.Load(home, opts.Lookup)
	if err != nil {
		return "", nserr.Wrap(nserr.Fail, err)
	}
	env, created, err := reg.EnsureFirstEnvironment(home, name, opts.Lookup)
	if err != nil {
		return "", nserr.Wrap(nserr.Usage, err)
	}
	if created {
		renderNewEnvironment(cmd, env)
		fmt.Fprintln(cmd.OutOrStdout())
		return env.Slug, nil
	}
	env, err = reg.Environment(name, opts.Lookup)
	if err != nil {
		return "", nserr.Wrap(nserr.Usage, err)
	}
	return env.Slug, nil
}
