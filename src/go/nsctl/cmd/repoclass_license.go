package cmd

import (
	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// newRepoClassLicenseCommand authors the `license` declaration (NERD017
// SPEC011): what nsctl publishes from this tree is under this SPDX
// expression, and these paths stay out of it. The declaration is the
// author's; nsctl records it, applies the exclusions when it zips the tree,
// annotates every artifact with the expression, and enforces nothing.
func newRepoClassLicenseCommand(path *repoClassPath) *cobra.Command {
	group := &cobra.Command{
		Use:   "license",
		Short: "Declare the licence of what is published from this repo class",
		Long: `Write or remove the manifest's "license": the SPDX expression of what nsctl
publishes from this tree, and the root-relative paths that stay out of every
zip it makes from it ("nsctl artifact push <dir>", "nsctl stack build/push",
"nsctl stack init --bundle-local", "nsctl artifact register <dir>"). Every
published layer is annotated org.opencontainers.image.licenses with the
expression. A repo class that declares nothing is published whole and
unannotated; nsctl never infers a licence and never refuses one.`,
		Example: `  nsctl repoclass license set MIT
  nsctl repoclass license set Apache-2.0 --exclude src/python --exclude src/docker --exclude src/typescript
  nsctl repoclass license clear`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(newLicenseSetCommand(path), newLicenseClearCommand(path))
	return group
}

func newLicenseSetCommand(path *repoClassPath) *cobra.Command {
	var exclude []string
	cmd := &cobra.Command{
		Use:   "set <spdx>",
		Short: "Declare the SPDX expression, and the paths kept out of what is published",
		Long: `Replace the declaration. With no --exclude the shorthand form is written
("license": "<spdx>"); with one or more, the object form with the cleaned
list. An exclude is a path relative to the repository root, matched on whole
segments (src/python covers src/python/app.py, not src/pythonic).`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.SetLicence(s.Doc, args[0], exclude)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
	cmd.Flags().StringArrayVar(&exclude, "exclude", nil, "a root-relative path kept out of every published zip (repeatable)")
	return cmd
}

func newLicenseClearCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "clear",
		Short:         "Remove the declaration",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, ok := bacon.ClearLicence(s.Doc)
			if !ok {
				return nil
			}
			return saveAndReport(cmd, s, key)
		},
	}
}
