package envactivity

import (
	"testing"
	"time"
)

func TestAnIdleEnvironmentIsNotActive(t *testing.T) {
	t.Parallel()
	if Active(t.TempDir(), "dev", time.Now().Add(-time.Hour)) {
		t.Error("Active() = true for an environment nothing ever touched")
	}
}

func TestAnEnvironmentIsActiveWhileBegun(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// Begun long before the caller's window opened, and still going: the
	// stamp alone would say no, the held marker says yes.
	done, err := Begin(home, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !Active(home, "dev", time.Now().Add(time.Hour)) {
		t.Error("Active() = false while a start holds it")
	}
	done()
	if Active(home, "dev", time.Now().Add(time.Hour)) {
		t.Error("Active() = true after the start finished outside the window")
	}
}

// A start that began and finished entirely inside the caller's window holds
// nothing by the time the caller looks, and its containers are no less its own.
func TestAStartThatFinishedInsideTheWindowStillCounts(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	since := time.Now().Add(-time.Second)
	done, err := Begin(home, "dev")
	if err != nil {
		t.Fatal(err)
	}
	done()
	if !Active(home, "dev", since) {
		t.Error("Active() = false for a start that finished inside the window")
	}
	if Active(home, "other", since) {
		t.Error("another environment's activity leaked into this one")
	}
}
