package artifact

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Licence is a manifest's `license` block: the author's declaration of what
// nsctl publishes from the tree. NERD017 SPEC011.
//
// SPDX is the licence expression of the published zip -- what an OCI layer
// is annotated with -- and Exclude names the root-relative paths that stay
// out of every zip nsctl makes from the tree. Both are the author's: nsctl
// reads them, applies them and surfaces them, and infers neither from a
// class name nor from a LICENSE file. A tree that declares nothing travels
// whole and is published unannotated.
type Licence struct {
	SPDX string
	// Exclude holds cleaned, slash-separated, root-relative prefixes; a path
	// is excluded when one of them is the path or a leading directory of it.
	Exclude []string
}

// Declared reports whether the manifest carried a `license` at all.
func (l Licence) Declared() bool { return l.SPDX != "" }

// Excludes reports whether rel (slash-separated, root-relative) falls under
// a declared exclude. The match is on whole path segments: `src/python`
// covers `src/python/app.py` and not `src/pythonic/x.py`, and never
// `src/local/scripts/python`, which is a different path entirely.
func (l Licence) Excludes(rel string) bool {
	if rel == "" {
		return false
	}
	for _, prefix := range l.Exclude {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
}

// ParseLicence reads the `license` member of a BACON manifest: a string is
// the SPDX expression alone, an object carries `spdx` and an optional
// `exclude` list. Absent (or null) is the zero Licence and no error; a
// present member that does not have that shape is an error naming the key,
// so that a declaration the author got wrong is never silently a full zip.
func ParseLicence(manifestJSON []byte) (Licence, error) {
	var m struct {
		License json.RawMessage `json:"license"`
	}
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return Licence{}, fmt.Errorf("parsing meta-data/manifest.json: %w", err)
	}
	raw := bytes.TrimSpace(m.License)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return Licence{}, nil
	}

	var spdx string
	if err := json.Unmarshal(raw, &spdx); err == nil {
		if strings.TrimSpace(spdx) == "" {
			return Licence{}, errors.New("license: the spdx expression is empty")
		}
		return Licence{SPDX: strings.TrimSpace(spdx)}, nil
	}

	var obj struct {
		SPDX    *string `json:"spdx"`
		Exclude []any   `json:"exclude"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Licence{}, errors.New("license: must be an SPDX string or an object with spdx and exclude")
	}
	if obj.SPDX == nil || strings.TrimSpace(*obj.SPDX) == "" {
		return Licence{}, errors.New("license: an object form needs a non-empty spdx")
	}
	l := Licence{SPDX: strings.TrimSpace(*obj.SPDX)}
	for _, e := range obj.Exclude {
		s, ok := e.(string)
		if !ok {
			return Licence{}, fmt.Errorf("license.exclude: %v is not a string", e)
		}
		cleaned, err := CleanExclude(s)
		if err != nil {
			return Licence{}, err
		}
		l.Exclude = append(l.Exclude, cleaned)
	}
	return l, nil
}

// CleanExclude normalises one exclude entry to a slash-separated relative
// prefix and refuses the ones that would name something outside the tree
// or the tree itself. The authoring verb and the validator apply the same
// rule, so what they accept is exactly what Zip will honour.
func CleanExclude(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "\\") || filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("license.exclude: %q must be a path relative to the repository root", s)
	}
	cleaned := path.Clean(filepath.ToSlash(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("license.exclude: %q does not name a path inside the repository", s)
	}
	return cleaned, nil
}

// LicenceOf reads the declaration of a working tree. A tree with no
// meta-data/manifest.json declares nothing.
func LicenceOf(root string) (Licence, error) {
	data, err := os.ReadFile(filepath.Join(root, "meta-data", "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Licence{}, nil
	}
	if err != nil {
		return Licence{}, err
	}
	return ParseLicence(data)
}

// LicenceIn reads the declaration out of a build zip's manifest, so that a
// zip that arrived as bytes -- a release directory, the cache, a registry,
// the librarian -- is annotated from what its author declared and not from
// where it was found. A zip without a manifest declares nothing; bytes that
// are not a zip are an error.
func LicenceIn(data []byte) (Licence, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Licence{}, fmt.Errorf("not a zip: %w", err)
	}
	for _, f := range zr.File {
		if filepath.ToSlash(filepath.Clean(f.Name)) != "meta-data/manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return Licence{}, err
		}
		manifestJSON, err := io.ReadAll(io.LimitReader(rc, maxEntry))
		rc.Close()
		if err != nil {
			return Licence{}, err
		}
		return ParseLicence(manifestJSON)
	}
	return Licence{}, nil
}
