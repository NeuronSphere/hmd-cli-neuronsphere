// Package tty answers whether a person is there, and asks them one question at
// a time when they are.
//
// It holds the two things `nsctl login` and `nsctl env purge` each grew their
// own copy of, because a third caller -- the guided first run -- needs both and
// a fourth copy would be the point at which they drift.
//
// Deliberately not a terminal UI. A full-screen interface would be the first
// thing in nsctl that cannot be driven from a script, and the flow it would
// serve is a short ordered sequence of yes/no and one-line answers. NERD023
// SPEC001.
package tty

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// IsTerminal reports whether there is a person to answer a prompt.
//
// term.IsTerminal rather than a check for a character device, because /dev/null
// *is* a character device on every Unix -- so `nsctl ... </dev/null`, the usual
// way a script says "there is nobody here", would be mistaken for a terminal and
// answered with a prompt into the void. term.IsTerminal asks the kernel for the
// terminal attributes instead, which /dev/null does not have.
//
// A reader that is not an *os.File -- a test's buffer, a pipe -- is not a
// person.
func IsTerminal(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

// Prompter asks questions on one pair of streams.
//
// The reader is kept across questions on purpose: a fresh bufio.Reader per
// question can lose buffered input, which on a sequence of prompts drops the
// answer to the next one.
type Prompter struct {
	In  *bufio.Reader
	Out io.Writer
}

// New builds a Prompter over a command's streams.
func New(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{In: bufio.NewReader(in), Out: out}
}

// Ask prints a question and returns the trimmed answer, or fallback when the
// answer is empty. An unreadable stdin gives the fallback rather than an error:
// end of input is a person declining to type, and every caller here has a
// default that is safe.
func (p *Prompter) Ask(question, fallback string) string {
	if fallback != "" {
		fmt.Fprintf(p.Out, "%s [%s]: ", question, fallback)
	} else {
		fmt.Fprintf(p.Out, "%s: ", question)
	}
	line, err := p.In.ReadString('\n')
	answer := strings.TrimSpace(line)
	if answer == "" || (err != nil && answer == "") {
		return fallback
	}
	return answer
}

// Confirm asks a yes/no question. def is the answer an empty line means.
//
// Anything that is not recognisably yes or no re-asks rather than being taken
// as the default: a mistyped answer to "shall I start this" should not be read
// as consent, and should not be read as a refusal either.
func (p *Prompter) Confirm(question string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for attempt := 0; attempt < 3; attempt++ {
		fmt.Fprintf(p.Out, "%s [%s]: ", question, hint)
		line, err := p.In.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			if err != nil {
				// End of input. The default stands, because there is no one
				// left to re-ask.
				return def
			}
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		fmt.Fprintln(p.Out, "  Please answer y or n.")
	}
	return def
}

// ConfirmWord asks for one exact word, the way `env purge` does: "are you sure"
// with a typed word rather than a keystroke, for something that cannot be
// undone.
func (p *Prompter) ConfirmWord(question, word string) bool {
	fmt.Fprintf(p.Out, "%s: ", question)
	line, err := p.In.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(line), word)
}
