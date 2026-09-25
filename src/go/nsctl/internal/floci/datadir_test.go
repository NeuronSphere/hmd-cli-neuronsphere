package floci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeExecer struct {
	out  string
	err  error
	args []string
}

func (f *fakeExecer) Exec(_ context.Context, name string, args ...string) ([]byte, error) {
	f.args = append([]string{name}, args...)
	return []byte(f.out), f.err
}

func TestWriteSentinelStampsTheDirectoryAndReturnsTheToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	token, err := WriteSentinel(dir)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, SentinelName))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != token {
		t.Errorf("the file holds %q but the token is %q", body, token)
	}
}

// Fresh on every start, not stable. A stable token would still be sitting in
// the orphaned directory a stale container is holding, and would match -- which
// is the one answer that must never be reachable by accident.
func TestWriteSentinelNeverRepeatsAToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, err := WriteSentinel(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WriteSentinel(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two starts stamped the same token")
	}
	body, _ := os.ReadFile(filepath.Join(dir, SentinelName))
	if string(body) != second {
		t.Errorf("the second stamp did not replace the first: %q", body)
	}
}

// The name has to be one Floci's own loader and PruneAPIGatewayGhosts both
// ignore, or the probe becomes a record Floci tries to rehydrate.
func TestTheSentinelIsNotSomethingFlociReads(t *testing.T) {
	t.Parallel()

	if !strings.HasPrefix(SentinelName, ".") {
		t.Errorf("%q is not hidden", SentinelName)
	}
	if strings.HasSuffix(SentinelName, ".s3data") || strings.HasPrefix(SentinelName, "apigateway-") {
		t.Errorf("%q collides with a store Floci loads", SentinelName)
	}
	if matched, _ := filepath.Match(apigwStoreGlob, SentinelName); matched {
		t.Errorf("%q is matched by the API Gateway store's glob", SentinelName)
	}
}

func TestSentinelMatchesReadsTheTokenBackFromTheContainer(t *testing.T) {
	t.Parallel()

	f := &fakeExecer{out: "abc123\n"}
	ok, err := SentinelMatches(context.Background(), f, "floci", "abc123")
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v; want true, nil", ok, err)
	}
	// Read from inside the container, at the path Floci was configured with.
	if got := strings.Join(f.args, " "); !strings.Contains(got, "/app/data/"+SentinelName) {
		t.Errorf("exec ran %q", got)
	}
}

// The reported failure: the container answers, from a directory that is not
// the one the host just wrote to.
func TestSentinelMatchesIsFalseWhenTheContainerSeesAnotherDirectory(t *testing.T) {
	t.Parallel()

	ok, err := SentinelMatches(context.Background(), &fakeExecer{out: "an older token"}, "floci", "abc123")
	if err != nil {
		t.Fatalf("a successful read must not error: %v", err)
	}
	if ok {
		t.Error("a stale directory was reported as current")
	}
}

// Conservative by construction. Recreating a Floci that was working costs the
// databases it spawned, so a probe that could not reach an answer must report
// "matches" and let the error decide what the caller says.
func TestSentinelMatchesRefusesToGuessWhenTheProbeFails(t *testing.T) {
	t.Parallel()

	ok, err := SentinelMatches(context.Background(), &fakeExecer{err: errors.New("no such container")}, "floci", "abc123")
	if err == nil {
		t.Fatal("a failed probe must report why")
	}
	if !ok {
		t.Error("a failed probe was reported as a definite mismatch")
	}
}

// The shape the failure actually takes, confirmed against a real engine: a host
// directory deleted and remade under a running container leaves it holding the
// empty, unlinked one, so the probe finds no file rather than an old token.
//
// This is why the probe exits 0 by itself. Exec reports any non-zero exit as an
// error, so a bare `cat` would have made this indistinguishable from an
// unreachable container -- and the conservative branch would then have refused
// to repair the one case the check exists for.
func TestSentinelMatchesIsFalseWhenTheFileIsNotThereAtAll(t *testing.T) {
	t.Parallel()

	f := &fakeExecer{out: ""}
	ok, err := SentinelMatches(context.Background(), f, "floci", "abc123")
	if err != nil {
		t.Fatalf("a missing file is an answer, not a probe failure: %v", err)
	}
	if ok {
		t.Error("an orphaned directory was reported as current")
	}
	// The command must survive the file being absent, or the exit code turns
	// the answer back into an error.
	if got := strings.Join(f.args, " "); !strings.Contains(got, "exit 0") {
		t.Errorf("the probe can fail on a missing file: %q", got)
	}
}
