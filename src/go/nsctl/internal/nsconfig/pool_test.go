package nsconfig

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPoolDefaultsWhenTheFileHasNone(t *testing.T) {
	t.Parallel()

	var cfg *Config
	got := cfg.EnvPool()
	if got.Size != DefaultPoolSize || !reflect.DeepEqual(got.Members, []string{"local"}) {
		t.Errorf("EnvPool() = %+v, want size %d and members [local]", got, DefaultPoolSize)
	}
}

func TestPoolIsReadFromTheFile(t *testing.T) {
	t.Parallel()

	home := write(t, "[pool]\nsize = 3\nmembers = [\"local\", \"dev\"]\n")
	cfg, err := Load(home, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.EnvPool()
	if got.Size != 3 || !reflect.DeepEqual(got.Members, []string{"local", "dev"}) {
		t.Errorf("EnvPool() = %+v", got)
	}
}

// Fewer members than the size is the normal case: the rest are created on
// demand. More members than the size would never all be used.
func TestPoolSizeCoversItsMembers(t *testing.T) {
	t.Parallel()

	home := write(t, "[pool]\nsize = 1\nmembers = [\"local\", \"dev\"]\n")
	_, err := Load(home, noEnv)
	if err == nil || !strings.Contains(err.Error(), "pool") {
		t.Fatalf("Load() error = %v, want a pool size error", err)
	}
}

func TestSessionTTLDefaultsToEightHours(t *testing.T) {
	t.Parallel()

	var cfg *Config
	if got := cfg.EnvPool().SessionTTLOrDefault(); got != 8*time.Hour {
		t.Errorf("SessionTTLOrDefault() = %s, want 8h", got)
	}
}

func TestSessionTTLIsReadFromTheFile(t *testing.T) {
	t.Parallel()

	home := write(t, "[pool]\nsession_ttl = \"2h30m\"\n")
	cfg, err := Load(home, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.EnvPool().SessionTTLOrDefault(); got != 150*time.Minute {
		t.Errorf("SessionTTLOrDefault() = %s, want 2h30m", got)
	}
}

func TestABadSessionTTLIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	for _, v := range []string{"soon", "-1h", "0s"} {
		home := write(t, "[pool]\nsession_ttl = \""+v+"\"\n")
		if _, err := Load(home, noEnv); err == nil || !strings.Contains(err.Error(), "session_ttl") {
			t.Errorf("Load(session_ttl=%q) error = %v, want a session_ttl error", v, err)
		}
	}
}

func TestMaxRunningIsReadAndANegativeOneRefused(t *testing.T) {
	t.Parallel()

	home := write(t, "[pool]\nmax_running = 2\n")
	cfg, err := Load(home, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.EnvPool().MaxRunning; got != 2 {
		t.Errorf("MaxRunning = %d, want 2", got)
	}
	if _, err := Load(write(t, "[pool]\nmax_running = -1\n"), noEnv); err == nil || !strings.Contains(err.Error(), "max_running") {
		t.Errorf("Load(max_running=-1) = %v, want a max_running error", err)
	}
	var none *Config
	if got := none.EnvPool().MaxRunning; got != 0 {
		t.Errorf("default MaxRunning = %d, want 0 (no cap)", got)
	}
}
