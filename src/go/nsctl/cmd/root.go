// Package cmd holds nsctl's cobra adapters. All behaviour lives in internal/;
// a file here binds flags, resolves options and calls one function.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/spf13/cobra"
)

// Options is the resolved state every command reads.
//
// It is built per invocation and handed to each command's constructor rather
// than kept in a package global. SPEC004 requires this, and nsx found the
// reason the hard way: t.Setenv panics under t.Parallel(), so a command whose
// behaviour depends on the environment can only be tested in parallel if its
// state arrives as an argument.
type Options struct {
	// Version is the -ldflags-injected build version.
	Version string

	// Home is $HMD_HOME after --home and the environment have been applied.
	// Empty when neither supplied one; use RequireHome rather than reading it
	// directly, so the refusal is uniform.
	Home string

	// Lookup resolves an environment variable: the process environment layered
	// over $HMD_HOME/.config/hmd.env, process wins.
	Lookup hmdenv.Lookup
}

// RequireHome returns Home, or a usage error naming both ways to supply it.
//
// HMD_HOME is not defaulted. The Python CLI raises when it is unset
// (env_registry: "HMD_HOME is not set") and guessing here would put nsctl's
// containers, volumes and network under a name no other HMD tool computes.
func (o *Options) RequireHome() (string, error) {
	if o.Home == "" {
		// A refusal that states a precondition without naming the command that
		// satisfies it is incomplete (NERD023 SPEC002).
		return "", nserr.New(nserr.Usage,
			"HMD_HOME is not set. Export it, pass --home <path>, or run `nsctl quickstart`")
	}
	return o.Home, nil
}

// resolve fills in Home and Lookup. It is deliberately tolerant: a command that
// does not need an HMD_HOME (version, help) must still run without one, so a
// missing home is recorded rather than raised here.
func (o *Options) resolve(homeFlag string, process hmdenv.Lookup, warn func(string)) {
	home := homeFlag
	if home == "" {
		home = process("HMD_HOME")
	}
	o.Home = home

	file, err := hmdenv.Load(home)
	if err != nil {
		// The file exists but could not be read. Warn and carry on with the
		// process environment alone: hmd.env is configuration, not a
		// prerequisite, and failing here would break `nsctl env list` on an
		// install whose env file has one bad line.
		warn(fmt.Sprintf("could not load the HMD environment: %v", err))
		file = map[string]string{}
	}
	o.Lookup = hmdenv.Layered(process, file)
}

// noArgs rejects positional arguments with a usage exit code. cobra.NoArgs
// returns a plain error, which CodeOf would report as a runtime failure.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return nserr.New(nserr.Usage, "unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return nil
}

// NewRootCommand builds the built-in command tree. process supplies
// environment variables; pass nil for the real one.
//
// Declared plugins are never attached here. tools/docref renders the reference
// from this tree and NERD015 validates skills against it, and neither may
// pick up whatever a developer happens to have declared; NewRootCommandFor is
// what main runs. NERD018 SPEC004.
func NewRootCommand(version string, process hmdenv.Lookup) *cobra.Command {
	root, _, _ := newRoot(version, process)
	return root
}

// NewRootCommandFor is the tree main executes: the built-ins plus one command
// per plugin declared in the HMD_HOME that argv (or the environment) names.
// warn receives a line for a declaration that could not be attached.
func NewRootCommandFor(version string, process hmdenv.Lookup, argv []string, warn io.Writer) *cobra.Command {
	root, opts, proc := newRoot(version, process)
	attachPlugins(root, opts, proc, argv, warn)
	return root
}

func newRoot(version string, process hmdenv.Lookup) (*cobra.Command, *Options, hmdenv.Lookup) {
	if process == nil {
		process = os.Getenv
	}
	opts := &Options{Version: version}
	var homeFlag string

	root := &cobra.Command{
		Use:   "nsctl",
		Short: "Run the local NeuronSphere",
		Long: `nsctl runs the local NeuronSphere: one control plane per HMD_HOME and the
environment substrate your deployments sit on. Docker is the only host
prerequisite.

New here? Run "nsctl quickstart". It checks the host, creates your first
environment and offers to adopt your own repository, naming each command before
it runs it.

It ships the control plane and the substrate -- a cluster, a database, and the
External Secrets operator a cloud chart's secrets resolve through. Everything
above that is a RepoClass you add.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          noArgs,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			opts.resolve(homeFlag, process, func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
			})
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	root.Version = version
	root.SetVersionTemplate("nsctl {{.Version}}\n")

	// A bad flag is a usage error, not a runtime one.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return nserr.Wrap(nserr.Usage, err)
	})

	root.PersistentFlags().StringVar(&homeFlag, "home", "",
		"Path to HMD_HOME (overrides $HMD_HOME)")

	// Listed in the order they are added rather than alphabetically. Within a
	// group the useful order is not the alphabet: "Start here" has to begin with
	// quickstart, which sorts third. Package-level in cobra, and set here
	// because this is the only place that builds a tree.
	cobra.EnableCommandSorting = false

	// Grouped so that --help leads with what a first run needs.
	//
	// Sixteen commands in one alphabetical block put `authd` -- a mock identity
	// provider -- beside `env` as an equal, and named no first command at all.
	// This is presentation only: nothing is renamed or moved, and every existing
	// invocation stays valid. NERD023 SPEC002.
	root.AddGroup(
		&cobra.Group{ID: groupStart, Title: "Start here:"},
		&cobra.Group{ID: groupAuthor, Title: "Author a repo class, stack or plugin:"},
		&cobra.Group{ID: groupSupply, Title: "Artifacts, locks and cloud BOMs:"},
		&cobra.Group{ID: groupAuth, Title: "Identity:"},
	)
	assign := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	assign(groupStart,
		newQuickstartCommand(opts),
		newDoctorCommand(opts),
		newEnvCommand(opts),
		newRepoCommand(opts),
		newVersionCommand(opts),
	)
	assign(groupAuthor,
		newRepoClassCommand(opts),
		newStackCommand(opts),
		newPluginCommand(opts),
		newAgentCommand(),
	)
	assign(groupSupply,
		newArtifactCommand(opts),
		newLockCommand(opts),
		newBOMCommand(opts),
		newControlPlaneCommand(opts),
	)
	assign(groupAuth,
		newLoginCommand(opts),
		newLogoutCommand(opts),
		newWhoamiCommand(opts),
		newAuthdCommand(opts),
	)

	return root, opts, process
}

// Command groups on the root. Plugin commands add their own (NERD018 SPEC004).
const (
	groupStart  = "start"
	groupAuthor = "author"
	groupSupply = "supply"
	groupAuth   = "auth"
)

// Execute runs the root command and exits with the error's code.
//
// Errors are printed here and only here -- every RunE returns rather than
// prints, and both SilenceUsage and SilenceErrors are set, because usage text
// helps with a flag mistake and is noise after a failed API call.
func Execute(version string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := NewRootCommandFor(version, nil, os.Args[1:], os.Stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		// A plugin's exit status has already spoken for itself; nsctl adds
		// nothing to it. NERD018 SPEC005.
		if !nserr.IsSilent(err) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(int(nserr.CodeOf(err)))
	}
}
