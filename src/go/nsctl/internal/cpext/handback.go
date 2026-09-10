package cpext

// NERD004 SPEC010's configuration handback: the variables an extension
// contributes back to the platform, so that nothing has to be told about it
// twice. Declared in instance_configuration beside the credentials, read the
// way credentialsFrom reads those, and written by internal/hmdenv.

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
)

// varNamePattern keeps a contributed name usable as an environment variable,
// because it is written into a file two other tools parse.
var varNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// unresolved finds a ${VAR} that survived interpolation.
var unresolved = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)

// handbackFrom reads the `handback` block out of an instance configuration.
func handbackFrom(instance string, config map[string]any, lookup compose.Lookup) ([]hmdenv.Var, error) {
	raw, ok := config["handback"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("instance_configuration.handback must be a list")
	}
	seen := map[string]bool{}
	vars := make([]hmdenv.Var, 0, len(list))
	for i, item := range list {
		block, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("instance_configuration.handback[%d] must be a block", i)
		}
		v := hmdenv.Var{
			Name:      stringField(block, "name"),
			Merge:     hmdenv.Merge(stringField(block, "merge")),
			Key:       stringField(block, "key"),
			Separator: stringField(block, "separator"),
			Source:    instance,
		}
		if v.Name == "" {
			return nil, fmt.Errorf("instance_configuration.handback[%d] has no 'name', so there is nothing to write", i)
		}
		if !varNamePattern.MatchString(v.Name) {
			return nil, fmt.Errorf("handback %q is not a usable variable name: it is written into hmd.env, so it "+
				"must start with a letter or underscore and hold only letters, digits and underscores", v.Name)
		}
		if seen[v.Name] {
			return nil, fmt.Errorf("instance_configuration.handback[%d]: duplicate name %q", i, v.Name)
		}
		seen[v.Name] = true

		if v.Merge == "" {
			v.Merge = hmdenv.MergeScalar
		}
		switch v.Merge {
		case hmdenv.MergeScalar, hmdenv.MergeJSONMap, hmdenv.MergeList:
		default:
			return nil, fmt.Errorf("handback %q: unknown merge %q; expected %q, %q or %q", v.Name, v.Merge,
				hmdenv.MergeScalar, hmdenv.MergeJSONMap, hmdenv.MergeList)
		}
		if v.Key != "" && v.Merge != hmdenv.MergeJSONMap {
			return nil, fmt.Errorf("handback %q names 'key' but merges as %s, where a key has no meaning", v.Name, v.Merge)
		}
		if v.Merge == hmdenv.MergeJSONMap && v.Key == "" {
			return nil, fmt.Errorf("handback %q merges as json-map but names no 'key', so there is no entry to merge under", v.Name)
		}
		if v.Separator != "" && v.Merge != hmdenv.MergeList {
			return nil, fmt.Errorf("handback %q names 'separator' but merges as %s, where there is no list to split", v.Name, v.Merge)
		}

		value, err := expandValue(v.Name, block["value"], lookup)
		if err != nil {
			return nil, err
		}
		v.Value = value
		vars = append(vars, v)
	}
	sort.Slice(vars, func(i, j int) bool { return vars[i].Name < vars[j].Name })
	return vars, nil
}

// expandValue resolves ${VAR} through every string in a declared value, so a
// handback states its URL once and refers to it.
//
// The manifest may nest -- a json-map entry is a block -- so this walks rather
// than calling expand on a single field.
func expandValue(name string, value any, lookup compose.Lookup) (any, error) {
	switch v := value.(type) {
	case string:
		return expandString(name, v, lookup)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			expanded, err := expandValue(name, item, lookup)
			if err != nil {
				return nil, err
			}
			out[i] = expanded
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			expanded, err := expandValue(name, item, lookup)
			if err != nil {
				return nil, err
			}
			out[k] = expanded
		}
		return out, nil
	default:
		return value, nil
	}
}

