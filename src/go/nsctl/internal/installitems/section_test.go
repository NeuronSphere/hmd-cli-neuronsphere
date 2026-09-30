package installitems

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const hashedLock = "hmd-cli-app==1.2.3 \\\n    --hash=sha256:aaaa\n"

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestParseReadsTheSection(t *testing.T) {
	t.Parallel()
	sec, err := Parse([]byte(`{"name": "kit", "install": {
	  "requires": [{"binary": "uv", "install_hint": "brew install uv"}],
	  "items": [
	    {"kind": "command", "noun": "hmd", "summary": "s",
	     "runtime": {"kind": "python", "python": "3.12", "lock": "requirements.lock", "entry_point": "hmd.main:main"}},
	    {"kind": "docs", "title": "Notes", "source": {"git": "https://x/notes.git", "ref": "main"}}
	  ]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sec.Items) != 2 || sec.Items[0].Runtime.EntryPoint != "hmd.main:main" || sec.Items[1].Source.Git == "" {
		t.Errorf("sec = %+v", sec)
	}
}

func TestParseWithoutASectionIsErrNoSection(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte(`{"name": "trino", "deploy": {}}`)); !errors.Is(err, ErrNoSection) {
		t.Errorf("err = %v", err)
	}
}

func TestParseRefusesUnknownKeys(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(`{"install": {"items": [{"kind": "docs", "title": "x", "dir": "d", "exec": "rm -rf /"}]}}`))
	if err == nil {
		t.Fatal("an unknown key inside install parsed; a newer artifact would half-install")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{
		"requirements.lock":     hashedLock,
		"unhashed.lock":         "hmd-cli-app==1.2.3\n",
		"scripts/drain.sh":      "#!/bin/sh\n",
		"skills/a/SKILL.md":     "---\nname: a\n---\n",
		"docs/index.rst":        "x",
		"bin/darwin_arm64/tool": "x",
	})
	good := `{"kind": "command", "noun": "hmd", "summary": "s", "runtime": {"kind": "python", "python": "3.12", "lock": "requirements.lock", "entry_point": "hmd.main:main"}}`
	cases := map[string]struct {
		items string
		want  string // "" means valid
	}{
		"python ok":         {good, ""},
		"scripts ok":        {`{"kind": "command", "noun": "ops", "summary": "s", "runtime": {"kind": "scripts", "scripts": {"drain": "scripts/drain.sh"}}}`, ""},
		"skills ok":         {`{"kind": "agent-skills", "dir": "skills"}`, ""},
		"docs git ok":       {`{"kind": "docs", "title": "t", "source": {"git": "https://x/y.git"}}`, ""},
		"unknown kind":      {`{"kind": "exec"}`, "unknown kind"},
		"unknown runtime":   {`{"kind": "command", "noun": "x", "summary": "s", "runtime": {"kind": "node"}}`, "unknown runtime"},
		"no noun":           {`{"kind": "command", "summary": "s", "runtime": {"kind": "scripts", "scripts": {"a": "scripts/drain.sh"}}}`, "noun"},
		"reserved noun":     {`{"kind": "command", "noun": "env", "summary": "s", "runtime": {"kind": "scripts", "scripts": {"a": "scripts/drain.sh"}}}`, "built-in"},
		"noun twice":        {good + "," + good, "twice"},
		"unhashed lock":     {`{"kind": "command", "noun": "hmd", "summary": "s", "runtime": {"kind": "python", "python": "3.12", "lock": "unhashed.lock", "entry_point": "hmd.main:main"}}`, "hash"},
		"bad entry point":   {`{"kind": "command", "noun": "hmd", "summary": "s", "runtime": {"kind": "python", "python": "3.12", "lock": "requirements.lock", "entry_point": "hmd.main"}}`, "module:function"},
		"escaping path":     {`{"kind": "docs", "title": "t", "dir": "../../etc"}`, "outside"},
		"missing path":      {`{"kind": "docs", "title": "t", "dir": "nope"}`, "nope"},
		"python from git":   {`{"kind": "command", "noun": "hmd", "summary": "s", "source": {"git": "https://x/y.git"}, "runtime": {"kind": "python", "python": "3.12", "lock": "requirements.lock", "entry_point": "a:b"}}`, "published"},
		"interpreter unreq": {`{"kind": "command", "noun": "ops", "summary": "s", "runtime": {"kind": "scripts", "interpreter": "bash", "scripts": {"drain": "scripts/drain.sh"}}}`, "requires"},
		"docs no title":     {`{"kind": "docs", "dir": "docs"}`, "title"},
	}
	for name, tc := range cases {
		sec, err := Parse([]byte(`{"install": {"items": [` + tc.items + `]}}`))
		if err == nil {
			err = sec.Validate(root)
		}
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && err == nil:
			t.Errorf("%s: validated", name)
		case tc.want != "" && !strings.Contains(err.Error(), tc.want):
			t.Errorf("%s: %v, want it to mention %q", name, err, tc.want)
		}
	}
}

func TestValidateRefusesASymlinkOut(t *testing.T) {
	t.Parallel()
	root := tree(t, map[string]string{"x": "x"})
	if err := os.Symlink("/etc", filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}
	sec, _ := Parse([]byte(`{"install": {"items": [{"kind": "docs", "title": "t", "dir": "docs"}]}}`))
	if err := sec.Validate(root); err == nil {
		t.Fatal("a symlinked docs dir validated")
	}
}

func TestRequiredBinariesIncludeGitForAGitItem(t *testing.T) {
	t.Parallel()
	sec, _ := Parse([]byte(`{"install": {"requires": [{"binary": "uv", "install_hint": "h"}],
	  "items": [{"kind": "docs", "title": "t", "source": {"git": "https://x/y.git"}}]}}`))
	var names []string
	for _, r := range sec.Required() {
		names = append(names, r.Binary)
	}
	if strings.Join(names, ",") != "uv,git" {
		t.Errorf("Required = %v", names)
	}
}
