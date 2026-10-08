// Package lease hands out exclusive use of a local environment.
//
// Several Claude sessions -- or a session and a person -- deploying into one
// environment at once undo each other's work, and every nsctl and hmd verb
// falls back to the same default environment. A lease says "this holder owns
// that environment until it releases it": every command that changes an
// environment refuses one someone else holds (NERD035 SPEC001).
//
// A run lease covers one verify or deploy run. A small warm pool of
// environments (`local` plus a couple of `cc-N`) therefore serves many runs: a
// run takes the free one whose deployed manifest is closest to what it needs,
// and queues when none is free.
//
// A session lease (NERD035 SPEC002) covers a whole working session instead:
// hours, between heartbeats, ended by the session's own process exiting or by
// ReleaseSession. A run inside the session is answered with it.
//
// State is plain files under $HMD_HOME/leases, every mutation made under the
// host's `leases` lock:
//
//	leases/<env>.json           the live lease, if any
//	leases/<env>.released       touched on release; its mtime breaks ties
//	leases/queue/<seq>-<id>.json one per waiting run, served in seq order
//
// A lease is live while its heartbeat is within its TTL and, when it was taken
// on this host with a PID, while that process lives. Dead leases are reaped by
// whoever looks next, so a session killed mid-run never wedges the pool.
package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hostlock"
)

// DefaultTTL is how long a lease lives without a heartbeat.
const DefaultTTL = 10 * time.Minute

// DefaultSessionTTL is DefaultTTL for a session lease (NERD035 SPEC002): a
// session works for hours and heartbeats rather than renewing per run.
const DefaultSessionTTL = 8 * time.Hour

// stopTTL bounds a stopping placeholder: long enough for `env stop`, short
// enough that a stop whose process vanished without its PID being checkable
// does not hold the environment for long.
const stopTTL = 15 * time.Minute

// ScopeSession marks a lease held for a whole working session rather than one
// run. A run lease stores no scope, so files written before scopes existed
// read the same.
const ScopeSession = "session"

// lockTimeout bounds the wait for the leases lock. Holders only read and
// write a few small files.
const lockTimeout = 30 * time.Second

// waiterStale is how long a queued run may go without a heartbeat before it
// is pruned. Waiters heartbeat every Poll.
const waiterStale = 30 * time.Second

// Lease is one environment's current holder.
type Lease struct {
	Env        string    `json:"env"`
	Holder     string    `json:"holder"`
	Token      string    `json:"token"`
	RunID      string    `json:"run_id,omitempty"`
	PID        int       `json:"pid,omitempty"`
	Host       string    `json:"host"`
	Acquired   time.Time `json:"acquired"`
	Heartbeat  time.Time `json:"heartbeat"`
	TTLSeconds int       `json:"ttl_seconds"`

	// Scope is ScopeSession for a session lease, empty for a run lease.
	Scope string `json:"scope,omitempty"`
	// Template and Repos say what a session environment was shaped from:
	// the template it started from and the working trees it deploys.
	Template string   `json:"template,omitempty"`
	Repos    []string `json:"repos,omitempty"`
	// KeepRunning says the session's end must not stop its environment
	// (NERD035 SPEC006).
	KeepRunning bool `json:"keep_running,omitempty"`

	// Nested says this acquire was answered with the caller's own session
	// lease rather than a lease of its own. Reported, not stored.
	Nested bool `json:"nested,omitempty"`
	// Stole is the lease this one replaced with Request.Steal. Reported, not
	// stored.
	Stole *Lease `json:"stole,omitempty"`
	// Created says the pool registered this environment for this run, so the
	// caller must start it before deploying into it. Reported, not stored.
	Created bool `json:"created,omitempty"`
}

// IsSession reports whether l is held for a session rather than a run.
func (l *Lease) IsSession() bool { return l.Scope == ScopeSession }

// Expires is when the lease lapses without a renewal.
func (l *Lease) Expires() time.Time {
	return l.Heartbeat.Add(time.Duration(l.TTLSeconds) * time.Second)
}

