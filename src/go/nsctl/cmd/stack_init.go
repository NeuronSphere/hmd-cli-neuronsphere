package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/stack"
)

// referenceBOM is where --from-env keeps what it read, so CI can re-derive.
const referenceBOM = "meta-data/reference-bom.json"

func newStackInitCommand(opts *Options) *cobra.Command {
	var (
		path, description, fromEnv, fromBOM, envName string
		selectList, bundleLocal                      []string
		dryRun, diff, update, includeProvided        bool
	)
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Scaffold a stack RepoClass, or derive one from an environment",
		Long: `Write a stack RepoClass: meta-data/manifest.json with a no-op deploy and a
"local" section, meta-data/VERSION, and the CI workflow that builds and
publishes it (.github/workflows/stack.yml).

With --from-env <env> or --from-bom <file> (a "nsctl bom show --json" export),
the "local" section is derived: the walk starts at the --select instances and
follows their dependencies, classifying each instance reached. The substrate
becomes a bound role; an instance another stack declared becomes an external
role with a suggestion (unless --include-provided); everything else is
bundled as a companion pinned to its running version, each selected root in
its own profile. An instance deployed from a working tree has no published
artifact and is refused unless --bundle-local names it. Configuration is
copied from the environment manifest's declarations only, and values that
look tied to this machine are listed for review.

--dry-run prints the classification and writes nothing. --update rewrites the
"local" section and dependencies of an existing manifest (the refresh job);
--diff only reports whether they would change, exiting 1 when they would.
--from-env also writes meta-data/reference-bom.json so CI can re-derive.`,
		Example: `  nsctl stack init hmd-stack-analytics
  nsctl stack init hmd-stack-analytics --from-env local --select superset,trino,airflow --dry-run
  nsctl stack init hmd-stack-analytics --from-env local --select superset,trino,airflow
  nsctl stack init hmd-stack-analytics --from-bom meta-data/reference-bom.json --select trino --update`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !repoClassNameRe.MatchString(name) {
				return nserr.New(nserr.Usage, "%q is not a RepoClass name (lower-case, digits and dashes); use hmd-stack-<name>", name)
			}
			dir := path
			if dir == "" {
				dir = name
			}
			dir, err := filepath.Abs(dir)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			manifestPath := filepath.Join(dir, "meta-data", "manifest.json")
			existing, err := os.ReadFile(manifestPath)
			exists := err == nil
			if exists && !update && !diff && !dryRun {
				return nserr.New(nserr.Usage, "%s already exists; pass --update to rewrite its local section, or --diff to compare", manifestPath)
			}
			if fromEnv == "" && fromBOM == "" {
				if update || diff || dryRun {
					return nserr.New(nserr.Usage, "--update, --diff and --dry-run derive; pass --from-env or --from-bom")
				}
				return scaffoldStack(cmd, dir, name, description, nil)
			}
			if fromEnv != "" && fromBOM != "" {
				return nserr.New(nserr.Usage, "--from-env and --from-bom are alternatives")
			}
			if len(selectList) == 0 {
				return nserr.Wrap(nserr.Usage, stack.ErrNoRoots)
			}

			graph, envManifest, bomJSON, err := loadGraph(cmd, opts, fromEnv, fromBOM, envName)
			if err != nil {
				return err
			}
			resolver := repoclass.NewWithHome(opts.Lookup("HMD_REPO_HOME"), opts.Home, opts.Lookup)
			if envManifest != nil {
				repoclass.Seed(resolver, envManifest.Repos)
			}
			d, err := stack.Derive(graph, stack.Options{
				Roots: splitList(selectList), BundleLocal: splitList(bundleLocal), IncludeProvided: includeProvided,
				ResourceOf: func(class string) string { return stack.FirstProduced(resolver, class) },
				Bundled:    isBundledClass,
			})
			if d != nil {
				renderRows(cmd, d)
			}
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "Dry run: nothing written.")
				return nil
			}
			if diff {
				if !exists {
					return nserr.New(nserr.Usage, "--diff needs an existing %s", manifestPath)
				}
				differs, why, err := d.LocalDiffers(existing)
				if err != nil {
					return nserr.Wrap(nserr.Usage, err)
				}
				if !differs {
					fmt.Fprintln(cmd.OutOrStdout(), "The checked-in local section matches the derivation.")
					return nil
				}
				return nserr.New(nserr.Fail, "the derivation differs from %s:\n%s", manifestPath, why)
			}
			if exists {
				differs, _, err := d.LocalDiffers(existing)
				if err != nil {
					return nserr.Wrap(nserr.Usage, err)
				}
				if !differs {
					fmt.Fprintf(cmd.OutOrStdout(), "%s is already up to date.\n", manifestPath)
					return nil
				}
				if err := rewriteLocal(manifestPath, existing, d); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Rewrote the local section and dependencies of %s\n", manifestPath)
			} else {
				if err := scaffoldStack(cmd, dir, name, description, d); err != nil {
					return err
				}
			}
			// A bundled working tree is zipped into the artifact cache now, from
			// this tree, so `stack build` finds it at its second tier and the
			// lock can carry its digest.
			if err := bundleLocalTrees(cmd, opts, envManifest, d, splitList(bundleLocal)); err != nil {
				return err
			}
			if bomJSON != nil {
				if err := writeFileAt(filepath.Join(dir, referenceBOM), bomJSON); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (the reference environment CI re-derives from)\n", filepath.Join(dir, referenceBOM))
			}
			// The lock, from the derived exact pins: no network.
			spec, err := localspec.Load(dir)
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			return runLockWrite(cmd, opts, dir, spec, "", nil, nil)
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "directory to write into (default: ./<name>)")
	cmd.Flags().StringVar(&description, "description", "", "manifest description")
	cmd.Flags().StringVar(&fromEnv, "from-env", "", "derive from a running environment's graph (writes the reference BOM)")
	cmd.Flags().StringVar(&fromBOM, "from-bom", "", "derive from a BOM export (nsctl bom show --json)")
	cmd.Flags().StringVar(&envName, "env", "", "with --from-bom, the environment whose manifest supplies sources, configuration and stack records")
	cmd.Flags().StringSliceVar(&selectList, "select", nil, "instances to derive from; their dependencies follow. Repeatable, or comma-separated")
	cmd.Flags().StringSliceVar(&bundleLocal, "bundle-local", nil, "working-tree instances to bundle rather than refuse")
	cmd.Flags().BoolVar(&includeProvided, "include-provided", false, "bundle instances other stacks declared rather than referencing those stacks")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the classification and write nothing")
	cmd.Flags().BoolVar(&diff, "diff", false, "report whether an existing manifest's local section would change; exit 1 when it would")
	cmd.Flags().BoolVar(&update, "update", false, "rewrite an existing manifest's local section and dependencies")
	return cmd
}

