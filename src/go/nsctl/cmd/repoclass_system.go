package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// newRepoClassSystemCommand authors the `system` key (NERD041 SPEC001,
// SPEC002; hmd-docs-bacon NERD0002): the group of repo classes this one is
// worked on with. Membership is declared here and nowhere inferred.
func newRepoClassSystemCommand(path *repoClassPath) *cobra.Command {
	group := &cobra.Command{
		Use:   "system",
		Short: "Declare the system this repo class belongs to",
		Long: `Write or remove the manifest's "system": the group of repo classes this one is
worked on together with, and the part it plays there. A system is not a unit of
deployment; nothing nsctl builds, deploys or resolves reads it. A repo class
belongs to at most one system.

--description and --parent define the system itself, and belong only in the one
manifest that introduces it. "nsctl repoclass systems" reports manifests that
define the same system differently.`,
		Example: `  nsctl repoclass system set transform --role engine
  nsctl repoclass system set cli-go --role cli --parent cli --description "The Go command-line tools"
  nsctl repoclass system clear`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(newSystemSetCommand(path), newSystemClearCommand(path))
	return group
}

func newSystemSetCommand(path *repoClassPath) *cobra.Command {
	var s bacon.System
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Name the system, and optionally the role, description and parent",
		Long: `Replace the declaration. With no flags the shorthand is written ("system":
"<name>"); with any flag, the object form holding only the fields given.
Names are lower-case letters, digits and single hyphens, starting with a
letter. Roles: ` + strings.Join(bacon.SystemRoles, ", ") + `.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := path.open()
			if err != nil {
				return err
			}
			s.Name = args[0]
			key, err := bacon.SetSystem(st.Doc, s)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, st, key)
		},
	}
	cmd.Flags().StringVar(&s.Role, "role", "", "the part this repo class plays: "+strings.Join(bacon.SystemRoles, "|"))
	cmd.Flags().StringVar(&s.Description, "description", "", "the system's description (only in the manifest that introduces it)")
	cmd.Flags().StringVar(&s.Parent, "parent", "", "the parent system's name (only in the manifest that introduces it)")
	return cmd
}

func newSystemClearCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "clear",
		Short:         "Remove the declaration",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := path.open()
			if err != nil {
				return err
			}
			key, ok := bacon.ClearSystem(st.Doc)
			if !ok {
				return nil
			}
			return saveAndReport(cmd, st, key)
		},
	}
}

// newRepoClassSystemsCommand resolves the systems of a workspace (SPEC004).
// It reads --root, not --path: it looks across repo classes, not at one.
func newRepoClassSystemsCommand() *cobra.Command {
	var roots []string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "systems",
		Short: "Resolve the systems declared across a workspace",
		Long: `Reads the repo classes directly under each --root (default: the current
directory), skipping hidden directories and git worktrees, and reports each
system: its resolved description and parent, its members with their roles,
and its subsystems. A system named only as a parent is listed too.

A system's definition comes from the manifests that give it inline. Manifests
that agree resolve; manifests that differ are a conflict, reported with every
manifest involved, and nothing is picked by order. Parent loops are reported
as cycles. The exit status is 1 when there is any conflict, cycle or
unreadable system key, and every system is still printed.`,
		Example: `  nsctl repoclass systems --root ~/projects
  nsctl repoclass systems --root ~/projects --json`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(roots) == 0 {
				roots = []string{"."}
			}
			entries, problems, err := bacon.ScanSystems(roots)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			report := bacon.ResolveSystems(entries)
			report.Problems = append(report.Problems, problems...)
			if asJSON {
				data, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
			} else {
				renderSystems(cmd.OutOrStdout(), report)
			}
			if !report.OK() {
				return nserr.New(nserr.Fail, "%d conflict(s), %d cycle(s), %d unreadable key(s)",
					len(report.Conflicts), len(report.Cycles), len(report.Problems))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&roots, "root", nil, "a directory whose repo classes are read (repeatable)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the report as JSON")
	return cmd
}

func renderSystems(out io.Writer, r bacon.SystemsReport) {
	for i, s := range r.Systems {
		if i > 0 {
			fmt.Fprintln(out)
		}
		head := s.Name
		if s.Parent != "" {
			head += "  (in " + s.Parent + ")"
		}
		fmt.Fprintln(out, head)
		if s.Description != "" {
			fmt.Fprintf(out, "  %s\n", s.Description)
		}
		if len(s.Subsystems) > 0 {
			fmt.Fprintf(out, "  subsystems: %s\n", strings.Join(s.Subsystems, ", "))
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, m := range s.Members {
			role := m.Role
			if role == "" {
				role = "-"
			}
			fmt.Fprintf(w, "  %s\t%s\n", m.RepoClass, role)
		}
		w.Flush()
	}
	for _, c := range r.Conflicts {
		fmt.Fprintf(out, "\nconflict: system %s, %s: %s\n", c.System, c.Field, strings.Join(c.Values, " | "))
		for _, m := range c.Manifests {
			fmt.Fprintf(out, "  %s\n", m)
		}
	}
	for _, c := range r.Cycles {
		fmt.Fprintf(out, "\ncycle: %s -> %s\n", strings.Join(c, " -> "), c[0])
	}
	for _, p := range r.Problems {
		fmt.Fprintf(out, "\nunreadable: %s\n", p)
	}
}
