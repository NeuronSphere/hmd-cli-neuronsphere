package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/stack"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versions"
)

// newStackCommand installs published sets of RepoClasses. NERD017.
func newStackCommand(opts *Options) *cobra.Command {
	group := &cobra.Command{
		Use:   "stack",
		Short: "Add, list, remove and publish stacks of RepoClasses",
		Long: `A stack is a RepoClass whose repository declares the companions it needs (a
"local" section and a neuronsphere.lock) published as one artifact in an OCI
registry, holding the build zip of every RepoClass the lock pins. "stack add"
fetches it -- anonymously from a public namespace, no tenant needed -- unpacks
every zip into the artifact cache, and declares the instances in an
environment manifest the way "env add --from-repo" would from a checkout.

Declaring is not deploying: run "nsctl env apply" afterwards, or pass --apply.
nsctl carries no list of stacks; a bare name expands to
` + stack.DefaultNamespace + `/<name> and the expansion is printed.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(
		newStackAddCommand(opts),
		newStackPullCommand(opts),
		newStackListCommand(opts),
		newStackRemoveCommand(opts),
		newStackVersionsCommand(opts),
		newStackBuildCommand(opts),
		newStackPushCommand(opts),
	)
	return group
}

// stackRef resolves what the user typed to a versioned reference and a
// client for its host, printing the expansion and the chosen version.
func stackRef(cmd *cobra.Command, opts *Options, typed, spec string) (oci.Ref, *oci.Client, error) {
	ref, expanded, err := expandRef(typed, stack.DefaultNamespace)
	if err != nil {
		return oci.Ref{}, nil, err
	}
	reportRef(cmd.OutOrStdout(), typed, ref, expanded)
	client := oci.New(registryCredential(opts, ref.Host, ""))
	ref, err = resolveVersion(cmd.Context(), cmd.OutOrStdout(), client, ref, spec)
	if err != nil {
		return oci.Ref{}, nil, err
	}
	return ref, client, nil
}

// installStack is SPEC003 steps 1-4: fetch, verify, cache, and offer the zips
// to the local librarian.
func installStack(cmd *cobra.Command, opts *Options, home string, ref oci.Ref, client *oci.Client, localURL string) (*stack.Installed, error) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Fetching %s (credential: %s)\n", ref, client.Credential.Source)
	inst, err := stack.Install(cmd.Context(), home, client, ref, func(line string) { fmt.Fprintln(out, line) })
	if err != nil {
		return nil, classifyRegistryError(err)
	}
	// Best effort: the cache is what resolution reads (NERD005), and the
	// librarian copy only keeps `artifact unpack` and in-container
	// pre_build_artifacts working. A control plane that is down is a warning.
	local := librarian.NewLocal(localURL)
	var failed []string
	for _, layer := range inst.Stack.Layers {
		spec := librarian.Spec{Name: layer.Class, Version: layer.Version, ItemType: stack.ItemType}
		if err := local.Put(cmd.Context(), spec.ContentPath(), spec.ItemType, inst.Zips[layer.Class]); err != nil {
			failed = append(failed, spec.String())
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: the control plane's Artifact Librarian at %s did not accept %d artifact(s); the cache holds them and "+
				"deploys will work, but `nsctl artifact unpack` will not until the control plane is up and they are re-added\n",
			localURL, len(failed))
	}
	if len(inst.Stack.ImageRegistries) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"note: this stack's images live outside the default registries. Add to $HMD_HOME/.config/hmd.env:\n"+
				"  HMD_LOCAL_IMAGE_PULL_REGISTRIES=%s\n", strings.Join(inst.Stack.ImageRegistries, ","))
	}
	return inst, nil
}

func newStackAddCommand(opts *Options) *cobra.Command {
	var (
		envName, localURL, spec string
		apply, verbose          bool
		repo                    fromRepo
	)
	cmd := &cobra.Command{
		Use:   "add <ref>",
		Short: "Fetch a stack and declare its instances in an environment",
		Long: `Fetch a stack from an OCI registry, verify and cache every RepoClass it
pins, and declare them -- and the stack itself -- in the environment manifest
with the same rules "env add --from-repo" applies to a checkout: --profile,
--all-profiles, --lean and --name mean what they mean there. The stack keeps
its own bindings in the manifest's "stacks" record, so it can share an
environment with a --from-repo repository or another stack.

<ref> is <host>/<repository>[:<version>]; a bare name expands to
` + stack.DefaultNamespace + `/<name>. No version means the newest, which is
printed. A public namespace needs no credential.

Nothing is deployed until "nsctl env apply <env>"; --apply runs it.`,
		Example: `  nsctl stack add observability
  nsctl stack add observability --env dev --apply
  nsctl stack add ghcr.io/acme/stacks/warehouse:0.4.0 --profile full --name warehouse-db=db`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			ref, client, err := stackRef(cmd, opts, args[0], spec)
			if err != nil {
				return err
			}
			_, _, slug, err := resolveEnvSlug(opts, envName)
			if err != nil {
				return err
			}
			m, err := openManifest(opts, home, slug)
			if err != nil {
				return err
			}

			inst, err := installStack(cmd, opts, home, ref, client, localURL)
			if err != nil {
				return err
			}
			s := inst.Stack
			record, _, known := m.Stack(s.Name)
			if known && record.Version == s.Version && record.Digest == s.Digest.String() {
				fmt.Fprintf(cmd.OutOrStdout(), "Stack %s %s is already declared in %s\n", s.Name, s.Version, slug)
			}

			// Plan through the --from-repo rules over the cached stack tree,
			// with the subject declared from its artifact.
			repo.path = inst.Dir
			repo.subject = &manifest.Source{Type: manifest.SourceArtifact}
			repo.subjectVersion = s.Version
			repo.recorded = &recordedPlan{Bindings: record.Bindings, Profiles: record.Profiles}
			repo.compose = composeAgainst(opts, home, m, record)
			plan, err := planFromRepo(&repo, m)
			if err != nil {
				return err
			}
			if err := checkNamesAreUsed(&repo, plan); err != nil {
				return err
			}
			if missing := plan.missingArtifacts(home); len(missing) > 0 {
				// Every layer was just stored; anything missing is a lock entry
				// the artifact did not carry, which Read already refused.
				return uncachedError(opts, home, missing, "`nsctl stack pull`")
			}
			bindings := record.Bindings
			added, kept := plan.applyInto(m, false, &bindings)
			declared := append([]string(nil), added...)
			sort.Strings(declared)
			m.SetStack(manifest.StackRecord{
				Name: s.Name, Version: s.Version, Ref: ref.WithTag("").String(), Digest: s.Digest.String(),
				Profiles: plan.Profiles, Bindings: bindings, Declared: declared,
			})
			if problems := m.Validate(opts.Lookup); len(problems) > 0 {
				return nserr.New(nserr.Usage, "the environment this stack describes is not valid:\n  - %s",
					strings.Join(problems, "\n  - "))
			}
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}

			plan.render(cmd, slug)
			if len(plan.Composed) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Shared with the environment: %s\n", stack.DescribeBinds(plan.Composed))
			}
			if len(kept) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "note: %s %s no longer asked for by this stack, left declared\n",
					strings.Join(kept, ", "), plural(len(kept), "is", "are"))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Declared stack %s %s in %s (%d instance(s))\n",
				s.Name, s.Version, m.Path, len(added))
			if !apply {
				fmt.Fprintf(cmd.OutOrStdout(), "Run: nsctl env apply %s\n", slug)
				return nil
			}
			return environment.Apply(cmd.Context(), &environment.Options{
				Home: home, Lookup: opts.Lookup, Verbose: verbose,
				Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(),
			}, slug)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to declare it in (default: the default environment)")
	cmd.Flags().StringVar(&spec, "spec", "", "a BACON version spec to choose the version by (e.g. \"~= 0.1\")")
	cmd.Flags().BoolVar(&apply, "apply", false, "Run `nsctl env apply` afterwards")
	cmd.Flags().BoolVarP(&verbose, "verbose", "V", false, "With --apply, show the underlying command output")
	cmd.Flags().StringVar(&localURL, "local-url", librarian.LocalBaseURL, "the control plane's Artifact Librarian")
	cmd.Flags().StringSliceVar(&repo.profiles, "profile", nil, "Local profiles to activate. Repeatable, or comma-separated")
	cmd.Flags().BoolVar(&repo.allProfiles, "all-profiles", false, "Activate every profile the stack's lock mentions")
	cmd.Flags().BoolVar(&repo.lean, "lean", false, "Activate no profiles: the stack and its unconditional entries alone")
	cmd.Flags().StringArrayVar(&repo.names, "name", nil, "Name one instance, as <role-or-declared-name>=<instance>. Repeatable")
	return cmd
}

// composeAgainst is NERD017 SPEC010 wired for one environment: index what
// it provides, and let the planner bind rather than declare.
func composeAgainst(opts *Options, home string, m *manifest.Manifest, record manifest.StackRecord) func(
	*localspec.Manifest, []localspec.Want, *lock.Lock, map[string]string) (map[string]string, error) {

	return func(_ *localspec.Manifest, wants []localspec.Want, l *lock.Lock, overrides map[string]string) (map[string]string, error) {
		resolver := repoclass.NewWithHome(opts.Lookup("HMD_REPO_HOME"), home, opts.Lookup)
		repoclass.Seed(resolver, m.Repos)
		providers := stack.IndexProviders(m, resolver)
		owned := map[string]bool{}
		for _, instance := range record.Bindings {
			owned[instance] = true
		}
		pinned := func(class string) bool { _, ok := l.Entry(class); return ok }
		binds, unsatisfied, conflicts := stack.Compose(wants, providers, owned, pinned, overrides)
		if len(unsatisfied) > 0 {
			lines := make([]string, 0, len(unsatisfied))
			for _, u := range unsatisfied {
				lines = append(lines, u.Error())
			}
			return nil, nserr.New(nserr.Usage, "this stack needs something the environment does not have:\n  - %s", strings.Join(lines, "\n  - "))
		}
		if len(conflicts) > 0 {
			return nil, nserr.New(nserr.Usage, "this stack's instance names collide with the environment's:\n  - %s", strings.Join(conflicts, "\n  - "))
		}
		return binds, nil
	}
}

func newStackPullCommand(opts *Options) *cobra.Command {
	var localURL, spec string
	cmd := &cobra.Command{
		Use:   "pull <ref>",
		Short: "Fetch a stack into the artifact cache without declaring it",
		Long: `The fetch half of "stack add": verify and cache every RepoClass the stack
pins, and nothing else. For preparing a machine that will be offline, or a CI
job warming a cache.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			ref, client, err := stackRef(cmd, opts, args[0], spec)
			if err != nil {
				return err
			}
			inst, err := installStack(cmd, opts, home, ref, client, localURL)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Cached stack %s %s (%s): %d artifact(s) under %s\n",
				inst.Stack.Name, inst.Stack.Version, inst.Stack.Digest, len(inst.Stack.Layers), artifact.Root(home))
			return nil
		},
	}
	cmd.Flags().StringVar(&spec, "spec", "", "a BACON version spec to choose the version by")
	cmd.Flags().StringVar(&localURL, "local-url", librarian.LocalBaseURL, "the control plane's Artifact Librarian")
	return cmd
}

