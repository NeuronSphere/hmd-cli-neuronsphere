package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/installitems"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/plugin"
)

// pluginGroupID is the help group declared plugins are listed under.
const pluginGroupID = "plugins"

// invocation is what a plugin dispatch needs from the raw argument vector,
// which cobra's rewriting loses: the home the user named before the noun,
// and the arguments after it, verbatim. NERD018 SPEC004 and SPEC005.
type invocation struct {
	home string   // --home before the noun, else ""
	noun string   // the first non-flag token
	args []string // everything after the noun
}

// scanInvocation reads argv without cobra. Only --home is understood before
// the noun, because it is the only persistent flag; a --home after the noun
// belongs to the plugin.
func scanInvocation(argv []string) invocation {
	var inv invocation
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		switch {
		case tok == "--home" && i+1 < len(argv):
			inv.home = argv[i+1]
			i++
		case strings.HasPrefix(tok, "--home="):
			inv.home = strings.TrimPrefix(tok, "--home=")
		case strings.HasPrefix(tok, "-"):
			// Some other flag before the noun; nothing here takes a value
			// except --home, so it is one token.
		default:
			inv.noun = tok
			inv.args = append([]string(nil), argv[i+1:]...)
			return inv
		}
	}
	return inv
}

// reservedNoun refuses a plugin name the built-in tree already answers to.
func reservedNoun(root *cobra.Command, name string) error {
	for _, r := range nsconfig.ReservedPluginNames {
		if name == r {
			return nserr.New(nserr.Usage, "plugin name %q is reserved", name)
		}
	}
	for _, c := range root.Commands() {
		if c.GroupID == pluginGroupID {
			continue
		}
		if c.Name() == name || c.HasAlias(name) {
			return nserr.New(nserr.Usage, "plugin name %q is a built-in nsctl command; the built-in wins", name)
		}
	}
	return nil
}

// attachPlugins adds one command per declaration in the home argv names.
//
// No home means no plugins and no complaint: `nsctl version` with nothing
// set must keep working. A missing config means no plugins. A malformed one
// is a warning and no plugins, never a failure, for the reason hmd.env gets
// the same treatment in Options.resolve.
func attachPlugins(root *cobra.Command, opts *Options, process hmdenv.Lookup, argv []string, warn io.Writer) {
	inv := scanInvocation(argv)
	home := inv.home
	if home == "" {
		home = process("HMD_HOME")
	}
	if home == "" {
		return
	}
	cfg, err := nsconfig.Load(home, process)
	if err != nil {
		if !errors.Is(err, nsconfig.ErrNoConfig) {
			fmt.Fprintf(warn, "warning: plugins not loaded: %v\n", err)
		}
		return
	}
	if len(cfg.Plugins) == 0 {
		return
	}
	root.AddGroup(&cobra.Group{ID: pluginGroupID, Title: "Plugin commands (declared in nsctl.toml):"})
	attached := map[string]string{}
	claim := func(noun, owner string) bool {
		if err := reservedNoun(root, noun); err != nil {
			fmt.Fprintf(warn, "warning: %v\n", err)
			return false
		}
		if first, ok := attached[noun]; ok {
			fmt.Fprintf(warn, "warning: plugins %s and %s both declare the noun %q; %s wins\n", first, owner, noun, first)
			return false
		}
		attached[noun] = owner
		return true
	}
	for _, name := range cfg.PluginNames() {
		decl := cfg.Plugins[name]
		if len(decl.Items) == 0 {
			if claim(name, name) {
				root.AddCommand(newPluginDispatchCommand(opts, process, decl, inv))
			}
			continue
		}
		// NERD031 SPEC012: one node per command item.
		for _, it := range decl.Commands() {
			if claim(it.Noun, name) {
				root.AddCommand(newItemDispatchCommand(opts, process, decl, it, inv))
			}
		}
	}
}

// newItemDispatchCommand is the cobra node for one command item of a plugin
// installed from an artifact. The exec protocol is NERD018 SPEC005's, with
// the noun as the plugin name and NSCTL_KNOWLEDGE added.
func newItemDispatchCommand(opts *Options, process hmdenv.Lookup, decl nsconfig.Plugin, it nsconfig.PluginItem, inv invocation) *cobra.Command {
	short := it.Summary
	if short == "" {
		short = "plugin " + decl.Name
	}
	return &cobra.Command{
		Use:                it.Noun,
		Short:              short,
		Long:               itemLong(decl, it),
		GroupID:            pluginGroupID,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.resolve(inv.home, process, func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
			})
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			args := inv.args
			if inv.noun != it.Noun {
				args = nil
			}
			bin, argv, err := itemTarget(cmd, decl, it, args)
			if err != nil || bin == "" {
				return err
			}
			file, _ := hmdenv.Load(home)
			env := plugin.Environ(os.Environ(), file, map[string]string{
				"HMD_HOME":                 home,
				plugin.EnvHome:             home,
				plugin.EnvVersion:          opts.Version,
				plugin.EnvBinary:           plugin.Executable(),
				plugin.EnvPluginName:       it.Noun,
				plugin.EnvPluginVersion:    decl.Version,
				plugin.EnvPluginDir:        it.Path,
				"NSCTL_PLUGIN_DECLARED_IN": nsconfig.Path(home, process),
				installitems.KnowledgeEnv:  installitems.KnowledgeDir(home),
			})
			code, err := plugin.Run(bin, argv, env, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return nserr.Wrap(nserr.Fail, fmt.Errorf("%s: %w", it.Noun, err))
			}
			return nserr.Silent(code)
		},
	}
}

