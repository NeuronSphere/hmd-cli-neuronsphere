package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The field existed for this and was never assigned, so every deploy
// bind-mounted a source under the system temp dir (NERD021 SPEC006).
func TestWorkDirDefaultsUnderHome(t *testing.T) {
	home := t.TempDir()
	got := (Config{Home: home}).workDir()
	want := filepath.Join(home, ".cache", "nsctl", "tmp")
	if got != want {
		t.Errorf("workDir() = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("workDir() must create the directory: %v", err)
	}
}

func TestWorkDirHonoursAnExplicitValue(t *testing.T) {
	explicit := t.TempDir()
	got := (Config{Home: t.TempDir(), WorkDir: explicit}).workDir()
	if got != explicit {
		t.Errorf("workDir() = %q, want the explicit %q", got, explicit)
	}
}

// No home is the one case that still falls back to the system temp dir, which
// is what an empty return means to CreateTemp and MkdirTemp.
func TestWorkDirWithoutAHomeFallsBack(t *testing.T) {
	got := (Config{}).workDir()
	if got != "" {
		t.Errorf("workDir() = %q, want empty", got)
	}
}

// The generated script and the overlay must land there, since both are
// bind-mount sources.
func TestGeneratedTempMaterialLandsUnderHome(t *testing.T) {
	home := t.TempDir()
	dir := (Config{Home: home}).workDir()

	f, err := os.CreateTemp(dir, "nsctl-deploy-*.sh")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()
	if !strings.HasPrefix(f.Name(), home) {
		t.Errorf("script at %q, want it under the home at %q", f.Name(), home)
	}
}
