package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/plugin"
)

// newPluginCommand manages declared CLI plugins. NERD018 SPEC003.
func newPluginCommand(opts *Options) *cobra.Command {
	group := &cobra.Command{
		Use:   "plugin",
		Short: "Install, list, update, remove and publish CLI plugins",
		Long: `A CLI plugin is an executable that adds one top-level noun to nsctl:
"nsctl <name> ..." runs "nsctl-<name>" with every argument after the noun.

A plugin runs because $HMD_HOME/.config/nsctl.toml declares it under
[plugin.<name>], and for no other reason; nothing on PATH or under the cache
is scanned. "install" fetches a published plugin from an OCI registry (a bare
name expands to ` + plugin.DefaultNamespace + `), unpacks this platform's
binary under $HMD_HOME/.cache/neuronsphere/plugins/, and writes the
declaration. A local build is declared by hand with "path = ..." instead.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newPluginInstallCommand(opts),
		newPluginRemoveCommand(opts),
		newPluginListCommand(opts),
		newPluginUpdateCommand(opts),
		newPluginPushCommand(opts),
	)
	return group
}

// loadConfigOrEmpty reads nsctl.toml, treating a missing file as empty. A
// malformed one is an error: writing a declaration into a file that does
// not parse would lose whatever the user had there.
func loadConfigOrEmpty(opts *Options) (*nsconfig.Config, error) {
	cfg, err := nsconfig.Load(opts.Home, opts.Lookup)
	if err != nil {
		if errors.Is(err, nsconfig.ErrNoConfig) {
			return &nsconfig.Config{}, nil
		}
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	return cfg, nil
}

// installPlugin is install and update's shared body.
func installPlugin(cmd *cobra.Command, opts *Options, typed, spec string) error {
	home, err := opts.RequireHome()
	if err != nil {
		return err
	}
	ref, expanded, err := expandRef(typed, plugin.DefaultNamespace)
	if err != nil {
		return err
	}
	reportRef(cmd.OutOrStdout(), typed, ref, expanded)
	client := oci.New(registryCredential(opts, ref.Host, ""))
	ref, err = resolveVersion(cmd.Context(), cmd.OutOrStdout(), client, ref, spec)
	if err != nil {
		return err
	}

	cfg, err := loadConfigOrEmpty(opts)
	if err != nil {
		return err
	}
	inst, err := plugin.Install(cmd.Context(), home, client, ref, opts.Version)
	if err != nil {
		return classifyRegistryError(err)
	}
	if inst.Warning != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", inst.Warning)
	}
	// The built-in tree is the authority on reserved nouns; nsconfig's copy
	// of the list is a first line of defence for a file edited by hand.
	if err := reservedNoun(cmd.Root(), inst.Descriptor.Name); err != nil {
		_ = plugin.Remove(home, inst.Descriptor.Name)
		return err
	}
	if prev, ok := cfg.Plugins[inst.Descriptor.Name]; ok && prev.Dev() {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: plugin %s was declared with path = %s; that path still wins over the installed version until you remove it from nsctl.toml\n",
			inst.Descriptor.Name, prev.Path)
	}
	decl := inst.Declaration(ref)
	if prev, ok := cfg.Plugins[decl.Name]; ok && prev.Dev() {
		decl.Path = prev.Path
	}
	cfg.SetPlugin(decl)
	if err := nsconfig.Save(home, opts.Lookup, cfg); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := plugin.Prune(home, decl.Name, decl.Version); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not remove previous versions of %s: %v\n", decl.Name, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Installed %s %s (%s) -> %s\nDeclared in %s; run: nsctl %s --help\n",
		inst.Descriptor.Name, inst.Descriptor.Version, inst.Digest, inst.Binary,
		nsconfig.Path(home, opts.Lookup), inst.Descriptor.Name)
	return nil
}

// classifyRegistryError attaches an exit code: a usage problem for a
// too-old nsctl or a missing platform, a failure for everything a registry
// said.
func classifyRegistryError(err error) error {
	switch {
	case errors.Is(err, plugin.ErrTooOld), errors.Is(err, plugin.ErrNoPlatform):
		return nserr.Wrap(nserr.Usage, err)
	case errors.Is(err, oci.ErrNoCredential), errors.Is(err, oci.ErrBoundToHost):
		return nserr.Wrap(nserr.Usage, err)
	}
	return nserr.Wrap(nserr.Fail, err)
}

func newPluginInstallCommand(opts *Options) *cobra.Command {
	var spec string
	cmd := &cobra.Command{
		Use:   "install <ref>",
		Short: "Install a published plugin and declare it in nsctl.toml",
		Long: `Fetch a plugin from an OCI registry and declare it. <ref> is
<host>/<repository>[:<version>]; a bare name expands to
` + plugin.DefaultNamespace + `/<name>. Without a version the newest published
one is installed and printed. Public namespaces need no credential; a private
one takes HMD_REGISTRY_TOKEN, or a profile whose registry_url matches the
host after "nsctl login".`,
		Example: `  nsctl plugin install hello
  nsctl plugin install ghcr.io/acme/plugins/deploy:1.4.0
  nsctl plugin install hello --spec "~= 1.2"`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installPlugin(cmd, opts, args[0], spec)
		},
	}
	cmd.Flags().StringVar(&spec, "spec", "", "a BACON version spec to choose the version by (e.g. \"~= 1.2\")")
	return cmd
}

func newPluginUpdateCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update [<name>]",
		Short: "Re-install a declared plugin at its newest published version",
		Long: `Resolve the newest version of one declared plugin, or of every plugin
declared with a source when no name is given, and install it. A plugin
declared with only a path is left alone.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := opts.RequireHome(); err != nil {
				return err
			}
			cfg, err := loadConfigOrEmpty(opts)
			if err != nil {
				return err
			}
			names := cfg.PluginNames()
			if len(args) == 1 {
				if _, ok := cfg.Plugins[args[0]]; !ok {
					return nserr.New(nserr.Usage, "plugin %q is not declared in %s", args[0], nsconfig.Path(opts.Home, opts.Lookup))
				}
				names = args[:1]
			}
			updated := 0
			for _, name := range names {
				decl := cfg.Plugins[name]
				if decl.Source == "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: declared by path only; nothing to update\n", name)
					continue
				}
				if err := installPlugin(cmd, opts, decl.Source, ""); err != nil {
					return err
				}
				updated++
			}
			if updated == 0 && len(args) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No plugins with a source are declared.")
			}
			return nil
		},
	}
	return cmd
}

func newPluginRemoveCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "remove <name>",
		Short:         "Undeclare a plugin and delete its installed versions",
		Long:          `Remove the [plugin.<name>] table from nsctl.toml and delete every installed version under the cache. A path declared for a dev build is never touched.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			cfg, err := loadConfigOrEmpty(opts)
			if err != nil {
				return err
			}
			if !cfg.RemovePlugin(args[0]) {
				return nserr.New(nserr.Usage, "plugin %q is not declared in %s", args[0], nsconfig.Path(home, opts.Lookup))
			}
			if err := nsconfig.Save(home, opts.Lookup, cfg); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			if err := plugin.Remove(home, args[0]); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed plugin %s\n", args[0])
			return nil
		},
	}
	return cmd
}

// pluginRow is one line of `plugin list --json`.
type pluginRow struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
	Path    string `json:"path,omitempty"`
	Digest  string `json:"digest,omitempty"`
	State   string `json:"state"`
	Binary  string `json:"binary,omitempty"`
}

func newPluginListCommand(opts *Options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List declared plugins and whether each is installed",
		Long:          `Read the [plugin.*] tables of nsctl.toml. Nothing else is consulted: a binary that is present but not declared is not a plugin.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			cfg, err := loadConfigOrEmpty(opts)
			if err != nil {
				return err
			}
			var rows []pluginRow
			for _, name := range cfg.PluginNames() {
				decl := cfg.Plugins[name]
				row := pluginRow{Name: name, Version: decl.Version, Source: decl.Source, Path: decl.Path,
					Digest: decl.Digest, State: string(plugin.StateOf(home, decl))}
				if bin, err := plugin.Resolve(home, decl); err == nil {
					row.Binary = bin
				}
				rows = append(rows, row)
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if rows == nil {
					rows = []pluginRow{}
				}
				return enc.Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No plugins declared in %s\n", nsconfig.Path(home, opts.Lookup))
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tVERSION\tSTATE\tFROM")
			for _, r := range rows {
				from := r.Source
				if r.Path != "" {
					from = r.Path
				}
				version := r.Version
				if version == "" {
					version = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, version, r.State, from)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	return cmd
}

func newPluginPushCommand(opts *Options) *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "push <dir> <ref>",
		Short: "Publish a plugin from a directory of release archives",
		Long: `Build the plugin artifact from <dir>, which holds plugin.json and one
<name>_<version>_<os>_<arch>.tar.gz per platform (GoReleaser's default
archive layout), and push it to <ref>. The tag is the descriptor's version
unless <ref> names one. A credential is required: --token, HMD_REGISTRY_TOKEN,
or a profile whose registry_url matches the host after "nsctl login".`,
		Example:       `  nsctl plugin push dist/ ghcr.io/acme/plugins/hello --token $GHCR_PAT`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := filepath.Abs(args[0])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				return nserr.New(nserr.Usage, "%s is not a directory", args[0])
			}
			ref, err := oci.ParseRef(args[1])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			cred := registryCredential(opts, ref.Host, token)
			if cred.Anonymous() {
				return nserr.Wrap(nserr.Usage, oci.ErrNoCredential)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pushing to %s with credential from %s\n", ref, cred.Source)
			d, tagged, err := plugin.Publish(cmd.Context(), oci.New(cred), ref, dir)
			if err != nil {
				return classifyRegistryError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pushed %s (%s)\n", tagged, d)
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "registry token or PAT (overrides "+oci.TokenEnv+")")
	return cmd
}