func expandString(name, value string, lookup compose.Lookup) (string, error) {
	// Refused before expansion rather than after. A Credential's resolved
	// value is deliberately unreachable from this lookup, so the reference
	// would expand to nothing -- and an empty string is a failure the reader
	// has to notice rather than one they are told about.
	if strings.Contains(value, SecretEnvPrefix) {
		ref := unresolved.FindString(value)
		return "", fmt.Errorf("handback %q references %s, which is a delivered credential. A handback is written "+
			"to a plain file, and SPEC009 keeps a credential off disk; deliver it into the container instead", name, ref)
	}
	// Checked before expansion, because os.Expand turns an unset name into an
	// empty string rather than leaving it visible. Silently interpolating a
	// registry URL down to "/hmd/local/+simple/" is the failure this catches.
	//
	// hmd.env is read by two tools that disagree about expanding it --
	// python-dotenv expands every value whatever the quoting, hmdenv.Parse
	// expands none -- so a contributed value must be fully resolved before it
	// is written, or the two readers see different things.
	for _, match := range unresolved.FindAllStringSubmatch(value, -1) {
		if lookup == nil || lookup(match[1]) == "" {
			return "", fmt.Errorf("handback %q names %s, and nothing sets it. A contributed value must be fully "+
				"resolved when it is written, because hmd.env is read by one tool that expands variables and one that does not",
				name, match[0])
		}
	}
	return expand(value, lookup), nil
}

// Handback is every variable the resolved extensions contribute.
//
// Sorted, so a regenerated block is byte-identical when nothing changed and a
// diff therefore means something did. A failed extension contributes nothing:
// its containers are not running, so neither should its configuration be.
func Handback(exts []Extension) ([]hmdenv.Var, error) {
	var vars []hmdenv.Var
	// owner tracks who claims each variable, and each json-map key within it,
	// so a collision is refused rather than resolved arbitrarily.
	owner := map[string]string{}
	for _, e := range exts {
		if e.Failed() {
			continue
		}
		for _, v := range e.Handback {
			claim := v.Name
			if v.Merge == hmdenv.MergeJSONMap {
				// Two extensions contributing different keys to one map is
				// the whole point of the map shape.
				claim = v.Name + "." + v.Key
			}
			if held, taken := owner[claim]; taken {
				return nil, fmt.Errorf("handback %q is declared by both %s and %s; two extensions cannot own one "+
					"variable, so neither is written", claim, held, v.Source)
			}
			owner[claim] = v.Source
			vars = append(vars, v)
		}
	}
	sort.Slice(vars, func(i, j int) bool {
		if vars[i].Name != vars[j].Name {
			return vars[i].Name < vars[j].Name
		}
		return vars[i].Key < vars[j].Key
	})
	return vars, nil
}

// ReportHandback says what the managed block now sets and what it left alone.
//
// The "next command" clause is not padding. cmd/root.go loads hmd.env once,
// before any of this runs, so a value written here is genuinely not visible to
// the command that wrote it. Refreshing the lookup mid-run is worse -- it is
// captured in every service's compose config hash, so two services in one
// start would hash the same input differently -- so the gap is reported.
func ReportHandback(w io.Writer, res hmdenv.Result) {
	for _, name := range res.Written {
		fmt.Fprintf(w, "  hmd.env  wrote %s; it takes effect in the next command\n", name)
	}
	for _, name := range res.Yielded {
		fmt.Fprintf(w, "  hmd.env  %s is already set outside the managed block, so nsctl left it alone\n", name)
	}
	for _, problem := range res.Problems {
		fmt.Fprintf(w, "warning: %s\n", problem)
	}
}

// handbackNames renders the block's contents for `status`, one line each.
func handbackNames(vars []hmdenv.Var) []string {
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		name := v.Name
		if v.Key != "" {
			name += "." + v.Key
		}
		out = append(out, fmt.Sprintf("%s (%s, from %s)", name, v.Merge, v.Source))
	}
	sort.Strings(out)
	return out
}
