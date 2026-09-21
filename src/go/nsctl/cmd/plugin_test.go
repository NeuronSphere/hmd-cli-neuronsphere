package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/plugin"
)

// helloScript is the plugin fixture: it prints its argv and the SPEC005
// variables and exits with NSCTL_TEST_EXIT.
const helloScript = `#!/bin/sh
echo "hello argv: $*"
echo "hello env: name=$NSCTL_PLUGIN_NAME version=$NSCTL_PLUGIN_VERSION home=$HMD_HOME nsctl=$NSCTL_VERSION"
exit ${NSCTL_TEST_EXIT:-0}
`

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin fixture")
	}
}

// publishHello pushes a hello plugin for this platform into the fake and
// returns its bare name's expansion host.
func publishHello(t *testing.T, reg *ocitest.Registry, version string) {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "nsctl-hello", Mode: 0o755, Size: int64(len(helloScript)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(helloScript)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	if err := os.WriteFile(filepath.Join(dir, "nsctl-hello_"+version+"_"+plugin.PlatformKey()+".tar.gz"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, plugin.DescriptorFile),
		[]byte(`{"name":"hello","version":"`+version+`","summary":"Say hello"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, blobs, _, err := plugin.Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg.Put("hmdlabs/plugins/hello", version, m, blobs)
}

// runFor executes the tree main would: built-ins plus the plugins declared
// in the home argv names.
func runFor(t *testing.T, env hmdenv.Lookup, args ...string) (string, string, error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	root := NewRootCommandFor("9.9.9", env, args, &errBuf)
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

func TestPluginListWithNoConfigReportsNone(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	out, _, err := run(t, fakeEnv(nil), "--home", home, "plugin", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No plugins declared") {
		t.Errorf("out = %q", out)
	}
	out, _, err = run(t, fakeEnv(nil), "--home", home, "plugin", "list", "--json")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Errorf("--json with none: %q, %v", out, err)
	}
}

func TestPluginVerbsNeedAHome(t *testing.T) {
	t.Parallel()
	for _, verb := range [][]string{{"plugin", "list"}, {"plugin", "install", "hello"}, {"plugin", "remove", "hello"}} {
		_, _, err := run(t, fakeEnv(nil), verb...)
		if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "HMD_HOME") {
			t.Errorf("%v: err = %v", verb, err)
		}
	}
}

func TestPluginInstallListUpdateRemove(t *testing.T) {
	t.Parallel()
	skipOnWindows(t)
	reg := ocitest.New(t)
	publishHello(t, reg, "1.0.0")
	publishHello(t, reg, "1.1.0")
	home := t.TempDir()
	env := fakeEnv(nil)
	ref := reg.Host() + "/hmdlabs/plugins/hello"

	// Install pins to the newest and says so.
	out, errOut, err := run(t, env, "--home", home, "plugin", "install", ref+":1.0.0")
	if err != nil {
		t.Fatalf("install: %v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "Installed hello 1.0.0") || !strings.Contains(out, "nsctl hello --help") {
		t.Errorf("install out = %q", out)
	}
	cfg, err := nsconfig.Load(home, env)
	if err != nil {
		t.Fatal(err)
	}
	decl := cfg.Plugins["hello"]
	if decl.Version != "1.0.0" || decl.Source != "oci://"+ref || !strings.HasPrefix(decl.Digest, "sha256:") {
		t.Errorf("declaration = %+v", decl)
	}
	if _, err := os.Stat(plugin.Binary(plugin.Dir(home, "hello", "1.0.0"), "hello")); err != nil {
		t.Errorf("binary not installed: %v", err)
	}

	out, _, err = run(t, env, "--home", home, "plugin", "list")
	if err != nil || !strings.Contains(out, "hello") || !strings.Contains(out, "installed") {
		t.Errorf("list: %q, %v", out, err)
	}

	// Update resolves the newest tag from the registry.
	out, _, err = run(t, env, "--home", home, "plugin", "update", "hello")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out, "Resolved oci://"+ref+" to 1.1.0") || !strings.Contains(out, "Installed hello 1.1.0") {
		t.Errorf("update out = %q", out)
	}
	if _, err := os.Stat(plugin.Dir(home, "hello", "1.0.0")); !os.IsNotExist(err) {
		t.Error("previous version's directory must be removed on upgrade")
	}

	// The installed plugin runs, argv verbatim, exit status propagated, no
	// "Error:" from nsctl (Execute honours ErrSilent; here we see the code).
	out, errOut, err = runFor(t, env, "--home", home, "hello", "a", "--b", "--help")
	if nserr.CodeOf(err) != nserr.OK {
		t.Fatalf("hello: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "hello argv: a --b --help") || !strings.Contains(out, "name=hello version=1.1.0 home="+home+" nsctl=9.9.9") {
		t.Errorf("hello out = %q", out)
	}

	out, _, err = run(t, env, "--home", home, "plugin", "remove", "hello")
	if err != nil || !strings.Contains(out, "Removed plugin hello") {
		t.Errorf("remove: %q, %v", out, err)
	}
	if _, err := os.Stat(plugin.Root(home)); err == nil {
		if entries, _ := os.ReadDir(plugin.Root(home)); len(entries) != 0 {
			t.Errorf("cache still holds %v", entries)
		}
	}
	cfg, _ = nsconfig.Load(home, env)
	if cfg != nil && len(cfg.Plugins) != 0 {
		t.Errorf("declaration survives remove: %v", cfg.Plugins)
	}
	_, _, err = run(t, env, "--home", home, "plugin", "remove", "hello")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("removing twice: %v", err)
	}
}

func TestPluginInstallRefusesAReservedNoun(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	// A descriptor named after a built-in: nsconfig refuses it before any
	// fetch, so there is nothing to publish here.
	home := t.TempDir()
	_, _, err := run(t, fakeEnv(nil), "--home", home, "plugin", "install", reg.Host()+"/x/env:1.0")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	// It fetched (the name comes from the descriptor, not the ref) and got a
	// 404; either way nothing was declared.
	if _, err := nsconfig.Load(home, fakeEnv(nil)); err == nil {
		t.Error("a failed install must not write nsctl.toml")
	}
}

func TestPluginInstallErrorsCarryTheRemedy(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	home := t.TempDir()
	_, _, err := run(t, fakeEnv(nil), "--home", home, "plugin", "install", reg.Host()+"/hmdlabs/plugins/absent:1.0")
	if nserr.CodeOf(err) != nserr.Fail || !strings.Contains(err.Error(), "private") {
		t.Errorf("absent: %v", err)
	}
	_, _, err = run(t, fakeEnv(nil), "--home", home, "plugin", "install", "github.com/acme/nsctl-foo@1.0")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "NERD016") {
		t.Errorf("github: %v", err)
	}
	_, _, err = run(t, fakeEnv(nil), "--home", home, "plugin", "install", "acme/plugins/foo")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "registry") {
		t.Errorf("no host with a slash must not expand: %v", err)
	}
}

func TestPluginPushRefusesAnonymousBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	dir := t.TempDir()
	_, _, err := run(t, fakeEnv(nil), "--home", t.TempDir(), "plugin", "push", dir, reg.Host()+"/x/hello")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "--token") {
		t.Errorf("err = %v", err)
	}
	if n := len(reg.Requests()); n != 0 {
		t.Errorf("made %d requests", n)
	}
}

func TestPluginPushThenInstallRoundTrip(t *testing.T) {
	t.Parallel()
	skipOnWindows(t)
	reg := ocitest.New(t, ocitest.RequireToken("ci", "pat"))
	// Lay out a release directory.
	dir := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "nsctl-hello", Mode: 0o755, Size: int64(len(helloScript)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(helloScript))
	tw.Close()
	gz.Close()
	if err := os.WriteFile(filepath.Join(dir, "nsctl-hello_2.0.0_"+plugin.PlatformKey()+".tar.gz"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, plugin.DescriptorFile), []byte(`{"name":"hello","version":"2.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	env := fakeEnv(map[string]string{"HMD_REGISTRY_TOKEN": "pat", "HMD_REGISTRY_USER": "ci"})
	out, _, err := run(t, env, "--home", home, "plugin", "push", dir, reg.Host()+"/acme/plugins/hello")
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !strings.Contains(out, "credential from HMD_REGISTRY_TOKEN") || !strings.Contains(out, "Pushed oci://"+reg.Host()+"/acme/plugins/hello:2.0.0") {
		t.Errorf("push out = %q", out)
	}
	// Private registry: the anonymous install fails naming the remedy...
	_, _, err = run(t, fakeEnv(nil), "--home", home, "plugin", "install", reg.Host()+"/acme/plugins/hello")
	if err == nil || !strings.Contains(err.Error(), "HMD_REGISTRY_TOKEN") {
		t.Errorf("anonymous install against a private registry: %v", err)
	}
	// ...and the credentialed one installs the pushed version.
	out, _, err = run(t, env, "--home", home, "plugin", "install", reg.Host()+"/acme/plugins/hello")
	if err != nil || !strings.Contains(out, "Installed hello 2.0.0") {
		t.Errorf("install: %q, %v", out, err)
	}
}

func TestDevPathPluginDispatch(t *testing.T) {
	t.Parallel()
	skipOnWindows(t)
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "nsctl-scratch")
	if err := os.WriteFile(bin, []byte(helloScript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfigAt(t, home, "[plugin.scratch]\npath = \""+bin+"\"\n")

	// --home before the noun is nsctl's; everything after is the plugin's,
	// including a second --home.
	out, errOut, err := runFor(t, fakeEnv(nil), "--home", home, "scratch", "--home", "/theirs", "x")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if !strings.Contains(out, "hello argv: --home /theirs x") || !strings.Contains(out, "version=dev home="+home) {
		t.Errorf("out = %q", out)
	}

	// The exit status comes back as a silent coded error. The variable
	// reaches the plugin through hmd.env, the layer SPEC005 adds beneath the
	// process environment.
	if err := os.WriteFile(filepath.Join(home, ".config", "hmd.env"), []byte("NSCTL_TEST_EXIT=7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, err = runFor(t, fakeEnv(nil), "--home", home, "scratch")
	if nserr.CodeOf(err) != 7 || !nserr.IsSilent(err) {
		t.Errorf("exit 7: err = %v (code %d)", err, nserr.CodeOf(err))
	}
	if strings.Contains(errOut, "Error:") {
		t.Errorf("nsctl must add no message: %q", errOut)
	}
	if err := os.Remove(filepath.Join(home, ".config", "hmd.env")); err != nil {
		t.Fatal(err)
	}

	// Help lists the plugin in its own group.
	out, _, _ = runFor(t, fakeEnv(nil), "--home", home, "--help")
	if !strings.Contains(out, "Plugin commands (declared in nsctl.toml)") || !strings.Contains(out, "scratch") {
		t.Errorf("--help = %q", out)
	}

	// HMD_HOME from the environment works the same way.
	out, _, err = runFor(t, fakeEnv(map[string]string{"HMD_HOME": home}), "scratch", "y")
	if err != nil || !strings.Contains(out, "hello argv: y") {
		t.Errorf("env home: %q, %v", out, err)
	}
}

func TestDeclaredPluginWithoutBinaryNamesTheInstallCommand(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeConfigAt(t, home, "[plugin.hello]\nsource = \"oci://ghcr.io/hmdlabs/plugins/hello\"\nversion = \"1.0\"\ndigest = \"sha256:0\"\n")
	_, _, err := runFor(t, fakeEnv(nil), "--home", home, "hello")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "nsctl plugin install hello") {
		t.Errorf("err = %v", err)
	}
	out, _, err := run(t, fakeEnv(nil), "--home", home, "plugin", "list")
	if err != nil || !strings.Contains(out, "missing binary") {
		t.Errorf("list: %q, %v", out, err)
	}
}

func TestReservedPluginNameWarnsAndTheBuiltinWins(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// nsconfig refuses "env" at parse time, so the whole file is rejected:
	// a warning, no plugins, and the built-in still runs.
	writeConfigAt(t, home, "[plugin.env]\npath = \"/x\"\n")
	out, errOut, err := runFor(t, fakeEnv(nil), "--home", home, "env", "--help")
	if err != nil || !strings.Contains(out, "Usage:") {
		t.Errorf("built-in env: %q, %v", out, err)
	}
	if !strings.Contains(errOut, "warning: plugins not loaded") || !strings.Contains(errOut, "env") {
		t.Errorf("warning = %q", errOut)
	}
}

func TestNoHomeMeansNoPluginsAndAnUnknownNoun(t *testing.T) {
	t.Parallel()
	_, errOut, err := runFor(t, fakeEnv(nil), "hello")
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("err = %v", err)
	}
	if errOut != "" {
		t.Errorf("no warning expected with no home: %q", errOut)
	}
	out, _, err := runFor(t, fakeEnv(nil), "version")
	if err != nil || !strings.Contains(out, "nsctl 9.9.9") {
		t.Errorf("version with nothing set: %q, %v", out, err)
	}
}

func TestMalformedConfigWarnsAndKeepsBuiltins(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeConfigAt(t, home, "this is not toml = = =\n")
	out, errOut, err := runFor(t, fakeEnv(nil), "--home", home, "version")
	if err != nil || !strings.Contains(out, "nsctl 9.9.9") {
		t.Errorf("version: %q, %v", out, err)
	}
	if !strings.Contains(errOut, "warning: plugins not loaded") {
		t.Errorf("warning = %q", errOut)
	}
}

// TestBuiltinNounsListMatchesTheTree keeps nsconfig's copy of the reserved
// nouns honest against the live cobra tree.
func TestBuiltinNounsListMatchesTheTree(t *testing.T) {
	t.Parallel()
	root := NewRootCommand("9.9.9", fakeEnv(nil))
	listed := map[string]bool{}
	for _, n := range nsconfig.BuiltinNouns() {
		listed[n] = true
	}
	for _, c := range root.Commands() {
		if !listed[c.Name()] {
			t.Errorf("built-in %q is not in nsconfig.BuiltinNouns", c.Name())
		}
		for _, a := range c.Aliases {
			if !listed[a] {
				t.Errorf("alias %q is not in nsconfig.BuiltinNouns", a)
			}
		}
	}
}

func TestScanInvocation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		argv []string
		want invocation
	}{
		{[]string{"--home", "/h", "hello", "a", "--home", "/x"}, invocation{home: "/h", noun: "hello", args: []string{"a", "--home", "/x"}}},
		{[]string{"--home=/h", "hello"}, invocation{home: "/h", noun: "hello", args: []string{}}},
		{[]string{"hello"}, invocation{noun: "hello", args: []string{}}},
		{[]string{"--help"}, invocation{}},
		{nil, invocation{}},
	}
	for _, tc := range cases {
		got := scanInvocation(tc.argv)
		if got.home != tc.want.home || got.noun != tc.want.noun || strings.Join(got.args, " ") != strings.Join(tc.want.args, " ") {
			t.Errorf("scanInvocation(%v) = %+v, want %+v", tc.argv, got, tc.want)
		}
	}
}

// writeConfigAt writes nsctl.toml under an existing home.
func writeConfigAt(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nsconfig.Name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
