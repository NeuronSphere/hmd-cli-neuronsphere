package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
)

// script is a plugin that prints its arguments and the SPEC005 variables,
// then exits with the code in NSCTL_TEST_EXIT.
const script = `#!/bin/sh
echo "args: $*"
echo "name=$NSCTL_PLUGIN_NAME version=$NSCTL_PLUGIN_VERSION dir=$NSCTL_PLUGIN_DIR home=$NSCTL_HOME"
exit ${NSCTL_TEST_EXIT:-0}
`

// targz builds an archive of files (name -> content), all mode 0755.
func targz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pluginDir lays out what `plugin push` takes: plugin.json and one tarball
// per platform.
func pluginDir(t *testing.T, name, version string, platforms []string, descriptor string) string {
	t.Helper()
	dir := t.TempDir()
	if descriptor == "" {
		descriptor = `{"name":"` + name + `","version":"` + version + `","summary":"Say hello","min_nsctl_version":"1.0"}`
	}
	if err := os.WriteFile(filepath.Join(dir, DescriptorFile), []byte(descriptor), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range platforms {
		archive := targz(t, map[string]string{BinaryName(name): script, "LICENSE": "MIT"})
		if err := os.WriteFile(filepath.Join(dir, BinaryName(name)+"_"+version+"_"+p+".tar.gz"), archive, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// serve publishes a plugin into the fake registry through Build, the way
// `plugin push` does, and returns its reference.
func serve(t *testing.T, reg *ocitest.Registry, name, version string, platforms []string) oci.Ref {
	t.Helper()
	m, blobs, _, err := Build(pluginDir(t, name, version, platforms, ""))
	if err != nil {
		t.Fatal(err)
	}
	reg.Put("hmdlabs/plugins/"+name, version, m, blobs)
	ref, err := oci.ParseRef(reg.Host() + "/hmdlabs/plugins/" + name + ":" + version)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestBuildFillsPlatformsFromTheArchives(t *testing.T) {
	t.Parallel()
	m, blobs, d, err := Build(pluginDir(t, "hello", "1.2.0", []string{"darwin_arm64", "linux_amd64"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if m.ArtifactType != ArtifactType || m.Config.MediaType != ArtifactType {
		t.Errorf("types = %q / %q", m.ArtifactType, m.Config.MediaType)
	}
	if len(m.Layers) != 2 || m.Layers[0].Annotations[AnnotationOS] != "darwin" || m.Layers[0].Annotations[AnnotationArch] != "arm64" {
		t.Errorf("layers = %+v", m.Layers)
	}
	if len(d.Platforms) != 2 || d.Platforms["linux_amd64"].Digest != m.Layers[1].Digest.String() {
		t.Errorf("platforms = %+v", d.Platforms)
	}
	if _, ok := blobs[m.Config.Digest]; !ok {
		t.Error("config blob missing")
	}
	if m.Annotations["org.opencontainers.image.description"] != "Say hello" {
		t.Errorf("annotations = %v", m.Annotations)
	}
}

func TestBuildRefusesADeclaredPlatformWithNoArchive(t *testing.T) {
	t.Parallel()
	dir := pluginDir(t, "hello", "1.0", []string{"linux_amd64"},
		`{"name":"hello","version":"1.0","platforms":{"windows_amd64":{}}}`)
	if _, _, _, err := Build(dir); err == nil || !strings.Contains(err.Error(), "windows_amd64") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallUnpacksThisPlatformAndDeclares(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	ref := serve(t, reg, "hello", "1.2.0", []string{PlatformKey(), "plan9_mips"})
	home := t.TempDir()

	inst, err := Install(context.Background(), home, oci.New(oci.Credential{}), ref, "1.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if inst.Descriptor.Name != "hello" || inst.Descriptor.Summary != "Say hello" {
		t.Errorf("descriptor = %+v", inst.Descriptor)
	}
	if inst.Dir != Dir(home, "hello", "1.2.0") || inst.Binary != Binary(inst.Dir, "hello") {
		t.Errorf("paths = %s / %s", inst.Dir, inst.Binary)
	}
	info, err := os.Stat(inst.Binary)
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("binary: %v, mode %v", err, info.Mode())
	}
	if _, err := os.Stat(inst.Dir + ".installing"); !os.IsNotExist(err) {
		t.Error("staging directory left behind")
	}
	decl := inst.Declaration(ref)
	if decl.Name != "hello" || decl.Version != "1.2.0" || decl.Digest != inst.Digest.String() ||
		decl.Source != "oci://"+reg.Host()+"/hmdlabs/plugins/hello" {
		t.Errorf("declaration = %+v", decl)
	}
	if inst.Warning != "" {
		t.Errorf("unexpected warning %q", inst.Warning)
	}
	// Re-installing the same version replaces rather than trusts.
	if _, err := Install(context.Background(), home, oci.New(oci.Credential{}), ref, "1.0.5"); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRefusesAMissingPlatformNamingWhatExists(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	ref := serve(t, reg, "hello", "1.0", []string{"plan9_mips", "js_wasm"})
	_, err := Install(context.Background(), t.TempDir(), oci.New(oci.Credential{}), ref, "1.0")
	if !errors.Is(err, ErrNoPlatform) {
		t.Fatalf("err = %v, want ErrNoPlatform", err)
	}
	if !strings.Contains(err.Error(), PlatformKey()) || !strings.Contains(err.Error(), "js_wasm, plan9_mips") {
		t.Errorf("message must name this platform and the published ones: %v", err)
	}
}

func TestMinNsctlVersion(t *testing.T) {
	t.Parallel()
	d := Descriptor{Name: "hello", Version: "1.0", MinNsctlVersion: "1.2"}
	if _, err := d.CheckNsctlVersion("1.1.9"); !errors.Is(err, ErrTooOld) {
		t.Errorf("1.1.9 < 1.2: err = %v", err)
	}
	if _, err := d.CheckNsctlVersion("1.2.0"); err != nil {
		t.Errorf("1.2.0 >= 1.2: %v", err)
	}
	warn, err := d.CheckNsctlVersion("dev")
	if err != nil || !strings.Contains(warn, "dev") {
		t.Errorf("dev build: warn=%q err=%v", warn, err)
	}
	if warn, err := (Descriptor{}).CheckNsctlVersion("0.1"); err != nil || warn != "" {
		t.Errorf("no minimum: warn=%q err=%v", warn, err)
	}
}

func TestInstallRefusesANonPluginArtifact(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	m, blobs, _, err := Build(pluginDir(t, "hello", "1.0", []string{PlatformKey()}, ""))
	if err != nil {
		t.Fatal(err)
	}
	m.ArtifactType, m.Config.MediaType = "application/vnd.neuronsphere.stack.v1+toml", "application/vnd.neuronsphere.lock.v1+toml"
	reg.Put("x/notaplugin", "1.0", m, blobs)
	ref, _ := oci.ParseRef(reg.Host() + "/x/notaplugin:1.0")
	_, err = Install(context.Background(), t.TempDir(), oci.New(oci.Credential{}), ref, "1.0")
	if err == nil || !strings.Contains(err.Error(), "not a plugin") {
		t.Fatalf("err = %v", err)
	}
}

func TestUntarGuards(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()
	if err := untar(targz(t, map[string]string{"../escape": "x"}), dest); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("traversal: %v", err)
	}
	if err := untar([]byte("not gzip"), dest); err == nil {
		t.Error("garbage accepted")
	}
	// A symlink is refused.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	gz.Close()
	if err := untar(buf.Bytes(), dest); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Errorf("symlink: %v", err)
	}
}

func TestResolvePrecedenceAndStates(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dev := filepath.Join(t.TempDir(), "nsctl-scratch")
	if err := os.WriteFile(dev, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// path wins over source.
	both := nsconfig.Plugin{Name: "scratch", Source: "oci://x/y", Version: "9", Path: dev}
	if bin, err := Resolve(home, both); err != nil || bin != dev {
		t.Errorf("Resolve(path+source) = %q, %v", bin, err)
	}
	if StateOf(home, both) != StateDev {
		t.Error("state must be path")
	}
	// A dev path that is gone is an error naming the path.
	if _, err := Resolve(home, nsconfig.Plugin{Name: "gone", Path: "/nowhere/nsctl-gone"}); err == nil || !strings.Contains(err.Error(), "/nowhere/nsctl-gone") {
		t.Errorf("missing dev path: %v", err)
	}
	// Declared but never installed.
	decl := nsconfig.Plugin{Name: "hello", Source: "oci://x/hello", Version: "1.0"}
	if _, err := Resolve(home, decl); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "nsctl plugin install hello") {
		t.Errorf("uninstalled: %v", err)
	}
	if StateOf(home, decl) != StateMissing {
		t.Error("state must be missing binary")
	}
	// Installed.
	dir := Dir(home, "hello", "1.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Binary(dir, "hello"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if StateOf(home, decl) != StateInstalled {
		t.Error("state must be installed")
	}
	if err := Remove(home, "hello"); err != nil {
		t.Fatal(err)
	}
	if StateOf(home, decl) != StateMissing {
		t.Error("Remove must delete the installed directory")
	}
}

func TestRunPassesArgsEnvAndExitStatus(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture")
	}
	bin := filepath.Join(t.TempDir(), "nsctl-hello")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := Environ([]string{"PATH=" + os.Getenv("PATH"), "KEEP=process"},
		map[string]string{"KEEP": "file", "FROM_FILE": "yes"},
		map[string]string{EnvPluginName: "hello", EnvPluginVersion: "1.0", EnvPluginDir: "/d", EnvHome: "/h", "NSCTL_TEST_EXIT": "7"})
	var out, errOut bytes.Buffer
	code, err := Run(bin, []string{"a", "--b", "--help"}, env, strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if !strings.Contains(out.String(), "args: a --b --help") {
		t.Errorf("argv not passed verbatim: %q", out.String())
	}
	if !strings.Contains(out.String(), "name=hello version=1.0 dir=/d home=/h") {
		t.Errorf("SPEC005 variables missing: %q", out.String())
	}
	// Process wins over hmd.env; hmd.env fills what the process lacks.
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "KEEP=process") || !strings.Contains(joined, "FROM_FILE=yes") {
		t.Errorf("Environ = %v", env)
	}
}

func TestRunReportsASignalDeathAs128PlusN(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("signals")
	}
	bin := filepath.Join(t.TempDir(), "nsctl-die")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nkill -TERM $$\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, err := Run(bin, nil, []string{"PATH=" + os.Getenv("PATH")}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code != 128+15 {
		t.Errorf("exit = %d, want 143", code)
	}
}

func TestRunOfAMissingBinaryIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := Run("/nowhere/nsctl-x", nil, nil, nil, nil, nil); err == nil {
		t.Error("expected an error")
	}
}
