package controlplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBuilder records what would have been run against a daemon.
type fakeBuilder struct {
	present map[string]bool
	calls   [][]string
	err     error
}

func (f *fakeBuilder) ImagePresent(ctx context.Context, ref string) bool { return f.present[ref] }

func (f *fakeBuilder) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, args)
	return nil, []byte("build output"), f.err
}

// runnerOptions is testOptions with a build version, which the image tag needs.
func runnerOptions(t *testing.T, vars map[string]string) *Options {
	t.Helper()
	opts := testOptions(t.TempDir(), vars)
	opts.Version = "9.9.9"
	return opts
}

// The tag has to change when the runner's sources change, or a control plane
// would keep serving an image built from code the binary no longer ships.
func TestRunnerImageRefCarriesTheSourceDigest(t *testing.T) {
	t.Parallel()

	ref := RunnerImageRef(runnerOptions(t, nil))
	prefix := RunnerImageName + ":9.9.9-"
	if !strings.HasPrefix(ref, prefix) {
		t.Fatalf("RunnerImageRef() = %q, want the %q prefix", ref, prefix)
	}
	if digest := strings.TrimPrefix(ref, prefix); len(digest) != 12 {
		t.Errorf("digest %q is %d characters, want 12", digest, len(digest))
	}
}

func TestRunnerImageRefHonoursAnExplicitTag(t *testing.T) {
	t.Parallel()

	opts := runnerOptions(t, map[string]string{"HMD_NSRUNNER_IMAGE": "hmd-img-nsrunner:mine"})
	if got := RunnerImageRef(opts); got != "hmd-img-nsrunner:mine" {
		t.Errorf("RunnerImageRef() = %q, want the explicit tag", got)
	}
}

func TestEnsureRunnerImageSkipsAPresentImage(t *testing.T) {
	t.Parallel()

	opts := runnerOptions(t, nil)
	docker := &fakeBuilder{present: map[string]bool{RunnerImageRef(opts): true}}
	if err := EnsureRunnerImage(context.Background(), opts, docker); err != nil {
		t.Fatal(err)
	}
	if len(docker.calls) != 0 {
		t.Errorf("built %v, want no build for an image already present", docker.calls)
	}
}

// The whole point of embedding the sources: a machine that installed nsctl from
// Homebrew has no working tree, and the build context has to come out of the
// binary.
func TestEnsureRunnerImageBuildsFromTheEmbeddedSources(t *testing.T) {
	t.Parallel()

	opts := runnerOptions(t, nil)
	docker := &fakeBuilder{present: map[string]bool{}}
	if err := EnsureRunnerImage(context.Background(), opts, docker); err != nil {
		t.Fatal(err)
	}
	if len(docker.calls) != 1 {
		t.Fatalf("ran %d builds, want 1", len(docker.calls))
	}
	args := strings.Join(docker.calls[0], " ")
	dir := RunnerBuildDir(opts.Home)
	for _, want := range []string{"build", "VERSION=9.9.9", RunnerImageRef(opts), RunnerImageName + ":9.9.9", dir} {
		if !strings.Contains(args, want) {
			t.Errorf("build args %q are missing %q", args, want)
		}
	}
	// The context has to be something docker can actually build.
	for _, name := range []string{"Dockerfile", "go.mod", "main.go", filepath.Join("internal", "bundled", "services", "docker-compose.control-plane.yml")} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("build context is missing %s: %v", name, err)
		}
	}
}

// An explicit tag that is absent is a mistake worth naming: silently building
// our own sources over it would ignore what the user asked for.
func TestEnsureRunnerImageRefusesAMissingExplicitTag(t *testing.T) {
	t.Parallel()

	opts := runnerOptions(t, map[string]string{"HMD_NSRUNNER_IMAGE": "hmd-img-nsrunner:mine"})
	docker := &fakeBuilder{present: map[string]bool{}}
	err := EnsureRunnerImage(context.Background(), opts, docker)
	if err == nil || !strings.Contains(err.Error(), "hmd-img-nsrunner:mine") {
		t.Fatalf("EnsureRunnerImage() = %v, want a refusal naming the tag", err)
	}
	if len(docker.calls) != 0 {
		t.Errorf("built %v, want no build", docker.calls)
	}
}

// nsctl running inside the runner image carries an empty archive, because the
// sources cannot contain themselves.
func TestUnpackRunnerSourceReportsAnEmptyArchive(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := tar.NewWriter(zw).Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "ctx")
	if err := unpackArchive(dir, buf.Bytes()); !errors.Is(err, errEmptyRunnerSource) {
		t.Fatalf("unpack of an empty archive = %v, want errEmptyRunnerSource", err)
	}
}
