package installitems

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/agentskills"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
)

// Remove undoes every item a plugin placed (SPEC014). A git clone is never
// deleted, and a skill the user has edited is kept; both are reported on w
// with their paths, because the user may want them and nsctl cannot tell.
func Remove(home, class string, items []nsconfig.PluginItem, w io.Writer) error {
	removeSkills(class, skillPaths(items), w)
	for _, it := range items {
		if it.Clone {
			fmt.Fprintf(w, "Kept %s: it is a git clone in your repository folder, and may hold your work\n", it.Path)
		}
		if it.Kind == nsconfig.KindCommand && it.Runtime == nsconfig.RuntimeScripts && it.Path != "" &&
			!strings.HasPrefix(it.Path, InstallsRoot(home)) {
			fmt.Fprintf(w, "Kept %s: it is a git clone in your repository folder, and may hold your work\n", it.Path)
		}
	}
	if _, err := setKnowledge(home, class, nil); err != nil {
		return err
	}
	return Prune(home, class, "")
}

// Supersede is what an upgrade does once the new declaration is written:
// drop the previous versions' directories and any skill the new version no
// longer ships.
func Supersede(home, class, version string, prev, next []nsconfig.PluginItem, w io.Writer) error {
	keep := map[string]bool{}
	for _, p := range skillPaths(next) {
		keep[p] = true
	}
	var gone []string
	for _, p := range skillPaths(prev) {
		if !keep[p] {
			gone = append(gone, p)
		}
	}
	removeSkills(class, gone, w)
	return Prune(home, class, version)
}

func skillPaths(items []nsconfig.PluginItem) []string {
	var out []string
	for _, it := range items {
		if it.Kind == nsconfig.KindSkills {
			out = append(out, it.Paths...)
		}
	}
	return out
}

func removeSkills(class string, paths []string, w io.Writer) {
	for _, p := range paths {
		switch agentskills.OwnedState(p, class) {
		case agentskills.Missing:
		case agentskills.Installed:
			if err := os.RemoveAll(p); err != nil {
				fmt.Fprintf(w, "warning: could not remove skill %s: %v\n", p, err)
			}
		default:
			fmt.Fprintf(w, "Kept skill %s: it was edited after install\n", p)
		}
	}
}

// Prune removes every installed version of class except keep.
func Prune(home, class, keep string) error {
	entries, err := os.ReadDir(InstallsRoot(home))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, class+"@") {
			continue
		}
		if keep != "" && name == class+"@"+keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(InstallsRoot(home), name)); err != nil {
			return err
		}
	}
	return nil
}
