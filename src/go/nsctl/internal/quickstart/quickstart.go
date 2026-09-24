// Package quickstart is the guided first run: from a freshly downloaded binary
// to a working environment, with the user's own repository offered along the way
// (NERD023 SPEC001).
//
// It owns a sequence, not any platform behaviour. Every step is an ordinary
// nsctl invocation, and the flow prints that invocation before running it -- the
// argv it shows and the argv it runs are the same slice, so the session doubles
// as a transcript the reader can repeat by hand. That property is why this
// package takes an Exec rather than calling internal/environment and friends
// directly: a step that did its own thing could drift from the command it
// advertised.
package quickstart

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tty"
)

// Options is everything the flow needs. The two function fields are the seam:
// they let the whole sequence be exercised with no container engine, no HMD_HOME
// and no network.
type Options struct {
	// Home is $HMD_HOME after --home and the environment have been applied,
	// empty when neither supplied one.
	Home string
	// Version is the running build, for the opening line.
	Version string

	In       io.Reader
	Out, Err io.Writer

	// Exec runs one nsctl invocation with its output going to the user.
	Exec func(ctx context.Context, argv ...string) error
	// Capture runs one and returns its combined output without showing it, for
	// the read-only questions the flow asks itself.
	Capture func(ctx context.Context, argv ...string) (string, error)

	// UserHome is the operating system's idea of the invoking user's home, used
	// to propose a path. Resolved by the caller rather than guessed from $HOME.
	UserHome string

	// Yes answers every question with its default, for a scripted walk-through.
	// It does not make the flow non-interactive: a run with nobody there is
	// refused, because a wizard that guessed would be a deploy nobody asked for.
	Yes bool

	// Repo is a repository to offer adopting, skipping the question that asks
	// for one.
	Repo string
}

// The candidate stack offered at step four.
//
// Resolved before it is offered and skipped when nothing answers. Deliberately
// not treated as always-present: the README, the first tutorial and the stacks
// how-to have all advertised this name while no such artifact was published, and
// a guided run whose "here is something real" step fails is worse than one that
// does not have the step.
const candidateStack = "observability"

// newPrompter is the seam the tests drive the sequence through: Run refuses a
// non-terminal, which is exactly the refusal under test, so the flow itself has
// to be constructible without going through it.
func newPrompter(o Options) *tty.Prompter { return tty.New(o.In, o.Out) }

type flow struct {
	Options
	p    *tty.Prompter
	home string
	// homeFromWizard is true when the flow chose the home, which is what decides
	// whether later invocations have to carry --home.
	homeFromWizard bool
}

// Run walks the sequence. Every step is declinable, and declining one does not
// abort the run.
func Run(ctx context.Context, o Options) error {
	if !tty.IsTerminal(o.In) {
		return notInteractive(o)
	}
	f := &flow{Options: o, p: newPrompter(o), home: o.Home}

	f.intro()
	if !f.checkHost(ctx) {
		return nserr.New(nserr.Usage,
			"the host is not ready. Fix the failure above and run `nsctl quickstart` again")
	}
	if !f.settleHome() {
		return nil
	}
	slug, ok := f.startEnvironment(ctx)
	if !ok {
		return nil
	}
	f.offerStack(ctx, slug)
	f.offerRepository(ctx)
	f.offerSkills(ctx)
	f.closing(slug)
	return nil
}

// notInteractive prints the sequence instead of half-running it.
//
// A wizard is a conversation, and there is nobody to have it with -- so the
// useful answer is the ordered list of commands the conversation would have
// produced, which is also exactly what a CI job or a script wants. Refusing with
// the list follows `nsctl login`, which does the same thing when no profile is
// configured and no terminal can supply one.
func notInteractive(o Options) error {
	home := o.Home
	if home == "" {
		home = filepath.Join(o.UserHome, "hmd")
	}
	fmt.Fprintln(o.Out, "nsctl quickstart is interactive and stdin is not a terminal.")
	fmt.Fprintln(o.Out, "\nThese are the commands it would walk you through:")
	fmt.Fprintf(o.Out, "\n  export HMD_HOME=%q\n", home)
	fmt.Fprintln(o.Out, "  nsctl doctor")
	fmt.Fprintln(o.Out, "  nsctl env start local")
	fmt.Fprintln(o.Out, "  nsctl env status local")
	fmt.Fprintln(o.Out, "\nTo add your own repository:")
	fmt.Fprintln(o.Out, "  nsctl repoclass detect --path /path/to/repo")
	fmt.Fprintln(o.Out, "  nsctl agent skills install nsctl-onboard nsctl-repoclass-adopt --path /path/to/repo")
	return nserr.New(nserr.Usage, "nothing was run and nothing was written")
}

func (f *flow) intro() {
	fmt.Fprintf(f.Out, "nsctl %s\n\n", f.Version)
	fmt.Fprintln(f.Out, "NeuronSphere runs locally: one control plane, and environments your")
	fmt.Fprintln(f.Out, "deployments sit in. A container engine is the only prerequisite.")
	fmt.Fprintln(f.Out, "\nEach step names the command it runs, so you can repeat any of this by hand.")
	fmt.Fprintln(f.Out, "Nothing is deleted, purged or redeployed.")
}

