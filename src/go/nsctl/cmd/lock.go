package cmd

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// exactVersion matches a version rather than a range. Every manifest in a real
// workspace writes ranges, which is why this is the *second* tier and not the
// only one.
var exactVersion = regexp.MustCompile(`^\d+(\.\d+)*$`)

func newLockCommand(opts *Options) *cobra.Command {
	var fromEnv string
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
			return runLockWrite(cmd, opts, repoDir, m, fromEnv, pins)
		},
	}
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

func runLockWrite(cmd *cobra.Command, opts *Options, repoDir string,
	m *localspec.Manifest, fromEnv string, pins []string) error {

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
