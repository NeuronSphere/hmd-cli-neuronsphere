package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// repoClassPath is the persistent --path every repoclass verb reads. It is a
// pointer shared by the group's subcommands because cobra binds a persistent
// flag once, on the group.
type repoClassPath struct {
	dir string
}

// resolve makes the path absolute against the working directory, so a
// message names a file the user can open.
func (p *repoClassPath) resolve() (string, error) {
	dir := p.dir
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nserr.Wrap(nserr.Usage, err)
	}
	return abs, nil
}

// stripOwnFlags is flag handling for the verbs whose arguments are another
// program's argv: it takes --path/--path=DIR, the root's --home and --help off the front, and
// hands everything else through untouched. A `--` ends the group's flags.
func (p *repoClassPath) stripOwnFlags(cmd *cobra.Command, args []string) ([]string, error) {
	rest := args
	for len(rest) > 0 {
		switch a := rest[0]; {
		case a == "--":
			return rest[1:], nil
		case a == "--help" || a == "-h":
			return nil, cmd.Help()
		case a == "--path":
			if len(rest) < 2 {
				return nil, nserr.New(nserr.Usage, "--path needs a directory")
			}
			p.dir = rest[1]
			rest = rest[2:]
		case strings.HasPrefix(a, "--path="):
			p.dir = strings.TrimPrefix(a, "--path=")
			rest = rest[1:]
		// The root's --home arrives raw here too, since nothing parsed it
		// on the way down. The repo class verbs read only --path, so it is
		// consumed and ignored rather than mistaken for the argv.
		case a == "--home":
			if len(rest) < 2 {
				return nil, nserr.New(nserr.Usage, "--home needs a directory")
			}
			rest = rest[2:]
		case strings.HasPrefix(a, "--home="):
			rest = rest[1:]
		default:
			return rest, nil
		}
	}
	return rest, nil
}

// open reads the repo class manifest under --path, turning "none" into a
// usage error that names init.
func (p *repoClassPath) open() (*bacon.Store, error) {
	dir, err := p.resolve()
	if err != nil {
		return nil, err
	}
	s, err := bacon.Open(dir)
	if errors.Is(err, bacon.ErrNotFound) {
		return nil, nserr.New(nserr.Usage, "%v. Create one with `nsctl repoclass init <name>`.", err)
	}
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	return s, nil
}

