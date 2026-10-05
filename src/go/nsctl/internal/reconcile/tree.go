package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TreeDigest digests what a deploy reads from a developer's working tree, so
// an edit to it is drift even though the entry hash -- a function of the
// environment manifest alone -- has not moved (NERD034 SPEC002).
//
// What is read: meta-data/ (less the resources a deploy writes back into it),
// src/local/, and src/<tool>/ for each tool the manifest's deploy.commands
// names -- or all of src/ when it names none nsctl can read, so an unreadable
// manifest over-notices rather than misses. An exec RepoClass's command is its
// own and may read anything, so its whole tree is read, less its tests and
// docs. Build and cache output is skipped: a digest that moved on every build
// would redeploy on every apply.
func TreeDigest(dir string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("digesting %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("digesting %s: not a directory", dir)
	}

	files := map[string]bool{}
	for _, root := range digestRoots(dir) {
		walkRoot := filepath.Join(dir, filepath.FromSlash(root))
		if _, err := os.Stat(walkRoot); err != nil {
			continue
		}
		err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if path != walkRoot && skippedDir(rel, d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !skippedFile(d.Name()) {
				files[rel] = true
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("digesting %s: %w", dir, err)
		}
	}

	sorted := make([]string, 0, len(files))
	for rel := range files {
		sorted = append(sorted, rel)
	}
	sort.Strings(sorted)

	h := sha256.New()
	for _, rel := range sorted {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("digesting %s: %w", dir, err)
		}
		fmt.Fprintf(h, "%s\x00", rel)
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", fmt.Errorf("digesting %s: %w", dir, err)
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// digestRoots are the slash-separated directories under a tree whose contents
// a deploy reads.
func digestRoots(dir string) []string {
	tools, exec := deployTools(filepath.Join(dir, "meta-data", "manifest.json"))
	switch {
	case exec:
		// An exec command (NERD009) is the repo's own, and may read anything
		// in its checkout.
		return []string{"."}
	case len(tools) == 0:
		return []string{"meta-data", "src"}
	}
	roots := []string{"meta-data", "src/local"}
	for _, tool := range tools {
		roots = append(roots, "src/"+tool)
	}
	return roots
}

// deployTools are the tools a manifest's deploy.commands name, in the
// [["cdktf"], ["helm"]] form, and whether one of them is an exec command.
// No tools and no exec when the manifest is absent, unreadable, or declares
// commands in a shape nsctl does not know.
func deployTools(manifestPath string) (tools []string, exec bool) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, false
	}
	var m struct {
		Deploy struct {
			Commands []json.RawMessage `json:"commands"`
		} `json:"deploy"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false
	}
	for _, raw := range m.Deploy.Commands {
		var cmd []any
		if err := json.Unmarshal(raw, &cmd); err != nil || len(cmd) == 0 {
			return nil, false
		}
		tool, ok := cmd[0].(string)
		switch {
		case ok && tool == "exec":
			return nil, true
		case !ok || tool == "" || strings.ContainsAny(tool, `/\.`):
			return nil, false
		}
		tools = append(tools, tool)
	}
	return tools, false
}

// skippedDir is build and cache output, what a deploy writes back, or -- when
// the whole tree is walked -- the tests and docs no deploy reads.
func skippedDir(rel, name string) bool {
	if rel == "meta-data/resources_output" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".egg-info") {
		return true
	}
	switch rel {
	case "test", "tests", "docs":
		return true
	}
	switch name {
	case "__pycache__", "node_modules", "imports", "cdktf.out", "build", "dist", "target":
		return true
	}
	return false
}

func skippedFile(name string) bool {
	return name == ".DS_Store" || strings.HasSuffix(name, ".pyc") || strings.HasSuffix(name, ".log")
}
