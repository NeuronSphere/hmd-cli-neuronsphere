package cmd

import (
	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

func newRepoClassDiscoveryCommand(path *repoClassPath) *cobra.Command {
	group := &cobra.Command{
		Use:           "discovery",
		Short:         "Edit the discovery section: what the repo class can do, for an agent reading it",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newSetSummaryCommand(path),
		newAddEntryPointCommand(path),
		newAddCapabilityCommand(path),
		newAddRelatedDocCommand(path),
	)
	return group
}

func newSetSummaryCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "set-summary <text>",
		Short:         "Set the one-paragraph summary",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.SetSummary(s.Doc, args[0])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newAddEntryPointCommand(path *repoClassPath) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:           "add-entry-point <path>",
		Short:         "Add or replace an entry point, keyed by path",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if description == "" {
				return nserr.New(nserr.Usage, "--description is required")
			}
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.AddEntryPoint(s.Doc, args[0], description)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "What is at this path (required)")
	return cmd
}

func newAddCapabilityCommand(path *repoClassPath) *cobra.Command {
	var kind, description, location string
	cmd := &cobra.Command{
		Use:           "add-capability <name>",
		Short:         "Add or replace a capability, keyed by name",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if description == "" {
				return nserr.New(nserr.Usage, "--description is required")
			}
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.AddCapability(s.Doc, args[0], kind, description, location)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "One of endpoint, cli_command, function, class, operation (required)")
	cmd.Flags().StringVar(&description, "description", "", "What the capability does (required)")
	cmd.Flags().StringVar(&location, "location", "", "Where it is implemented")
	return cmd
}

func newAddRelatedDocCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "add-related-doc <title> <path>",
		Short:         "Add or replace a related document, keyed by path",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.AddRelatedDoc(s.Doc, args[0], args[1])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
}
