package cmd

import (
	"bytes"
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/quickstart"
)

// newQuickstartCommand is the guided first run (NERD023 SPEC001).
//
// The flow itself is internal/quickstart. What this file supplies is the one
// thing only cmd can: a way to run an nsctl invocation. Every step goes through
// it, so the command the wizard prints and the command it runs are the same
// argv -- there is no second path into the platform to drift from the
// documented one.
func newQuickstartCommand(opts *Options) *cobra.Command {
	var yes bool
	var repo string
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "Guided first run: check the host, start an environment, adopt a repository",
		Long: `Walks a new installation through what it needs, in order: the host checks,
where local state lives, a first environment, something deployed into it, and
your own repository.

Every step names the command it runs before running it, so the session is a
transcript you can repeat by hand, and every step can be declined. It creates
nothing the named commands would not create, and never purges, deletes or
redeploys.

It does not edit your shell configuration. When HMD_HOME is unset it proposes a
path, prints the export line for you to keep, and uses --home for the rest of
the run.

With stdin closed -- every CI job -- it prints the ordered list of commands and
runs nothing.`,
		Example: `  nsctl quickstart
  nsctl quickstart --repo ~/src/my-service`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			userHome, err := os.UserHomeDir()
			if err != nil {
				// Not fatal: the flow only needs it to propose a path, and a
				// user who types an absolute one never notices.
				userHome = ""
			}
			return quickstart.Run(cmd.Context(), quickstart.Options{
				Home:     opts.Home,
				Version:  opts.Version,
				In:       cmd.InOrStdin(),
				Out:      cmd.OutOrStdout(),
				Err:      cmd.ErrOrStderr(),
				UserHome: userHome,
				Yes:      yes,
				Repo:     repo,
				Exec:     execNsctl(opts, cmd, false),
				Capture:  captureNsctl(opts, cmd),
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Take the default answer to every question")
	cmd.Flags().StringVar(&repo, "repo", "", "A repository to offer adopting, instead of asking for one")
	return cmd
}

// execNsctl runs one invocation against a freshly built command tree.
//
// A fresh root per invocation rather than a reused one: cobra keeps parsed flag
// values on the command, so a second run through the same tree would inherit the
// first run's flags. Plugins are deliberately not attached -- the wizard only
// ever names built-ins, and a declared plugin sharing a noun would change what a
// printed command means.
func execNsctl(opts *Options, parent *cobra.Command, quiet bool) func(context.Context, ...string) error {
	return func(ctx context.Context, argv ...string) error {
		root := NewRootCommand(opts.Version, hmdenv.Lookup(os.Getenv))
		root.SetArgs(argv)
		root.SetIn(parent.InOrStdin())
		if quiet {
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
		} else {
			root.SetOut(parent.OutOrStdout())
			root.SetErr(parent.ErrOrStderr())
		}
		return root.ExecuteContext(ctx)
	}
}

// captureNsctl runs one invocation and returns its output, for the read-only
// questions the flow asks itself -- whether a stack reference resolves, say.
func captureNsctl(opts *Options, parent *cobra.Command) func(context.Context, ...string) (string, error) {
	return func(ctx context.Context, argv ...string) (string, error) {
		var out bytes.Buffer
		root := NewRootCommand(opts.Version, hmdenv.Lookup(os.Getenv))
		root.SetArgs(argv)
		root.SetIn(parent.InOrStdin())
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		err := root.ExecuteContext(ctx)
		return out.String(), err
	}
}
