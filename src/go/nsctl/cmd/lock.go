package cmd

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versions"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// exactVersion matches a version rather than a range. Every manifest in a real
// workspace writes ranges, which is why this is the *second* tier and not the
// only one.
var exactVersion = regexp.MustCompile(`^\d+(\.\d+)*$`)

func newLockCommand(opts *Options) *cobra.Command {
	var fromEnv string
	var resolve bool
	var libs librarians
	var pins []string
	var check bool

	cmd := &cobra.Command{
		Use:   "lock [path]",
		Short: "Pin a repository's local environment to concrete versions",
		Long: `Reads a repository's deploy.dependencies and its local section and writes
neuronsphere.lock at the repository root -- a generated file the repository
checks in, so that a fresh clone stands up the same platform as the machine
that wrote it.

Resolving a version specifier to a concrete version is the hard part, so the
tiers are ordered by how certain they are:

  --from-env <env>  pin what is actually running. A known-good environment is
                    the only thing that has ever proved these versions work
                    together, which is how a lock should be born.
  --pin c@v         and any version_spec that is already an exact version.

A specifier neither settles is reported with both remedies rather than guessed
at. Resolving a range against a librarian is a separate mechanism; see NERD011.

Every profile's entries are pinned, not only the ones active now: activation is
a read-time filter, and a lock covering one profile would force a re-resolve the
first time anybody switched.

The lock never names an instance, only a repo class and the dependency roles it
fills. That is what lets two engineers who name their local instances
differently share one checked-in lock.`,
		Example: `  nsctl lock
  nsctl lock --from-env local
  nsctl lock --pin hmd-ms-transform@0.5.201
  nsctl lock --check`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoDir := "."
			if len(args) == 1 {
				repoDir = args[0]
			}
			m, err := localspec.Load(repoDir)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if check {
				return runLockCheck(cmd, repoDir, m)
			}
			var r *lockResolver
			if resolve {
				r = &lockResolver{libs: &libs}
			}
			return runLockWrite(cmd, opts, repoDir, m, fromEnv, pins, r)
		},
	}
	cmd.Flags().BoolVar(&resolve, "resolve", false,
		"Pin each range to the newest published version: from the lock entry's OCI source, else the cloud librarian (NERD019 SPEC005)")
	libs.bindCloud(cmd)
	libs.bindTenant(cmd)
	cmd.Flags().StringVar(&fromEnv, "from-env", "",
		"Pin the versions an environment is actually running")
	cmd.Flags().StringArrayVar(&pins, "pin", nil,
		"Pin one repo class, as <repo-class>@<version>. Repeatable")
	cmd.Flags().BoolVar(&check, "check", false,
		"Report whether the lock still covers the manifest, writing nothing")
	return cmd
}

// runLockCheck reports whether the lock still covers what the manifest declares.
//
// It writes nothing and contacts nothing. Both are requirements rather than
// conveniences: this is the hook a repository puts in pre-commit and in CI, so
// it has to run on a dirty tree, on an aeroplane, and in a job with no librarian
// credential.
func runLockCheck(cmd *cobra.Command, repoDir string, m *localspec.Manifest) error {
	l, err := lock.Read(repoDir)
	if err != nil {
		return nserr.Wrap(nserr.Usage, withLockRemedy(err))
	}

	missing, extra := lock.Check(l, m.Wants())
	// Stale in this direction is a warning, not an error: a developer
	// mid-refactor who has deleted a dependency should not be blocked by the
	// lock still remembering it.
	if len(extra) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: %s pins %s, which %s no longer declares. Run `nsctl lock` when you are done.\n",
			lock.FileName, strings.Join(extra, ", "), m.Path)
	}
	if len(missing) > 0 {
		return nserr.New(nserr.Fail,
			"%s is stale.\n  declared but not pinned:  %s\nRegenerate it with `nsctl lock`",
			lock.FileName, strings.Join(missing, ", "))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s covers every want %s declares (%d pinned).\n",
		lock.FileName, m.Path, len(l.Resolved))
	return nil
}

// lockResolver is --resolve: the published-version sources a range is
// settled against, in order. NERD019 SPEC005.
type lockResolver struct {
	libs *librarians
}

