package versionspec

import (
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// allowed is every package internal/versionspec may import.
//
// All of them are standard library, and none of them can reach the network or
// internal/librarian, so the guarantee this test makes is transitive without the
// test having to walk a dependency graph. Adding to this list is a deliberate
// act: it is where the promise in the package doc is actually kept.
var allowed = map[string]bool{
	"errors": true, "fmt": true, "sort": true, "strconv": true, "strings": true,
}

// TestImportsCannotReachTheNetwork is NERD011 acceptance criterion 6.
//
// The criterion is written as `go list -deps` showing neither net/http nor
// internal/librarian, and this is that check without shelling out to the
// toolchain. It exists because SPEC005's split is load bearing rather than
// tidy: manifest validation, status reporting and error messages use the
// satisfaction test freely, and they can only do that if nobody has to wonder
// whether asking "does this version satisfy this range" made an HTTP request.
// internal/artifact states the same kind of guarantee in prose; this one is
// enforced.
func TestImportsCannotReachTheNetwork(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no package parsed")
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if !allowed[path] {
					t.Errorf("%s imports %q, which is not on the allowlist."+
						"\nThis package is pure by design: no I/O, no net/http, no internal/librarian."+
						"\nEnumerating what a librarian holds belongs in internal/versions.", name, path)
				}
			}
		}
	}
}
