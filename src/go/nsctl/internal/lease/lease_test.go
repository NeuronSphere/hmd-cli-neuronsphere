package lease

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testStore is a Store over a temp home with a controllable clock and an
// explicit set of live PIDs.
func testStore(t *testing.T) (*Store, *time.Time, map[int]bool) {
	t.Helper()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	alive := map[int]bool{100: true, 200: true, 300: true}
	var mu sync.Mutex
	s := New(t.TempDir())
	s.Host = "testhost"
	s.Poll = 5 * time.Millisecond
	s.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	s.Alive = func(pid int) bool { mu.Lock(); defer mu.Unlock(); return alive[pid] }
	return s, &now, alive
}

func req(holder string, pid int) Request {
	return Request{Holder: holder, PID: pid, TTL: 10 * time.Minute}
}

func TestAcquireThenSomeoneElseIsRefusedWithTheHolderNamed(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	l, err := s.Acquire("local", req("session-a", 100))
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if l.Token == "" || l.Env != "local" || l.Host != "testhost" {
		t.Errorf("lease = %+v", l)
	}

	_, err = s.Acquire("local", req("session-b", 200))
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("second Acquire() error = %v, want *HeldError", err)
	}
	if held.Lease.Holder != "session-a" {
		t.Errorf("HeldError names %q, want session-a", held.Lease.Holder)
	}
}

