package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/classart"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/stack"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// layoutHost is the placeholder host `stack build` uses: the layout is
// pushed to a reference chosen later, so the artifact carries no name.
const layoutHost = "layout"

// zipSources is NERD019 SPEC002: where a pinned zip comes from, in order.
// Each tier is optional (nil means "not available"); the first that answers
// wins, and the answer is verified against the lock's digest when it has one.
type zipSources struct {
	artifactsDir string
	home         string
	// registry builds a client for an entry's OCI source.
	registry func(host string) *oci.Client
	// cloud is the paid path, built lazily so a publisher whose lock is
	// fully served by the free tiers never needs a tenant.
	cloud func() (*librarian.Client, error)
	// report receives one line per entry naming the tier that served it.
	report func(string)
}

func (z zipSources) fetch(ctx context.Context, e lock.Entry) ([]byte, error) {
	say := func(tier string) {
		if z.report != nil {
			z.report(fmt.Sprintf("  %s@%s  <- %s", e.RepoClassName, e.Version, tier))
		}
	}
	verify := func(data []byte, tier string) ([]byte, error) {
		if err := e.VerifyDigest(digest.FromBytes(data).String()); err != nil {
			return nil, fmt.Errorf("%s: %w", tier, err)
		}
		say(tier)
		return data, nil
	}
	var tried []string
	name := fmt.Sprintf("%s_%s_%s.zip", e.RepoClassName, e.Version, stack.ItemType)

	// 1. A release directory.
	if z.artifactsDir != "" {
		for _, candidate := range []string{filepath.Join(z.artifactsDir, name), filepath.Join(z.artifactsDir, e.RepoClassName, name)} {
			if data, err := os.ReadFile(candidate); err == nil {
				return verify(data, "--artifacts "+candidate)
			}
		}
		tried = append(tried, "--artifacts "+z.artifactsDir+" (no "+name+")")
	}
	// 2. The artifact cache, re-zipped. Bytes differ from the original zip,
	// so only a digest the lock carries can vouch for them; without one the
	// tree is what the environment deployed from, and that is the vouch.
	if z.home != "" && artifact.Cached(z.home, e.RepoClassName, e.Version) {
		dir := artifact.Dir(z.home, e.RepoClassName, e.Version)
		if data, err := artifact.Zip(dir); err == nil {
			if e.Digest == "" || e.Digest == digest.FromBytes(data).String() {
				say("artifact cache " + dir)
				return data, nil
			}
			tried = append(tried, "artifact cache (digest differs from the lock's)")
		}
	} else if z.home != "" {
		tried = append(tried, "artifact cache (not cached)")
	}
	// 3. The entry's OCI source: the free path.
	if e.Source != "" && z.registry != nil {
		ref, err := oci.ParseRef(e.Source)
		if err != nil {
			return nil, fmt.Errorf("%s: lock source %q: %w", e.RepoClassName, e.Source, err)
		}
		got, err := classart.Fetch(ctx, z.registry(ref.Host), ref.WithTag(e.Version))
		if err == nil {
			return verify(got.Zip, "source "+ref.WithTag(e.Version).String())
		}
		tried = append(tried, fmt.Sprintf("source %s (%v)", e.Source, err))
	} else if e.Source == "" {
		tried = append(tried, "no `source` in the lock entry")
	}
	// 4. The librarian: the paid path.
	if z.cloud != nil {
		cloud, err := z.cloud()
		if err != nil {
			tried = append(tried, "cloud librarian ("+err.Error()+")")
		} else {
			data, err := cloud.Fetch(ctx, e.ContentPath)
			if err == nil {
				return verify(data, "librarian "+cloud.BaseURL)
			}
			tried = append(tried, fmt.Sprintf("librarian %s (%v)", cloud.BaseURL, err))
		}
	}
	return nil, nserr.New(nserr.Usage, "no source holds %s@%s:\n  - %s\nPublish it with `nsctl artifact push <its-dir> <oci-ref>` and set that reference as the lock entry's `source`",
		e.RepoClassName, e.Version, strings.Join(tried, "\n  - "))
}

// buildStackFromRepo is NERD017 SPEC006's assembly: the repository tree as
// the subject's zip, each pinned companion from the sources in order, the
// tag from the reference, --tag, or meta-data/VERSION.
func buildStackFromRepo(ctx context.Context, repoDir string, ref oci.Ref, tag string, sources zipSources) (v1.Manifest, map[digest.Digest][]byte, *lock.Lock, oci.Ref, error) {

	fail := func(err error) (v1.Manifest, map[digest.Digest][]byte, *lock.Lock, oci.Ref, error) {
		return v1.Manifest{}, nil, nil, ref, err
	}
	repoDir, err := filepath.Abs(repoDir)
	if err != nil {
		return fail(nserr.Wrap(nserr.Usage, err))
	}
	spec, err := localspec.Load(repoDir)
	if err != nil {
		return fail(nserr.Wrap(nserr.Usage, fmt.Errorf("%s is not a stack: %w", repoDir, err)))
	}
	if len(spec.Wants()) == 0 {
		return fail(nserr.New(nserr.Usage, "%s is not a stack: its manifest declares no companions in a `local` section (NERD010 SPEC001)", repoDir))
	}
	l, err := lock.Read(repoDir)
	if err != nil {
		return fail(nserr.Wrap(nserr.Usage, withLockRemedy(err)))
	}
	if ref.Digest != "" {
		return fail(nserr.New(nserr.Usage, "push needs a tag, not a digest: %s", ref))
	}
	switch {
	case tag != "":
		ref = ref.WithTag(tag)
	case ref.Tag == "":
		version, err := os.ReadFile(filepath.Join(repoDir, "meta-data", "VERSION"))
		if err != nil {
			return fail(nserr.Wrap(nserr.Usage, fmt.Errorf("no tag and no meta-data/VERSION: %w", err)))
		}
		ref = ref.WithTag(strings.TrimSpace(string(version)))
	}
	if !versionspec.IsVersion(ref.Tag) {
		return fail(nserr.New(nserr.Usage, "tag %q is not a version; a stack is tagged with its version so `stack versions` can find it", ref.Tag))
	}

	zips := map[string][]byte{}
	subject, err := artifact.Zip(repoDir)
	if err != nil {
		return fail(nserr.Wrap(nserr.Fail, err))
	}
	zips[l.RepoClassName] = subject
	for _, e := range l.Resolved {
		data, err := sources.fetch(ctx, e)
		if err != nil {
			return fail(err)
		}
		zips[e.RepoClassName] = data
	}
	name := ""
	if ref.Host != layoutHost {
		name = stack.ShortName(ref)
	}
	m, blobs, pinned, err := stack.Build(name, ref.Tag, l, zips)
	if err != nil {
		return fail(nserr.Wrap(nserr.Fail, err))
	}
	return m, blobs, pinned, ref, nil
}

func lockPath(repoDir string) string { return lock.Path(repoDir) }

func writeLockAt(repoDir string, l *lock.Lock) error { return lock.Write(repoDir, l) }

func readLockAt(dir string) (*lock.Lock, error) { return lock.Read(dir) }

// staticTags is a Fetcher over an already-fetched tag list, for --spec.
type staticTags []string

func (s staticTags) Tags(context.Context, oci.Ref) ([]string, error) { return s, nil }
func (staticTags) Fetch(context.Context, oci.Ref) (*oci.Bundle, error) {
	return nil, fmt.Errorf("not fetchable")
}
func (staticTags) Blob(context.Context, oci.Ref, v1.Descriptor, io.Writer) error {
	return fmt.Errorf("not fetchable")
}
