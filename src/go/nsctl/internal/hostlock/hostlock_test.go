package hostlock

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireAndReleaseLetsTheNextHolderIn(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	first, err := Acquire(context.Background(), home, "registry", "first", time.Second)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	second, err := Acquire(context.Background(), home, "registry", "second", time.Second)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	defer second.Release()
}

func TestAcquireTimesOutAndNamesTheHolder(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	held, err := Acquire(context.Background(), home, "substrate", "env start local", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	_, err = Acquire(context.Background(), home, "substrate", "env start cc-1", 150*time.Millisecond)
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("Acquire() error = %v, want *BusyError", err)
	}
	if !strings.Contains(err.Error(), "env start local") {
		t.Errorf("error %q does not name the holder", err)
	}
}

func TestAcquireHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	held, err := Acquire(context.Background(), home, "registry", "holder", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Acquire(ctx, home, "registry", "waiter", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire() error = %v, want context.Canceled", err)
	}
}

func TestLocksWithDifferentNamesDoNotContend(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	a, err := Acquire(context.Background(), home, "registry", "a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	b, err := Acquire(context.Background(), home, "substrate", "b", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("a different lock name should not contend: %v", err)
	}
	defer b.Release()
}

func TestHoldersAreMutuallyExclusive(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	var inside, maxInside atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := Acquire(context.Background(), home, "registry", "racer", 10*time.Second)
			if err != nil {
				t.Errorf("Acquire() error = %v", err)
				return
			}
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			inside.Add(-1)
			l.Release()
		}()
	}
	wg.Wait()
	if got := maxInside.Load(); got != 1 {
		t.Errorf("max concurrent holders = %d, want 1", got)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	l, err := Acquire(context.Background(), t.TempDir(), "registry", "x", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Errorf("second Release() error = %v", err)
	}
}

func TestSharedHoldersCoexistAndShowAsInUse(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	if InUse(home, "env-dev") {
		t.Fatal("InUse() = true before anyone took it")
	}
	a, err := Share(home, "env-dev")
	if err != nil {
		t.Fatal(err)
	}
	// Nested: environment.Start holds it and calls Apply, which takes it again.
	b, err := Share(home, "env-dev")
	if err != nil {
		t.Fatalf("a second shared holder was refused: %v", err)
	}
	if !InUse(home, "env-dev") {
		t.Error("InUse() = false while shared holders exist")
	}
	a.Release()
	if !InUse(home, "env-dev") {
		t.Error("InUse() = false while one shared holder remains")
	}
	b.Release()
	if InUse(home, "env-dev") {
		t.Error("InUse() = true after every holder released")
	}
}
