package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/spf13/cobra"
)

// newDataCommand is `nsctl data` (NERD039): a person's own files, queryable
// with DuckDB, with no environment, bucket or repository. What nsctl learns is
// kept in a workspace under $HMD_HOME/data; sources are only read.
//
// In "Start here" rather than "Author": it is the first thing a newcomer with
// data runs, before they have heard of a repo class.
func newDataCommand(opts *Options) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "data",
		Short: "Explore your own data files with SQL",
		Long: `Explore your own data files (CSV, Parquet, JSON, Excel) with SQL.

Needs only a container engine: DuckDB runs from a pinned image, your files are
read where they are and never changed, and nothing leaves this machine. What
nsctl learns is kept in a workspace under $HMD_HOME/data/<workspace>.

  nsctl data init sales ~/Downloads/exports   # start a workspace
  nsctl data scan --apply                      # group files into tables
  nsctl data profile orders                    # what needs a decision
  nsctl data register                          # make the tables queryable
  nsctl data sql "SELECT count(*) FROM orders"
  nsctl data connect                           # open it from your own tool`,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.PersistentFlags().StringVar(&workspace, "workspace", "", "Workspace to act on (default: the current one)")
	d := &dataCtx{opts: opts, workspace: &workspace}
	cmd.AddCommand(
		newDataInitCommand(d), newDataUseCommand(d), newDataListCommand(d), newDataDeleteCommand(d),
		newDataScanCommand(d), newDataProfileCommand(d), newDataRegisterCommand(d),
		newDataSnapshotCommand(d), newDataSQLCommand(d), newDataConnectCommand(d), newDataStatusCommand(d),
		newDataUnderstandCommand(d), newDataApproveCommand(d), newDataRejectCommand(d), newDataDocsCommand(d),
		newDataStorageCommand(d), newDataLandCommand(d), newDataExportCommand(d),
	)
	return cmd
}

type dataCtx struct {
	opts      *Options
	workspace *string
}

func (d *dataCtx) home() (string, error) { return d.opts.RequireHome() }

func (d *dataCtx) open() (*data.Workspace, error) {
	home, err := d.home()
	if err != nil {
		return nil, err
	}
	return data.Current(home, *d.workspace)
}

func (d *dataCtx) image() string {
	if d.opts.Lookup != nil {
		if ref := d.opts.Lookup(data.ImageEnv); ref != "" {
			return ref
		}
	}
	return data.DefaultImage
}

// engine returns the DuckDB engine, pulling its image the first time. A
// workspace with storage configured is attached to its environment first, so
// every session can read the tables landed there.
func (d *dataCtx) engine(ctx context.Context, ws *data.Workspace) (data.DockerEngine, error) {
	if ws != nil && ws.Config.Storage != nil && ws.Remote == nil {
		s, err := d.storageOf(ctx, ws.Config.Storage.Env)
		if err != nil {
			return data.DockerEngine{}, err
		}
		ws.Remote = s.remote()
	}
	ref := d.image()
	dk := container.New()
	if err := dk.Available(ctx); err != nil {
		return data.DockerEngine{}, nserr.New(nserr.Fail, "nsctl data runs DuckDB in a container, and the container engine is not reachable: %v. `nsctl doctor` checks", err)
	}
	if !dk.ImagePresent(ctx, ref) {
		if err := dk.PullImage(ctx, ref); err != nil {
			return data.DockerEngine{}, nserr.New(nserr.Fail,
				"pulling the DuckDB image %s: %v. To use a local build, set %s (for example hmd-img-duckdb:dev)", ref, err, data.ImageEnv)
		}
	}
	return data.DockerEngine{Image: ref}, nil
}

