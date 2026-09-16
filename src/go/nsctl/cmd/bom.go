package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

func newBOMCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bom",
		Short: "Read a cloud environment's Bill of Materials",
		Long: `Reads what a cloud environment is running, and builds a local one from part of it.

A cloud environment is a known-good version set -- somebody is running it in
earnest -- which makes it the strongest thing to seed a local environment from.
` + "`nsctl bom show`" + ` says what one is running; ` + "`nsctl bom import`" + ` copies a
selection of it down and declares it here.

Every verb here reads a cloud service and writes only local files. Nothing in
nsctl writes to a cloud hmd-ms-deployment: a BOM is an input to a local
manifest, never an output to a cloud deploy.

Which tenant is answered by --profile, from ` + nsconfig.Name + `. See NERD012.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newBOMEnvsCommand(opts),
		newBOMShowCommand(opts),
		newBOMImportCommand(opts),
	)
	return cmd
}

// cloudServices holds the flags a bom verb shares.
//
// Two endpoints, resolved independently from one profile: the deployment
// service the BOM comes from, and the librarian the artifacts come from. They
// are ordinarily the same tenant and nothing requires them to be.
type cloudServices struct {
	deploymentURL string
	librarianURL  string
	localURL      string
	profile       profileFlag
}

func (s *cloudServices) bindRead(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.deploymentURL, "url", "",
		"cloud hmd-ms-deployment URL, overriding "+nsconfig.DeploymentURLEnv)
	s.profile.bind(cmd)
}

func (s *cloudServices) bindFetch(cmd *cobra.Command) {
	s.bindRead(cmd)
	cmd.Flags().StringVar(&s.librarianURL, "librarian-url", "",
		"cloud Artifact Librarian URL, overriding "+librarian.URLEnv)
	cmd.Flags().StringVar(&s.localURL, "local-url", librarian.LocalBaseURL,
		"the control plane's Artifact Librarian")
}

// deployment builds the cloud deployment client, reporting a shadowed endpoint.
func (s *cloudServices) deployment(cmd *cobra.Command, opts *Options) (*msdeploy.Client, error) {
	profile, err := s.profile.resolve(opts)
	if err != nil {
		return nil, err
	}
	c, err := msdeploy.NewCloud(msdeploy.CloudConfig{
		Home:    opts.Home,
		Lookup:  opts.Lookup,
		Profile: profile,
		FlagURL: s.deploymentURL,
	})
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	reportShadowed(cmd, &s.profile, nsconfig.Deployment, c.Endpoint)
	return c, nil
}

// librarianClient builds the cloud librarian client from the same profile.
func (s *cloudServices) librarianClient(cmd *cobra.Command, opts *Options) (*librarian.Client, error) {
	profile, err := s.profile.resolve(opts)
	if err != nil {
		return nil, err
	}
	c, err := librarian.New(librarian.Config{
		Home:    opts.Home,
		Lookup:  opts.Lookup,
		Profile: profile,
		FlagURL: s.librarianURL,
	})
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	reportShadowed(cmd, &s.profile, nsconfig.ArtifactLibrarian, c.Endpoint)
	return c, nil
}

// roleResolver builds the reader the closure asks "is this role required".
//
// fetching decides whether it may go and get a manifest it does not have. A
// resolver that may not fetch is not a degraded one: it answers from the
// artifact cache, and every class it could not read is reported as such, so the
// closure over-approximates rather than guesses.
func (s *cloudServices) roleResolver(cmd *cobra.Command, opts *Options, home string,
	fetching bool) (*roleResolver, error) {

	if !fetching {
		return newRoleResolver(cmd.Context(), home, nil, nil), nil
	}
	cloud, err := s.librarianClient(cmd, opts)
	if err != nil {
		return nil, err
	}
	return newRoleResolver(cmd.Context(), home, cloud, librarian.NewLocal(s.localURL)), nil
}

func newBOMEnvsCommand(opts *Options) *cobra.Command {
	var svc cloudServices

	cmd := &cobra.Command{
		Use:     "envs",
		Aliases: []string{"environments"},
		Short:   "List the environments a cloud deployment service knows",
		Long: `Lists every environment in a tenant's deployment graph.

