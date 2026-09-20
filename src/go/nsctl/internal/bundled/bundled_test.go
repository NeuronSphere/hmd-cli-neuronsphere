package bundled

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

// TestRepoArchivesCarryNoServiceCode guards the licence boundary on the
// artifacts actually shipped, not just on the tool that produces them. Every
// embedded repo archive is a deploy descriptor -- Apache 2.0 in each repo
// class -- while src/python, src/typescript and src/docker are Business
// Source License 1.1 in hmd-ms-deployment, hmd-ms-librarian and
// hmd-app-neuronsphere. One such file inside the binary would turn the
// Apache-licensed nsctl into a mixed-licence artifact.
func TestRepoArchivesCarryNoServiceCode(t *testing.T) {
	classes := RepoClasses()
	if len(classes) == 0 {
		t.Skip("no repo archives generated; run make generate")
	}
	for _, class := range classes {
		data, ok := RepoArchive(class)
		if !ok {
			t.Fatalf("%s: listed but unreadable", class)
		}
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", class, err)
		}
		tr := tar.NewReader(zr)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", class, err)
			}
			for _, banned := range []string{"src/python/", "src/typescript/", "src/docker/"} {
				if strings.HasPrefix(hdr.Name, banned) {
					t.Errorf("%s embeds %s -- service code is not part of the deploy descriptor", class, hdr.Name)
				}
			}
		}
	}
}
