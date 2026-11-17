package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/datamodel"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// understand, approve and reject (NERD039 SPEC007): what the tables mean,
// asked as yes/no questions and recorded as NEP051 files in the workspace.
// The words a person sees are tables, rows and keys; the files are a model
// they never need to open.

func newDataUnderstandCommand(d *dataCtx) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "understand",
		Short: "Propose what each table is and how tables join, as questions to answer",
		Long: `Turns the profiles into questions: is each row of a table one thing,
identified by which column, and does a column refer to another table? Each
proposal has an id, the evidence behind it, and a question. Answer with
` + "`nsctl data approve <id>`" + ` or ` + "`nsctl data reject <id>`" + `; a rejected proposal is
not asked again. Nothing here uses machine learning: proposals come from the
profiles (` + "`nsctl data profile`" + `, and ` + "`--relationships`" + ` for joins).

What is learned is kept in the workspace's model/ folder as NEP051 files, so
` + "`nsctl model inspect`" + ` reads it like any model. Running understand again
proposes only what is new.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			rep, err := datamodel.Understand(cmd.Context(), ws)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, rep)
			}
			printUnderstanding(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}

func newDataApproveCommand(d *dataCtx) *cobra.Command {
	var asJSON bool
	var dec datamodel.Decision
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Answer yes to a proposal from understand",
		Long: `Records a yes to one proposal. --name renames the thing (a table of orders
becomes "order"); --description says what it is in a sentence, and appears in
the data dictionary. For a table, --key chooses the column that identifies a
row, or "-" for none. A join is approved after both of its tables.`,
		Example: `  nsctl data approve orders --name order --description "One order a customer placed."
  nsctl data approve orders.customer_id=customers.customer_id`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			rep, err := datamodel.Approve(cmd.Context(), ws, args[0], dec)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, rep)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Approved %s.\n\n", args[0])
			printUnderstanding(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.Flags().StringVar(&dec.Name, "name", "", "Name for the table's rows or the join")
	cmd.Flags().StringVar(&dec.Description, "description", "", "What it is, in a sentence")
	cmd.Flags().StringVar(&dec.Key, "key", "", `Column that identifies a row of a table ("-" for none)`)
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}

func newDataRejectCommand(d *dataCtx) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "reject <id>",
		Short: "Answer no to a proposal from understand",
		Long: `Records a no: the proposal is removed and understand does not ask again.
Rejecting something approved earlier withdraws the approval.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			rep, err := datamodel.Reject(cmd.Context(), ws, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, rep)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Rejected %s.\n\n", args[0])
			printUnderstanding(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}

func printUnderstanding(out io.Writer, rep *datamodel.Report) {
	if len(rep.Proposals) == 0 {
		fmt.Fprintln(out, "Nothing waiting for an answer.")
	} else {
		fmt.Fprintln(out, "Waiting for an answer:")
		for _, p := range rep.Proposals {
			fmt.Fprintf(out, "\n  %s\n    %s\n", p.ID, p.Question)
			for _, e := range p.Evidence {
				fmt.Fprintf(out, "    - %s\n", e)
			}
		}
	}
	if len(rep.Approved) > 0 {
		fmt.Fprintln(out, "\nApproved:")
		for _, p := range rep.Approved {
			what := p.Name
			if p.Kind == datamodel.Entity && p.Name != p.Table {
				what = p.Table + " is " + p.Name
			}
			if p.Key != "" {
				what += ", identified by " + p.Key
			}
			fmt.Fprintf(out, "  %s: %s\n", p.ID, what)
		}
	}
	for _, d := range rep.Disagreements {
		fmt.Fprintf(out, "\n%s: %s %s\n", d.Severity, d.Subject, d.Message)
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(out, "\nnote: %s\n", n)
	}
}

func newDataDocsCommand(d *dataCtx) *cobra.Command {
	var asJSON, force bool
	var out string
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Write a data dictionary, relationship diagram, views and example queries",
		Long: `Renders what the workspace knows into plain files you can read, share and
reuse, in --out (default ./ns-data-out, then wherever the last run wrote):

  DATA.md              every table and its columns, with meaning and quality notes
  relationships.md     how the tables join, with the evidence
  relationships.puml   the same as a PlantUML diagram
  views.sql            CREATE VIEW statements that run in any DuckDB
  questions.md         saved and suggested queries, each one runnable

Anything not yet confirmed with ` + "`nsctl data approve`" + ` is marked unconfirmed.
The named views in views.sql are also created in the workspace catalog.

Rendering runs the data docs image (Mickey with its templates) with no
network. docs replaces only files it wrote and nobody has edited since; pass
--force to replace them anyway. It never deletes anything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			dir, err := docsOut(ws.Config.Docs, out)
			if err != nil {
				return err
			}
			eng, err := d.engine(cmd.Context(), ws)
			if err != nil {
				return err
			}
			r, err := d.renderer(cmd.Context())
			if err != nil {
				return err
			}
			rep, err := datamodel.Docs(cmd.Context(), ws, eng, r, dir, force)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, rep)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %d file(s) to %s", len(rep.Written), rep.Out)
			if len(rep.Unchanged) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), " (%d unchanged)", len(rep.Unchanged))
			}
			fmt.Fprintln(cmd.OutOrStdout(), ".")
			if len(rep.Views) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Views in the catalog: %s\n", strings.Join(rep.Views, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "Folder to write into (default ./ns-data-out, or the last one used)")
	cmd.Flags().BoolVar(&force, "force", false, "Replace output files even if they were edited")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}

// docsOut resolves the output folder: --out, else the last one used, else
// ./ns-data-out. A relative path is relative to where nsctl runs.
func docsOut(rec *data.DocsRecord, flag string) (string, error) {
	dir := flag
	if dir == "" && rec != nil && rec.Out != "" {
		return rec.Out, nil
	}
	if dir == "" {
		dir = "ns-data-out"
	}
	return filepath.Abs(dir)
}

// renderer returns the docs renderer, pulling its image the first time.
func (d *dataCtx) renderer(ctx context.Context) (datamodel.Renderer, error) {
	ref := datamodel.DefaultDocsImage
	if d.opts.Lookup != nil {
		if v := d.opts.Lookup(datamodel.DocsImageEnv); v != "" {
			ref = v
		}
	}
	dk := container.New()
	if !dk.ImagePresent(ctx, ref) {
		if err := dk.PullImage(ctx, ref); err != nil {
			return nil, nserr.New(nserr.Fail,
				"pulling the data docs image %s: %v. To use a local build, set %s (for example hmd-tmpl-data-docs:dev)", ref, err, datamodel.DocsImageEnv)
		}
	}
	return datamodel.MickeyRenderer{Image: ref}, nil
}
