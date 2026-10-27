package floci

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockercreds"
)

// fakeAuthDocker holds the file as the container would, and records the
// docker commands the sync issued.
type fakeAuthDocker struct {
	file     string
	runs     [][]string
	copied   string
	copyMode os.FileMode
}

func (f *fakeAuthDocker) Exec(_ context.Context, _ string, args ...string) ([]byte, error) {
	if f.file == "" {
		return nil, errors.New("cat: can't open")
	}
	return []byte(f.file), nil
}

func (f *fakeAuthDocker) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	f.runs = append(f.runs, args)
	if args[0] == "cp" {
		b, err := os.ReadFile(args[1])
		if err != nil {
			return nil, nil, err
		}
		st, _ := os.Stat(args[1])
		f.copied, f.copyMode, f.file = string(b), st.Mode().Perm(), string(b)
	}
	return nil, nil, nil
}

var ghcr = map[string]dockercreds.Credential{"ghcr.io": {Username: "alice", Secret: "ghp_secret"}}

// A cluster with no registries.yaml gets one, carrying the credential, written
// owner-only, and k3s is restarted so containerd reads it.
func TestRegistryAuthIsWrittenAndK3sRestarted(t *testing.T) {
	t.Parallel()

	f := &fakeAuthDocker{}
	changed, err := SyncK3sRegistryAuth(context.Background(), f, "floci-eks-x", ghcr, t.TempDir())
	if err != nil || !changed {
		t.Fatalf("SyncK3sRegistryAuth = %v, %v; want changed", changed, err)
	}
	for _, want := range []string{`"ghcr.io"`, "alice", "ghp_secret"} {
		if !strings.Contains(f.copied, want) {
			t.Errorf("registries.yaml does not carry %s:\n%s", want, f.copied)
		}
	}
	if f.copyMode != 0o600 {
		t.Errorf("staged with mode %v, want 0600", f.copyMode)
	}
	var restarted bool
	for _, r := range f.runs {
		if r[0] == "restart" && r[len(r)-1] == "floci-eks-x" {
			restarted = true
		}
		if strings.Contains(strings.Join(r, " "), "ghp_secret") {
			t.Errorf("the secret is on a docker command line: %v", r)
		}
	}
	if !restarted {
		t.Errorf("k3s was not restarted: %v", f.runs)
	}
}

// The same credential twice is a no-op: a warm start must not restart a
// running cluster for nothing.
func TestUnchangedRegistryAuthRestartsNothing(t *testing.T) {
	t.Parallel()

	f := &fakeAuthDocker{file: renderRegistries(ghcr)}
	changed, err := SyncK3sRegistryAuth(context.Background(), f, "floci-eks-x", ghcr, t.TempDir())
	if err != nil || changed {
		t.Fatalf("SyncK3sRegistryAuth = %v, %v; want unchanged", changed, err)
	}
	if len(f.runs) != 0 {
		t.Errorf("ran %v for an unchanged credential", f.runs)
	}
}

// No credential is not a reason to touch the cluster at all.
func TestNoCredentialTouchesNothing(t *testing.T) {
	t.Parallel()

	f := &fakeAuthDocker{}
	if changed, err := SyncK3sRegistryAuth(context.Background(), f, "floci-eks-x", nil, t.TempDir()); err != nil || changed {
		t.Fatalf("SyncK3sRegistryAuth = %v, %v; want nothing", changed, err)
	}
	if len(f.runs) != 0 {
		t.Errorf("ran %v with no credential", f.runs)
	}
}
