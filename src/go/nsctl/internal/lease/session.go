package lease

import "path/filepath"

// ParentFunc reads one row of the process table: pid's parent and its command
// name. ProcParent is the real one.
type ParentFunc func(pid int) (ppid int, name string, err error)

// SessionPID is the process whose exit should end a session lease taken by a
// command whose parent is parent (NERD035 SPEC002).
//
// A Claude Code session runs every command in a shell of its own that exits
// with the command, so watching the immediate parent would end the lease the
// moment `acquire` returned. The session is the nearest `claude` ancestor.
// Without one -- a person at a terminal -- the parent is their interactive
// shell, and is the answer. An unreadable process table also answers the
// parent: a lease that ends early is a nuisance, one that never ends is not.
func SessionPID(parent int, read ParentFunc) int {
	if read == nil {
		return parent
	}
	pid := parent
	for i := 0; i < 64 && pid > 1; i++ {
		ppid, name, err := read(pid)
		if err != nil {
			return parent
		}
		if filepath.Base(name) == "claude" {
			return pid
		}
		pid = ppid
	}
	return parent
}
