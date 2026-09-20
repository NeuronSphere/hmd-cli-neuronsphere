package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/spf13/cobra"
)

func newEnvPlanCommand(opts *Options) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "plan [name]",
		Short: "Preview what `env apply` would add or change, without applying it",
		Long: `Builds exactly what ` + "`env apply`" + ` would build -- the same reconcile diff,
the same catalog registrations -- and stops there: it never calls
apply_changeset and never runs a node.

Beyond the reconcile diff, it POSTs the would-be ChangeSet definition to
ms-deployment's validate_changeset, and separately warns on any resource-typed
dependency validate_changeset accepts (the bound instance exists) but
apply_changeset would reject (the instance does not produce the required
resource type). That second check otherwise only runs at apply time, which is
exactly the gap a reviewer needs closed before merging a proposal.

--output md renders the same result as Markdown suitable for pasting directly
into a pull request body; --output json is the same data for a script to
consume.`,
		Example: `  nsctl env plan
  nsctl env plan dev --output json
  nsctl env plan dev --output md > plan.md`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			switch output {
			case "text", "json", "md":
			default:
				return nserr.New(nserr.Usage, "--output: %q is not one of text, json, md", output)
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			result, err := environment.ComputePlan(cmd.Context(), &environment.Options{
				Home: home, Lookup: opts.Lookup,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}, name)
			if err != nil {
				return err
			}
			switch output {
			case "json":
				return renderPlanJSON(cmd.OutOrStdout(), result)
			case "md":
				renderPlanMD(cmd.OutOrStdout(), result)
			default:
				renderPlanText(cmd.OutOrStdout(), result)
			}
			if result.Validation != nil && !result.Validation.Valid {
				return nserr.New(nserr.Fail, "validate_changeset rejects this definition")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "text", "Output format: text, json or md")
	return cmd
}

// renderPlanText is the terminal-friendly default.
func renderPlanText(out io.Writer, r *environment.PlanResult) {
	fmt.Fprintf(out, "Environment: %s\n", r.EnvSlug)
	if r.Substrate != "" && r.Substrate != manifest.SubstrateFull {
		fmt.Fprintf(out, "Substrate: %s\n", r.Substrate)
	}
	fmt.Fprintf(out, "\n%s\n", r.Reconcile.Summary())

	if len(r.Reconcile.Add) > 0 || len(r.Reconcile.Change) > 0 || len(r.Reconcile.Remove) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, e := range r.Reconcile.Add {
			fmt.Fprintf(w, "  add\t%s\t%s\n", e.RepoInstanceName, e.RepoClassName)
		}
		for _, e := range r.Reconcile.Change {
			fmt.Fprintf(w, "  change\t%s\t%s\n", e.RepoInstanceName, e.RepoClassName)
		}
		for _, name := range r.Reconcile.Remove {
			fmt.Fprintf(w, "  undeclared\t%s\t-\n", name)
		}
		w.Flush()
	}

	if r.Validation == nil {
		return
	}
	fmt.Fprintln(out)
	if r.Validation.Valid {
		fmt.Fprintln(out, "Validation: OK (validate_changeset)")
	} else {
		fmt.Fprintln(out, "Validation: FAILED (validate_changeset)")
	}
	for _, e := range r.Validation.Errors {
		fmt.Fprintf(out, "  error   [%s] %s: %s\n", e.Type, e.Instance, e.Message)
	}
	for _, w2 := range r.Validation.Warnings {
		fmt.Fprintf(out, "  warning [%s] %s: %s\n", w2.Type, w2.Instance, w2.Message)
	}

	if len(r.Warnings) > 0 {
		fmt.Fprintln(out, "\nCandidate warnings (validate_changeset would not catch these):")
		for _, cw := range r.Warnings {
			fmt.Fprintf(out, "  %s role=%s target=%s: %s\n", cw.Instance, cw.Role, cw.Target, cw.Message)
		}
	}
}

