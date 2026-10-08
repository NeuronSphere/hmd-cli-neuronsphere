package cmd

import "github.com/spf13/cobra"

// newModelCommand is the data model a set of repositories describes
// (NERD032, NERD033): inspecting it, diffing snapshots of it, and the
// perspectives that bind its nouns to physical tables.
func newModelCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Inspect, diff and derive the data model repositories describe",
		Long: `The logical data model a set of repositories describes: .hms nouns, the
tables, views, dbt models and exports that carry them, lineage between them,
and every place those artifacts disagree. Nothing has to be declared first:
perspectives no repository declares are derived from the files. NERD032,
NERD033.`,
		Example: `  nsctl model inspect ~/src
  nsctl model inspect ~/src ntc_instances_export --sources
  nsctl model diff ~/src --live
  nsctl model perspective derive ~/src --evidence`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newModelInspectCommand(opts), newModelDiffCommand(opts), newModelPerspectiveCommand(opts))
	return cmd
}