// resolveRange pins one ranged want: the previous lock entry's OCI source
// first (free), then the cloud librarian (paid, only with a credential).
func (r *lockResolver) resolveRange(cmd *cobra.Command, opts *Options, w localspec.Want, previous *lock.Lock) (string, string, error) {
	spec, err := versionspec.Parse(w.VersionSpec)
	if err != nil {
		return "", "", err
	}
	var tried []string
	if previous != nil {
		if e, ok := previous.Entry(w.RepoClassName); ok && e.Source != "" {
			ref, err := oci.ParseRef(e.Source)
			if err == nil {
				client := oci.New(registryCredential(opts, ref.Host, ""))
				tags, err := client.Tags(cmd.Context(), ref)
				if err == nil {
					if v, ok := spec.Highest(tags); ok {
						return v, "source " + e.Source, nil
					}
					tried = append(tried, fmt.Sprintf("source %s (published: %s)", e.Source, orNone(tags)))
				} else {
					tried = append(tried, fmt.Sprintf("source %s (%v)", e.Source, err))
				}
			}
		}
	}
	cloud, err := r.libs.cloud(cmd, opts)
	if err != nil {
		tried = append(tried, "cloud librarian ("+strings.SplitN(err.Error(), "\n", 2)[0]+")")
	} else {
		published, err := versions.Enumerate(cmd.Context(), cloud, w.RepoClassName, nil)
		if err != nil {
			tried = append(tried, fmt.Sprintf("librarian %s (%v)", cloud.BaseURL, err))
		} else {
			if v, ok := spec.Highest(published.Versions(manifest.DefaultArtifactType)); ok {
				return v, "librarian " + cloud.BaseURL, nil
			}
			tried = append(tried, fmt.Sprintf("librarian %s (nothing satisfies)", cloud.BaseURL))
		}
	}
	return "", "", fmt.Errorf("%s %q: %s", w.RepoClassName, w.VersionSpec, strings.Join(tried, "; "))
}

func runLockWrite(cmd *cobra.Command, opts *Options, repoDir string,
	m *localspec.Manifest, fromEnv string, pins []string, resolver *lockResolver) error {

	wants := m.Wants()
	wanted := map[string]bool{}
	bound := map[string]string{}
	for _, w := range wants {
		if w.Bind != "" {
			bound[w.RepoClassName] = w.Bind
			continue
		}
		wanted[w.RepoClassName] = true
	}
	// Every class the manifest names at all, which is a larger set: an optional
	// dependency the `local` section does not gate is declared and never wanted
	// locally, and telling a user those two apart is the difference between a
	// message they can act on and one they cannot.
	declared := map[string]bool{}
	for _, d := range m.Dependencies {
		declared[d.RepoClassName] = true
	}
	for _, r := range m.Local.Repos {
		declared[r.RepoClassName] = true
	}

	pinned, err := parsePins(pins, wanted, bound, declared)
	if err != nil {
		return err
	}

	generatedFrom := "pins"
	deployed := map[string]string{}
	if fromEnv != "" {
		slug, versions, err := deployedVersions(cmd, opts, fromEnv)
		if err != nil {
			return err
		}
		deployed = versions
		generatedFrom = "env:" + slug
	}

	previous, _ := lock.Read(repoDir)

	// --resolve settles every range up front, so the report can name where
	// each answer came from and a failure lists them all at once.
	resolved := map[string]string{}
	if resolver != nil {
		var failures []string
		for _, w := range wants {
			if w.Bind != "" || w.External {
				continue
			}
			if _, ok := pinned[w.RepoClassName]; ok {
				continue
			}
			if _, ok := deployed[w.RepoClassName]; ok {
				continue
			}
			if v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(w.VersionSpec), "==")); exactVersion.MatchString(v) {
				continue
			}
			if _, done := resolved[w.RepoClassName]; done {
				continue
			}
			v, from, err := resolver.resolveRange(cmd, opts, w, previous)
			if err != nil {
				failures = append(failures, err.Error())
				continue
			}
			resolved[w.RepoClassName] = v
			fmt.Fprintf(cmd.OutOrStdout(), "Resolved %s %q to %s (%s)\n", w.RepoClassName, w.VersionSpec, v, from)
		}
		if len(failures) > 0 {
			return nserr.New(nserr.Fail, "--resolve could not settle:\n  - %s\nPublish the class with `nsctl artifact push` and name it as the lock entry's `source`, or pin it", strings.Join(failures, "\n  - "))
		}
		generatedFrom = "resolve"
	}

	// --pin beats --from-env: one is a version the user typed for this run and
	// the other is whatever happens to be deployed, and an explicit answer
	// should never lose to an ambient one.
	versionOf := func(w localspec.Want) string {
		if v, ok := pinned[w.RepoClassName]; ok {
			return v
		}
		if v, ok := deployed[w.RepoClassName]; ok {
			return v
		}
		if v, ok := resolved[w.RepoClassName]; ok {
			return v
		}
		if v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(w.VersionSpec), "==")); exactVersion.MatchString(v) {
			return v
		}
		return ""
	}

	l, unresolved, err := lock.Build(m.RepoClassName, generatedFrom, wants, versionOf)
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}
	if len(unresolved) > 0 {
		return unresolvedError(unresolved)
	}
	// Digests come from the cache when the bytes have been seen (NERD017
	// SPEC007); nothing is fetched to find one, and an entry stays without
	// one until a pull or a push sees the zip. Also kept when the previous
	// lock had one for the same version, so a regenerate does not lose it.
	if previous != nil {
		for _, e := range previous.Resolved {
			cur, ok := l.Entry(e.RepoClassName)
			if !ok {
				continue
			}
			if cur.Version == e.Version && e.Digest != "" {
				l.SetDigest(e.RepoClassName, e.Digest)
			}
			// A source outlives a version change: it names where the class
			// is published, not which version.
			if e.Source != "" {
				l.SetSource(e.RepoClassName, e.Source)
			}
		}
	}
	for _, e := range l.Resolved {
		if d, ok := artifact.Digest(opts.Home, e.RepoClassName, e.Version); ok {
			l.SetDigest(e.RepoClassName, d)
		}
	}
	if err := lock.Write(repoDir, l); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Wrote %s (%s)\n", lock.Path(repoDir), l.GeneratedFrom)
	for _, e := range l.Resolved {
		gated := ""
		if len(e.Profiles) > 0 {
			gated = "  [" + strings.Join(e.Profiles, " ") + "]"
		}
		fmt.Fprintf(out, "  %-40s %s%s\n", e.RepoClassName, e.Version, gated)
	}
	return nil
}

