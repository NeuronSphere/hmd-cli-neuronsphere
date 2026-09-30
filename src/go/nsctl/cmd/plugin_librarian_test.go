package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/installitems"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// NERD031: `nsctl plugin install librarian:<class>` carries out the install
// section of a RepoClass artifact from the Artifact Librarian.

const kitClass = "acme-kit"

func kitPath(version string) string {
	return librarian.Spec{Name: kitClass, Version: version, ItemType: "build"}.ContentPath()
}

// kitZip is a RepoClass artifact whose manifest carries install.
func kitZip(t *testing.T, version, install string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	all := map[string]string{
		"meta-data/manifest.json": `{"name": "` + kitClass + `", "description": "d", "build": {"commands": []}` +
			map[bool]string{true: `, "install": ` + install, false: ""}[install != ""] + `}`,
		"meta-data/VERSION": version,
	}
	for k, v := range files {
		all[k] = v
	}
	for name, body := range all {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const kitInstall = `{"items": [
  {"kind": "command", "noun": "ops", "summary": "Acme runbooks",
   "runtime": {"kind": "scripts", "scripts": {"drain": "scripts/drain.sh"}}},
  {"kind": "docs", "title": "Acme notes", "dir": "docs", "format": "markdown"},
  {"kind": "agent-skills", "dir": "skills"}]}`

var kitFiles = map[string]string{
	"scripts/drain.sh":         "#!/bin/sh\necho \"drain argv: $*\"\necho \"knowledge=$NSCTL_KNOWLEDGE home=$HMD_HOME noun=$NSCTL_PLUGIN_NAME\"\nexit ${KIT_EXIT:-0}\n",
	"docs/README.md":           "# Notes",
	"skills/acme-ops/SKILL.md": "---\nname: acme-ops\ndescription: Run Acme's runbooks.\n---\n",
}

type kitEnv struct {
	home, user, repos string
	lib               *fakeLibrarian
	vars              map[string]string
}

func newKitEnv(t *testing.T) *kitEnv {
	t.Helper()
	k := &kitEnv{home: t.TempDir(), user: t.TempDir(), repos: t.TempDir(), lib: newFakeLibrarian(t)}
	k.vars = map[string]string{librarian.APIKeyEnv: "k", "HOME": k.user, "HMD_REPO_HOME": k.repos}
	return k
}

func (k *kitEnv) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runFor(t, fakeEnv(k.vars), append([]string{"--home", k.home}, args...)...)
}

func TestPluginInstallFromTheLibrarianPlacesEveryItem(t *testing.T) {
	t.Parallel()
	skipOnWindows(t)
	k := newKitEnv(t)
	k.lib.content[kitPath("1.0.0")] = kitZip(t, "1.0.0", kitInstall, kitFiles)

	out, errOut, err := k.run(t, "plugin", "install", "librarian:"+kitClass+"@1.0.0", "--url", k.lib.URL)
	if err != nil {
		t.Fatalf("install: %v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "Installed "+kitClass+" 1.0.0") || !strings.Contains(out, "nsctl ops --help") {
		t.Errorf("install out = %q", out)
	}

	cfg, err := nsconfig.Load(k.home, fakeEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	decl := cfg.Plugins[kitClass]
	if decl.Source != "librarian:"+kitClass || decl.Version != "1.0.0" || !strings.HasPrefix(decl.Digest, "sha256:") || len(decl.Items) != 3 {
		t.Fatalf("declaration = %+v", decl)
	}
	skill := filepath.Join(k.user, ".claude", "skills", "acme-ops", "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Errorf("skill not installed for the user: %v", err)
	}

	// The noun dispatches, with the scripts named and argv verbatim.
	out, errOut, err = k.run(t, "ops", "drain", "--now", "x")
	if err != nil {
		t.Fatalf("ops drain: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "drain argv: --now x") ||
		!strings.Contains(out, "knowledge="+installitems.KnowledgeDir(k.home)) ||
		!strings.Contains(out, "noun=ops") {
		t.Errorf("ops drain out = %q", out)
	}
	out, _, err = k.run(t, "ops")
	if err != nil || !strings.Contains(out, "drain") {
		t.Errorf("ops alone should list its scripts: %q, %v", out, err)
	}
	_, _, err = k.run(t, "ops", "nope")
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("an unknown script: %v", err)
	}
	// The script's exit status comes back unchanged; the variable reaches it
	// through hmd.env, as it would a NERD018 plugin.
	envFile := filepath.Join(k.home, ".config", "hmd.env")
	if err := os.WriteFile(envFile, []byte("KIT_EXIT=7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = k.run(t, "ops", "drain"); nserr.CodeOf(err) != 7 {
		t.Errorf("exit status: %v (code %d)", err, nserr.CodeOf(err))
	}
	if err := os.Remove(envFile); err != nil {
		t.Fatal(err)
	}

	// Help lists the noun with the item's summary.
	out, _, _ = k.run(t, "--help")
	if !strings.Contains(out, "ops") || !strings.Contains(out, "Acme runbooks") {
		t.Errorf("--help = %q", out)
	}

	// List shows the plugin and its items.
	out, _, err = k.run(t, "plugin", "list")
	if err != nil || !strings.Contains(out, kitClass) || !strings.Contains(out, "ops") || !strings.Contains(out, "Acme notes") {
		t.Errorf("list = %q, %v", out, err)
	}
	out, _, err = k.run(t, "plugin", "list", "--json")
	var rows []pluginRow
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || len(rows[0].Items) != 3 {
		t.Errorf("list --json = %q, %v", out, err)
	}

	// Remove undoes it all.
	out, _, err = k.run(t, "plugin", "remove", kitClass)
	if err != nil || !strings.Contains(out, "Removed plugin "+kitClass) {
		t.Fatalf("remove: %q, %v", out, err)
	}
	if _, err := os.Stat(skill); !os.IsNotExist(err) {
		t.Errorf("skill survived remove: %v", err)
	}
	if entries, _ := os.ReadDir(installitems.InstallsRoot(k.home)); len(entries) != 0 {
		t.Errorf("installs survived remove: %v", entries)
	}
}

func TestPluginUpdateFromTheLibrarianReplacesTheVersion(t *testing.T) {
	t.Parallel()
	skipOnWindows(t)
	k := newKitEnv(t)
	k.lib.content[kitPath("1.0.0")] = kitZip(t, "1.0.0", kitInstall, kitFiles)
	if _, errOut, err := k.run(t, "plugin", "install", "librarian:"+kitClass+"@1.0.0", "--url", k.lib.URL); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	k.lib.content[kitPath("1.1.0")] = kitZip(t, "1.1.0", kitInstall, kitFiles)
	out, errOut, err := k.run(t, "plugin", "update", kitClass, "--url", k.lib.URL)
	if err != nil {
		t.Fatalf("update: %v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "Installed "+kitClass+" 1.1.0") {
		t.Errorf("update out = %q", out)
	}
	if _, err := os.Stat(installitems.VersionDir(k.home, kitClass, "1.0.0")); !os.IsNotExist(err) {
		t.Errorf("the previous version was not pruned: %v", err)
	}
	if _, err := os.Stat(installitems.VersionDir(k.home, kitClass, "1.1.0")); err != nil {
		t.Errorf("the new version is not installed: %v", err)
	}
}

func TestPluginInstallRefusesAnArtifactWithNothingToInstall(t *testing.T) {
	t.Parallel()
	k := newKitEnv(t)
	k.lib.content[kitPath("1.0.0")] = kitZip(t, "1.0.0", "", nil)
	_, _, err := k.run(t, "plugin", "install", "librarian:"+kitClass+"@1.0.0", "--url", k.lib.URL)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "nothing to install") {
		t.Fatalf("err = %v", err)
	}
	if cfg, _ := nsconfig.Load(k.home, fakeEnv(nil)); cfg != nil && len(cfg.Plugins) != 0 {
		t.Errorf("declared %v", cfg.Plugins)
	}
}

func TestPluginInstallRefusesANounAnotherPluginOwns(t *testing.T) {
	t.Parallel()
	k := newKitEnv(t)
	writeConfigAt(t, k.home, "[plugin.ops]\npath = \"/bin/true\"\n")
	k.lib.content[kitPath("1.0.0")] = kitZip(t, "1.0.0", kitInstall, kitFiles)
	_, _, err := k.run(t, "plugin", "install", "librarian:"+kitClass+"@1.0.0", "--url", k.lib.URL)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "ops") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(installitems.InstallsRoot(k.home)); len(entries) != 0 {
		t.Errorf("wrote %v before refusing", entries)
	}
}

func TestPluginInstallRefusesABuiltinNoun(t *testing.T) {
	t.Parallel()
	k := newKitEnv(t)
	install := `{"items": [{"kind": "command", "noun": "env", "summary": "s",
	  "runtime": {"kind": "scripts", "scripts": {"a": "scripts/drain.sh"}}}]}`
	k.lib.content[kitPath("1.0.0")] = kitZip(t, "1.0.0", install, kitFiles)
	_, _, err := k.run(t, "plugin", "install", "librarian:"+kitClass+"@1.0.0", "--url", k.lib.URL)
	if nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "env") {
		t.Fatalf("err = %v", err)
	}
}

// A python item end to end against a fake uv: the environment it makes is
// what `nsctl hmd` runs, through the launcher, with no console script.
func TestPluginPythonItemRunsThroughTheLauncher(t *testing.T) {
	t.Parallel()
	skipOnWindows(t)
	k := newKitEnv(t)
	bin := t.TempDir()
	uv := `#!/bin/sh
case "$1" in
venv) shift; for a in "$@"; do dir="$a"; done
  mkdir -p "$dir/bin"
  printf '#!/bin/sh\ncase "$2" in *importlib*) exit 0;; esac\necho "python $*"\n' > "$dir/bin/python"
  chmod 755 "$dir/bin/python"
  printf 'from hmd.main import main\n' > "$dir/bin/hmd";;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "uv"), []byte(uv), 0o755); err != nil {
		t.Fatal(err)
	}
	k.vars["PATH"] = bin + ":/usr/bin:/bin"
	install := `{"requires": [{"binary": "uv", "install_hint": "brew install uv"}], "items": [
	  {"kind": "command", "noun": "hmd", "summary": "The Python hmd toolset",
	   "runtime": {"kind": "python", "python": "3.12", "lock": "requirements.lock", "entry_point": "hmd.main:main"}}]}`
	k.lib.content[kitPath("1.0.0")] = kitZip(t, "1.0.0", install,
		map[string]string{"requirements.lock": "hmd-cli-app==1.0 --hash=sha256:ab\n"})
	if out, errOut, err := k.run(t, "plugin", "install", "librarian:"+kitClass+"@1.0.0", "--url", k.lib.URL); err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	out, errOut, err := k.run(t, "hmd", "repo", "create", "--x")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if !strings.Contains(out, "from hmd.main import main") || !strings.HasSuffix(strings.TrimSpace(out), "repo create --x") {
		t.Errorf("hmd out = %q", out)
	}
	py := filepath.Join(installitems.VersionDir(k.home, kitClass, "1.0.0"), "hmd", "env", "bin")
	if _, err := os.Stat(filepath.Join(py, "hmd")); !os.IsNotExist(err) {
		t.Errorf("the console script survived: %v", err)
	}
}

// NERD031 SPEC001: an author finds an install section's mistakes with
// `repoclass validate`, before a user finds them with `plugin install`.
func TestRepoClassValidateChecksTheInstallSection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustRepoclass(t, dir, "init", "acme-kit", "--description", "d")
	manifest := filepath.Join(dir, "meta-data", "manifest.json")
	body, _ := os.ReadFile(manifest)
	bad := strings.Replace(string(body), `"name": "acme-kit",`, `"name": "acme-kit",
  "install": {"items": [{"kind": "command", "noun": "hmd", "summary": "s",
    "runtime": {"kind": "python", "python": "3.12", "lock": "requirements.lock", "entry_point": "hmd.main:main"}}]},`, 1)
	if err := os.WriteFile(manifest, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.lock"), []byte("hmd-cli-app==1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := rc(t, dir, "validate")
	if err == nil || !strings.Contains(out, "install") || !strings.Contains(out, "--hash") {
		t.Errorf("validate did not flag the unhashed lock: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.lock"), []byte("hmd-cli-app==1.0 --hash=sha256:ab\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _, err := rc(t, dir, "validate"); err != nil {
		t.Errorf("a valid install section failed validate: %v\n%s", err, out)
	}
}
