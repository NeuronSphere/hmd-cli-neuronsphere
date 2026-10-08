package lease

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ProcParent reads pid's parent and command name from /proc/<pid>/stat.
func ProcParent(pid int) (int, string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", err
	}
	// "pid (comm) state ppid ...": comm may hold spaces and parentheses, so
	// it is everything between the first "(" and the last ")".
	s := string(data)
	open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return 0, "", fmt.Errorf("unreadable /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[end+1:])
	if len(fields) < 2 {
		return 0, "", fmt.Errorf("unreadable /proc/%d/stat", pid)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, "", fmt.Errorf("unreadable /proc/%d/stat: %w", pid, err)
	}
	return ppid, s[open+1 : end], nil
}
