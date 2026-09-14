// Package atomicfile replaces a file's contents through a temporary sibling.
//
// Every file nsctl writes under $HMD_HOME is one another process may be
// reading: hmd.env is read by the Python CLI, tokens.yaml by forty repos and
// by internal/librarian. A plain truncate-and-write leaves a window in which
// that reader sees an empty or half-written file, and an interrupted write
// leaves it that way permanently. Renaming over the target closes both.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path with data, creating the directory if it is absent.
//
// dirMode applies only to directories this call creates; an existing one is
// left alone. Files carrying credentials pass 0o700 here and 0o600 as mode,
// so the secret is not readable by other users on a shared machine.
func Write(path string, data []byte, mode, dirMode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Remove is a no-op once the rename below has succeeded, and the only
	// thing that stops a failure part-way leaving litter beside the target.
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Chmod rather than relying on CreateTemp's 0600: the caller may want a
	// wider mode, and an existing file's mode must not silently narrow.
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
