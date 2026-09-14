package browser

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestOpenPassesTheURLToThePlatformOpener(t *testing.T) {
	t.Parallel()

	var gotName string
	var gotArgs []string
	err := Open(context.Background(), "https://example.test/device?user_code=BCDF-GHJK",
		func(_ context.Context, name string, args ...string) error {
			gotName, gotArgs = name, args
			return nil
		})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	want := map[string]string{"darwin": "open", "windows": "rundll32"}[runtime.GOOS]
	if want == "" {
		want = "xdg-open"
	}
	if gotName != want {
		t.Errorf("opener = %q, want %q on %s", gotName, want, runtime.GOOS)
	}
	// However the platform spells it, the URL has to be the last argument --
	// that is the contract the login command's own test relies on.
	if len(gotArgs) == 0 || !strings.Contains(gotArgs[len(gotArgs)-1], "user_code=BCDF-GHJK") {
		t.Errorf("args = %v, want the URL last", gotArgs)
	}
}

// The browser is a convenience: a failure must be reportable, so the caller can
// downgrade it to a note rather than having it swallowed here.
func TestOpenReportsAFailingOpener(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("no display")
	err := Open(context.Background(), "https://example.test",
		func(context.Context, string, ...string) error { return sentinel })
	if err == nil {
		t.Fatal("Open() hid the failure")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("Open() error = %v, want it to wrap the opener's", err)
	}
	if !strings.Contains(err.Error(), "opening a browser") {
		t.Errorf("Open() error = %v, want it to say what was attempted", err)
	}
}

func TestCommandNamesTheURLOnEveryPlatform(t *testing.T) {
	t.Parallel()

	name, args := command("https://example.test")
	if name == "" {
		t.Fatalf("no opener for %s", runtime.GOOS)
	}
	if len(args) == 0 || args[len(args)-1] != "https://example.test" {
		t.Errorf("command() = %q %v, want the URL last", name, args)
	}
}
