package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/modelstore"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

// newModelPerspectiveCommand is the perspective IR (NERD033 SPEC006):
// which perspectives are in effect, what was derived and why, edits to a
// derivation, and materialising one into a repository.
func newModelPerspectiveCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "perspective",
		Aliases: []string{"perspectives"},
		Short:   "List, derive, edit and materialise perspectives",
		Long: `A perspective is the data a generator needs beside the core .hms schema to
produce one technology's manifestation of a noun: a table's storage format, a
column's physical type. nsctl embeds none. A repository declares one under
src/perspectives/<name>.perspective.json; otherwise nsctl derives it from the
files that realise the model (Trino DDL in transforms, dbt projects), with the
evidence for each piece, and keeps it with the inspection under HMD_HOME until
it is edited and materialised into a repository. NERD033.`,
		Example: `  nsctl model perspective list ~/src
  nsctl model perspective derive ~/src --evidence
  nsctl model perspective show trino ~/src
  nsctl model perspective edit trino rename-key format storage_format --path ~/src
  nsctl model perspective materialise trino ~/src --to ~/src/hmd-lang-reporting`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newPerspectiveListCommand(opts), newPerspectiveDeriveCommand(opts), newPerspectiveShowCommand(opts),
		newPerspectiveEditCommand(opts), newPerspectiveMaterialiseCommand(opts))
	return cmd
}

func pathsOr(args []string) []string {
	if len(args) == 0 {
		return []string{"."}
	}
	return args
}

func encodeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	return nil
}

// derivationOf finds a perspective's derivation in an inspection.
func derivationOf(res *inspection, name string) *perspective.Derivation {
	for _, dv := range res.Perspectives {
		if dv.Definition.Name == name {
			return dv
		}
	}
	return nil
}

func reviewCount(dv *perspective.Derivation) int {
	n := 0
	if dv == nil {
		return 0
	}
	for _, e := range dv.Evidence {
		if e.Review {
			n++
		}
	}
	return n
}