// Request describes who is asking.
type Request struct {
	Holder string
	RunID  string
	// PID is the process whose death ends the lease; 0 relies on the TTL.
	PID   int
	TTL   time.Duration
	Steal bool
	// Scope, Template, Repos and KeepRunning are recorded on the lease; see
	// Lease.
	Scope       string
	Template    string
	Repos       []string
	KeepRunning bool
}

// HeldError says the environment is leased by someone else.
type HeldError struct{ Lease Lease }

func (e *HeldError) Error() string {
	return fmt.Sprintf("environment %q is leased by %s (%s) until %s",
		e.Lease.Env, e.Lease.Holder, e.Lease.Where(), e.Lease.Expires().Format(time.RFC3339))
}

// Where says which process holds the lease, for messages.
func (l *Lease) Where() string {
	if l.PID > 0 {
		return fmt.Sprintf("pid %d on %s", l.PID, l.Host)
	}
	return "on " + l.Host
}

// PoolBusyError says every pool environment is leased and the pool is full.
type PoolBusyError struct {
	Held    []Lease
	Waiting int
	// MaxRunning and Running are set when the running budget (NERD035
	// SPEC008) is what refused: the cap, and the environments using it.
	MaxRunning int
	Running    []string
}

func (e *PoolBusyError) Error() string {
	parts := make([]string, 0, len(e.Held))
	for _, l := range e.Held {
		parts = append(parts, fmt.Sprintf("%s by %s", l.Env, l.Holder))
	}
	msg := "every pool environment is leased: " + strings.Join(parts, ", ")
	if e.MaxRunning > 0 {
		msg = fmt.Sprintf("%d pool environment(s) are already running, the [pool] max_running of %d: %s;"+
			" stop or release one, or raise max_running", len(e.Running), e.MaxRunning, strings.Join(e.Running, ", "))
	}
	if e.Waiting > 0 {
		msg += fmt.Sprintf("; %d run(s) already waiting", e.Waiting)
	}
	return msg + ". Pass --wait to queue"
}

var (
	// ErrNotHolder is a renew or release with a token that is not the live
	// lease's: it expired, was stolen, or was never this caller's.
	ErrNotHolder = errors.New("this token does not hold the lease")
	// ErrSessionLease is a run-style release of a session lease. A run nested
	// in a session holds the session's own token, so the token cannot say
	// which of the two is letting go; only ReleaseSession ends a session.
	ErrSessionLease = errors.New("this is a session lease; it is ended by the session, not by a run")
)

// Store is the lease state of one HMD_HOME.
type Store struct {
	Home string
	Host string
	// Poll is how often a queued run checks for a free environment.
	Poll  time.Duration
	Now   func() time.Time
	Alive func(pid int) bool

	// OnSessionEnd stops env when a session lease on it ends (NERD035
	// SPEC006). It runs after the host lock is released, while a placeholder
	// lease held by PID keeps the environment from being handed out. Nil
	// means nothing here can stop an environment, and an ended session
	// simply frees it.
	OnSessionEnd func(env string) error
	// PID is this process: the holder of a stopping placeholder, so that a
	// stop whose process died is itself reaped.
	PID int

	// stops is shared by copies of a Store, so it is behind a pointer.
	stops *stopQueue
}

// stopQueue is the placeholders whose stops are due once the lock is free.
type stopQueue struct {
	mu      sync.Mutex
	pending []Lease
}

func (q *stopQueue) push(l Lease) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, l)
}

func (q *stopQueue) drain() []Lease {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.pending
	q.pending = nil
	return out
}

// New returns the Store for home, wired to the real clock and process table.
func New(home string) *Store {
	host, _ := os.Hostname()
	return &Store{Home: home, Host: host, Poll: 2 * time.Second, Now: time.Now, Alive: pidAlive, PID: os.Getpid(),
		stops: &stopQueue{}}
}

