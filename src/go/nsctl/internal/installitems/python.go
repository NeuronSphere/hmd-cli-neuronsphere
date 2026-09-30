package installitems

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
)

// indexVariables are what uv reads for a package index. Not PIP_*: uv
// ignores pip's variables, and a user whose index is configured only for pip
// has not configured it for this (SPEC006).
const indexVariables = "UV_INDEX_URL, UV_EXTRA_INDEX_URL or UV_CONFIG_FILE"

// uvEnviron is the environment uv runs in: the composed environment, plus
// UV_CONFIG_FILE pointing at the home's own uv.toml when that file exists and
// the user has not chosen one. That file is where `hmd python login` writes
// its index configuration; nsctl names it and never reads it.
func uvEnviron(env Env) []string {
	out := append([]string(nil), env.Environ...)
	if _, set := getenv(out, "UV_CONFIG_FILE"); set {
		return out
	}
	cfg := filepath.Join(env.Home, ".config", "uv.toml")
	if _, err := os.Stat(cfg); err == nil {
		out = append(out, "UV_CONFIG_FILE="+cfg)
	}
	return out
}

// placePython provisions a command + python item into stage, whose final
// home is final (SPEC005). The environment is created --relocatable so the
// staged directory can be renamed into place.
func placePython(ctx context.Context, env Env, root, stage, final string, it Item) (nsconfig.PluginItem, error) {
	rt := it.Runtime
	uv, _ := lookPath("uv", env.Environ)
	lock := filepath.Join(root, filepath.FromSlash(rt.Lock))
	data, err := os.ReadFile(lock)
	if err != nil {
		return nsconfig.PluginItem{}, err
	}
	sum := sha256.Sum256(data)
	environ := uvEnviron(env)
	envDir := filepath.Join(stage, "env")
	py := filepath.Join(envDir, "bin", "python")

	fmt.Fprintf(env.Stdout, "Creating a Python %s environment for %s with uv\n", rt.Python, it.Noun)
	if tail, err := run(ctx, env, environ, root, uv, "venv", "--relocatable", "--python", rt.Python, envDir); err != nil {
		return nsconfig.PluginItem{}, uvFailure(it, "uv venv", tail, err)
	}
	fmt.Fprintf(env.Stdout, "Installing %s into it\n", rt.Lock)
	if tail, err := run(ctx, env, environ, root, uv, "pip", "sync", "--python", py, "--require-hashes", lock); err != nil {
		return nsconfig.PluginItem{}, uvFailure(it, "uv pip sync", tail, err)
	}

	module, function, _ := strings.Cut(rt.EntryPoint, ":")
	probe := fmt.Sprintf("import importlib; getattr(importlib.import_module(%q), %q)", module, function)
	if tail, err := run(ctx, env, env.Environ, root, py, "-c", probe); err != nil {
		return nsconfig.PluginItem{}, fmt.Errorf("%s: uv succeeded, but the entry point %s does not import (%v)\n%s",
			it.Noun, rt.EntryPoint, err, tail)
	}
	if err := removeConsoleScripts(filepath.Join(envDir, "bin"), module, function); err != nil {
		return nsconfig.PluginItem{}, err
	}

	return nsconfig.PluginItem{
		Kind:       nsconfig.KindCommand,
		Runtime:    nsconfig.RuntimePython,
		Noun:       it.Noun,
		Summary:    it.Summary,
		Path:       final,
		Target:     filepath.Join(final, "env", "bin", "python"),
		Args:       []string{"-c", Launcher(it.Noun, module, function)},
		LockSHA256: hex.EncodeToString(sum[:]),
	}, nil
}

// Launcher is the -c program that runs module:function as noun. With the
// console script deleted, this is the only way the entry point is reached,
// which is what leaves no executable named for the noun anywhere (SPEC013).
func Launcher(noun, module, function string) string {
	return fmt.Sprintf("import sys; from %s import %s as _main; sys.argv[0] = %q; sys.exit(_main())", module, function, noun)
}

// removeConsoleScripts deletes every script in bin that uv generated for the
// entry point. uv's template imports it by name, so that line identifies one
// regardless of what the distribution called its script.
func removeConsoleScripts(bin, module, function string) error {
	entries, err := os.ReadDir(bin)
	if err != nil {
		return err
	}
	needle := []byte("from " + module + " import " + function)
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		p := filepath.Join(bin, e.Name())
		info, err := e.Info()
		if err != nil || info.Size() > 64<<10 {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), string(needle)) {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// uvFailure classifies what went wrong. The tail is already redacted.
func uvFailure(it Item, step, tail string, err error) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s failed (%v)", it.Noun, step, err)
	switch {
	case strings.Contains(tail, "401") || strings.Contains(tail, "403") ||
		strings.Contains(tail, "Unauthorized") || strings.Contains(tail, "Forbidden"):
		b.WriteString("\nThe package index refused the request: the credential is missing or rejected.")
		if it.IndexHint != "" {
			b.WriteString("\n  " + it.IndexHint)
		}
		b.WriteString("\n  uv reads " + indexVariables + " (not PIP_*); nsctl never stores the credential.")
	case strings.Contains(tail, "hash") && strings.Contains(tail, "mismatch"):
		b.WriteString("\nA downloaded distribution does not match the lock's hash: " + it.Runtime.Lock + ".")
	case strings.Contains(tail, "No solution found") || strings.Contains(tail, "not found in the package registry") ||
		strings.Contains(tail, "was not found in"):
		b.WriteString("\nThe lock names something the configured index does not have: " + it.Runtime.Lock + ".")
		if it.IndexHint != "" {
			b.WriteString("\n  " + it.IndexHint)
		}
	case strings.Contains(tail, "dns error") || strings.Contains(tail, "Connection") ||
		strings.Contains(tail, "connect") || strings.Contains(tail, "offline"):
		b.WriteString("\nThe package index could not be reached.")
	}
	if tail != "" {
		b.WriteString("\n--- uv said (last lines) ---\n" + tail)
	}
	return fmt.Errorf("%s", b.String())
}
