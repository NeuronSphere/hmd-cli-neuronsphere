package quickstart

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder captures the invocations the flow makes, which is the property under
// test: the flow's whole job is to run the commands it advertises.
type recorder struct {
	calls   [][]string
	fail    map[string]error
	capture map[string]string
}

func (r *recorder) exec(_ context.Context, argv ...string) error {
	r.calls = append(r.calls, argv)
	if err, ok := r.fail[strings.Join(argv, " ")]; ok {
		return err
	}
	return nil
}

func (r *recorder) captureFn(_ context.Context, argv ...string) (string, error) {
	key := strings.Join(argv, " ")
	if out, ok := r.capture[key]; ok {
		return out, nil
	}
	return "", fmt.Errorf("nothing answers for %s", key)
}

func (r *recorder) ran(prefix ...string) bool {
	want := strings.Join(prefix, " ")
	for _, c := range r.calls {
		if strings.HasPrefix(strings.Join(c, " "), want) {
			return true
		}
	}
	return false
}

// run drives the flow with scripted answers. A bytes.Reader is not a terminal, so
// the flow is built directly rather than through Run, which refuses one.
func drive(t *testing.T, answers string, o Options, rec *recorder) string {
	t.Helper()
	var out bytes.Buffer
	o.In = strings.NewReader(answers)
	o.Out = &out
	o.Err = &out
	o.Exec = rec.exec
	o.Capture = rec.captureFn
	if o.UserHome == "" {
		o.UserHome = t.TempDir()
	}

	f := &flow{Options: o, p: newPrompter(o), home: o.Home}
	f.intro()
	if !f.checkHost(context.Background()) {
		return out.String()
	}
	if !f.settleHome() {
		return out.String()
	}
	slug, running := f.startEnvironment(context.Background())
	if running {
		f.offerStack(context.Background(), slug)
	}
	f.offerRepository(context.Background())
	f.offerSkills(context.Background())
	f.closing(slug)
	return out.String()
}

// The flow refuses rather than half-running when there is nobody to answer, and
// prints the sequence instead -- which is what a CI job actually wants.
func TestNonInteractiveRefusesAndPrintsTheSequence(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	rec := &recorder{}
	err := Run(context.Background(), Options{
		In: strings.NewReader(""), Out: &out, Err: &out,
		UserHome: "/home/someone", Exec: rec.exec, Capture: rec.captureFn,
	})
	if err == nil {
		t.Fatal("want a refusal with no terminal")
	}
	text := out.String()
	for _, want := range []string{
		`export HMD_HOME="/home/someone/hmd"`,
		"nsctl doctor",
		"nsctl env start local",
		"nsctl repoclass detect --path",
		"nsctl agent skills install",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the printed sequence is missing %q:\n%s", want, text)
		}
	}
	if len(rec.calls) != 0 {
		t.Errorf("nothing should have run, got %v", rec.calls)
	}
	if !strings.Contains(err.Error(), "nothing was run and nothing was written") {
		t.Errorf("err = %v", err)
	}
}

// A failed host check stops the run before anything is created.
func TestAFailedHostCheckStops(t *testing.T) {
	t.Parallel()

	rec := &recorder{fail: map[string]error{"doctor": fmt.Errorf("no engine")}}
	home := t.TempDir()
	text := drive(t, "", Options{Home: home, Version: "v1"}, rec)

	if !rec.ran("doctor") {
		t.Error("doctor should have run")
	}
	if rec.ran("env", "start") {
		t.Error("nothing should be started after a failed host check")
	}
	if strings.Contains(text, "Starting an environment") {
		t.Errorf("the run should stop before the environment section:\n%s", text)
	}
}

// Every step names the command it runs, so the session is a transcript. The
// commands the flow shows and the commands it runs must be the same.
func TestEveryStepShowsTheCommandItRuns(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	rec := &recorder{capture: map[string]string{
		"stack versions observability": "0.1.0\n",
	}}
	// name=dev, start=yes, stack=yes, adopt path given, apply=yes, skills=yes
	text := drive(t, "dev\ny\ny\ny\ny\n", Options{
		Home: t.TempDir(), Version: "v1", Repo: repo,
	}, rec)

	for _, want := range [][]string{
		{"doctor"},
		{"env", "start", "dev"},
		{"stack", "add", "observability", "--env", "dev", "--apply"},
		{"repoclass", "detect", "--path", repo},
		{"agent", "skills", "install"},
	} {
		if !rec.ran(want...) {
			t.Errorf("did not run %v; calls were %v", want, rec.calls)
		}
		if shown := "$ nsctl " + strings.Join(want, " "); !strings.Contains(text, shown) {
			t.Errorf("did not show %q:\n%s", shown, text)
		}
	}
	// The closing block names the two things a reader needs next.
	for _, want := range []string{"env status dev", "env credentials dev"} {
		if !strings.Contains(text, want) {
			t.Errorf("the closing block should name %q:\n%s", want, text)
		}
	}
}

// A stack reference that resolves to nothing is not offered. The README, the
// first tutorial and the stacks how-to have all named a stack that is not
// published, and a guided run whose "here is something real" step fails is worse
// than one without the step.
func TestAnUnresolvableStackIsNotOffered(t *testing.T) {
	t.Parallel()

	rec := &recorder{} // capture answers for nothing
	text := drive(t, "dev\ny\ny\n", Options{Home: t.TempDir(), Version: "v1", Repo: t.TempDir()}, rec)

	if rec.ran("stack", "add") {
		t.Errorf("an unresolvable stack must not be added; calls %v", rec.calls)
	}
	if !strings.Contains(text, "No stack is published as") {
		t.Errorf("the skip should be explained:\n%s", text)
	}
}