// Dir is where lease state lives.
func Dir(home string) string { return filepath.Join(home, "leases") }

func (s *Store) leasePath(env string) string    { return filepath.Join(Dir(s.Home), env+".json") }
func (s *Store) releasedPath(env string) string { return filepath.Join(Dir(s.Home), env+".released") }
func (s *Store) queueDir() string               { return filepath.Join(Dir(s.Home), "queue") }

// locked runs fn under the host's leases lock, then -- with the lock
// released, because a stop is a container operation and may take a minute --
// any stops fn's reaping or releasing queued.
func (s *Store) locked(holder string, fn func() error) error {
	lock, err := hostlock.Acquire(context.Background(), s.Home, hostlock.Leases, holder, lockTimeout)
	if err != nil {
		return err
	}
	err = fn()
	lock.Release()
	s.runStops()
	return err
}

// endSession is what happens to a session lease l that just ended, under the
// lock: with somewhere to send the stop, l is replaced by a placeholder held
// by this process and the stop is queued; the placeholder is returned as the
// environment's current holder. Otherwise the environment is simply free.
func (s *Store) endSession(l *Lease) (*Lease, error) {
	if !l.IsSession() || l.KeepRunning || s.OnSessionEnd == nil || s.stops == nil {
		return nil, nil
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	now := s.Now()
	stop := &Lease{
		Env: l.Env, Holder: "nsctl, stopping " + l.Env + " after " + l.Holder + "'s session", Token: token,
		RunID: token[:12], PID: s.PID, Host: s.Host, Acquired: now, Heartbeat: now,
		TTLSeconds: int(stopTTL / time.Second),
	}
	if err := s.write(stop); err != nil {
		return nil, err
	}
	s.stops.push(*stop)
	return stop, nil
}

// runStops stops every environment endSession queued, then gives each back.
// A failed stop is reported by OnSessionEnd itself and still frees the
// environment: holding it would only stop the next session using it.
func (s *Store) runStops() {
	for _, stop := range s.stops.drain() {
		_ = s.OnSessionEnd(stop.Env)
		_ = s.locked("stopped "+stop.Env, func() error {
			data, err := os.ReadFile(s.leasePath(stop.Env))
			if err != nil {
				return nil
			}
			var cur Lease
			if json.Unmarshal(data, &cur) == nil && cur.Token == stop.Token {
				_ = os.Remove(s.leasePath(stop.Env))
				touch(s.releasedPath(stop.Env), s.Now())
			}
			return nil
		})
	}
}

// live reports whether l still holds its environment.
func (s *Store) live(l *Lease) bool {
	if !s.Now().Before(l.Expires()) {
		return false
	}
	if l.PID > 0 && l.Host == s.Host && !s.Alive(l.PID) {
		return false
	}
	return true
}

// current reads env's lease, reaping it if it is dead. Caller holds the lock.
func (s *Store) current(env string) (*Lease, error) {
	data, err := os.ReadFile(s.leasePath(env))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l Lease
	if err := json.Unmarshal(data, &l); err != nil {
		// A torn or foreign file holds nothing; it is replaced, not obeyed.
		return nil, nil
	}
	if !s.live(&l) {
		_ = os.Remove(s.leasePath(env))
		if l.IsSession() {
			touch(s.releasedPath(env), s.Now())
		}
		return s.endSession(&l)
	}
	return &l, nil
}

func (s *Store) write(l *Lease) error {
	stored := *l
	stored.Stole, stored.Created, stored.Nested = nil, false, false
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.leasePath(l.Env), data, 0o644, 0o755)
}

