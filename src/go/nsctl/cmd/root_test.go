package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/spf13/cobra"
)

// fakeEnv builds a Lookup over a map. Every test here runs in parallel, which
// t.Setenv forbids -- injecting the environment is what makes that possible.
func fakeEnv(vars map[string]string) hmdenv.Lookup {
	return func(key string) string { return vars[key] }
}

// run executes the command tree with args and returns stdout, stderr and the error.
func run(t *testing.T, env hmdenv.Lookup, args ...string) (string, string, error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	root := NewRootCommand("9.9.9", env)
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

// noControlPlane points the ms-deployment probe at a closed port, so a version
// test asserts on the binary line alone and never reaches whatever platform
// happens to be running on the machine executing it.
func noControlPlane() hmdenv.Lookup {
	return fakeEnv(map[string]string{"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1"})
}

func TestVersionPrintsTheInjectedVersion(t *testing.T) {
	t.Parallel()

	out, _, err := run(t, noControlPlane(), "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if got := strings.TrimSpace(out); got != "nsctl 9.9.9" {
		t.Errorf("version printed %q, want %q", got, "nsctl 9.9.9")
	}
}

// SPEC013: the second line appears only when a control plane answers, and it
// names the service version rather than the binary's.
func TestVersionReportsMSDeploymentWhenReachable(t *testing.T) {
	t.Parallel()

	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"openapi":"3.1.0","info":{"title":"ms-deployment","version":"0.4"}}`)
	}))
	defer service.Close()

	out, _, err := run(t, fakeEnv(map[string]string{"HMD_LOCAL_MS_DEPLOYMENT_URL": service.URL}), "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "nsctl 9.9.9") {
		t.Errorf("version printed %q, want the binary version", out)
	}
	if !strings.Contains(out, "hmd-ms-deployment-core 0.4 ("+service.URL+")") {
		t.Errorf("version printed %q, want the ms-deployment line", out)
	}
}

func TestVersionFlagUsesTheSameTemplate(t *testing.T) {
	t.Parallel()

	out, _, err := run(t, noControlPlane(), "--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if got := strings.TrimSpace(out); got != "nsctl 9.9.9" {
		t.Errorf("--version printed %q, want %q", got, "nsctl 9.9.9")
	}
}

func TestVersionRunsWithoutAnHmdHome(t *testing.T) {
	t.Parallel()

	// A missing HMD_HOME must not stop the one command you run to check the
	// install.
	if _, _, err := run(t, noControlPlane(), "version"); err != nil {
		t.Errorf("version without HMD_HOME: %v", err)
	}
}

func TestUnknownFlagIsAUsageError(t *testing.T) {
	t.Parallel()

	_, _, err := run(t, fakeEnv(nil), "--nope")
	if err == nil {
		t.Fatal("--nope succeeded, want an error")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
	}
}

func TestUnknownArgumentIsAUsageError(t *testing.T) {
	t.Parallel()

	_, _, err := run(t, fakeEnv(nil), "definitely-not-a-command")
	if err == nil {
		t.Fatal("unknown command succeeded, want an error")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
	}
}

func TestHelpListsEveryCommandAndPersistentFlag(t *testing.T) {
	t.Parallel()

	out, _, err := run(t, fakeEnv(nil), "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"version", "--home"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help output does not mention %q:\n%s", want, out)
		}
	}
}

func TestResolveHomePrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		homeFlag string
		env      map[string]string
		want     string
	}{
		{"neither is set", "", nil, ""},
		{"the environment supplies it", "", map[string]string{"HMD_HOME": "/from/env"}, "/from/env"},
		{"the flag supplies it", "/from/flag", nil, "/from/flag"},
		{"the flag beats the environment", "/from/flag", map[string]string{"HMD_HOME": "/from/env"}, "/from/flag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := &Options{}
			opts.resolve(tt.homeFlag, fakeEnv(tt.env), func(string) {})
			if opts.Home != tt.want {
				t.Errorf("Home = %q, want %q", opts.Home, tt.want)
			}
		})
	}
}

func TestRequireHomeRefusesWhenUnset(t *testing.T) {
	t.Parallel()

	opts := &Options{}
	_, err := opts.RequireHome()
	if err == nil {
		t.Fatal("RequireHome succeeded with no home, want an error")
	}
	if got := nserr.CodeOf(err); got != nserr.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, nserr.Usage)
	}
	// The refusal has to name both ways to fix it.
	for _, want := range []string{"HMD_HOME", "--home"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestResolveLayersHmdEnvUnderTheProcessEnvironment(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := hmdenv.Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("HMD_DID=from-file\nHMD_REGION=us-west-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := &Options{}
	opts.resolve("", fakeEnv(map[string]string{"HMD_HOME": home, "HMD_DID": "from-shell"}), func(string) {})

	if got := opts.Lookup("HMD_DID"); got != "from-shell" {
		t.Errorf("HMD_DID = %q, want the process value", got)
	}
	if got := opts.Lookup("HMD_REGION"); got != "us-west-2" {
		t.Errorf("HMD_REGION = %q, want the file value", got)
	}
}

func TestResolveWarnsButContinuesOnAnUnreadableHmdEnv(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := hmdenv.Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("NOT_AN_ASSIGNMENT\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var warnings []string
	opts := &Options{}
	opts.resolve("", fakeEnv(map[string]string{"HMD_HOME": home}), func(msg string) {
		warnings = append(warnings, msg)
	})

	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
	// One bad line must not take the whole CLI down.
	if opts.Home != home {
		t.Errorf("Home = %q, want %q", opts.Home, home)
	}
	if opts.Lookup == nil {
		t.Fatal("Lookup is nil after a failed load")
	}
	if got := opts.Lookup("HMD_HOME"); got != home {
		t.Errorf("Lookup still answers from the process environment: got %q", got)
	}
}

func TestNoArgsAcceptsNone(t *testing.T) {
	t.Parallel()

	if err := noArgs(&cobra.Command{Use: "nsctl"}, nil); err != nil {
		t.Errorf("noArgs(nil) = %v, want nil", err)
	}
}

func TestErrorsArePrintedOnceByExecuteNotByRunE(t *testing.T) {
	t.Parallel()

	// A runtime failure must leave stdout empty and print no usage text; the
	// single "Error:" line is Execute's job. This asserts the RunE half --
	// nothing written, error returned.
	var out, errBuf bytes.Buffer
	root := NewRootCommand("9.9.9", fakeEnv(nil))
	sentinel := errors.New("boom")
	root.AddCommand(&cobra.Command{
		Use:           "explode",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(*cobra.Command, []string) error { return sentinel },
	})
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"explode"})

	err := root.Execute()
	if !errors.Is(err, sentinel) {
		t.Fatalf("Execute() = %v, want the sentinel", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	if strings.Contains(errBuf.String(), "Usage:") {
		t.Errorf("stderr contains usage text after a runtime error:\n%s", errBuf.String())
	}
}
