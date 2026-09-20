package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/spf13/cobra"
)

func newRepoCommand(opts *Options) *cobra.Command {
	repo := &cobra.Command{
		Use:   "repo",
		Short: "Declare the repo instances an environment deploys",
		Long: `Edits the environment manifest at $HMD_HOME/environments/<env>.yaml.

These verbs are wrappers: they change that file and nothing else. Editing it
by hand is equivalent, and either way ` + "`nsctl env apply`" + ` is what deploys the
result -- so nothing here touches a running environment on its own.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	repo.AddCommand(
		newRepoAddCommand(opts),
		newRepoRemoveCommand(opts),
		newRepoListCommand(opts),
		newRepoImportCommand(opts),
	)
	return repo
}

// resolveEnvSlug picks the environment to act on: the flag, else the default.
func resolveEnvSlug(opts *Options, name string) (*registry.Registry, string, string, error) {
	reg, home, err := loadRegistry(opts)
	if err != nil {
		return nil, "", "", err
	}
	env, err := reg.Environment(name, opts.Lookup)
	if err != nil {
		return nil, "", "", nserr.Wrap(nserr.Usage, err)
	}
	return reg, home, env.Slug, nil
}

// openManifest reads the environment's manifest, or builds an empty one when
// it has none so the first `repo add` creates the file rather than failing.
func openManifest(opts *Options, home, slug string) (*manifest.Manifest, error) {
	m, err := manifest.Load(home, slug, opts.Lookup)
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	if m == nil {
		m = &manifest.Manifest{Version: manifest.Version, Name: slug,
			Path: manifest.DefaultPath(home, slug)}
	}
	if m.Path == "" {
		m.Path = manifest.DefaultPath(home, slug)
	}
	return m, nil
}

func newRepoAddCommand(opts *Options) *cobra.Command {
	var envName, instanceName, path string
	var depends, config []string

	cmd := &cobra.Command{
		Use:   "add <repo-class>[@<version>]",
		Short: "Declare a repo instance in the environment manifest",
		Long: `Adds a repo class to the environment's manifest.

The instance is named after the repo class with its hmd- prefix dropped unless
--name says otherwise, so ` + "`nsctl repo add hmd-ms-transform`" + ` declares an
instance called ms-transform.

Declaring is not deploying. Run ` + "`nsctl env apply`" + ` to deploy the result.`,
		Example: `  nsctl repo add hmd-ms-transform
  nsctl repo add hmd-ms-transform@0.3 --name transform
  nsctl repo add hmd-inf-trino --depends eks-cluster=eks-cluster --depends database-instance=environment-db
  nsctl repo add hmd-ms-myapi --path ~/work/hmd-ms-myapi --config replicas=2`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoClass, version := splitVersion(args[0])
			if repoClass == "" {
				return nserr.New(nserr.Usage, "a repo class name is required")
			}
			_, home, slug, err := resolveEnvSlug(opts, envName)
			if err != nil {
				return err
			}
			m, err := openManifest(opts, home, slug)
			if err != nil {
				return err
			}

			name := instanceName
			if name == "" {
				name = defaultInstanceName(repoClass)
			}
			if manifest.Reserved(name) {
				return nserr.New(nserr.Usage,
					"%q is reserved for the environment substrate; pass --name to choose another. Reserved: %s",
					name, strings.Join(manifest.ReservedNames(), ", "))
			}
			if _, exists := m.Repo(name); exists {
				return nserr.New(nserr.Usage,
					"%q is already declared in %s. Remove it first, or pass --name to declare a second instance.",
					name, m.Path)
			}

			dependencies, err := parseDepends(depends)
			if err != nil {
				return err
			}
			configuration, err := parseConfig(config)
			if err != nil {
				return err
			}
			declaration := manifest.Repo{
				InstanceName:          name,
				RepoClassName:         repoClass,
				Version:               version,
				InstanceConfiguration: configuration,
				Dependencies:          dependencies,
			}
			if path != "" {
				// Absolute, because the manifest is read from wherever `env
				// apply` runs and the path ends up as a bind-mount source the
				// Docker daemon resolves. A path carrying a variable is left
				// as written for RepoPath to expand.
				if !filepath.IsAbs(path) && !strings.Contains(path, "$") {
					if abs, err := filepath.Abs(path); err == nil {
						path = abs
					}
				}
				declaration.Source = &manifest.Source{Type: manifest.SourceLocal, Path: path}
			}

			// Validate the whole manifest, not just the addition: writing a
			// file that `env apply` will refuse helps nobody.
			candidate := *m
			candidate.Repos = append(append([]manifest.Repo{}, m.Repos...), declaration)
			if problems := candidate.Validate(opts.Lookup); len(problems) > 0 {
				return nserr.New(nserr.Usage, "cannot declare %s:\n  - %s",
					name, strings.Join(problems, "\n  - "))
			}

			m.Repos = candidate.Repos
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Declared %s (%s) in %s\n", name, repoClass, m.Path)
			fmt.Fprintf(cmd.OutOrStdout(), "Run `nsctl env apply %s` to deploy it.\n", slug)
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to declare it in (default: the default environment)")
	cmd.Flags().StringVar(&instanceName, "name", "", "Instance name (default: the repo class without its hmd- prefix)")
	cmd.Flags().StringVar(&path, "path", "", "Working tree to deploy from (default: $HMD_REPO_HOME/<repo-class>)")
	cmd.Flags().StringArrayVar(&depends, "depends", nil, "A dependency as role=instance; repeatable")
	cmd.Flags().StringArrayVar(&config, "config", nil, "An instance configuration value as key=value; repeatable")
	return cmd
}

