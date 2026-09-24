package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/detect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// newRepoClassDetectCommand reports how a repository already deploys
// (NERD009 SPEC009), and with --apply writes what is unambiguous through the
// same authoring verbs a person would use (SPEC008).
func newRepoClassDetectCommand(path *repoClassPath) *cobra.Command {
	var asJSON, apply bool
	var description string
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "Report how this repository already deploys, and in what",
		Long: `Inspects the repository and prints a classification: what deploys it, what
image it runs in, what could not be decided, and what will not be guessed --
each with the file and line the conclusion came from, so you can disagree with a
finding by opening its source.

With --apply it writes what is unambiguous: the name, a provisional description,
the build mechanism, and the deploy command with its image. Without --apply it
writes nothing. When nothing in the repository states what it is in one line,
--apply refuses and asks for --description: BACON requires one, and a manifest
without it cannot validate.

It will not write a dependency, a resource, or any discovery metadata -- not even
a provisional one. Those need the environment's vocabulary and your judgement: a
dependency role marked required that is wrong fails the entire ChangeSet, naming
only the role. The "refused" rows say so explicitly, so their absence from the
manifest is a statement rather than an omission.`,
		Example: `  nsctl repoclass detect
  nsctl repoclass detect --path ~/src/my-service --json
  nsctl repoclass detect --apply`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := path.resolve()
			if err != nil {
				return err
			}
			result, err := detect.Run(dir, detect.Known{
				BundledClasses: bundled.RepoClasses(),
				ReservedNames:  manifest.ReservedNames(),
			})
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if asJSON {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(result); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
			} else {
				renderDetect(cmd.OutOrStdout(), result, apply)
			}
			if !apply {
				return nil
			}
			written, err := detect.Apply(result, &baconWriter{dir: dir}, description)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if !asJSON {
				fmt.Fprintf(cmd.OutOrStdout(), "\nwrote meta-data/manifest.json %s\n",
					joinFields(written))
				fmt.Fprintln(cmd.OutOrStdout(),
					"Next: `nsctl repoclass validate`, then add the dependencies detection refused to guess.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the classification as JSON")
	cmd.Flags().BoolVar(&apply, "apply", false, "Write what is unambiguous")
	cmd.Flags().StringVar(&description, "description", "",
		"The one-line description, overriding a detected one and required when none was detected")
	return cmd
}

func renderDetect(out io.Writer, r detect.Result, apply bool) {
	if r.AlreadyAClass {
		fmt.Fprintln(out, "This repository is already a repo class.")
		for _, f := range r.Findings {
			fmt.Fprintf(out, "  %s  %s\n", f.Where, f.Why)
		}
		return
	}
	if r.Mechanism == "" {
		fmt.Fprintln(out, "No deploy mechanism could be decided.")
	} else {
		fmt.Fprintf(out, "Deploy mechanism: %s\n", r.Mechanism)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\n  FIELD\tCONFIDENCE\tVALUE\tWHERE")
	for _, f := range r.Findings {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n",
			orDash(f.Field), f.Confidence, orDash(f.Value), orDash(f.Where))
	}
	w.Flush()

	fmt.Fprintln(out, "\nWhy:")
	yw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, f := range r.Findings {
		fmt.Fprintf(yw, "  %s\t%s\n", orDash(f.Field)+" ("+string(f.Confidence)+")", f.Why)
	}
	yw.Flush()
	if !apply {
		fmt.Fprintln(out, "\nNothing was written. Pass --apply to write the decided rows.")
	}
}

func joinFields(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += ", "
		}
		out += f
	}
	return out
}

// baconWriter is detect.Writer over the manifest store, so one code path writes
// manifests. SPEC009 requires this: detect must not serialise a document of its
// own, because then a field would be written two ways.
type baconWriter struct {
	dir   string
	store *bacon.Store
}

func (b *baconWriter) Init(name, description string) error {
	s, err := bacon.Init(b.dir, name, description)
	if err != nil {
		return err
	}
	b.store = s
	return s.Save()
}

func (b *baconWriter) SetMechanism(section, mechanism string) error {
	if _, err := bacon.SetMechanism(b.store.Doc, section, mechanism); err != nil {
		return err
	}
	return b.store.Save()
}

func (b *baconWriter) SetExec(argv []string) error {
	if _, _, err := bacon.SetExecCommand(b.store.Doc, "deploy", argv); err != nil {
		return err
	}
	return b.store.Save()
}

func (b *baconWriter) SetToolCommands(tools []string) error {
	for _, tool := range tools {
		if _, err := bacon.AddCommand(b.store.Doc, "deploy", tool, nil); err != nil {
			return err
		}
	}
	return b.store.Save()
}

func (b *baconWriter) SetImage(ref string) error {
	if _, err := bacon.SetImage(b.store.Doc, ref); err != nil {
		return err
	}
	return b.store.Save()
}
