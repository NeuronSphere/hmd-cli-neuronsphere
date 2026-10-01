package cmd

import (
	"context"
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

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/derive"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/dbt"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/hms"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/nsexport"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/nstransform"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/modelstore"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

// inspectors is every inspector nsctl model inspect runs. Adding one is a new
// package and a line here (NERD032 SPEC001).
func inspectors() []inspect.Inspector {
	return []inspect.Inspector{
		hms.Inspector{},
		nstransform.Inspector{},
		dbt.Inspector{},
		nsexport.Inspector{},
	}
}

// declared is the perspective definitions the source repositories declare
// under src/perspectives. nsctl embeds none (NERD033 SPEC001).
func declared(srcs []inspect.Source, warn io.Writer) *perspective.Registry {
	r := perspective.New()
	for _, s := range srcs {
		if s.FS == nil {
			continue
		}
		for _, err := range r.LoadDir(s.FS, perspective.Dir, s.Repo) {
			fmt.Fprintf(warn, "warning: perspective definition skipped: %v\n", err)
		}
	}
	return r
}

// effective is the declared definitions plus the derived ones nothing
// declares.
func effective(decl *perspective.Registry, derived []*perspective.Derivation) *perspective.Registry {
	r := perspective.New()
	for _, dv := range derived {
		r.Add(dv.Definition)
	}
	for _, n := range decl.Names() {
		r.Add(decl.Get(n))
	}
	return r
}

// inspectNow runs every inspector over the sources, derives the
// perspectives nothing declares (replaying edits), consolidates, and
// validates perspective values against their definitions.
func inspectNow(ctx context.Context, srcs []inspect.Source, decl *perspective.Registry, edits []perspective.Edit) ([]model.Observation, []inspect.Report, *model.Model, derive.Result, *perspective.Registry) {
	obs, reports := inspect.Run(ctx, srcs, inspectors())
	res := derive.Run(obs, decl, edits)
	reg := effective(decl, res.Derivations)
	m := model.Consolidate(res.Observations)
	m.Disagreements = append(m.Disagreements, perspective.Validate(m, reg)...)
	model.SortDisagreements(m.Disagreements)
	return obs, reports, m, res, reg
}

// attributeLimit caps the attributes printed per noun when no noun is named.
const attributeLimit = 25

func newModelInspectCommand(opts *Options) *cobra.Command {
	var asJSON, sources, refresh, asHMS, lossy, asContext bool
	var outDir string
	cmd := &cobra.Command{
		Use:   "inspect [path|noun]...",
		Short: "Show the data model a set of repositories describes",
		Long: `Reads .hms language pack schemas and their perspective sidecars, NeuronSphere
transform SQL and dbt projects in the named directories (default: the current
one) and prints the logical model they describe: nouns and their .hms
attributes, each noun's perspective bindings (the Trino tables, views and dbt
models that carry it, with their physical types), lineage between nouns, and
every place the artifacts disagree. A directory that is not itself a repository
stands for the repositories directly inside it.

An argument that is not a directory names a noun to show in full: its fully
qualified name (hmd_lang_transform.transform_instance) or just its name.

Perspectives are not built in: a repository declares one under
src/perspectives/<name>.perspective.json, or it is derived from the files
(` + "`nsctl model perspective`" + `). NERD033.

With --hms the selected nouns are printed as an .hms document plus one
<name>.<perspective>.hms sidecar per perspective, and each perspective's
definition; with --out <dir> those files are written under <dir>, laid out as
src/schemas/<namespace>/ and src/perspectives/. With --context each noun is
printed as the one document a code generator reads: the .hms schema with each
perspective's values under extensions.<perspective>.

Each inspection is stored as a snapshot under HMD_HOME, keyed by the set of
directories inspected. Without --refresh the latest snapshot is shown; with it,
the directories are inspected again and a new snapshot is stored, which
` + "`nsctl model diff`" + ` compares with the one before. Without HMD_HOME
nothing is stored.

nsctl model inspect never writes to the inspected repositories. NERD032.`,
		Example: `  nsctl model inspect
  nsctl model inspect ../hmd-config-transform-reporting ../hmd-lang-transform
  nsctl model inspect ~/src --refresh
  nsctl model inspect ~/src ntc_instances_export --sources
  nsctl model inspect ~/src ntc_instances_export --hms --out /tmp/model`,
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, filters := splitInspectArgs(args)
			res, err := runInspection(cmd.Context(), opts, paths, refresh, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			nouns := res.Model.Nouns
			if len(filters) > 0 {
				nouns = matchNouns(res.Model, filters)
				if len(nouns) == 0 {
					return nserr.New(nserr.Usage, "no noun matches %s", strings.Join(filters, ", "))
				}
			}
			out := cmd.OutOrStdout()
			switch {
			case asContext:
				return exportContexts(out, outDir, nouns, res.registry, lossy)
			case asHMS || outDir != "":
				return exportHMS(out, outDir, nouns, res.registry, lossy)
			case asJSON:
				view := *res
				if len(filters) > 0 {
					m := *res.Model
					m.Nouns = nouns
					view.Model = &m
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(view); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
			default:
				renderInspection(out, res, nouns, len(filters) > 0, sources)
				for _, e := range res.Stale {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: perspective edit no longer applies: %s %s %s %s %s\n", e.Perspective, e.Op, e.Key, e.Value, e.To)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the model as JSON")
	cmd.Flags().BoolVar(&sources, "sources", false, "Show where every noun, attribute and link came from, and informational notes")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Inspect again and store a new snapshot")
	cmd.Flags().BoolVar(&asHMS, "hms", false, "Print the selected nouns as .hms documents and perspective sidecars")
	cmd.Flags().StringVar(&outDir, "out", "", "With --hms or --context, write the documents under this directory instead of printing them")
	cmd.Flags().BoolVar(&asContext, "context", false, "Print each selected noun as a generator context: .hms plus extensions.<perspective>")
	cmd.Flags().BoolVar(&lossy, "lossy", false, "With --hms, write attributes of unknown type as string instead of refusing")
	return cmd
}

// inspection is what one run of nsctl model inspect shows.
type inspection struct {
	Snapshot *modelstore.Snapshot `json:"snapshot,omitempty"`
	// Fresh is false when the model came from a stored snapshot rather than
	// from inspecting now.
	Fresh        bool             `json:"fresh"`
	Sources      []inspect.Source `json:"sources"`
	Reports      []inspect.Report `json:"reports,omitempty"`
	Observations int              `json:"observations"`
	Model        *model.Model     `json:"model"`
	// Perspectives are the ones derived for this inspection (NERD033).
	Perspectives []*perspective.Derivation `json:"perspectives,omitempty"`
	// Stale are edits that no longer apply to what was derived.
	Stale    []perspective.Edit `json:"stale_edits,omitempty"`
	registry *perspective.Registry
}

// splitInspectArgs separates directories from noun names.
func splitInspectArgs(args []string) (paths, filters []string) {
	for _, a := range args {
		if info, err := os.Stat(a); err == nil && info.IsDir() {
			paths = append(paths, a)
		} else {
			filters = append(filters, a)
		}
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	return paths, filters
}

func openStore(opts *Options) (*modelstore.Store, error) {
	if opts.Home == "" {
		return nil, nil
	}
	s, err := modelstore.Open(modelstore.Path(opts.Home))
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	return s, nil
}

func roots(srcs []inspect.Source) []string {
	out := make([]string, len(srcs))
	for i, s := range srcs {
		out[i] = s.Root
	}
	return out
}

// runInspection returns the latest snapshot for the paths, or inspects them
// (and stores the result when there is a store) when there is none or
// refresh is set.
func runInspection(ctx context.Context, opts *Options, paths []string, refresh bool, warn io.Writer) (*inspection, error) {
	return inspectWith(ctx, opts, paths, refresh, nil, warn)
}

// inspectWith is runInspection with an edit not yet stored replayed after
// the stored ones; with one, the result is never stored.
func inspectWith(ctx context.Context, opts *Options, paths []string, refresh bool, extra *perspective.Edit, warn io.Writer) (*inspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	srcs, err := inspect.Discover(paths)
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	decl := declared(srcs, warn)
	store, err := openStore(opts)
	if err != nil {
		return nil, err
	}
	var edits []perspective.Edit
	if store == nil {
		fmt.Fprintln(warn, "note: HMD_HOME is not set, so this inspection is not stored and cannot be diffed")
	} else {
		defer store.Close()
		if edits, err = store.Edits(modelstore.ScopeKey(roots(srcs))); err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
		if !refresh && extra == nil {
			snaps, err := store.Snapshots(modelstore.ScopeKey(roots(srcs)), 1)
			if err != nil {
				return nil, nserr.Wrap(nserr.Fail, err)
			}
			if len(snaps) == 1 {
				m, err := store.Model(snaps[0].ID)
				if err != nil {
					return nil, nserr.Wrap(nserr.Fail, err)
				}
				obs, err := store.Observations(snaps[0].ID)
				if err != nil {
					return nil, nserr.Wrap(nserr.Fail, err)
				}
				dvs, err := store.Derivations(snaps[0].ID)
				if err != nil {
					return nil, nserr.Wrap(nserr.Fail, err)
				}
				return &inspection{Snapshot: &snaps[0], Sources: srcs, Observations: len(obs), Model: m,
					Perspectives: dvs, registry: effective(decl, dvs)}, nil
			}
		}
	}
	if extra != nil {
		edits = append(edits, *extra)
	}
	obs, reports, m, dres, reg := inspectNow(ctx, srcs, decl, edits)
	res := &inspection{Fresh: true, Sources: srcs, Reports: reports, Observations: len(obs), Model: m,
		Perspectives: dres.Derivations, Stale: dres.Stale, registry: reg}
	if store != nil && extra == nil {
		revs := map[string]string{}
		for _, s := range srcs {
			if s.Revision != "" {
				revs[s.Repo] = s.Revision
			}
		}
		snap, err := store.Save(roots(srcs), revs, obs, res.Model, dres.Derivations, time.Now())
		if err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
		res.Snapshot = &snap
	}
	return res, nil
}

// matchNouns finds nouns by full name, by name alone, or by an alias.
func matchNouns(m *model.Model, filters []string) []*model.Noun {
	var out []*model.Noun
	for _, n := range m.Nouns {
		for _, f := range filters {
			if nounMatches(n, f) {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

func nounMatches(n *model.Noun, f string) bool {
	if n.ID.String() == f || n.ID.Name == f {
		return true
	}
	for _, a := range n.Aliases {
		if a.ID.String() == f || a.ID.Name == f {
			return true
		}
	}
	return false
}

// hmsFile is one document an export produces.
type hmsFile struct {
	path string
	data []byte
}

// sidecarDir is where a noun's .hms document and its sidecars live.
func sidecarDir(id model.ID) string {
	return filepath.Join(append([]string{"src", "schemas"}, strings.Split(id.Namespace, ".")...)...)
}

// hmsFiles is a noun's core .hms document and one sidecar per perspective
// it has bindings of, laid out as hmd-schema-loader expects them.
func hmsFiles(n *model.Noun, r *perspective.Registry, lossy bool) ([]hmsFile, error) {
	core, err := model.ExportHMS(n, lossy)
	if err != nil {
		return nil, err
	}
	dir := sidecarDir(n.ID)
	files := []hmsFile{{filepath.Join(dir, n.ID.Name+".hms"), core}}
	seen := map[string]bool{}
	for _, b := range n.Bindings {
		if seen[b.Perspective] {
			continue
		}
		seen[b.Perspective] = true
		key := ""
		if d := r.Get(b.Perspective); d != nil {
			key = d.BindingKey
		}
		if data, ok := model.ExportSidecar(n, b.Perspective, key); ok {
			files = append(files, hmsFile{filepath.Join(dir, n.ID.Name+"."+b.Perspective+".hms"), data})
		}
	}
	return files, nil
}

// definitionFiles is the definition of each perspective the nouns have
// bindings of, so that an export reads back without the repositories it
// came from.
func definitionFiles(nouns []*model.Noun, r *perspective.Registry) []hmsFile {
	var out []hmsFile
	seen := map[string]bool{}
	for _, n := range nouns {
		for _, b := range n.Bindings {
			if d := r.Get(b.Perspective); d != nil && !seen[d.Name] {
				seen[d.Name] = true
				out = append(out, hmsFile{filepath.Join(perspective.Dir, d.Name+perspective.Suffix), d.Marshal()})
			}
		}
	}
	return out
}

// exportHMS prints the selected nouns' documents, or writes them under dir.
func exportHMS(out io.Writer, dir string, nouns []*model.Noun, r *perspective.Registry, lossy bool) error {
	var errs []string
	var files []hmsFile
	for _, n := range nouns {
		nf, err := hmsFiles(n, r, lossy)
		var ee *model.ExportError
		if errors.As(err, &ee) {
			errs = append(errs, ee.Error())
			continue
		}
		if err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		files = append(files, nf...)
	}
	files = append(files, definitionFiles(nouns, r)...)
	if err := writeFiles(out, dir, files); err != nil {
		return err
	}
	if len(errs) > 0 {
		return nserr.New(nserr.Usage, "%s", strings.Join(errs, "\n"))
	}
	return nil
}

// writeFiles prints files, or writes them under dir.
func writeFiles(out io.Writer, dir string, files []hmsFile) error {
	for _, f := range files {
		if dir == "" {
			fmt.Fprintf(out, "# %s\n", f.path)
			out.Write(f.data)
			continue
		}
		p := filepath.Join(dir, f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		fmt.Fprintf(out, "wrote %s\n", p)
	}
	return nil
}

func renderInspection(out io.Writer, res *inspection, nouns []*model.Noun, full, sources bool) {
	how := "inspected now"
	if !res.Fresh && res.Snapshot != nil {
		how = fmt.Sprintf("from snapshot #%d taken %s; --refresh to inspect again",
			res.Snapshot.ID, res.Snapshot.CreatedAt.Local().Format("2006-01-02 15:04"))
	} else if res.Snapshot != nil {
		how = fmt.Sprintf("stored as snapshot #%d", res.Snapshot.ID)
	}
	fmt.Fprintf(out, "Inspected %d repositories, %d observations (%s)\n\n", len(res.Sources), res.Observations, how)
	if len(res.Model.Nouns) == 0 {
		fmt.Fprintln(out, "No data model found: no .hms schemas, transform SQL or dbt projects.")
		return
	}

	fmt.Fprintln(out, "Detected model:")
	for _, n := range nouns {
		renderNoun(out, n, res.registry, full, sources)
	}

	shown := map[string]bool{}
	for _, n := range nouns {
		shown[n.ID.String()] = true
	}
	var edges []model.Edge
	for _, e := range res.Model.Lineage {
		if shown[e.From] || shown[e.To] {
			edges = append(edges, e)
		}
	}
	if len(edges) > 0 {
		fmt.Fprintln(out, "\nLineage:")
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, e := range edges {
			where := ""
			if sources {
				where = e.Source.Where()
			}
			fmt.Fprintf(w, "  %s\t-> %s\t%s\t%s\n", e.From, e.To, e.Via, where)
		}
		w.Flush()
	}
	renderDisagreements(out, res.Model, shown, full, sources)
}

// physical renders a column's physical type: the value of the attribute key
// its perspective maps to core .hms types.
func physical(c *model.Column, d *perspective.Definition) string {
	if c == nil {
		return ""
	}
	key := d.TypeKey()
	if v, ok := c.Values[key]; ok && key != "" {
		return d.Physical(key, v)
	}
	return ""
}

// flags are a column's true bool values, named without an is_ prefix.
func flags(c *model.Column, d *perspective.Definition) []string {
	if c == nil || d == nil {
		return nil
	}
	var out []string
	for _, k := range d.Keys(perspective.Attribute) {
		if ext, _ := d.Extension(perspective.Attribute, k); ext.ExtensionType == "bool" && c.Values[k].Value == true {
			out = append(out, strings.TrimPrefix(k, "is_"))
		}
	}
	sort.Strings(out)
	return out
}

// tags are a binding's enumerated entity values other than its name, by
// key.
func tags(b *model.Binding, d *perspective.Definition) []string {
	if d == nil {
		return nil
	}
	var keys []string
	for _, k := range d.Keys(perspective.Entity) {
		if ext, _ := d.Extension(perspective.Entity, k); ext.ExtensionType == "enum" && k != d.BindingKey {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		if v := b.Values[k].String(); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func renderNoun(out io.Writer, n *model.Noun, r *perspective.Registry, full, sources bool) {
	kind := string(n.Metatype)
	if n.Authoritative {
		kind += ", .hms"
	}
	fmt.Fprintf(out, "\n%s  (%s)\n", n.ID, kind)
	if n.Metatype == model.MetaRelationship {
		fmt.Fprintf(out, "  %s -> %s\n", n.RefFrom, n.RefTo)
	}
	if n.Description != nil && *n.Description != "" {
		fmt.Fprintf(out, "  %s\n", *n.Description)
	}
	for _, b := range n.Bindings {
		tags := tags(b, r.Get(b.Perspective))
		if b.Primary {
			tags = append(tags, "primary")
		}
		if b.Reference {
			tags = append(tags, "reference")
		}
		loc := b.Location.String()
		if t := b.Values["template"].String(); t != "" {
			loc = t
		}
		line := fmt.Sprintf("  @%s %s", b.Label(), loc)
		if len(tags) > 0 {
			line += " (" + strings.Join(tags, ", ") + ")"
		}
		if sources && len(b.Sources) > 0 {
			line += "  " + b.Sources[0].Where()
		}
		fmt.Fprintln(out, line)
	}
	if sources {
		for _, a := range n.Aliases {
			fmt.Fprintf(out, "  = %s: %s\n", a.ID, a.Reason)
		}
		for _, s := range n.Sources {
			fmt.Fprintf(out, "  ^ %s\n", s.Where())
		}
	}
	primary := n.Primary()
	var pdef *perspective.Definition
	if primary != nil {
		pdef = r.Get(primary.Perspective)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, a := range n.Attributes {
		if !full && i == attributeLimit {
			fmt.Fprintf(w, "    ... %d more (nsctl model inspect %s)\n", len(n.Attributes)-attributeLimit, n.ID)
			break
		}
		var notes []string
		if a.Required != nil {
			if *a.Required {
				notes = append(notes, "required")
			} else {
				notes = append(notes, "optional")
			}
		}
		var col *model.Column
		if primary != nil {
			col = primary.Column(a.Name)
		}
		notes = append(notes, flags(col, pdef)...)
		if len(a.EnumDef) > 0 {
			notes = append(notes, fmt.Sprintf("%d values", len(a.EnumDef)))
		}
		where := ""
		if sources && len(a.Sources) > 0 {
			where = a.Sources[0].Where()
		}
		phys := physical(col, pdef)
		if phys != "" && primary != nil {
			phys = "@" + primary.Label() + " " + phys
		}
		fmt.Fprintf(w, "    %s\t%s\t%s\t%s\t%s\n", a.Name, a.Type, phys, strings.Join(notes, ", "), where)
	}
	w.Flush()
}

func renderDisagreements(out io.Writer, m *model.Model, shown map[string]bool, filtered, sources bool) {
	counts := map[model.Severity]int{}
	var list []model.Disagreement
	for _, d := range m.Disagreements {
		if filtered && !relevant(d.Subject, shown) {
			continue
		}
		counts[d.Severity]++
		if d.Severity == model.SevInfo && !sources {
			continue
		}
		list = append(list, d)
	}
	total := counts[model.SevError] + counts[model.SevWarning] + counts[model.SevInfo]
	if total == 0 {
		return
	}
	fmt.Fprintf(out, "\nDisagreements: %d error, %d warning, %d info", counts[model.SevError], counts[model.SevWarning], counts[model.SevInfo])
	if !sources && counts[model.SevInfo] > 0 {
		fmt.Fprint(out, " (--sources lists info)")
	}
	fmt.Fprintln(out)
	for _, d := range list {
		fmt.Fprintf(out, "  %-7s %s  [%s]\n          %s\n", d.Severity, d.Subject, d.Code, d.Message)
	}
}

// relevant reports whether a disagreement's subject is about a shown noun:
// the noun, or one of its attributes (#) or bindings (@).
func relevant(subject string, shown map[string]bool) bool {
	for id := range shown {
		if subject == id || strings.HasPrefix(subject, id+"#") || strings.HasPrefix(subject, id+"@") ||
			strings.HasPrefix(subject, id+".") {
			return true
		}
	}
	return false
}
