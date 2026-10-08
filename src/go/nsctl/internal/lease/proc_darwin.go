package lease

import (
	"golang.org/x/sys/unix"
)

// ProcParent reads pid's parent and command name from the kernel with sysctl,
// not by running ps: a sandboxed caller may not be allowed to exec it.
func ProcParent(pid int) (int, string, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, "", err
	}
	return int(kp.Eproc.Ppid), unix.ByteSliceToString(kp.Proc.P_comm[:]), nil
}
