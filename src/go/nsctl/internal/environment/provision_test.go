package environment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

type fakeProvisioner struct {
	secretErr  error
	bucketErr  error
	storageErr error
	checked    string
}

func (f *fakeProvisioner) AdminDBSecret(context.Context, floci.Names, string) error {
	return f.secretErr
}
func (f *fakeProvisioner) TFStateBucket(region string) string {
	return "hmd.000000000001." + region + ".tfstate"
}
func (f *fakeProvisioner) EnsureBucket(context.Context, string) error { return f.bucketErr }
func (f *fakeProvisioner) CheckStorage(_ context.Context, bucket string) error {
	f.checked = bucket
	return f.storageErr
}

func provision(t *testing.T, f *fakeProvisioner) (error, []string) {
	t.Helper()
	var warnings []string
	err := provisionEnvironment(context.Background(), f, floci.Names{Region: "reg1"}, "hmd_db",
		func(format string, a ...any) { warnings = append(warnings, format) })
	return err, warnings
}

// The admin secret is needed by a database-account deploy and by nothing else,
// so a substrate that deploys no database starts fine without it. Refusing
// there would forbid a state that is not broken.
func TestASecretFailureWarnsAndTheStartContinues(t *testing.T) {
	t.Parallel()

	err, warnings := provision(t, &fakeProvisioner{secretErr: errors.New("secrets manager said no")})
	if err != nil {
		t.Fatalf("a secret failure must not stop the start: %v", err)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want exactly one", warnings)
	}
}

// The state bucket is the other kind. EnsureBucket tolerates one that already
// exists, so it fails only when Floci cannot serve S3 at all -- and every node
// Apply runs reaches `tofu init`, which can do nothing without it. This used to
// warn and carry on, which is how the failure came to be reported hundreds of
// log lines later as a Terraform problem.
func TestABucketFailureStopsTheStart(t *testing.T) {
	t.Parallel()

	err, _ := provision(t, &fakeProvisioner{bucketErr: errors.New("connection refused")})
	if err == nil {
		t.Fatal("a state bucket that cannot be created must stop the start")
	}
	if got := nserr.CodeOf(err); got != nserr.Fail {
		t.Errorf("code = %v, want %v", got, nserr.Fail)
	}
}

// And the case the whole change is about: a Floci that answers every
// control-plane call from memory while its disk is unreachable.
func TestAStorageFailureStopsTheStartAndProbesTheStateBucket(t *testing.T) {
	t.Parallel()

	f := &fakeProvisioner{storageErr: errors.New("api error InternalError")}
	err, _ := provision(t, f)
	if err == nil {
		t.Fatal("a Floci that cannot read its own storage must stop the start")
	}
	if !strings.Contains(err.Error(), "InternalError") {
		t.Errorf("the error drops the cause: %v", err)
	}
	// The bucket probed must be the one `tofu init` will refresh from, not a
	// scratch bucket that proves nothing about it.
	if want := f.TFStateBucket("reg1"); f.checked != want {
		t.Errorf("probed %q, want %q", f.checked, want)
	}
}

func TestAWorkingProvisionIsSilent(t *testing.T) {
	t.Parallel()

	err, warnings := provision(t, &fakeProvisioner{})
	if err != nil || len(warnings) != 0 {
		t.Errorf("err = %v, warnings = %v; want neither", err, warnings)
	}
}