No instance counts: nothing carries one, so reporting them would mean fetching
every environment's BOM -- one expensive request each, hidden behind a listing
that looks cheap. ` + "`nsctl bom show <env>`" + ` is where a count comes from.`,
		Example: `  nsctl bom envs
  nsctl bom envs --profile acme`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := svc.deployment(cmd, opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Asking %s what environments it has...\n", client.BaseURL)
			envs, err := client.Environments(cmd.Context())
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			if len(envs) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s has no environments.\n", client.BaseURL)
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ENVIRONMENT\tACCOUNT\tREGION")
			for _, e := range envs {
				fmt.Fprintf(w, "%s\t%s\t%s\n", e.Type, orDash(e.AccountNumber), orDash(e.Region))
			}
			return w.Flush()
		},
	}
	svc.bindRead(cmd)
	return cmd
}

func newBOMShowCommand(opts *Options) *cobra.Command {
	var svc cloudServices
	var sel selectionFlags
	var asJSON, resolve bool
	var saveSelection string

	cmd := &cobra.Command{
		Use:   "show <env>",
		Short: "Show what a cloud environment is running",
		Long: `Prints a cloud environment's Bill of Materials: every deployed instance, the
concrete version it is running, and whether this machine already holds that
artifact.

With selection flags it prints exactly what ` + "`nsctl bom import`" + ` would take,
dependency closure included -- so a show is a preview of the import that
follows it, and needs no --dry-run of its own.

The closure follows only the roles a repo class marks required, which is read
from that class's own manifest -- and that manifest arrives with its artifact.
Without --resolve this reads whatever is already unpacked here and closes over
every role of everything else, saying how many. With --resolve it fetches what
it needs, and the preview is exact.

Only instances whose current deployment is DEPLOYED or FAILED appear at all:
the service builds the BOM from those and drops the rest, so this is not a
listing of everything the environment declares.`,
		Example: `  nsctl bom show dev
  nsctl bom show dev --instance ms-transform
  nsctl bom show dev --class hmd-inf-trino --json`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			client, err := svc.deployment(cmd, opts)
			if err != nil {
				return err
			}
			bom, err := fetchBOM(cmd, client, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd, bom)
			}

			out := cmd.OutOrStdout()
			// With no selection this is the whole BOM, which is what "show me
			// what dev is running" means.
			if !sel.any() {
				fmt.Fprintf(out, "%s is running %s.\n\n", args[0], count(len(bom), "instance"))
				return reportBOM(out, home, bom, nil)
			}

			roles, err := svc.roleResolver(cmd, opts, home, resolve)
			if err != nil {
				return err
			}
			selection, err := sel.apply(bom, alwaysFalse, manifest.Reserved, roles.facts)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s is running %s; this selects %d of them.\n\n",
				args[0], count(len(bom), "instance"), len(selection.Picked))
			if err := reportBOM(out, home, selection.Picked, selection.viaDeps); err != nil {
				return err
			}
			selection.report(cmd)
			return saveSelectionTo(cmd, saveSelection, args[0], selection, bom)
		},
	}
	svc.bindFetch(cmd)
	sel.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the BOM as the service returned it")
	cmd.Flags().BoolVar(&resolve, "resolve", false,
		"Fetch the artifacts needed to read which roles are required, making this an exact preview")
	cmd.Flags().StringVar(&saveSelection, "save-selection", "",
		"Write this selection to a `file` to edit and pass back to bom import --selection")
	return cmd
}

func newBOMImportCommand(opts *Options) *cobra.Command {
	var svc cloudServices
	var sel selectionFlags
	var envName string
	var dryRun, noPull, resolve, apply, verbose bool
	var selectionPath, saveSelection string

	cmd := &cobra.Command{
		Use:   "import <env>",
		Short: "Declare part of a cloud environment locally",
		Long: `Copies a selection of a cloud environment's BOM into a local environment.

Each selected instance is declared at the version the cloud is running, from an
artifact -- never from a checkout, because a version that came from a cloud
librarian must not silently resolve to whatever happens to be under
$HMD_REPO_HOME. The artifacts are fetched into the control plane's Artifact
Librarian on the way, so what is declared can actually deploy.

The selection is closed under the roles the repo classes mark *required*.
hmd-ms-deployment fails a whole ChangeSet on a required role nothing fills, so
importing one instance without what fills those roles would produce a manifest
that cannot deploy -- and the failure would arrive at ` + "`nsctl env apply`" + `
naming a role rather than the import that omitted it. Optional roles are not
followed, and are listed; --with <role> follows one anyway.

Roles filled by the environment substrate are bound rather than imported. A
required role nothing here fills is bound to the core instance when only its
presence is validated -- reported, because the core instance does not deploy
whatever was stubbed -- and refused when the role asks for a resource type,
which cannot be faked. --no-stub-roles refuses in both cases; --no-deps takes
the selection literally instead.

To narrow a large closure by hand, write it out with
` + "`nsctl bom show <env> --save-selection <file>`" + `, edit the take lines, and
pass it back with --selection. See NERD013.

