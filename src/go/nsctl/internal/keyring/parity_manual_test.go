package keyring

import (
	"context"
	"os"
	"testing"
)

// TestRealKeychainParity is a manual check against this host's keychain, run
// only when NSCTL_KEYRING_PARITY_URL names an index to probe.
//
// It exists because the unit tests above prove the port is self-consistent and
// nothing more: whether `security find-generic-password` finds what
// keyring.get_password finds is a claim about two libraries and one keychain,
// and the only way to settle it is to ask both. It never prints a password --
// only whether one was found and how long it is.
func TestRealKeychainParity(t *testing.T) {
	indexURL := os.Getenv("NSCTL_KEYRING_PARITY_URL")
	if indexURL == "" {
		t.Skip("set NSCTL_KEYRING_PARITY_URL to probe this host's keychain")
	}
	user := os.Getenv("NSCTL_KEYRING_PARITY_USER")
	if user == "" {
		user = os.Getenv("USER")
	}

	k := New(func(format string, a ...any) { t.Logf("warn: "+format, a...) })
	t.Logf("helper %q available: %v", HelperName, k.Available())
	t.Logf("candidates: %q", Candidates(indexURL))

	password, err := k.ResolveIndexPassword(context.Background(), indexURL, user)
	t.Logf("go: found=%v length=%d err=%v", err == nil, len(password), err)
}

// TestRealKeychainService reads one explicit service from this host's keychain,
// for the positive half of the parity check: both readers agreeing that a
// credential is absent proves much less than both reading back the same stored
// value. Driven by a throwaway item the caller creates and deletes.
func TestRealKeychainService(t *testing.T) {
	service := os.Getenv("NSCTL_PARITY_SERVICE")
	if service == "" {
		t.Skip("set NSCTL_PARITY_SERVICE to read one service from this host's keychain")
	}
	user := os.Getenv("NSCTL_PARITY_USER")
	if user == "" {
		user = os.Getenv("USER")
	}

	k := New(func(format string, a ...any) { t.Logf("warn: "+format, a...) })
	got, err := k.Resolve(context.Background(), service, user)
	if err != nil {
		t.Fatalf("go: %v", err)
	}
	// The value is only ever compared, never logged.
	if expect := os.Getenv("NSCTL_PARITY_EXPECT"); expect != "" {
		t.Logf("go: found=true matches=%v", got == expect)
		if got != expect {
			t.Errorf("go read a different value than was stored (lengths %d vs %d)", len(got), len(expect))
		}
	} else {
		t.Logf("go: found=true length=%d", len(got))
	}
}
