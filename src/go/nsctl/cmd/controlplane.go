package cmd

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/controlplane"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/cpext"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/status"
	"github.com/spf13/cobra"
)

func newControlPlaneCommand(opts *Options) *cobra.Command {
	cp := &cobra.Command{
		Use:     "control-plane",
		Aliases: []string{"cp"},
		Short:   "Work with the shared control plane",
		Long: `The control plane is shared: one per HMD_HOME, serving every environment.

It is given its own verb because stopping it takes every environment's emulated
AWS with it -- their Lambdas, gateways, buckets and secrets, not just the
deployment graph's availability. The Python CLI has no independent lifecycle
for it at all; it is only ever stopped as a side effect of a bare
` + "`hmd neuronsphere down`" + `.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cp.AddCommand(
		newControlPlaneStartCommand(opts),
		newControlPlaneStopCommand(opts),
		newControlPlaneStatusCommand(opts),
		newControlPlaneApplyCommand(opts),
		newControlPlaneRepoCommand(opts),
		newControlPlaneResetCommand(opts),
	)
	return cp
}

func newControlPlaneResetCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reset [repo-class...]",
		Short: "Redeploy the foundation Lambdas with freshly-resolved versions",
		Long: `Redeploys hmd-ms-naming, hmd-ms-artifact-lib and hmd-ms-deployment (or only
the ones named) on a control plane that has already bootstrapped, resolving
each one's version the same way a first bootstrap does.

It touches nothing else: no VPC, no control-plane database, no graph, no
environment's k3s/Postgres/graph state. Use this instead of a stop/start when a
foundation service needs to pick up a newer image without a full re-bootstrap
-- stop/start does not do that for these three, since they are only ever
deployed from inside Bootstrap, which runs exactly once.`,
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			return controlplane.Reset(cmd.Context(), &controlplane.Options{
				Home: home, Lookup: opts.Lookup, Version: opts.Version,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}, args...)
		},
	}
	return cmd
}

func newControlPlaneStartCommand(opts *Options) *cobra.Command {
	var upgrade, verbose bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the shared control plane",
		Long: `Starts the control plane and leaves it running.

Idempotent: containers whose configuration has not changed are started in
place rather than rebuilt.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			return controlplane.Start(cmd.Context(), &controlplane.Options{
				Home: home, Lookup: opts.Lookup, Version: opts.Version,
				Upgrade: upgrade, Verbose: verbose,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().BoolVar(&upgrade, "upgrade", false, "Pull the latest images before starting")
	cmd.Flags().BoolVarP(&verbose, "verbose", "V", false, "Show the underlying command output")
	return cmd
}

func newControlPlaneApplyCommand(opts *Options) *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Start the extensions the control-plane manifest declares",
		Long: `Converges the running control plane to $HMD_HOME/.config/control-plane.yaml.

` + "`control-plane start`" + ` applies too -- an extension is up whenever the control
plane is. This is the fast path: it starts, restarts and reconfigures the
declared extensions without the Floci health wait, the bootstrap or the route
rewrite a full start does.

Editing the manifest by hand and running this is the same operation as using
` + "`nsctl control-plane repo`" + `.

An extension that fails is reported and skipped; nothing else is affected. The
exit status is non-zero when any did, so a script notices.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			return controlplane.Apply(cmd.Context(), &controlplane.Options{
				Home: home, Lookup: opts.Lookup, Version: opts.Version, Verbose: verbose,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "V", false, "Show the underlying command output")
	return cmd
}

func newControlPlaneStopCommand(opts *Options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the shared control plane",
		Long: `Stops the control plane. This is the only command that does.

It refuses while any environment is still running, because stopping it takes
every environment's emulated AWS with it -- their Lambdas, gateways, buckets and
secrets, not just the deployment graph's availability.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			reg, _, err := loadRegistry(opts)
			if err != nil {
				return err
			}
			snap := reporter(opts, reg).ControlPlaneStatus(cmd.Context(), reg)
			if len(snap.RunningEnvs) > 0 && !force {
				return nserr.New(nserr.InUse,
					"%s still running. Stopping the control plane would take their emulated AWS with it.\nStop them first with `nsctl env stop <name>`, or pass --force.",
					pluralEnvs(snap.RunningEnvs))
			}
			return controlplane.Stop(cmd.Context(), &controlplane.Options{
				Home: home, Lookup: opts.Lookup,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Stop even while environments are running")
	return cmd
}

// pluralEnvs words the refusal so it reads correctly for one or many.
func pluralEnvs(names []string) string {
	if len(names) == 1 {
		return "Environment " + names[0] + " is"
	}
	return "Environments " + strings.Join(names, ", ") + " are"
}

func newControlPlaneStatusCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:           "status",
		Short:         "Report the control plane's containers, health and environments",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, home, err := loadRegistry(opts)
			if err != nil {
				return err
			}
			rep := reporter(opts, reg)
			snap := rep.ControlPlaneStatus(cmd.Context(), reg)
			// Best effort: an unreadable or invalid manifest is worth a note
			// rather than a failed status, which is the command a user runs to
			// find out what is wrong in the first place.
			cpOpts := &controlplane.Options{Home: home, Lookup: opts.Lookup,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
			project, perr := controlplane.Project(cpOpts, reg, "")
			if perr == nil {
				exts, eerr := controlplane.ResolveExtensions(cmd.Context(), cpOpts, reg, project)
				if eerr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", eerr)
				}
				snap.Extensions = rep.ExtensionStatus(cmd.Context(), extensionQueries(exts, reg.ControlPlane.ComposeProject))
				snap.Handback, snap.HandbackPath = handbackVars(home, exts, cmd.ErrOrStderr())
			}
			renderControlPlaneStatus(cmd.OutOrStdout(), home, snap)
			return nil
		},
	}
}

func renderControlPlaneStatus(out io.Writer, home string, s status.ControlPlane) {
	fmt.Fprintf(out, "Control plane (%s)\n", home)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  bootstrapped\t%s\n", yesNo(s.Bootstrapped))
	fmt.Fprintf(w, "  compose project\t%s\n", s.ComposeProject)
	fmt.Fprintf(w, "  network\t%s (%s)\n", s.Network, presence(s.NetworkExists))
	fmt.Fprintf(w, "  floci data\t%s\n", s.FlociDataDir)
	fmt.Fprintf(w, "  ms-deployment\t%s\n", reachable(s.MSDeploymentUp))
	w.Flush()

	fmt.Fprintln(out, "\nContainers:")
	cw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, c := range s.Containers {
		fmt.Fprintf(cw, "  %s\t%s\t%s\n", c.Role, state(c), c.Name)
	}
	cw.Flush()

	if len(s.Extensions) > 0 {
		fmt.Fprintln(out, "\nExtensions:")
		ew := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, e := range s.Extensions {
			fmt.Fprintf(ew, "  %s\t%s\t%s\t%s\t%s\n",
				e.Instance, e.RepoClass, extensionVersion(e), extensionState(e), orDash(e.URL))
		}
		ew.Flush()
		for _, e := range s.Extensions {
			if e.Problem != "" {
				fmt.Fprintf(out, "  %s: %s\n", e.Instance, e.Problem)
			}
		}
	}

	if len(s.Handback) > 0 {
		fmt.Fprintf(out, "\nHandback (%s):\n", s.HandbackPath)
		hw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, v := range s.Handback {
			fmt.Fprintf(hw, "  %s\t%s\t%s\t%s\n", v.Name, v.Merge, v.State, v.Owner)
		}
		hw.Flush()
	}

	fmt.Fprintln(out, "\nEnvironments:")
	if len(s.RunningEnvs) == 0 && len(s.StoppedEnvs) == 0 {
		fmt.Fprintln(out, "  (none registered)")
		return
	}
	if len(s.RunningEnvs) > 0 {
		fmt.Fprintf(out, "  running  %s\n", strings.Join(s.RunningEnvs, ", "))
	}
	if len(s.StoppedEnvs) > 0 {
		fmt.Fprintf(out, "  stopped  %s\n", strings.Join(s.StoppedEnvs, ", "))
	}
}

// extensionQueries maps resolved extensions onto the plain data status wants,
// naming each container the same way compose does.
func extensionQueries(exts []cpext.Extension, project string) []status.ExtensionQuery {
	queries := make([]status.ExtensionQuery, 0, len(exts))
	for _, e := range exts {
		q := status.ExtensionQuery{
			Instance: e.Instance, RepoClass: e.RepoClass,
			Version: e.Version, Source: string(e.Source), URL: e.URL,
		}
		if e.Failed() {
			q.Problem = e.Err.Error()
		}
		for _, s := range e.Services {
			q.Containers = append(q.Containers, s.Name(project))
		}
		queries = append(queries, q)
	}
	sort.Slice(queries, func(i, j int) bool { return queries[i].Instance < queries[j].Instance })
	return queries
}

// extensionVersion carries the source alongside the version, because
// "0.1.4 from a working tree" and "0.1.4 from a bundled tree" are different
// facts and the difference decides what a user does next.
func extensionVersion(e status.Extension) string {
	if e.Version == "" {
		return "-"
	}
	if e.Source == "" {
		return e.Version
	}
	return e.Version + " (" + e.Source + ")"
}

func extensionState(e status.Extension) string {
	switch {
	case e.Problem != "":
		return "unresolved"
	case e.Containers == 0:
		return "declared"
	case e.Running == e.Containers:
		return "running"
	case e.Running == 0:
		return "stopped"
	default:
		return fmt.Sprintf("%d/%d running", e.Running, e.Containers)
	}
}

func presence(exists bool) string {
	if exists {
		return "present"
	}
	return "absent"
}

func reachable(up bool) string {
	if up {
		return "reachable at " + status.MSDeploymentURL()
	}
	return "not answering at " + status.MSDeploymentURL()
}

// handbackVars reports what the declared extensions contribute to hmd.env and
// what became of each (SPEC010, SPEC012).
//
// Read-only: `status` says what the file holds and never writes it, so running
// status can never be the thing that changed the environment.
func handbackVars(home string, exts []cpext.Extension, errOut io.Writer) ([]status.HandbackVar, string) {
	vars, err := cpext.Handback(exts)
	if err != nil {
		fmt.Fprintf(errOut, "warning: %v\n", err)
		return nil, ""
	}
	if len(vars) == 0 {
		return nil, ""
	}
	managed, user, err := hmdenv.Split(home)
	if err != nil {
		fmt.Fprintf(errOut, "warning: %v\n", err)
		return nil, ""
	}

	out := make([]status.HandbackVar, 0, len(vars))
	for _, v := range vars {
		name := v.Name
		if v.Key != "" {
			name += "." + v.Key
		}
		out = append(out, status.HandbackVar{
			Name: name, Owner: v.Source, Merge: string(v.Merge),
			State: handbackState(v, managed, user),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, hmdenv.Path(home)
}

// handbackState is what became of one contribution: the block carries it, a
// value set outside the block won instead, or the next apply will write it.
func handbackState(v hmdenv.Var, managed, user map[string]string) string {
	if _, ok := managed[v.Name]; ok {
		return "managed"
	}
	if _, ok := user[v.Name]; ok {
		return "yours"
	}
	return "pending"
}