func newStackListCommand(opts *Options) *cobra.Command {
	var envName string
	var asJSON bool
	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List the stacks declared in an environment",
		Long:          `Read the "stacks" records of the environment manifest: name, version, reference, digest and the instances each bound.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			_, _, slug, err := resolveEnvSlug(opts, envName)
			if err != nil {
				return err
			}
			m, err := openManifest(opts, home, slug)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				records := m.Stacks
				if records == nil {
					records = []manifest.StackRecord{}
				}
				return enc.Encode(records)
			}
			if len(m.Stacks) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No stacks declared in %s\n", m.Path)
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tVERSION\tINSTANCES\tFROM")
			for _, s := range m.Stacks {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.Version, strings.Join(s.Owned(), ","), s.Ref)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to list (default: the default environment)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	return cmd
}

func newStackRemoveCommand(opts *Options) *cobra.Command {
	var envName string
	var pruneCache bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Undeclare a stack's instances and drop its record",
		Long: `Remove the instances a stack bound -- and only those; an instance provided
by the substrate, or declared by another stack or by hand, is not the stack's
to remove -- and delete its record from the environment manifest. Nothing is
torn down: the next "nsctl env apply" reconciles. The artifact cache is kept
unless --prune-cache.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			_, _, slug, err := resolveEnvSlug(opts, envName)
			if err != nil {
				return err
			}
			m, err := openManifest(opts, home, slug)
			if err != nil {
				return err
			}
			record, _, ok := m.Stack(args[0])
			if !ok {
				return nserr.New(nserr.Usage, "no stack %q is declared in %s", args[0], m.Path)
			}
			// Instances other records or the manifest's own bindings also
			// claim are not this stack's alone.
			shared := map[string]bool{}
			for _, other := range m.Stacks {
				if other.Name == record.Name {
					continue
				}
				for _, in := range other.Bindings {
					shared[in] = true
				}
			}
			for _, in := range m.Bindings {
				shared[in] = true
			}
			owned := map[string]bool{}
			for _, in := range record.Owned() {
				if !shared[in] && !manifest.ScopeEnvironment.Reserved(in) {
					owned[in] = true
				}
			}
			var removed []string
			kept := m.Repos[:0]
			for _, r := range m.Repos {
				if owned[r.InstanceName] {
					removed = append(removed, r.InstanceName)
					continue
				}
				kept = append(kept, r)
			}
			m.Repos = kept
			m.RemoveStack(record.Name)
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			sort.Strings(removed)
			fmt.Fprintf(cmd.OutOrStdout(), "Removed stack %s from %s; undeclared: %s\nRun: nsctl env apply %s\n",
				record.Name, m.Path, strings.Join(removed, ", "), slug)
			if pruneCache {
				// The record knows the stack's version; its companions' versions
				// are in the cached lock, which is under the stack's own tree.
				for class, version := range cachedStackVersions(home, record) {
					if err := artifact.Invalidate(home, class, version); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to edit (default: the default environment)")
	cmd.Flags().BoolVar(&pruneCache, "prune-cache", false, "Also delete the stack's artifacts from the cache")
	return cmd
}

// cachedStackVersions lists what a stack put in the cache, from the lock in
// its cached tree; the stack's own class from the record.
func cachedStackVersions(home string, record manifest.StackRecord) map[string]string {
	out := map[string]string{}
	for key := range record.Bindings {
		// Bindings are keyed by role or class; the subject's key is its class,
		// and its tree holds the lock naming the rest.
		if artifact.Cached(home, key, record.Version) {
			out[key] = record.Version
			if l, err := readLockAt(artifact.Dir(home, key, record.Version)); err == nil {
				for _, e := range l.Resolved {
					out[e.RepoClassName] = e.Version
				}
			}
		}
	}
	return out
}

func newStackVersionsCommand(opts *Options) *cobra.Command {
	var spec string
	var offline bool
	cmd := &cobra.Command{
		Use:   "versions <ref>",
		Short: "List a stack's published versions",
		Long: `List the version-shaped tags a registry holds for a stack, newest first, and
with --spec the one a BACON version spec would choose. Results are cached
under $HMD_HOME/.cache/neuronsphere/versions/; --offline reads the cache only.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			ref, expanded, err := expandRef(args[0], stack.DefaultNamespace)
			if err != nil {
				return err
			}
			reportRef(cmd.OutOrStdout(), args[0], ref, expanded)
			key := stack.VersionsKey(ref)
			var tags []string
			if offline {
				p, err := versions.Load(home, key)
				if err != nil {
					return nserr.Wrap(nserr.Usage, fmt.Errorf("no cached versions for %s: %w", ref, err))
				}
				tags = p.Versions(stack.ItemType)
				fmt.Fprintf(cmd.OutOrStdout(), "From cache (%s old)\n", p.Age(time.Now()).Round(time.Second))
			} else {
				client := oci.New(registryCredential(opts, ref.Host, ""))
				tags, err = client.Tags(cmd.Context(), ref)
				if err != nil {
					return classifyRegistryError(err)
				}
				p := &versions.Published{RepoClass: key, QueriedAt: time.Now()}
				for _, t := range tags {
					p.Items = append(p.Items, versions.Item{Version: t, ItemType: stack.ItemType})
				}
				if err := versions.Save(home, p); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not cache versions: %v\n", err)
				}
			}
			if len(tags) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s has no version-shaped tags\n", ref)
				return nil
			}
			for _, t := range tags {
				fmt.Fprintln(cmd.OutOrStdout(), t)
			}
			if spec != "" {
				chosen, err := resolveVersion(cmd.Context(), cmd.OutOrStdout(), staticTags(tags), ref, spec)
				if err != nil {
					return err
				}
				_ = chosen
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&spec, "spec", "", "report which version a BACON version spec would choose")
	cmd.Flags().BoolVar(&offline, "offline", false, "read the cache only")
	return cmd
}

// stackSources wires the four zip tiers for a build or a laptop push.
func stackSources(cmd *cobra.Command, opts *Options, libs *librarians, artifactsDir string) zipSources {
	return zipSources{
		artifactsDir: artifactsDir,
		home:         opts.Home,
		registry:     func(host string) *oci.Client { return oci.New(registryCredential(opts, host, "")) },
		cloud:        func() (*librarian.Client, error) { return libs.cloud(cmd, opts) },
		report:       func(line string) { fmt.Fprintln(cmd.OutOrStdout(), line) },
	}
}

func newStackBuildCommand(opts *Options) *cobra.Command {
	var (
		out, artifactsDir, tag string
		libs                   librarians
	)
	cmd := &cobra.Command{
		Use:   "build [<repo-dir>]",
		Short: "Build the stack artifact into an OCI image layout, offline",
		Long: `Read the repository's "local" section and neuronsphere.lock, obtain every
pinned build zip, and write the stack artifact as an OCI image layout under
--out (default build/stack). The same lock and bytes produce the same layout,
so a build in CI reproduces a build on a laptop, and nothing is pushed.

Each pinned zip comes from the first of: --artifacts <dir>; the artifact
cache (what an environment this stack was derived from deployed from); the
lock entry's "source", an OCI reference published with "nsctl artifact push"
(no tenant needed); the cloud Artifact Librarian (paid, only with a
credential). The tier that served each entry is printed.`,
		Example: `  nsctl stack build
  nsctl stack build ~/src/hmd-stack-obs --out dist/stack --artifacts ./release`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoDir := "."
			if len(args) == 1 {
				repoDir = args[0]
			}
			// The layout is pushed to whatever reference push is given, so
			// the artifact carries no stack name; a consumer names it after
			// the reference it pulls from.
			ref := oci.Ref{Host: layoutHost, Repository: "stack"}
			m, blobs, _, tagged, err := buildStackFromRepo(cmd.Context(), repoDir, ref, tag, stackSources(cmd, opts, &libs, artifactsDir))
			if err != nil {
				return err
			}
			dir := out
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(repoDir, dir)
			}
			d, err := stack.WriteLayout(dir, m, blobs, tagged.Tag)
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Built stack %s (%d layer(s), %s) -> %s\nPush it with: nsctl stack push <ref> --from %s --bump\n",
				tagged.Tag, len(m.Layers), d, dir, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", stack.DefaultLayoutDir, "where to write the OCI image layout (relative to the repository)")
	cmd.Flags().StringVar(&artifactsDir, "artifacts", "", "directory holding <class>_<version>_build.zip for pinned companions")
	cmd.Flags().StringVar(&tag, "tag", "", "the stack version to record (default: meta-data/VERSION)")
	libs.bindCloud(cmd)
	libs.bindTenant(cmd)
	return cmd
}

func newStackPushCommand(opts *Options) *cobra.Command {
	var (
		token, artifactsDir, from, tag string
		updateLock, bump               bool
		libs                           librarians
	)
	cmd := &cobra.Command{
		Use:   "push [<repo-dir>] <ref> [--from build/stack] [--bump]",
		Short: "Publish a stack to an OCI registry",
		Long: `Push a stack artifact to an OCI registry. With --from, push the OCI image
layout "nsctl stack build" wrote -- the CI path: build, inspect, then push.
Without it, build from the repository first (the laptop path), taking pinned
zips from --artifacts, the artifact cache, each lock entry's "source", or the
cloud Artifact Librarian, in that order.

The tag is the reference's, or --tag, or with --bump the next patch of the
newest version the registry already holds (meta-data/VERSION.0 when it holds
none or VERSION is a newer major.minor). A tag the registry already has is
refused: a published version is immutable. A credential is required:
--token, HMD_REGISTRY_TOKEN (GITHUB_TOKEN works for ghcr.io within the
repository's owner), or a profile's registry_url after "nsctl login".`,
		Example: `  nsctl stack build && nsctl stack push ghcr.io/acme/stacks/obs --from build/stack --bump
  nsctl stack push . ghcr.io/hmdlabs/stacks/observability:0.1.0 --token $GHCR_PAT
  nsctl stack push ~/src/hmd-stack-obs ghcr.io/acme/stacks/obs --artifacts ./dist --update-lock`,
		Args:          cobra.RangeArgs(1, 2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoDir, refArg := ".", args[0]
			if len(args) == 2 {
				repoDir, refArg = args[0], args[1]
			}
			ref, err := oci.ParseRef(refArg)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if ref.Digest != "" {
				return nserr.New(nserr.Usage, "push needs a tag, not a digest: %s", ref)
			}
			if tag != "" {
				ref = ref.WithTag(tag)
			}
			cred := registryCredential(opts, ref.Host, token)
			if cred.Anonymous() {
				return nserr.Wrap(nserr.Usage, oci.ErrNoCredential)
			}
			client := oci.New(cred)

			// The version: --bump asks the registry, a bare reference asks
			// the layout or meta-data/VERSION.
			versionFile := func() string {
				v, _ := os.ReadFile(filepath.Join(repoDir, "meta-data", "VERSION"))
				return strings.TrimSpace(string(v))
			}
			if bump {
				if ref.Tag != "" {
					return nserr.New(nserr.Usage, "--bump chooses the tag; %s names one already", ref)
				}
				published, err := client.Tags(cmd.Context(), ref)
				if err != nil {
					var oe *oci.Error
					if !errors.As(err, &oe) || !oe.NotFound() {
						return classifyRegistryError(err)
					}
					published = nil // a repository that does not exist yet
				}
				base := versionFile()
				if from != "" {
					if _, _, layoutTag, err := stack.ReadLayout(from); err == nil && layoutTag != "" {
						base = layoutTag
					}
				}
				next, err := stack.NextVersion(published, base)
				if err != nil {
					return nserr.Wrap(nserr.Usage, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Version: %s (--bump; published: %s)\n", next, orNone(published))
				ref = ref.WithTag(next)
			}

			if from != "" {
				if ref.Tag == "" {
					_, _, layoutTag, err := stack.ReadLayout(from)
					if err != nil {
						return nserr.Wrap(nserr.Usage, err)
					}
					ref = ref.WithTag(layoutTag)
				}
				if err := refuseIfPublished(cmd, client, ref); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Pushing %s from %s with credential from %s\n", ref, from, cred.Source)
				d, err := stack.PushLayout(cmd.Context(), client, ref, from)
				if err != nil {
					return classifyRegistryError(err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Pushed %s (%s)\n", ref, d)
				return nil
			}

			m, blobs, pinned, tagged, err := buildStackFromRepo(cmd.Context(), repoDir, ref, "", stackSources(cmd, opts, &libs, artifactsDir))
			if err != nil {
				return err
			}
			if err := refuseIfPublished(cmd, client, tagged); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pushing %s (%d layer(s)) with credential from %s\n", tagged, len(m.Layers), cred.Source)
			d, err := stack.Push(cmd.Context(), client, tagged, m, blobs)
			if err != nil {
				return classifyRegistryError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pushed %s (%s)\n", tagged, d)
			if updateLock {
				if err := writeLockAt(repoDir, pinned); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Updated %s with digests\n", lockPath(repoDir))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "registry token or PAT (overrides "+oci.TokenEnv+")")
	cmd.Flags().StringVar(&from, "from", "", "push the OCI image layout `nsctl stack build` wrote at this path")
	cmd.Flags().BoolVar(&bump, "bump", false, "tag with the next patch of the newest published version")
	cmd.Flags().StringVar(&tag, "tag", "", "tag to publish under")
	cmd.Flags().StringVar(&artifactsDir, "artifacts", "", "directory holding <class>_<version>_build.zip for every pinned companion")
	cmd.Flags().BoolVar(&updateLock, "update-lock", false, "write the zips' digests back into the repository's lock")
	libs.bindCloud(cmd)
	libs.bindTenant(cmd)
	return cmd
}

// refuseIfPublished is SPEC003's immutability rule.
func refuseIfPublished(cmd *cobra.Command, client *oci.Client, ref oci.Ref) error {
	tags, err := client.AllTags(cmd.Context(), ref)
	if err != nil {
		var oe *oci.Error
		if errors.As(err, &oe) && oe.NotFound() {
			return nil
		}
		return classifyRegistryError(err)
	}
	for _, t := range tags {
		if t == ref.Tag {
			return nserr.New(nserr.Usage, "%s is already published; a published version is immutable. Use --bump or a new tag", ref)
		}
	}
	return nil
}

func orNone(tags []string) string {
	if len(tags) == 0 {
		return "none"
	}
	if len(tags) > 5 {
		return strings.Join(tags[:5], ", ") + ", ..."
	}
	return strings.Join(tags, ", ")
}