func (s *Store) grant(env string, r Request) (*Lease, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	ttl := r.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	runID := r.RunID
	if runID == "" {
		runID = token[:12]
	}
	now := s.Now()
	scope := r.Scope
	if scope != ScopeSession {
		scope = ""
	}
	l := &Lease{
		Env: env, Holder: r.Holder, Token: token, RunID: runID, PID: r.PID, Host: s.Host,
		Acquired: now, Heartbeat: now, TTLSeconds: int(ttl / time.Second),
		Scope: scope, Template: r.Template, Repos: append([]string(nil), r.Repos...),
		KeepRunning: r.KeepRunning && scope == ScopeSession,
	}
	if err := s.write(l); err != nil {
		return nil, err
	}
	return l, nil
}

// Acquire leases env, failing with *HeldError if someone else holds it --
// unless r.Steal, which replaces their lease and reports it in Stole.
func (s *Store) Acquire(env string, r Request) (*Lease, error) {
	var out *Lease
	err := s.locked("lease "+env+" for "+r.Holder, func() error {
		cur, err := s.current(env)
		if err != nil {
			return err
		}
		if cur != nil && !r.Steal {
			return &HeldError{Lease: *cur}
		}
		out, err = s.grant(env, r)
		if err == nil {
			out.Stole = cur
		}
		return err
	})
	return out, err
}

// Renew pushes the lease's expiry out by its TTL.
func (s *Store) Renew(env, token string) error {
	return s.locked("renew "+env, func() error {
		cur, err := s.current(env)
		if err != nil {
			return err
		}
		if cur == nil || cur.Token != token {
			return ErrNotHolder
		}
		cur.Heartbeat = s.Now()
		return s.write(cur)
	})
}

// Release gives a run lease back. A session lease is left in place with
// ErrSessionLease; see ReleaseSession.
func (s *Store) Release(env, token string) error {
	return s.release(env, token, false, false)
}

// ReleaseSession gives back any lease the token holds, session or run. A
// session's end stops its environment (see OnSessionEnd) unless keepRunning
// or the lease was taken with KeepRunning.
func (s *Store) ReleaseSession(env, token string, keepRunning bool) error {
	return s.release(env, token, true, keepRunning)
}

func (s *Store) release(env, token string, session, keepRunning bool) error {
	return s.locked("release "+env, func() error {
		cur, err := s.current(env)
		if err != nil {
			return err
		}
		if cur == nil || cur.Token != token {
			return ErrNotHolder
		}
		if cur.IsSession() && !session {
			return ErrSessionLease
		}
		if err := os.Remove(s.leasePath(env)); err != nil {
			return err
		}
		touch(s.releasedPath(env), s.Now())
		if cur.IsSession() && !keepRunning {
			_, err := s.endSession(cur)
			return err
		}
		return nil
	})
}

// ByToken returns the live lease token holds, or nil. A token is a bearer
// credential, so finding its lease needs nothing else -- which is what lets a
// session heartbeat or ask who it is without naming its environment.
func (s *Store) ByToken(token string) (*Lease, error) {
	if token == "" {
		return nil, nil
	}
	var out *Lease
	err := s.locked("find lease", func() error {
		l, err := s.byToken(token)
		out = l
		return err
	})
	return out, err
}

