package container

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker writes an executable that prints its argv on one line, then the
// DOCKER_HOST it inherited ("<unset>" when absent).
func fakeDocker(t *testing.T) *Docker {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\necho \"$*|${DOCKER_HOST-<unset>}\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Docker{Bin: bin}
}

// Resolution is one `docker context inspect` exec, not a parse of the context
// store and not a docker/cli dependency.
//
// Requirements: HMD_CLI_NEURONSPHERE_NERD021_SPEC002
func TestContextEndpointShellsOutToDockerContextInspect(t *testing.T) {
	out, err := fakeDocker(t).ContextEndpoint(context.Background())
	if err != nil {
		t.Fatalf("ContextEndpoint: %v", err)
	}
	argv, _, _ := strings.Cut(out, "|")
	if !strings.HasPrefix(argv, "context inspect ") {
		t.Errorf("argv = %q, want it to start with `context inspect`", argv)
	}

	mod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mod), "github.com/docker/cli") {
		t.Error("go.mod requires github.com/docker/cli; SPEC002 forbids it")
	}

	// Nothing under internal/ or cmd/ reads the context store directly.
	for _, root := range []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "cmd")} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), "\"github.com/docker/cli") {
				t.Errorf("%s imports github.com/docker/cli", p)
			}
			for _, line := range strings.Split(string(b), "\n") {
				code := strings.TrimSpace(line)
				if strings.HasPrefix(code, "//") {
					continue
				}
				if strings.Contains(code, `"meta.json"`) || strings.Contains(code, "contexts/meta") {
					t.Errorf("%s parses the docker context store: %s", p, code)
				}
			}
			return nil
		})
	}
}

// nsctl neither adds nor strips DOCKER_HOST for the docker commands it runs.
//
// Requirements: HMD_CLI_NEURONSPHERE_NERD021_SPEC010
func TestDockerChildInheritsEnvWithoutInjectedDOCKERHOST(t *testing.T) {
	d := fakeDocker(t)

	prev, had := os.LookupEnv("DOCKER_HOST")
	os.Unsetenv("DOCKER_HOST")
	t.Cleanup(func() {
		if had {
			os.Setenv("DOCKER_HOST", prev)
		} else {
			os.Unsetenv("DOCKER_HOST")
		}
	})

	out, err := d.ContextEndpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "|<unset>") {
		t.Errorf("child saw DOCKER_HOST with none in the parent: %q", out)
	}

	t.Setenv("DOCKER_HOST", "ssh://user@elsewhere")
	out, err = d.ContextEndpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "|ssh://user@elsewhere") {
		t.Errorf("child did not inherit the user's DOCKER_HOST unchanged: %q", out)
	}

	// No non-test source sets DOCKER_HOST anywhere nsctl builds an environment.
	for _, root := range []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "cmd")} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, _ := os.ReadFile(p)
			s := string(b)
			if strings.Contains(s, `"DOCKER_HOST=`) || strings.Contains(s, `Setenv("DOCKER_HOST"`) {
				t.Errorf("%s injects DOCKER_HOST", p)
			}
			return nil
		})
	}
}