func TestReleaseNeedsTheTokenAndFreesTheEnvironment(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	l, err := s.Acquire("local", req("a", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Release("local", "wrong"); !errors.Is(err, ErrNotHolder) {
		t.Errorf("Release(wrong token) error = %v, want ErrNotHolder", err)
	}
	if err := s.Release("local", l.Token); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := s.Acquire("local", req("b", 200)); err != nil {
		t.Errorf("Acquire() after release error = %v", err)
	}
}

func TestOnlyOneOfManyRacersWins(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Acquire("local", req("racer", 100)); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Errorf("%d racers acquired the lease, want exactly 1", got)
	}
}

func TestALeasePastItsTTLIsReaped(t *testing.T) {
	t.Parallel()
	s, now, _ := testStore(t)

	if _, err := s.Acquire("local", req("a", 100)); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(11 * time.Minute)
	if _, err := s.Acquire("local", req("b", 200)); err != nil {
		t.Errorf("Acquire() over an expired lease error = %v", err)
	}
}

func TestRenewKeepsALeaseAlive(t *testing.T) {
	t.Parallel()
	s, now, _ := testStore(t)

	l, err := s.Acquire("local", req("a", 100))
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(8 * time.Minute)
	if err := s.Renew("local", l.Token); err != nil {
		t.Fatalf("Renew() error = %v", err)
	}
	*now = now.Add(8 * time.Minute)
	if _, err := s.Acquire("local", req("b", 200)); err == nil {
		t.Error("a renewed lease was taken over")
	}
}

func TestALeaseWhoseProcessDiedIsReaped(t *testing.T) {
	t.Parallel()
	s, _, alive := testStore(t)

	if _, err := s.Acquire("local", req("a", 100)); err != nil {
		t.Fatal(err)
	}
	delete(alive, 100)
	if _, err := s.Acquire("local", req("b", 200)); err != nil {
		t.Errorf("Acquire() over a dead holder error = %v", err)
	}
}

// The PID is only meaningful on the host that recorded it.
func TestAnotherHostsPIDIsNotChecked(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	if _, err := s.Acquire("local", req("a", 999)); err != nil {
		t.Fatal(err)
	}
	other := *s
	other.Host = "elsewhere"
	if _, err := other.Acquire("local", req("b", 200)); err == nil {
		t.Error("a lease from another host was reaped by PID")
	}
}

func TestStealReplacesALiveLeaseAndReportsIt(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	first, err := s.Acquire("local", req("a", 100))
	if err != nil {
		t.Fatal(err)
	}
	r := req("b", 200)
	r.Steal = true
	got, err := s.Acquire("local", r)
	if err != nil {
		t.Fatalf("steal error = %v", err)
	}
	if got.Stole == nil || got.Stole.Token != first.Token {
		t.Errorf("Stole = %+v, want the first lease", got.Stole)
	}
	if err := s.Renew("local", first.Token); !errors.Is(err, ErrNotHolder) {
		t.Errorf("the robbed holder's Renew() error = %v, want ErrNotHolder", err)
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	if err := s.Check("local", ""); err != nil {
		t.Errorf("Check() with no lease = %v, want nil", err)
	}
	l, err := s.Acquire("local", req("a", 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check("local", l.Token); err != nil {
		t.Errorf("Check() by the holder = %v, want nil", err)
	}
	var held *HeldError
	if err := s.Check("local", ""); !errors.As(err, &held) {
		t.Errorf("Check() by someone else = %v, want *HeldError", err)
	}
}

func TestTheLeaseFileIsWhereTheDocsSay(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)
	if _, err := s.Acquire("dev", req("a", 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Home + "/leases/dev.json"); err != nil {
		t.Errorf("lease file: %v", err)
	}
}

// pool is a Pool over a fixed registry, recording what it was asked to create.
func pool(registered []string, size int, scores map[string]int) (*Pool, *[]string) {
	reg := append([]string(nil), registered...)
	var created []string
	return &Pool{
		Members:    []string{"local"},
		Size:       size,
		Registered: func() ([]string, error) { return reg, nil },
		Create: func(name string) error {
			created = append(created, name)
			reg = append(reg, name)
			return nil
		},
		Score: func(env string) int { return scores[env] },
	}, &created
}

func TestPoolPicksTheClosestFreeEnvironment(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	p, created := pool([]string{"local", "cc-1"}, 2, map[string]int{"local": 5, "cc-1": 1})
	g, err := s.AcquireFromPool(context.Background(), p, req("a", 100), false, nil)
	if err != nil {
		t.Fatalf("AcquireFromPool() error = %v", err)
	}
	if g.Env != "cc-1" || g.Created {
		t.Errorf("got %s (created %v), want the existing cc-1", g.Env, g.Created)
	}
	if len(*created) > 0 {
		t.Errorf("created %v with a free member available", *created)
	}
}

func TestPoolTiesGoToTheMostRecentlyReleased(t *testing.T) {
	t.Parallel()
	s, now, _ := testStore(t)

	p, _ := pool([]string{"local", "cc-1"}, 2, nil)
	for _, env := range []string{"cc-1", "local"} {
		l, err := s.Acquire(env, req("warm", 100))
		if err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Minute)
		if err := s.Release(env, l.Token); err != nil {
			t.Fatal(err)
		}
	}
	g, err := s.AcquireFromPool(context.Background(), p, req("a", 100), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.Env != "local" {
		t.Errorf("got %s, want local (released last)", g.Env)
	}
}

func TestPoolCreatesAMemberWhenAllAreBusyAndThereIsRoom(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	p, created := pool([]string{"local"}, 2, nil)
	if _, err := s.Acquire("local", req("a", 100)); err != nil {
		t.Fatal(err)
	}
	g, err := s.AcquireFromPool(context.Background(), p, req("b", 200), false, nil)
	if err != nil {
		t.Fatalf("AcquireFromPool() error = %v", err)
	}
	if g.Env != "cc-1" || !g.Created {
		t.Errorf("got %s (created %v), want a new cc-1", g.Env, g.Created)
	}
	if len(*created) != 1 || (*created)[0] != "cc-1" {
		t.Errorf("created = %v", *created)
	}
}

func TestPoolFullWithoutWaitFailsAndSaysWhoHoldsWhat(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	p, _ := pool([]string{"local", "cc-1"}, 2, nil)
	for _, env := range []string{"local", "cc-1"} {
		if _, err := s.Acquire(env, req("holder-"+env, 100)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.AcquireFromPool(context.Background(), p, req("c", 300), false, nil)
	var busy *PoolBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("error = %v, want *PoolBusyError", err)
	}
	if len(busy.Held) != 2 {
		t.Errorf("Held = %+v, want both leases", busy.Held)
	}
}

func TestWaitersAreServedInOrder(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	p, _ := pool([]string{"local"}, 1, nil)
	first, err := s.Acquire("local", req("holder", 100))
	if err != nil {
		t.Fatal(err)
	}

	order := make(chan string, 2)
	positions := make(chan int, 64)
	var wg sync.WaitGroup
	start := func(name string, pid int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := s.AcquireFromPool(context.Background(), p, req(name, pid), true, func(ahead int) {
				if name == "second" {
					positions <- ahead
				}
			})
			if err != nil {
				t.Errorf("%s: %v", name, err)
				return
			}
			order <- name
			time.Sleep(20 * time.Millisecond)
			s.Release(g.Env, g.Token)
		}()
	}
	start("first", 200)
	waitForQueue(t, s, 1)
	start("second", 300)
	waitForQueue(t, s, 2)

	if err := s.Release("local", first.Token); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(order)
	var got []string
	for n := range order {
		got = append(got, n)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("served %v, want [first second]", got)
	}
	close(positions)
	sawAhead := false
	for a := range positions {
		if a == 1 {
			sawAhead = true
		}
	}
	if !sawAhead {
		t.Error("the second waiter was never told one run was ahead of it")
	}
}

func TestACancelledWaiterLeavesTheQueue(t *testing.T) {
	t.Parallel()
	s, _, _ := testStore(t)

	p, _ := pool([]string{"local"}, 1, nil)
	if _, err := s.Acquire("local", req("holder", 100)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.AcquireFromPool(ctx, p, req("w", 200), true, nil)
		done <- err
	}()
	waitForQueue(t, s, 1)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	_, waiters, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(waiters) != 0 {
		t.Errorf("waiters = %+v after cancellation", waiters)
	}
}

func waitForQueue(t *testing.T, s *Store, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, w, err := s.List()
		if err == nil && len(w) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("queue never reached %d waiters", n)
}
