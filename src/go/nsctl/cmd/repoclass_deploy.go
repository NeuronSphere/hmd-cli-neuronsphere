package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

func newRepoClassDeployCommand(path *repoClassPath) *cobra.Command {
	group := &cobra.Command{
		Use:           "deploy",
		Short:         "Edit the deploy section: command, image, dependencies, resources, configuration",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newSetExecCommand(path, "deploy"),
		newSetMechanismCommand(path, "deploy"),
		newAddCommandCommand(path, "deploy"),
		newRemoveCommandCommand(path, "deploy"),
		newSetImageCommand(path),
		newAddDependencyCommand(path),
		newRemoveDependencyCommand(path),
		newAddResourceCommand(path),
		newRemoveResourceCommand(path),
		newSetConfigCommand(path),
		newUnsetConfigCommand(path),
	)
	return group
}

func newSetImageCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "set-image <ref>",
		Short:         "Set the image the deploy command runs in",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.SetImage(s.Doc, args[0])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newAddDependencyCommand(path *repoClassPath) *cobra.Command {
	var (
		d                  bacon.Dependency
		required, optional bool
		tags               []string
	)
	cmd := &cobra.Command{
		Use:   "add-dependency <role>",
		Short: "Declare a dependency role, by repo class, by resource type, or both",
		Long: `Adds or replaces deploy.dependencies.<role>. --required is the default, as it
is for hmd manifest. A resource-typed dependency names what is needed rather
than which repo produces it, and resolves by type inheritance and tag
selector; --repo-class-name alongside it is a suggestion, not a pin.`,
		Example: `  nsctl repoclass deploy add-dependency warehouse --resource-namespace acme.com --resource-definition-name sql-warehouse --resource-version 0.1.0
  nsctl repoclass deploy add-dependency cluster --repo-class-name hmd-inf-eks-cluster --optional`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if required && optional {
				return nserr.New(nserr.Usage, "--required and --optional exclude each other")
			}
			d.Required = !optional
			if len(tags) > 0 {
				d.Tags = map[string]string{}
				for _, t := range tags {
					k, v, err := splitPair(t, "--tag")
					if err != nil {
						return err
					}
					d.Tags[k] = v
				}
			}
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.AddDependency(s.Doc, args[0], d)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
	cmd.Flags().StringVar(&d.RepoClassName, "repo-class-name", "", "The repo class that fills the role (a suggestion when a resource is also named)")
	cmd.Flags().BoolVar(&required, "required", false, "The role must be filled (the default)")
	cmd.Flags().BoolVar(&optional, "optional", false, "The role may be left unfilled")
	cmd.Flags().StringVar(&d.VersionSpec, "version-spec", "", "PEP 440 version specifier for the repo class")
	cmd.Flags().StringVar(&d.ResourceNamespace, "resource-namespace", "", "Resource type namespace, e.g. database.neuronsphere.io")
	cmd.Flags().StringVar(&d.ResourceDefinitionName, "resource-definition-name", "", "Resource type name, e.g. postgres")
	cmd.Flags().StringVar(&d.ResourceVersion, "resource-version", "", "Resource type version, e.g. 0.1.0")
	cmd.Flags().StringVar(&d.ResourceVersionSpec, "resource-version-spec", "", "Resource type version specifier")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "Tag selector key=value (repeatable)")
	return cmd
}

func newRemoveDependencyCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "remove-dependency <role>",
		Short:         "Remove a dependency role",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, removed := bacon.RemoveDependency(s.Doc, args[0])
			if !removed {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: %s is not declared; nothing to remove\n", key)
				return nil
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newAddResourceCommand(path *repoClassPath) *cobra.Command {
	var (
		r                    bacon.Resource
		produces, noProduces bool
	)
	cmd := &cobra.Command{
		Use:           "add-resource <name>",
		Short:         "Declare a resource type this repo class relates to, under deploy.resources",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if produces && noProduces {
				return nserr.New(nserr.Usage, "--produces and --no-produces exclude each other")
			}
			if produces || noProduces {
				v := produces
				r.Produces = &v
			}
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.AddResource(s.Doc, args[0], r)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
	cmd.Flags().StringVar(&r.Namespace, "resource-namespace", "", "Resource type namespace (required)")
	cmd.Flags().StringVar(&r.Name, "resource-definition-name", "", "Resource type name (required)")
	cmd.Flags().StringVar(&r.Version, "version", "", "Resource type version (required)")
	cmd.Flags().BoolVar(&produces, "produces", false, "This repo class produces the resource")
	cmd.Flags().BoolVar(&noProduces, "no-produces", false, "This repo class only consumes the resource")
	cmd.Flags().StringVar(&r.Role, "role", "", "The role the resource fills")
	cmd.Flags().StringVar(&r.Description, "description", "", "What the resource is")
	return cmd
}

func newRemoveResourceCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "remove-resource <name>",
		Short:         "Remove a resource declaration",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, removed := bacon.RemoveResource(s.Doc, args[0])
			if !removed {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: %s is not declared; nothing to remove\n", key)
				return nil
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newSetConfigCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:   "set-config <dotted.key> <value>",
		Short: "Set a default configuration value",
		Long: `Writes a dotted key under deploy.default_configuration. The value is typed
the way nsctl repo add --config types it: valid JSON is taken as JSON
(2 is a number, true a boolean, {"a":1} an object), anything else as the
literal string.`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var typed any = args[1]
			var parsed any
			if err := json.Unmarshal([]byte(args[1]), &parsed); err == nil {
				typed = parsed
			}
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.SetConfig(s.Doc, args[0], typed)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newUnsetConfigCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "unset-config <dotted.key>",
		Short:         "Remove a default configuration value",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, removed := bacon.UnsetConfig(s.Doc, args[0])
			if !removed {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: %s is not set; nothing to remove\n", key)
				return nil
			}
			return saveAndReport(cmd, s, key)
		},
	}
}