func newRepoRemoveCommand(opts *Options) *cobra.Command {
	var envName string
	cmd := &cobra.Command{
		Use:     "remove <instance>",
		Aliases: []string{"rm"},
		Short:   "Undeclare a repo instance",
		Long: `Removes an instance from the environment's manifest.

This edits the manifest only. What is already deployed stays deployed: nsctl
does not tear an instance down on your behalf.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			_, home, slug, err := resolveEnvSlug(opts, envName)
			if err != nil {
				return err
			}
			m, err := manifest.Load(home, slug, opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if m == nil {
				return nserr.New(nserr.Usage,
					"environment %q has no manifest, so it declares nothing to remove", slug)
			}
			if _, found := m.Repo(name); !found {
				return nserr.New(nserr.Usage, "%s does not declare %q. Declared: %s",
					m.Path, name, strings.Join(declaredNames(m), ", "))
			}

			kept := make([]manifest.Repo, 0, len(m.Repos))
			for _, r := range m.Repos {
				if r.InstanceName != name {
					kept = append(kept, r)
				}
			}
			m.Repos = kept
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s from %s\n", name, m.Path)
			fmt.Fprintf(cmd.ErrOrStderr(),
				"note: anything already deployed for %s is still running; nsctl does not destroy it for you\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to edit (default: the default environment)")
	return cmd
}

func newRepoListCommand(opts *Options) *cobra.Command {
	var envName string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show what an environment declares and what it has deployed",
		Long: `Lists the environment manifest's declarations beside the deployment graph.

The substrate is listed too, marked as such: nsctl deploys it whether or not a
manifest exists, so seeing it here explains instances you never declared.

An instance shown as undeclared is deployed but named by no manifest -- what a
deleted line leaves behind.

DECLARED and FROM answer two different questions. DECLARED is where the
declaration came from; FROM is where the version came from, which the
declaration cannot say -- an instance deploying 0.1.4 out of an artifact and one
deploying 0.1.4 out of a checkout are both declared in the manifest.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, home, slug, err := resolveEnvSlug(opts, envName)
			if err != nil {
				return err
			}
			env, err := reg.Environment(slug, opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			m, err := manifest.Load(home, slug, opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}

			// Best-effort: the manifest half is worth showing on its own, and
			// an environment that is simply stopped should not make this fail.
			status := map[string]string{}
			client := msdeploy.New(environment.MSDeploymentURL(opts.Lookup))
			if client.Reachable(cmd.Context()) {
				if s, err := client.InstanceStatus(cmd.Context(), env.DeploymentID); err == nil {
					status = s
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not read the deployment graph: %v\n", err)
				}
			} else {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"note: hmd-ms-deployment is not answering, so no deployed status is shown")
			}

			if m == nil {
				fmt.Fprintf(cmd.OutOrStdout(),
					"%s has no manifest; it deploys the substrate alone.\n"+
						"Declare something with `nsctl repo add <repo-class> --env %s`.\n\n", slug, slug)
			}

			// Two different facts, and one column used to carry both. DECLARED
			// says where the declaration came from; FROM says where the version
			// came from, which is what separates "0.1.4 from an artifact" from
			// "0.1.4 from a working tree". Answering the second needs a real
			// resolver, built the way internal/environment/apply.go builds one
			// so the listing reads the same tiers the deploy will.
			//
			// It never fetches and never fails: every tier either stats the
			// filesystem or answers from the manifest, so a listing stays
			// usable with nothing running.
			resolver := repoclass.NewWithHome(opts.Lookup("HMD_REPO_HOME"), home, opts.Lookup)
			if m != nil {
				repoclass.Seed(resolver, m.Repos)
			}

			var uncached []string
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "INSTANCE\tREPO CLASS\tVERSION\tDECLARED\tFROM\tDEPLOYED")
			// Only what this environment's substrate deploys (NERD014
			// SPEC007); the names stay reserved either way.
			for _, name := range bom.SubstrateNames(m.SubstrateMode()) {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					name, "-", "-", "substrate", "-", deployedCell(status, name))
			}
			if m != nil {
				declarations := append([]manifest.Repo{}, m.Repos...)
				sort.Slice(declarations, func(i, j int) bool {
					return declarations[i].InstanceName < declarations[j].InstanceName
				})
				for _, r := range declarations {
					resolution := resolver.ResolveVersion(r.RepoClassName, r.Version)
					from := string(resolution.Source)
					if resolution.Source == repoclass.SourceArtifact && resolution.Root == "" {
						// The version is known either way -- an artifact is
						// addressed *by* version -- but its bytes are not on
						// this machine, so that version will not deploy. This
						// listing is where a user would notice, rather than at
						// the next apply.
						from = "artifact (uncached)"
						uncached = append(uncached, librarian.Spec{
							Name: r.RepoClassName, Version: r.Version, ItemType: r.ArtifactType(),
						}.String())
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
						r.InstanceName, r.RepoClassName, orDash(resolution.Version),
						"manifest", orDash(from), deployedCell(status, r.InstanceName))
				}
			}
			// Deployed here but declared nowhere: what a removed line leaves
			// behind, and the only way to notice it. Nothing declares it, so
			// there is no version to resolve and nowhere for it to come from.
			for _, name := range msdeploy.DeployedNames(status) {
				if manifest.Reserved(name) {
					continue
				}
				if m != nil {
					if _, declared := m.Repo(name); declared {
						continue
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", name, "-", "-", "undeclared", "-", "yes")
			}
			if err := w.Flush(); err != nil {
				return err
			}
			for _, spec := range uncached {
				fmt.Fprintf(cmd.OutOrStdout(),
					"\n%s is declared but nothing is cached for it, so that version cannot deploy.\n"+
						"Fetch it with:  nsctl artifact pull %s\n", spec, spec)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to list (default: the default environment)")
	return cmd
}

func deployedCell(status map[string]string, name string) string {
	switch s, known := status[name]; {
	case len(status) == 0:
		return "?"
	case !known:
		return "no"
	case s == msdeploy.StatusDeployed:
		return "yes"
	case s == msdeploy.StatusNeverDeployed:
		return "no"
	default:
		return strings.ToLower(s)
	}
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func declaredNames(m *manifest.Manifest) []string {
	names := make([]string, 0, len(m.Repos))
	for _, r := range m.Repos {
		names = append(names, r.InstanceName)
	}
	if len(names) == 0 {
		return []string{"(nothing)"}
	}
	sort.Strings(names)
	return names
}

// splitVersion parses `hmd-ms-foo@0.3` into its parts. A bare name has no pin.
func splitVersion(arg string) (repoClass, version string) {
	if at := strings.LastIndex(arg, "@"); at > 0 {
		return arg[:at], arg[at+1:]
	}
	return arg, ""
}

// defaultInstanceName drops the hmd- prefix, matching how the substrate names
// its own instances (hmd-postgres-rds deploys as environment-db, but
// hmd-inf-eks-cluster deploys as eks-cluster).
func defaultInstanceName(repoClass string) string {
	return strings.TrimPrefix(repoClass, "hmd-")
}

// splitPair parses one key=value flag argument.
func splitPair(pair, flag string) (string, string, error) {
	key, value, found := strings.Cut(pair, "=")
	if !found || key == "" {
		return "", "", nserr.New(nserr.Usage, "%s expects key=value, got %q", flag, pair)
	}
	return key, value, nil
}

// parseDepends builds a dependency mapping. A comma-separated value becomes
// the list form the schema also allows, so a role satisfied by several
// instances can be written as --depends role=a,b.
func parseDepends(pairs []string) (map[string]any, error) {
	out := map[string]any{}
	for _, pair := range pairs {
		key, value, err := splitPair(pair, "--depends")
		if err != nil {
			return nil, err
		}
		if strings.Contains(value, ",") {
			targets := strings.Split(value, ",")
			list := make([]any, 0, len(targets))
			for _, t := range targets {
				if t = strings.TrimSpace(t); t != "" {
					list = append(list, t)
				}
			}
			out[key] = list
			continue
		}
		out[key] = value
	}
	return out, nil
}

// parseConfig builds an instance configuration mapping, reading each value as
// JSON when it parses as one.
//
// A configuration value is typed -- `replicas: 2` is an integer to whatever
// consumes it, and a quoted "2" is not the same thing. Falling back to the
// literal string keeps unquoted words working, so `--config profile=minimal`
// needs no JSON quoting.
func parseConfig(pairs []string) (map[string]any, error) {
	out := map[string]any{}
	for _, pair := range pairs {
		key, value, err := splitPair(pair, "--config")
		if err != nil {
			return nil, err
		}
		var typed any
		if err := json.Unmarshal([]byte(value), &typed); err == nil {
			out[key] = typed
			continue
		}
		out[key] = value
	}
	return out, nil
}

func newRepoImportCommand(opts *Options) *cobra.Command {
	var envName string
	var dryRun, all bool

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Write the environment's deployed instances into its manifest",
		Long: `Reads what the deployment graph has for this environment and declares it.

This is the migration path from the Python CLI. An environment brought up by
` + "`hmd neuronsphere up`" + ` gets its workloads from installed plugin packages, which
nsctl does not read -- so those instances show as "undeclared" until they are
written into a manifest. This writes them.

Substrate instances are skipped: nsctl deploys those whether or not a manifest
names them, and declaring one would make it removable by deleting a line.

Instances the manifest already declares are left exactly as they are, so
running this twice changes nothing the second time and a hand-edited
declaration is never overwritten.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, home, slug, err := resolveEnvSlug(opts, envName)
			if err != nil {
				return err
			}
			if _, err := reg.Environment(slug, opts.Lookup); err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}

			url := environment.MSDeploymentURL(opts.Lookup)
			client := msdeploy.New(url)
			if !client.Reachable(cmd.Context()) {
				return nserr.New(nserr.Fail,
					"hmd-ms-deployment is not answering at %s, so there is nothing to read. Start the environment first.", url)
			}
			deployed, err := client.EnvironmentInstances(cmd.Context(), slug)
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}

			m, err := openManifest(opts, home, slug)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			var added []manifest.Repo
			var skipped, unresolved []string
			for _, instance := range deployed {
				switch {
				case manifest.Reserved(instance.Name):
					continue
				case !all && instance.Status != msdeploy.StatusDeployed:
					skipped = append(skipped, fmt.Sprintf("%s (%s)", instance.Name, statusWord(instance.Status)))
					continue
				case instance.RepoClassName == "":
					// Without a repo class there is nothing to deploy from, so
					// a declaration would be one `env apply` refuses.
					unresolved = append(unresolved, instance.Name)
					continue
				}
				if _, already := m.Repo(instance.Name); already {
					continue
				}
				added = append(added, declarationFor(instance))
			}

			if len(unresolved) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: no repo class in the graph for %s; not declared\n", strings.Join(unresolved, ", "))
			}
			for _, s := range skipped {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: skipping %s; pass --all to declare it anyway\n", s)
			}
			if len(added) == 0 {
				fmt.Fprintf(out, "Nothing to import: %s already declares everything deployed in %s.\n", m.Path, slug)
				return nil
			}

			candidate := *m
			candidate.Repos = append(append([]manifest.Repo{}, m.Repos...), added...)
			// Validated before writing, so an import that `env apply` would
			// refuse is reported here instead of landing in the file.
			if problems := candidate.Validate(opts.Lookup); len(problems) > 0 {
				return nserr.New(nserr.Usage,
					"the imported manifest would not be valid:\n  - %s", strings.Join(problems, "\n  - "))
			}

			for _, r := range added {
				fmt.Fprintf(out, "  %s (%s@%s)\n", r.InstanceName, r.RepoClassName, orDash(r.Version))
			}
			if dryRun {
				fmt.Fprintf(out, "\n%d instance(s) would be declared in %s. Nothing was written.\n", len(added), m.Path)
				return nil
			}

			m.Repos = candidate.Repos
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(out, "\nDeclared %d instance(s) in %s\n", len(added), m.Path)
			fmt.Fprintf(out, "`nsctl env apply %s` should now report nothing to deploy.\n", slug)
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to import from (default: the default environment)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be declared without writing")
	cmd.Flags().BoolVar(&all, "all", false, "Import instances that are not currently deployed too")
	return cmd
}

// declarationFor turns a graph instance into a manifest declaration.
//
// A role with a single target is written as a bare string rather than a
// one-element list: both are valid, and the string is how a person writes it.
func declarationFor(instance msdeploy.DeployedInstance) manifest.Repo {
	r := manifest.Repo{
		InstanceName:          instance.Name,
		RepoClassName:         instance.RepoClassName,
		Version:               instance.RepoClassVersion,
		InstanceConfiguration: instance.InstanceConfiguration,
	}
	if len(instance.Dependencies) > 0 {
		deps := make(map[string]any, len(instance.Dependencies))
		for role, targets := range instance.Dependencies {
			if len(targets) == 1 {
				deps[role] = targets[0]
				continue
			}
			list := make([]any, 0, len(targets))
			for _, t := range targets {
				list = append(list, t)
			}
			deps[role] = list
		}
		r.Dependencies = deps
	}
	return r
}

func statusWord(status string) string {
	if status == msdeploy.StatusNeverDeployed {
		return "never deployed here"
	}
	return strings.ToLower(status)
}
