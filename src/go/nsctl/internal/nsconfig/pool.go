package nsconfig

import (
	"fmt"
	"time"
)

// DefaultPoolSize is how many local environments run leases may spread over
// when the file does not say. Each one is a k3s cluster and a Postgres, so the
// pool is kept small and reused rather than grown per session.
const DefaultPoolSize = 2

// DefaultPoolMember is the environment every pool starts from.
const DefaultPoolMember = "local"

// DefaultSessionTTL is how long a session lease lives without a heartbeat when
// the file does not say (NERD035 SPEC002). It matches lease.DefaultSessionTTL;
// this package does not import that one.
const DefaultSessionTTL = 8 * time.Hour

// Pool is the [pool] table.
type Pool struct {
	// Size caps the pool. Environments beyond Members are created on demand,
	// named cc-1, cc-2, ..., until the pool reaches it.
	Size int `toml:"size,omitempty"`
	// Members are existing environments that belong to the pool.
	Members []string `toml:"members,omitempty"`
	// SessionTTL is a Go duration ("8h", "90m") a session lease lives
	// without a heartbeat.
	SessionTTL string `toml:"session_ttl,omitempty"`
	// MaxRunning caps how many pool environments a session acquire may
	// leave running (NERD035 SPEC008). 0 is no cap beyond Size.
	MaxRunning int `toml:"max_running,omitempty"`
}

// SessionTTLOrDefault is SessionTTL parsed, or DefaultSessionTTL. A value
// validate refused never reaches here.
func (p Pool) SessionTTLOrDefault() time.Duration {
	if d, err := time.ParseDuration(p.SessionTTL); err == nil && d > 0 {
		return d
	}
	return DefaultSessionTTL
}

func (p *Pool) validate() error {
	if p.Size < 0 {
		return fmt.Errorf("[pool] size must be positive, not %d", p.Size)
	}
	if p.Size > 0 && len(p.Members) > p.Size {
		return fmt.Errorf("[pool] size %d is smaller than its %d members", p.Size, len(p.Members))
	}
	if p.MaxRunning < 0 {
		return fmt.Errorf("[pool] max_running must be zero or more, not %d", p.MaxRunning)
	}
	if p.SessionTTL != "" {
		d, err := time.ParseDuration(p.SessionTTL)
		if err != nil || d <= 0 {
			return fmt.Errorf("[pool] session_ttl must be a positive duration such as \"8h\", not %q", p.SessionTTL)
		}
	}
	return nil
}

// EnvPool is the effective pool: the file's, with defaults for what it omits.
// A nil Config (no file) gets the defaults.
func (c *Config) EnvPool() Pool {
	out := Pool{Size: DefaultPoolSize, Members: []string{DefaultPoolMember}}
	if c == nil || c.Pool == nil {
		return out
	}
	if len(c.Pool.Members) > 0 {
		out.Members = append([]string(nil), c.Pool.Members...)
	}
	if c.Pool.Size > 0 {
		out.Size = c.Pool.Size
	}
	if out.Size < len(out.Members) {
		out.Size = len(out.Members)
	}
	out.SessionTTL = c.Pool.SessionTTL
	out.MaxRunning = c.Pool.MaxRunning
	return out
}
