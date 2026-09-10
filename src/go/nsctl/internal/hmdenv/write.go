package hmdenv

// The writer half of the package. NERD004 SPEC010's configuration handback:
// an extension contributes environment variables back to the platform, and
// they land here because this is the file every HMD tool already reads.
//
// Everything the writer does is shaped by one fact -- hmd.env is a file a
// person edits. Parse throws away comments, blank lines, key order, `export `
// prefixes and the original quoting, so a writer that loaded the map and
// re-serialised it would silently reformat a hand-maintained file. This one
// splices a delimited block into the raw text instead and leaves every byte
// outside it alone, in the way router.UpsertServiceRoute already splices one
// nginx route between markers.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Merge is how a contributed value combines with one the user set themselves.
//
// SPEC010's first draft had a single rule -- an explicit user value replaces
// the contribution outright -- and NERD006 SPEC008 found it wrong for every
// variable this mechanism actually has to write. Whole-value precedence
// deletes the shape: a user with a JFrog index in PYTHON_REGISTRIES loses it,
// because the variable is a map and not a scalar.
type Merge string

const (
	// MergeScalar is a plain value. A user value wins outright, and the block
	// omits the name entirely rather than emitting a losing assignment.
	MergeScalar Merge = "scalar"
	// MergeJSONMap is a JSON object -- PYTHON_REGISTRIES, TYPESCRIPT_REGISTRIES.
	// The contribution is merged in under Key, and a user-defined key of the
	// same name wins. Every other key they configured survives.
	MergeJSONMap Merge = "json-map"
	// MergeList is a delimited list -- GOPROXY, HMD_LOCAL_IMAGE_PULL_REGISTRIES.
	// The user's entries keep their order and stay first; the contribution is
	// appended, and only when it is not already there.
	MergeList Merge = "list"
)

// DefaultSeparator is the delimiter a list uses when none is declared. Both
// live consumers split on it -- see controlplane.ImageCandidates.
const DefaultSeparator = ","

// The managed block's delimiters. The wording follows internal/router's,
// because a reader who has seen one of these files has seen both.
const (
	blockBegin = "# >>> ns handback >>>"
	blockEnd   = "# <<< ns handback <<<"
	blockNote  = "# Written by nsctl from the control-plane manifest. Edit that, not this block."
)

// blockPattern matches the managed block including the newlines around it, so
// removing it leaves the surrounding text exactly as it was.
var blockPattern = regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(blockBegin) + `.*?` + regexp.QuoteMeta(blockEnd) + `\n?`)

// Var is one environment variable an extension contributes.
type Var struct {
	Name  string
	Merge Merge
	// Key is the map key a json-map contribution is filed under.
	Key string
	// Separator overrides DefaultSeparator for a list.
	Separator string
	// Value is a string for scalar and list, and a map for json-map.
	Value any
	// Source names the instance that contributed this, for reporting. It has
	// no effect on what is written.
	Source string
}

// Result is what apply and status report about a write.
type Result struct {
	// Written names the variables the block now sets.
	Written []string
	// Yielded names those it does not, because the user's own value already
	// covers them. Reported rather than silent: a hand-set value is otherwise
	// an invisible reason for a registry not being used.
	Yielded []string
	// Problems are contributions that could not be merged and were skipped.
	// Never fatal -- one unusable variable must not take the others down.
	Problems []string
	Path     string
}

