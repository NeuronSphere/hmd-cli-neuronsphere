// Package hostlock serialises the few nsctl operations that mutate state
// shared by every environment under one HMD_HOME.
//
// Named environments let several sessions work side by side, but some state
// is not per environment: the registry file, and the control plane (Floci,
// hmd_proxy, ms-deployment) whose containers a start may recreate. Two
// processes doing load-modify-save on the registry silently lose one write --
// including a port slot or account allocation, which then gets handed out
// twice -- and two concurrent first starts would both run the bootstrap.
//
// The lock is an advisory flock on $HMD_HOME/.cache/neuronsphere/<name>.lock.
// The kernel drops it when the holding process exits, so a crashed nsctl can
// never wedge the next one. The holder writes a one-line description into the
// file so a waiter that gives up can say who it was waiting for.
package hostlock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The lock names nsctl uses. Registry is held only around a load-modify-save;
// Substrate around control-plane and Floci mutation, which can take minutes.
const (
	Registry  = "registry"
	Substrate = "substrate"
	Leases    = "leases"
)

// pollInterval is how often a waiter retries. A non-blocking flock in a loop
// rather than a blocking one, so the wait honours both the timeout and ctx.
const pollInterval = 25 * time.Millisecond

// Lock is a held host lock. Release it with Release; it is also released when
// the process exits.
type Lock struct {
	f      *os.File
	shared bool
}

// BusyError is returned when the lock is still held when the timeout expires.
type BusyError struct {
	Name   string
	Holder string
	Waited time.Duration
}

func (e *BusyError) Error() string {
	holder := e.Holder
	if holder == "" {
		holder = "another nsctl process"
	}
	return fmt.Sprintf("waited %s for the %s lock, still held by %s", e.Waited.Round(time.Millisecond), e.Name, holder)
}

// Path is where the named lock lives for one HMD_HOME.
func Path(home, name string) string {
	return filepath.Join(home, ".cache", "neuronsphere", name+".lock")
}

// Acquire takes the named lock, waiting up to timeout. holder describes the
// operation, e.g. "env start cc-1", and is shown to anyone left waiting.
func Acquire(ctx context.Context, home, name, holder string, timeout time.Duration) (*Lock, error) {
	path := Path(home, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	start := time.Now()
	for {
		ok, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if ok {
			break
		}
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		if waited := time.Since(start); waited >= timeout {
			f.Close()
			return nil, &BusyError{Name: name, Holder: readHolder(path), Waited: waited}
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	// Best effort: the description is for messages, not for correctness.
	if err := f.Truncate(0); err == nil {
		fmt.Fprintf(f, "%s (pid %d, since %s)", holder, os.Getpid(), time.Now().Format(time.RFC3339))
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. Calling it again is a no-op.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	if !l.shared {
		_ = f.Truncate(0)
	}
	if err := unlock(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Share takes the named lock in shared mode: any number of holders at once,
// including several in one process. It marks something as in use for InUse
// rather than excluding anyone, so it never waits.
func Share(home, name string) (*Lock, error) {
	path := Path(home, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := lockShared(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return &Lock{f: f, shared: true}, nil
}

// InUse reports whether anyone holds the named lock, in either mode.
func InUse(home, name string) bool {
	f, err := os.OpenFile(Path(home, name), os.O_RDWR, 0)
	if err != nil {
		// Never created, so never held.
		return false
	}
	defer f.Close()
	ok, err := tryLock(f)
	if err != nil {
		return false
	}
	if ok {
		_ = unlock(f)
		return false
	}
	return true
}

func readHolder(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
