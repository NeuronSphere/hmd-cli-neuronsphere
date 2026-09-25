package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/controlplane"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/pgupgrade"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tty"
	"github.com/spf13/cobra"
)

// newDBCommand groups the operations on the local databases themselves, as
// opposed to the environments that use them.
//
// Beside `doctor` rather than under `control-plane` or `env`: this is the
// repair for what doctor and the start pre-flight report, it sweeps every
// environment's databases rather than one control plane's, and it requires
// everything stopped -- the opposite of what the env verbs need.
func newDBCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Work on the local PostgreSQL data directories",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newDBUpgradeCommand(opts))
	return cmd
}

func newDBUpgradeCommand(opts *Options) *cobra.Command {
	var (
		dryRun   bool
		check    bool
		yes      bool
		force    bool
		keepDump bool
		volumes  []string
		dumpImg  string
	)
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Migrate PostgreSQL data directories to the configured major version",
		Long: `Migrates the local databases across a PostgreSQL major version.

Floci recreates a database's container from the currently configured image on
every start while reusing the instance's volume, so a major version bump leaves
an old data directory under a new binary. Postgres refuses that outright, and
Floci still reports the instance available -- so the first symptom is whatever
connects next failing. ` + "`nsctl env start`" + ` refuses rather than let that happen,
and this is the repair it names.

For each affected volume: the old data directory is copied to a backup volume,
dumped with pg_dumpall using the image that wrote it, cleared so the new image
initialises it, and the dump replayed. Nothing is cleared until the dump has
been read back and confirmed complete, and the databases and roles found before
the dump are looked for again afterwards.

Everything must be stopped first. A container running on a volume this would
rewrite is refused absolutely; a running Floci is refused because it restarts
databases on demand, and only that refusal takes --force.

The backup volume is kept. It holds an old-major data directory, so the new
image cannot read it -- it is there to roll back to, by hand, if the migration
is not what you wanted.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBUpgrade(cmd, opts, pgupgrade.Options{
				Only:      volumes,
				DumpImage: dumpImg,
				KeepDump:  keepDump,
				Force:     force,
			}, dryRun || check, yes)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be migrated and stop")
	cmd.Flags().BoolVar(&check, "check", false, "")
	// The Python spells this --check, and that is what is in shell history and
	// in the refusal people arrive here from. Hidden rather than dropped: it
	// costs nothing and the alternative is a usage error at the worst moment.
	_ = cmd.Flags().MarkHidden("check")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation")
	cmd.Flags().BoolVar(&force, "force", false,
		"Proceed although Floci is running. Cannot bypass a container running on a volume being migrated")
	cmd.Flags().BoolVar(&keepDump, "keep-dump", false,
		"Leave the dump volume in place after a verified migration")
	cmd.Flags().StringArrayVar(&volumes, "volume", nil,
		"Migrate only this volume. Repeatable")
	cmd.Flags().StringVar(&dumpImg, "dump-image", "",
		"Image used to read the old data directory (default postgres:<old major>-alpine)")
	return cmd
}

func runDBUpgrade(cmd *cobra.Command, opts *Options, up pgupgrade.Options, dryRun, yes bool) error {
	ctx := cmd.Context()
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()

	home, err := opts.RequireHome()
	if err != nil {
		return err
	}
	reg, err := registry.Load(home, opts.Lookup)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	// The image off the parsed compose project, exactly as the start pre-flight
	// reads it: the value lives behind nested ${VAR:-default} expansions, and a
	// reconstruction that disagreed would migrate towards the wrong version.
	cp := &controlplane.Options{
		Home: home, Lookup: opts.Lookup, Version: opts.Version,
		Out: out, Err: errOut,
	}
	project, err := controlplane.Project(cp, reg, "")
	if err != nil {
		return err
	}
	up.Image = controlplane.ConfiguredPostgresImage(project)
	up.FlociDataDir = reg.ControlPlane.FlociDataDir
	if up.Image == "" {
		return nserr.New(nserr.Fail,
			"the control-plane compose project declares no PostgreSQL image, so there is nothing to migrate towards")
	}

	docker := container.New()
	if err := docker.Available(ctx); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	plan, err := pgupgrade.BuildPlan(ctx, docker, up)
	if err != nil {
		return err
	}
	if plan.Empty() {
		fmt.Fprintf(out, "Every data directory can be read by %s. Nothing to migrate.\n", plan.Image)
		return nil
	}

	renderUpgradePlan(out, plan)
	if dryRun {
		fmt.Fprintln(out, "\nNothing was changed. Run `nsctl db upgrade` to perform it.")
		return nil
	}

	if err := pgupgrade.Refuse(pgupgrade.Gate(ctx, docker, plan), up.Force, stopTheseFirst(reg)); err != nil {
		return err
	}
	if !yes && !confirmUpgrade(cmd, plan) {
		return nserr.New(nserr.Usage, "upgrade cancelled")
	}

	e := &pgupgrade.Executor{
		Docker:   docker,
		Timeouts: pgupgrade.DefaultTimeouts(),
		KeepDump: up.KeepDump,
		Progress: func(volume string, step pgupgrade.Step) {
			fmt.Fprintf(out, "  %s: %s\n", volume, step.What)
		},
		Warn: func(format string, args ...any) {
			fmt.Fprintf(errOut, "warning: "+format+"\n", args...)
		},
	}
	fmt.Fprintln(out)
	if err := e.Run(ctx, plan); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	fmt.Fprintf(out, "\nMigrated %s to PostgreSQL %s.\n",
		plural(len(plan.Volumes), "1 data directory", fmt.Sprintf("%d data directories", len(plan.Volumes))),
		plan.Volumes[0].To)
	fmt.Fprintln(out, "The old data is kept in:")
	for _, v := range plan.Volumes {
		fmt.Fprintf(out, "  %s\n", v.Backup)
	}
	fmt.Fprintln(out, "\nStart the platform with `nsctl env start`. Once it is healthy, the backups can go:")
	for _, v := range plan.Volumes {
		fmt.Fprintf(out, "  docker volume rm %s\n", v.Backup)
	}
	return nil
}

// renderUpgradePlan prints what will happen before it happens. A destructive
// command that describes itself only in the past tense is one whose --dry-run
// is the only safe way to read it.
func renderUpgradePlan(out io.Writer, plan pgupgrade.Plan) {
	fmt.Fprintf(out, "%s configured: PostgreSQL %s\n\n", plan.Image, plan.Volumes[0].To)
	for _, v := range plan.Volumes {
		fmt.Fprintf(out, "%s  (PostgreSQL %s -> %s)\n", v.Volume, v.From, v.To)
		if v.Resume != "" {
			fmt.Fprintf(out, "  resuming a migration that reached %q\n", v.Resume)
		}
		for _, s := range v.Steps {
			marker := " "
			if s.Kind.Destructive() {
				marker = "!"
			}
			fmt.Fprintf(out, "  %s %s\n", marker, s.What)
		}
		fmt.Fprintln(out)
	}
}

// confirmUpgrade asks, unless there is nobody to ask.
//
// A non-terminal stdin is refused rather than defaulted either way: this
// rewrites databases, and an unattended run that meant to pass --yes should
// fail loudly rather than proceed on an assumption.
func confirmUpgrade(cmd *cobra.Command, plan pgupgrade.Plan) bool {
	out := cmd.OutOrStdout()
	if !stdinIsTerminal(cmd) {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"error: this rewrites the data directories above. Pass --yes to confirm.")
		return false
	}
	return tty.New(cmd.InOrStdin(), out).ConfirmWord(
		fmt.Sprintf("  Type 'yes' to migrate %d data director%s",
			len(plan.Volumes), plural(len(plan.Volumes), "y", "ies")), "yes")
}

// stopTheseFirst names what the user has to stop, which the refusal in
// pgupgrade has no business resolving for itself.
func stopTheseFirst(reg *registry.Registry) string {
	names := reg.Names()
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("Environments registered here: %s\nStop each with `nsctl env stop <name>`.",
		strings.Join(names, ", "))
}
