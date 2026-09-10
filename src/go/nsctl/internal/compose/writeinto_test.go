package compose

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The value must travel on standard input, never in argv: argv is visible in
// any process listing inside the container.
func TestWriteIntoSendsTheValueOnStdin(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	err := newRunner(f).WriteInto(context.Background(), "c1", "/run/ns-secrets/upstream", []byte("s3cr3t"))
	if err != nil {
		t.Fatalf("WriteInto: %v", err)
	}

	e := f.execs["c1|/run/ns-secrets/upstream"]
	if e == nil {
		t.Fatalf("no exec was created; got %v", execKeys(f))
	}
	if string(e.written) != "s3cr3t" {
		t.Errorf("stdin = %q, want the value", e.written)
	}
	if e.path != "/run/ns-secrets/upstream" {
		t.Errorf("path = %q", e.path)
	}
}

// docker cp cannot reach a tmpfs mounted inside a container: it resolves the
// destination in the rootfs the daemon sees, so the write lands on disk under
// the mount. An exec runs in the container's own mount namespace instead. This
// pins the mechanism, because switching back would be silent -- the copy
// succeeds and the file is simply invisible.
func TestWriteIntoUsesAnExecNotACopy(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	if err := newRunner(f).WriteInto(context.Background(), "c1", "/run/ns-secrets/x", []byte("v")); err != nil {
		t.Fatalf("WriteInto: %v", err)
	}
	if len(f.execs) != 1 {
		t.Fatalf("execs = %v, want exactly one", execKeys(f))
	}
}

func TestWriteIntoReportsANonZeroExit(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.execExit = 1
	err := newRunner(f).WriteInto(context.Background(), "c1", "/run/ns-secrets/x", []byte("v"))
	if err == nil {
		t.Fatal("a failed write reported success")
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("error %q does not carry the exit code", err)
	}
}

// A container with no shell cannot be written to this way, and has to say so
// rather than appear to have been given a credential.
func TestWriteIntoReportsAnUncreatableExec(t *testing.T) {
	t.Parallel()

	f := newFakeAPI()
	f.execErr = errors.New("no such file or directory: /bin/sh")
	err := newRunner(f).WriteInto(context.Background(), "c1", "/run/ns-secrets/x", []byte("v"))
	if err == nil {
		t.Fatal("a container with no shell reported success")
	}
	if !strings.Contains(err.Error(), "/bin/sh") {
		t.Errorf("error %q does not name the cause", err)
	}
}

func execKeys(f *fakeAPI) []string {
	out := make([]string, 0, len(f.execs))
	for k := range f.execs {
		out = append(out, k)
	}
	return out
}
