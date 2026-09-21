package oci

import (
	"errors"
	"strings"
	"testing"
)

// TestParseRef is NERD016 SPEC001's grammar, one row per production.
func TestParseRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want Ref
		err  error
	}{
		{in: "ghcr.io/hmdlabs/stacks/analytics",
			want: Ref{Host: "ghcr.io", Repository: "hmdlabs/stacks/analytics"}},
		{in: "oci://ghcr.io/hmdlabs/stacks/analytics:0.3.1",
			want: Ref{Host: "ghcr.io", Repository: "hmdlabs/stacks/analytics", Tag: "0.3.1"}},
		{in: "ghcr.io/hmdlabs/stacks/analytics@0.3.1",
			want: Ref{Host: "ghcr.io", Repository: "hmdlabs/stacks/analytics", Tag: "0.3.1"}},
		{in: "ghcr.io/hmdlabs/plugins/hello@sha256:" + zeros64,
			want: Ref{Host: "ghcr.io", Repository: "hmdlabs/plugins/hello", Digest: "sha256:" + zeros64}},
		{in: "localhost:5000/team/thing:1.0",
			want: Ref{Host: "localhost:5000", Repository: "team/thing", Tag: "1.0"}},
		{in: "127.0.0.1:41234/x/y@2",
			want: Ref{Host: "127.0.0.1:41234", Repository: "x/y", Tag: "2"}},
		{in: "localhost/x",
			want: Ref{Host: "localhost", Repository: "x"}},

		{in: "analytics", err: ErrNoHost},
		{in: "hmdlabs/stacks/analytics", err: ErrNoHost},
		{in: "ghcr.io", err: ErrNoHost},
		{in: "github.com/acme/nsctl-foo@1.2.0", err: ErrUnsupportedScheme},
		{in: "https://ghcr.io/x/y", err: ErrUnsupportedScheme},
		{in: "ghcr.io/x/y:", err: ErrInvalidRef},
		{in: "ghcr.io/x/y@", err: ErrInvalidRef},
		{in: "ghcr.io/x/y@sha256:short", err: ErrInvalidRef},
		{in: "ghcr.io/Upper/Case", err: ErrInvalidRef},
		{in: "", err: ErrInvalidRef},
	}
	for _, tc := range cases {
		got, err := ParseRef(tc.in)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Errorf("ParseRef(%q): err = %v, want %v", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRef(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseRef(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestRefStringAndReference(t *testing.T) {
	t.Parallel()

	r := Ref{Host: "ghcr.io", Repository: "hmdlabs/stacks/analytics", Tag: "0.3.1"}
	if got := r.String(); got != "oci://ghcr.io/hmdlabs/stacks/analytics:0.3.1" {
		t.Errorf("String = %q", got)
	}
	if got := r.Name(); got != "ghcr.io/hmdlabs/stacks/analytics" {
		t.Errorf("Name = %q", got)
	}
	if got := r.Reference(); got != "0.3.1" {
		t.Errorf("Reference = %q", got)
	}
	if !r.Versioned() {
		t.Error("tagged ref must be Versioned")
	}

	bare := r.WithTag("")
	if bare.Versioned() {
		t.Error("untagged ref must not be Versioned")
	}
	if got := bare.String(); got != "oci://ghcr.io/hmdlabs/stacks/analytics" {
		t.Errorf("bare String = %q", got)
	}

	pinned := Ref{Host: "ghcr.io", Repository: "x/y", Digest: "sha256:" + zeros64}
	if got := pinned.Reference(); got != "sha256:"+zeros64 {
		t.Errorf("digest Reference = %q", got)
	}
	if got := pinned.String(); got != "oci://ghcr.io/x/y@sha256:"+zeros64 {
		t.Errorf("digest String = %q", got)
	}
}

// TestGitHubRefusalNamesTheDeferredSpec is SPEC008: a user who tries the
// GitHub Releases shape learns the plan, not a parse error.
func TestGitHubRefusalNamesTheDeferredSpec(t *testing.T) {
	t.Parallel()

	_, err := ParseRef("github.com/acme/nsctl-foo@1.2.0")
	if err == nil || !strings.Contains(err.Error(), "NERD016") {
		t.Fatalf("err = %v, want a message naming NERD016", err)
	}
}

const zeros64 = "0000000000000000000000000000000000000000000000000000000000000000"
