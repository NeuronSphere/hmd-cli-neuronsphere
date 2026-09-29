package cmd

import (
	"bytes"
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/quickstart"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
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
	var stack string
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
				Stack:    stack,
				// Reading the registry, never writing it: the flow uses this
				// only to decide whether a name has to be created before it can
				// be started. Creating it is `env add`, run through Exec like
				// every other step.
				Environments: func(home string) ([]string, error) {
					reg, err := registry.Load(home, opts.Lookup)
					if err != nil {
						return nil, err
					}
					return reg.Names(), nil
				},
				Exec:    execNsctl(opts, cmd, false),
				Capture: captureNsctl(opts, cmd),
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Take the default answer to every question")
	cmd.Flags().StringVar(&repo, "repo", "", "A repository to offer adopting, instead of asking for one")
	cmd.Flags().StringVar(&stack, "stack", "", "Deploy this stack into the new environment; bare --stack means "+quickstart.DefaultStack)
	// Naming a stack is the request, so --yes deploys it. Without this flag
	// --yes still declines, because a scripted walk-through must not become a
	// deploy nobody asked for.
	cmd.Flags().Lookup("stack").NoOptDefVal = quickstart.DefaultStack
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
