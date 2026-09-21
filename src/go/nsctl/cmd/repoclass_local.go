package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// newRepoClassLocalCommand edits the `local` section: the companions a
// repository or stack wants beside it, and how its dependency roles are
// filled locally. NERD019 SPEC008.
func newRepoClassLocalCommand(path *repoClassPath) *cobra.Command {
	group := &cobra.Command{
		Use:   "local",
		Short: "Edit the local section: companions, bound and external roles, default profiles",
		Long: `The "local" section (NERD010) declares what a repository -- or a stack --
wants beside it locally: companions to start, and how each dependency role
under deploy.dependencies is filled. "add" declares a companion; "bind" fills
a role with an instance the environment substrate provides; "require" marks a
role external, to be filled by another stack's instance and matched by
resource; "set-default-profiles" chooses what is on by default. Run
"nsctl lock" afterwards to pin what was declared.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newLocalAddCommand(path),
		newLocalRemoveCommand(path),
		newLocalBindCommand(path),
		newLocalRequireCommand(path),
		newLocalSetDefaultProfilesCommand(path),
		newLocalListCommand(path),
	)
	return group
}

func newLocalAddCommand(path *repoClassPath) *cobra.Command {
	var (
		c       bacon.Companion
		depends []string
	)
	cmd := &cobra.Command{
		Use:   "add <repo-class>",
		Short: "Declare a companion to start alongside this repository",
		Example: `  nsctl repoclass local add hmd-inf-otel-collector --spec "~= 0.1" --name otel
  nsctl repoclass local add hmd-inf-clickhouse --spec "== 0.3.12" --name clickhouse --profile full --depends sink=bucket`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c.RepoClassName = args[0]
			if c.InstanceName == "" {
				c.InstanceName = defaultInstanceName(args[0])
			}
			if len(depends) > 0 {
				c.Dependencies = map[string]string{}
				for _, d := range depends {
					k, v, err := splitPair(d, "--depends")
					if err != nil {
						return err
					}
					c.Dependencies[k] = v
				}
			}
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.AddCompanion(s.Doc, c)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
	cmd.Flags().StringVar(&c.InstanceName, "name", "", "instance name (default: the class without its hmd- prefix)")
	cmd.Flags().StringVar(&c.VersionSpec, "spec", "", "BACON version spec, e.g. \"~= 0.1\" or \"== 0.1.5\"")
	cmd.Flags().StringSliceVar(&c.Profiles, "profile", nil, "profiles that activate it (none = unconditional)")
	cmd.Flags().StringArrayVar(&depends, "depends", nil, "role=instance the companion depends on (repeatable)")
	return cmd
}

func newLocalRemoveCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "remove <instance>",
		Short:         "Remove a companion",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, removed := bacon.RemoveCompanion(s.Doc, args[0])
			if !removed {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: %s is not declared; nothing to remove\n", key)
				return nil
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newLocalBindCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:   "bind <role> <instance>",
		Short: "Fill a dependency role with an instance the environment provides",
		Long: `Writes local.dependencies.<role>.bind. The substrate's instances --
local-neuronsphere for compute, environment-db for a database -- are the
usual targets. Nothing is declared or pinned for a bound role.`,
		Example:       `  nsctl repoclass local bind compute local-neuronsphere`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.BindRole(s.Doc, args[0], args[1])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newLocalRequireCommand(path *repoClassPath) *cobra.Command {
	var suggest string
	cmd := &cobra.Command{
		Use:   "require <role>",
		Short: "Mark a dependency role external: another stack's instance fills it",
		Long: `Writes local.dependencies.<role> with external: true. Nothing is pinned or
bundled for the role; "nsctl stack add" binds it to an instance in the
environment that produces the role's resource type, or refuses naming
--suggest. Declare the role first with "deploy add-dependency", with a
resource type, so the match is by what is needed rather than by name.`,
		Example:       `  nsctl repoclass local require warehouse-bucket --suggest ghcr.io/hmdlabs/stacks/storage`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.RequireRole(s.Doc, args[0], suggest)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
	cmd.Flags().StringVar(&suggest, "suggest", "", "a stack reference that would satisfy the role")
	return cmd
}

func newLocalSetDefaultProfilesCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "set-default-profiles [<profile>,...]",
		Short:         "Set which profiles are on by default (none for lean)",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var profiles []string
			if len(args) == 1 {
				profiles = strings.Split(args[0], ",")
			}
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.SetDefaultProfiles(s.Doc, profiles)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newLocalListCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "list",
		Short:         "Show every want the local section declares and how it is filled",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			spec, err := localspec.Load(s.Dir)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			wants := spec.Wants()
			if len(wants) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "The local section declares nothing.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "KEY\tCLASS\tSPEC\tKIND\tPROFILES")
			for _, w := range wants {
				kind := "companion"
				switch {
				case w.Bind != "":
					kind = "bound -> " + w.Bind
				case w.External:
					kind = "external"
					if w.Suggest != "" {
						kind += " (suggest " + w.Suggest + ")"
					}
				case len(w.Satisfies) > 0:
					kind = "dependency"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", w.Key, w.RepoClassName, w.VersionSpec, kind, strings.Join(w.Profiles, ","))
			}
			if len(spec.Local.DefaultProfiles) > 0 {
				fmt.Fprintf(tw, "\ndefault profiles: %s\n", strings.Join(spec.Local.DefaultProfiles, ", "))
			}
			return tw.Flush()
		},
	}
}
