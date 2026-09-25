// Package compose runs the control-plane services described by
// services/docker-compose.control-plane.yml without the `docker compose`
// binary. Docker is the only host tool nsctl needs.
//
// In extend mode compose was orchestrating exactly three containers --
// hmd_proxy, floci and the Deployment GUI -- while adding a toolchain
// dependency and, through _get_base_command, a `pip config get` shell-out on
// every invocation. The per-environment compose file declares `services: {}`
// and exists only to give compose a project to attach, so nsctl skips it
// entirely.
//
// The YAML is parsed rather than transcribed into Go structs: 227 lines with
// roughly forty ${VAR:-default} entries copied by hand is a drift hazard, and
// the interpolator that avoids it is small.
package compose

import (
	"fmt"
	"strings"
)

// Lookup resolves a variable, returning "" when unset.
type Lookup = func(string) string

// Interpolate expands Compose's variable syntax in s.
//
// Supported, because these are the forms the bundled file uses:
//
//	$VAR            ${VAR}          the value, or empty when unset
//	${VAR:-default} ${VAR-default}  the default when unset
//	$$                              a literal $
//
// Defaults nest. The bundled file relies on it:
//
//	${HMD_LOCAL_K3S_WRAPPER_IMAGE:-${HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/hmdlabs}/hmd-img-k3s-floci:0.3.4}
//
// which is why this matches braces rather than running a regexp over the line.
//
// Compose distinguishes :- (unset or empty) from - (unset only). nsctl treats
// them alike, matching os.Getenv's own conflation of unset and empty that every
// HMD tool already relies on -- and the bundled file only ever uses :-.
//
// ${VAR:?message} and ${VAR:+alt} are rejected rather than silently
// mis-expanded; nothing bundled uses them, and guessing at a required-variable
// error would be worse than naming it.
func Interpolate(s string, lookup Lookup) (string, error) {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			// A trailing $ is literal.
			b.WriteByte('$')
			break
		}
		switch next := s[i+1]; {
		case next == '$':
			b.WriteByte('$')
			i += 2
		case next == '{':
			end, err := matchBrace(s, i+1)
			if err != nil {
				return "", err
			}
			expanded, err := expandBraced(s[i+2:end], lookup)
			if err != nil {
				return "", err
			}
			b.WriteString(expanded)
			i = end + 1
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameByte(s[j]) {
				j++
			}
			b.WriteString(lookup(s[i+1 : j]))
			i = j
		default:
			// $ followed by anything else is literal, as in a nginx $variable
			// that happens to sit in a compose value.
			b.WriteByte('$')
			i++
		}
	}
	return b.String(), nil
}

// matchBrace returns the index of the '}' closing the '{' at open, honouring
// nested ${...}.
func matchBrace(s string, open int) (int, error) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated ${...} in %q", s)
}

// expandBraced expands the inside of a ${...}, which is either a bare name or
// a name, a separator and a default that may itself interpolate.
func expandBraced(body string, lookup Lookup) (string, error) {
	name, sep, def := splitBraced(body)
	if name == "" {
		return "", fmt.Errorf("empty variable name in ${%s}", body)
	}
	switch sep {
	case "":
		return lookup(name), nil
	case ":-", "-":
		if v := lookup(name); v != "" {
			return v, nil
		}
		return Interpolate(def, lookup)
	default:
		return "", fmt.Errorf("unsupported substitution ${%s}: nsctl handles ${VAR}, ${VAR:-default} and ${VAR-default}", body)
	}
}

// splitBraced finds the separator at the top level of a ${...} body, ignoring
// any that appear inside a nested ${...}.
func splitBraced(body string) (name, sep, def string) {
	depth := 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
			continue
		case '}':
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		if isNameByte(body[i]) {
			continue
		}
		// The first character that cannot be part of a name starts the
		// separator. Two-character separators come first so ":-" is not read
		// as ":".
		for _, s := range []string{":-", ":?", ":+", "-", "?", "+"} {
			if strings.HasPrefix(body[i:], s) {
				return body[:i], s, body[i+len(s):]
			}
		}
		return body[:i], body[i : i+1], body[i+1:]
	}
	return body, "", ""
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameByte(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}