// renderPlanMD is what Demo 2 pastes directly into a PR body -- it has to
// read as a complete, self-contained review artifact with no further editing.
func renderPlanMD(out io.Writer, r *environment.PlanResult) {
	fmt.Fprintf(out, "## Plan: %s\n\n", r.EnvSlug)
	fmt.Fprintf(out, "%s\n\n", r.Reconcile.Summary())

	renderMDList(out, "Add", r.Reconcile.Add)
	renderMDList(out, "Change", r.Reconcile.Change)
	if len(r.Reconcile.Remove) > 0 {
		fmt.Fprintln(out, "**Deployed but no longer declared** (left running, never destroyed):")
		for _, name := range r.Reconcile.Remove {
			fmt.Fprintf(out, "- `%s`\n", name)
		}
		fmt.Fprintln(out)
	}

	if r.Validation == nil {
		fmt.Fprintln(out, "Nothing to validate: everything declared is already deployed and current.")
		return
	}
	fmt.Fprintln(out, "### Validation")
	fmt.Fprintln(out)
	if r.Validation.Valid {
		fmt.Fprintln(out, "✅ `validate_changeset` accepts this definition.")
	} else {
		fmt.Fprintln(out, "❌ `validate_changeset` rejects this definition:")
		fmt.Fprintln(out)
		for _, e := range r.Validation.Errors {
			fmt.Fprintf(out, "- **%s** (%s): %s\n", e.Instance, e.Type, e.Message)
		}
	}
	if len(r.Validation.Warnings) > 0 {
		fmt.Fprintln(out, "\nWarnings:")
		fmt.Fprintln(out)
		for _, w2 := range r.Validation.Warnings {
			fmt.Fprintf(out, "- **%s** (%s): %s\n", w2.Instance, w2.Type, w2.Message)
		}
	}

	if len(r.Warnings) > 0 {
		fmt.Fprintln(out, "\n### Candidate warnings")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "`validate_changeset` only checks that these bound instances exist, not that "+
			"they satisfy the role -- `env apply` would reject them:")
		fmt.Fprintln(out)
		for _, cw := range r.Warnings {
			fmt.Fprintf(out, "- `%s` role `%s` → `%s`: %s\n", cw.Instance, cw.Role, cw.Target, cw.Message)
		}
	}
}

func renderMDList(out io.Writer, heading string, entries []bom.Entry) {
	if len(entries) == 0 {
		return
	}
	fmt.Fprintf(out, "**%s:**\n", heading)
	for _, e := range entries {
		fmt.Fprintf(out, "- `%s` (%s)\n", e.RepoInstanceName, e.RepoClassName)
	}
	fmt.Fprintln(out)
}

// planJSON is the CLI's stable JSON contract for `env plan --output json` --
// its own field names, not PlanResult verbatim, so an internal type change
// does not silently change what a script parses.
type planJSON struct {
	Environment string          `json:"environment"`
	Substrate   string          `json:"substrate,omitempty"`
	Summary     string          `json:"summary"`
	Degraded    bool            `json:"degraded"`
	Add         []planEntryJSON `json:"add"`
	Change      []planEntryJSON `json:"change"`
	Unchanged   []string        `json:"unchanged"`
	Remove      []string        `json:"remove"`
	Validation  *validationJSON `json:"validation,omitempty"`
	Warnings    []warningJSON   `json:"candidate_warnings,omitempty"`
}

type planEntryJSON struct {
	Instance      string `json:"instance"`
	RepoClassName string `json:"repo_class_name"`
}

type validationJSON struct {
	Valid    bool        `json:"valid"`
	Errors   []issueJSON `json:"errors"`
	Warnings []issueJSON `json:"warnings"`
}

type issueJSON struct {
	Type     string `json:"type"`
	Instance string `json:"instance"`
	Message  string `json:"message"`
}

type warningJSON struct {
	Instance string `json:"instance"`
	Role     string `json:"role"`
	Target   string `json:"target"`
	Message  string `json:"message"`
}

func renderPlanJSON(out io.Writer, r *environment.PlanResult) error {
	doc := planJSON{
		Environment: r.EnvSlug,
		Substrate:   string(r.Substrate),
		Summary:     r.Reconcile.Summary(),
		Degraded:    r.Reconcile.Degraded,
		Add:         planEntries(r.Reconcile.Add),
		Change:      planEntries(r.Reconcile.Change),
		Unchanged:   r.Reconcile.Unchanged,
		Remove:      r.Reconcile.Remove,
	}
	if r.Validation != nil {
		doc.Validation = &validationJSON{
			Valid:    r.Validation.Valid,
			Errors:   planIssues(r.Validation.Errors),
			Warnings: planIssues(r.Validation.Warnings),
		}
	}
	for _, cw := range r.Warnings {
		doc.Warnings = append(doc.Warnings, warningJSON{
			Instance: cw.Instance, Role: cw.Role, Target: cw.Target, Message: cw.Message,
		})
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	fmt.Fprintln(out, string(data))
	return nil
}

func planEntries(entries []bom.Entry) []planEntryJSON {
	out := make([]planEntryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, planEntryJSON{Instance: e.RepoInstanceName, RepoClassName: e.RepoClassName})
	}
	return out
}

func planIssues(issues []msdeploy.ValidateIssue) []issueJSON {
	out := make([]issueJSON, 0, len(issues))
	for _, i := range issues {
		out = append(out, issueJSON{Type: i.Type, Instance: i.Instance, Message: i.Message})
	}
	return out
}
