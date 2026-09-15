// Package versionspec evaluates BACON version specifiers: which published
// versions a `version_spec` admits, and which of them is the highest.
//
// It is a port of hmd_ms_deployment.version.VersionSpecifier, and that module is
// the authority rather than PEP 440 or `packaging` -- because hmd-ms-deployment
// re-validates every version it is handed, so a disagreement here is a version
// nsctl picks and the control plane then rejects. The port was made from
// observed behaviour, not from reading the source: testdata/generate_golden.py
// runs the Python evaluator over every specifier and version this package claims
// to understand, and golden_test.go asserts the same answers. The table is the
// contract; this comment is commentary. NERD011 SPEC002.
//
// # Nothing here touches a network
//
// This package is string-and-arithmetic only. Enumerating what a librarian holds
// is internal/versions' job, and the split is load bearing rather than tidiness:
// manifest validation, status reporting and error messages can use the
// satisfaction test freely, without any caller having to wonder whether asking
// "does this version satisfy this range" just made an HTTP request. The
// guarantee is a property of the import list, which TestImportsCannotReachTheNetwork
// asserts. NERD011 SPEC005.
//
// # The ordered operators are refused
//
// `>`, `>=`, `<` and `<=` do not parse here, deliberately, and the refusal names
// what to write instead. They are broken upstream in two independent ways -- the
// evaluator demands exactly three components, so every real-world use raises on
// construction, and it compares component-wise rather than lexicographically, so
// `< 0.3.0` rejects 0.2.9. Implementing them correctly would make nsctl pick
// versions hmd-ms-deployment then rejects; implementing them as observed would
// copy a defect into a second codebase and make it a contract. Fixing Ordered is
// referred to hmd-ms-deployment, and when it is fixed this refusal becomes dead
// code -- which is the right shape for a dependency on somebody else's bug.
package versionspec

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrOrdered reports a specifier using an ordered operator. Matched with
// errors.Is so a caller can tell "you wrote something this cannot support" from
// "you wrote something malformed", which have different remedies.
var ErrOrdered = errors.New("ordered version specifier")

// OrderedError is ErrOrdered with the clause and the remedy filled in.
type OrderedError struct {
	// Clause is the offending clause as written, spaces and all.
	Clause string
	// Series is the first two components of the operand, which is what the
	// remedies are phrased in terms of.
	Series string
}

func (e *OrderedError) Unwrap() error { return ErrOrdered }

func (e *OrderedError) Error() string {
	return fmt.Sprintf(
		"ordered specifiers (>, >=, <, <=) are not supported: the platform's own evaluator requires"+
			" exactly three components and compares them component-wise, so %q does not parse and"+
			" \"< 0.3.0\" would reject 0.2.9.\n"+
			"  Write `~= %s` for \"%s or later in major %s\", or `== %s.*` for \"the %s series\".",
		e.Clause, e.Series, e.Series, major(e.Series), e.Series, e.Series)
}

// kind is which of the three supported operators a clause uses.
type kind int

const (
	compatible kind = iota // ~=
	match                  // ==
	exclude                // !=
)

// component is one dot-separated part of a specifier's operand: a number, or
// the `*` that `==` and `!=` permit as their last.
type component struct {
	num  int
	star bool
}

type clause struct {
	kind       kind
	components []component
}

// Spec is a parsed version_spec: a comma-separated list of clauses, every one of
// which must be satisfied.
type Spec struct {
	raw     string
	clauses []clause
}

// String renders the specifier as it was written, so a message names what the
// manifest says rather than a normalised form the reader has to translate back.
func (s Spec) String() string { return s.raw }

// Empty reports whether this is the zero Spec, which admits everything. Parse
// never produces one; it is what a caller holds when nothing constrained the
// choice.
func (s Spec) Empty() bool { return len(s.clauses) == 0 }

// Parse reads a version_spec.
//
// Spaces are stripped first and the clauses split on commas, which is what the
// Python evaluator does before looking at anything -- so `~= 0.1,!= 0.1.5` and
// `~=0.1, !=0.1.5` are the same specifier.
func Parse(spec string) (Spec, error) {
	stripped := strings.ReplaceAll(spec, " ", "")
	if stripped == "" {
		return Spec{}, fmt.Errorf("empty version specifier")
	}
	out := Spec{raw: strings.TrimSpace(spec)}
	for _, written := range strings.Split(stripped, ",") {
		c, err := parseClause(written)
		if err != nil {
			return Spec{}, err
		}
		out.clauses = append(out.clauses, c)
	}
	return out, nil
}

func parseClause(written string) (clause, error) {
	var k kind
	var operand string
	switch {
	case strings.HasPrefix(written, "~="):
		k, operand = compatible, written[2:]
	case strings.HasPrefix(written, "=="):
		k, operand = match, written[2:]
	case strings.HasPrefix(written, "!="):
		k, operand = exclude, written[2:]
	case strings.HasPrefix(written, ">="), strings.HasPrefix(written, "<="):
		return clause{}, &OrderedError{Clause: written, Series: series(written[2:])}
	case strings.HasPrefix(written, ">"), strings.HasPrefix(written, "<"):
		return clause{}, &OrderedError{Clause: written, Series: series(written[1:])}
	default:
		return clause{}, fmt.Errorf(
			"%q is not a version specifier: it must begin with ~=, == or !=", written)
	}

	parts := strings.Split(operand, ".")
	if len(parts) < 2 {
		return clause{}, fmt.Errorf(
			"version specifier %q needs more than one component, as in \"~= 0.1\"", written)
	}
	c := clause{kind: k}
	for n, part := range parts {
		last := n == len(parts)-1
		if last && part == "*" && k != compatible {
			c.components = append(c.components, component{star: true})
			continue
		}
		num, err := strconv.Atoi(part)
		if err != nil || num < 0 || strings.ContainsAny(part, "+-") {
			if last && k != compatible {
				return clause{}, fmt.Errorf(
					"version specifier %q: the last component must be a number or \"*\"", written)
			}
			return clause{}, fmt.Errorf(
				"version specifier %q: every component must be a number", written)
		}
		c.components = append(c.components, component{num: num})
	}
	return c, nil
}