func newPerspectiveListCommand(opts *Options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:           "list [path...]",
		Short:         "List the perspectives in effect, declared or derived",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runInspection(cmd.Context(), opts, pathsOr(args), false, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			r, out := res.registry, cmd.OutOrStdout()
			if asJSON {
				var defs []*perspective.Definition
				for _, n := range r.Names() {
					defs = append(defs, r.Get(n))
				}
				return encodeJSON(out, defs)
			}
			bound := map[string]int{}
			for _, n := range res.Model.Nouns {
				seen := map[string]bool{}
				for _, b := range n.Bindings {
					if !seen[b.Perspective] {
						seen[b.Perspective] = true
						bound[b.Perspective]++
					}
				}
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "PERSPECTIVE\tFROM\tBINDING KEY\tNOUNS\tATTRIBUTE KEYS\tREVIEW")
			for _, n := range r.Names() {
				d := r.Get(n)
				from, review := d.Origin, "-"
				if dv := derivationOf(res, n); dv != nil && d.Origin == perspective.OriginDerived {
					from = dv.Status
					review = fmt.Sprint(reviewCount(dv))
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", d.Name, from, orDash(d.BindingKey), bound[n],
					orDash(strings.Join(d.Keys(perspective.Attribute), ",")), review)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the definitions as JSON")
	return cmd
}

func newPerspectiveDeriveCommand(opts *Options) *cobra.Command {
	var asJSON, evidence bool
	cmd := &cobra.Command{
		Use:   "derive [path...]",
		Short: "Derive the perspectives no repository declares, and show why",
		Long: `Inspects the directories again and derives a definition for every perspective
no inspected repository declares: its keys, each key's kind, enum values, the
core .hms type of each physical type, the binding key and name pattern. Each
piece carries the rule that produced it and the objects that support it; a
piece marked for review is one the files could not decide. Edits recorded with
` + "`nsctl model perspective edit`" + ` are replayed. NERD033 SPEC003.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runInspection(cmd.Context(), opts, pathsOr(args), true, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				list := res.Perspectives
				if list == nil {
					list = []*perspective.Derivation{}
				}
				return encodeJSON(out, list)
			}
			if len(res.Perspectives) == 0 {
				fmt.Fprintln(out, "Nothing to derive: every perspective in use is declared by a repository.")
			}
			for _, dv := range res.Perspectives {
				renderDerivation(out, dv, evidence)
			}
			for _, e := range res.Stale {
				fmt.Fprintf(out, "stale edit, no longer applies: %s %s %s\n", e.Perspective, e.Op, strings.TrimSpace(e.Key+" "+e.Value+" "+e.To))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the derivations as JSON")
	cmd.Flags().BoolVar(&evidence, "evidence", false, "Show the rule and support behind every piece")
	return cmd
}

func renderDerivation(out io.Writer, dv *perspective.Derivation, evidence bool) {
	d := dv.Definition
	fmt.Fprintf(out, "%s  (%s", d.Name, dv.Status)
	if n := reviewCount(dv); n > 0 {
		fmt.Fprintf(out, ", %d to review", n)
	}
	fmt.Fprintln(out, ")")
	if d.BindingKey != "" {
		ext, _ := d.Extension(perspective.Entity, d.BindingKey)
		var vals []string
		for _, v := range ext.EnumValues {
			vals = append(vals, v.ID)
		}
		if len(vals) > 0 {
			fmt.Fprintf(out, "  binding key  %s: %s\n", d.BindingKey, strings.Join(vals, " -> "))
		} else {
			fmt.Fprintf(out, "  binding key  %s\n", d.BindingKey)
		}
	}
	if d.NamePattern != nil {
		fmt.Fprintf(out, "  name pattern %s.%s\n", patternString(d.NamePattern.Schema), patternString(d.NamePattern.Table))
	}
	byItem := map[string][]perspective.Evidence{}
	for _, e := range dv.Evidence {
		byItem[e.Item] = append(byItem[e.Item], e)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, at := range []perspective.Attach{perspective.Entity, perspective.Attribute} {
		for _, k := range d.Keys(at) {
			ext, _ := d.Extension(at, k)
			kind := ext.ExtensionType
			var vals []string
			for _, v := range ext.EnumValues {
				s := v.ID
				if v.HMSType != "" {
					s += "→" + v.HMSType
				}
				for _, e := range byItem[string(at)+"."+k+"="+v.ID] {
					if e.Review {
						s += " (review)"
					}
				}
				vals = append(vals, s)
			}
			if len(vals) > 0 && k != d.BindingKey {
				kind += " " + strings.Join(vals, ", ")
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\n", at, k, kind)
			if evidence {
				for _, e := range byItem[string(at)+"."+k] {
					fmt.Fprintf(w, "  \t\t  %s (%d)%s\n", e.Rule, e.Support, examples(e))
				}
			}
		}
	}
	w.Flush()
	if evidence {
		for _, item := range []string{"binding_key", "name_pattern", "primary"} {
			for _, e := range byItem[item] {
				fmt.Fprintf(out, "  %s: %s (%d)%s\n", item, e.Rule, e.Support, examples(e))
			}
		}
		var typed []string
		for item := range byItem {
			if strings.Contains(item, "=") {
				typed = append(typed, item)
			}
		}
		sort.Strings(typed)
		for _, item := range typed {
			for _, e := range byItem[item] {
				line := fmt.Sprintf("  %s: %s (%d)", item, e.Rule, e.Support)
				if len(e.Exceptions) > 0 {
					line += "; exceptions: " + strings.Join(e.Exceptions, "; ")
				}
				fmt.Fprintln(out, line)
			}
		}
	}
	fmt.Fprintln(out)
}

func examples(e perspective.Evidence) string {
	if len(e.Examples) == 0 {
		return ""
	}
	return "  e.g. " + strings.Join(e.Examples, ", ")
}

func patternString(parts []perspective.NamePart) string {
	var b strings.Builder
	for _, p := range parts {
		switch {
		case p.Lit != "":
			b.WriteString(p.Lit)
		case p.Ref != "":
			b.WriteString("<" + p.Ref + ">")
		case p.Runtime != "":
			b.WriteString("{" + p.Runtime + "}")
		}
	}
	return b.String()
}

func newPerspectiveShowCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "show <perspective> [path...]",
		Short:         "Print a perspective's definition",
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runInspection(cmd.Context(), opts, pathsOr(args[1:]), false, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			d := res.registry.Get(args[0])
			if d == nil {
				return nserr.New(nserr.Usage, "no perspective %q; nsctl model perspective list shows the ones in effect", args[0])
			}
			_, err = cmd.OutOrStdout().Write(d.Marshal())
			return err
		},
	}
	return cmd
}

func newPerspectiveEditCommand(opts *Options) *cobra.Command {
	var paths []string
	cmd := &cobra.Command{
		Use:   "edit <perspective> <op> <arg>...",
		Short: "Record an edit to a derived perspective",
		Long: `Records an edit to a derived perspective, replayed every time it is derived
again, so it survives changes to the files:

  rename <new-name>             rename the perspective
  rename-key <key> <new-key>    rename a key (its values follow)
  drop-key <key>                drop a key (its values go)
  hms-type <enum-value> <type>  set the core .hms type of a data type value

Edits are kept under HMD_HOME for the inspected directories. An edit that
does not apply to the current derivation is refused. NERD033 SPEC005.`,
		Args:          cobra.MinimumNArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Home == "" {
				return nserr.New(nserr.Usage, "perspective edits are kept under HMD_HOME. Export it or pass --home <path>")
			}
			e, err := perspective.ParseEdit(args[0], args[1], args[2:])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			res, err := inspectWith(cmd.Context(), opts, pathsOr(paths), true, &e, io.Discard)
			if err != nil {
				return err
			}
			for _, s := range res.Stale {
				if s == e {
					return nserr.New(nserr.Usage, "edit does not apply: %s has no %s to %s", e.Perspective, strings.TrimSpace(e.Key+" "+e.Value), e.Op)
				}
			}
			if derivationOf(res, renamedTo(e)) == nil {
				return nserr.New(nserr.Usage, "no derived perspective %q; a declared perspective is edited in its repository", e.Perspective)
			}
			store, err := openStore(opts)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.AddEdit(modelstore.ScopeKey(roots(res.Sources)), e, time.Now()); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			if _, err := runInspection(cmd.Context(), opts, pathsOr(paths), true, io.Discard); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded: %s %s %s\n", e.Perspective, e.Op, strings.Join(args[2:], " "))
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&paths, "path", nil, "The inspected directories the edit belongs to (default: the current one)")
	return cmd
}

// renamedTo is the perspective's name after an edit.
func renamedTo(e perspective.Edit) string {
	if e.Op == "rename" {
		return e.To
	}
	return e.Perspective
}

func newPerspectiveMaterialiseCommand(opts *Options) *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:     "materialise <perspective> [path...] --to <repository>",
		Aliases: []string{"materialize"},
		Short:   "Write a perspective's definition and values into a repository",
		Long: `Writes src/perspectives/<perspective>.perspective.json and one
<noun>.<perspective>.hms sidecar per noun (under src/schemas/<namespace>/) into
the repository named by --to, and nowhere else. From then on that repository
declares the perspective: inspecting it reads the sidecars as declared values,
and a file that later disagrees with them is reported. NERD033 SPEC005.`,
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return nserr.New(nserr.Usage, "--to <repository> is required: materialise writes only where it is told to")
			}
			if info, err := os.Stat(to); err != nil || !info.IsDir() {
				return nserr.New(nserr.Usage, "--to %s is not a directory", to)
			}
			res, err := runInspection(cmd.Context(), opts, pathsOr(args[1:]), false, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			name := args[0]
			d := res.registry.Get(name)
			if d == nil {
				return nserr.New(nserr.Usage, "no perspective %q; nsctl model perspective list shows the ones in effect", name)
			}
			files := []hmsFile{{filepath.Join(perspective.Dir, d.Name+perspective.Suffix), d.Marshal()}}
			for _, n := range res.Model.Nouns {
				if data, ok := model.ExportSidecar(n, name, d.BindingKey); ok {
					files = append(files, hmsFile{filepath.Join(sidecarDir(n.ID), n.ID.Name+"."+name+".hms"), data})
				}
			}
			if err := writeFiles(cmd.OutOrStdout(), to, files); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "materialised %s: a definition and %d sidecars in %s\n", name, len(files)-1, to)
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "The repository to write into")
	return cmd
}

// exportContexts prints, or writes under dir as <namespace>.<name>.json,
// each noun as the document a generator reads: its .hms schema with each
// perspective's values under extensions.<perspective>, as hmd-schema-loader
// merges sidecars (NERD033 SPEC004).
func exportContexts(out io.Writer, dir string, nouns []*model.Noun, r *perspective.Registry, lossy bool) error {
	var errs []string
	var files []hmsFile
	for _, n := range nouns {
		core, err := model.ExportHMS(n, lossy)
		var ee *model.ExportError
		if errors.As(err, &ee) {
			errs = append(errs, ee.Error())
			continue
		}
		if err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(core, &doc); err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		ext, _ := doc["extensions"].(map[string]any)
		if ext == nil {
			ext = map[string]any{}
		}
		for _, b := range n.Bindings {
			if _, done := ext[b.Perspective]; done {
				continue
			}
			key := ""
			if d := r.Get(b.Perspective); d != nil {
				key = d.BindingKey
			}
			data, ok := model.ExportSidecar(n, b.Perspective, key)
			if !ok {
				continue
			}
			var side map[string]any
			_ = json.Unmarshal(data, &side)
			delete(side, "namespace")
			delete(side, "name")
			ext[b.Perspective] = side
		}
		if len(ext) > 0 {
			doc["extensions"] = ext
		}
		data, _ := json.MarshalIndent(doc, "", "  ")
		files = append(files, hmsFile{n.ID.String() + ".json", append(data, '\n')})
	}
	if err := writeFiles(out, dir, files); err != nil {
		return err
	}
	if len(errs) > 0 {
		return nserr.New(nserr.Usage, "%s", strings.Join(errs, "\n"))
	}
	return nil
}