func newDataInitCommand(d *dataCtx) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "init <workspace> [<folder>...]",
		Short: "Start a workspace over one or more folders of files",
		Long: `Creates $HMD_HOME/data/<workspace> and makes it the current workspace.
Each folder becomes a source: it is read in place and never written. Running
init again on an existing workspace adds the folders it does not have yet.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := d.home()
			if err != nil {
				return err
			}
			ws, err := data.Init(home, args[0], args[1:])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, map[string]any{"workspace": ws.Name, "root": ws.Root, "sources": ws.Config.Sources})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Workspace %s is ready at %s and is now current.\n", ws.Name, ws.Root)
			for _, s := range ws.Config.Sources {
				fmt.Fprintf(out, "  source %s: %s (read only)\n", s.Name, s.Path)
			}
			if len(ws.Config.Sources) == 0 {
				fmt.Fprintln(out, "Next: nsctl data scan <folder>")
			} else {
				fmt.Fprintln(out, "Next: nsctl data scan")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}

func newDataUseCommand(d *dataCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "use <workspace>",
		Short: "Make a workspace the current one",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := d.home()
			if err != nil {
				return err
			}
			if err := data.Use(home, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Workspace %s is now current.\n", args[0])
			return nil
		},
	}
}

func newDataListCommand(d *dataCtx) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := d.home()
			if err != nil {
				return err
			}
			names, current, err := data.List(home)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, map[string]any{"workspaces": names, "current": current})
			}
			if len(names) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No workspaces. Start one with `nsctl data init <name> <folder>`.")
				return nil
			}
			for _, n := range names {
				mark := " "
				if n == current {
					mark = "*"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", mark, n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}

func newDataDeleteCommand(d *dataCtx) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <workspace>",
		Short: "Delete a workspace (your files are not touched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := d.home()
			if err != nil {
				return err
			}
			if !yes {
				return nserr.New(nserr.Usage,
					"this deletes everything nsctl learned in workspace %s (tables, profiles, catalog); your source files are not touched. Pass --yes to confirm", args[0])
			}
			if err := data.Delete(home, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted workspace %s. Source files were not touched.\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the deletion")
	return cmd
}

func newDataScanCommand(d *dataCtx) *cobra.Command {
	var (
		asJSON bool
		apply  bool
		rename []string
	)
	cmd := &cobra.Command{
		Use:   "scan [<folder>...]",
		Short: "Find data files and propose which ones form each table",
		Long: `Walks the workspace's sources (and any folders given, which are added as
sources), decides each file's format from its content, and proposes groups:
files that share a format, a folder pattern and most of their columns form one
table. Hive-style key=value folders become partition columns, and each sheet of
an Excel workbook is a table of its own. Files it cannot read are listed with
the reason.

Nothing is written without --apply, which creates one table definition per
proposal. Rename a proposal with --name proposed=new. Files an existing table
already reads are not proposed again.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			renames := map[string]string{}
			for _, r := range rename {
				from, to, ok := strings.Cut(r, "=")
				if !ok {
					return nserr.New(nserr.Usage, "--name takes proposed=new, got %q", r)
				}
				renames[from] = to
			}
			eng, err := d.engine(cmd.Context(), ws)
			if err != nil {
				return err
			}
			rep, err := ws.Scan(cmd.Context(), eng, data.ScanOptions{Add: args, Apply: apply, Rename: renames})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, rep)
			}
			printScan(cmd.OutOrStdout(), rep, apply)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	cmd.Flags().BoolVar(&apply, "apply", false, "Create a table for each proposal")
	cmd.Flags().StringArrayVar(&rename, "name", nil, "Rename a proposal: proposed=new (repeatable)")
	return cmd
}

func printScan(out io.Writer, rep *data.ScanReport, applied bool) {
	fmt.Fprintf(out, "Workspace %s: %d file(s) seen", rep.Workspace, rep.FilesSeen)
	if rep.Covered > 0 {
		fmt.Fprintf(out, ", %d already in tables", rep.Covered)
	}
	fmt.Fprintln(out, ".")
	if len(rep.Proposals) == 0 {
		fmt.Fprintln(out, "No new tables to propose.")
	}
	for _, p := range rep.Proposals {
		where := p.Paths[0]
		if len(p.Paths) > 1 {
			where = fmt.Sprintf("%d files", len(p.Paths))
		}
		extra := ""
		if p.Sheet != "" {
			extra = ", sheet " + p.Sheet
		}
		if len(p.Partitions) > 0 {
			extra += ", partitioned by " + strings.Join(p.Partitions, ", ")
		}
		fmt.Fprintf(out, "  %s: %d file(s) of %s (%s%s), %d columns\n", p.Table, len(p.Files), p.Format, where, extra, len(p.Columns))
	}
	for _, u := range rep.Unsupported {
		fmt.Fprintf(out, "  skipped %s/%s: %s\n", u.Source, u.Path, u.Reason)
	}
	for _, m := range rep.Missing {
		fmt.Fprintf(out, "  table %s no longer matches any file\n", m)
	}
	switch {
	case applied && len(rep.Applied) > 0:
		fmt.Fprintf(out, "Created %d table(s). Next: nsctl data profile %s\n", len(rep.Applied), rep.Applied[0])
	case len(rep.Proposals) > 0:
		fmt.Fprintln(out, "Nothing written. Accept these with: nsctl data scan --apply")
	}
}

