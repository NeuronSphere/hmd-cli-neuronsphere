package oci

import (
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// allowed is every package internal/oci may import. NERD016 SPEC007.
//
// The standard library, the two OCI specification type packages that are
// already in the module graph through the Docker client, and the two internal
// packages SPEC006 needs to resolve a credential. Not oras-go, and not any
// package that resolves or deploys -- those must never import this one, and
// this one importing them would make the cycle a build error rather than a
// design question.
var allowed = map[string]bool{
	"bufio": true, "bytes": true, "context": true, "encoding/base64": true,
	"encoding/json": true, "errors": true, "fmt": true, "io": true,
	"net/http": true, "net/url": true, "os": true, "path/filepath": true,
	"regexp": true, "sort": true, "strconv": true, "strings": true, "sync": true,
	"time": true,

	"github.com/opencontainers/go-digest":              true,
	"github.com/opencontainers/image-spec/specs-go":    true,
	"github.com/opencontainers/image-spec/specs-go/v1": true,

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig":    true,
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tokenstore":  true,
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec": true,
}

func TestImportsAreTheAllowlist(t *testing.T) {
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
					t.Errorf("%s imports %q, which is not on the allowlist (NERD016 SPEC007)", name, path)
				}
			}
		}
	}
}
