package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scriptedDocker answers each docker call from a function and records them.
type scriptedDocker struct {
	calls []string
	fn    func(args []string) ([]byte, error)
}

func (s *scriptedDocker) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	s.calls = append(s.calls, strings.Join(args, " "))
	out, err := s.fn(args)
	return nil, out, err
}

func lambdaTree(t *testing.T, deployCmds, record string) string {
	t.Helper()
	dir := t.TempDir()
	md := filepath.Join(dir, "meta-data")
	if err := os.MkdirAll(md, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(md, "manifest.json"),
		[]byte(`{"deploy":{"commands":`+deployCmds+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if record != "" {
		if err := os.WriteFile(filepath.Join(md, lambdaImageRecord), []byte(record), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const transformRecord = `{"name":"hmd-ms-transform","tool":"docker","external_location":"ghcr.io/hmdlabs/hmd-ms-transform:1.0.916"}`

func TestStagePullsTheRecordedRefAndTagsTheBareOne(t *testing.T) {
	t.Parallel()
	cached := map[string]bool{}
	d := &scriptedDocker{fn: func(a []string) ([]byte, error) {
		switch a[0] {
		case "image":
			if cached[a[len(a)-1]] {
				return nil, nil
			}
			return nil, errors.New("no such image")
		case "pull":
			cached[a[1]] = true
		case "tag":
			cached[a[2]] = true
		}
		return nil, nil
	}}
	r := testRunner(t, &fakeDocker{}, t.TempDir())
	r.Docker = d

	tree := lambdaTree(t, `["docker",["helm"]]`, transformRecord)
	if err := r.stageLambdaImage(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	if !cached["hmd-ms-transform:1.0.916"] {
		t.Fatalf("bare tag not staged; calls: %v", d.calls)
	}
	want := "pull ghcr.io/hmdlabs/hmd-ms-transform:1.0.916"
	found := false
	for _, c := range d.calls {
		found = found || c == want
	}
	if !found {
		t.Fatalf("expected %q, calls: %v", want, d.calls)
	}
}

func TestStageLeavesALocalBuildAlone(t *testing.T) {
	t.Parallel()
	d := &scriptedDocker{fn: func([]string) ([]byte, error) { return nil, nil }}
	r := testRunner(t, &fakeDocker{}, t.TempDir())
	r.Docker = d

	if err := r.stageLambdaImage(context.Background(), lambdaTree(t, `["docker"]`, transformRecord)); err != nil {
		t.Fatal(err)
	}
	for _, c := range d.calls {
		if strings.HasPrefix(c, "pull") || strings.HasPrefix(c, "tag") {
			t.Fatalf("a cached bare tag must win, got %q", c)
		}
	}
}

func TestStageSkipsWhatDeploysNoLambdaImageOrHasNoRecord(t *testing.T) {
	t.Parallel()
	d := &scriptedDocker{fn: func([]string) ([]byte, error) { return nil, errors.New("must not be called") }}
	r := testRunner(t, &fakeDocker{}, t.TempDir())
	r.Docker = d

	for name, tree := range map[string]string{
		"no docker command": lambdaTree(t, `["helm"]`, transformRecord),
		"no record":         lambdaTree(t, `["docker"]`, ""),
		"no tree":           t.TempDir(),
	} {
		if err := r.stageLambdaImage(context.Background(), tree); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("docker was called: %v", d.calls)
	}
}

func TestStageFailsNamingTheImageWhenItCannotBePulled(t *testing.T) {
	t.Parallel()
	d := &scriptedDocker{fn: func(a []string) ([]byte, error) {
		if a[0] == "pull" {
			return []byte("denied"), errors.New("exit 1")
		}
		return nil, errors.New("no such image")
	}}
	r := testRunner(t, &fakeDocker{}, t.TempDir())
	r.Docker = d

	err := r.stageLambdaImage(context.Background(), lambdaTree(t, `["docker"]`, transformRecord))
	if err == nil || !strings.Contains(err.Error(), "ghcr.io/hmdlabs/hmd-ms-transform:1.0.916") ||
		!strings.Contains(err.Error(), "denied") {
		t.Fatalf("error should name the image and the cause, got %v", err)
	}
}

func TestImageTag(t *testing.T) {
	t.Parallel()
	for ref, want := range map[string]string{
		"ghcr.io/hmdlabs/x:1.2.3": "1.2.3",
		"localhost:5000/x":        "",
		"localhost:5000/x:9":      "9",
		"x":                       "",
	} {
		if got := imageTag(ref); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", ref, got, want)
		}
	}
}
