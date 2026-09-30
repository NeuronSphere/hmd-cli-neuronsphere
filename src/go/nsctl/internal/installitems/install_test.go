package installitems

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/agentskills"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
)

// fakeUV stands in for uv. venv makes an environment whose python answers
// the smoke test with $FAKE_IMPORT_EXIT and otherwise echoes its argv, and
// drops a console script the way uv's template writes one; pip sync fails
// like a private index refusing a credential when $FAKE_UV_401 is set.
const fakeUV = `#!/bin/sh
echo "UV_CONFIG_FILE=${UV_CONFIG_FILE:-}" >> "$FAKE_UV_LOG"
case "$1" in
venv)
  shift
  for a in "$@"; do dir="$a"; done
  mkdir -p "$dir/bin"
  cat > "$dir/bin/python" <<'PY'
#!/bin/sh
case "$2" in *importlib*) exit ${FAKE_IMPORT_EXIT:-0};; esac
echo "python $*"
PY
  chmod 755 "$dir/bin/python"
  printf '#!%s/bin/python\nimport sys\nfrom hmd.main import main\nsys.exit(main())\n' "$dir" > "$dir/bin/hmd"
  chmod 755 "$dir/bin/hmd"
  ;;
pip)
  if [ -n "$FAKE_UV_401" ]; then
    echo "Resolving against https://alice:s3cret@idx.example/simple" >&2
    echo "error: HTTP status client error (401 Unauthorized) for url (https://alice:s3cret@idx.example/simple/hmd-cli-app/)" >&2
    exit 2
  fi
  ;;
esac
`

type fixture struct {
	t       *testing.T
	home    string
	userDir string
	repos   string
	bin     string
	log     string
	extra   []string
	stderr  bytes.Buffer
}

