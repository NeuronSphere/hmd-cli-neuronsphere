package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/agentskills"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/installitems"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/plugin"
)

// librarianScheme prefixes a plugin source that is a RepoClass artifact in
// the Artifact Librarian. NERD031 SPEC002.
const librarianScheme = "librarian:"

// isLibrarianSource reports whether a typed reference or declared source is
// a librarian artifact rather than an OCI plugin.
func isLibrarianSource(ref string) bool { return strings.HasPrefix(ref, librarianScheme) }

// installFlags are the flags a librarian install takes beyond the OCI ones.
type installFlags struct {
	libs     librarians
	host     string
	scope    string
	project  string
	repoHome string
}

func (f *installFlags) bind(cmd *cobra.Command) {
	f.libs.bindCloud(cmd)
	f.libs.bindTenant(cmd)
	cmd.Flags().StringVar(&f.host, "host", string(agentskills.All),
		"for an artifact's agent skills: codex, claude or all")
	cmd.Flags().StringVar(&f.scope, "scope", string(agentskills.User),
		"for an artifact's agent skills: user, or project (with --path)")
	cmd.Flags().StringVar(&f.project, "path", "", "project directory for --scope project (default: the current directory)")
	cmd.Flags().StringVar(&f.repoHome, "repo-home", "",
		"where an artifact's git items are cloned (default: $HMD_REPO_HOME)")
}

// childEnviron is what uv and git run in: the environment a plugin sees
// (NERD018 SPEC005) with HMD_HOME resolved. PATH comes through the injected
// lookup when it names one, which is the process's own PATH in production
// and a test's fake in a test.
func childEnviron(opts *Options, home string) []string {
	file, _ := hmdenv.Load(home)
	extra := map[string]string{"HMD_HOME": home}
	if p := opts.Lookup("PATH"); p != "" {
		extra["PATH"] = p
	}
	if h := opts.Lookup("HOME"); h != "" {
		extra["HOME"] = h
	}
	return plugin.Environ(os.Environ(), file, extra)
}

// installFromLibrarian is `plugin install librarian:<class>[@<spec>]`.
func installFromLibrarian(cmd *cobra.Command, opts *Options, flags *installFlags, typed string) error {
	home, err := opts.RequireHome()
	if err != nil {
		return err
	}
	req, err := parseArtifactRequest(strings.TrimPrefix(typed, librarianScheme))
	if err != nil {
		return err
	}
	cloud, err := flags.libs.cloud(cmd, opts)
	if err != nil {
		return err
	}
	spec, err := resolveRef(cmd, opts, &flags.libs, cloud, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Fetching %s from %s\n", spec, cloud.BaseURL)
	data, err := cloud.Fetch(cmd.Context(), spec.ContentPath())
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := artifact.Invalidate(home, spec.Name, spec.Version); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	root, err := artifact.Store(home, spec.Name, spec.Version, data)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	sec, err := installitems.ParseDir(root)
	if errors.Is(err, installitems.ErrNoSection) {
		return nserr.New(nserr.Usage, "%s@%s %v; it is deployed with `nsctl env`, not installed as a plugin",
			spec.Name, spec.Version, err)
	}
	if err != nil {
		return nserr.Wrap(nserr.Usage, fmt.Errorf("%s@%s: %w", spec.Name, spec.Version, err))
	}

	cfg, err := loadConfigOrEmpty(opts)
	if err != nil {
		return err
	}
	if prev, ok := cfg.Plugins[spec.Name]; ok && len(prev.Items) == 0 {
		return nserr.New(nserr.Usage, "%s is already declared as a single-binary plugin; `nsctl plugin remove %s` first",
			spec.Name, spec.Name)
	}
	env, err := installEnv(cmd, opts, flags, home, cfg, spec.Name)
	if err != nil {
		return err
	}
	items, err := installitems.Install(cmd.Context(), env, spec.Name, spec.Version, root, sec)
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}

	sum := sha256.Sum256(data)
	prevItems := cfg.Plugins[spec.Name].Items
	cfg.SetPlugin(nsconfig.Plugin{Name: spec.Name, Source: librarianScheme + spec.Name, Version: spec.Version,
		Digest: "sha256:" + hex.EncodeToString(sum[:]), Items: items})
	if err := nsconfig.Save(home, opts.Lookup, cfg); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := installitems.Supersede(home, spec.Name, spec.Version, prevItems, items, cmd.ErrOrStderr()); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not remove previous versions of %s: %v\n", spec.Name, err)
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Installed %s %s\n", spec.Name, spec.Version)
	for _, it := range items {
		fmt.Fprintf(w, "  %s\n", describeItem(it))
	}
	fmt.Fprintf(w, "Declared in %s\n", nsconfig.Path(home, opts.Lookup))
	for _, it := range items {
		if it.Kind == nsconfig.KindCommand {
			fmt.Fprintf(w, "Run: nsctl %s --help\n", it.Noun)
		}
	}
	return nil
}

// installEnv is the host side of an install: where things go, and which
// nouns the tree will not attach.
func installEnv(cmd *cobra.Command, opts *Options, flags *installFlags, home string,
	cfg *nsconfig.Config, class string) (installitems.Env, error) {

	scope := agentskills.Scope(flags.scope)
	project := flags.project
	if scope == agentskills.Project && project == "" {
		wd, err := os.Getwd()
		if err != nil {
			return installitems.Env{}, nserr.Wrap(nserr.Fail, err)
		}
		project = wd
	}
	if _, err := agentskills.Hosts(agentskills.Host(flags.host)); err != nil {
		return installitems.Env{}, nserr.Wrap(nserr.Usage, err)
	}
	repoHome := flags.repoHome
	if repoHome == "" {
		repoHome = opts.Lookup("HMD_REPO_HOME")
	}
	root := cmd.Root()
	return installitems.Env{
		Home:     home,
		RepoHome: repoHome,
		Environ:  childEnviron(opts, home),
		Stdout:   cmd.OutOrStdout(),
		Stderr:   cmd.ErrOrStderr(),
		Skills: installitems.SkillTarget{Host: agentskills.Host(flags.host), Scope: scope,
			Project: project, UserHome: opts.Lookup("HOME")},
		CheckNoun: func(noun string) error {
			if err := reservedNoun(root, noun); err != nil {
				return err
			}
			if owner, ok := cfg.NounOwner(noun); ok && owner != class {
				return fmt.Errorf("plugin %s already answers to it", owner)
			}
			return nil
		},
	}, nil
}

// describeItem is one line of install output and of `plugin list`.
func describeItem(it nsconfig.PluginItem) string {
	switch it.Kind {
	case nsconfig.KindCommand:
		return fmt.Sprintf("command  nsctl %s (%s) -- %s", it.Noun, it.Runtime, it.Summary)
	case nsconfig.KindSkills:
		return fmt.Sprintf("skills   %s", strings.Join(it.Paths, ", "))
	case nsconfig.KindDocs:
		where := it.Path
		if it.Clone {
			where += " (your clone)"
		}
		return fmt.Sprintf("docs     %q at %s", it.Title, where)
	}
	return it.Kind
}

// itemsState is `plugin list`'s state for a plugin with items: installed
// when every command's target and every placed path is still there.
func itemsState(items []nsconfig.PluginItem) string {
	for _, it := range items {
		check := it.Path
		if it.Target != "" {
			check = it.Target
		}
		if check == "" {
			continue
		}
		if _, err := os.Stat(check); err != nil {
			return string(plugin.StateMissing)
		}
	}
	return string(plugin.StateInstalled)
}
