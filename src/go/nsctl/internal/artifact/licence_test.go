package artifact

import (
	"bytes"
	"strings"
	"testing"
)

// NERD017 SPEC011: the manifest's `license` is the author's declaration of
// what nsctl publishes from the tree -- an SPDX expression, and the paths
// that stay out of the zip.

func TestParseLicence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		manifest string
		want     Licence
		wantErr  string
	}{
		{name: "absent", manifest: `{"name": "x"}`},
		{name: "null", manifest: `{"name": "x", "license": null}`},
		{name: "shorthand", manifest: `{"license": "MIT"}`, want: Licence{SPDX: "MIT"}},
		{name: "object", manifest: `{"license": {"spdx": "Apache-2.0", "exclude": ["src/python/", "src/docker"]}}`,
			want: Licence{SPDX: "Apache-2.0", Exclude: []string{"src/python", "src/docker"}}},
		{name: "object without exclude", manifest: `{"license": {"spdx": "BUSL-1.1"}}`, want: Licence{SPDX: "BUSL-1.1"}},
		{name: "empty shorthand", manifest: `{"license": ""}`, wantErr: "spdx"},
		{name: "object without spdx", manifest: `{"license": {"exclude": ["src/python"]}}`, wantErr: "spdx"},
		{name: "exclude not strings", manifest: `{"license": {"spdx": "MIT", "exclude": [1]}}`, wantErr: "exclude"},
		{name: "exclude escapes", manifest: `{"license": {"spdx": "MIT", "exclude": ["../secrets"]}}`, wantErr: "exclude"},
		{name: "exclude absolute", manifest: `{"license": {"spdx": "MIT", "exclude": ["/etc"]}}`, wantErr: "exclude"},
		{name: "exclude everything", manifest: `{"license": {"spdx": "MIT", "exclude": ["."]}}`, wantErr: "exclude"},
		{name: "wrong type", manifest: `{"license": 7}`, wantErr: "license"},
		{name: "not json", manifest: `{`, wantErr: "manifest.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLicence([]byte(tc.manifest))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.SPDX != tc.want.SPDX || strings.Join(got.Exclude, ",") != strings.Join(tc.want.Exclude, ",") {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLicenceExcludesMatchesWholeSegments(t *testing.T) {
	t.Parallel()

	l := Licence{Exclude: []string{"src/python", "src/docker/Dockerfile", "docs"}}
	for rel, want := range map[string]bool{
		"src/python":                 true,
		"src/python/app.py":          true,
		"src/python/pkg/__init__.py": true,
		"src/pythonic/x.py":          false,
		"src/local/scripts/python/s": false,
		"src/docker/Dockerfile":      true,
		"src/docker/compose.yaml":    false,
		"docs/index.rst":             true,
		"documents/x":                false,
		"meta-data/manifest.json":    false,
		"":                           false,
	} {
		if got := l.Excludes(rel); got != want {
			t.Errorf("Excludes(%q) = %v, want %v", rel, got, want)
		}
	}
	if (Licence{}).Excludes("src/python/app.py") {
		t.Error("an undeclared licence excludes nothing")
	}
}

// A declared exclude is the one exclusion besides SkipDirs: Zip reads the
// tree's own manifest and leaves those paths out, so every verb that zips a
// tree honours the declaration without knowing it exists.
func TestZipHonoursTheDeclaredExclude(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, root, "meta-data/manifest.json",
		`{"name": "hmd-ms-x", "license": {"spdx": "Apache-2.0", "exclude": ["src/python/", "src/docker"]}}`)
	write(t, root, "meta-data/VERSION", "0.1")
	write(t, root, "LICENSE.txt", "Apache License")
	write(t, root, "src/cdktf/stack.py", "stack")
	write(t, root, "src/python/app.py", "print()")
	write(t, root, "src/python/pkg/__init__.py", "")
	write(t, root, "src/docker/Dockerfile", "FROM scratch")
	write(t, root, "src/pythonic/x.py", "kept")
	write(t, root, "src/local/scripts/python/seed.sh", "kept")

	first, err := Zip(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Zip(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("two Zips of one tree differ")
	}
	names := entryNames(t, first)
	for _, want := range []string{"meta-data/manifest.json", "meta-data/VERSION", "LICENSE.txt",
		"src/cdktf/stack.py", "src/pythonic/x.py", "src/local/scripts/python/seed.sh"} {
		if !names[want] {
			t.Errorf("%s is missing from the zip", want)
		}
	}
	for _, unwanted := range []string{"src/python/app.py", "src/python/pkg/__init__.py", "src/docker/Dockerfile"} {
		if names[unwanted] {
			t.Errorf("%s was packed despite the declared exclude", unwanted)
		}
	}

	// The same tree with nothing declared travels whole.
	plain := t.TempDir()
	write(t, plain, "meta-data/manifest.json", `{"name": "hmd-ms-x"}`)
	write(t, plain, "meta-data/VERSION", "0.1")
	write(t, plain, "src/python/app.py", "print()")
	data, err := Zip(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !entryNames(t, data)["src/python/app.py"] {
		t.Error("an undeclared tree lost src/python/app.py")
	}

	// A declaration that cannot be read is an error, not a silently full zip.
	broken := t.TempDir()
	write(t, broken, "meta-data/manifest.json", `{"name": "hmd-ms-x", "license": {"exclude": ["src/python"]}}`)
	write(t, broken, "src/python/app.py", "print()")
	if _, err := Zip(broken); err == nil || !strings.Contains(err.Error(), "license") {
		t.Errorf("Zip of a tree with a malformed license block: err = %v, want one naming license", err)
	}
}

func TestLicenceInReadsTheZipsManifest(t *testing.T) {
	t.Parallel()

	declared := zipOf(t, map[string]string{
		"meta-data/manifest.json": `{"name": "hmd-ms-x", "license": {"spdx": "BUSL-1.1", "exclude": ["src/python"]}}`,
		"meta-data/VERSION":       "0.1",
	})
	l, err := LicenceIn(declared)
	if err != nil {
		t.Fatal(err)
	}
	if l.SPDX != "BUSL-1.1" || len(l.Exclude) != 1 || l.Exclude[0] != "src/python" {
		t.Errorf("got %+v", l)
	}

	l, err = LicenceIn(zipOf(t, tree("x")))
	if err != nil {
		t.Fatal(err)
	}
	if l.SPDX != "" || len(l.Exclude) != 0 {
		t.Errorf("an undeclared zip gave %+v", l)
	}

	l, err = LicenceIn(zipOf(t, map[string]string{"README.md": "no manifest"}))
	if err != nil {
		t.Fatalf("a zip without a manifest is undeclared, not an error: %v", err)
	}
	if l.SPDX != "" {
		t.Errorf("got %+v", l)
	}

	if _, err := LicenceIn([]byte("not a zip")); err == nil {
		t.Error("LicenceIn accepted bytes that are not a zip")
	}
}
