package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/envtemplate"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// newTemplateCommand groups the environment-template verbs (NERD035 SPEC003).
func newTemplateCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Keep named environment shapes a session environment starts from",
		Long: `A template is an environment manifest kept under a name in
$HMD_HOME/templates: the instances, stacks and profiles an environment for some
kind of work starts with -- "analytics" for Trino and Airflow, "telemetry" for
the OpenTelemetry collector and ClickHouse.

nsctl ships no templates. Make one from a manifest file, from a published stack,
or from an environment that already has the right shape.`,
		Example: `  nsctl template add telemetry --stack observability
  nsctl template add analytics --from-env dev
  nsctl template add warehouse ./warehouse.yaml
  nsctl template list`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newTemplateAddCommand(opts),
		newTemplateListCommand(opts),
		newTemplateShowCommand(opts),
		newTemplateRemoveCommand(opts),
	)
	return cmd
}

func newTemplateAddCommand(opts *Options) *cobra.Command {
	var stackName, fromEnv, spec, localURL string
	var force bool
	var repo fromRepo
	cmd := &cobra.Command{
		Use:   "add <name> [<file> | --stack <ref> | --from-env <env>]",
		Short: "Store a template from a manifest file, a stack or an environment",
		Long: `Stores a template under <name> from exactly one source:

  <file>            an environment manifest, copied in and renamed <name>
  --stack <ref>     a published stack, planned as "nsctl stack add" plans it
                    but into an empty manifest: the stack's instances at their
                    pinned versions, from their artifacts, and its record.
                    --profile, --all-profiles, --lean and --name mean what they
                    mean there. The stack is fetched now, so a session later
                    started from the template needs no network.
  --from-env <env>  an environment's manifest, minus the instances it deploys
                    from a working tree (source: {type: local}), which belong
                    to whoever was editing them

An existing template is replaced only with --force.`,
		Example: `  nsctl template add telemetry --stack observability --profile full
  nsctl template add analytics --from-env dev
  nsctl template add warehouse ./warehouse.yaml --force`,
		Args:          cobra.RangeArgs(1, 2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := envtemplate.ValidName(name); err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			sources := 0
			for _, given := range []bool{len(args) == 2, stackName != "", fromEnv != ""} {
				if given {
					sources++
				}
			}
			if sources != 1 {
				return nserr.New(nserr.Usage, "name exactly one source: a manifest file, --stack <ref> or --from-env <env>")
			}
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			if envtemplate.Exists(home, name) && !force {
				return nserr.New(nserr.Usage, "template %q already exists; pass --force to replace it", name)
			}

			var tmpl *manifest.Manifest
			switch {
			case len(args) == 2:
				tmpl, err = manifest.LoadFile(args[1], opts.Lookup)
				if err != nil {
					return nserr.Wrap(nserr.Usage, err)
				}
			case fromEnv != "":
				_, _, slug, err := resolveEnvSlug(opts, fromEnv)
				if err != nil {
					return err
				}
				m, err := manifest.Load(home, slug, opts.Lookup)
				if err != nil {
					return nserr.Wrap(nserr.Usage, err)
				}
				if m == nil {
					return nserr.New(nserr.Usage, "environment %q has no manifest, so it has no shape to keep", slug)
				}
				var dropped []string
				tmpl, dropped = envtemplate.FromEnvironment(m)
				if len(dropped) > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "note: left out %s, deployed from a working tree in %s\n",
						strings.Join(dropped, ", "), slug)
				}
			default:
				ref, client, err := stackRef(cmd, opts, stackName, spec)
				if err != nil {
					return err
				}
				tmpl = &manifest.Manifest{Version: manifest.Version, Name: name}
				if _, err := declareStack(cmd, opts, home, tmpl, ref, client, localURL, &repo); err != nil {
					return err
				}
			}

			if err := envtemplate.Save(home, name, tmpl); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stored template %s (%s) at %s\n", name, describeTemplate(tmpl), tmpl.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&stackName, "stack", "", "a published stack to plan the template from")
	cmd.Flags().StringVar(&fromEnv, "from-env", "", "an environment whose manifest the template copies, minus its working trees")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing template of the same name")
	cmd.Flags().StringVar(&spec, "spec", "", "with --stack, a BACON version spec to choose the version by (e.g. \"~= 0.1\")")
	cmd.Flags().StringVar(&localURL, "local-url", "", "with --stack, the control plane's Artifact Librarian")
	cmd.Flags().StringSliceVar(&repo.profiles, "profile", nil, "with --stack, local profiles to activate. Repeatable, or comma-separated")
	cmd.Flags().BoolVar(&repo.allProfiles, "all-profiles", false, "with --stack, activate every profile the stack's lock mentions")
	cmd.Flags().BoolVar(&repo.lean, "lean", false, "with --stack, activate no profiles")
	cmd.Flags().StringArrayVar(&repo.names, "name", nil, "with --stack, name one instance, as <role-or-declared-name>=<instance>. Repeatable")
	return cmd
}

// describeTemplate is a template's size, for one line of output.
func describeTemplate(m *manifest.Manifest) string {
	parts := []string{fmt.Sprintf("%d instance%s", len(m.Repos), plural(len(m.Repos), "", "s"))}
	if len(m.Stacks) > 0 {
		names := make([]string, 0, len(m.Stacks))
		for _, s := range m.Stacks {
			names = append(names, s.Name+" "+s.Version)
		}
		parts = append(parts, "stack "+strings.Join(names, ", "))
	}
	return strings.Join(parts, "; ")
}

func newTemplateListCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:           "list",
		Short:         "List the stored templates",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			names, err := envtemplate.List(home)
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			out := cmd.OutOrStdout()
			if len(names) == 0 {
				fmt.Fprintln(out, "No templates. Add one with `nsctl template add <name> --stack <ref>`, --from-env <env> or a manifest file.")
				return nil
			}
			for _, name := range names {
				m, err := envtemplate.Load(home, name, opts.Lookup)
				if err != nil {
					// One broken file must not hide the others.
					fmt.Fprintf(out, "%-20s (unreadable: %v)\n", name, firstLine(err.Error()))
					continue
				}
				fmt.Fprintf(out, "%-20s %s\n", name, describeTemplate(m))
			}
			return nil
		},
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func newTemplateShowCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:           "show <name>",
		Short:         "Print a template",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			m, err := envtemplate.Load(home, args[0], opts.Lookup)
			if err != nil {
				return templateError(err)
			}
			data, err := os.ReadFile(m.Path)
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func newTemplateRemoveCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:           "remove <name>",
		Aliases:       []string{"rm"},
		Short:         "Delete a template",
		Long:          "Deletes the template file. Environments started from it keep their own manifests.",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			if err := envtemplate.Remove(home, args[0]); err != nil {
				return templateError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed template %s\n", args[0])
			return nil
		},
	}
}

// templateError is a missing template or a bad name as a usage error, and
// anything else as a failure.
func templateError(err error) error {
	if errors.Is(err, envtemplate.ErrNotFound) || errors.Is(err, envtemplate.ErrInvalidName) {
		return nserr.Wrap(nserr.Usage, err)
	}
	return nserr.Wrap(nserr.Fail, err)
}
