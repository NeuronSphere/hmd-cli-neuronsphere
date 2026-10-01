package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// A section is one noun's answer to "what is this, and what is wrong with
// it" (NERD032 SPEC008). Every noun with an inspect verb produces one, and
// nsctl inspect runs them all over the same repositories.
type section struct {
	Noun string `json:"noun"`
	// Skipped says why the section could not run; the others still do.
	Skipped  string    `json:"skipped,omitempty"`
	Summary  any       `json:"summary,omitempty"`
	Findings []finding `json:"findings"`
	// text renders the summary for a person.
	text func(io.Writer)
}

// finding is the one shape every noun's findings take.
type finding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
	Where    string `json:"where,omitempty"`
}

const (
	sevError   = "error"
	sevWarning = "warning"
	sevInfo    = "info"
)

// inspectArgs is what every section is given: the repositories found, and
// the paths they were found under.
type inspectArgs struct {
	paths   []string
	sources []inspect.Source
	refresh bool
	env     string
	warn    io.Writer
}

type sectionFunc func(ctx context.Context, opts *Options, a inspectArgs) section

// sections is every noun nsctl inspect runs, in order. Adding a noun is a
// function and a line here.
var sections = []struct {
	noun string
	run  sectionFunc
}{
	{"repoclass", repoclassSection},
	{"instance", instanceSection},
	{"model", modelSection},
}

func severityRank(s string) int {
	switch s {
	case sevError:
		return 0
	case sevWarning:
		return 1
	}
	return 2
}

func sortFindings(fs []finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if a, b := severityRank(fs[i].Severity), severityRank(fs[j].Severity); a != b {
			return a < b
		}
		return fs[i].Subject < fs[j].Subject
	})
}

// renderSection prints a section's summary and its findings; info findings
// only when asked for.
func renderSection(out io.Writer, s section, info bool) {
	if s.Skipped != "" {
		fmt.Fprintf(out, "  skipped: %s\n", s.Skipped)
		return
	}
	if s.text != nil {
		s.text(out)
	}
	counts := map[string]int{}
	for _, f := range s.Findings {
		counts[f.Severity]++
	}
	if len(s.Findings) == 0 {
		return
	}
	fmt.Fprintf(out, "\n  %d error, %d warning, %d info", counts[sevError], counts[sevWarning], counts[sevInfo])
	if !info && counts[sevInfo] > 0 {
		fmt.Fprint(out, " (--info lists info)")
	}
	fmt.Fprintln(out)
	for _, f := range s.Findings {
		if f.Severity == sevInfo && !info {
			continue
		}
		fmt.Fprintf(out, "  %-7s %s  [%s]\n          %s\n", f.Severity, f.Subject, f.Code, f.Message)
		if f.Where != "" {
			fmt.Fprintf(out, "          %s\n", f.Where)
		}
	}
}

// printSection is a noun's own inspect verb: its section alone.
func printSection(cmd *cobra.Command, s section, asJSON, info bool) error {
	sortFindings(s.Findings)
	if s.Findings == nil {
		s.Findings = []finding{}
	}
	out := cmd.OutOrStdout()
	if asJSON {
		return encodeJSON(out, s)
	}
	renderSection(out, s, info)
	return failOnErrors([]section{s})
}

// failOnErrors is the exit status of an inspection: 1 when any section
// reports an error.
func failOnErrors(ss []section) error {
	n := 0
	for _, s := range ss {
		for _, f := range s.Findings {
			if f.Severity == sevError {
				n++
			}
		}
	}
	if n > 0 {
		return nserr.New(nserr.Fail, "inspection found %d error(s)", n)
	}
	return nil
}

func newInspectCommand(opts *Options) *cobra.Command {
	var asJSON, info, refresh bool
	var only, skip []string
	cmd := &cobra.Command{
		Use:   "inspect [path...]",
		Short: "Inspect repositories through every noun that can: repo class, instances, data model",
		Long: `Runs the inspect verb of every noun that has one over the repositories in the
named directories (default: the current one; a directory that is not itself a
repository stands for the repositories in it), and prints one section per noun:

  repoclass  what each repository is: its BACON manifest's summary and
             validation, or for one without a manifest, what detection
             can and cannot tell
  instance   where those repo classes are declared in environments under
             HMD_HOME, and whether their dependency wiring resolves
  model      the data model the repositories describe, and every place
             its artifacts disagree

Findings share one shape and one severity scale across nouns. Exits 1 when
any section reports an error, so it can gate a change. Writes nothing in any
repository. Each noun's own inspect verb shows its section in more detail.
NERD032 SPEC008.`,
		Example: `  nsctl inspect
  nsctl inspect ~/src --only repoclass,model
  nsctl inspect ~/src/hmd-config-transform-reporting --json`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := pathsOr(args)
			srcs, err := inspect.Discover(paths)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			chosen := map[string]bool{}
			for _, s := range sections {
				chosen[s.noun] = len(only) == 0
			}
			for _, lists := range []struct {
				names []string
				to    bool
			}{{only, true}, {skip, false}} {
				for _, n := range lists.names {
					if _, ok := chosen[n]; !ok {
						return nserr.New(nserr.Usage, "no section %q; the sections are %s", n, sectionNames())
					}
					chosen[n] = lists.to
				}
			}
			a := inspectArgs{paths: paths, sources: srcs, refresh: refresh, warn: cmd.ErrOrStderr()}
			var ran []section
			for _, s := range sections {
				if !chosen[s.noun] {
					continue
				}
				sec := s.run(cmd.Context(), opts, a)
				sec.Noun = s.noun
				sortFindings(sec.Findings)
				if sec.Findings == nil {
					sec.Findings = []finding{}
				}
				ran = append(ran, sec)
			}
			out := cmd.OutOrStdout()
			if asJSON {
				doc := map[string]any{"sources": srcs, "sections": ran}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(doc); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				return failOnErrors(ran)
			}
			fmt.Fprintf(out, "Inspected %d repositories\n", len(srcs))
			counts := map[string]int{}
			for _, s := range ran {
				fmt.Fprintf(out, "\n%s\n", strings.ToUpper(s.Noun))
				renderSection(out, s, info)
				for _, f := range s.Findings {
					counts[f.Severity]++
				}
			}
			fmt.Fprintf(out, "\n%d error, %d warning, %d info\n", counts[sevError], counts[sevWarning], counts[sevInfo])
			return failOnErrors(ran)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print every section as one JSON document")
	cmd.Flags().BoolVar(&info, "info", false, "List info findings too, not only count them")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Inspect the data model again rather than show its latest snapshot")
	cmd.Flags().StringSliceVar(&only, "only", nil, "Run only these sections ("+sectionNames()+")")
	cmd.Flags().StringSliceVar(&skip, "skip", nil, "Skip these sections")
	return cmd
}

func sectionNames() string {
	var out []string
	for _, s := range sections {
		out = append(out, s.noun)
	}
	return strings.Join(out, ", ")
}
