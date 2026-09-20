package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// selectionFile is what `--save-selection` writes and `--selection` reads.
//
// NERD013 SPEC004. A closure of twenty-five entries is small enough to read and
// too large to narrow by typing flags, and a file is the form of "let me pick"
// that is diffable, committable, reusable on another machine and usable from a
// script -- none of which an interactive picker is.
type selectionFile struct {
	Instance []selectionEntry `toml:"instance"`
}

type selectionEntry struct {
	Take      bool   `toml:"take"`
	Name      string `toml:"name"`
	RepoClass string `toml:"repo_class"`
	Version   string `toml:"version"`
	Why       string `toml:"why"`
}

// writeSelection records what a selection resolved to, as something to edit.
//
// Every instance the closure had an opinion about appears, with `take` set to
// what it decided, so narrowing an import is flipping a line to false and
// widening it is flipping one to true. Instances that were *bound* rather than
// imported -- the substrate, anything already declared, a stubbed role -- are
// deliberately absent: they are not decisions this file can revisit, and
// offering them as `take = false` would invite an edit that means nothing.
func writeSelection(path, env string, selection bomSelection, entries []msdeploy.BOMEntry) error {
	index := make(map[string]msdeploy.BOMEntry, len(entries))
	for _, e := range entries {
		index[e.RepoInstanceName] = e
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# nsctl bom selection, from the %s environment.\n", env)
	fmt.Fprintf(&b, "# Flip `take` and pass this back with `nsctl bom import %s --selection <file>`.\n", env)
	b.WriteString("# Instances bound rather than imported -- the substrate, anything already\n")
	b.WriteString("# declared, a role stubbed onto the core instance -- are not listed: they are\n")
	b.WriteString("# not decisions this file can change.\n")

	write := func(e selectionEntry) {
		b.WriteString("\n[[instance]]\n")
		fmt.Fprintf(&b, "take       = %t\n", e.Take)
		fmt.Fprintf(&b, "name       = %q\n", e.Name)
		fmt.Fprintf(&b, "repo_class = %q\n", e.RepoClass)
		fmt.Fprintf(&b, "version    = %q\n", e.Version)
		fmt.Fprintf(&b, "why        = %q\n", e.Why)
	}

	taken := map[string]bool{}
	for _, e := range selection.Picked {
		taken[e.RepoInstanceName] = true
		why := "asked for"
		if via, ok := selection.viaDeps[e.RepoInstanceName]; ok {
			why = via
		}
		write(selectionEntry{true, e.RepoInstanceName, e.RepoClassName, e.RepoClassVersion, why})
	}

	// Everything the closure left out, so widening is an edit rather than a
	// different command. One line per instance, under the first role that
	// reached it, since several roles may point at one thing.
	left := map[string]string{}
	for _, r := range sortedRefs(selection.notFollowed) {
		if _, ok := left[r.Target]; !ok {
			left[r.Target] = fmt.Sprintf("optional: %s needs it for %s", r.Instance, r.Role)
		}
	}
	for name, why := range selection.skipped {
		if _, ok := left[name]; !ok {
			left[name] = why
		}
	}
	for _, name := range sortedKeysOf(left) {
		if taken[name] || selection.bound[name] != "" {
			continue
		}
		e := index[name]
		if e.RepoInstanceName == "" {
			// Named by a role but absent from the BOM, so there is nothing to
			// take. Warned about elsewhere; not an entry here.
			continue
		}
		write(selectionEntry{false, name, e.RepoClassName, e.RepoClassVersion, left[name]})
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// readSelection turns an edited file back into a selection.
//
// A `take = true` becomes an explicit instance and a `take = false` becomes an
// exclusion, so the file needs no closure rules of its own: the ordinary ones
// apply, including the refusal to exclude something filling a resource-typed
// required role. That is the point of doing it this way -- a hand-edited file
// is exactly the input that can be wrong, and it is refused here rather than at
// `env apply`.
//
// Decoded strictly. A misspelt key in a file somebody edited by hand is a
// mistake to name, not a setting to ignore -- the same rule nsctl.toml follows.
func readSelection(path string, entries []msdeploy.BOMEntry) (take []string, drop []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nserr.Wrap(nserr.Usage, err)
	}
	var file selectionFile
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, nil, nserr.New(nserr.Usage, "%s: %v", path, err)
	}

	known := make(map[string]bool, len(entries))
	for _, e := range entries {
		known[e.RepoInstanceName] = true
	}

	var unknown []string
	seen := map[string]bool{}
	for i, e := range file.Instance {
		switch {
		case e.Name == "":
			return nil, nil, nserr.New(nserr.Usage, "%s: instance[%d] has no name", path, i)
		case seen[e.Name]:
			return nil, nil, nserr.New(nserr.Usage, "%s: %s is listed twice", path, e.Name)
		case !known[e.Name]:
			unknown = append(unknown, e.Name)
			continue
		}
		seen[e.Name] = true
		if e.Take {
			take = append(take, e.Name)
		} else {
			drop = append(drop, e.Name)
		}
	}

	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, nil, nserr.New(nserr.Usage,
			"%s names %s this environment's BOM does not have: %s.\n"+
				"The BOM may have moved on since the file was written; regenerate it with"+
				" `nsctl bom show --save-selection`.",
			path, count(len(unknown), "instance"), strings.Join(unknown, ", "))
	}
	if len(take) == 0 {
		return nil, nil, nserr.New(nserr.Usage,
			"%s takes nothing: every instance in it is `take = false`.", path)
	}
	return take, drop, nil
}

// applySelectionFile folds a file into the flags, refusing to mix the two.
//
// Combining them would make the file's `take = false` and a --instance on the
// command line argue about the same thing, and which won would depend on an
// order nobody wrote down.
func (s *selectionFlags) applySelectionFile(path string, entries []msdeploy.BOMEntry) error {
	if path == "" {
		return nil
	}
	if s.any() || len(s.exclude) > 0 {
		return nserr.New(nserr.Usage,
			"--selection cannot be combined with --instance, --class, --all or --exclude:"+
				" the file already says which instances to take.")
	}
	take, drop, err := readSelection(path, entries)
	if err != nil {
		return err
	}
	s.instances = take
	s.exclude = drop
	return nil
}

// saveSelectionTo writes a selection file when one was asked for, and says so.
//
// The path is reported on stderr with the command that reads it back: a file
// written into a directory is invisible otherwise, and the next step is not
// guessable from the flag that produced it.
func saveSelectionTo(cmd *cobra.Command, path, env string,
	selection bomSelection, entries []msdeploy.BOMEntry) error {

	if path == "" {
		return nil
	}
	if err := writeSelection(path, env, selection, entries); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Wrote %s. Edit it, then: nsctl bom import %s --selection %s\n", path, env, path)
	return nil
}