// loadGraph reads the environment a derivation walks: the live graph (and
// the reference BOM to write) or a BOM export, plus the environment
// manifest when one can be found.
func loadGraph(cmd *cobra.Command, opts *Options, fromEnv, fromBOM, envName string) (stack.Graph, *manifest.Manifest, []byte, error) {
	var envManifest *manifest.Manifest
	loadManifest := func(name string) {
		if opts.Home == "" {
			return
		}
		if _, _, slug, err := resolveEnvSlug(opts, name); err == nil {
			if m, err := manifest.Load(opts.Home, slug, opts.Lookup); err == nil {
				envManifest = m
			}
		}
	}
	if fromBOM != "" {
		data, err := os.ReadFile(fromBOM)
		if err != nil {
			return stack.Graph{}, nil, nil, nserr.Wrap(nserr.Usage, err)
		}
		var entries []msdeploy.BOMEntry
		if err := json.Unmarshal(data, &entries); err != nil {
			return stack.Graph{}, nil, nil, nserr.New(nserr.Usage, "%s is not a BOM export (a JSON list as `nsctl bom show --json` writes): %v", fromBOM, err)
		}
		if envName != "" {
			loadManifest(envName)
		}
		return stack.GraphFromBOM(entries, envManifest), envManifest, nil, nil
	}
	if _, err := opts.RequireHome(); err != nil {
		return stack.Graph{}, nil, nil, err
	}
	_, _, slug, err := resolveEnvSlug(opts, fromEnv)
	if err != nil {
		return stack.Graph{}, nil, nil, err
	}
	url := environment.MSDeploymentURL(opts.Lookup)
	client := msdeploy.New(url)
	if !client.Reachable(cmd.Context()) {
		return stack.Graph{}, nil, nil, nserr.New(nserr.Fail,
			"hmd-ms-deployment is not answering at %s. Start the environment first with `nsctl env start %s`, or derive from an export with --from-bom", url, slug)
	}
	instances, err := client.EnvironmentInstances(cmd.Context(), slug)
	if err != nil {
		return stack.Graph{}, nil, nil, nserr.Wrap(nserr.Fail, err)
	}
	loadManifest(fromEnv)
	entries := stack.BOMFromInstances(instances)
	bomJSON, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return stack.Graph{}, nil, nil, err
	}
	return stack.GraphFromBOM(entries, envManifest), envManifest, bomJSON, nil
}