// unresolvedError reports every specifier that could not be settled, with both
// remedies.
//
// All of them at once, deliberately: a real manifest carries several ranges, and
// one failure per run would mean one edit per run.
func unresolvedError(unresolved []localspec.Want) error {
	seen := map[string]string{}
	var classes []string
	for _, w := range unresolved {
		if _, dup := seen[w.RepoClassName]; dup {
			continue
		}
		seen[w.RepoClassName] = w.VersionSpec
		classes = append(classes, w.RepoClassName)
	}
	sort.Strings(classes)

	var b strings.Builder
	for _, class := range classes {
		fmt.Fprintf(&b, "cannot resolve %q for %s.\n", seen[class], class)
	}
	b.WriteString("  pin it:                 nsctl lock --pin <class>@<version>\n")
	b.WriteString("  or take a known-good:   nsctl lock --from-env <env>")
	return nserr.New(nserr.Usage, "%s", b.String())
}

// parsePins reads the --pin flag, refusing a class nothing wants locally rather
// than writing an entry nothing will ever read.
//
// The three ways that happens have different fixes, so they are reported apart.
// A class the manifest never names is a typo. A class it names only as an
// optional dependency the `local` section does not gate is a deliberate state --
// an ungated optional dependency keeps today's behaviour, which is to stay
// undeclared -- and the fix is to gate it, not to correct the spelling. A class
// whose role is bound to what the environment provides is never deployed, so a
// version for it is a version of nothing.
func parsePins(pins []string, wanted map[string]bool, bound map[string]string,
	declared map[string]bool) (map[string]string, error) {
	out := map[string]string{}
	for _, pin := range pins {
		class, version := splitVersion(pin)
		if class == "" || version == "" {
			return nil, nserr.New(nserr.Usage,
				"--pin %q: expected <repo-class>@<version>", pin)
		}
		switch {
		case wanted[class]:
		case bound[class] != "":
			return nil, nserr.New(nserr.Usage,
				"--pin %q: %s is bound to %q, which the environment provides, so nothing of that"+
					" class is deployed and there is no version to pin", pin, class, bound[class])
		case declared[class]:
			return nil, nserr.New(nserr.Usage,
				"--pin %q: %s is declared, but nothing wants it locally, so pinning it would write"+
					" an entry nothing reads.\nIt is an optional dependency the `local` section does"+
					" not gate. Add it under local.dependencies with a profile to deploy it here",
				pin, class)
		default:
			return nil, nserr.New(nserr.Usage,
				"--pin %q: this repository does not declare %s at all, so pinning it would write an"+
					" entry nothing reads", pin, class)
		}
		out[class] = version
	}
	return out, nil
}

// deployedVersions reads what an environment is actually running.
//
// This contacts ms-deployment, which is local. Nothing in `lock` contacts a
// librarian in either tier.
func deployedVersions(cmd *cobra.Command, opts *Options, envName string) (string, map[string]string, error) {
	_, _, slug, err := resolveEnvSlug(opts, envName)
	if err != nil {
		return "", nil, err
	}
	url := environment.MSDeploymentURL(opts.Lookup)
	client := msdeploy.New(url)
	if !client.Reachable(cmd.Context()) {
		return "", nil, nserr.New(nserr.Fail,
			"hmd-ms-deployment is not answering at %s, so there is nothing to pin."+
				" Start the environment first with `nsctl env start %s`", url, slug)
	}
	instances, err := client.EnvironmentInstances(cmd.Context(), slug)
	if err != nil {
		return "", nil, nserr.Wrap(nserr.Fail, err)
	}
	versions := map[string]string{}
	for _, i := range instances {
		if i.RepoClassName != "" && i.RepoClassVersion != "" {
			versions[i.RepoClassName] = i.RepoClassVersion
		}
	}
	return slug, versions, nil
}

// withLockRemedy appends "generate one" only when there is no lock to read.
//
// A lock that exists but cannot be parsed already carries its own remedy, and
// following this one instead would overwrite a file whose problem is that this
// binary is too old for it.
func withLockRemedy(err error) error {
	if errors.Is(err, lock.ErrAbsent) {
		return fmt.Errorf("%w.\nGenerate one with `nsctl lock`", err)
	}
	return err
}
