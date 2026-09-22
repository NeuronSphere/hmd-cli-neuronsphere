package dockerhost

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/docker/docker/client"
)

// Resolution and the connection built from it, against whatever engine this
// machine actually runs. No unit test can stand in for it: the whole defect
// was that the endpoint nsctl computed and the one the docker CLI uses were
// different, and only a real context store can show that they agree.
//
// Skipped unless NSCTL_LIVE is set, and worth running under every runtime --
// it is the check that says "nsctl works here" in one command.
func TestLiveEndpointIsReachable(t *testing.T) {
	if os.Getenv("NSCTL_LIVE") == "" {
		t.Skip("set NSCTL_LIVE=1 with a running container engine to run this")
	}

	ep, err := (&Resolver{Inspect: CLIInspector(nil)}).Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolving the endpoint: %v", err)
	}
	t.Logf("endpoint: %s", ep.Describe())

	opts, err := ep.ClientOpts(nil)
	if err != nil {
		t.Fatalf("ClientOpts: %v", err)
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		t.Fatalf("connecting to %s: %v", ep.Describe(), err)
	}
	defer cli.Close()

	d, err := Probe(context.Background(), cli)
	if err != nil {
		t.Fatalf("reaching %s: %v", ep.Describe(), err)
	}
	if d.ServerVersion == "" {
		t.Error("the engine reported no version")
	}
	t.Logf("engine: %s %s (%s), %s, rootless=%v, vm-backed=%v",
		d.OperatingSystem, d.ServerVersion, d.OSType, d.DescribeCapacity(),
		d.Rootless, d.VMBacked(runtime.GOOS))
}
