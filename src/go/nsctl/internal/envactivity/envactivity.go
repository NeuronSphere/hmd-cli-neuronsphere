// Package envactivity records which environments some nsctl process is
// starting or applying, so one start does not undo another's.
//
// Starting anything wakes Floci, and Floci restarts every environment it has
// persisted. environment.Start and controlplane.Start therefore snapshot what
// was running beforehand and stop whatever came up in between
// (floci.StopWoken). That is right when nothing else is going on, and wrong
// when a second session is starting a second environment in the same window:
// its containers appear after the snapshot and get stopped as "woken".
//
// Begin marks an environment active for the duration of a start or apply,
// with a shared host lock that the kernel drops if the process dies, and
// stamps the time at both ends. Active asks "is it being worked on now, or
// was it since this moment" -- the second half covers a start that began and
// finished entirely inside the sweeping caller's window.
package envactivity

import (
	"os"
	"path/filepath"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hostlock"
)

func lockName(slug string) string { return "env-" + slug }

func stampPath(home, slug string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "activity", slug)
}

// Begin marks slug active until the returned func is called. The marker is
// shared, so nested calls (Start applies the manifest through Apply) are fine.
func Begin(home, slug string) (func(), error) {
	lock, err := hostlock.Share(home, lockName(slug))
	if err != nil {
		return nil, err
	}
	stamp(home, slug)
	return func() {
		stamp(home, slug)
		_ = lock.Release()
	}, nil
}

// Active reports whether slug is being started or applied now, or was at any
// point since since.
func Active(home, slug string, since time.Time) bool {
	if hostlock.InUse(home, lockName(slug)) {
		return true
	}
	info, err := os.Stat(stampPath(home, slug))
	if err != nil {
		return false
	}
	return !info.ModTime().Before(since)
}

// stamp is best effort: losing it costs the "finished inside the window"
// case, not the start itself.
func stamp(home, slug string) {
	path := stampPath(home, slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		if f, err := os.Create(path); err == nil {
			f.Close()
		}
	}
}
