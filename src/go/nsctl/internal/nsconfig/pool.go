package nsconfig

import "fmt"

// DefaultPoolSize is how many local environments run leases may spread over
// when the file does not say. Each one is a k3s cluster and a Postgres, so the
// pool is kept small and reused rather than grown per session.
const DefaultPoolSize = 2

// DefaultPoolMember is the environment every pool starts from.
const DefaultPoolMember = "local"

// Pool is the [pool] table.
type Pool struct {
	// Size caps the pool. Environments beyond Members are created on demand,
	// named cc-1, cc-2, ..., until the pool reaches it.
	Size int `toml:"size,omitempty"`
	// Members are existing environments that belong to the pool.
	Members []string `toml:"members,omitempty"`
}

func (p *Pool) validate() error {
	if p.Size < 0 {
		return fmt.Errorf("[pool] size must be positive, not %d", p.Size)
	}
	if p.Size > 0 && len(p.Members) > p.Size {
		return fmt.Errorf("[pool] size %d is smaller than its %d members", p.Size, len(p.Members))
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
	return out
}
