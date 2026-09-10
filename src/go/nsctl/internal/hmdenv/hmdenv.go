// Package hmdenv reads $HMD_HOME/.config/hmd.env -- the file the Python CLI
// loads through hmd_cli_tools.load_hmd_env, and the one holding a local
// install's customer code, region and deployment id.
//
// The parser is ported from hmd-cli-bartleby's internal/hmdenv, which already
// reproduces the Python's handling of `export ` prefixes, quoting, escapes and
// inline comments. The one deliberate change: this version returns a map
// instead of calling os.Setenv.
//
// That matters for more than tidiness. SPEC004 requires per-command state to
// arrive through constructor closures rather than package globals, because
// t.Setenv panics under t.Parallel() and the command tests need both. A loader
// that mutates the process environment would put the precedence rule back out
// of a test's reach. Layered keeps the rule -- process environment wins, as
// load_hmd_env(override=False) does -- as an ordinary function of two maps.
//
// The file is mode 600 and holds live secrets. It is read for its keys and
// never logged.
package hmdenv

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RelPath is the file's location relative to $HMD_HOME.
var RelPath = filepath.Join(".config", "hmd.env")

// Lookup resolves one environment variable, returning "" when unset. It is the
// shape os.Getenv already has, so the process environment is a valid Lookup.
//
// An alias rather than a defined type: internal/registry and internal/status
// describe the same thing, and defined types would need an explicit conversion
// at every boundary for no gain in safety.
type Lookup = func(string) string

// Path returns the hmd.env path for an HMD_HOME, or "" when home is empty.
func Path(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, RelPath)
}

// Load reads $HMD_HOME/.config/hmd.env.
//
// A missing file yields an empty map and no error: an HMD_HOME that has never
// been configured is a normal state, not a failure. A file that exists but
// cannot be read or parsed is an error, because the user plainly expected its
// values to apply.
func Load(home string) (map[string]string, error) {
	path := Path(home)
	if path == "" {
		return map[string]string{}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()

	vars, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return vars, nil
}

// Layered returns a Lookup that answers from the process environment first and
// falls back to the file, matching load_hmd_env(override=False): a variable
// exported in the shell beats the same variable in hmd.env.
//
// A variable set to the empty string in the process environment is treated as
// unset, so `FOO= nsctl ...` does not shadow a real value in the file. That
// matches os.Getenv's own conflation of the two, which every HMD tool already
// relies on.
func Layered(process Lookup, file map[string]string) Lookup {
	if process == nil {
		process = func(string) string { return "" }
	}
	return func(key string) string {
		if v := process(key); v != "" {
			return v
		}
		return file[key]
	}
}

// Parse reads dotenv-style content: KEY=VALUE, one per line, with an optional
// `export ` prefix, # comments, blank lines, and single- or double-quoted
// values. Escape sequences inside double quotes (\n, \t, \\, \") are expanded;
// single-quoted values are literal.
func Parse(r io.Reader) (map[string]string, error) {
	vars := make(map[string]string)
	scanner := bufio.NewScanner(r)
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, rawValue, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE, got %q", lineNo, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", lineNo)
		}

		vars[key] = unquote(strings.TrimSpace(rawValue))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return vars, nil
}

// unquote strips matching surrounding quotes and, for double quotes, expands
// the common escape sequences. An unquoted value has any trailing inline
// comment removed.
func unquote(v string) string {
	if len(v) >= 2 {
		switch {
		case v[0] == '\'' && v[len(v)-1] == '\'':
			// python-dotenv's _single_quote_escapes: inside single quotes only
			// \\ and \' are escapes, and \n stays two literal characters.
			// set_key writes this file in exactly that dialect, so a value
			// containing a quote round-trips only if this side agrees.
			inner := v[1 : len(v)-1]
			inner = strings.ReplaceAll(inner, `\'`, `'`)
			return strings.ReplaceAll(inner, `\\`, `\`)
		case v[0] == '"' && v[len(v)-1] == '"':
			inner := v[1 : len(v)-1]
			inner = strings.ReplaceAll(inner, `\n`, "\n")
			inner = strings.ReplaceAll(inner, `\t`, "\t")
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, `\\`, `\`)
			return inner
		}
	}
	// An unquoted value ends at the first " #" -- the dotenv convention for an
	// inline comment.
	if idx := strings.Index(v, " #"); idx >= 0 {
		v = v[:idx]
	}
	return strings.TrimSpace(v)
}
