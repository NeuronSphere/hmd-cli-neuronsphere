package cmd

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/controlplane"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/status"
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
func reporter(opts *Options) *status.Reporter {
	return &status.Reporter{
		Docker: container.New(),
		Probe:  status.HTTPProber(3 * time.Second),
		Lookup: opts.Lookup,
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
		newEnvStartCommand(opts),
		newEnvStopCommand(opts),
		newEnvApplyCommand(opts),
	)
	return env
}

func newEnvStartCommand(opts *Options) *cobra.Command {
	var noDeploy, verbose, force bool
	cmd := &cobra.Command{
		Use:   "start [name]",
		Short: "Start an environment's infrastructure",
		Long: `Starts one environment: its database, graph, k3s cluster and routes.

The control plane is started first if it is down, and is left running
afterwards -- stopping it would take every other environment's emulated AWS
with it.`,
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
			// `env start` starts the control plane implicitly if it is down,
			// and then leaves it running.
			if err := controlplane.Start(cmd.Context(), &controlplane.Options{
				Home: home, Lookup: opts.Lookup, Verbose: verbose,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
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
	return cmd
}

func newEnvApplyCommand(opts *Options) *cobra.Command {
	var verbose, force bool
	cmd := &cobra.Command{
		Use:   "apply [name]",
		Short: "Reconcile an environment to its manifest",
		Long: `Deploys the environment substrate and everything the manifest declares.

The manifest at $HMD_HOME/environments/<name>.yaml is the desired state.
Editing it by hand and running this is the same operation as ` + "`nsctl repo add`" + `,
which edits that file and reconciles for you.

This is what ` + "`env start`" + ` runs at the end, so applying after an edit does not
need a restart. An environment with no manifest gets the substrate alone.`,
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
			return environment.Apply(cmd.Context(), &environment.Options{
				Home: home, Lookup: opts.Lookup, Verbose: verbose, ForceRedeploy: force,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}, name)
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "V", false, "Show the underlying command output")
	cmd.Flags().BoolVar(&force, "force-full-redeploy", false,
		"Deploy everything declared, ignoring what the graph says is already deployed")
	return cmd
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
			renderEnvList(cmd.OutOrStdout(), cmd.ErrOrStderr(), reg)
			return nil
		},
	}
}

func renderEnvList(out, errOut io.Writer, reg *registry.Registry) {
	names := reg.Names()
	if len(names) == 0 {
		// An empty result is worth a sentence, not a blank line.
		fmt.Fprintln(out, "No environments registered.")
		fmt.Fprintln(out, "Create one with `nsctl env add --name <name>`.")
		return
	}
	if reg.Synthesized {
		fmt.Fprintln(errOut, "note: no registry file yet; this view was derived from a pre-multi-environment install and has not been written")
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tACCOUNT\tSLOT\tCLUSTER\tBOOTSTRAPPED\tLAYOUT")
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
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
			marker, e.AccountID, e.PortSlot, e.K3sCluster, yesNo(e.Bootstrapped()), layout)
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
			snap := reporter(opts).EnvironmentStatus(cmd.Context(), e)
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
	fmt.Fprintf(w, "  k3s cluster\t%s\n", s.K3sCluster)
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
