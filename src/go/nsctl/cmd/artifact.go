package cmd

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
	"github.com/spf13/cobra"
)

// BuildOutputDirEnv is where `hmd build` writes its zip when HMD_AUTO_PUBLISH
// is not set. The two halves of that fork are the same artifact; `register` is
// the second half made available after the fact.
const BuildOutputDirEnv = "HMD_BUILD_OUTPUT_DIR"

func newArtifactCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Fill the control plane's Artifact Librarian",
		Long: `Manages the versioned artifacts a ` + "`source: {type: artifact}`" + ` instance deploys from.

A RepoClass declared that way is resolved from the control plane's own Artifact
Librarian and an unpacked copy under $HMD_HOME/.cache/neuronsphere/artifacts,
never from a checkout -- so a plugin is distributed by version number rather
than by git remote. These verbs are how that librarian gets filled.

Nothing else reaches the network for an artifact. An apply resolves from the
cache alone, which is what makes it behave the same on an aeroplane.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newArtifactVersionsCommand(opts),
		newArtifactPullCommand(opts),
		newArtifactRegisterCommand(opts),
		newArtifactUnpackCommand(opts),
		newArtifactCacheCommand(opts),
		newArtifactPushCommand(opts),
	)
	return cmd
}

// librarians holds the flags every verb here shares.
type librarians struct {
	cloudURL string
	localURL string
	profile  profileFlag
}

func (l *librarians) bind(cmd *cobra.Command) {
	l.bindCloud(cmd)
	cmd.Flags().StringVar(&l.localURL, "local-url", "",
		"the control plane's Artifact Librarian")
}

// bindCloud binds only the cloud URL, for a verb that reads a cloud librarian
// and never writes to the control plane's. A flag a command cannot act on is a
// question about what it secretly does with it.
func (l *librarians) bindCloud(cmd *cobra.Command) {
	cmd.Flags().StringVar(&l.cloudURL, "url", "",
		"cloud Artifact Librarian URL, overriding "+librarian.URLEnv)
}

// bindTenant adds --profile, and is separate from bind for a reason worth
// stating: `nsctl env apply` and `nsctl env add` already have a --profile, and
// it means something else entirely -- NERD010's local profiles, which select
// part of a repository's declared environment rather than which tenant to
// reach.
//
// The two have never needed to appear on one command, and binding the tenant
// flag explicitly is what makes that a decision rather than an accident: a
// future command that wants both will fail to start, loudly, instead of
// shipping one flag that means two things.
func (l *librarians) bindTenant(cmd *cobra.Command) {
	l.profile.bind(cmd)
}

// cloud builds a client for the cloud librarian.
//
// --url and the profile are handed to the client rather than layered over the
// injected lookup, which is what this used to do. Both are inputs to one
// precedence rule that now lives in internal/nsconfig, and a flag smuggled in
// as an environment variable cannot be told apart from one that was really set
// -- which is precisely the distinction the shadowing warning needs.
func (l *librarians) cloud(cmd *cobra.Command, opts *Options) (*librarian.Client, error) {
	profile, err := l.profile.resolve(opts)
	if err != nil {
		return nil, err
	}
	c, err := librarian.New(librarian.Config{
		Home:    opts.Home,
		Lookup:  opts.Lookup,
		Profile: profile,
		FlagURL: l.cloudURL,
	})
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	reportShadowed(cmd, &l.profile, nsconfig.ArtifactLibrarian, c.Endpoint)
	return c, nil
}

// local resolves the flag at use rather than at construction: the command tree
// is built before the registry is read, so a default captured there would name
// port 80 on a home that publishes somewhere else.
func (l *librarians) local() *librarian.Client {
	url := l.localURL
	if url == "" {
		url = librarian.LocalBaseURL()
	}
	return librarian.NewLocal(url)
}

func newArtifactPullCommand(opts *Options) *cobra.Command {
	var libs librarians
	var spec string

	cmd := &cobra.Command{
		Use:   "pull <repo-class>[@<version>][:<type>]",
		Short: "Copy a published artifact into the control plane",
		Long: `Downloads a versioned artifact from a cloud Artifact Librarian, stores it in
the control plane's own, and unpacks it into the cache an apply resolves from.

The item type defaults to ` + "`build`" + `, which is what ` + "`hmd build`" + ` publishes.

