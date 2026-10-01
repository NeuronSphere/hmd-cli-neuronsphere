package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/modelstore"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

func newInspectDiffCommand(opts *Options) *cobra.Command {
	var asJSON, live bool
	cmd := &cobra.Command{
		Use:   "diff [path...]",
		Short: "Show what changed in the data model between two snapshots",
		Long: `Compares the latest two snapshots of the same directories, or with --live the
latest snapshot against the directories as they are now (without storing the
result), and prints the semantic difference: nouns and attributes added or
removed, an attribute's type, requiredness or enum values changed, a column
added to one layer, lineage edges and disagreements that appeared or went away.

Snapshots are taken by ` + "`nsctl inspect --refresh`" + ` and stored under HMD_HOME. NERD032.`,
		Example: `  nsctl inspect ~/src --refresh   # baseline
  # ...edit a transform or an .hms schema...
  nsctl inspect ~/src --refresh
  nsctl inspect diff ~/src
  nsctl inspect diff ~/src --live`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Home == "" {
				return nserr.New(nserr.Usage,
					"inspect diff compares stored snapshots, which live under HMD_HOME. Export it or pass --home <path>")
			}
			if len(args) == 0 {
				args = []string{"."}
			}
			srcs, err := inspect.Discover(args)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			before, after, label, err := diffModels(cmd.Context(), opts, srcs, live)
			if err != nil {
				return err
			}
			changes := model.Diff(before, after)
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if changes == nil {
					changes = []model.Change{}
				}
				if err := enc.Encode(changes); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				return nil
			}
			renderChanges(out, label, changes)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the changes as JSON")
	cmd.Flags().BoolVar(&live, "live", false, "Compare the latest snapshot with the directories as they are now")
	return cmd
}

func diffModels(ctx context.Context, opts *Options, srcs []inspect.Source, live bool) (before, after *model.Model, label string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	store, err := modelstore.Open(modelstore.Path(opts.Home))
	if err != nil {
		return nil, nil, "", nserr.Wrap(nserr.Fail, err)
	}
	defer store.Close()
	want := 2
	if live {
		want = 1
	}
	snaps, err := store.Snapshots(modelstore.ScopeKey(roots(srcs)), want)
	if err != nil {
		return nil, nil, "", nserr.Wrap(nserr.Fail, err)
	}
	if len(snaps) < want {
		return nil, nil, "", nserr.New(nserr.Usage,
			"%d snapshot(s) of these directories, %d needed; take one with `nsctl inspect --refresh`", len(snaps), want)
	}
	if live {
		if before, err = store.Model(snaps[0].ID); err != nil {
			return nil, nil, "", nserr.Wrap(nserr.Fail, err)
		}
		edits, err := store.Edits(modelstore.ScopeKey(roots(srcs)))
		if err != nil {
			return nil, nil, "", nserr.Wrap(nserr.Fail, err)
		}
		_, _, now, _, _ := inspectNow(ctx, srcs, declared(srcs, io.Discard), edits)
		return before, now, fmt.Sprintf("snapshot #%d -> working tree", snaps[0].ID), nil
	}
	if before, err = store.Model(snaps[1].ID); err != nil {
		return nil, nil, "", nserr.Wrap(nserr.Fail, err)
	}
	if after, err = store.Model(snaps[0].ID); err != nil {
		return nil, nil, "", nserr.Wrap(nserr.Fail, err)
	}
	return before, after, fmt.Sprintf("snapshot #%d -> #%d", snaps[1].ID, snaps[0].ID), nil
}

func renderChanges(out io.Writer, label string, changes []model.Change) {
	if len(changes) == 0 {
		fmt.Fprintf(out, "No model changes (%s).\n", label)
		return
	}
	fmt.Fprintf(out, "%d model changes (%s):\n\n", len(changes), label)
	last := ""
	for _, c := range changes {
		if c.SemanticID != last {
			fmt.Fprintln(out, c.SemanticID)
			last = c.SemanticID
		}
		line := "  " + string(c.Kind)
		if c.Key != "" {
			line += " " + c.Key
		}
		switch {
		case c.From != "" && c.To != "":
			line += ": " + c.From + " -> " + c.To
		case c.To != "":
			line += ": " + c.To
		case c.From != "":
			line += ": " + c.From
		}
		fmt.Fprintln(out, line)
	}
}