// byToken is ByToken under the lock.
func (s *Store) byToken(token string) (*Lease, error) {
	entries, err := os.ReadDir(Dir(s.Home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		l, err := s.current(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		if l != nil && l.Token == token {
			return l, nil
		}
	}
	return nil, nil
}

// BySessionPID returns the live session lease watching pid on this host, or
// nil. It is how a session finds its own lease without its token: a Claude
// Code hook may not see the exports another hook wrote.
func (s *Store) BySessionPID(pid int) (*Lease, error) {
	if pid <= 0 {
		return nil, nil
	}
	var out *Lease
	err := s.locked("find session", func() error {
		entries, err := os.ReadDir(Dir(s.Home))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			l, err := s.current(strings.TrimSuffix(e.Name(), ".json"))
			if err != nil {
				return err
			}
			if l != nil && l.IsSession() && l.PID == pid && l.Host == s.Host {
				out = l
				return nil
			}
		}
		return nil
	})
	return out, err
}

// Heartbeat renews whichever lease token holds, and returns it.
func (s *Store) Heartbeat(token string) (*Lease, error) {
	var out *Lease
	err := s.locked("heartbeat", func() error {
		l, err := s.byToken(token)
		if err != nil {
			return err
		}
		if l == nil {
			return ErrNotHolder
		}
		l.Heartbeat = s.Now()
		out = l
		return s.write(l)
	})
	return out, err
}

// Check is the enforcement question: may the bearer of token mutate env?
// Yes when nobody holds it or token is the holder's; otherwise *HeldError.
func (s *Store) Check(env, token string) error {
	return s.locked("check "+env, func() error {
		cur, err := s.current(env)
		if err != nil {
			return err
		}
		if cur == nil || (token != "" && cur.Token == token) {
			return nil
		}
		return &HeldError{Lease: *cur}
	})
}

// Waiter is one queued run.
type Waiter struct {
	ID         string    `json:"id"`
	Seq        int       `json:"seq"`
	Holder     string    `json:"holder"`
	PID        int       `json:"pid,omitempty"`
	Host       string    `json:"host"`
	Enqueued   time.Time `json:"enqueued"`
	Heartbeat  time.Time `json:"heartbeat"`
	Candidates []string  `json:"candidates"`
	file       string
}

// List returns the live leases and the live queue, in queue order.
func (s *Store) List() ([]Lease, []Waiter, error) {
	var leases []Lease
	var waiters []Waiter
	err := s.locked("list", func() error {
		entries, err := os.ReadDir(Dir(s.Home))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			l, err := s.current(strings.TrimSuffix(e.Name(), ".json"))
			if err != nil {
				return err
			}
			if l != nil {
				leases = append(leases, *l)
			}
		}
		waiters, err = s.queue()
		return err
	})
	// A placeholder this call wrote while reading is gone by now -- locked ran
	// its stop and released it before returning -- so it is not reported as a
	// holder. One another call of this process still holds is.
	live := leases[:0]
	for _, l := range leases {
		if l.PID == s.PID && l.Host == s.Host && !s.stillHolds(&l) {
			continue
		}
		live = append(live, l)
	}
	return live, waiters, err
}

// stillHolds reports whether l's file still carries l's token. Unlocked: it
// only decides what a listing shows.
func (s *Store) stillHolds(l *Lease) bool {
	data, err := os.ReadFile(s.leasePath(l.Env))
	if err != nil {
		return false
	}
	var cur Lease
	return json.Unmarshal(data, &cur) == nil && cur.Token == l.Token
}

// queue reads the live waiters in order, pruning dead ones. Caller holds the
// lock.
func (s *Store) queue() ([]Waiter, error) {
	entries, err := os.ReadDir(s.queueDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Waiter
	for _, e := range entries {
		path := filepath.Join(s.queueDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var w Waiter
		if json.Unmarshal(data, &w) != nil ||
			s.Now().Sub(w.Heartbeat) > waiterStale ||
			(w.PID > 0 && w.Host == s.Host && !s.Alive(w.PID)) {
			_ = os.Remove(path)
			continue
		}
		w.file = path
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].file < out[j].file
	})
	return out, nil
}

func (s *Store) writeWaiter(w *Waiter) error {
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(w.file, data, 0o644, 0o755)
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating a lease token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func touch(path string, at time.Time) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.Chtimes(path, at, at); err != nil {
		if f, err := os.Create(path); err == nil {
			f.Close()
			_ = os.Chtimes(path, at, at)
		}
	}
}

// ReleasedAt is when env's last lease ended, or zero if none ever did here.
func (s *Store) ReleasedAt(env string) time.Time { return releasedAt(s.releasedPath(env)) }

// Forget drops what the store remembers about env once it no longer exists:
// its release time. A live lease is left alone.
func (s *Store) Forget(env string) error {
	return s.locked("forget "+env, func() error {
		err := os.Remove(s.releasedPath(env))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}

func releasedAt(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