With no version, the newest published one is used; with a BACON version
specifier in its place, the newest that satisfies it. Either way the version
chosen is printed, because a command that quietly picked one has told you
nothing you could check. ` + "`nsctl artifact versions`" + ` answers the same question
without fetching anything.

This is the only command that reaches a cloud librarian on your behalf.
Resolution never does: an apply that reached the internet without being asked
is an apply that behaves differently on an aeroplane.`,
		Example: `  nsctl artifact pull hmd-inf-local-registry@0.1.4
  nsctl artifact pull hmd-inf-local-registry
  nsctl artifact pull "hmd-inf-trino@~= 0.1"
  nsctl artifact pull hmd-lang-foo@0.3.1:schema
  nsctl artifact pull ghcr.io/acme/classes/hmd-inf-otel-collector:0.1.188   # an OCI artifact, no tenant`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			// An OCI reference -- something with a registry host in front --
			// is NERD016 SPEC009's free path and needs no librarian at all.
			if ociRef, perr := oci.ParseRef(args[0]); perr == nil {
				return pullFromOCI(cmd, opts, &libs, home, ociRef, spec)
			}
			req, err := parseArtifactRequest(args[0])
			if err != nil {
				return err
			}
			cloud, err := libs.cloud(cmd, opts)
			if err != nil {
				return err
			}
			spec, err := resolveRef(cmd, opts, &libs, cloud, req)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			fmt.Fprintf(cmd.OutOrStdout(), "Fetching %s from %s\n", spec, cloud.BaseURL)
			data, err := cloud.Fetch(ctx, spec.ContentPath())
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			return storeLocally(cmd, libs.local(), home, spec, data)
		},
	}
	libs.bind(cmd)
	libs.bindTenant(cmd)
	cmd.Flags().StringVar(&spec, "spec", "", "with an OCI reference, choose the version by a BACON version spec")
	return cmd
}

