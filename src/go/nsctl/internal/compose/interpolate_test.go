package compose

import "testing"

func fakeEnv(vars map[string]string) Lookup {
	return func(key string) string { return vars[key] }
}

func TestInterpolate(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"HMD_HOME":                        "/home/hmd",
		"SET":                             "value",
		"EMPTY":                           "",
		"HMD_LOCAL_NS_CONTAINER_REGISTRY": "my.registry",
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no variables", "plain text", "plain text"},
		{"a braced variable", "${SET}", "value"},
		{"a bare variable", "$SET", "value"},
		{"a variable inside a path", "${HMD_HOME}/.cache/nginx", "/home/hmd/.cache/nginx"},
		{"an unset variable is empty", "${NOPE}", ""},
		{"a default for an unset variable", "${NOPE:-fallback}", "fallback"},
		{"a set variable beats its default", "${SET:-fallback}", "value"},
		{"an empty variable takes its default", "${EMPTY:-fallback}", "fallback"},
		{"the single-hyphen form behaves the same", "${NOPE-fallback}", "fallback"},
		{"a default may be empty", "${NOPE:-}", ""},
		{"a default may contain a colon", "${NOPE:-host:1234}", "host:1234"},
		{"a default may contain a slash", "${NOPE:-ghcr.io/neuronsphere}", "ghcr.io/neuronsphere"},

		// The port lines.
		{"a port pair", "${NOPE:-18080}:${NOPE:-18080}", "18080:18080"},
		{"a port range", "${NOPE:-19000-19079}:${NOPE:-19000-19079}", "19000-19079:19000-19079"},

		// The image lines: two substitutions and a literal tag separator.
		{
			"an image with registry and version defaults",
			"${HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/neuronsphere}/hmd-postgres-base:${HMD_POSTGRES_BASE_VERSION:-stable}",
			"my.registry/hmd-postgres-base:stable",
		},

		// The one that forces brace matching over a regexp.
		{
			"a nested default",
			"${HMD_LOCAL_K3S_WRAPPER_IMAGE:-${HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/neuronsphere}/hmd-img-k3s-floci:0.3.4}",
			"my.registry/hmd-img-k3s-floci:0.3.4",
		},
		{
			"a nested default where both are unset",
			"${A:-${B:-ghcr.io/neuronsphere}/img:1}",
			"ghcr.io/neuronsphere/img:1",
		},

		{"a doubled dollar is literal", "$$notavar", "$notavar"},
		{"a trailing dollar is literal", "cost: $", "cost: $"},
		{"a dollar before punctuation is literal", "$-", "$-"},
		{"adjacent variables", "${SET}${SET}", "valuevalue"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Interpolate(tt.input, fakeEnv(env))
			if err != nil {
				t.Fatalf("Interpolate(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Interpolate(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Guessing at a required-variable error would be worse than naming it, so the
// forms nsctl does not implement are refused rather than mis-expanded.
func TestInterpolateRejectsWhatItDoesNotImplement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"the required form", "${VAR:?must be set}"},
		{"the alternate form", "${VAR:+alt}"},
		{"an unterminated brace", "${VAR"},
		{"an empty name", "${}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, err := Interpolate(tt.input, fakeEnv(nil)); err == nil {
				t.Errorf("Interpolate(%q) = %q, want an error", tt.input, got)
			}
		})
	}
}

func TestInterpolateToleratesANilLookup(t *testing.T) {
	t.Parallel()

	got, err := Interpolate("${NOPE:-fallback}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fallback" {
		t.Errorf("got %q, want %q", got, "fallback")
	}
}
