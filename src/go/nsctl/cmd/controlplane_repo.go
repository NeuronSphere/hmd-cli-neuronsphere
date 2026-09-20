package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/controlplane"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/cpext"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/spf13/cobra"
)

func newControlPlaneRepoCommand(opts *Options) *cobra.Command {
	repo := &cobra.Command{
		Use:   "repo",
		Short: "Declare the repo instances the control plane runs alongside itself",
		Long: `Edits the control-plane manifest at $HMD_HOME/.config/control-plane.yaml.

These verbs are wrappers: they change that file and nothing else. Editing it by
hand is equivalent, and either way ` + "`nsctl control-plane apply`" + ` is what starts the
result -- so nothing here touches a running control plane on its own.

An extension declared here outlives every environment, is up whenever the
control plane is, and there is one of it per HMD_HOME. Anything that should be
one-per-environment belongs in an environment manifest instead; see
` + "`nsctl repo add`" + `.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	repo.AddCommand(
		newControlPlaneRepoAddCommand(opts),
		newControlPlaneRepoRemoveCommand(opts),
		newControlPlaneRepoListCommand(opts),
	)
	return repo
}

// openControlPlaneManifest reads the control-plane manifest, or builds an empty
// one when there is none so the first `add` creates the file rather than
// failing -- openManifest's decision, for openManifest's reason.
func openControlPlaneManifest(opts *Options, home string) (*manifest.Manifest, error) {
	m, err := manifest.LoadControlPlane(home, opts.Lookup)
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	if m == nil {
		m = &manifest.Manifest{
			Version: manifest.Version, Name: manifest.ControlPlaneName,
			Scope: manifest.ScopeControlPlane, Path: manifest.ControlPlaneDefaultPath(home),
		}
	}
	if m.Path == "" {
		m.Path = manifest.ControlPlaneDefaultPath(home)
	}
	return m, nil
}

func newControlPlaneRepoAddCommand(opts *Options) *cobra.Command {
	var instanceName, path string
	var config []string

	cmd := &cobra.Command{
		Use:   "add <repo-class>[@<version>]",
		Short: "Declare a control-plane extension",
		Long: `Adds a repo class to the control-plane manifest.

The instance is named after the repo class with its hmd- prefix dropped unless
--name says otherwise. The repo class must carry
src/local/docker-compose.extension.yml; that file is what it contributes.

Declaring is not starting. Run ` + "`nsctl control-plane apply`" + ` to start the result.`,
		Example: `  nsctl control-plane repo add hmd-inf-local-registry --name registry \
    --config url=http://registry.local.neuronsphere.io --config upstream=server:3141
  nsctl control-plane repo add hmd-inf-local-registry --config pypi.enabled=true`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoClass, version := splitVersion(args[0])
			if repoClass == "" {
				return nserr.New(nserr.Usage, "a repo class name is required")
			}
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			m, err := openControlPlaneManifest(opts, home)
			if err != nil {
				return err
			}

			name := instanceName
			if name == "" {
				name = defaultInstanceName(repoClass)
			}
			if manifest.ScopeControlPlane.Reserved(name) {
				return nserr.New(nserr.Usage,
					"%q is reserved and cannot be declared; pass --name to choose another. Reserved: %s",
					name, strings.Join(manifest.ScopeControlPlane.ReservedNames(), ", "))
			}
			if _, exists := m.Repo(name); exists {
				return nserr.New(nserr.Usage,
					"%q is already declared in %s. Remove it first, or pass --name to declare a second instance.",
					name, m.Path)
			}

			configuration, err := parseNestedConfig(config)
			if err != nil {
				return err
			}
			declaration := manifest.Repo{
				InstanceName: name, RepoClassName: repoClass, Version: version,
				InstanceConfiguration: configuration,
			}
			if path != "" {
				declaration.Source = &manifest.Source{Type: manifest.SourceLocal, Path: path}
			}

			// The whole manifest, not just the addition: writing a file that
			// `apply` will refuse helps nobody.
			candidate := *m
			candidate.Repos = append(append([]manifest.Repo{}, m.Repos...), declaration)
			if problems := candidate.Validate(opts.Lookup); len(problems) > 0 {
				return nserr.New(nserr.Usage, "cannot declare %s:\n  - %s",
					name, strings.Join(problems, "\n  - "))
			}

			m.Repos = candidate.Repos
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Declared %s (%s) in %s\n", name, repoClass, m.Path)
			fmt.Fprintln(cmd.OutOrStdout(), "Run `nsctl control-plane apply` to start it.")
			return nil
		},
	}
	cmd.Flags().StringVar(&instanceName, "name", "", "Instance name (default: the repo class without its hmd- prefix)")
	cmd.Flags().StringVar(&path, "path", "", "Working tree to run from (default: $HMD_REPO_HOME/<repo-class>)")
	cmd.Flags().StringArrayVar(&config, "config", nil,
		"An instance configuration value as key=value; dotted keys nest; repeatable")
	return cmd
}

func newControlPlaneRepoRemoveCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove <instance>",
		Aliases: []string{"rm"},
		Short:   "Undeclare a control-plane extension",
		Long: `Removes an instance from the control-plane manifest.

This edits the manifest only. What is already running stays running: nsctl does
not tear an extension down on your behalf. ` + "`nsctl env purge`" + ` with no name is
the verb that does, and it removes the whole control plane with it.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			m, err := manifest.LoadControlPlane(home, opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if m == nil {
				return nserr.New(nserr.Usage,
					"there is no control-plane manifest, so it declares nothing to remove")
			}
			if _, found := m.Repo(name); !found {
				return nserr.New(nserr.Usage, "%s does not declare %q. Declared: %s",
					m.Path, name, strings.Join(declaredNames(m), ", "))
			}

			kept := make([]manifest.Repo, 0, len(m.Repos))
			for _, r := range m.Repos {
				if r.InstanceName != name {
					kept = append(kept, r)
				}
			}
			m.Repos = kept
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s from %s\n", name, m.Path)
			fmt.Fprintf(cmd.ErrOrStderr(),
				"note: %s's containers are still running and its data under $HMD_HOME is untouched;\n"+
					"      `nsctl control-plane apply` will report it as undeclared rather than remove it\n", name)
			return nil
		},
	}
	return cmd
}

func newControlPlaneRepoListCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show what the control plane declares and whether it resolved",
		Long: `Lists the control-plane manifest's declarations with their resolved versions.

The version source matters as much as the version: "0.1.4 from a working tree"
and "0.1.4 from a bundled tree" are different facts.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, home, err := loadRegistry(opts)
			if err != nil {
				return err
			}
			cpOpts := &controlplane.Options{
				Home: home, Lookup: opts.Lookup,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}
			project, err := controlplane.Project(cpOpts, reg, "")
			if err != nil {
				return err
			}
			exts, err := controlplane.ResolveExtensions(cmd.Context(), cpOpts, reg, project)
			if err != nil {
				return err
			}
			if len(exts) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(),
					"The control plane declares no extensions.\n"+
						"Declare one with `nsctl control-plane repo add <repo-class>`.\n")
				return nil
			}
			renderExtensionTable(cmd.OutOrStdout(), exts)
			return nil
		},
	}
}

// parseNestedConfig is parseConfig with dotted keys nesting, so
// `--config pypi.enabled=true` writes the block shape a compose file reads as
// NS_CONFIG_PYPI_ENABLED and an extension reads as a profile.
func parseNestedConfig(pairs []string) (map[string]any, error) {
	out := map[string]any{}
	for _, pair := range pairs {
		key, value, err := splitPair(pair, "--config")
		if err != nil {
			return nil, err
		}
		var typed any = value
		var parsed any
		if err := json.Unmarshal([]byte(value), &parsed); err == nil {
			typed = parsed
		}
		if err := nest(out, strings.Split(key, "."), typed); err != nil {
			return nil, nserr.Wrap(nserr.Usage, err)
		}
	}
	return out, nil
}

// nest writes value at a dotted path, refusing to overwrite a scalar with a
// block. Doing that silently would drop the earlier --config the user passed.
func nest(into map[string]any, path []string, value any) error {
	for i, segment := range path[:len(path)-1] {
		existing, ok := into[segment]
		if !ok {
			child := map[string]any{}
			into[segment], into = child, child
			continue
		}
		child, ok := existing.(map[string]any)
		if !ok {
			return fmt.Errorf("--config %s conflicts with the earlier --config %s, which is not a block",
				strings.Join(path, "."), strings.Join(path[:i+1], "."))
		}
		into = child
	}
	into[path[len(path)-1]] = value
	return nil
}

func renderExtensionTable(out io.Writer, exts []cpext.Extension) {
	sorted := append([]cpext.Extension(nil), exts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Instance < sorted[j].Instance })

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "INSTANCE\tREPO CLASS\tVERSION\tFROM\tURL\tSTATUS")
	for _, e := range sorted {
		status := "ok"
		if e.Failed() {
			status = "unresolved"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Instance, e.RepoClass, orDash(e.Version), orDash(string(e.Source)), orDash(e.URL), status)
	}
	w.Flush()

	for _, e := range sorted {
		if e.Failed() {
			fmt.Fprintf(out, "\n%s: %v\n", e.Instance, e.Err)
		}
	}
}