func newArtifactRegisterCommand(opts *Options) *cobra.Command {
	var libs librarians
	var itemType, name, version, repoDir string

	cmd := &cobra.Command{
		Use:   "register [<path>]",
		Short: "Store a local build in the control plane",
		Long: `Stores an ` + "`hmd build`" + ` output in the control plane's Artifact Librarian, so
your own build is deployable by a manifest that names it by version -- with no
checkout under $HMD_REPO_HOME and no cloud round trip.

<path> may be a zip, or a directory which is zipped on the fly. With neither,
the zip ` + "`hmd build`" + ` writes under $HMD_BUILD_OUTPUT_DIR is used, resolving the
repo and version from meta-data/ in --repo.

Re-registering a version overwrites it and drops the unpacked copy, so the next
apply deploys the build that was just registered rather than the one before it.`,
		Example: `  nsctl artifact register
  nsctl artifact register ./dist/hmd-inf-local-registry_0.1.4_build.zip
  nsctl artifact register ~/work/hmd-inf-local-registry`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			if itemType == "" {
				itemType = manifest.DefaultArtifactType
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			data, found, err := registerInput(opts, path, repoDir, itemType)
			if err != nil {
				return err
			}

			spec := librarian.Spec{Name: name, Version: version, ItemType: itemType}
			if spec.Name == "" || spec.Version == "" {
				// From inside the zip rather than from its filename: the two
				// disagree for anything renamed, and the tree is what a deploy
				// will actually read its version out of.
				inner, err := repoAndVersionFromZip(data)
				if err != nil {
					return nserr.New(nserr.Usage, "%s: %v\n  Pass --name and --version to say what it is.", found, err)
				}
				if spec.Name == "" {
					spec.Name = inner.Name
				}
				if spec.Version == "" {
					spec.Version = inner.Version
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Registering %s from %s\n", spec, found)
			return storeLocally(cmd, libs.local(), home, spec, data)
		},
	}
	libs.bind(cmd)
	libs.bindTenant(cmd)
	cmd.Flags().StringVar(&itemType, "type", manifest.DefaultArtifactType, "librarian content item type")
	cmd.Flags().StringVar(&repoDir, "repo", ".", "repo whose meta-data names the build to look for")
	cmd.Flags().StringVar(&name, "name", "", "repo class, overriding the one in the artifact")
	cmd.Flags().StringVar(&version, "version", "", "version, overriding the one in the artifact")
	return cmd
}

func newArtifactUnpackCommand(opts *Options) *cobra.Command {
	var libs librarians

	cmd := &cobra.Command{
		Use:   "unpack <repo-class>@<version>[:<type>]",
		Short: "Unpack an artifact the control plane already holds",
		Long: `Fetches a versioned artifact from the control plane's own Artifact Librarian
and unpacks it into the cache an apply resolves from.

` + "`pull`" + ` and ` + "`register`" + ` both do this on the way past, so this is the repair
path: it refills a cache that was deleted, without a cloud round trip and
without rebuilding anything.`,
		Example:       `  nsctl artifact unpack hmd-inf-local-registry@0.1.4`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			spec, err := parseArtifactRef(args[0])
			if err != nil {
				return err
			}
			local := libs.local()
			data, err := local.Fetch(cmd.Context(), spec.ContentPath())
			if err != nil {
				return nserr.Wrap(nserr.Fail, artifact.Unavailable(
					home, spec.Name, spec.Version, spec.ContentPath(), opts.Lookup("HMD_REPO_HOME"), err))
			}
			if err := artifact.Invalidate(home, spec.Name, spec.Version); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			dir, err := artifact.Store(home, spec.Name, spec.Version, data)
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Unpacked %s to %s\n", spec, dir)
			return nil
		},
	}
	libs.bind(cmd)
	libs.bindTenant(cmd)
	return cmd
}

func newArtifactCacheCommand(opts *Options) *cobra.Command {
	var libs librarians
	var manifestPath string

	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Copy a repo's pre-build artifacts into the control plane",
		Long: `Reads build.pre_build_artifacts from a BACON manifest and copies each one from
a cloud Artifact Librarian into the control plane's, so a later build in this
HMD_HOME resolves them without the network.

The spec string BACON writes -- <name>@<version>:<item_type> -- is already the
librarian's own content-path grammar, parsed by the same function. Nothing is
translated and nothing new is named.

These are build *inputs*, so they are stored and not unpacked: an apply
resolves RepoClasses, not schemas.`,
		Example: `  nsctl artifact cache
  nsctl artifact cache --manifest ../hmd-ms-transform/meta-data/manifest.json`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if manifestPath == "" {
				manifestPath = filepath.Join("meta-data", "manifest.json")
			}
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				return nserr.New(nserr.Usage, "reading %s: %v", manifestPath, err)
			}
			specs, err := librarian.PreBuildArtifacts(data)
			if err != nil {
				return nserr.New(nserr.Usage, "%s: %v", manifestPath, err)
			}
			out := cmd.OutOrStdout()
			if len(specs) == 0 {
				fmt.Fprintf(out, "%s declares no build.pre_build_artifacts\n", manifestPath)
				return nil
			}
			cloud, err := libs.cloud(cmd, opts)
			if err != nil {
				return err
			}
			local := libs.local()

			ctx := cmd.Context()
			for _, spec := range specs {
				fmt.Fprintf(out, "  %-40s fetching %s\n", spec.Name, spec.Version)
				payload, err := cloud.Fetch(ctx, spec.ContentPath())
				if err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				if err := local.Put(ctx, spec.ContentPath(), spec.ItemType, payload); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
			}
			fmt.Fprintf(out, "Cached %d artifact(s) in the control plane\n", len(specs))
			return nil
		},
	}
	libs.bind(cmd)
	libs.bindTenant(cmd)
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "BACON manifest to read (default meta-data/manifest.json)")
	return cmd
}

// storeLocally puts an artifact in the control plane's librarian and refreshes
// the unpacked copy.
//
// Invalidate before Store, always: re-registering a version overwrites it, and
// an unpacked copy left in place would deploy the previous build under the new
// build's version. That is the same failure, from the other direction, that an
// artifact source refusing to fall back to a stale checkout exists to prevent.
func storeLocally(cmd *cobra.Command, local *librarian.Client, home string,
	spec librarian.Spec, data []byte) error {

	dir, err := storeArtifact(cmd.Context(), local, home, spec, data)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Stored %s in %s\n", spec, local.BaseURL)
	fmt.Fprintf(cmd.OutOrStdout(), "Unpacked to %s\n", dir)
	return nil
}

