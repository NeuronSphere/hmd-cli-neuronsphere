package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesTheDirectoryAndTheFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "deeper", "file.txt")
	if err := Write(path, []byte("hello"), 0o600, 0o700); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("directory mode = %o, want 700", perm)
	}
}

func TestWriteReplacesAndLeavesNoLitter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := Write(path, []byte("first"), 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("second"), 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "second" {
		t.Errorf("content = %q, want the replacement", data)
	}

	// The temporary sibling must be gone: a directory accumulating .file.txt.*
	// after every write is how a token cache turns into a pile of secrets.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the target", names)
	}
}

// An existing file's mode is set from the argument, not inherited, so a file
// previously written 0644 by another tool is narrowed rather than left open.
func TestWriteAppliesTheModeToAnExistingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestWriteReportsAnUnwritableDirectory(t *testing.T) {
	t.Parallel()

	// A path whose parent is an existing regular file cannot be created.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := Write(filepath.Join(blocker, "file.txt"), []byte("x"), 0o600, 0o700)
	if err == nil {
		t.Fatal("Write() succeeded through a regular file")
	}
}
