package versionspec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestSortIsSemanticAndNotSortVersions is SPEC003's case verbatim.
//
// The second half is the point. hmd_ms_deployment.version.sort_versions is the
// only thing in the platform that sorts versions and it is not a version
// ordering: it applies three stable sorts in the order major, minor, patch, and
// because a stable sort makes the last key primary it comes out ordered by patch
// first and major last. Its callers produce a `latest_version` for display, so
// the consequence there is a wrong "latest" in a GUI -- but the same algorithm
// reused for *selection* would be a wrong deploy. Asserting that this package
// does not reproduce it is what stops a later "simplification" onto the
// platform's existing sorter.
func TestSortIsSemanticAndNotSortVersions(t *testing.T) {
	t.Parallel()

	got := []string{"0.1.100", "0.2.5", "1.0.3", "0.1.9", "2.0.1"}
	Sort(got)

	want := []string{"2.0.1", "1.0.3", "0.2.5", "0.1.100", "0.1.9"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sort = %v, want %v", got, want)
	}
	// What sort_versions answers for the same input, read back to front.
	sortVersions := []string{"2.0.1", "1.0.3", "0.2.5", "0.1.9", "0.1.100"}
	if reflect.DeepEqual(got, sortVersions) {
		t.Errorf("Sort reproduced sort_versions' order (%v), which is patch-primary", got)
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want int
	}{
		{"0.1.100", "0.1.9", 1},
		{"0.2.5", "0.10.0", -1},
		{"1.0.0", "0.99.99", 1},
		{"0.1", "0.1.0", -1}, // equal once padded; fewer components sorts lower
		{"0.1.0", "0.1.0", 0},
		{"0.1.2.3", "0.1.2", 1},
		// A version nothing can select sorts below every real one.
		{"nightly", "0.0.1", -1},
		{"nightly", "alpha", 1},
	}
	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			t.Parallel()
			if got := sign(Compare(tt.a, tt.b)); got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			if got := sign(Compare(tt.b, tt.a)); got != -tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d (not antisymmetric)", tt.b, tt.a, got, -tt.want)
			}
		})
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// TestOrderedSpecifiersAreRefusedWithBothRemedies asserts on the remedies rather
// than on the word "unsupported".
//
// That is the difference between a message a reader can act on and one that
// merely stops them: SPEC002 requires the refusal to name `~= 0.3` and
// `== 0.3.*`, because every one of the five real-world uses is written ">=0.3"
// and its author needs to know which of the two they meant.
func TestOrderedSpecifiersAreRefusedWithBothRemedies(t *testing.T) {
	t.Parallel()

	for _, spec := range []string{">=0.3", ">= 0.3", "> 0.3.1", "<0.3.0", "<= 0.3.9", "~= 0.1,>= 0.3"} {
		t.Run(spec, func(t *testing.T) {
			t.Parallel()

			_, err := Parse(spec)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded", spec)
			}
			if !errors.Is(err, ErrOrdered) {
				t.Fatalf("Parse(%q) = %v, want ErrOrdered", spec, err)
			}
			for _, remedy := range []string{"~= 0.3", "== 0.3.*"} {
				if !strings.Contains(err.Error(), remedy) {
					t.Errorf("the refusal does not name %q:\n%s", remedy, err)
				}
			}
		})
	}
}

func TestParseRefusesMalformedSpecifiers(t *testing.T) {
	t.Parallel()

	// Every one of these raises inside the Python evaluator too; the golden
	// table records them as spec_error and this names why each is refused.
	for _, spec := range []string{"", "0.1", "~= 1", "== *", "~= a.b", "~= 0.1.*", "== 0.*.1"} {
		t.Run(spec, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(spec); err == nil {
				t.Errorf("Parse(%q) succeeded", spec)
			} else if errors.Is(err, ErrOrdered) {
				t.Errorf("Parse(%q) blamed an ordered operator: %v", spec, err)
			}
		})
	}
}

// TestHighestPicksTheNewestSatisfyingVersion is the selection SPEC003 defines,
// on the shape the document works through: `~= 0.1` against a repo class that
// has moved past 0.1.
func TestHighestPicksTheNewestSatisfyingVersion(t *testing.T) {
	t.Parallel()

	published := []string{"0.1.9", "0.1.100", "0.2.5", "0.0.7", "1.0.0", "notaversion"}

	spec, err := Parse("~= 0.1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 0.2.5, not 0.1.100: `~= 0.1` is major 0 and minor at least 1.
	if got, ok := spec.Highest(published); !ok || got != "0.2.5" {
		t.Errorf("Highest = %q (%v), want 0.2.5", got, ok)
	}
	if got, ok := Highest(published); !ok || got != "1.0.0" {
		t.Errorf("unconstrained Highest = %q (%v), want 1.0.0", got, ok)
	}

	series, err := Parse("== 0.1.*")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, ok := series.Highest(published); !ok || got != "0.1.100" {
		t.Errorf("Highest for the 0.1 series = %q (%v), want 0.1.100", got, ok)
	}

	none, err := Parse("~= 9.0")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, ok := none.Highest(published); ok {
		t.Errorf("Highest = %q, want nothing satisfying", got)
	}
}

func TestIsVersion(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"0.1.4", true}, {"0.1", true}, {"0.1.2.3", true}, {"12", true},
		{"~= 0.1", false}, {"0.1.*", false}, {"", false}, {"0.1.x", false}, {"-1.0", false},
	} {
		if got := IsVersion(tt.in); got != tt.want {
			t.Errorf("IsVersion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestStringRendersWhatWasWritten(t *testing.T) {
	t.Parallel()

	spec, err := Parse("~= 0.1, != 0.1.5")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Not a normalised form: a message should name what the manifest says, so
	// the reader can find it.
	if got := spec.String(); got != "~= 0.1, != 0.1.5" {
		t.Errorf("String = %q", got)
	}
	if spec.Empty() {
		t.Error("a parsed specifier reports itself empty")
	}
	if !(Spec{}).Empty() {
		t.Error("the zero Spec does not report itself empty")
	}
}