Declaring is not deploying. Run ` + "`nsctl env apply`" + ` afterwards, or pass
--apply to run it here once the selection is declared. A partial import -- one
where an artifact could not be fetched -- is declared but never applied.`,
		Example: `  nsctl bom import dev --instance ms-transform
  nsctl bom import dev --instance ms-transform --apply
  nsctl bom import dev --class hmd-inf-trino --dry-run
  nsctl bom import dev --all --exclude superset --env scratch`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			if !sel.any() && selectionPath == "" {
				return nserr.New(nserr.Usage,
					"nothing selected. Importing a whole cloud environment is a thing to ask for out loud:\n"+
						"  one instance:   nsctl bom import %s --instance <name>\n"+
						"  one repo class: nsctl bom import %s --class <repo-class>\n"+
						"  all of it:      nsctl bom import %s --all\n"+
						"  from a file:    nsctl bom import %s --selection <file>\n"+
						"`nsctl bom show %s` lists what is there.",
					args[0], args[0], args[0], args[0], args[0])
			}
			// Refuse the contradictions before anything is fetched. A dry run
			// deploys nothing by definition, and an apply refuses an uncached
			// artifact anyway -- better said here than minutes from now.
			if apply && dryRun {
				return nserr.New(nserr.Usage, "--apply and --dry-run contradict: a dry run deploys nothing")
			}
			if apply && noPull {
				return nserr.New(nserr.Usage,
					"--apply needs the artifacts here, and --no-pull leaves them where they are. Drop one")
			}

			_, _, slug, err := resolveEnvSlug(opts, envName)
			if err != nil {
				return err
			}
			m, err := openManifest(opts, home, slug)
			if err != nil {
				return err
			}

			client, err := svc.deployment(cmd, opts)
			if err != nil {
				return err
			}
			bom, err := fetchBOM(cmd, client, args[0])
			if err != nil {
				return err
			}

			if err := sel.applySelectionFile(selectionPath, bom); err != nil {
				return err
			}

			// NERD013 SPEC005. A run that is going to fetch anyway resolves
			// role declarations as it goes; a --dry-run fetches nothing unless
			// asked, and says what it therefore could not read.
			roles, err := svc.roleResolver(cmd, opts, home, resolve || !(dryRun || noPull))
			if err != nil {
				return err
			}
			declared := func(name string) bool { _, ok := m.Repo(name); return ok }
			selection, err := sel.apply(bom, declared, manifest.Reserved, roles.facts)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if len(selection.Picked) == 0 {
				fmt.Fprintf(out, "Nothing to import: %s already declares everything selected from %s.\n",
					m.Path, args[0])
				selection.report(cmd)
				return nil
			}

			fmt.Fprintf(out, "From %s into %s:\n", args[0], m.Path)
			if err := reportBOM(out, home, selection.Picked, selection.viaDeps); err != nil {
				return err
			}
			selection.report(cmd)
			if err := saveSelectionTo(cmd, saveSelection, args[0], selection, bom); err != nil {
				return err
			}

			if dryRun {
				// --resolve fetched manifests in order to read which roles are
				// required, so the usual sentence would be false -- and a
				// --dry-run is worth nothing if you cannot believe it.
				if resolve {
					fmt.Fprintf(out,
						"\n%s would be declared. Nothing was written; artifacts were fetched"+
							" only to read which roles are required.\n",
						count(len(selection.Picked), "instance"))
					return nil
				}
				fmt.Fprintf(out, "\n%s would be declared. Nothing was written and nothing was fetched.\n",
					count(len(selection.Picked), "instance"))
				return nil
			}

			// Fetch before declare, always. An import must never leave behind a
			// declaration whose artifact is not here, because that is a
			// manifest that fails at apply for a reason the manifest does not
			// mention.
			fetched := selection.Picked
			var failures []string
			if !noPull {
				fetched, failures, err = fetchSelection(cmd, opts, &svc, home, selection.Picked)
				if err != nil {
					return err
				}
			}
			if len(fetched) == 0 {
				return nserr.New(nserr.Fail,
					"nothing could be fetched, so nothing was declared:\n  %s", strings.Join(failures, "\n  "))
			}

			added := make([]manifest.Repo, 0, len(fetched))
			for _, e := range fetched {
				added = append(added, bomDeclaration(e, selection.resolvedRoles(e)))
			}
			candidate := *m
			candidate.Repos = append(append([]manifest.Repo{}, m.Repos...), added...)
			if problems := candidate.Validate(opts.Lookup); len(problems) > 0 {
				return nserr.New(nserr.Usage,
					"the imported manifest would not be valid:\n  - %s", strings.Join(problems, "\n  - "))
			}
			m.Repos = candidate.Repos
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}

			fmt.Fprintf(out, "\nDeclared %s in %s\n", count(len(added), "instance"), m.Path)
			if noPull {
				fmt.Fprintln(out, "Nothing was fetched. Before applying:")
				for _, e := range fetched {
					fmt.Fprintf(out, "  nsctl artifact pull %s\n", bomSpec(e))
				}
			}

			if len(failures) > 0 {
				// A partial import is declared and never applied. NERD012
				// SPEC006 has this exit non-zero naming what is missing, and
				// deploying a knowingly incomplete import on the way out would
				// be the quiet failure that rule exists to prevent.
				fmt.Fprintf(out, "Run `nsctl env apply %s` to deploy it.\n", slug)
				msg := "%s could not be fetched and %s not declared:\n  %s"
				if apply {
					msg += "\nNot applied, because the import was partial. Run `nsctl env apply " + slug + "` to deploy what was declared."
				}
				return nserr.New(nserr.Fail, msg,
					count(len(failures), "artifact"), wereOrWas(len(failures)), strings.Join(failures, "\n  "))
			}
			if !apply {
				fmt.Fprintf(out, "Run `nsctl env apply %s` to deploy it.\n", slug)
				return nil
			}

			// The same call `nsctl env apply` makes, against the same name the
			// user gave (empty means the default environment), so the two
			// forms cannot drift.
			fmt.Fprintf(out, "Applying %s...\n\n", slug)
			return environment.Apply(cmd.Context(), &environment.Options{
				Home: home, Lookup: opts.Lookup, Verbose: verbose,
				Out: out, Err: cmd.ErrOrStderr(),
			}, envName)
		},
	}
	svc.bindFetch(cmd)
	sel.bind(cmd)
	cmd.Flags().StringVar(&envName, "env", "", "Local environment to declare into (default: the default environment)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be declared, writing and fetching nothing")
	cmd.Flags().BoolVar(&noPull, "no-pull", false, "Declare without fetching, naming the pulls to run")
	cmd.Flags().BoolVar(&resolve, "resolve", false,
		"With --dry-run or --no-pull, still fetch what is needed to read which roles are required")
	cmd.Flags().StringVar(&selectionPath, "selection", "",
		"Take the instances an edited selection `file` marks take = true")
	cmd.Flags().StringVar(&saveSelection, "save-selection", "",
		"Write what this run resolved to a `file`, to edit and pass back with --selection")
	cmd.Flags().BoolVar(&apply, "apply", false,
		"Run nsctl env apply on the environment once the selection is declared")
	cmd.Flags().BoolVarP(&verbose, "verbose", "V", false, "With --apply, show the underlying command output")
	return cmd
}

// fetchBOM asks for an environment's BOM, mapping "no such environment" to a
// usage error that lists the ones there are.
//
// get_valid_environment asserts exactly one match and fails the request
// otherwise, so an unknown slug arrives as a server error. Answering that with
// the listing is the difference between a message about an assertion and one
// about a typo.
func fetchBOM(cmd *cobra.Command, client *msdeploy.Client, envType string) ([]msdeploy.BOMEntry, error) {
	fmt.Fprintf(cmd.ErrOrStderr(), "Asking %s for %s's BOM...\n", client.BaseURL, envType)
	bom, err := client.DeploymentBOM(cmd.Context(), envType)
	if err == nil {
		return bom, nil
	}
	envs, listErr := client.Environments(cmd.Context())
	if listErr == nil && !containsEnv(envs, envType) {
		return nil, nserr.New(nserr.Usage, "no environment %q at %s. It has: %s",
			envType, client.BaseURL, strings.Join(msdeploy.EnvironmentTypes(envs), ", "))
	}
	return nil, nserr.Wrap(nserr.Fail, err)
}

func containsEnv(envs []msdeploy.Environment, envType string) bool {
	for _, e := range envs {
		if e.Type == envType {
			return true
		}
	}
	return false
}

// reportBOM prints the table both show and import use.
func reportBOM(out interface{ Write([]byte) (int, error) }, home string,
	entries []msdeploy.BOMEntry, viaDeps map[string]string) error {

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := "INSTANCE\tREPO CLASS\tVERSION\tSTATUS\tCACHED"
	if viaDeps != nil {
		header += "\tSELECTED"
	}
	fmt.Fprintln(w, header)
	for _, e := range entries {
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			e.RepoInstanceName, e.RepoClassName, orDash(e.RepoClassVersion),
			strings.ToLower(orDash(e.Status)), yesNo(artifact.Cached(home, e.RepoClassName, e.RepoClassVersion)))
		if viaDeps != nil {
			if why, ok := viaDeps[e.RepoInstanceName]; ok {
				row += "\t" + why
			} else {
				row += "\tasked for"
			}
		}
		fmt.Fprintln(w, row)
	}
	return w.Flush()
}

func writeJSON(cmd *cobra.Command, bom []msdeploy.BOMEntry) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(bom)
}

// fetchSelection copies each selected artifact into the control plane.
//
// Every entry is attempted before anything is reported as failed: stopping at
// the first failure would leave a half-imported manifest whose remaining work
// is a different command each time.
func fetchSelection(cmd *cobra.Command, opts *Options, svc *cloudServices, home string,
	entries []msdeploy.BOMEntry) ([]msdeploy.BOMEntry, []string, error) {

	var needed []msdeploy.BOMEntry
	held := map[string]bool{}
	for _, e := range entries {
		if artifact.Cached(home, e.RepoClassName, e.RepoClassVersion) {
			held[e.RepoInstanceName] = true
			continue
		}
		needed = append(needed, e)
	}

	out := cmd.OutOrStdout()
	if len(needed) == 0 {
		fmt.Fprintf(out, "\nEvery artifact is already here.\n")
		return entries, nil, nil
	}

	cloud, err := svc.librarianClient(cmd, opts)
	if err != nil {
		return nil, nil, err
	}
	local := librarian.NewLocal(svc.localURL)

	fmt.Fprintf(out, "\nFetching %s from %s:\n", count(len(needed), "artifact"), cloud.BaseURL)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	var failures []string
	fetchedOK := map[string]bool{}
	for _, e := range needed {
		spec := bomSpec(e)
		err := pullArtifact(cmd.Context(), cloud, local, home, spec)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", spec, err))
			fmt.Fprintf(w, "  %s\t%s\tfailed\n", e.RepoClassName, e.RepoClassVersion)
			continue
		}
		fetchedOK[e.RepoInstanceName] = true
		fmt.Fprintf(w, "  %s\t%s\tok\n", e.RepoClassName, e.RepoClassVersion)
	}
	if err := w.Flush(); err != nil {
		return nil, nil, err
	}

	kept := make([]msdeploy.BOMEntry, 0, len(entries))
	for _, e := range entries {
		if held[e.RepoInstanceName] || fetchedOK[e.RepoInstanceName] {
			kept = append(kept, e)
		}
	}
	return kept, failures, nil
}

// pullArtifact copies one artifact into the control plane, unless it is already
// unpacked here.
//
// The one path both callers take: fetchSelection, which is fetching in order to
// declare, and roleResolver, which is fetching in order to read which roles are
// required. What must not differ between them is the already-here check and
// storeArtifact's invalidate-before-store order, which is why this is one
// function rather than two loops that happen to agree today.
func pullArtifact(ctx context.Context, cloud, local *librarian.Client, home string,
	spec librarian.Spec) error {

	if artifact.Cached(home, spec.Name, spec.Version) {
		return nil
	}
	data, err := cloud.Fetch(ctx, spec.ContentPath())
	if err != nil {
		return err
	}
	_, err = storeArtifact(ctx, local, home, spec, data)
	return err
}

// bomSpec is the artifact address a BOM entry already is: a repo class and a
// concrete version.
func bomSpec(e msdeploy.BOMEntry) librarian.Spec {
	return librarian.Spec{
		Name:     e.RepoClassName,
		Version:  e.RepoClassVersion,
		ItemType: manifest.DefaultArtifactType,
	}
}

// bomDeclaration turns a BOM entry into a manifest declaration.
//
// The source is set to artifact explicitly rather than left to default. The
// version came from a cloud librarian, and the default is a working tree: a
// declaration that could silently resolve to whatever sits under $HMD_REPO_HOME
// would deploy something other than what the cloud is running, which is the one
// thing this whole verb exists to prevent.
func bomDeclaration(e msdeploy.BOMEntry, roles map[string]any) manifest.Repo {
	r := manifest.Repo{
		InstanceName:          e.RepoInstanceName,
		RepoClassName:         e.RepoClassName,
		Version:               e.RepoClassVersion,
		Source:                &manifest.Source{Type: manifest.SourceArtifact},
		InstanceConfiguration: e.InstanceConfiguration,
	}
	if len(roles) > 0 {
		r.Dependencies = roles
	}
	return r
}

func wereOrWas(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func alwaysFalse(string) bool { return false }

// sortedKeysOf is a small helper for reporting maps in a stable order.
func sortedKeysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