// checkHost runs the same gate `control-plane start` runs, so the diagnostic and
// the gate cannot drift.
func (f *flow) checkHost(ctx context.Context) bool {
	f.section("Checking the host")
	f.shows("nsctl doctor")
	// doctor exits 2 when something needs fixing and 0 when checks only warn,
	// which is exactly the distinction this step wants.
	return f.Exec(ctx, "doctor") == nil
}

// settleHome gets HMD_HOME sorted without touching the user's shell.
//
// It prints the export line rather than writing it. The correct file is
// unknowable -- and it is theirs -- so an appended line would be a change they
// did not review. The run continues with --home so that declining to edit a
// profile does not cost them the walk-through.
func (f *flow) settleHome() bool {
	f.section("Choosing where local state lives")
	if f.home != "" {
		fmt.Fprintf(f.Out, "  HMD_HOME is %s\n", f.home)
		return true
	}
	proposed := filepath.Join(f.UserHome, "hmd")
	fmt.Fprintln(f.Out, "  HMD_HOME is not set. It is where nsctl keeps this machine's")
	fmt.Fprintln(f.Out, "  environments, caches and container names; nsctl never guesses it,")
	fmt.Fprintln(f.Out, "  because a guess would name containers no other HMD tool computes.")
	answer := f.p.Ask("\n  Path for HMD_HOME", proposed)
	if answer == "" {
		return false
	}
	path, err := filepath.Abs(expandUser(answer, f.UserHome))
	if err != nil {
		fmt.Fprintf(f.Err, "warning: %v\n", err)
		return false
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		fmt.Fprintf(f.Err, "warning: could not create %s: %v\n", path, err)
		return false
	}
	f.home = path
	f.homeFromWizard = true
	fmt.Fprintf(f.Out, "\n  Using %s for this run. To make it permanent, add this to your shell:\n", path)
	fmt.Fprintf(f.Out, "\n      export HMD_HOME=%q\n", path)
	fmt.Fprintln(f.Out, "\n  (nsctl does not edit your shell configuration.)")
	return true
}

// startEnvironment starts the first one, reusing env start's own first-run
// registration rather than a second way to create an environment.
func (f *flow) startEnvironment(ctx context.Context) (string, bool) {
	f.section("Starting an environment")
	fmt.Fprintln(f.Out, "  An environment is an emulated AWS account with its own cluster and")
	fmt.Fprintln(f.Out, "  database. The first start registers one for you.")

	slug := f.p.Ask("\n  Name it", "local")
	if slug == "" {
		slug = "local"
	}
	if !f.confirm(fmt.Sprintf("\n  Start %q now? This pulls images and takes a few minutes", slug), true) {
		fmt.Fprintf(f.Out, "\n  Skipped. Start it later with `nsctl%s env start %s`.\n", f.homeArg(), slug)
		return slug, false
	}
	if err := f.run(ctx, "env", "start", slug); err != nil {
		fmt.Fprintf(f.Err, "\nwarning: the environment did not start: %v\n", err)
		fmt.Fprintf(f.Out, "  `nsctl%s doctor` and `nsctl%s env status %s` say more.\n",
			f.homeArg(), f.homeArg(), slug)
		return slug, false
	}
	return slug, true
}

// offerStack offers a published stack, and only one that resolves.
func (f *flow) offerStack(ctx context.Context, slug string) {
	f.section("Deploying something to look at")
	fmt.Fprintln(f.Out, "  The substrate is not an application. A stack is a published set of")
	fmt.Fprintln(f.Out, "  repo classes pinned to versions known to work together.")

	if !f.stackResolves(ctx, candidateStack) {
		fmt.Fprintf(f.Out, "\n  No stack is published as %q on this machine's registries, so there is\n", candidateStack)
		fmt.Fprintln(f.Out, "  nothing to offer here. `nsctl stack add <ref>` takes any reference.")
		return
	}
	if !f.confirm(fmt.Sprintf("\n  Add and deploy the %q stack?", candidateStack), false) {
		fmt.Fprintf(f.Out, "\n  Skipped. `nsctl%s stack add %s --env %s --apply` does it later.\n",
			f.homeArg(), candidateStack, slug)
		return
	}
	if err := f.run(ctx, "stack", "add", candidateStack, "--env", slug, "--apply"); err != nil {
		fmt.Fprintf(f.Err, "\nwarning: the stack did not deploy: %v\n", err)
	}
}

// stackResolves asks the registry, quietly, whether the candidate exists.
func (f *flow) stackResolves(ctx context.Context, ref string) bool {
	if f.Capture == nil {
		return false
	}
	out, err := f.Capture(ctx, "stack", "versions", ref)
	return err == nil && strings.TrimSpace(out) != ""
}

