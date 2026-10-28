package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// newBundleMigrateCommand moves a repository's local section into a bundle
// file (NERD0019 SPEC0006).
func newBundleMigrateCommand(opts *Options) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "migrate [repo-dir]",
		Short: "Move a repository's local section into meta-data/bundles/<name>.json",
		Long: `Writes the manifest's "local" section as a bundle file and removes it, with
deploy.dependencies, from meta-data/manifest.json: the bundle's roles say what both
said. Everything else in the manifest is kept, and neuronsphere.lock is untouched
-- the bundle activates the same wants for every profile set, so the lock still
pins them.

The bundle is named after the repository with any "hmd-stack-" prefix removed,
or --name.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			spec, err := localspec.Load(dir)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if !spec.HasLocal() {
				return nserr.New(nserr.Usage, "%s has no local section to migrate", spec.Path)
			}
			if name == "" {
				name = strings.TrimPrefix(spec.RepoClassName, "hmd-stack-")
			}
			bundle, err := localspec.ToBundle(spec, name)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			bundlePath := filepath.Join(dir, localspec.BundleDir, name+".json")
			if _, err := os.Stat(bundlePath); err == nil {
				return nserr.New(nserr.Usage, "%s already exists", bundlePath)
			}

			raw, err := os.ReadFile(spec.Path)
			if err != nil {
				return err
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				return err
			}
			delete(doc, "local")
			if deploy, ok := doc["deploy"].(map[string]any); ok {
				delete(deploy, "dependencies")
			}
			manifestJSON, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return err
			}
			if err := writeFileAt(bundlePath, bundle); err != nil {
				return err
			}
			if err := writeFileAt(spec.Path, append(manifestJSON, '\n')); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\nRemoved local and deploy.dependencies from %s\n", bundlePath, spec.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the bundle's name (default: the repository name without hmd-stack-)")
	return cmd
}
