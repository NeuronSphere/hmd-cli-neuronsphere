package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

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
	cp.AddCommand(newControlPlaneStatusCommand(opts))
	return cp
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
			snap := reporter(opts).ControlPlaneStatus(cmd.Context(), reg)
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

func presence(exists bool) string {
	if exists {
		return "present"
	}
	return "absent"
}

func reachable(up bool) string {
	if up {
		return "reachable at " + status.MSDeploymentURL
	}
	return "not answering at " + status.MSDeploymentURL
}