// offerRepository offers to work out how the user's own repository deploys.
func (f *flow) offerRepository(ctx context.Context) {
	f.section("Adding your own repository")
	fmt.Fprintln(f.Out, "  nsctl deploys a repository that carries a BACON manifest. It can read")
	fmt.Fprintln(f.Out, "  yours and report how it already deploys -- from CI workflows, a")
	fmt.Fprintln(f.Out, "  Makefile, a chart, a Dockerfile -- and write what is unambiguous.")
	fmt.Fprintln(f.Out, "\n  It will not guess a dependency or a resource. Those need a person.")

	dir := f.Repo
	if dir == "" {
		if !f.confirm("\n  Point it at a repository now?", false) {
			fmt.Fprintln(f.Out, "\n  Skipped. `nsctl repoclass detect --path <dir>` does it later.")
			return
		}
		dir = f.p.Ask("\n  Repository path", ".")
	}
	path, err := filepath.Abs(expandUser(dir, f.UserHome))
	if err != nil || !isDir(path) {
		fmt.Fprintf(f.Err, "\nwarning: %s is not a directory; skipping.\n", dir)
		return
	}
	if err := f.run(ctx, "repoclass", "detect", "--path", path); err != nil {
		fmt.Fprintf(f.Err, "\nwarning: %v\n", err)
		return
	}
	if !f.confirm("\n  Write what it could decide into a manifest?", false) {
		fmt.Fprintf(f.Out, "\n  Nothing written. `nsctl repoclass detect --path %s --apply` writes it.\n", path)
		return
	}
	if err := f.run(ctx, "repoclass", "detect", "--path", path, "--apply"); err != nil {
		fmt.Fprintf(f.Err, "\nwarning: %v\n", err)
	}
	f.Repo = path
}

// offerSkills hands the judgement half to the user's coding agent.
func (f *flow) offerSkills(ctx context.Context) {
	f.section("Working with an AI agent")
	fmt.Fprintln(f.Out, "  What detect refuses to guess -- which dependency roles this repository")
	fmt.Fprintln(f.Out, "  needs, what its deploy command should be -- is judgement. nsctl bundles")
	fmt.Fprintln(f.Out, "  guidance that teaches Codex or Claude Code to work it out with you and")
	fmt.Fprintln(f.Out, "  to drive nsctl rather than invent a manifest layout.")

	dir := f.Repo
	if dir == "" {
		dir = "."
	}
	if !f.confirm("\n  Install the agent skills into this repository?", false) {
		fmt.Fprintln(f.Out, "\n  Skipped. `nsctl agent skills list --path <dir>` shows what there is.")
		return
	}
	if err := f.run(ctx, "agent", "skills", "install",
		"nsctl-onboard", "nsctl-local-environment", "nsctl-repoclass-adopt",
		"--host", "all", "--path", dir); err != nil {
		fmt.Fprintf(f.Err, "\nwarning: %v\n", err)
	}
}

func (f *flow) closing(slug string) {
	f.section("Where to go next")
	h := f.homeArg()
	fmt.Fprintf(f.Out, "  nsctl%s env status %s        what is running, and its URLs\n", h, slug)
	fmt.Fprintf(f.Out, "  nsctl%s env credentials %s   how to sign in to what you deployed\n", h, slug)
	fmt.Fprintf(f.Out, "  nsctl%s env stop %s          stop it, keeping its state\n", h, slug)
	if f.homeFromWizard {
		fmt.Fprintf(f.Out, "\n  Remember `export HMD_HOME=%q`, or every command needs --home.\n", f.home)
	}
}

// run shows the invocation and then makes it, so the transcript and the action
// cannot disagree.
func (f *flow) run(ctx context.Context, argv ...string) error {
	full := f.withHome(argv)
	f.shows("nsctl " + strings.Join(full, " "))
	return f.Exec(ctx, full...)
}

// withHome adds --home only when the flow chose the home, so a user who already
// exported it sees commands they can paste unchanged.
func (f *flow) withHome(argv []string) []string {
	if !f.homeFromWizard || f.home == "" {
		return argv
	}
	return append([]string{"--home", f.home}, argv...)
}

func (f *flow) homeArg() string {
	if !f.homeFromWizard || f.home == "" {
		return ""
	}
	return " --home " + f.home
}

func (f *flow) confirm(question string, def bool) bool {
	if f.Yes {
		fmt.Fprintf(f.Out, "%s: %s (--yes)\n", question, yesNo(def))
		return def
	}
	return f.p.Confirm(question, def)
}

func (f *flow) section(title string) {
	fmt.Fprintf(f.Out, "\n%s\n%s\n", title, strings.Repeat("-", len(title)))
}

func (f *flow) shows(command string) {
	fmt.Fprintf(f.Out, "\n  $ %s\n\n", command)
}

// expandUser resolves a leading ~ against the resolved user home rather than
// leaving the shell's expansion to a path typed at a prompt, where no shell ran.
func expandUser(path, userHome string) string {
	if path == "~" {
		return userHome
	}
	if strings.HasPrefix(path, "~/") && userHome != "" {
		return filepath.Join(userHome, path[2:])
	}
	return path
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