// newRepoClassCommand is NERD009 SPEC001's group: BACON authoring lives here,
// and no verb under it calls RequireHome (SPEC007) -- a user pointing this
// binary at their repository before installing anything is the entire motion.
func newRepoClassCommand(opts *Options) *cobra.Command {
	path := &repoClassPath{}
	group := &cobra.Command{
		Use:   "repoclass",
		Short: "Author and read a repo class's BACON manifest",
		Long: `Reads and writes the repo class manifest under --path (default: the current
directory): meta-data/manifest.json, or meta-data/manifest.toml and a repo-root
neuronsphere.toml for reading. Every write goes back to the file it was read
from with every key it did not touch intact, and prints one line naming the
file and the key it changed. Reads print a table, or JSON under --json.

None of these verbs needs HMD_HOME or a running platform.`,
		Example: `  nsctl repoclass init acme-api --description "The Acme public API"
  nsctl repoclass build set-mechanism external
  nsctl repoclass deploy set-command exec make deploy
  nsctl repoclass deploy set-image acme/ci-tools:1
  nsctl repoclass deploy add-dependency warehouse --resource-namespace acme.com --resource-definition-name sql-warehouse --resource-version 0.1.0
  nsctl repoclass license set Apache-2.0 --exclude src/python
  nsctl repoclass validate
  nsctl repoclass describe --json`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.PersistentFlags().StringVar(&path.dir, "path", ".", "The repo class's root directory")
	group.AddCommand(
		newRepoClassInitCommand(path),
		newRepoClassDescribeCommand(path),
		newRepoClassDetectCommand(path),
		newRepoClassValidateCommand(path),
		newRepoClassBuildCommand(path),
		newRepoClassDeployCommand(path),
		newRepoClassLocalCommand(path),
		newRepoClassTestCommand(path),
		newRepoClassDiscoveryCommand(path),
		newRepoClassLicenseCommand(path),
		newRepoClassAccessCommand(path),
	)
	return group
}

// wrote is the one line a write prints (SPEC008).
func wrote(cmd *cobra.Command, s *bacon.Store, key string) {
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s %s\n", s.Rel, key)
}

// saveAndReport is every write verb's tail.
func saveAndReport(cmd *cobra.Command, s *bacon.Store, key string) error {
	if err := s.Save(); err != nil {
		return err
	}
	wrote(cmd, s, key)
	return nil
}

func newRepoClassInitCommand(path *repoClassPath) *cobra.Command {
	var description, format, at string
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Write the minimum viable repo class manifest",
		Long: `Writes name, description and an empty build section -- the three members the
BACON schema requires -- to meta-data/manifest.json, and meta-data/VERSION as
0.1 when there is none. Refuses when a manifest already exists.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "json" {
				return nserr.New(nserr.Usage, "--format %s is not implemented yet (NERD009 SPEC006); only json is", format)
			}
			if at != "meta-data" {
				return nserr.New(nserr.Usage, "--at %s is not implemented yet (NERD009 SPEC006); only meta-data is", at)
			}
			dir, err := path.resolve()
			if err != nil {
				return err
			}
			s, err := bacon.Init(dir, args[0], description)
			if err != nil {
				return err
			}
			wrote(cmd, s, "name, description, build")
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "The repo class's one-line description")
	cmd.Flags().StringVar(&format, "format", "json", "Manifest format: json (toml not implemented yet)")
	cmd.Flags().StringVar(&at, "at", "meta-data", "Where to put it: meta-data (root not implemented yet)")
	return cmd
}

func newRepoClassDescribeCommand(path *repoClassPath) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "describe",
		Short: "Print the normalized summary of the repo class",
		Long: `The summary hmd describe prints -- name, description, discovery, resources,
dependencies -- as a table, or as the same JSON document under --json. Each
dependency also carries its resource block, and a test section is reported
when present; both are additions the Python omits.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			summary := bacon.Describe(s.Doc)
			if asJSON {
				data, err := bacon.Encode(summary)
				if err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			renderDescribe(cmd.OutOrStdout(), s, summary)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the summary as JSON")
	return cmd
}

// renderDescribe is the table form: what an engineer reads at a glance.
func renderDescribe(out io.Writer, s *bacon.Store, summary *bacon.Object) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	name, _ := summary.String("name")
	description, _ := summary.String("description")
	fmt.Fprintf(w, "name\t%s\n", name)
	fmt.Fprintf(w, "description\t%s\n", description)
	fmt.Fprintf(w, "manifest\t%s\n", s.Rel)
	if disc, ok := summary.Object("discovery"); ok {
		if sum, ok := disc.String("summary"); ok && sum != description {
			fmt.Fprintf(w, "summary\t%s\n", sum)
		}
	}
	if deploy, ok := s.Doc.Object("deploy"); ok {
		if image, ok := deploy.String("image"); ok {
			fmt.Fprintf(w, "image\t%s\n", image)
		}
		if cmds, ok := deploy.Array("commands"); ok {
			fmt.Fprintf(w, "deploy\t%s\n", joinCommands(cmds))
		}
	}
	if test, ok := summary.Object("test"); ok {
		if cmds, ok := test.Array("commands"); ok {
			fmt.Fprintf(w, "test\t%s\n", joinCommands(cmds))
		}
	}
	if raw, ok := s.Doc.Get("license"); ok {
		switch l := raw.(type) {
		case string:
			fmt.Fprintf(w, "license\t%s\n", l)
		case *bacon.Object:
			spdx, _ := l.String("spdx")
			line := spdx
			if ex, ok := l.Array("exclude"); ok && len(ex) > 0 {
				parts := make([]string, 0, len(ex))
				for _, e := range ex {
					parts = append(parts, fmt.Sprint(e))
				}
				line += "  (excludes " + strings.Join(parts, ", ") + ")"
			}
			fmt.Fprintf(w, "license\t%s\n", line)
		}
	}
	w.Flush()

	if deps, ok := summary.Array("dependencies"); ok {
		fmt.Fprintln(out, "\nDependencies:")
		dw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(dw, "  ROLE\tREQUIRED\tRESOURCE\tREPO CLASS")
		for _, d := range deps {
			dep := d.(*bacon.Object)
			role, _ := dep.String("name")
			req, _ := dep.Get("required")
			class, _ := dep.String("repo_class_name")
			resource := "-"
			if res, ok := dep.Object("resource"); ok {
				ns, _ := res.String("resource_namespace")
				rn, _ := res.String("resource_definition_name")
				ver, _ := res.String("version")
				resource = fmt.Sprintf("%s/%s %s", ns, rn, ver)
			}
			if class == "" {
				class = "-"
			}
			fmt.Fprintf(dw, "  %s\t%v\t%s\t%s\n", role, req, resource, class)
		}
		dw.Flush()
	}
	if resources, ok := summary.Array("resources"); ok {
		fmt.Fprintln(out, "\nResources:")
		rw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(rw, "  NAME\tTYPE")
		for _, r := range resources {
			res := r.(*bacon.Object)
			name, _ := res.String("name")
			ns, _ := res.String("resource_namespace")
			rn, _ := res.String("resource_definition_name")
			ver, _ := res.String("version")
			fmt.Fprintf(rw, "  %s\t%s/%s %s\n", name, ns, rn, ver)
		}
		rw.Flush()
	}
	if entries := bacon.ReadAccess(s.Doc); len(entries) > 0 {
		bacon.SortAccess(entries)
		fmt.Fprintln(out, "\nAccess:")
		aw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(aw, "  NAME\tURL\tUSERNAME\tCREDENTIAL")
		for _, e := range entries {
			fmt.Fprintf(aw, "  %s\t%s\t%s\t%s\n",
				e.Name, orDash(e.URL), orDash(e.Username), accessCredentialLabel(e))
		}
		aw.Flush()
	}
	if disc, ok := summary.Object("discovery"); ok {
		if caps, ok := disc.Array("capabilities"); ok {
			fmt.Fprintln(out, "\nCapabilities:")
			cw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(cw, "  NAME\tKIND\tDESCRIPTION")
			for _, c := range caps {
				cap, ok := c.(*bacon.Object)
				if !ok {
					continue
				}
				name, _ := cap.String("name")
				kind, _ := cap.String("kind")
				desc, _ := cap.String("description")
				fmt.Fprintf(cw, "  %s\t%s\t%s\n", name, kind, desc)
			}
			cw.Flush()
		}
		if eps, ok := disc.Array("entry_points"); ok {
			fmt.Fprintln(out, "\nEntry points:")
			for _, e := range eps {
				ep, ok := e.(*bacon.Object)
				if !ok {
					continue
				}
				p, _ := ep.String("path")
				desc, _ := ep.String("description")
				fmt.Fprintf(out, "  %s  %s\n", p, desc)
			}
		}
	}
}

func joinCommands(cmds []any) string {
	var parts []string
	for _, c := range cmds {
		argv, ok := c.([]any)
		if !ok {
			continue
		}
		var words []string
		for _, a := range argv {
			if s, ok := a.(string); ok {
				words = append(words, s)
			} else {
				data, _ := json.Marshal(a)
				words = append(words, string(data))
			}
		}
		parts = append(parts, strings.Join(words, " "))
	}
	return strings.Join(parts, "; ")
}

func newRepoClassValidateCommand(path *repoClassPath) *cobra.Command {
	var strict, asJSON bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check the repo class manifest against the schema and the rules that predict failure",
		Long: `Runs a structural pass against the BACON schema and a semantic pass against
the rules that actually predict a failed deploy, reporting findings at three
severities: an error will fail a deploy, a warning will surprise you, a note is
inert. Advisory by default -- the exit code is 1 only on errors -- and --strict
promotes warnings to errors. Never rewrites the file.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			findings := bacon.Validate(s, bacon.Known{
				BundledClasses: bundled.RepoClasses(),
				ReservedNames:  manifest.ReservedNames(),
			})
			findings = append(findings, stackFindings(s.Dir)...)
			errs, warns, notes := bacon.Summary(findings)
			failed := errs > 0 || (strict && warns > 0)
			if asJSON {
				data, err := bacon.JSONFindings(findings)
				if err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
			} else {
				for _, f := range findings {
					fmt.Fprintln(cmd.OutOrStdout(), f.String())
				}
				verdict := "ok"
				if failed {
					verdict = "failed"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "validate: %s -- %s, %s, %s\n", verdict,
					counted(errs, "error", "errors"), counted(warns, "warning", "warnings"), counted(notes, "note", "notes"))
			}
			if failed {
				return nserr.New(nserr.Fail, "%s does not validate", s.Rel)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "Treat warnings as errors")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the findings as JSON")
	return cmd
}

// addCommandHint names the add-command verb for a section. Spelled out per
// section so every command string in this file resolves against the tree
// (TestEverySuggestedCommandExists), rather than formatted at runtime.
func addCommandHint(section string) string {
	switch section {
	case "build":
		return "`nsctl repoclass build add-command <tool>`"
	case "test":
		return "`nsctl repoclass test add-command <tool>`"
	default:
		return "`nsctl repoclass deploy add-command <tool>`"
	}
}

func counted(n int, one, many string) string {
	return fmt.Sprintf("%d %s", n, plural(n, one, many))
}

func newRepoClassBuildCommand(path *repoClassPath) *cobra.Command {
	group := &cobra.Command{
		Use:           "build",
		Short:         "Edit the build section",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newSetMechanismCommand(path, "build"),
		newAddCommandCommand(path, "build"),
		newRemoveCommandCommand(path, "build"),
	)
	return group
}

func newRepoClassTestCommand(path *repoClassPath) *cobra.Command {
	group := &cobra.Command{
		Use:           "test",
		Short:         "Edit the test section",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newSetExecCommand(path, "test"),
		newAddCommandCommand(path, "test"),
		newRemoveCommandCommand(path, "test"),
	)
	return group
}

func newSetMechanismCommand(path *repoClassPath, section string) *cobra.Command {
	return &cobra.Command{
		Use:           "set-mechanism <tool_set|external>",
		Short:         "Record whether the " + section + " is done by the tool set or by something else",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.SetMechanism(s.Doc, section, args[0])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newAddCommandCommand(path *repoClassPath, section string) *cobra.Command {
	return &cobra.Command{
		Use:   "add-command <tool> [args...]",
		Short: "Add or replace a tool in " + section + ".commands",
		// The tool's own flags are its arguments, not this command's, so
		// cobra does not parse them; --path and --help are picked off by hand.
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			args, err := path.stripOwnFlags(cmd, args)
			if err != nil {
				return err
			}
			if len(args) < 1 {
				return nserr.New(nserr.Usage, "add-command needs a tool name")
			}
			s, err := path.open()
			if err != nil {
				return err
			}
			key, err := bacon.AddCommand(s.Doc, section, args[0], args[1:])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newRemoveCommandCommand(path *repoClassPath, section string) *cobra.Command {
	return &cobra.Command{
		Use:           "remove-command <tool>",
		Short:         "Remove a tool from " + section + ".commands",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, removed, err := bacon.RemoveCommand(s.Doc, section, args[0])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if !removed {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: %s names no %s command; nothing to remove\n", key, args[0])
				return nil
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

// newSetExecCommand is `<section> set-command exec <argv...>`: the whole
// phase becomes one exec entry (SPEC003), and what it replaced is said.
func newSetExecCommand(path *repoClassPath, section string) *cobra.Command {
	return &cobra.Command{
		Use:   "set-command exec <argv...>",
		Short: "Make the " + section + " one command run in the repo's own image",
		Long: `Sets ` + section + `.commands to exactly one exec entry: the argv given, run
as-is in deploy.image with the repo at /workspace. Any tool set commands that
were there are replaced, and named.`,
		// The argv's own flags are its arguments, not this command's, so
		// cobra does not parse them; --path and --help are picked off by hand.
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			args, err := path.stripOwnFlags(cmd, args)
			if err != nil {
				return err
			}
			if len(args) < 2 {
				return nserr.New(nserr.Usage, "set-command takes `exec <argv...>`")
			}
			if args[0] != "exec" {
				return nserr.New(nserr.Usage, "set-command takes `exec <argv...>`; a tool set command is added with %s", addCommandHint(section))
			}
			s, err := path.open()
			if err != nil {
				return err
			}
			key, replaced, err := bacon.SetExecCommand(s.Doc, section, args[1:])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if err := s.Save(); err != nil {
				return err
			}
			if len(replaced) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: replaced %s\n", joinCommands(replaced))
			}
			wrote(cmd, s, key)
			return nil
		},
	}
}

// stackFindings is NERD019 SPEC006: for a manifest with a `local` section
// that names companions, the lock must cover every want and every role must
// be bound, external, or pinned. A manifest with no local section, or an
// empty one, is not a stack and gets nothing here.
func stackFindings(dir string) []bacon.Finding {
	spec, err := localspec.Load(dir)
	if err != nil || spec.Local.Version == 0 {
		// No `local` section at all: an ordinary RepoClass, not a stack.
		return nil
	}
	var out []bacon.Finding
	if len(spec.Local.Repos) == 0 {
		out = append(out, bacon.Finding{Severity: bacon.Error, Path: "local.repos",
			Message: "a stack declares at least one companion; this manifest declares none"})
	}
	l, err := lock.Read(dir)
	if err != nil {
		out = append(out, bacon.Finding{Severity: bacon.Error, Path: lock.FileName,
			Message: withLockRemedy(err).Error()})
		return out
	}
	missing, extra := lock.Check(l, spec.Wants())
	if len(missing) > 0 {
		out = append(out, bacon.Finding{Severity: bacon.Error, Path: lock.FileName,
			Message: "stale: declared but not pinned: " + strings.Join(missing, ", ") + " -- run `nsctl lock`"})
	}
	if len(extra) > 0 {
		out = append(out, bacon.Finding{Severity: bacon.Warning, Path: lock.FileName,
			Message: "pins " + strings.Join(extra, ", ") + ", which the manifest no longer declares -- run `nsctl lock`"})
	}
	for _, w := range spec.Wants() {
		if len(w.Satisfies) == 0 || w.Bind != "" || w.External {
			continue
		}
		if _, ok := l.Entry(w.RepoClassName); !ok {
			out = append(out, bacon.Finding{Severity: bacon.Error, Path: "deploy.dependencies." + w.Key,
				Message: "neither bound, external, nor pinned: `stack add` would refuse it; add `bind`, `external: true`, or pin " + w.RepoClassName})
		}
	}
	for _, e := range l.Resolved {
		if e.Digest == "" {
			out = append(out, bacon.Finding{Severity: bacon.Note, Path: lock.FileName,
				Message: e.RepoClassName + "@" + e.Version + " has no digest yet; `stack build` records one when it sees the bytes"})
		}
	}
	return out
}

// accessCredentialLabel is what an access entry says about its credential, and
// deliberately never the credential: a repository has no environment to resolve
// one in, and `nsctl env credentials --reveal` is the one thing that prints a
// value.
func accessCredentialLabel(e bacon.AccessEntry) string {
	if e.Secret == nil {
		return "none"
	}
	where := e.Secret.Key
	if e.Secret.Output != "" {
		where = "output:" + e.Secret.Output
	}
	if e.Secret.Property != "" {
		where += "#" + e.Secret.Property
	}
	return e.Secret.Store + " " + where
}