func newFixture(t *testing.T, withUV bool) *fixture {
	t.Helper()
	f := &fixture{t: t, home: t.TempDir(), userDir: t.TempDir(), repos: t.TempDir(), bin: t.TempDir()}
	f.log = filepath.Join(t.TempDir(), "uv.log")
	if withUV {
		if err := os.WriteFile(filepath.Join(f.bin, "uv"), []byte(fakeUV), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"git", "sh", "bash", "mkdir", "chmod", "cat"} {
		if p, err := exec.LookPath(tool); err == nil {
			if err := os.Symlink(p, filepath.Join(f.bin, tool)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}

func (f *fixture) env() Env {
	environ := append([]string{
		"PATH=" + f.bin,
		"HOME=" + f.userDir,
		"FAKE_UV_LOG=" + f.log,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}, f.extra...)
	return Env{
		Home:     f.home,
		RepoHome: f.repos,
		Environ:  environ,
		Stdout:   &bytes.Buffer{},
		Stderr:   &f.stderr,
		Skills:   SkillTarget{Host: agentskills.Claude, Scope: agentskills.User, UserHome: f.userDir},
	}
}

func (f *fixture) install(root string) ([]nsconfig.PluginItem, error) {
	f.t.Helper()
	sec, err := ParseDir(root)
	if err != nil {
		f.t.Fatal(err)
	}
	return Install(context.Background(), f.env(), "kit", "1.0.0", root, sec)
}

func manifestTree(t *testing.T, install string, files map[string]string) string {
	t.Helper()
	all := map[string]string{
		"meta-data/manifest.json": `{"name": "kit", "description": "d", "build": {"commands": []}, "install": ` + install + `}`,
		"meta-data/VERSION":       "1.0",
	}
	for k, v := range files {
		all[k] = v
	}
	return tree(t, all)
}

const pythonItem = `{"kind": "command", "noun": "hmd", "summary": "The hmd toolset",
  "runtime": {"kind": "python", "python": "3.12", "lock": "requirements.lock", "entry_point": "hmd.main:main"},
  "index_hint": "Configure the hmd-cli-* index in $HMD_HOME/.config/uv.toml"}`

func pythonTree(t *testing.T, extraItems string, files map[string]string) string {
	items := pythonItem
	if extraItems != "" {
		items = extraItems + ", " + pythonItem
	}
	all := map[string]string{"requirements.lock": hashedLock}
	for k, v := range files {
		all[k] = v
	}
	return manifestTree(t, `{"requires": [{"binary": "uv", "install_hint": "brew install uv"}], "items": [`+items+`]}`, all)
}

func installsOf(t *testing.T, home string) []string {
	t.Helper()
	entries, _ := os.ReadDir(InstallsRoot(home))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestPythonItemInstallsAndLeavesNoConsoleScript(t *testing.T) {
	t.Parallel()
	f := newFixture(t, true)
	items, err := f.install(pythonTree(t, "", nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, f.stderr.String())
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	it := items[0]
	want := filepath.Join(InstallsRoot(f.home), "kit@1.0.0", "hmd", "env", "bin", "python")
	if it.Target != want || it.Noun != "hmd" || it.Runtime != nsconfig.RuntimePython || it.Summary != "The hmd toolset" {
		t.Errorf("item = %+v", it)
	}
	if len(it.Args) != 2 || it.Args[0] != "-c" || !strings.Contains(it.Args[1], "from hmd.main import main") {
		t.Errorf("launcher = %v", it.Args)
	}
	if it.LockSHA256 == "" {
		t.Error("the lock's hash was not recorded")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(want), "hmd")); !os.IsNotExist(err) {
		t.Errorf("the hmd console script survived: %v", err)
	}
	if got := installsOf(t, f.home); strings.Join(got, ",") != "kit@1.0.0" {
		t.Errorf("installs = %v; a staging directory was left behind", got)
	}
}

func TestMissingRequirementsAreReportedTogetherWithTheirHints(t *testing.T) {
	t.Parallel()
	f := newFixture(t, false)
	root := manifestTree(t, `{"requires": [
	    {"binary": "uv", "install_hint": "brew install uv"},
	    {"binary": "no-such-tool", "install_hint": "ask the platform team"}],
	  "items": [`+pythonItem+`]}`, map[string]string{"requirements.lock": hashedLock})
	_, err := f.install(root)
	if err == nil {
		t.Fatal("installed without uv")
	}
	for _, want := range []string{"brew install uv", "ask the platform team"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not carry %q:\n%v", want, err)
		}
	}
	if got := installsOf(t, f.home); len(got) != 0 {
		t.Errorf("wrote %v before the preflight passed", got)
	}
}

func TestARefusedIndexIsRedactedAndNamesTheHint(t *testing.T) {
	t.Parallel()
	f := newFixture(t, true)
	f.extra = []string{"FAKE_UV_401=1"}
	_, err := f.install(pythonTree(t, "", nil))
	if err == nil {
		t.Fatal("installed against a refusing index")
	}
	all := err.Error() + f.stderr.String()
	if strings.Contains(all, "s3cret") || strings.Contains(all, "alice") {
		t.Errorf("the index credential leaked:\n%s", all)
	}
	for _, want := range []string{"$HMD_HOME/.config/uv.toml", "UV_INDEX_URL", "idx.example"} {
		if !strings.Contains(all, want) {
			t.Errorf("output does not mention %q:\n%s", want, all)
		}
	}
	if got := installsOf(t, f.home); len(got) != 0 {
		t.Errorf("a failed provision left %v", got)
	}
}

func TestUVExitingZeroWithoutAWorkingEntryPointIsAFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t, true)
	f.extra = []string{"FAKE_IMPORT_EXIT=1"}
	if _, err := f.install(pythonTree(t, "", nil)); err == nil {
		t.Fatal("an environment whose entry point does not import was accepted")
	}
	if got := installsOf(t, f.home); len(got) != 0 {
		t.Errorf("left %v", got)
	}
}

func TestUVConfigFileDefaultsToTheHomesOwn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, true)
	cfg := filepath.Join(f.home, ".config", "uv.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("index-url = 'x'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.install(pythonTree(t, "", nil)); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(f.log)
	if !strings.Contains(string(log), "UV_CONFIG_FILE="+cfg) {
		t.Errorf("uv did not see the home's uv.toml:\n%s", log)
	}

	g := newFixture(t, true)
	g.extra = []string{"UV_CONFIG_FILE=/mine/uv.toml"}
	if err := os.MkdirAll(filepath.Join(g.home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(g.home, ".config", "uv.toml"), []byte(""), 0o600)
	if _, err := g.install(pythonTree(t, "", nil)); err != nil {
		t.Fatal(err)
	}
	log, _ = os.ReadFile(g.log)
	if !strings.Contains(string(log), "UV_CONFIG_FILE=/mine/uv.toml") {
		t.Errorf("a UV_CONFIG_FILE the user set was overridden:\n%s", log)
	}
}

func TestScriptsDocsAndSkillsFromTheArtifact(t *testing.T) {
	t.Parallel()
	f := newFixture(t, false)
	root := manifestTree(t, `{"items": [
	    {"kind": "command", "noun": "ops", "summary": "Runbooks",
	     "runtime": {"kind": "scripts", "scripts": {"drain": "scripts/drain.sh"}}},
	    {"kind": "docs", "title": "Authoring", "dir": "docs", "format": "rst"},
	    {"kind": "agent-skills", "dir": "skills"}]}`, map[string]string{
		"scripts/drain.sh":           "#!/bin/sh\necho drained \"$@\"\n",
		"scripts/unlisted.sh":        "#!/bin/sh\n",
		"docs/index.rst":             "Hello",
		"skills/kit-helper/SKILL.md": "---\nname: kit-helper\ndescription: Helps.\n---\n",
		"skills/kit-helper/ref/a.md": "ref",
	})
	items, err := f.install(root)
	if err != nil {
		t.Fatalf("%v\n%s", err, f.stderr.String())
	}
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	ops := items[0]
	drain := ops.Scripts["drain"]
	if drain == "" || len(ops.Scripts) != 1 {
		t.Fatalf("scripts = %v; only named scripts are commands", ops.Scripts)
	}
	if info, err := os.Stat(drain); err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("drain is not an executable file: %v", err)
	}
	docs := items[1]
	if b, err := os.ReadFile(filepath.Join(docs.Path, "index.rst")); err != nil || string(b) != "Hello" {
		t.Errorf("docs not placed at %s: %v", docs.Path, err)
	}
	idx, err := ReadKnowledgeIndex(f.home)
	if err != nil || len(idx.Entries) != 1 || idx.Entries[0].Title != "Authoring" || idx.Entries[0].Path != docs.Path {
		t.Errorf("knowledge index = %+v, %v", idx, err)
	}
	skills := items[2]
	want := filepath.Join(f.userDir, ".claude", "skills", "kit-helper")
	if len(skills.Paths) != 1 || skills.Paths[0] != want {
		t.Errorf("skill paths = %v", skills.Paths)
	}
	if _, err := os.Stat(filepath.Join(want, "ref", "a.md")); err != nil {
		t.Errorf("skill resources missing: %v", err)
	}
}

func TestAFailureUndoesEverythingThisRunPlaced(t *testing.T) {
	t.Parallel()
	f := newFixture(t, true)
	f.extra = []string{"FAKE_IMPORT_EXIT=1"}
	root := pythonTree(t, `{"kind": "docs", "title": "Authoring", "dir": "docs"},
	    {"kind": "agent-skills", "dir": "skills"}`, map[string]string{
		"docs/index.rst":             "Hello",
		"skills/kit-helper/SKILL.md": "---\nname: kit-helper\n---\n",
	})
	if _, err := f.install(root); err == nil {
		t.Fatal("installed with a broken entry point")
	}
	if got := installsOf(t, f.home); len(got) != 0 {
		t.Errorf("installs left: %v", got)
	}
	if _, err := os.Stat(filepath.Join(f.userDir, ".claude", "skills", "kit-helper")); !os.IsNotExist(err) {
		t.Errorf("a skill from a failed install is live: %v", err)
	}
	if idx, _ := ReadKnowledgeIndex(f.home); len(idx.Entries) != 0 {
		t.Errorf("the knowledge index names a failed install: %+v", idx)
	}
}

func TestANounTheHostRefusesStopsTheInstallFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t, true)
	env := f.env()
	env.CheckNoun = func(noun string) error {
		if noun == "hmd" {
			return os.ErrExist
		}
		return nil
	}
	sec, _ := ParseDir(pythonTree(t, "", nil))
	if _, err := Install(context.Background(), env, "kit", "1.0.0", pythonTree(t, "", nil), sec); err == nil {
		t.Fatal("installed a noun the host refused")
	}
	if log, _ := os.ReadFile(f.log); len(log) != 0 {
		t.Errorf("uv ran before the noun check:\n%s", log)
	}
}

