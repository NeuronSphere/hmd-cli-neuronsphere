// Package browser opens a URL in the user's browser, best-effort.
//
// Best-effort is the whole contract. In a device authorization flow the URL and
// the code are printed to the terminal first and the browser is a convenience;
// a session over SSH, in a container, or on a headless machine has no browser
// and must still be able to sign in. So a failure here is worth a note and
// never worth an error.
//
// Shelling out rather than taking a dependency: the platform commands are one
// line each, and internal/container already shells out to `docker` for the
// same reason.
package browser

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// ErrUnsupported means this platform has no known way to open a URL.
var ErrUnsupported = fmt.Errorf("no way to open a browser on this platform")

// Runner executes a command. Replaceable so tests need not open a browser.
type Runner func(ctx context.Context, name string, args ...string) error

// Open opens url. A nil run uses the real one.
func Open(ctx context.Context, url string, run Runner) error {
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		}
	}
	name, args := command(url)
	if name == "" {
		return ErrUnsupported
	}
	if err := run(ctx, name, args...); err != nil {
		return fmt.Errorf("opening a browser with %s: %w", name, err)
	}
	return nil
}

// command is the platform's URL opener.
func command(url string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{url}
	case "windows":
		// The empty string is rundll32's title argument; without it a URL
		// containing a space is taken as the window title.
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}
