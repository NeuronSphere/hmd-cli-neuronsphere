package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockercreds"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockerhost"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
)

func TestNoRegistryLoginWarnsWithTheCommand(t *testing.T) {
	t.Parallel()

	none := func(context.Context, string) (dockercreds.Credential, bool) { return dockercreds.Credential{}, false }
	checks := registryLoginCheck(context.Background(), none)
	if len(checks) != 1 || checks[0].Status != doctor.StatusWarn {
		t.Fatalf("want one warning, got %+v", checks)
	}
	if !strings.Contains(checks[0].Remedy, "docker login ghcr.io") {
		t.Errorf("remedy does not name the command: %q", checks[0].Remedy)
	}
}

func TestARegistryLoginIsOKAndNeverShown(t *testing.T) {
	t.Parallel()

	some := func(context.Context, string) (dockercreds.Credential, bool) {
		return dockercreds.Credential{Username: "u", Secret: "ghp_topsecret"}, true
	}
	checks := registryLoginCheck(context.Background(), some)
	if len(checks) != 1 || checks[0].Status != doctor.StatusOK {
		t.Fatalf("want one OK row, got %+v", checks)
	}
	if strings.Contains(checks[0].Detail+checks[0].Remedy, "ghp_topsecret") {
		t.Error("the row prints the secret")
	}
}

// Colima's default forwarder carries no UDP; the row says so before anything
// depends on the resolver, and an engine that is not Colima gets no row.
func TestForwarderChecks(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	profile := home + "/.colima/default"
	if err := mkdirWrite(profile+"/colima.yaml", "cpu: 8\n"); err != nil {
		t.Fatal(err)
	}
	ep := dockerhost.Endpoint{Host: "unix://" + profile + "/docker.sock"}
	checks := ForwarderChecks(ep)
	if len(checks) != 1 || checks[0].Status != doctor.StatusWarn || !strings.Contains(checks[0].Remedy, "portForwarder: grpc") {
		t.Fatalf("default forwarder: got %+v", checks)
	}

	if err := mkdirWrite(profile+"/colima.yaml", "portForwarder: grpc\n"); err != nil {
		t.Fatal(err)
	}
	if checks := ForwarderChecks(ep); len(checks) != 1 || checks[0].Status != doctor.StatusOK {
		t.Fatalf("grpc forwarder: got %+v", checks)
	}

	if checks := ForwarderChecks(dockerhost.Endpoint{Host: "unix:///var/run/docker.sock"}); len(checks) != 0 {
		t.Errorf("not Colima: got %+v", checks)
	}
}

func mkdirWrite(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
