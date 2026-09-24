package cmd

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// newRepoClassAccessCommand authors the `access` declaration (NERD023 SPEC004):
// how a deployed instance of this class is reached, who logs in, and where the
// credential lives.
//
// The declaration exists because the alternative is inference, and nsctl cannot
// infer this well. It can see an Ingress host and a Secret in a cluster, and it
// cannot know which host a human uses, which secret backs it, or that
// self-registration is off. The author knows. A confident wrong answer about a
// credential is worse than no answer.
func newRepoClassAccessCommand(path *repoClassPath) *cobra.Command {
	group := &cobra.Command{
		Use:   "access",
		Short: "Declare how a deployed instance of this repo class is reached",
		Long: `Write, list or remove the manifest's "access": one entry per way in to a
deployed instance -- a URL, the user who logs in, and where the credential
lives. It never holds a credential: a manifest is a file a developer edits and
may commit, so an entry names a store and a key and "nsctl repoclass validate"
refuses a literal password.

A url, secret key or secret property may use the placeholders {instance_name},
{repo_class_name}, {deployment_id}, {environment} and {ingress_host}, which is
what makes one declaration work in every environment under any instance name.
"nsctl env credentials <env>" fills them in and resolves the secret.`,
		Example: `  nsctl repoclass access add superset --url 'http://{ingress_host}/' --username admin \
      --secret-store secrets-manager \
      --secret-key '{instance_name}-{deployment_id}-{environment}-admin-credentials' \
      --secret-property password
  nsctl repoclass access add api --url 'http://{ingress_host}/api/' --secret-store parameter-store --secret-output secret_name
  nsctl repoclass access list
  nsctl repoclass access remove superset`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newAccessAddCommand(path),
		newAccessRemoveCommand(path),
		newAccessListCommand(path),
	)
	return group
}

func newAccessAddCommand(path *repoClassPath) *cobra.Command {
	var e bacon.AccessEntry
	var store, key, property, output string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or replace one way in, keyed on its name",
		Long: `Keyed and idempotent: re-running with the same name replaces that entry
wholesale rather than adding a second or merging halves of both. --secret-store
is required with any other --secret-* flag, and a secret names either a key or
the resource output to read its name from.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			e.Name = args[0]
			if store != "" || key != "" || property != "" || output != "" {
				if store == "" {
					return nserr.New(nserr.Usage,
						"--secret-store is required with any other --secret-* flag: %s or %s. "+
							"It cannot be inferred -- create_secret() writes Parameter Store whatever its name "+
							"suggests, and a chart may read Secrets Manager",
						bacon.StoreSecretsManager, bacon.StoreParameterStore)
				}
				e.Secret = &bacon.AccessSecret{Store: store, Key: key, Property: property, Output: output}
			}
			k, err := bacon.AddAccess(s.Doc, e)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			return saveAndReport(cmd, s, k)
		},
	}
	cmd.Flags().StringVar(&e.URL, "url", "", "Where it is reached; may use placeholders (required)")
	cmd.Flags().StringVar(&e.Username, "username", "", "The user who logs in, when there is a fixed one")
	cmd.Flags().StringVar(&e.Notes, "notes", "", "One line a reader needs that the fields do not carry")
	cmd.Flags().StringVar(&store, "secret-store", "",
		fmt.Sprintf("Where the credential lives: %s or %s", bacon.StoreSecretsManager, bacon.StoreParameterStore))
	cmd.Flags().StringVar(&key, "secret-key", "", "The secret's name; may use placeholders")
	cmd.Flags().StringVar(&property, "secret-property", "", "The field inside a JSON secret, e.g. password")
	cmd.Flags().StringVar(&output, "secret-output", "",
		"A resource output key to read the secret's name from, instead of --secret-key")
	return cmd
}

func newAccessRemoveCommand(path *repoClassPath) *cobra.Command {
	return &cobra.Command{
		Use:           "remove <name>",
		Short:         "Remove one way in",
		Long:          `Removing the last entry removes the "access" key, so a repo class that declares no front door does not carry an empty list saying so.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			key, ok := bacon.RemoveAccess(s.Doc, args[0])
			if !ok {
				return nserr.New(nserr.Usage, "no access entry named %q", args[0])
			}
			return saveAndReport(cmd, s, key)
		},
	}
}

func newAccessListCommand(path *repoClassPath) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print the declared ways in",
		Long: `As written, not as resolved: a repository has no instance name and no
environment, so the placeholders stand. Use "nsctl env credentials <env>" to see
them filled in against a deployment.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := path.open()
			if err != nil {
				return err
			}
			entries := bacon.ReadAccess(s.Doc)
			bacon.SortAccess(entries)
			if asJSON {
				list, _ := s.Doc.Array("access")
				out := bacon.NewObject()
				out.Set("access", list)
				data, err := bacon.Encode(out)
				if err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			renderAccessList(cmd.OutOrStdout(), entries)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the declaration as JSON")
	return cmd
}

func renderAccessList(out io.Writer, entries []bacon.AccessEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(out, "No access declared.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tURL\tUSERNAME\tCREDENTIAL")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Name, orDash(e.URL), orDash(e.Username), accessCredentialLabel(e))
	}
	w.Flush()
	for _, e := range entries {
		if e.Notes != "" {
			fmt.Fprintf(out, "\n%s: %s\n", e.Name, e.Notes)
		}
	}
}