// storeArtifact is storeLocally without the reporting, for a caller storing
// many artifacts in a row.
//
// Two lines per artifact is right when there is one of them and is noise when
// there are twenty; what must not differ between the two callers is the
// invalidate-before-store order, which is why this is a split rather than a
// second implementation.
func storeArtifact(ctx context.Context, local *librarian.Client, home string,
	spec librarian.Spec, data []byte) (string, error) {

	if err := local.Put(ctx, spec.ContentPath(), spec.ItemType, data); err != nil {
		return "", nserr.Wrap(nserr.Fail, err)
	}
	if err := artifact.Invalidate(home, spec.Name, spec.Version); err != nil {
		return "", nserr.Wrap(nserr.Fail, err)
	}
	dir, err := artifact.Store(home, spec.Name, spec.Version, data)
	if err != nil {
		return "", nserr.Wrap(nserr.Fail, err)
	}
	return dir, nil
}

// artifactRequest is a reference to an artifact whose version may not be settled
// yet: a bare repo class, a version, or a specifier standing in for one.
type artifactRequest struct {
	Name     string
	ItemType string
	// Version is set when the reference already named one, in which case
	// nothing needs resolving and no librarian is asked.
	Version string
	// Spec is the specifier to resolve against what is published. The zero Spec
	// admits everything, which is what a bare repo class means.
	Spec versionspec.Spec
}

// Describe names what was asked for, for the line that reports what it resolved
// to.
func (r artifactRequest) Describe() string {
	if r.Spec.Empty() {
		return r.Name
	}
	return r.Name + " " + r.Spec.String()
}

// parseArtifactRequest reads <repo-class>[@<version-or-spec>][:<type>].
//
// This is parseArtifactRef's grammar with the version made optional, kept apart
// from it because the two have different rules for a good reason: `unpack` reads
// the *local* librarian, and resolving a range against that would answer with
// what this machine happens to hold rather than with what is published -- the
// right answer for a cache probe and the wrong one for choosing a version.
func parseArtifactRequest(ref string) (artifactRequest, error) {
	out := artifactRequest{ItemType: manifest.DefaultArtifactType}

	rest := ref
	if at := strings.Index(ref, "@"); at > 0 {
		out.Name, rest = ref[:at], ref[at+1:]
	} else {
		out.Name, rest = ref, ""
		if colon := strings.Index(ref, ":"); colon > 0 {
			out.Name, rest = ref[:colon], ":"+ref[colon+1:]
		}
	}
	if colon := strings.Index(rest, ":"); colon >= 0 {
		if itemType := rest[colon+1:]; itemType != "" {
			out.ItemType = itemType
		}
		rest = rest[:colon]
	}
	if out.Name == "" {
		return artifactRequest{}, nserr.New(nserr.Usage,
			"%q: expected <repo-class>[@<version>][:<type>]", ref)
	}

	switch {
	case rest == "":
		// A bare repo class: the newest published, which the zero Spec admits.
	case versionspec.IsVersion(rest):
		out.Version = rest
	default:
		spec, err := versionspec.Parse(rest)
		if err != nil {
			return artifactRequest{}, specError(out.Name, rest, err)
		}
		out.Spec = spec
	}
	return out, nil
}

// parseArtifactRef reads <repo-class>@<version>[:<type>], defaulting the type.
//
// The grammar is ParseSpec's, which requires the type; it is defaulted here
// rather than there because BACON's pre_build_artifacts always writes one and a
// silently defaulted type would be a silently wrong address in that setting.
func parseArtifactRef(ref string) (librarian.Spec, error) {
	at := strings.Index(ref, "@")
	if at > 0 && !strings.Contains(ref[at+1:], ":") {
		ref += ":" + manifest.DefaultArtifactType
	}
	spec, err := librarian.ParseSpec(ref)
	if err != nil {
		return librarian.Spec{}, nserr.Wrap(nserr.Usage, err)
	}
	return spec, nil
}