// Declining a step does not abort the run.
func TestDecliningAStepContinues(t *testing.T) {
	t.Parallel()

	rec := &recorder{capture: map[string]string{"stack versions observability": "0.1.0\n"}}
	// name default, start=no -> the stack step is skipped with it, then adopt=no,
	// skills=no.
	text := drive(t, "\nn\nn\nn\n", Options{Home: t.TempDir(), Version: "v1"}, rec)

	if rec.ran("env", "start") {
		t.Error("a declined start should not run")
	}
	if !strings.Contains(text, "Skipped. Start it later with") {
		t.Errorf("a declined step should say how to do it later:\n%s", text)
	}
	// The run still reaches the end.
	if !strings.Contains(text, "Where to go next") {
		t.Errorf("declining should not abort the run:\n%s", text)
	}
}

// HMD_HOME is never written into a shell profile: the correct file is unknowable
// and it is the user's. The path is printed and --home carries the run.
func TestSettleHomePrintsTheExportAndNeverWritesAProfile(t *testing.T) {
	t.Parallel()

	userHome := t.TempDir()
	rec := &recorder{}
	text := drive(t, "\n\nn\nn\nn\n", Options{Version: "v1", UserHome: userHome}, rec)

	proposed := filepath.Join(userHome, "hmd")
	if !strings.Contains(text, fmt.Sprintf("export HMD_HOME=%q", proposed)) {
		t.Errorf("the export line should be printed:\n%s", text)
	}
	if !strings.Contains(text, "does not edit your shell configuration") {
		t.Errorf("the run should say it wrote no profile:\n%s", text)
	}
	if info, err := os.Stat(proposed); err != nil || !info.IsDir() {
		t.Errorf("the chosen home should be created: %v", err)
	}
	// Nothing else in the user's home was touched.
	entries, err := os.ReadDir(userHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "hmd" {
		t.Errorf("the flow wrote outside the chosen home: %v", entries)
	}
	// Later invocations carry --home, because the export may not have been run.
	for _, c := range rec.calls {
		if c[0] == "doctor" {
			continue
		}
		if c[0] != "--home" {
			t.Errorf("invocation %v should carry --home", c)
		}
	}
}

// A home the user already exported is left alone, and the printed commands are
// ones they can paste unchanged.
func TestAnExistingHomeIsNotReadvertised(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	rec := &recorder{}
	text := drive(t, "dev\nn\nn\nn\n", Options{Home: home, Version: "v1"}, rec)

	if strings.Contains(text, "export HMD_HOME") {
		t.Errorf("an already-set home should not be re-proposed:\n%s", text)
	}
	if strings.Contains(text, "--home") {
		t.Errorf("printed commands should not carry --home when the home is exported:\n%s", text)
	}
}

// --yes takes the default answer, which is "no" for everything that deploys: a
// scripted walk-through must not become a deploy nobody asked for.
func TestYesTakesTheDefaultsAndDefaultsAreConservative(t *testing.T) {
	t.Parallel()

	rec := &recorder{capture: map[string]string{"stack versions observability": "0.1.0\n"}}
	drive(t, "", Options{Home: t.TempDir(), Version: "v1", Yes: true, Repo: t.TempDir()}, rec)

	if !rec.ran("env", "start", "local") {
		t.Errorf("starting an environment is the default; calls %v", rec.calls)
	}
	for _, mustNot := range [][]string{
		{"stack", "add"},
		{"repoclass", "detect", "--path", "", "--apply"},
		{"agent", "skills", "install"},
	} {
		if rec.ran(mustNot...) {
			t.Errorf("%v should not happen by default; calls %v", mustNot, rec.calls)
		}
	}
}

func TestExpandUser(t *testing.T) {
	t.Parallel()

	if got := expandUser("~/src/x", "/home/a"); got != "/home/a/src/x" {
		t.Errorf("expandUser = %q", got)
	}
	if got := expandUser("~", "/home/a"); got != "/home/a" {
		t.Errorf("expandUser = %q", got)
	}
	if got := expandUser("/abs", "/home/a"); got != "/abs" {
		t.Errorf("expandUser = %q", got)
	}
	// No resolved home: left as written rather than joined to nothing.
	if got := expandUser("~/x", ""); got != "~/x" {
		t.Errorf("expandUser = %q", got)
	}
}

// A start that fails skips only the step that needs a running environment. This
// is the case a live run found: the two steps that work with nothing up --
// adopting a repository and installing skills -- were being denied to exactly
// the user whose first start did not work.
func TestAFailedStartStillOffersTheOfflineSteps(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	rec := &recorder{
		fail:    map[string]error{"env start local": fmt.Errorf("another home owns those containers")},
		capture: map[string]string{"stack versions observability": "0.1.0\n"},
	}
	// name (default), start=y, detect --apply=y, skills=y. With Repo set the
	// "point it at a repository" question is not asked.
	text := drive(t, "\ny\ny\ny\n", Options{Home: t.TempDir(), Version: "v1", Repo: repo}, rec)

	if !rec.ran("env", "start", "local") {
		t.Fatalf("the start should have been attempted; calls %v", rec.calls)
	}
	if rec.ran("stack", "add") {
		t.Error("a stack needs a running environment and must be skipped")
	}
	if !rec.ran("repoclass", "detect", "--path", repo) {
		t.Errorf("adopting a repository needs no environment; calls %v", rec.calls)
	}
	if !rec.ran("agent", "skills", "install") {
		t.Errorf("installing skills needs no environment; calls %v", rec.calls)
	}
	if !strings.Contains(text, "Where to go next") {
		t.Errorf("the run should still reach its end:\n%s", text)
	}
	// And it names how to start it, since it is not running.
	if !strings.Contains(text, "env start local") {
		t.Errorf("the closing block should name the start command:\n%s", text)
	}
}