// Upsert writes vars into the managed block of $HMD_HOME/.config/hmd.env.
//
// The block is regenerated whole on every call, so a variable belonging to an
// extension that is no longer declared disappears by absence, and an empty
// vars removes the block entirely. That is WriteControlPlaneVhosts' property,
// applied to a second file for the same reason: declaring nothing is a
// convergence too, and the interesting one.
func Upsert(home string, vars []Var) (Result, error) {
	path := Path(home)
	if path == "" {
		return Result{}, fmt.Errorf("no HMD_HOME, so there is no hmd.env to write the handback into")
	}
	res := Result{Path: path}

	content, mode, err := readFileWithMode(path)
	if err != nil {
		return res, err
	}
	if err := checkMarkers(content, path); err != nil {
		return res, err
	}

	// The user's value is what the file says with the block removed -- never
	// the merged result of a previous run, or each apply would re-absorb its
	// own output and a list would grow without bound.
	outside := blockPattern.ReplaceAllString(content, "\n")
	user, err := Parse(strings.NewReader(outside))
	if err != nil {
		return res, fmt.Errorf("parsing %s: %w", path, err)
	}

	managed := map[string]string{}
	for _, v := range vars {
		value, act, err := v.merge(user)
		switch {
		case err != nil:
			res.Problems = append(res.Problems, err.Error())
		case act == yielded:
			res.Yielded = append(res.Yielded, v.Name)
		case strings.Contains(value, "$"):
			// python-dotenv expands $VAR in every value whatever the quoting;
			// hmdenv.Parse does not expand at all. A surviving $ would make one
			// line mean two different things to the two readers of this file.
			//
			// Skipped rather than fatal, like every other unusable value here:
			// one variable nobody can write must not stop the others, which is
			// the same reason a failed extension does not fail an apply.
			res.Problems = append(res.Problems, fmt.Sprintf("the handback value for %s contains a '$', which every "+
				"Python reader of hmd.env would expand and nsctl would not; a contributed value is already resolved, "+
				"so remove it", v.Name))
		default:
			managed[v.Name] = value
			res.Written = append(res.Written, v.Name)
		}
	}
	sort.Strings(res.Written)
	sort.Strings(res.Yielded)

	updated := splice(content, renderBlock(managed))
	if updated == content {
		return res, nil
	}
	return res, writeFileAtomic(path, updated, mode)
}

// action distinguishes a contribution that was written from one the user's own
// value already covers.
type action int

const (
	written action = iota
	yielded
)

// merge combines one contribution with the user's own value for it.
func (v Var) merge(user map[string]string) (string, action, error) {
	existing, isSet := user[v.Name]
	switch v.Merge {
	case MergeScalar, "":
		if isSet {
			return "", yielded, nil
		}
		s, err := v.stringValue()
		return s, written, err

	case MergeList:
		add, err := v.stringValue()
		if err != nil {
			return "", written, err
		}
		sep := v.Separator
		if sep == "" {
			sep = DefaultSeparator
		}
		var entries []string
		for _, part := range strings.Split(existing, sep) {
			if part = strings.TrimSpace(part); part != "" {
				entries = append(entries, part)
			}
		}
		for _, e := range entries {
			if e == add {
				return "", yielded, nil
			}
		}
		return strings.Join(append(entries, add), sep), written, nil

	case MergeJSONMap:
		value, ok := v.Value.(map[string]any)
		if !ok {
			return "", written, fmt.Errorf("the handback value for %s must be a block, because it merges into a JSON map", v.Name)
		}
		if v.Key == "" {
			return "", written, fmt.Errorf("the handback for %s declares no 'key' to file its entry under", v.Name)
		}
		merged := map[string]any{}
		if strings.TrimSpace(existing) != "" {
			if err := json.Unmarshal([]byte(existing), &merged); err != nil {
				// Unparseable is not the same as absent. Overwriting it would
				// destroy something the user plainly meant, so it is reported
				// and left exactly as it is.
				return "", written, fmt.Errorf("%s is set to something that is not a JSON object, so the %s entry "+
					"cannot be merged into it; nsctl has left the value alone", v.Name, v.Key)
			}
		}
		if _, taken := merged[v.Key]; taken {
			return "", yielded, nil
		}
		merged[v.Key] = value
		encoded, err := json.Marshal(merged)
		if err != nil {
			return "", written, fmt.Errorf("encoding the handback for %s: %w", v.Name, err)
		}
		// An identical result is a yield, not a write: it keeps the block out
		// of the file when it has nothing to add.
		if isSet && sameJSON(existing, string(encoded)) {
			return "", yielded, nil
		}
		return string(encoded), written, nil

	default:
		return "", written, fmt.Errorf("the handback for %s declares an unknown merge %q; expected %q, %q or %q",
			v.Name, v.Merge, MergeScalar, MergeJSONMap, MergeList)
	}
}

