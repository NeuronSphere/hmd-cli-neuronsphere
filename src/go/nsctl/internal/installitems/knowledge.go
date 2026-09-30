package installitems

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
)

// KnowledgeEnv names the knowledge directory for a plugin's child, so a
// skill or command finds the index without knowing HMD_HOME's layout.
const KnowledgeEnv = "NSCTL_KNOWLEDGE"

// KnowledgeDir holds the docs index. NERD031 SPEC010.
func KnowledgeDir(home string) string { return filepath.Join(home, "knowledge") }

func knowledgeIndexPath(home string) string { return filepath.Join(KnowledgeDir(home), "index.json") }

// KnowledgeEntry is one docs item on this workstation.
type KnowledgeEntry struct {
	Class   string `json:"class"`
	Version string `json:"version"`
	Title   string `json:"title"`
	Format  string `json:"format,omitempty"`
	Path    string `json:"path"`
	Clone   bool   `json:"clone,omitempty"`
}

// KnowledgeIndex is knowledge/index.json.
type KnowledgeIndex struct {
	Entries []KnowledgeEntry `json:"entries"`
}

// ReadKnowledgeIndex reads the index; a missing one is empty.
func ReadKnowledgeIndex(home string) (KnowledgeIndex, error) {
	var idx KnowledgeIndex
	data, err := os.ReadFile(knowledgeIndexPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return idx, nil
	}
	if err != nil {
		return idx, err
	}
	err = json.Unmarshal(data, &idx)
	return idx, err
}

// setKnowledge replaces class's entries with entries and returns the index
// as it was, for an undo.
func setKnowledge(home, class string, entries []KnowledgeEntry) (KnowledgeIndex, error) {
	prev, err := ReadKnowledgeIndex(home)
	if err != nil {
		return prev, err
	}
	next := KnowledgeIndex{Entries: []KnowledgeEntry{}}
	for _, e := range prev.Entries {
		if e.Class != class {
			next.Entries = append(next.Entries, e)
		}
	}
	next.Entries = append(next.Entries, entries...)
	sort.SliceStable(next.Entries, func(i, j int) bool { return next.Entries[i].Class < next.Entries[j].Class })
	return prev, writeKnowledge(home, next)
}

func writeKnowledge(home string, idx KnowledgeIndex) error {
	if idx.Entries == nil {
		idx.Entries = []KnowledgeEntry{}
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(KnowledgeDir(home), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(knowledgeIndexPath(home), append(data, '\n'), 0o644, 0o755)
}
