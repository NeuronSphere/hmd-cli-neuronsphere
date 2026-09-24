package cmd

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/credentials"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// newEnvCredentialsCommand reports how to reach and log in to what an
// environment deploys (NERD023 SPEC006).
//
// It withholds the values by default. That is not a hedge: a summary printed on
// every apply lands in terminal scrollback, in CI logs and in screen shares, for
// a value the reader needed once, and a password cannot be un-printed. Where each
// credential lives is printed, because that is the answer most readers actually
// want and it is safe to repeat.
func newEnvCredentialsCommand(opts *Options) *cobra.Command {
	var reveal, asJSON bool
	var instance string
	cmd := &cobra.Command{
		Use:   "credentials [<name>]",
		Short: "Show how to reach and sign in to what an environment deploys",
		Long: `Resolves every declared way in to the environment's instances -- the "access"
section of each repo class's BACON manifest (nsctl repoclass access) -- filling in
the instance name, deployment id, environment and Ingress host.

The credential itself is withheld unless --reveal is passed: what is printed by
default is the URL, the user who signs in, and where the credential lives. An
entry that could not be resolved is reported with the reason rather than omitted,
because "declared, and here is why it is not answering yet" is what a reader
needs after a deploy that has not finished.

Reading a value needs the control plane's Floci; everything else is read from
local state and works with nothing running.`,
		Example: `  nsctl env credentials
  nsctl env credentials dev
  nsctl env credentials dev --instance superset --reveal
  nsctl env credentials dev --json`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, home, err := loadRegistry(opts)
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			env, err := reg.Environment(name, opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}

			m, err := manifest.Load(home, env.Slug, opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			var repos []manifest.Repo
			if m != nil {
				repos = m.Repos
			}

			o := credentials.Options{
				Environment:  env.Slug,
				DeploymentID: env.DeploymentID,
				Repos:        repos,
				OutputDir:    environment.ResourceOutputDir(env),
				Class:        classReader(opts, home, repos),
				Instance:     instance,
			}
			// The secret reader is built only when a value is wanted, so the
			// default path needs no Floci and no account credential at all.
			if reveal {
				target := floci.ForAccount(opts.Lookup, env.AccountID, env.LegacyLayout)
				if r, err := floci.NewSecretReader(cmd.Context(), target); err == nil {
					o.Get = r.Get
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not reach Floci: %v\n", err)
				}
			}

			// A --instance naming nothing is a typo, and answering it with
			// "nothing declares access" would send the reader looking for a
			// declaration when the name is what is wrong.
			if instance != "" && !declaresInstance(repos, instance) {
				return nserr.New(nserr.Usage,
					"environment %q declares no instance named %q; `nsctl repo list %s` lists them",
					env.Slug, instance, env.Slug)
			}

			entries := credentials.Resolve(cmd.Context(), o, reveal)
			if asJSON {
				return printJSON(cmd, credentialsDocument(env, entries, reveal))
			}
			renderCredentials(cmd.OutOrStdout(), env, entries, reveal)
			return nil
		},
	}
	cmd.Flags().BoolVar(&reveal, "reveal", false, "Print the credential values, not only where they live")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	cmd.Flags().StringVar(&instance, "instance", "", "Only this instance")
	return cmd
}

// classReader loads a declared instance's repo class manifest through the same
// resolver a deploy uses, so a listing reads the tiers the deploy will. It never
// fetches: every tier either stats the filesystem or answers from the manifest,
// which is what keeps this usable with nothing running.
func classReader(opts *Options, home string, repos []manifest.Repo) credentials.ClassReader {
	resolver := repoclass.NewWithHome(opts.Lookup("HMD_REPO_HOME"), home, opts.Lookup)
	repoclass.Seed(resolver, repos)
	return func(class string) (*bacon.Store, error) {
		dir := resolver.Dir(class)
		if dir == "" {
			return nil, fmt.Errorf("no tree for %s on this machine", class)
		}
		return bacon.Open(dir)
	}
}

func declaresInstance(repos []manifest.Repo, instance string) bool {
	for _, r := range repos {
		if r.InstanceName == instance {
			return true
		}
	}
	return false
}

func renderCredentials(out io.Writer, env *registry.Environment, entries []credentials.Entry, reveal bool) {
	if len(entries) == 0 {
		fmt.Fprintf(out, "No instance in %q declares how to reach it.\n", env.Slug)
		fmt.Fprintln(out, "A repo class says so with `nsctl repoclass access add`; see `nsctl repoclass access --help`.")
		return
	}
	fmt.Fprintf(out, "Access to %q:\n", env.Slug)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := "  INSTANCE\tNAME\tURL\tUSERNAME\tCREDENTIAL"
	if reveal {
		header = "  INSTANCE\tNAME\tURL\tUSERNAME\tSECRET"
	}
	fmt.Fprintln(w, header)
	for _, e := range entries {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
			e.Instance, e.Name, orDash(e.URL), orDash(e.Username), credentialCell(e, reveal))
	}
	w.Flush()

	for _, e := range entries {
		if e.Notes != "" {
			fmt.Fprintf(out, "\n%s/%s: %s\n", e.Instance, e.Name, e.Notes)
		}
	}
	if !reveal && anySecret(entries) {
		// Named rather than left to be discovered: withholding a value is only
		// reasonable if getting it is one line away.
		fmt.Fprintf(out, "\nRead a value with `nsctl env credentials %s --reveal`.\n", env.Slug)
	}
}

// credentialCell is what one entry's credential column says.
//
// A problem displaces the value rather than sitting beside it, because an entry
// that did not resolve has no value and a blank cell reads as "no credential".
func credentialCell(e credentials.Entry, reveal bool) string {
	if e.Problem != "" {
		return "unresolved: " + e.Problem
	}
	if !e.HasSecret() {
		return "none declared"
	}
	if reveal {
		return e.Secret
	}
	where := e.Store + " " + e.Key
	if e.Property != "" {
		where += "#" + e.Property
	}
	return where
}

func anySecret(entries []credentials.Entry) bool {
	for _, e := range entries {
		if e.HasSecret() {
			return true
		}
	}
	return false
}

// credentialsDocument is the --json shape. The secret key is present only when
// a value was asked for and resolved, so a document produced without --reveal
// cannot carry one by accident.
func credentialsDocument(env *registry.Environment, entries []credentials.Entry, reveal bool) map[string]any {
	list := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		item := map[string]any{
			"instance":   e.Instance,
			"repo_class": e.RepoClass,
			"name":       e.Name,
			"url":        e.URL,
		}
		if e.Username != "" {
			item["username"] = e.Username
		}
		if e.Notes != "" {
			item["notes"] = e.Notes
		}
		if e.HasSecret() {
			secret := map[string]any{"store": e.Store, "key": e.Key}
			if e.Property != "" {
				secret["property"] = e.Property
			}
			if reveal && e.Secret != "" {
				secret["value"] = e.Secret
			}
			item["secret"] = secret
		}
		if e.Problem != "" {
			item["problem"] = e.Problem
		}
		list = append(list, item)
	}
	return map[string]any{"environment": env.Slug, "access": list}
}
