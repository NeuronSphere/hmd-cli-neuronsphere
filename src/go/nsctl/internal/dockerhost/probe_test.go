package dockerhost

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/system"
)

type fakeInfo struct {
	info system.Info
	err  error
}

func (f fakeInfo) Info(context.Context) (system.Info, error) { return f.info, f.err }

func TestProbeReadsCapacityAndRootless(t *testing.T) {
	api := fakeInfo{info: system.Info{
		ServerVersion:   "27.3.1",
		OSType:          "linux",
		OperatingSystem: "Alpine Linux v3.20",
		NCPU:            2,
		MemTotal:        2 << 30,
		SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless"},
	}}
	d, err := Probe(context.Background(), api)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if d.NCPU != 2 || d.MemTotal != 2<<30 {
		t.Errorf("capacity = %d/%d", d.NCPU, d.MemTotal)
	}
	if !d.Rootless {
		t.Error("want Rootless derived from SecurityOptions")
	}
	if got := d.DescribeCapacity(); got != "2 CPUs and 2.0 GiB of memory" {
		t.Errorf("DescribeCapacity() = %q", got)
	}
}

func TestProbeNotRootlessByDefault(t *testing.T) {
	d, err := Probe(context.Background(), fakeInfo{info: system.Info{
		OSType:          "linux",
		SecurityOptions: []string{"name=seccomp,profile=builtin"},
	}})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if d.Rootless {
		t.Error("seccomp alone must not read as rootless")
	}
}

func TestProbeSurfacesFailure(t *testing.T) {
	if _, err := Probe(context.Background(), fakeInfo{err: errors.New("no daemon")}); err == nil {
		t.Fatal("want the daemon failure surfaced, not swallowed")
	}
}

// The rule is "a different operating system", never a runtime name.
func TestVMBackedIsOSComparison(t *testing.T) {
	cases := []struct {
		osType, goos string
		want         bool
	}{
		{"linux", "darwin", true},  // Docker Desktop, Colima, Rancher, OrbStack
		{"linux", "windows", true}, // Docker Desktop on Windows, WSL2
		{"linux", "linux", false},  // a native daemon
		{"", "darwin", false},      // unknown: do not guess
	}
	for _, c := range cases {
		d := Daemon{OSType: c.osType}
		if got := d.VMBacked(c.goos); got != c.want {
			t.Errorf("VMBacked(%q on %q) = %v, want %v", c.osType, c.goos, got, c.want)
		}
	}
}
