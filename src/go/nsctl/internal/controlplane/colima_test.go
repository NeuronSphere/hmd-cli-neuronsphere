package controlplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func colimaHome(t *testing.T, yaml string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".colima", "default")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(dir, "colima.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return "unix://" + filepath.Join(dir, "docker.sock")
}

func TestTheDefaultColimaForwarderIsNamedAsTheCause(t *testing.T) {
	t.Parallel()
	why := ColimaUDPUnreachable(colimaHome(t, "portForwarder: ssh\ncpus: 8\n"))
	if why == "" {
		t.Fatal("the ssh forwarder was not reported")
	}
	for _, want := range []string{"portForwarder: grpc", "colima stop && colima start", "UDP"} {
		if !strings.Contains(why, want) {
			t.Errorf("message is missing %q: %s", want, why)
		}
	}
}

// Colima's default is ssh, so an absent or unreadable config is the broken case,
// not the healthy one. Treating "no config" as "fine" would stay silent on a
// stock install, which is every install that hits this.
func TestAnAbsentColimaConfigIsTreatedAsTheDefault(t *testing.T) {
	t.Parallel()
	if ColimaUDPUnreachable(colimaHome(t, "")) == "" {
		t.Error("a Colima engine with no config read as healthy")
	}
}

func TestTheGrpcForwarderIsSilent(t *testing.T) {
	t.Parallel()
	if why := ColimaUDPUnreachable(colimaHome(t, "portForwarder: grpc\n")); why != "" {
		t.Errorf("grpc reported a problem: %s", why)
	}
}

func TestANonColimaEngineIsSilent(t *testing.T) {
	t.Parallel()
	for _, host := range []string{
		"unix:///var/run/docker.sock",
		"unix:///Users/you/.docker/run/docker.sock",
		"tcp://127.0.0.1:2375",
		"",
	} {
		if why := ColimaUDPUnreachable(host); why != "" {
			t.Errorf("%s reported a Colima problem: %s", host, why)
		}
	}
}

// The profile directory is under the user's home; a path sliced at the marker
// would name a directory that exists for nobody.
func TestTheProfileDirectoryKeepsItsFullPath(t *testing.T) {
	t.Parallel()
	got := colimaProfileDir("unix:///Users/you/.colima/default/docker.sock")
	if want := "/Users/you/.colima/default"; got != want {
		t.Errorf("profile dir = %q, want %q", got, want)
	}
}