// registerInput resolves the three inputs SPEC005 allows, in order, and returns
// the bytes with a description of where they came from.
func registerInput(opts *Options, path, repoDir, itemType string) (data []byte, found string, err error) {
	if path != "" {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, "", nserr.New(nserr.Usage, "%s: %v", path, statErr)
		}
		if info.IsDir() {
			// Zipped deterministically, so registering an unchanged tree twice
			// stores the same bytes rather than a version whose contents move
			// under it.
			payload, zipErr := artifact.Zip(path)
			if zipErr != nil {
				return nil, "", nserr.New(nserr.Usage, "zipping %s: %v", path, zipErr)
			}
			return payload, path + " (zipped)", nil
		}
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, "", nserr.New(nserr.Usage, "%s: %v", path, readErr)
		}
		return payload, path, nil
	}

	// The conventional path. Deliberately not `hmd build` shelled out to: the
	// Python push_artifact builds by default, and nsctl's premise is that
	// Docker is the only host prerequisite, so a register that needed the
	// Python CLI installed would fail in exactly the environment nsctl exists
	// to serve.
	if repoDir == "" {
		repoDir = "."
	}
	repo, verErr := repoAndVersionFromDir(repoDir)
	outputDir := opts.Lookup(BuildOutputDirEnv)
	if verErr == nil && outputDir != "" {
		conventional := filepath.Join(outputDir,
			fmt.Sprintf("%s-%s-build", repo.Name, repo.Version),
			fmt.Sprintf("%s_%s_%s.zip", repo.Name, repo.Version, itemType))
		if payload, readErr := os.ReadFile(conventional); readErr == nil {
			return payload, conventional, nil
		}
	}
	return nil, "", nserr.New(nserr.Usage, "nothing to register.\n"+
		"  a zip:        nsctl artifact register <path>.zip\n"+
		"  a directory:  nsctl artifact register <path>\n"+
		"  a build:      %s, which `hmd build` writes when HMD_AUTO_PUBLISH is unset\n"+
		"Produce the last with:  HMD_BUILD_OUTPUT_DIR=%s hmd build",
		buildOutputHint(outputDir, repo), orNotSetHere(outputDir))
}

// repoIdentity is a repo class and the version it declares.
type repoIdentity struct{ Name, Version string }

// repoAndVersionFromDir reads meta-data/manifest.json and meta-data/VERSION,
// which is what _resolve_repo_and_version reads.
func repoAndVersionFromDir(dir string) (repoIdentity, error) {
	data, err := os.ReadFile(filepath.Join(dir, "meta-data", "manifest.json"))
	if err != nil {
		return repoIdentity{}, err
	}
	version, err := os.ReadFile(filepath.Join(dir, "meta-data", "VERSION"))
	if err != nil {
		return repoIdentity{}, err
	}
	return repoIdentityFrom(data, version)
}

// repoAndVersionFromZip reads the same two files out of an artifact.
func repoAndVersionFromZip(data []byte) (repoIdentity, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return repoIdentity{}, fmt.Errorf("not a zip: %w", err)
	}
	var manifestJSON, version []byte
	for _, f := range zr.File {
		switch filepath.ToSlash(filepath.Clean(f.Name)) {
		case "meta-data/manifest.json":
			manifestJSON, err = readZipEntry(f)
		case "meta-data/VERSION":
			version, err = readZipEntry(f)
		default:
			continue
		}
		if err != nil {
			return repoIdentity{}, err
		}
	}
	if manifestJSON == nil || version == nil {
		return repoIdentity{}, fmt.Errorf("the artifact carries no meta-data/manifest.json and meta-data/VERSION")
	}
	return repoIdentityFrom(manifestJSON, version)
}

func repoIdentityFrom(manifestJSON, version []byte) (repoIdentity, error) {
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return repoIdentity{}, fmt.Errorf("parsing meta-data/manifest.json: %w", err)
	}
	id := repoIdentity{Name: m.Name, Version: strings.TrimSpace(string(version))}
	if id.Name == "" || id.Version == "" {
		return repoIdentity{}, fmt.Errorf("meta-data names no repo class or no version")
	}
	return id, nil
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 1<<20))
}

// buildOutputHint names the exact file that was looked for when enough is known
// to name it, and the directory otherwise.
func buildOutputHint(outputDir string, repo repoIdentity) string {
	if repo.Name == "" || outputDir == "" {
		return "$" + BuildOutputDirEnv + "/<repo>-<version>-build/<repo>_<version>_build.zip"
	}
	return filepath.Join(outputDir, fmt.Sprintf("%s-%s-build", repo.Name, repo.Version),
		fmt.Sprintf("%s_%s_build.zip", repo.Name, repo.Version))
}

func orNotSetHere(s string) string {
	if s == "" {
		return "<a directory>"
	}
	return s
}

// localLibrarianURL is the control plane's Artifact Librarian, or the override
// a caller passed. Resolved at use: the flag default would otherwise be fixed
// when the command tree is built, before this home's ports are known.
func localLibrarianURL(override string) string {
	if override != "" {
		return override
	}
	return librarian.LocalBaseURL()
}
