package plugin

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
)

// Variables SPEC005 sets for a plugin.
const (
	EnvHome          = "NSCTL_HOME"
	EnvVersion       = "NSCTL_VERSION"
	EnvBinary        = "NSCTL_BINARY"
	EnvPluginName    = "NSCTL_PLUGIN_NAME"
	EnvPluginVersion = "NSCTL_PLUGIN_VERSION"
	EnvPluginDir     = "NSCTL_PLUGIN_DIR"
)

// Environ builds the child's environment: the process environment, then
// every hmdEnv key the process did not set, then extra, which always wins.
// Sorted, so a test can compare it and a plugin sees a stable order.
func Environ(process []string, hmdEnv map[string]string, extra map[string]string) []string {
	merged := map[string]string{}
	for _, kv := range process {
		k, v, _ := strings.Cut(kv, "=")
		merged[k] = v
	}
	for k, v := range hmdEnv {
		if _, set := merged[k]; !set {
			merged[k] = v
		}
	}
	for k, v := range extra {
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// Run executes bin with args and returns its exit status. A signal death is
// 128+n, as a shell reports it. err is set only when the process could not
// be started; a non-zero exit is not an error here, it is the plugin's answer.
func Run(bin string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	return -1, err
}

// Executable is this nsctl's own path, or "" when the OS cannot say.
func Executable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