func newDataProfileCommand(d *dataCtx) *cobra.Command {
	var asJSON, full, relationships bool
	cmd := &cobra.Command{
		Use:   "profile <table>... | --relationships",
		Short: "Report what a table's data looks like and what needs a decision",
		Long: `Reports, per column, the inferred type, null fraction, distinct values, range
and a small sample, and flags what is worth a decision, each with a suggested
fix: dates or numbers read as text, identifiers that would lose leading zeros,
junk lines above the header, columns that differ across files, candidate keys,
and likely personal data (whose samples are redacted).

Statistics come from a sample of up to 100,000 rows unless --full. The profile
is saved in the workspace. It is evidence; nothing is changed.

--relationships measures every registered table together instead: for each
column that could refer to another table's key, how many of its distinct
values the key holds. It is evidence for ` + "`nsctl data understand`" + `, which
asks about the likely joins.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if relationships {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.MinimumNArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			eng, err := d.engine(cmd.Context(), ws)
			if err != nil {
				return err
			}
			if relationships {
				rel, err := ws.ProfileRelationships(cmd.Context(), eng)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd, rel)
				}
				printRelationships(cmd.OutOrStdout(), rel)
				return nil
			}
			var profiles []*data.Profile
			for _, name := range args {
				p, err := ws.ProfileTable(cmd.Context(), eng, name, full)
				if err != nil {
					return err
				}
				profiles = append(profiles, p)
			}
			if asJSON {
				if len(profiles) == 1 {
					return printJSON(cmd, profiles[0])
				}
				return printJSON(cmd, profiles)
			}
			for _, p := range profiles {
				printProfile(cmd.OutOrStdout(), p)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	cmd.Flags().BoolVar(&full, "full", false, "Read every row instead of a sample")
	cmd.Flags().BoolVar(&relationships, "relationships", false, "Measure which columns refer to other tables' keys")
	return cmd
}

func printRelationships(out io.Writer, r *data.Relationships) {
	if len(r.Joins) == 0 {
		fmt.Fprintf(out, "No column in %d table(s) refers to another table's key.\n", len(r.Tables))
		return
	}
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FROM\tTO\tMATCHED\tCARDINALITY\t")
	for _, j := range r.Joins {
		fmt.Fprintf(tw, "%s.%s\t%s.%s\t%d of %d (%.0f%%)\t%s\t\n", j.FromTable, j.FromColumn, j.ToTable, j.ToColumn,
			j.Matched, j.FromDistinct, j.Inclusion*100, strings.ReplaceAll(j.Cardinality, "_", " "))
	}
	_ = tw.Flush()
	if r.Truncated {
		fmt.Fprintln(out, "Only the first 2000 column pairs were measured.")
	}
}

func printProfile(out io.Writer, p *data.Profile) {
	sampled := ""
	if p.Sampled && p.Rows > 100000 {
		sampled = " (statistics from a sample)"
	}
	fmt.Fprintf(out, "%s: %d rows from %d file(s)%s\n", p.Table, p.Rows, p.Files, sampled)
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  COLUMN\tTYPE\tNULL\tDISTINCT\tMIN\tMAX\t")
	for _, c := range p.Columns {
		name := c.Name
		if c.CandidateKey {
			name += " (key?)"
		}
		minV, maxV := clip(c.Min), clip(c.Max)
		if c.PII {
			minV, maxV = "(redacted)", "(redacted)"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%.0f%%\t%d\t%s\t%s\t\n", name, c.Type, 100*c.NullFraction, c.Distinct, minV, maxV)
	}
	_ = tw.Flush()
	if len(p.Issues) == 0 {
		fmt.Fprintln(out, "  Nothing needs a decision.")
	}
	for _, i := range p.Issues {
		fmt.Fprintf(out, "  ! %s\n", i.Message)
	}
}

func clip(s string) string {
	if len(s) > 24 {
		return s[:21] + "..."
	}
	return s
}

func newDataRegisterCommand(d *dataCtx) *cobra.Command {
	var asJSON, pin bool
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Make every table queryable as a view in the workspace catalog",
		Long: `(Re)builds the workspace's DuckDB catalog with one view per table, reading
the files in place (or the table's snapshot). Views of tables that no longer
exist are dropped.

--pin records each table's current schema in its definition. After that, files
that no longer match are reported as drift instead of silently changing the
view. A table whose schema_mode is declared refuses files that do not match.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			eng, err := d.engine(cmd.Context(), ws)
			if err != nil {
				return err
			}
			rep, runErr := ws.Register(cmd.Context(), eng, pin)
			if rep == nil {
				return runErr
			}
			if asJSON {
				if err := printJSON(cmd, rep); err != nil {
					return err
				}
				return runErr
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Catalog %s:\n", rep.Catalog)
			for _, t := range rep.Tables {
				note := ""
				if t.Pinned {
					note = ", pinned"
				}
				if t.Snapshot {
					note += ", from snapshot"
				}
				fmt.Fprintf(out, "  %s: %d rows, %d columns%s\n", t.Table, t.Rows, t.Columns, note)
				for _, dr := range t.Drift {
					fmt.Fprintf(out, "    drift: %s\n", dr)
				}
				if t.Error != "" {
					fmt.Fprintf(out, "    error: %s\n", t.Error)
				}
			}
			for _, v := range rep.Dropped {
				fmt.Fprintf(out, "  dropped view %s (its table is gone)\n", v)
			}
			if runErr == nil && len(rep.Tables) > 0 {
				fmt.Fprintf(out, "Next: nsctl data sql \"SELECT * FROM %s LIMIT 10\"\n", rep.Tables[0].Table)
			}
			return runErr
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	cmd.Flags().BoolVar(&pin, "pin", false, "Record each table's current schema in its definition")
	return cmd
}

func newDataSnapshotCommand(d *dataCtx) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "snapshot <table>...",
		Short: "Copy tables to Parquet so later edits to the files do not change answers",
		Long: `Copies each table to Parquet in the workspace and points its view there,
recording a hash of every source file. ` + "`nsctl data status`" + ` then reports
when the files have changed since. Run register afterwards to update the
catalog; snapshot again to refresh.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			eng, err := d.engine(cmd.Context(), ws)
			if err != nil {
				return err
			}
			var res []*data.TableStatus
			for _, name := range args {
				st, err := ws.SnapshotTable(cmd.Context(), eng, name)
				if err != nil {
					return err
				}
				res = append(res, st)
			}
			if asJSON {
				return printJSON(cmd, res)
			}
			for _, st := range res {
				fmt.Fprintf(cmd.OutOrStdout(), "Snapshot of %s: %d rows. Run `nsctl data register` to use it.\n", st.Table, st.Rows)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}

func newDataSQLCommand(d *dataCtx) *cobra.Command {
	var (
		asJSON, write bool
		save          string
	)
	cmd := &cobra.Command{
		Use:   "sql [<statement>]",
		Short: "Run SQL against the workspace catalog, or open a DuckDB shell",
		Long: `Runs one statement against the workspace catalog and prints the result. With
no statement, opens an interactive DuckDB shell on it. The catalog is opened
read-only unless --write. --save <name> keeps the statement in the workspace's
saved queries.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			eng, err := d.engine(cmd.Context(), ws)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return eng.Interactive(cmd.Context(), ws.Session(data.Request{DB: ws.Path(data.CatalogFile), ReadOnly: !write}))
			}
			rows, cols, err := ws.Query(cmd.Context(), eng, args[0], write)
			if err != nil {
				return err
			}
			if save != "" {
				p, err := ws.SaveQuery(save, args[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Saved as %s\n", p)
			}
			if asJSON {
				return printJSON(cmd, rows)
			}
			printRows(cmd.OutOrStdout(), rows, cols)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print rows as JSON")
	cmd.Flags().BoolVar(&write, "write", false, "Open the catalog read-write")
	cmd.Flags().StringVar(&save, "save", "", "Keep the statement as a saved query with this name")
	return cmd
}

// printRows prints rows as a table, in DuckDB's column order.
func printRows(out io.Writer, rows []map[string]any, cols []string) {
	if len(rows) == 0 {
		fmt.Fprintln(out, "(no rows)")
		return
	}
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t")+"\t")
	for _, r := range rows {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = clip(fmt.Sprint(nilToEmpty(r[c])))
		}
		fmt.Fprintln(tw, strings.Join(vals, "\t")+"\t")
	}
	_ = tw.Flush()
	fmt.Fprintf(out, "(%d rows)\n", len(rows))
}

func nilToEmpty(v any) any {
	if v == nil {
		return "NULL"
	}
	return v
}

func newDataConnectCommand(d *dataCtx) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Show how to open the workspace catalog from your own tools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			catalog := ws.Path(data.CatalogFile)
			// Landed tables read the environment's bucket: a DuckDB on this
			// machine needs a secret for the endpoint as seen from here.
			secret, storageNote := "", ""
			if st := ws.Config.Storage; st != nil {
				if s, err := d.storageOf(cmd.Context(), st.Env); err == nil {
					r := s.remote()
					r.Endpoint = strings.TrimPrefix(strings.TrimPrefix(s.Target.Endpoint, "http://"), "https://")
					secret = strings.TrimSpace(r.SecretStatement())
				} else {
					storageNote = err.Error()
				}
			}
			if asJSON {
				return printJSON(cmd, map[string]any{
					"catalog": catalog, "duckdb_version": data.DuckDBVersion,
					"duckdb_cli": "duckdb -readonly " + catalog,
					"duckdb_ui":  "duckdb -readonly -ui " + catalog,
					"agent":      "nsctl data sql --json \"<statement>\"",
					"s3_secret":  secret,
				})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, `The catalog for workspace %s is a DuckDB database:
  %s

Its views read your files at their own paths, so any DuckDB %s or newer on
this machine can open it. Open it read-only, so nsctl can keep updating it.

  DuckDB CLI:  duckdb -readonly %s
  DuckDB UI:   duckdb -readonly -ui %s
  DBeaver:     New connection > DuckDB > Path: %s
               (set read-only in the connection's driver properties)
  An agent:    nsctl data sql --json "<statement>"
  Here:        nsctl data sql
`, ws.Name, catalog, data.DuckDBVersion, catalog, catalog, catalog)
			if secret != "" {
				fmt.Fprintf(out, `
Tables landed in environment %s read its bucket. Run this first in a DuckDB on
this machine (it needs the httpfs extension, which DuckDB installs on first
use):

  %s
`, ws.Config.Storage.Env, secret)
			} else if storageNote != "" {
				fmt.Fprintf(out, "\nTables landed in environment %s read its bucket, which cannot be reached now: %s\n",
					ws.Config.Storage.Env, storageNote)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}

func newDataStatusCommand(d *dataCtx) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show how far each table has come, and what to run next",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			rep, err := ws.Status()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, rep)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Workspace %s (%s)\n", rep.Workspace, rep.Root)
			for _, s := range rep.Sources {
				fmt.Fprintf(out, "  source %s: %s\n", s.Name, s.Path)
			}
			for _, t := range rep.Tables {
				fmt.Fprintf(out, "  table %s: %s\n", t.Table, strings.Join(t.Steps, ", "))
				since := "snapshot"
				if t.Landed != "" {
					since = "landing"
				}
				for _, s := range t.Stale {
					fmt.Fprintf(out, "    changed since %s: %s\n", since, s)
				}
			}
			if rep.Storage != nil {
				fmt.Fprintf(out, "  storage: %s in environment %s\n", rep.Storage.Target, rep.Storage.Env)
			}
			if rep.Export != "" {
				fmt.Fprintf(out, "  exported to: %s\n", rep.Export)
			}
			fmt.Fprintf(out, "Next: %s\n", rep.Next)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}
