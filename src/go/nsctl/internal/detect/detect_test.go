package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write builds a repository from a map of relative path to contents.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func run(t *testing.T, files map[string]string) Result {
	t.Helper()
	r, err := Run(write(t, files), Known{
		BundledClasses: []string{"hmd-ms-naming"},
		ReservedNames:  []string{"local-neuronsphere"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func has(r Result, field string, c Confidence) (Finding, bool) {
	for _, f := range r.Findings {
		if f.Field == field && f.Confidence == c {
			return f, true
		}
	}
	return Finding{}, false
}

// A CI step that deploys is the author's own statement of how the repository
// deploys, and it outranks anything inferred from a chart being present.
func TestWorkflowIsAuthoritative(t *testing.T) {
	t.Parallel()

	r := run(t, map[string]string{
		"package.json": `{"name":"@acme/checkout","description":"Takes payment. And more prose."}`,
		".github/workflows/release.yml": `name: release
jobs:
  ship:
    container: ghcr.io/acme/ci-tools:3.2
    steps:
      - run: helm upgrade --install checkout ./chart
`,
		"chart/Chart.yaml": "apiVersion: v2\nname: checkout\n",
	})

	if r.Mechanism != "external" {
		t.Errorf("Mechanism = %q, want external", r.Mechanism)
	}
	entry, ok := has(r, "build.external.entry_point", Decided)
	if !ok || entry.Value != ".github/workflows/release.yml" {
		t.Errorf("entry point = %+v", entry)
	}
	if !strings.Contains(entry.Where, "release.yml:6") {
		t.Errorf("the conclusion should name the line it came from, got %q", entry.Where)
	}
	image, ok := has(r, "deploy.image", Decided)
	if !ok || image.Value != "ghcr.io/acme/ci-tools:3.2" {
		t.Errorf("image = %+v", image)
	}
	// An npm scope is not part of the name, and the hmd- prefix is not
	// prepended: this is the user's repository.
	name, _ := has(r, "name", Decided)
	if name.Value != "checkout" {
		t.Errorf("name = %q, want the scope stripped and no hmd- prefix", name.Value)
	}
	// Provisional, and only the first sentence.
	desc, _ := has(r, "description", Decided)
	if desc.Value != "Takes payment." {
		t.Errorf("description = %q, want the first sentence only", desc.Value)
	}
}

// A repository that is already NeuronSphere-shaped gets the native tools, not an
// exec wrapping them.
func TestNativeLayoutProposesTheNativeTools(t *testing.T) {
	t.Parallel()

	r := run(t, map[string]string{
		"README.md":             "# Thing\n\nDoes a thing.\n",
		"src/helm/Chart.yaml":   "name: thing\n",
		"src/docker/Dockerfile": "FROM alpine\n",
	})
	if r.Mechanism != "tool_set" {
		t.Errorf("Mechanism = %q, want tool_set", r.Mechanism)
	}
	cmds, ok := has(r, "deploy.commands", Decided)
	if !ok || cmds.Value != "docker helm" {
		t.Errorf("commands = %+v, want the native tools", cmds)
	}
}

// A task runner is the best exec candidate short of a CI step: an entry point the
// author maintains and tests.
func TestMakefileTargetIsTheExecCandidate(t *testing.T) {
	t.Parallel()

	r := run(t, map[string]string{
		"README.md": "# Thing\n\nDoes a thing.\n",
		"Makefile":  "build:\n\tgo build\n\ndeploy:\n\t./scripts/ship.sh\n",
	})
	if r.Mechanism != "external" {
		t.Errorf("Mechanism = %q", r.Mechanism)
	}
	cmds, ok := has(r, "deploy.commands", Decided)
	if !ok || cmds.Value != "make deploy" {
		t.Errorf("commands = %+v", cmds)
	}
	if cmds.Where != "Makefile:4" {
		t.Errorf("Where = %q, want the target's line", cmds.Where)
	}
}

// A PaaS manifest is reported and stopped at: there is no local equivalent of a
// platform deploy, and saying so is the honest answer.
func TestPaaSReportsAndStops(t *testing.T) {
	t.Parallel()

	r := run(t, map[string]string{
		"README.md": "# Thing\n\nDoes a thing.\n",
		"Procfile":  "web: node server.js\n",
		"fly.toml":  "app = \"thing\"\n",
	})
	if r.Mechanism != "" {
		t.Errorf("Mechanism = %q, want none decided", r.Mechanism)
	}
	var seen []string
	for _, f := range r.Findings {
		if f.Field == "deploy.commands" && f.Confidence == Undecided {
			seen = append(seen, f.Where)
		}
	}
	if len(seen) != 2 {
		t.Errorf("want both PaaS files reported, got %v", seen)
	}
	for _, f := range r.Findings {
		if f.Where == "Procfile" && !strings.Contains(f.Why, "no local equivalent") {
			t.Errorf("Why = %q", f.Why)
		}
	}
}

// The refusals are the assertions that matter: SPEC010 draws the boundary between
// the deterministic layer and the judgement layer, and the cost of crossing it is
// asymmetric.
func TestRefusalsAreAlwaysReported(t *testing.T) {
	t.Parallel()

	r := run(t, map[string]string{
		"README.md":          "# Thing\n\nDoes a thing.\n",
		"Makefile":           "deploy:\n\t./ship\n",
		"docker-compose.yml": "services:\n  web:\n    environment:\n      DB_HOST: db\n",
	})
	for _, field := range []string{"deploy.dependencies", "deploy.resources", "discovery"} {
		f, ok := has(r, field, Refused)
		if !ok {
			t.Errorf("%s: no refusal reported", field)
			continue
		}
		if f.Why == "" {
			t.Errorf("%s: a refusal with no reason is not a statement", field)
		}
	}
	// A compose environment: block is evidence for a person, never a written
	// default_configuration.
	if f, ok := has(r, "deploy.default_configuration", Refused); !ok {
		t.Error("a compose file's environment: block should be refused explicitly")
	} else if !strings.Contains(f.Why, "cannot be told apart") {
		t.Errorf("Why = %q", f.Why)
	}
}

// A name that would collide is not silently taken.
func TestCollidingNameIsUndecided(t *testing.T) {
	t.Parallel()

	r := run(t, map[string]string{
		"package.json": `{"name":"hmd-ms-naming","description":"A thing."}`,
	})
	f, ok := has(r, "name", Undecided)
	if !ok {
		t.Fatalf("a colliding name should be undecided, got %+v", r.Findings)
	}
	if !strings.Contains(f.Why, "already carries") {
		t.Errorf("Why = %q", f.Why)
	}
	// And the directory name is then taken instead, so detection still gets a
	// name: the collision rules out one candidate, not all of them.
	if _, ok := has(r, "name", Decided); !ok {
		t.Error("a later candidate should still supply a name")
	}
}

// An existing manifest stops detection: `describe` and `validate` are the verbs.
func TestAlreadyARepoClass(t *testing.T) {
	t.Parallel()

	r := run(t, map[string]string{
		"meta-data/manifest.json": `{"name":"x","description":"d","build":{}}`,
	})
	if !r.AlreadyAClass {
		t.Fatal("an existing manifest should be recognised")
	}
	if len(r.Findings) != 1 || !strings.Contains(r.Findings[0].Why, "describe") {
		t.Errorf("findings = %+v", r.Findings)
	}
	if _, err := Apply(r, &fakeWriter{}, "d"); err == nil {
		t.Error("applying to an existing class should refuse")
	}
}

type fakeWriter struct {
	name, description, image, mechanism string
	exec, tools                         []string
}

func (f *fakeWriter) Init(name, description string) error {
	f.name, f.description = name, description
	return nil
}
func (f *fakeWriter) SetMechanism(_, mechanism string) error { f.mechanism = mechanism; return nil }
func (f *fakeWriter) SetExec(argv []string) error            { f.exec = argv; return nil }
func (f *fakeWriter) SetToolCommands(tools []string) error   { f.tools = tools; return nil }
func (f *fakeWriter) SetImage(ref string) error              { f.image = ref; return nil }

// Apply writes exactly four members and nothing else. This is the test that
// keeps SPEC010 true as the detector grows: the Writer interface has no method
// that could write a dependency or a resource.
func TestApplyWritesOnlyWhatWasDecided(t *testing.T) {
	t.Parallel()

	r := run(t, map[string]string{
		"package.json": `{"name":"checkout","description":"Takes payment."}`,
		".github/workflows/ship.yml": `jobs:
  go:
    container: acme/tools:1
    steps:
      - run: kubectl apply -f k8s/
`,
	})
	w := &fakeWriter{}
	written, err := Apply(r, w, "")
	if err != nil {
		t.Fatal(err)
	}
	if w.name != "checkout" || w.description != "Takes payment." {
		t.Errorf("writer got %+v", w)
	}
	if w.mechanism != "external" {
		t.Errorf("mechanism = %q", w.mechanism)
	}
	if w.image != "acme/tools:1" {
		t.Errorf("image = %q", w.image)
	}
	for _, field := range written {
		switch field {
		case "name", "description", "build", "build.mechanism", "deploy.commands", "deploy.image":
		default:
			t.Errorf("Apply wrote %q, which is outside SPEC010's boundary", field)
		}
	}
}

// BACON requires a description, so a manifest written without one cannot
// validate -- refusing and naming the flag beats writing a broken file.
func TestApplyRefusesWithoutADescription(t *testing.T) {
	t.Parallel()

	r := run(t, map[string]string{"Procfile": "web: node x\n"})
	if _, err := Apply(r, &fakeWriter{}, ""); err == nil {
		t.Fatal("want a refusal when no description could be decided")
	} else if !strings.Contains(err.Error(), "--description") {
		t.Errorf("the refusal should name the flag, got %q", err)
	}
	w := &fakeWriter{}
	if _, err := Apply(r, w, "Supplied by hand"); err != nil {
		t.Fatalf("a supplied description should be accepted: %v", err)
	}
	if w.description != "Supplied by hand" {
		t.Errorf("description = %q", w.description)
	}
}

func TestSlugify(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"@acme/Checkout Service": "checkout-service",
		"My_Repo.v2":             "my-repo-v2",
		"  spaced  ":             "spaced",
		"---":                    "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
