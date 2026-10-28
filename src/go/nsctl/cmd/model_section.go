package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// modelSummary is the data model at a glance, for nsctl inspect.
type modelSummary struct {
	Nouns         int      `json:"nouns"`
	Relationships int      `json:"relationships"`
	Bindings      int      `json:"bindings"`
	Lineage       int      `json:"lineage"`
	Perspectives  []string `json:"perspectives"`
	Snapshot      int64    `json:"snapshot,omitempty"`
}

// modelSection is the model noun's section: one line per noun, and its
// disagreements as findings. nsctl model inspect is the detail.
func modelSection(ctx context.Context, opts *Options, a inspectArgs) section {
	res, err := runInspection(ctx, opts, a.paths, a.refresh, io.Discard)
	if err != nil {
		return section{Skipped: err.Error()}
	}
	sum := modelSummary{Lineage: len(res.Model.Lineage), Perspectives: res.registry.Names()}
	if res.Snapshot != nil {
		sum.Snapshot = res.Snapshot.ID
	}
	for _, n := range res.Model.Nouns {
		if n.Metatype == model.MetaRelationship {
			sum.Relationships++
		} else {
			sum.Nouns++
		}
		sum.Bindings += len(n.Bindings)
	}
	var fs []finding
	for _, d := range res.Model.Disagreements {
		f := finding{Severity: string(d.Severity), Code: d.Code, Subject: d.Subject, Message: d.Message}
		if len(d.Sources) > 0 {
			f.Where = d.Sources[0].Where()
		}
		fs = append(fs, f)
	}
	nouns := res.Model.Nouns
	return section{Summary: sum, Findings: fs, text: func(out io.Writer) {
		fmt.Fprintf(out, "  %d nouns, %d relationships, %d bindings, %d lineage edges; perspectives: %s\n",
			sum.Nouns, sum.Relationships, sum.Bindings, sum.Lineage, orDash(strings.Join(sum.Perspectives, ", ")))
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, n := range nouns {
			var labels []string
			for _, b := range n.Bindings {
				labels = append(labels, "@"+b.Label())
			}
			kind := string(n.Metatype)
			if n.Authoritative {
				kind += ", .hms"
			}
			fmt.Fprintf(w, "  %s\t(%s)\t%s\n", n.ID, kind, strings.Join(labels, " "))
		}
		w.Flush()
	}}
}