// renderRows prints SPEC004's classification table.
func renderRows(cmd *cobra.Command, d *stack.Derivation) {
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "INSTANCE\tCLASS\tVERSION\tBECOMES\tVIA")
	for _, r := range d.Rows {
		becomes := string(r.Kind)
		switch r.Kind {
		case stack.KindRoot:
			becomes = "companion (profile " + r.Instance + ")"
		case stack.KindSubstrate:
			becomes = "bound role (the environment provides it)"
		case stack.KindBundled:
			becomes = "bound role (nsctl bundles it)"
		case stack.KindCrossStack:
			becomes = "external role (stack " + r.Stack + ")"
		case stack.KindLocal:
			becomes = "REFUSED: working tree, no published artifact"
		}
		via := ""
		if r.Via != "" {
			via = r.Via + "." + r.Role
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Instance, r.Class, r.Version, becomes, via)
	}
	_ = tw.Flush()
	for _, r := range d.Rows {
		if len(r.HostSpecific) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "review: %s copies instance_configuration %s, which looks tied to this machine\n",
				r.Instance, strings.Join(r.HostSpecific, ", "))
		}
	}
}

// scaffoldStack writes a stack RepoClass: manifest (bare or derived),
// VERSION and the CI workflow.
func scaffoldStack(cmd *cobra.Command, dir, name, description string, d *stack.Derivation) error {
	if description == "" {
		description = "A NeuronSphere stack"
	}
	var manifestJSON []byte
	var err error
	if d != nil {
		manifestJSON, err = d.ManifestJSON(name, description)
	} else {
		manifestJSON, err = json.MarshalIndent(map[string]any{
			"name":        name,
			"description": description,
			"build":       map[string]any{},
			"deploy":      map[string]any{"commands": []any{[]any{"exec", "true"}}},
			"local":       map[string]any{"version": 1, "default_profiles": []any{}, "repos": []any{}},
		}, "", "  ")
	}
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	files := map[string][]byte{
		filepath.Join("meta-data", "manifest.json"): append(manifestJSON, '\n'),
		filepath.Join("meta-data", "VERSION"):       []byte("0.1\n"),
		filepath.Join(".github", "workflows", "stack.yml"): []byte(strings.ReplaceAll(stack.WorkflowTemplate, "{{.Name}}",
			strings.TrimPrefix(name, "hmd-stack-"))),
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := writeFileAt(filepath.Join(dir, p), files[p]); err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", filepath.Join(dir, p))
	}
	if d == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Next: add companions (nsctl repoclass local add ...) or derive them (nsctl stack init %s --from-env <env> --select ... --update), then nsctl lock\n", name)
	}
	return nil
}

// rewriteLocal replaces the local section and deploy.dependencies of an
// existing manifest, preserving everything else it carries.
func rewriteLocal(path string, existing []byte, d *stack.Derivation) error {
	var doc map[string]any
	if err := json.Unmarshal(existing, &doc); err != nil {
		return err
	}
	doc["local"] = d.Local
	if _, ok := doc["build"]; !ok {
		doc["build"] = map[string]any{}
	}
	deploy, _ := doc["deploy"].(map[string]any)
	if deploy == nil {
		deploy = map[string]any{"commands": []any{[]any{"exec", "true"}}}
	}
	if len(d.Dependencies) > 0 {
		deploy["dependencies"] = d.Dependencies
	} else {
		delete(deploy, "dependencies")
	}
	doc["deploy"] = deploy
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAt(path, append(out, '\n'))
}

func writeFileAt(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

var repoClassNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// bundleLocalTrees stores each --bundle-local instance's working tree in the
// artifact cache under the version the derivation pinned.
func bundleLocalTrees(cmd *cobra.Command, opts *Options, envManifest *manifest.Manifest, d *stack.Derivation, names []string) error {
	if len(names) == 0 {
		return nil
	}
	if opts.Home == "" {
		return nserr.New(nserr.Usage, "--bundle-local zips a working tree into the artifact cache, which needs an HMD_HOME")
	}
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	for _, r := range d.Rows {
		if !wanted[r.Instance] {
			continue
		}
		var repo manifest.Repo
		if envManifest != nil {
			repo, _ = envManifest.Repo(r.Instance)
		}
		path := repo.RepoPath(opts.Lookup)
		if path == "" {
			return nserr.New(nserr.Usage, "--bundle-local %s: the environment manifest names no working tree for it", r.Instance)
		}
		data, err := artifact.Zip(path)
		if err != nil {
			return nserr.Wrap(nserr.Fail, fmt.Errorf("bundling %s from %s: %w", r.Instance, path, err))
		}
		if err := artifact.Invalidate(opts.Home, r.Class, r.Version); err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		if _, err := artifact.Store(opts.Home, r.Class, r.Version, data); err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Bundled %s (%s@%s) from %s into the artifact cache\n", r.Instance, r.Class, r.Version, path)
	}
	return nil
}

// isBundledClass reports a class nsctl ships in every environment.
func isBundledClass(class string) bool {
	for _, c := range bundled.RepoClasses() {
		if c == class {
			return true
		}
	}
	return false
}
