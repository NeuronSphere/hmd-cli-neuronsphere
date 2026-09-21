// Command docref writes the checked-in nsctl Cobra reference.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func main() {
	out := flag.String("out", "", "path to write (stdout when empty)")
	flag.Parse()
	contents := render()
	if *out == "" {
		_, _ = os.Stdout.Write(contents)
		return
	}
	if err := os.WriteFile(*out, contents, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func render() []byte {
	var b bytes.Buffer
	b.WriteString("nsctl command reference\n=======================\n\n")
	b.WriteString("This page is generated from the Cobra command tree. Do not edit it by hand; run ``make docs-reference``.\n\n")
	writeCommand(&b, cmd.NewRootCommand("v1.0.207", nil), 1)
	return b.Bytes()
}

func writeCommand(b *bytes.Buffer, c *cobra.Command, depth int) {
	title := c.CommandPath()
	underline := "-"
	if depth == 1 {
		underline = "="
	}
	b.WriteString(title + "\n" + strings.Repeat(underline, len(title)) + "\n\n")
	if c.Long != "" {
		b.WriteString(strings.TrimSpace(c.Long) + "\n\n")
	} else if c.Short != "" {
		b.WriteString(c.Short + "\n\n")
	}
	b.WriteString("Usage\n~~~~~\n\n.. code-block:: text\n\n   " + c.UseLine() + "\n\n")
	if c.Example != "" {
		b.WriteString("Examples\n~~~~~~~~\n\n.. code-block:: shell\n\n")
		for _, line := range strings.Split(strings.TrimSpace(c.Example), "\n") {
			b.WriteString("   " + line + "\n")
		}
		b.WriteString("\n")
	}
	if len(c.Aliases) > 0 {
		b.WriteString("Aliases: ``" + strings.Join(c.Aliases, "``, ``") + "``.\n\n")
	}
	writeFlags(b, "Local flags", c.LocalFlags())
	writeFlags(b, "Inherited flags", c.InheritedFlags())
	children := append([]*cobra.Command(nil), c.Commands()...)
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, child := range children {
		if !child.IsAvailableCommand() {
			continue
		}
		writeCommand(b, child, depth+1)
	}
}

func writeFlags(b *bytes.Buffer, heading string, fs *pflag.FlagSet) {
	var flags []*pflag.Flag
	fs.VisitAll(func(f *pflag.Flag) { flags = append(flags, f) })
	if len(flags) == 0 {
		return
	}
	b.WriteString(heading + "\n" + strings.Repeat("~", len(heading)) + "\n\n")
	for _, f := range flags {
		name := "--" + f.Name
		if f.Shorthand != "" {
			name = "-" + f.Shorthand + ", " + name
		}
		detail := f.Usage
		if f.DefValue != "" && f.DefValue != "false" {
			detail += " (default: ``" + f.DefValue + "``)"
		}
		b.WriteString("* ``" + name + "`` — " + detail + "\n")
	}
	b.WriteString("\n")
}
