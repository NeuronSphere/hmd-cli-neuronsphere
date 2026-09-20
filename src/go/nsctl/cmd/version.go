package cmd

import (
	"fmt"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/spf13/cobra"
)

// newVersionCommand reports the binary version and, per SPEC013, the version of
// the ms-deployment it is talking to when a control plane is reachable.
//
// The second line is best-effort and silent when it fails. This is the command
// you run to check an install, so it has to answer on a machine with no
// platform at all; the probe carries its own two-second budget rather than the
// client's, and a control plane that is down simply does not appear.
func newVersionCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:           "version",
		Short:         "Print the nsctl version",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "nsctl %s\n", opts.Version)

			url := environment.MSDeploymentURL(opts.Lookup)
			if version, ok := msdeploy.New(url).ServiceVersion(cmd.Context()); ok {
				fmt.Fprintf(out, "%s %s (%s)\n", floci.MSDeploymentImageClass, version, url)
			}
			return nil
		},
	}
}
