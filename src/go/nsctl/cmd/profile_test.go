package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// writeConfig puts an nsctl.toml in a home and returns the home.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nsconfig.Name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestProfileSuppliesTheLibrarianEndpoint is the point of the whole phase: a
// verb that reaches a cloud librarian can be told which tenant by name, without
// exporting anything.
func TestProfileSuppliesTheLibrarianEndpoint(t *testing.T) {
	t.Parallel()

	f := trinoLibrarian(t)
	home := writeConfig(t, ""+
		"[profile.acme]\n"+
		"auth_url = \"https://auth.example/oauth2/ns\"\n"+
		"artifact_librarian_url = \""+f.URL+"\"\n")

	out, _, err := run(t, fakeEnv(map[string]string{librarian.APIKeyEnv: "k"}),
		"artifact", "versions", "--home", home, "--profile", "acme", "hmd-inf-trino")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if !strings.Contains(out, "0.2.5") {
		t.Errorf("the profile's librarian was not reached:\n%s", out)
	}
}

// TestProfileComposesFromCustomerAndRegion proves the tier that makes a profile
// worth writing: two short keys, and both services follow.
func TestProfileComposesFromCustomerAndRegion(t *testing.T) {
	t.Parallel()

	home := writeConfig(t, ""+
		"[profile.acme]\n"+
		"auth_url = \"https://auth.example/oauth2/ns\"\n"+
		"customer_code = \"acme\"\n"+
		"region = \"reg1\"\n")

	// No librarian answers there, so this asserts on the address in the error
	// rather than on a result -- which is the thing under test.
	_, _, err := run(t, fakeEnv(map[string]string{librarian.APIKeyEnv: "k"}),
		"artifact", "versions", "--home", home, "--profile", "acme", "hmd-inf-trino")
	if err == nil {
		t.Fatal("versions succeeded against a composed hostname that does not exist")
	}
	if !strings.Contains(err.Error(), "artifact-aaa-reg1.acme-admin-neuronsphere.io") {
		t.Errorf("the composed hostname is not what was reached:\n%v", err)
	}
}

// TestShadowedProfileEndpointIsReported is SPEC001's guard. The environment
// still wins; what must not happen is for it to win in silence when somebody
// asked for a tenant by name.
func TestShadowedProfileEndpointIsReported(t *testing.T) {
	t.Parallel()

	f := trinoLibrarian(t)
	home := writeConfig(t, ""+
		"[profile.acme]\n"+
		"auth_url = \"https://auth.example/oauth2/ns\"\n"+
		"artifact_librarian_url = \"https://lib.acme.example\"\n")

	env := fakeEnv(map[string]string{
		librarian.APIKeyEnv:              "k",
		nsconfig.ArtifactLibrarianURLEnv: f.URL,
	})

	_, errOut, err := run(t, env,
		"artifact", "versions", "--home", home, "--profile", "acme", "hmd-inf-trino")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	for _, want := range []string{"lib.acme.example", nsconfig.ArtifactLibrarianURLEnv, f.URL} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the shadowing warning does not name %q:\n%s", want, errOut)
		}
	}

	// Without --profile there is nothing to be surprised by: nobody named a
	// tenant, so the environment is simply the configuration.
	_, quiet, err := run(t, env, "artifact", "versions", "--home", home, "hmd-inf-trino")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if strings.Contains(quiet, "warning:") {
		t.Errorf("warned about a shadowing nobody could be surprised by:\n%s", quiet)
	}
}

// TestNamedProfileThatDoesNotExistIsRefused separates the two ways a profile can
// be absent. Asking for a tenant by name and silently getting another one is
// the failure this whole mechanism exists to prevent.
func TestNamedProfileThatDoesNotExistIsRefused(t *testing.T) {
	t.Parallel()

	f := trinoLibrarian(t)
	env := fakeEnv(map[string]string{librarian.APIKeyEnv: "k"})

	home := writeConfig(t, "[profile.acme]\nauth_url = \"https://auth.example/oauth2/ns\"\n")
	_, _, err := run(t, env, "artifact", "versions", "--home", home,
		"--profile", "other", "--url", f.URL, "hmd-inf-trino")
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Fatalf("error = %v, want one naming the profile that is not there", err)
	}
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("exit code = %d, want Usage for a profile that does not exist", nserr.CodeOf(err))
	}

	// And with no configuration file at all, the refusal says so and shows
	// what to write rather than reporting a missing profile.
	_, _, err = run(t, env, "artifact", "versions", "--home", t.TempDir(),
		"--profile", "acme", "--url", f.URL, "hmd-inf-trino")
	if err == nil || !strings.Contains(err.Error(), "customer_code") {
		t.Fatalf("error = %v, want one showing the profile table to write", err)
	}
}

// TestNoProfileStillWorks is the compatibility guarantee: every existing
// install configures these verbs with environment variables and nothing else,
// and must keep doing so.
func TestNoProfileStillWorks(t *testing.T) {
	t.Parallel()

	f := trinoLibrarian(t)
	_, _, err := run(t, fakeEnv(map[string]string{
		librarian.APIKeyEnv:              "k",
		nsconfig.ArtifactLibrarianURLEnv: f.URL,
	}), "artifact", "versions", "--home", t.TempDir(), "hmd-inf-trino")
	if err != nil {
		t.Fatalf("versions without a profile: %v", err)
	}
}
