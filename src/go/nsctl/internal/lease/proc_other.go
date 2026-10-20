//go:build !darwin && !linux

package lease

import "errors"

// ProcParent cannot read the process table here, so SessionPID falls back to
// the caller's parent.
func ProcParent(int) (int, string, error) {
	return 0, "", errors.New("reading the process table is not supported on this platform")
}
