package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// suggestion matches a backticked nsctl command in source: an error message
// telling the user what to run, or a comment telling a reader what does it.
//
// Single-line only. A backtick in Go also opens a raw string literal, and the
// root command's Long description happens to begin "nsctl runs the local
// NeuronSphere..." -- across newlines this pattern would swallow the whole
// paragraph. The cost is that a suggestion wrapped across two comment lines is
// only checked as far as the wrap.
var suggestion = regexp.MustCompile("`nsctl ([^`\n]*)`")

// commandToken is what a word in a command line looks like: a subcommand, a
// flag, or a placeholder. Prose does not match it -- which is how a sentence
// that merely starts with the binary's name is told apart from an instruction
// to run something.
var commandToken = regexp.MustCompile(`^(--?[a-z][a-z0-9-]*(=.*)?|<[^>]+>|%[sdqv]|[a-z][a-z0-9._-]*)$`)

// isSuggestion reports whether every word could be part of a command line.
func isSuggestion(fields []string) bool {
	for _, f := range fields {
		if !commandToken.MatchString(f) {
			return false
		}
	}
	return len(fields) > 0
}

// TestEverySuggestedCommandExists checks that every `nsctl ...` this module
// prints or documents actually resolves against the command tree.
//
// This is a real class of bug, not a hypothetical one: `nsctl env add --name
// local` was printed by the error a user hits when no environment is
// registered, and `env add` takes a positional -- so following the advice
// produced a second error. Four such strings existed, plus a comment naming an
// `nsctl env down --purge` that has never been a command. Nothing catches
// these otherwise, because a suggestion is a string literal that no test would
// otherwise execute.
func TestEverySuggestedCommandExists(t *testing.T) {
	t.Parallel()

	root := NewRootCommand("test", func(string) string { return "" })
	for _, found := range scanSuggestions(t) {
		t.Run(found.text, func(t *testing.T) {
			words, flags := splitSuggestion(found.words)

			cmd, leftover := resolve(root, words)
			if cmd == root && len(words) > 0 {
				t.Fatalf("%s: `nsctl %s` names no command (%s)", found.file, found.text, cmd.Name())
			}
			// A command with children takes a subcommand, so a leftover word
			// there is a subcommand that does not exist -- which is how
			// `nsctl env down --purge` read as plausible for so long.
			if len(leftover) > 0 && cmd.HasSubCommands() {
				t.Errorf("%s: `nsctl %s` -- %q is not a subcommand of %q",
					found.file, found.text, leftover[0], cmd.CommandPath())
			}
			for _, flag := range flags {
				if cmd.Flags().Lookup(flag) == nil && cmd.InheritedFlags().Lookup(flag) == nil {
					t.Errorf("%s: `nsctl %s` -- %q has no --%s flag",
						found.file, found.text, cmd.CommandPath(), flag)
				}
			}
		})
	}
}

type found struct {
	file  string
	text  string
	words []string
}

// scanSuggestions reads the module's non-test sources. Walking the tree rather
// than listing files is the point: a suggestion added to a package this test
// has never heard of is still checked.
func scanSuggestions(t *testing.T) []found {
	t.Helper()

	var results []found
	seen := map[string]bool{}
	root := ".."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Not the root itself: WalkDir reports ".." with Name() == "..",
			// which the dotfile guard below would match and skip everything.
			if path == root {
				return nil
			}
			if d.Name() == "build" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range suggestion.FindAllStringSubmatch(string(data), -1) {
			text := strings.TrimSpace(m[1])
			key := rel + "|" + text
			if text == "" || seen[key] {
				continue
			}
			fields := strings.Fields(text)
			if !isSuggestion(fields) {
				continue
			}
			seen[key] = true
			results = append(results, found{file: rel, text: text, words: fields})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning sources: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("found no `nsctl ...` suggestions; the scan is broken, not the sources")
	}
	return results
}

// splitSuggestion separates command words from flag names, dropping the
// placeholders a message interpolates (%s) or shows to the reader (<name>).
func splitSuggestion(fields []string) (words, flags []string) {
	for _, f := range fields {
		switch {
		case strings.HasPrefix(f, "--"):
			flags = append(flags, strings.SplitN(strings.TrimPrefix(f, "--"), "=", 2)[0])
		case strings.HasPrefix(f, "-"):
			// Shorthand; the tree is checked by long name only.
		case strings.HasPrefix(f, "<") || strings.Contains(f, "%"):
			// A placeholder for a value the caller supplies.
		default:
			words = append(words, f)
		}
	}
	return words, flags
}

// resolve walks words down the tree while they name subcommands, and returns
// the deepest match plus whatever is left. The leftovers of a leaf command are
// its positional arguments; the leftovers of a parent are a mistake.
func resolve(root *cobra.Command, words []string) (*cobra.Command, []string) {
	cmd := root
	for i, word := range words {
		next, _, err := cmd.Find([]string{word})
		if err != nil || next == cmd {
			return cmd, words[i:]
		}
		cmd = next
	}
	return cmd, nil
}
