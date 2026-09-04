package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCommand reports the binary version.
//
// SPEC013 also asks it to report the ms-deployment version when a control
// plane is reachable. That waits for the ms-deployment client (Phase 2a); a
// version command that cannot run without a platform would be the wrong shape
// for the one command you run to check the install.
func newVersionCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:           "version",
		Short:         "Print the nsctl version",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "nsctl %s\n", opts.Version)
			return nil
		},
	}
}