// itemTarget is what to exec for an item and the user's args. An empty bin
// with a nil error means the node answered itself (a scripts listing).
func itemTarget(cmd *cobra.Command, decl nsconfig.Plugin, it nsconfig.PluginItem, args []string) (string, []string, error) {
	remedy := fmt.Sprintf("run `nsctl plugin install %s`", decl.Source)
	switch it.Runtime {
	case nsconfig.RuntimeScripts:
		if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
			fmt.Fprint(cmd.OutOrStdout(), scriptsHelp(decl, it))
			return "", nil, nil
		}
		file, ok := it.Scripts[args[0]]
		if !ok {
			return "", nil, nserr.New(nserr.Usage, "%s has no script %q; it has: %s",
				it.Noun, args[0], strings.Join(scriptNames(it), ", "))
		}
		if _, err := os.Stat(file); err != nil {
			return "", nil, nserr.New(nserr.Usage, "%s %s: %v; %s", it.Noun, args[0], err, remedy)
		}
		if it.Interpreter == "" {
			return file, args[1:], nil
		}
		interp, err := exec.LookPath(it.Interpreter)
		if err != nil {
			return "", nil, nserr.New(nserr.Usage, "%s needs %s on PATH to run its scripts", it.Noun, it.Interpreter)
		}
		return interp, append([]string{file}, args[1:]...), nil
	default:
		if _, err := os.Stat(it.Target); err != nil {
			return "", nil, nserr.New(nserr.Usage, "%s is declared but not installed (%s is missing); %s",
				it.Noun, it.Target, remedy)
		}
		return it.Target, append(append([]string(nil), it.Args...), args...), nil
	}
}

func scriptNames(it nsconfig.PluginItem) []string {
	names := make([]string, 0, len(it.Scripts))
	for name := range it.Scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func scriptsHelp(decl nsconfig.Plugin, it nsconfig.PluginItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nUsage:\n  nsctl %s <script> [args...]\n\nScripts:\n", it.Summary, it.Noun)
	for _, name := range scriptNames(it) {
		fmt.Fprintf(&b, "  %s\n", name)
	}
	fmt.Fprintf(&b, "\nFrom %s %s.\n", decl.Name, decl.Version)
	return b.String()
}

func itemLong(decl nsconfig.Plugin, it nsconfig.PluginItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nInstalled by plugin %s %s from %s.", it.Summary, decl.Name, decl.Version, decl.Source)
	if it.Runtime == nsconfig.RuntimeScripts {
		fmt.Fprintf(&b, "\n\nScripts: %s", strings.Join(scriptNames(it), ", "))
	}
	b.WriteString("\n\nEverything after the noun is passed through verbatim; ask it for help with:\n  nsctl " +
		it.Noun + " --help")
	return b.String()
}

// newPluginDispatchCommand is the cobra node for one declared plugin. Flag
// parsing is off so the plugin sees its arguments untouched; the arguments
// come from the raw argv rather than cobra's rewrite, so a --home before the
// noun is nsctl's and one after it is the plugin's.
func newPluginDispatchCommand(opts *Options, process hmdenv.Lookup, decl nsconfig.Plugin, inv invocation) *cobra.Command {
	short := "plugin " + decl.Name
	if decl.Summary != "" && !decl.Dev() {
		short = decl.Summary
	} else if decl.Dev() {
		short += " (dev build at " + decl.Path + ")"
	} else {
		short += " " + decl.Version
	}
	return &cobra.Command{
		Use:                decl.Name,
		Short:              short,
		Long:               pluginLong(decl),
		GroupID:            pluginGroupID,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// cobra did not parse --home for this command; resolve it the way
			// PersistentPreRun would have.
			opts.resolve(inv.home, process, func(msg string) {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
			})
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			bin, err := plugin.Resolve(home, decl)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			file, _ := hmdenv.Load(home)
			version := decl.Version
			if decl.Dev() {
				version = "dev"
			}
			env := plugin.Environ(os.Environ(), file, map[string]string{
				"HMD_HOME":                 home,
				plugin.EnvHome:             home,
				plugin.EnvVersion:          opts.Version,
				plugin.EnvBinary:           plugin.Executable(),
				plugin.EnvPluginName:       decl.Name,
				plugin.EnvPluginVersion:    version,
				plugin.EnvPluginDir:        dirOf(bin),
				"NSCTL_PLUGIN_DECLARED_IN": nsconfig.Path(home, process),
			})
			args := inv.args
			if inv.noun != decl.Name {
				// Reached through the help command or a test harness that
				// bypassed argv scanning: nothing to pass through.
				args = nil
			}
			code, err := plugin.Run(bin, args, env, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return nserr.Wrap(nserr.Fail, fmt.Errorf("plugin %s: %w", decl.Name, err))
			}
			return nserr.Silent(code)
		},
	}
}

func pluginLong(decl nsconfig.Plugin) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Declared plugin %q", decl.Name)
	if decl.Dev() {
		fmt.Fprintf(&b, ", a dev build at %s", decl.Path)
	} else {
		fmt.Fprintf(&b, " %s from %s", decl.Version, decl.Source)
	}
	b.WriteString(".\n\nEverything after the noun is passed to the plugin verbatim; ask it for help with:\n  nsctl " +
		decl.Name + " --help")
	return b.String()
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, string(os.PathSeparator)); i >= 0 {
		return path[:i]
	}
	return "."
}
