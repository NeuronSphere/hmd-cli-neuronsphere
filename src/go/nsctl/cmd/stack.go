package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
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
			m.SetStack(manifest.StackRecord{
				Name: s.Name, Version: s.Version, Ref: ref.WithTag("").String(), Digest: s.Digest.String(),
				Profiles: plan.Profiles, Bindings: bindings,
			})
			if problems := m.Validate(opts.Lookup); len(problems) > 0 {
				return nserr.New(nserr.Usage, "the environment this stack describes is not valid:\n  - %s",
					strings.Join(problems, "\n  - "))
			}
			if err := m.Save(m.Path); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}

			plan.render(cmd, slug)
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
				instances := make([]string, 0, len(s.Bindings))
				for _, in := range s.Bindings {
					instances = append(instances, in)
				}
				sort.Strings(instances)
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.Version, strings.Join(instances, ","), s.Ref)
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
			for _, in := range record.Bindings {
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

func newStackPushCommand(opts *Options) *cobra.Command {
	var (
		token, artifactsDir string
		updateLock          bool
		libs                librarians
	)
	cmd := &cobra.Command{
		Use:   "push [<repo-dir>] <ref>",
		Short: "Publish a stack from a repository with a lock",
		Long: `Build the stack artifact from a repository that has a "local" section and a
neuronsphere.lock, and push it to an OCI registry. The stack's own zip is the
repository tree; each pinned companion's zip comes from --artifacts <dir>
(the "hmd build" output layout, <class>_<version>_build.zip) or else from the
cloud Artifact Librarian by the lock's content path -- the publisher is a paid
user; the consumer is not. Every zip's digest is written into the lock inside
the artifact; --update-lock writes them into the repository's lock too.

The tag is meta-data/VERSION unless <ref> names one. A credential is
required: --token, HMD_REGISTRY_TOKEN, or a profile whose registry_url
matches the host after "nsctl login".`,
		Example: `  nsctl stack push . ghcr.io/hmdlabs/stacks/observability:0.1.0 --token $GHCR_PAT
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
			cred := registryCredential(opts, ref.Host, token)
			if cred.Anonymous() {
				return nserr.Wrap(nserr.Usage, oci.ErrNoCredential)
			}
			var fetch func(contentPath string) ([]byte, error)
			if artifactsDir == "" {
				// Lazily built: a publisher whose --artifacts holds everything
				// never needs a tenant.
				fetch = func(contentPath string) ([]byte, error) {
					cloud, err := libs.cloud(cmd, opts)
					if err != nil {
						return nil, err
					}
					return cloud.Fetch(cmd.Context(), contentPath)
				}
			}
			m, blobs, pinned, tagged, err := buildStackFromRepo(repoDir, ref, artifactsDir, fetch)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pushing %s (%d layer(s)) with credential from %s\n", tagged, len(m.Layers), cred.Source)
			d, err := stack.Push(cmd.Context(), oci.New(cred), tagged, m, blobs)
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
	cmd.Flags().StringVar(&artifactsDir, "artifacts", "", "directory holding <class>_<version>_build.zip for every pinned companion")
	cmd.Flags().BoolVar(&updateLock, "update-lock", false, "write the zips' digests back into the repository's lock")
	libs.bindCloud(cmd)
	libs.bindTenant(cmd)
	return cmd
}
