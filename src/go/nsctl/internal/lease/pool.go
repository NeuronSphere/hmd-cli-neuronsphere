package lease

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// CreatedPrefix names the environments a pool creates on demand: cc-1, cc-2...
const CreatedPrefix = "cc-"

// Pool is the set of environments a run may be given, and how to judge and
// grow it. The functions keep this package free of the registry and the
// manifest; cmd wires them.
type Pool struct {
	// Members are the configured environments, e.g. [local].
	Members []string
	// Size caps the pool; the gap above len(Members) is filled with cc-N.
	Size int
	// Registered lists the registered environment slugs.
	Registered func() ([]string, error)
	// Create registers a new environment under name.
	Create func(name string) error
	// Score is how much a run would have to redeploy in env; lower is better.
	// Nil scores everything equally.
	Score func(env string) int

	// MaxRunning caps how many of the pool's environments may be running
	// once this grant starts its environment (NERD035 SPEC008); 0 is no cap.
	// At the cap only an environment already running can be granted, and
	// the pool waits rather than evicts.
	MaxRunning int
	// Running reports the environments whose containers are up. It is
	// called before each attempt, outside the lock, because it asks the
	// container engine. A session lease counts as running whatever it says.
	Running func() map[string]bool
	// Counted are environments whose running counts against MaxRunning
	// besides the candidates: the whole pool, when a named environment is
	// placed as a pool of one.
	Counted []string
}

// Candidates are every environment this pool may use, created or not, in
// preference order for creation.
func (p *Pool) Candidates() []string {
	out := append([]string(nil), p.Members...)
	for i := 1; len(out) < p.Size; i++ {
		name := fmt.Sprintf("%s%d", CreatedPrefix, i)
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// AcquireFromPool leases the best free pool environment for r.
//
// Among free registered candidates the lowest Score wins, then the most
// recently released (its deployed state is the warmest), then name order. With
// none free it registers the next unregistered candidate, if the pool has room.
// Otherwise it fails with *PoolBusyError, or with wait it queues: onQueue is
// told how many runs are ahead each time it looks, and ctx cancels the wait.
func (s *Store) AcquireFromPool(ctx context.Context, p *Pool, r Request, wait bool, onQueue func(ahead int)) (*Lease, error) {
	candidates := p.Candidates()

	var me *Waiter
	defer func() {
		if me != nil {
			_ = s.locked("leave queue", func() error { return os.Remove(me.file) })
		}
	}()

	for {
		var got *Lease
		var busy *PoolBusyError
		ahead := 0
		var up map[string]bool
		if p.MaxRunning > 0 && p.Running != nil {
			up = p.Running()
		}
		err := s.locked("pool lease for "+r.Holder, func() error {
			waiters, err := s.queue()
			if err != nil {
				return err
			}
			// Only waiters ahead of this run count, and only those that could
			// use the same environment: a run waiting for a named environment
			// does not hold back pool runs that want something else.
			blocked := map[string]bool{}
			for _, w := range waiters {
				if me != nil && w.ID == me.ID {
					break
				}
				for _, c := range w.Candidates {
					if slices.Contains(candidates, c) {
						blocked[c] = true
					}
				}
				ahead++
			}

			got, busy, err = s.pick(p, candidates, blocked, r, up)
			if err != nil || got != nil {
				return err
			}
			busy.Waiting = len(waiters)
			if !wait {
				return nil
			}
			now := s.Now()
			if me == nil {
				// Random, not the clock: two runs can enqueue in the same tick.
				id, err := newToken()
				if err != nil {
					return err
				}
				me = &Waiter{
					ID: id[:16], Holder: r.Holder, PID: r.PID, Host: s.Host,
					Enqueued: now, Candidates: candidates,
				}
				// Queue order is this sequence, one past the last waiter's, so
				// it is arrival order under the lock whatever the clock says.
				seq := 1
				if n := len(waiters); n > 0 {
					seq = waiters[n-1].Seq + 1
				}
				me.Seq = seq
				me.file = filepath.Join(s.queueDir(),
					fmt.Sprintf("%020d-%s-%s.json", seq, me.ID, sanitize(r.Holder)))
			}
			me.Heartbeat = now
			return s.writeWaiter(me)
		})
		if err != nil {
			return nil, err
		}
		if got != nil {
			return got, nil
		}
		if !wait {
			return nil, busy
		}
		if onQueue != nil {
			onQueue(ahead)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.Poll):
		}
	}
}

// pick grants the best free candidate not blocked by an earlier waiter, or
// creates one, within the running budget. Caller holds the lock.
func (s *Store) pick(p *Pool, candidates []string, blocked map[string]bool, r Request, up map[string]bool) (*Lease, *PoolBusyError, error) {
	registered := map[string]bool{}
	if p.Registered != nil {
		names, err := p.Registered()
		if err != nil {
			return nil, nil, err
		}
		for _, n := range names {
			registered[n] = true
		}
	}

	type option struct {
		env      string
		score    int
		released time.Time
	}
	current := map[string]*Lease{}
	running := map[string]bool{}
	counted := append(append([]string(nil), candidates...), p.Counted...)
	for _, c := range counted {
		if !registered[c] {
			continue
		}
		if _, seen := current[c]; seen {
			continue
		}
		cur, err := s.current(c)
		if err != nil {
			return nil, nil, err
		}
		current[c] = cur
		if up[c] || (cur != nil && cur.IsSession()) {
			running[c] = true
		}
	}
	// admit is the budget: an environment already running costs nothing,
	// any other may start only while the pool is under its cap.
	admit := func(env string) bool {
		return p.MaxRunning <= 0 || running[env] || len(running) < p.MaxRunning
	}

	var free []option
	busy := &PoolBusyError{}
	var unregistered []string
	for _, c := range candidates {
		if !registered[c] {
			unregistered = append(unregistered, c)
			continue
		}
		cur := current[c]
		if cur != nil {
			busy.Held = append(busy.Held, *cur)
			continue
		}
		if blocked[c] || !admit(c) {
			continue
		}
		o := option{env: c, released: releasedAt(s.releasedPath(c))}
		if p.Score != nil {
			o.score = p.Score(c)
		}
		free = append(free, o)
	}

	if len(free) > 0 {
		sort.SliceStable(free, func(i, j int) bool {
			if free[i].score != free[j].score {
				return free[i].score < free[j].score
			}
			if !free[i].released.Equal(free[j].released) {
				return free[i].released.After(free[j].released)
			}
			return free[i].env < free[j].env
		})
		l, err := s.grant(free[0].env, r)
		return l, nil, err
	}

	for _, name := range unregistered {
		if blocked[name] || p.Create == nil || !admit(name) {
			continue
		}
		if err := p.Create(name); err != nil {
			return nil, nil, fmt.Errorf("adding %s to the pool: %w", name, err)
		}
		l, err := s.grant(name, r)
		if l != nil {
			l.Created = true
		}
		return l, nil, err
	}
	if p.MaxRunning > 0 && len(running) >= p.MaxRunning {
		busy.MaxRunning = p.MaxRunning
		for env := range running {
			busy.Running = append(busy.Running, env)
		}
		sort.Strings(busy.Running)
	}
	return nil, busy, nil
}

func sanitize(s string) string {
	out := []rune{}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return string(out)
}
