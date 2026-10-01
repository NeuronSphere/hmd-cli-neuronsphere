package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/dbt"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/hms"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect/nstransform"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/modelstore"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// inspectors is every inspector nsctl inspect runs. Adding one is a new
// package and a line here (NERD032 SPEC001).
func inspectors() []inspect.Inspector {
	return []inspect.Inspector{hms.Inspector{}, nstransform.Inspector{}, dbt.Inspector{}}
}

// attributeLimit caps the attributes printed per noun when no noun is named.
const attributeLimit = 25

func newInspectCommand(opts *Options) *cobra.Command {
	var asJSON, sources, refresh, asHMS, lossy bool
	cmd := &cobra.Command{
		Use:   "inspect [path|noun]...",
		Short: "Show the data model a set of repositories describes",
		Long: `Reads .hms language pack schemas, NeuronSphere transform SQL and dbt projects
in the named directories (default: the current one) and prints the logical
model they describe: nouns and their attributes, the tables, views and dbt
models that carry each one, lineage between them, and every place the artifacts
disagree. A directory that is not itself a repository stands for the
repositories directly inside it.

An argument that is not a directory names a noun to show in full: its fully
qualified name (hmd_lang_transform.transform_instance) or just its name.

Each inspection is stored as a snapshot under HMD_HOME, keyed by the set of
directories inspected. Without --refresh the latest snapshot is shown; with it,
the directories are inspected again and a new snapshot is stored, which
` + "`nsctl inspect diff`" + ` compares with the one before. Without HMD_HOME
nothing is stored.

nsctl inspect never writes to the inspected repositories. NERD032.`,
		Example: `  nsctl inspect
  nsctl inspect ../hmd-config-transform-reporting ../hmd-lang-transform
  nsctl inspect ~/src --refresh
  nsctl inspect ~/src ntc_instances_export --sources
  nsctl inspect ~/src hmd_lang_nsreporting.environment --hms`,
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
			case asHMS:
				return printHMS(out, nouns, lossy)
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
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the model as JSON")
	cmd.Flags().BoolVar(&sources, "sources", false, "Show where every noun, attribute and link came from, and informational notes")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Inspect again and store a new snapshot")
	cmd.Flags().BoolVar(&asHMS, "hms", false, "Print the selected nouns as .hms documents")
	cmd.Flags().BoolVar(&lossy, "lossy", false, "With --hms, write types .hms lacks (date, decimal) as string instead of refusing")
	cmd.AddCommand(newInspectDiffCommand(opts))
	return cmd
}

// inspection is what one run of nsctl inspect shows.
type inspection struct {
	Snapshot *modelstore.Snapshot `json:"snapshot,omitempty"`
	// Fresh is false when the model came from a stored snapshot rather than
	// from inspecting now.
	Fresh        bool             `json:"fresh"`
	Sources      []inspect.Source `json:"sources"`
	Reports      []inspect.Report `json:"reports,omitempty"`
	Observations int              `json:"observations"`
	Model        *model.Model     `json:"model"`
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
	if ctx == nil {
		ctx = context.Background()
	}
	srcs, err := inspect.Discover(paths)
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	store, err := openStore(opts)
	if err != nil {
		return nil, err
	}
	if store == nil {
		fmt.Fprintln(warn, "note: HMD_HOME is not set, so this inspection is not stored and cannot be diffed")
	} else {
		defer store.Close()
		if !refresh {
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
				return &inspection{Snapshot: &snaps[0], Sources: srcs, Observations: len(obs), Model: m}, nil
			}
		}
	}
	obs, reports := inspect.Run(ctx, srcs, inspectors())
	res := &inspection{Fresh: true, Sources: srcs, Reports: reports, Observations: len(obs), Model: model.Consolidate(obs)}
	if store != nil {
		revs := map[string]string{}
		for _, s := range srcs {
			if s.Revision != "" {
				revs[s.Repo] = s.Revision
			}
		}
		snap, err := store.Save(roots(srcs), revs, obs, res.Model, time.Now())
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

func printHMS(out io.Writer, nouns []*model.Noun, lossy bool) error {
	var errs []string
	for _, n := range nouns {
		doc, err := model.ExportHMS(n, lossy)
		var ee *model.ExportError
		if errors.As(err, &ee) {
			errs = append(errs, ee.Error())
			continue
		}
		if err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		if len(nouns) > 1 {
			fmt.Fprintf(out, "# %s\n", n.ID)
		}
		out.Write(doc)
	}
	if len(errs) > 0 {
		return nserr.New(nserr.Usage, "%s", strings.Join(errs, "\n"))
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
		renderNoun(out, n, full, sources)
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

func renderNoun(out io.Writer, n *model.Noun, full, sources bool) {
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
	for _, m := range n.Manifestations {
		var tags []string
		for _, t := range []string{m.Layer, m.Format} {
			if t != "" {
				tags = append(tags, t)
			}
		}
		if m.Primary {
			tags = append(tags, "primary")
		}
		if m.Reference {
			tags = append(tags, "reference")
		}
		loc := m.Location.String()
		if loc == "" {
			loc = m.Key
		}
		line := fmt.Sprintf("  ~ %s %s", m.Tech, loc)
		if len(tags) > 0 {
			line += " (" + strings.Join(tags, ", ") + ")"
		}
		if sources && len(m.Sources) > 0 {
			line += "  " + m.Sources[0].Where()
		}
		fmt.Fprintln(out, line)
	}
	if sources {
		for _, a := range n.Aliases {
			fmt.Fprintf(out, "  = %s: %s\n", a.ID, a.Reason)
		}
		for _, s := range n.Sources {
			fmt.Fprintf(out, "  @ %s\n", s.Where())
		}
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, a := range n.Attributes {
		if !full && i == attributeLimit {
			fmt.Fprintf(w, "    ... %d more (nsctl inspect %s)\n", len(n.Attributes)-attributeLimit, n.ID)
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
		if a.Partition {
			notes = append(notes, "partition")
		}
		if len(a.EnumDef) > 0 {
			notes = append(notes, fmt.Sprintf("%d values", len(a.EnumDef)))
		}
		if !a.Type.IsHMS() && a.Type != model.Unknown {
			notes = append(notes, "not an .hms type")
		}
		where := ""
		if sources && len(a.Sources) > 0 {
			where = a.Sources[0].Where()
		}
		fmt.Fprintf(w, "    %s\t%s\t%s\t%s\t%s\n", a.Name, a.Type, a.PhysicalType, strings.Join(notes, ", "), where)
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

// relevant reports whether a disagreement's subject is about a shown noun.
func relevant(subject string, shown map[string]bool) bool {
	for id := range shown {
		if subject == id || strings.HasPrefix(subject, id+".") {
			return true
		}
	}
	return false
}
