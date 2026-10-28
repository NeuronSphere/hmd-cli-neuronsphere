//go:build !unix

package lease

// Without a cheap liveness probe the TTL alone ends a lease.
func pidAlive(int) bool { return true }
