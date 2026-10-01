package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/detect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// repoClassRow is one repository as the repoclass section sees it.
type repoClassRow struct {
	Repo        string `json:"repo"`
	Manifest    string `json:"manifest,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Build       string `json:"build,omitempty"`
	Deploy      string `json:"deploy,omitempty"`
	// Detected is the deploy mechanism detection proposes for a repository
	// with no manifest; "" when it could not decide.
	Detected     string `json:"detected,omitempty"`
	Dependencies int    `json:"dependencies"`
}

func mechanism(doc *bacon.Object, sectionName string) string {
	if sec, ok := doc.Object(sectionName); ok {
		if m, ok := sec.String("mechanism"); ok {
			return m
		}
		if _, ok := sec.Array("commands"); ok {
			return "tool_set"
		}
	}
	return ""
}

// repoclassSection is what each repository is (NERD032 SPEC009): its
// manifest's summary and validation, or detection's view of one without.
func repoclassSection(_ context.Context, _ *Options, a inspectArgs) section {
	var rows []repoClassRow
	var fs []finding
	for _, src := range a.sources {
		row := repoClassRow{Repo: src.Repo}
		s, err := bacon.Open(src.Root)
		switch {
		case errors.Is(err, bacon.ErrNotFound):
			r, err := detect.Run(src.Root, detect.Known{BundledClasses: bundled.RepoClasses(), ReservedNames: manifest.ReservedNames()})
			if err != nil {
				fs = append(fs, finding{Severity: sevError, Code: "repoclass-detect", Subject: src.Repo, Message: err.Error()})
				break
			}
			row.Detected = r.Mechanism
			for _, f := range r.Findings {
				sev := ""
				switch f.Confidence {
				case detect.Undecided:
					sev = sevWarning
				case detect.Refused:
					sev = sevInfo
				default:
					continue
				}
				subject := src.Repo
				if f.Field != "" {
					subject += ":" + f.Field
				}
				where := ""
				if f.Where != "" {
					where = path.Join(src.Repo, f.Where)
				}
				fs = append(fs, finding{Severity: sev, Code: "repoclass-" + string(f.Confidence), Subject: subject,
					Message: f.Why, Where: where})
			}
		case err != nil:
			fs = append(fs, finding{Severity: sevError, Code: "repoclass-unreadable", Subject: src.Repo, Message: err.Error()})
		default:
			sum := bacon.Describe(s.Doc)
			row.Manifest = s.Rel
			row.Name, _ = sum.String("name")
			row.Description, _ = sum.String("description")
			row.Build, row.Deploy = mechanism(s.Doc, "build"), mechanism(s.Doc, "deploy")
			if deps, ok := sum.Array("dependencies"); ok {
				row.Dependencies = len(deps)
			}
			for _, f := range repoClassFindings(s) {
				sev := string(f.Severity)
				if f.Severity == bacon.Note {
					sev = sevInfo
				}
				fs = append(fs, finding{Severity: sev, Code: "repoclass-validate", Subject: src.Repo + ":" + f.Path,
					Message: f.Message, Where: path.Join(src.Repo, s.Rel)})
			}
		}
		rows = append(rows, row)
	}
	return section{Summary: rows, Findings: fs, text: func(out io.Writer) {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, r := range rows {
			if r.Manifest == "" {
				d := "no manifest"
				if r.Detected != "" {
					d += "; detect proposes deploy " + r.Detected
				} else {
					d += "; detect cannot decide how it deploys"
				}
				fmt.Fprintf(w, "  %s\t%s\n", r.Repo, d)
				continue
			}
			var parts []string
			if r.Build != "" {
				parts = append(parts, "build "+r.Build)
			}
			if r.Deploy != "" {
				parts = append(parts, "deploy "+r.Deploy)
			}
			parts = append(parts, counted(r.Dependencies, "dependency", "dependencies"))
			name := r.Name
			if name != r.Repo {
				name += " (in " + r.Repo + ")"
			}
			fmt.Fprintf(w, "  %s\t%s\n", name, strings.Join(parts, ", "))
		}
		w.Flush()
	}}
}

func newRepoClassInspectCommand(opts *Options, p *repoClassPath) *cobra.Command {
	var asJSON, info bool
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "What the repository under --path is, and what is wrong with its manifest",
		Long: `Describes and validates the repo class manifest under --path, or for a
repository without one, prints what detection can and cannot tell about it.
A --path that is not itself a repository stands for the repositories in it.
Findings are those of validate (error, warning; a note is info) and of detect
(an undecided question is a warning, a refusal info). Exits 1 on an error.
This is the repoclass section of ` + "`nsctl inspect`" + `. NERD032 SPEC009.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := p.resolve()
			if err != nil {
				return err
			}
			srcs, err := inspect.Discover([]string{dir})
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			s := repoclassSection(cmd.Context(), opts, inspectArgs{paths: []string{dir}, sources: srcs})
			s.Noun = "repoclass"
			return printSection(cmd, s, asJSON, info)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the section as JSON")
	cmd.Flags().BoolVar(&info, "info", false, "List info findings too, not only count them")
	return cmd
}
