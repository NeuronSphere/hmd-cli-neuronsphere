package floci

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func canaryProvisioner(s3api s3API) *Provisioner {
	return &Provisioner{Target: Target{AccountID: "000000000001", Region: "us-west-2", Endpoint: "http://neuronsphere:4566"}, S3: s3api}
}

func TestCheckStorageRoundTripsAndCleansUpAfterItself(t *testing.T) {
	t.Parallel()

	f := &fakeS3{}
	if err := canaryProvisioner(f).CheckStorage(context.Background(), "hmd.000000000001.reg1.tfstate"); err != nil {
		t.Fatalf("a working store must pass: %v", err)
	}
	if f.getCalls != 1 {
		t.Errorf("read the object %d times, want once", f.getCalls)
	}
	// Left behind, the probe is an object someone inspecting the bucket by
	// hand has to explain.
	if len(f.deleted) != 1 || f.deleted[0] != storageCanaryKey {
		t.Errorf("deleted %v, want just %q", f.deleted, storageCanaryKey)
	}
	// Under the prefix hmd-lib-cdktf's S3Backend uses, so the probe exercises
	// the path a deploy takes rather than a neighbouring one.
	if !strings.HasPrefix(storageCanaryKey, "hmd/") {
		t.Errorf("the probe key %q is not under the state prefix", storageCanaryKey)
	}
}

// The reported failure: Floci answers, and returns 500 on the object. The error
// has to carry the cause, because the reader has just watched the control plane
// start without complaint.
func TestCheckStorageExplainsAFlociThatAnswersButCannotRead(t *testing.T) {
	t.Parallel()

	f := &fakeS3{}
	f.getErr = apiErr{"InternalError"}
	err := canaryProvisioner(f).CheckStorage(context.Background(), "hmd.000000000001.reg1.tfstate")
	if err == nil {
		t.Fatal("a store that cannot read back must fail the start")
	}
	for _, want := range []string{
		"hmd.000000000001.reg1.tfstate", // which bucket
		"http://neuronsphere:4566",      // which endpoint
		"$HMD_HOME/floci/data",          // where to look
		"Do not delete $HMD_HOME",       // and what not to do about it
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q:\n%v", want, err)
		}
	}
}

// 200 and the wrong bytes is a worse failure than an error, and it is the shape
// a half-written directory takes -- so the bytes are compared, not the status.
func TestCheckStorageRejectsAnObjectThatComesBackWrong(t *testing.T) {
	t.Parallel()

	f := &fakeS3{}
	f.corrupt = []byte("not what was written")
	if err := canaryProvisioner(f).CheckStorage(context.Background(), "b"); err == nil {
		t.Fatal("a store that returns the wrong bytes must fail the start")
	}
}

func TestCheckStorageFailsWhenItCannotWrite(t *testing.T) {
	t.Parallel()

	f := &fakeS3{}
	f.putErr = errors.New("no space left on device")
	err := canaryProvisioner(f).CheckStorage(context.Background(), "b")
	if err == nil {
		t.Fatal("a store that cannot be written to must fail the start")
	}
	if !strings.Contains(err.Error(), "no space left on device") {
		t.Errorf("the error drops the cause: %v", err)
	}
	if f.getCalls != 0 {
		t.Error("read back an object that was never written")
	}
}
