package lease

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// NERD035 SPEC008: max_running is a budget the pool waits on, never evicts for.

// budgeted is pool() with MaxRunning and a fixed set of environments whose
// containers are up. Running lists the leases, which only works if it is not
// called under the host lock.
func budgeted(t *testing.T, s *Store, registered []string, size, max int, up map[string]bool, scores map[string]int) (*Pool, *[]string) {
	t.Helper()
	p, created := pool(registered, size, scores)
	p.MaxRunning = max
	var mu sync.Mutex
	p.Running = func() map[string]bool {
		if _, _, err := s.List(); err != nil {
			t.Errorf("listing inside Running: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		out := map[string]bool{}
		for k, v := range up {
			out[k] = v
		}
		return out
	}
	return p, created
}

func TestAtTheBudgetOnlyARunningEnvironmentIsGranted(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)
	// cc-1 scores better, but only local is running and the budget is 1.
	p, _ := budgeted(t, s, []string{"local", "cc-1"}, 2, 1, map[string]bool{"local": true},
		map[string]int{"local": 5, "cc-1": 0})

	g, err := s.AcquireFromPool(context.Background(), p, sessionReq("a", 100), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.Env != "local" {
		t.Errorf("got %s, want local: starting cc-1 would exceed max_running", g.Env)
	}
}

func TestASessionLeaseCountsAsRunningAndTheNextIsRefused(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)
	if _, err := s.Acquire("local", sessionReq("a", 100)); err != nil {
		t.Fatal(err)
	}
	p, created := budgeted(t, s, []string{"local", "cc-1"}, 3, 1, nil, nil)

	_, err := s.AcquireFromPool(context.Background(), p, sessionReq("b", 200), false, nil)
	var busy *PoolBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("error = %v, want *PoolBusyError", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "max_running") || !strings.Contains(msg, "local") {
		t.Errorf("error %q does not say which environments use the budget", msg)
	}
	if len(*created) != 0 {
		t.Errorf("created %v over the budget", *created)
	}
}

func TestUnderTheBudgetThePoolGrowsAsBefore(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)
	if _, err := s.Acquire("local", sessionReq("a", 100)); err != nil {
		t.Fatal(err)
	}
	p, created := budgeted(t, s, []string{"local"}, 3, 2, nil, nil)

	g, err := s.AcquireFromPool(context.Background(), p, sessionReq("b", 200), false, nil)
	if err != nil || g.Env != "cc-1" || len(*created) != 1 {
		t.Errorf("got %+v, %v (created %v); want cc-1 created", g, err, *created)
	}
}

func TestAWaiterOverTheBudgetIsServedWhenASessionEnds(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)
	first, err := s.Acquire("local", sessionReq("a", 100))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := budgeted(t, s, []string{"local", "cc-1"}, 2, 1, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan *Lease, 1)
	go func() {
		g, err := s.AcquireFromPool(ctx, p, sessionReq("b", 200), true, nil)
		if err != nil {
			t.Errorf("waiting acquire: %v", err)
		}
		done <- g
	}()
	waitForQueue(t, s, 1)
	if err := s.ReleaseSession("local", first.Token, false); err != nil {
		t.Fatal(err)
	}
	if g := <-done; g == nil {
		t.Error("the waiter was not served once the budget freed")
	}
}

func TestNoBudgetMeansNoLimit(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)
	if _, err := s.Acquire("local", sessionReq("a", 100)); err != nil {
		t.Fatal(err)
	}
	p, _ := budgeted(t, s, []string{"local", "cc-1"}, 2, 0, map[string]bool{"cc-9": true}, nil)

	if g, err := s.AcquireFromPool(context.Background(), p, sessionReq("b", 200), false, nil); err != nil || g.Env != "cc-1" {
		t.Errorf("got %+v, %v; want cc-1", g, err)
	}
}

func TestANamedEnvironmentCountsTheWholePool(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)
	if _, err := s.Acquire("local", sessionReq("a", 100)); err != nil {
		t.Fatal(err)
	}
	// dev, placed as a pool of one, while local's session uses the budget.
	p, _ := budgeted(t, s, []string{"local", "dev"}, 1, 1, nil, nil)
	p.Members = []string{"dev"}
	p.Counted = []string{"local", "cc-1"}

	if _, err := s.AcquireFromPool(context.Background(), p, sessionReq("b", 200), false, nil); err == nil {
		t.Error("dev was granted while local's session already used max_running 1")
	}
}
