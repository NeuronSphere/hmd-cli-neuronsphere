package nsconfig

import (
	"reflect"
	"strings"
	"testing"
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
