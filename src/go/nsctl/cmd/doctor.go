package cmd

import (
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/controlplane"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/spf13/cobra"
)

// newDoctorCommand reports whether nsctl can work on this machine.
//
// It exists because the question "is the container engine running" had two
// different answers here, and nothing printed either of them: nsctl's preflight
// asked the docker CLI, and its work went through the Engine API, which
// resolves a different endpoint. The checks are the start preflight's own, so
// what this prints is what a start would find (NERD021 SPEC009).
//
// Read-only: it creates, starts, pulls and removes nothing, so it is safe on a
// machine with a running platform.
func newDoctorCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that nsctl can reach the container engine and the host is ready",
		Long: `Reports what nsctl resolved and what the container engine says about itself.

Prints the endpoint nsctl will use and where that came from, the engine's
version and size, whether host paths are visible to it, and whether the local
host names resolve. It changes nothing.

With a home set it also reports each environment's substrate: whether the
service versions Floci is running are the ones this nsctl resolves, and whether
their routes answer. A stale one warns and names the restart that refreshes it;
a service returning 5xx where a healthy one answers 404 fails, and says what
that means. Nothing here needs a credential, and nothing here is fixed by
signing in.

Exits 2 when something needs fixing, 0 otherwise -- including when checks only
warn.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cp := &controlplane.Options{
				Home:    opts.Home,
				Lookup:  opts.Lookup,
				Version: opts.Version,
				Out:     cmd.OutOrStdout(),
				Err:     cmd.ErrOrStderr(),
			}
			dopts := controlplane.DoctorOptions(cp)
			// Only when a home is known. Substrate currency is a property of
			// the environments in one, and doctor must still answer the
			// engine question on a machine that has none (NERD024 SPEC006).
			if home, err := opts.RequireHome(); err == nil {
				dopts.Environments = environment.DoctorChecks(&environment.Options{
					Home: home, Lookup: opts.Lookup,
					Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
				})
			}
			checks := doctor.Run(cmd.Context(), dopts)
			doctor.Report(cmd.OutOrStdout(), checks)
			if doctor.Failed(checks) {
				return nserr.New(nserr.Usage, "%s", doctor.FirstFailure(checks))
			}
			return nil
		},
	}
}
