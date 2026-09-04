package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
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
deleted line leaves behind.`,
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

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "INSTANCE\tREPO CLASS\tVERSION\tSOURCE\tDEPLOYED")
			for _, name := range manifest.ReservedNames() {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, "-", "-", "substrate", deployedCell(status, name))
			}
			if m != nil {
				declarations := append([]manifest.Repo{}, m.Repos...)
				sort.Slice(declarations, func(i, j int) bool {
					return declarations[i].InstanceName < declarations[j].InstanceName
				})
				for _, r := range declarations {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
						r.InstanceName, r.RepoClassName, orDash(r.Version),
						"manifest", deployedCell(status, r.InstanceName))
				}
			}
			// Deployed here but declared nowhere: what a removed line leaves
			// behind, and the only way to notice it.
			for _, name := range msdeploy.DeployedNames(status) {
				if manifest.Reserved(name) {
					continue
				}
				if m != nil {
					if _, declared := m.Repo(name); declared {
						continue
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, "-", "-", "undeclared", "yes")
			}
			return w.Flush()
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