func (v Var) stringValue() (string, error) {
	switch value := v.Value.(type) {
	case string:
		return value, nil
	case nil:
		return "", fmt.Errorf("the handback for %s declares no 'value'", v.Name)
	default:
		return "", fmt.Errorf("the handback value for %s must be a string for merge %q", v.Name, v.Merge)
	}
}

func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// renderBlock builds the managed region, or "" when nothing is contributed.
//
// Keys are sorted so a regenerated block is byte-identical when nothing
// changed and a diff therefore means something did.
func renderBlock(managed map[string]string) string {
	if len(managed) == 0 {
		return ""
	}
	names := make([]string, 0, len(managed))
	for name := range managed {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(blockBegin + "\n" + blockNote + "\n")
	for _, name := range names {
		b.WriteString(name + "=" + quote(managed[name]) + "\n")
	}
	b.WriteString(blockEnd + "\n")
	return b.String()
}

// quote renders a value the way python-dotenv's set_key already writes into
// this file: single-quoted, in the dialect unquote reads back. Backslash first,
// or escaping the quote would then escape its own backslash.
func quote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return "'" + strings.ReplaceAll(value, "'", `\'`) + "'"
}

// splice removes any existing managed block and appends the new one at the end.
//
// Relocated rather than replaced in place, because the block only wins by being
// last. A user who appends a line below it -- which `hmd configure` does on
// every set_key of a new name -- would otherwise silently outrank the merge
// computed here, and the merge is the thing that carries their own value
// forward.
func splice(content, block string) string {
	content = ensureTrailingNewline(blockPattern.ReplaceAllString(content, "\n"))
	if content == "\n" {
		content = ""
	}
	return content + block
}

func ensureTrailingNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// readFileWithMode returns the file's content and the mode to write it back
// with. A missing file is empty content and 0600 -- the mode this package's
// doc comment says the file has, and the one a file holding live secrets needs.
func readFileWithMode(path string) (string, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0o600, nil
		}
		return "", 0, fmt.Errorf("reading %s: %w", path, err)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return string(data), mode, nil
}

// writeFileAtomic replaces the file through a temporary sibling, so an
// interrupted write cannot leave the environment of every HMD tool truncated.
func writeFileAtomic(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".hmd.env.*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// checkMarkers refuses a file whose managed block nsctl cannot identify.
//
// An unterminated begin marker is not treated as running to the end of the
// file: doing so would silently swallow every variable below it. Two blocks
// are refused for the same reason -- nsctl writes one and cannot tell which
// of them is its own.
func checkMarkers(content, path string) error {
	begins := strings.Count(content, blockBegin)
	ends := strings.Count(content, blockEnd)
	switch {
	case begins == ends && begins <= 1:
		return nil
	case begins != ends:
		return fmt.Errorf("the handback block in %s is not closed (%d begin markers, %d end markers); "+
			"nsctl will not guess how far it runs, because it would take every variable below it with it", path, begins, ends)
	default:
		return fmt.Errorf("%s holds %d handback blocks; nsctl writes one and cannot tell which is its own, "+
			"so remove the ones you did not mean to keep", path, begins)
	}
}

// Split reports what the managed block sets and what the rest of the file
// does, as two separate maps.
//
// Read-only, and the reason it exists: `status` has to say whether a variable
// is live in the block or was yielded to a value the user set, and it must be
// able to say so without writing anything. Reading the whole file with Load
// cannot distinguish the two, because the block's own assignment is the last
// one and would look like the user's.
func Split(home string) (managed, user map[string]string, err error) {
	content, _, err := readFileWithMode(Path(home))
	if err != nil {
		return nil, nil, err
	}
	if err := checkMarkers(content, Path(home)); err != nil {
		return nil, nil, err
	}
	block := blockPattern.FindString(content)
	if managed, err = Parse(strings.NewReader(block)); err != nil {
		return nil, nil, err
	}
	if user, err = Parse(strings.NewReader(blockPattern.ReplaceAllString(content, "\n"))); err != nil {
		return nil, nil, err
	}
	return managed, user, nil
}
