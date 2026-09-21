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
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/stack"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// buildStackFromRepo is NERD017 SPEC006's assembly: the repository tree as
// the subject's zip, each pinned companion from artifactsDir or fetch, the
// tag from the reference or meta-data/VERSION.
func buildStackFromRepo(repoDir string, ref oci.Ref, artifactsDir string,
	fetch func(contentPath string) ([]byte, error)) (v1.Manifest, map[digest.Digest][]byte, *lock.Lock, oci.Ref, error) {

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
	if ref.Tag == "" {
		version, err := os.ReadFile(filepath.Join(repoDir, "meta-data", "VERSION"))
		if err != nil {
			return fail(nserr.Wrap(nserr.Usage, fmt.Errorf("no tag in %s and no meta-data/VERSION: %w", ref, err)))
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
		data, err := companionZip(e, artifactsDir, fetch)
		if err != nil {
			return fail(err)
		}
		zips[e.RepoClassName] = data
	}
	m, blobs, pinned, err := stack.Build(ref, ref.Tag, l, zips)
	if err != nil {
		return fail(nserr.Wrap(nserr.Fail, err))
	}
	return m, blobs, pinned, ref, nil
}

// companionZip finds a pinned class's build zip: in artifactsDir by the
// librarian file name, else through fetch.
func companionZip(e lock.Entry, artifactsDir string, fetch func(string) ([]byte, error)) ([]byte, error) {
	if artifactsDir != "" {
		name := fmt.Sprintf("%s_%s_%s.zip", e.RepoClassName, e.Version, stack.ItemType)
		for _, candidate := range []string{
			filepath.Join(artifactsDir, name),
			filepath.Join(artifactsDir, e.RepoClassName, name),
		} {
			if data, err := os.ReadFile(candidate); err == nil {
				return data, nil
			}
		}
		if fetch == nil {
			return nil, nserr.New(nserr.Usage, "--artifacts %s holds no %s for %s@%s", artifactsDir, name, e.RepoClassName, e.Version)
		}
	}
	if fetch == nil {
		return nil, nserr.New(nserr.Usage, "no --artifacts directory and no librarian to fetch %s@%s from", e.RepoClassName, e.Version)
	}
	data, err := fetch(e.ContentPath)
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, fmt.Errorf("fetching %s@%s: %w", e.RepoClassName, e.Version, err))
	}
	return data, nil
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
