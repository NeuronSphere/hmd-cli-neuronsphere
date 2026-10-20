//go:build !unix

package hostlock

import "os"

// nsctl's local platform runs on macOS and Linux. Elsewhere the lock is a
// no-op, which is the behaviour every platform had before this package.
func tryLock(*os.File) (bool, error) { return true, nil }

func unlock(*os.File) error { return nil }

func lockShared(*os.File) error { return nil }