func TestRemoveKeepsEditedSkillsAndDropsTheRest(t *testing.T) {
	t.Parallel()
	f := newFixture(t, false)
	root := manifestTree(t, `{"items": [
	    {"kind": "docs", "title": "Authoring", "dir": "docs"},
	    {"kind": "agent-skills", "dir": "skills"}]}`, map[string]string{
		"docs/index.rst":        "Hello",
		"skills/kit-a/SKILL.md": "---\nname: kit-a\n---\n",
		"skills/kit-b/SKILL.md": "---\nname: kit-b\n---\n",
	})
	items, err := f.install(root)
	if err != nil {
		t.Fatal(err)
	}
	edited := filepath.Join(f.userDir, ".claude", "skills", "kit-b")
	if err := os.WriteFile(filepath.Join(edited, "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Remove(f.home, "kit", items, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.userDir, ".claude", "skills", "kit-a")); !os.IsNotExist(err) {
		t.Errorf("an unedited skill survived remove: %v", err)
	}
	if _, err := os.Stat(edited); err != nil {
		t.Errorf("an edited skill was deleted: %v", err)
	}
	if !strings.Contains(out.String(), edited) {
		t.Errorf("remove did not say it kept %s:\n%s", edited, out.String())
	}
	if got := installsOf(t, f.home); len(got) != 0 {
		t.Errorf("installs left: %v", got)
	}
	if idx, _ := ReadKnowledgeIndex(f.home); len(idx.Entries) != 0 {
		t.Errorf("index still names the plugin: %+v", idx)
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()
	got := Redact("fetch https://alice:s3cret@idx.example/simple and http://tok@x.io/p and git@github.com:a/b")
	if strings.Contains(got, "s3cret") || strings.Contains(got, "alice") || strings.Contains(got, "tok@") {
		t.Errorf("Redact = %q", got)
	}
	if !strings.Contains(got, "idx.example") || !strings.Contains(got, "git@github.com:a/b") {
		t.Errorf("Redact removed more than userinfo: %q", got)
	}
}

func TestAnUnbuildableLockNamesThePython(t *testing.T) {
	t.Parallel()
	it := Item{Noun: "hmd", Runtime: &Runtime{Kind: "python", Python: "3.12", Lock: "requirements.lock"}}
	err := uvFailure(it, "uv pip sync", "  ╰─▶ Call to `setuptools.build_meta.build_wheel` failed (exit status: 1)", os.ErrInvalid)
	if !strings.Contains(err.Error(), "no wheel for Python 3.12") || !strings.Contains(err.Error(), "regenerate") {
		t.Errorf("err = %v", err)
	}
}
