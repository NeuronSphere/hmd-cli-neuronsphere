package hmdenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{"plain assignment", "HMD_DID=aaa\n", map[string]string{"HMD_DID": "aaa"}},
		{"export prefix is stripped", "export HMD_REGION=us-west-2\n", map[string]string{"HMD_REGION": "us-west-2"}},
		{"comments and blanks are skipped", "# a comment\n\nHMD_DID=aaa\n", map[string]string{"HMD_DID": "aaa"}},
		{"double quotes are stripped", `A="hello world"`, map[string]string{"A": "hello world"}},
		{"single quotes are literal", `A='no \n escape'`, map[string]string{"A": `no \n escape`}},
		{"double-quoted escapes expand", `A="one\ttwo"`, map[string]string{"A": "one\ttwo"}},
		{"inline comments are dropped when unquoted", "A=value # trailing\n", map[string]string{"A": "value"}},
		{"a # inside quotes is kept", `A="value # kept"`, map[string]string{"A": "value # kept"}},
		{"an empty value is allowed", "A=\n", map[string]string{"A": ""}},
		{"a value may contain =", "A=k=v\n", map[string]string{"A": "k=v"}},
		{"surrounding whitespace is trimmed", "  A =  b  \n", map[string]string{"A": "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Parse() = %v, want %v", got, tt.want)
			}
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("Parse()[%q] = %q, want %q", k, got[k], want)
				}
			}
		})
	}
}

func TestParseRejectsMalformedLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"no equals sign", "JUST_A_WORD\n"},
		{"empty key", "=value\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(strings.NewReader(tt.input)); err == nil {
				t.Errorf("Parse(%q) succeeded, want an error", tt.input)
			}
		})
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	t.Parallel()

	// An HMD_HOME that has never been configured is a normal state.
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Load() = %v, want empty", got)
	}
}

func TestLoadEmptyHomeIsNotAnError(t *testing.T) {
	t.Parallel()

	got, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Load(\"\") = %v, want empty", got)
	}
}

func TestLoadReadsTheFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("HMD_DID=aaa\nexport HMD_REGION=us-west-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(home)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got["HMD_DID"] != "aaa" || got["HMD_REGION"] != "us-west-2" {
		t.Errorf("Load() = %v", got)
	}
}

func TestLoadReportsAMalformedFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("NOT_AN_ASSIGNMENT\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(home); err == nil {
		t.Error("Load() succeeded on a malformed file, want an error")
	}
}

// fakeEnv builds a Lookup over a map, so precedence is testable without
// t.Setenv -- which panics under t.Parallel().
func fakeEnv(vars map[string]string) Lookup {
	return func(key string) string { return vars[key] }
}

func TestLayeredPrefersTheProcessEnvironment(t *testing.T) {
	t.Parallel()

	process := fakeEnv(map[string]string{"HMD_DID": "from-shell"})
	file := map[string]string{"HMD_DID": "from-file", "HMD_REGION": "us-west-2"}

	lookup := Layered(process, file)

	if got := lookup("HMD_DID"); got != "from-shell" {
		t.Errorf("lookup(HMD_DID) = %q, want the process value", got)
	}
	if got := lookup("HMD_REGION"); got != "us-west-2" {
		t.Errorf("lookup(HMD_REGION) = %q, want the file value", got)
	}
	if got := lookup("UNSET"); got != "" {
		t.Errorf("lookup(UNSET) = %q, want empty", got)
	}
}

func TestLayeredTreatsAnEmptyProcessValueAsUnset(t *testing.T) {
	t.Parallel()

	// `FOO= nsctl ...` must not shadow a real value in the file.
	lookup := Layered(fakeEnv(map[string]string{"HMD_DID": ""}), map[string]string{"HMD_DID": "from-file"})
	if got := lookup("HMD_DID"); got != "from-file" {
		t.Errorf("lookup(HMD_DID) = %q, want the file value", got)
	}
}

func TestLayeredToleratesANilProcessLookup(t *testing.T) {
	t.Parallel()

	lookup := Layered(nil, map[string]string{"HMD_DID": "from-file"})
	if got := lookup("HMD_DID"); got != "from-file" {
		t.Errorf("lookup(HMD_DID) = %q, want the file value", got)
	}
}