// Satisfies reports whether a version is admitted by every clause.
//
// A version the Python evaluator would refuse outright -- a non-numeric
// component, or fewer components than a clause compares -- is not satisfied
// rather than an error. That is deliberate and it is conservative in the
// direction that matters: those versions raise inside hmd-ms-deployment, so a
// version nsctl cannot evaluate is one it must never select.
func (s Spec) Satisfies(version string) bool {
	v, ok := parseVersion(version)
	if !ok {
		return false
	}
	for _, c := range s.clauses {
		if !c.admits(v) {
			return false
		}
	}
	return true
}

// admits is the evaluator, ported clause by clause.
//
// Every `return false` guarded by a length check stands for a place the Python
// raises IndexError -- validate() indexes the version by the specifier's own
// length without checking it. Those are not rewritten into something more
// sensible: a version this cannot evaluate is one the control plane cannot
// evaluate either.
func (c clause) admits(v []int) bool {
	last := len(c.components) - 1
	if c.kind == compatible {
		for n := 0; n < last; n++ {
			if n >= len(v) || c.components[n].num != v[n] {
				return false
			}
		}
		if last >= len(v) {
			return false
		}
		return c.components[last].num <= v[last]
	}

	// == and != share an evaluation and differ only in what they do with the
	// answer. Note that the leading components are compared without an early
	// exit, which is the Python's shape and matters only for where the index
	// checks fall.
	matched := true
	for n := 0; n < last; n++ {
		if n >= len(v) {
			return false
		}
		if c.components[n].num != v[n] {
			matched = false
		}
	}
	if matched && !c.components[last].star {
		if last >= len(v) {
			return false
		}
		if c.components[last].num != v[last] {
			matched = false
		}
		// A version with more components than the specifier is not a match:
		// `== 0.1` admits 0.1 and not 0.1.0.
		if len(v) > len(c.components) {
			matched = false
		}
	}
	if c.kind == match {
		return matched
	}
	return !matched
}

// Highest returns the greatest version the specifier admits.
//
// NERD011 SPEC003. The ordering is defined by this package because nothing
// upstream defines one: hmd-ms-deployment only ever filters candidates, so there
// is no existing selection rule to agree with. In particular
// hmd_ms_deployment.version.sort_versions is *not* that ordering despite its
// name -- it applies three stable sorts in the order major, minor, patch, and a
// stable sort makes the last key primary, so it comes out ordered by patch first
// and major last. Reusing it for selection would turn a display defect into a
// deployment one.
func (s Spec) Highest(versions []string) (string, bool) {
	var admitted []string
	for _, v := range versions {
		if s.Satisfies(v) {
			admitted = append(admitted, v)
		}
	}
	return Highest(admitted)
}

// Highest returns the greatest version, unconstrained by any specifier.
func Highest(versions []string) (string, bool) {
	best := ""
	found := false
	for _, v := range versions {
		if !found || Compare(v, best) > 0 {
			best, found = v, true
		}
	}
	return best, found
}

// Compare orders two versions: negative when a is lower, positive when a is
// higher, zero only when they are the same string.
//
// Component-wise numeric from most significant to least, a shorter version
// padded with zeros. Two versions that are equal once padded are ordered by
// component count and then by string, so 0.1 and 0.1.0 have a stable order
// rather than an arbitrary one -- a lock regenerated twice must be the same
// bytes. A version with a non-numeric component sorts below every real one,
// since nothing can select it anyway.
func Compare(a, b string) int {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	switch {
	case !aok && !bok:
		return strings.Compare(a, b)
	case !aok:
		return -1
	case !bok:
		return 1
	}
	for n := 0; n < len(av) || n < len(bv); n++ {
		x, y := at(av, n), at(bv, n)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	if len(av) != len(bv) {
		if len(av) < len(bv) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// Sort orders versions newest first, in place.
func Sort(versions []string) {
	sort.SliceStable(versions, func(i, j int) bool {
		return Compare(versions[i], versions[j]) > 0
	})
}

// IsVersion reports whether a string is a concrete version rather than a range:
// validate_version_number's rule, which is that every dot-separated component is
// numeric.
func IsVersion(s string) bool {
	_, ok := parseVersion(s)
	return ok
}

func parseVersion(version string) ([]int, bool) {
	if version == "" {
		return nil, false
	}
	parts := strings.Split(version, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		// isnumeric()'s rule: digits only. strconv would accept a sign, and
		// "+1" is not a version component anywhere in the platform.
		if part == "" || strings.ContainsAny(part, "+-") {
			return nil, false
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func at(v []int, n int) int {
	if n < len(v) {
		return v[n]
	}
	return 0
}

// series is the first two components of an operand, which is what an ordered
// specifier's remedies are phrased in terms of: ">= 0.3" and "> 0.3.1" are both
// answered with "~= 0.3".
func series(operand string) string {
	parts := strings.Split(operand, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return operand
}

func major(s string) string {
	if i := strings.Index(s, "."); i > 0 {
		return s[:i]
	}
	return s
}
