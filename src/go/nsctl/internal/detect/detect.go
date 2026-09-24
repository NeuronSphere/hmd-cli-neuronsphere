// Package detect inspects a repository and reports how it already deploys, in
// what image, what could not be decided, and what will not be guessed
// (NERD009 SPEC009 and SPEC010).
//
// Every conclusion carries the file and line that produced it. That is the whole
// point: a reader has to be able to disagree with a finding by opening the file
// it came from, because a classification they cannot check is a classification
// they have to trust.
//
// The questions are asked in order, because the later ones are only interesting
// when the earlier ones fail: is there an author-maintained deploy entry point?
// then what image does their pipeline run it in? then what deployable artifacts
// exist at all?
//
// SPEC010's refusals are the load-bearing half of this package, and they are
// enforced in one place -- Apply writes exactly four things and nothing reaches
// deploy.dependencies, deploy.resources or meta-data/resources/. A wrong
// dependency role marked required does not fail its own node; it fails the entire
// ChangeSet server-side with a message naming only the role, and a newcomer's
// first `env apply` failing on a role they never chose is the worst outcome
// available -- produced by guessing helpfully.
package detect

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Confidence is how much a finding settles.
type Confidence string

const (
	// Decided is unambiguous and is what --apply writes.
	Decided Confidence = "decided"
	// Evidence is a real signal that does not settle the question on its own.
	Evidence Confidence = "evidence"
	// Undecided is a question the repository raises and does not answer.
	Undecided Confidence = "undecided"
	// Refused is something detection will not infer on principle. Reported so
	// that its absence from the manifest is a statement rather than an omission.
	Refused Confidence = "refused"
)

// Finding is one conclusion about the repository.
type Finding struct {
	// Field is the manifest member the finding is about, or "" for one that is
	// about the repository as a whole.
	Field      string     `json:"field,omitempty"`
	Confidence Confidence `json:"confidence"`
	// Value is what was concluded, empty for an Undecided or Refused finding.
	Value string `json:"value,omitempty"`
	// Where is the repo-relative file and line the conclusion came from.
	Where string `json:"where,omitempty"`
	Why   string `json:"why"`
}

// Result is the whole classification.
type Result struct {
	// Mechanism is the deploy mechanism proposed: "exec" for an
	// author-maintained entry point, "tool_set" for a repository that is already
	// NeuronSphere-shaped, or "" when neither could be decided.
	Mechanism string `json:"mechanism,omitempty"`
	// AlreadyAClass is true when meta-data/manifest.* exists. Detection then
	// reports and writes nothing: `describe` and `validate` are the verbs.
	AlreadyAClass bool      `json:"already_a_class"`
	Findings      []Finding `json:"findings"`
}

// Decided returns the value of a decided finding for a field.
func (r Result) Decided(field string) (string, bool) {
	for _, f := range r.Findings {
		if f.Field == field && f.Confidence == Decided {
			return f.Value, true
		}
	}
	return "", false
}

// Known is what the detector cannot learn from the repository: the names that
// would collide. Passed in so this package stays free of the bundled registry
// and the environment manifest's reserved list.
type Known struct {
	BundledClasses []string
	ReservedNames  []string
}

// Run inspects dir. It never writes and never fails on a repository it cannot
// classify: "I could not decide" is a result, not an error.
func Run(dir string, known Known) (Result, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return Result{}, fmt.Errorf("%s is not a directory", dir)
	}
	d := &detector{dir: abs, known: known}

	if path, ok := d.existingManifest(); ok {
		d.add(Finding{Confidence: Decided, Value: path, Where: path,
			Why: "this is already a repo class; `nsctl repoclass describe` reads it and `validate` checks it"})
		return Result{AlreadyAClass: true, Findings: d.findings}, nil
	}

	d.name()
	d.description()
	d.version()
	d.workflows()
	d.nativeLayout()
	d.skaffold()
	d.taskRunners()
	d.otherEvidence()
	d.paas()
	d.refusals()

	sort.SliceStable(d.findings, func(i, j int) bool {
		return rank(d.findings[i].Confidence) < rank(d.findings[j].Confidence)
	})
	return Result{Mechanism: d.mechanism, Findings: d.findings}, nil
}

func rank(c Confidence) int {
	switch c {
	case Decided:
		return 0
	case Evidence:
		return 1
	case Undecided:
		return 2
	default:
		return 3
	}
}

type detector struct {
	dir       string
	known     Known
	findings  []Finding
	mechanism string
	// entryPoint is the author-maintained command, once one is found. The first
	// one found wins, which is why workflows are read before task runners.
	entryPoint []string
}

func (d *detector) add(f Finding) { d.findings = append(d.findings, f) }

// Argv is the deploy command to propose, empty when none was decided.
func (r Result) Argv() []string {
	v, ok := r.Decided("deploy.commands")
	if !ok {
		return nil
	}
	return strings.Fields(v)
}

func (d *detector) existingManifest() (string, bool) {
	for _, rel := range []string{
		filepath.Join("meta-data", "manifest.json"),
		filepath.Join("meta-data", "manifest.toml"),
	} {
		if _, err := os.Stat(filepath.Join(d.dir, rel)); err == nil {
			return filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// name slugifies the repository's own name. The hmd- prefix is deliberately not
// prepended: this is the user's repository, not a NeuronSphere one.
func (d *detector) name() {
	candidates := []struct{ value, where, why string }{}
	if n, where := d.jsonField("package.json", "name"); n != "" {
		candidates = append(candidates, struct{ value, where, why string }{n, where, "package.json names it"})
	}
	if n, where := d.yamlName(filepath.Join("chart", "Chart.yaml")); n != "" {
		candidates = append(candidates, struct{ value, where, why string }{n, where, "the chart names it"})
	}
	if n, where := d.yamlName("Chart.yaml"); n != "" {
		candidates = append(candidates, struct{ value, where, why string }{n, where, "the chart names it"})
	}
	candidates = append(candidates, struct{ value, where, why string }{
		filepath.Base(d.dir), ".", "the directory name"})

	for _, c := range candidates {
		slug := slugify(c.value)
		if slug == "" {
			continue
		}
		if why, bad := d.nameCollides(slug); bad {
			d.add(Finding{Field: "name", Confidence: Undecided, Where: c.where,
				Why: fmt.Sprintf("%s gives %q, which %s; choose another with `nsctl repoclass init <name>`", c.why, slug, why)})
			continue
		}
		d.add(Finding{Field: "name", Confidence: Decided, Value: slug, Where: c.where, Why: c.why})
		return
	}
}

func (d *detector) nameCollides(slug string) (string, bool) {
	for _, r := range d.known.ReservedNames {
		if r == slug {
			return "is a reserved instance name", true
		}
	}
	for _, b := range d.known.BundledClasses {
		if b == slug {
			return "is a repo class nsctl already carries", true
		}
	}
	return "", false
}

// description takes a provisional first sentence and says it is provisional.
// SPEC010 refuses anything beyond that: a description is prose about intent, and
// a generated paragraph reads as considered when it is not.
func (d *detector) description() {
	if v, where := d.jsonField("package.json", "description"); v != "" {
		d.add(Finding{Field: "description", Confidence: Decided, Value: firstSentence(v), Where: where,
			Why: "package.json's description, as a provisional line for a human to improve"})
		return
	}
	if v, where := d.tomlDescription(); v != "" {
		d.add(Finding{Field: "description", Confidence: Decided, Value: firstSentence(v), Where: where,
			Why: "pyproject.toml's description, as a provisional line for a human to improve"})
		return
	}
	for _, rel := range []string{"README.md", "README.rst", "README.txt", "README"} {
		if v, where := d.readmeSentence(rel); v != "" {
			d.add(Finding{Field: "description", Confidence: Decided, Value: v, Where: where,
				Why: "the README's first sentence, as a provisional line for a human to improve"})
			return
		}
	}
	d.add(Finding{Field: "description", Confidence: Undecided,
		Why: "nothing in the repository states what it is in one line; BACON requires a description"})
}

func (d *detector) version() {
	rel := filepath.Join("meta-data", "VERSION")
	if _, err := os.Stat(filepath.Join(d.dir, rel)); err == nil {
		return
	}
	d.add(Finding{Field: "meta-data/VERSION", Confidence: Decided, Value: "0.1",
		Why: "there is no meta-data/VERSION; 0.1 is written (SPEC007)"})
}

// deployVerbs are the run: steps that mean "this is how the repository deploys".
var deployVerbs = []*regexp.Regexp{
	regexp.MustCompile(`helm\s+upgrade`),
	regexp.MustCompile(`kubectl\s+apply`),
	regexp.MustCompile(`kustomize\s+build`),
	regexp.MustCompile(`skaffold\s+run`),
	regexp.MustCompile(`(terraform|tofu)\s+apply`),
	regexp.MustCompile(`pulumi\s+up`),
	regexp.MustCompile(`docker\s+(build|push)`),
	regexp.MustCompile(`argocd\s+app\s+sync`),
}

var (
	containerLine = regexp.MustCompile(`^\s*(?:image:\s*)?container:\s*(\S+)\s*$`)
	imageLine     = regexp.MustCompile(`^\s*image:\s*(\S+)\s*$`)
	dockerPush    = regexp.MustCompile(`docker\s+push\s+(\S+)`)
)

// workflows reads the CI workflows. A run: step that deploys is the author's own
// statement of how the repository deploys, which outranks everything inferred
// from the presence of a chart or a Dockerfile.
func (d *detector) workflows() {
	dir := filepath.Join(d.dir, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		rel := filepath.ToSlash(filepath.Join(".github", "workflows", name))
		lines, err := readLines(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for i, line := range lines {
			for _, verb := range deployVerbs {
				if !verb.MatchString(line) {
					continue
				}
				where := fmt.Sprintf("%s:%d", rel, i+1)
				if d.mechanism == "" {
					d.mechanism = "external"
					d.add(Finding{Field: "build.external.entry_point", Confidence: Decided, Value: rel, Where: where,
						Why: "a workflow step deploys, which is this repository's own statement of how it does so"})
				}
				d.add(Finding{Field: "deploy", Confidence: Evidence, Value: strings.TrimSpace(line), Where: where,
					Why: "a deploying step in CI"})
				if img, iw := imageNear(lines, i, rel); img != "" {
					if _, already := d.decided("deploy.image"); !already {
						d.add(Finding{Field: "deploy.image", Confidence: Decided, Value: img, Where: iw,
							Why: "the image that job runs in, or the one it builds and pushes"})
					}
				}
				break
			}
		}
	}
}

// imageNear looks for the job's container: or a pushed image around a matching
// step. Deliberately a scan rather than a YAML walk: the file may be templated,
// and a parse failure would lose the evidence the scan still finds.
func imageNear(lines []string, at int, rel string) (string, string) {
	lo := at - 40
	if lo < 0 {
		lo = 0
	}
	hi := at + 10
	if hi > len(lines) {
		hi = len(lines)
	}
	for i := lo; i < hi; i++ {
		if m := containerLine.FindStringSubmatch(lines[i]); m != nil {
			return m[1], fmt.Sprintf("%s:%d", rel, i+1)
		}
	}
	for i := lo; i < hi; i++ {
		if m := dockerPush.FindStringSubmatch(lines[i]); m != nil {
			return strings.Trim(m[1], `"'`), fmt.Sprintf("%s:%d", rel, i+1)
		}
	}
	for i := lo; i < hi; i++ {
		if m := imageLine.FindStringSubmatch(lines[i]); m != nil && !strings.Contains(lines[i], "${{") {
			return m[1], fmt.Sprintf("%s:%d", rel, i+1)
		}
	}
	return "", ""
}

// nativeLayout recognises a repository that is already NeuronSphere-shaped and
// proposes the native tool rather than exec.
func (d *detector) nativeLayout() {
	var tools []string
	for tool, probe := range map[string]string{
		"helm":   filepath.Join("src", "helm", "Chart.yaml"),
		"docker": filepath.Join("src", "docker", "Dockerfile"),
		"cdktf":  filepath.Join("src", "cdktf"),
	} {
		if _, err := os.Stat(filepath.Join(d.dir, probe)); err == nil {
			tools = append(tools, tool)
		}
	}
	if len(tools) == 0 {
		return
	}
	sort.Strings(tools)
	if d.mechanism == "" || d.mechanism == "tool_set" {
		d.mechanism = "tool_set"
		d.entryPoint = nil
		d.add(Finding{Field: "deploy.commands", Confidence: Decided, Value: strings.Join(tools, " "),
			Where: "src/", Why: "this repository is already NeuronSphere-shaped; the native tools deploy it, not an exec"})
		return
	}
	d.add(Finding{Field: "deploy.commands", Confidence: Evidence, Value: strings.Join(tools, " "), Where: "src/",
		Why: "NeuronSphere tool directories are present, but CI already states how this repository deploys"})
}

// skaffold is the clearest machine-readable deploy description available when
// there is one: take its paths directly.
func (d *detector) skaffold() {
	for _, rel := range []string{"skaffold.yaml", "skaffold.yml"} {
		lines, err := readLines(filepath.Join(d.dir, rel))
		if err != nil {
			continue
		}
		for i, line := range lines {
			t := strings.TrimSpace(line)
			for _, key := range []string{"chartPath:", "rawYaml:", "image:"} {
				if strings.HasPrefix(t, key) {
					d.add(Finding{Field: "deploy", Confidence: Evidence, Value: t,
						Where: fmt.Sprintf("%s:%d", rel, i+1),
						Why:   "skaffold describes this repository's deploy directly; take its paths"})
				}
			}
		}
		if d.mechanism == "" {
			d.mechanism = "external"
			d.propose([]string{"skaffold", "run"}, rel,
				"skaffold describes the deploy and `skaffold run` performs it")
		}
		return
	}
}

var makeTarget = regexp.MustCompile(`^(deploy|release|publish):`)

// taskRunners find the entry point the author maintains and tests, which is the
// best exec candidate short of a CI step.
func (d *detector) taskRunners() {
	if lines, err := readLines(filepath.Join(d.dir, "Makefile")); err == nil {
		for i, line := range lines {
			if m := makeTarget.FindStringSubmatch(line); m != nil {
				d.propose([]string{"make", m[1]}, fmt.Sprintf("Makefile:%d", i+1),
					fmt.Sprintf("a Makefile target the author maintains and tests (%s)", m[1]))
				return
			}
		}
	}
	for _, rel := range []string{"Taskfile.yml", "Taskfile.yaml"} {
		if _, err := os.Stat(filepath.Join(d.dir, rel)); err == nil {
			d.propose([]string{"task", "deploy"}, rel, "a Taskfile the author maintains")
			return
		}
	}
	if _, err := os.Stat(filepath.Join(d.dir, "justfile")); err == nil {
		d.propose([]string{"just", "deploy"}, "justfile", "a justfile the author maintains")
	}
}

// propose records the deploy command, unless something better already did.
func (d *detector) propose(argv []string, where, why string) {
	if _, already := d.decided("deploy.commands"); already {
		d.add(Finding{Field: "deploy.commands", Confidence: Evidence, Value: strings.Join(argv, " "),
			Where: where, Why: why + ", but something more authoritative was found first"})
		return
	}
	if d.mechanism == "" {
		d.mechanism = "external"
	}
	d.entryPoint = argv
	d.add(Finding{Field: "deploy.commands", Confidence: Decided, Value: strings.Join(argv, " "),
		Where: where, Why: why})
}

func (d *detector) decided(field string) (string, bool) {
	for _, f := range d.findings {
		if f.Field == field && f.Confidence == Decided {
			return f.Value, true
		}
	}
	return "", false
}

var exposeLine = regexp.MustCompile(`(?i)^\s*EXPOSE\s+(.+)$`)

// otherEvidence reports what is there without concluding from it. Which of
// several charts, compose services or exposed ports is *the* one is a judgement
// SPEC010 refuses.
func (d *detector) otherEvidence() {
	simple := []struct{ rel, why string }{
		{"Tiltfile", "a Tilt configuration"},
		{"kustomization.yaml", "a kustomization"},
		{filepath.Join("chart", "Chart.yaml"), "a Helm chart"},
		{"Chart.yaml", "a Helm chart at the root"},
		{"docker-compose.yml", "a compose file"},
		{"docker-compose.yaml", "a compose file"},
		{"compose.yaml", "a compose file"},
	}
	for _, s := range simple {
		if _, err := os.Stat(filepath.Join(d.dir, s.rel)); err == nil {
			d.add(Finding{Field: "deploy", Confidence: Evidence, Value: filepath.ToSlash(s.rel),
				Where: filepath.ToSlash(s.rel), Why: s.why})
		}
	}
	for _, dir := range []string{"k8s", "manifests"} {
		if info, err := os.Stat(filepath.Join(d.dir, dir)); err == nil && info.IsDir() {
			d.add(Finding{Field: "deploy", Confidence: Evidence, Value: dir + "/",
				Where: dir + "/", Why: "raw Kubernetes manifests"})
		}
	}
	if tf := d.glob("*.tf"); len(tf) > 0 {
		d.add(Finding{Field: "deploy", Confidence: Evidence, Value: strings.Join(tf, " "),
			Where: tf[0], Why: "Terraform configuration at the root"})
	}
	if lines, err := readLines(filepath.Join(d.dir, "Dockerfile")); err == nil {
		var ports []string
		var first string
		for i, line := range lines {
			if m := exposeLine.FindStringSubmatch(line); m != nil {
				ports = append(ports, strings.Fields(m[1])...)
				if first == "" {
					first = fmt.Sprintf("Dockerfile:%d", i+1)
				}
			}
		}
		if len(ports) > 0 {
			d.add(Finding{Field: "deploy", Confidence: Evidence, Value: strings.Join(ports, " "),
				Where: first, Why: "the root Dockerfile exposes these ports"})
		}
	}
}

var paasFiles = map[string]string{
	"Procfile":    "a Procfile",
	"fly.toml":    "a Fly.io configuration",
	"render.yaml": "a Render configuration",
	"vercel.json": "a Vercel configuration",
	"app.yaml":    "an App Engine configuration",
}

// paas reports and stops. There is no local equivalent of a PaaS deploy: a chart
// or a command has to be authored, and saying so is the honest answer.
func (d *detector) paas() {
	names := make([]string, 0, len(paasFiles))
	for rel := range paasFiles {
		if _, err := os.Stat(filepath.Join(d.dir, rel)); err == nil {
			names = append(names, rel)
		}
	}
	sort.Strings(names)
	for _, rel := range names {
		d.add(Finding{Field: "deploy.commands", Confidence: Undecided, Where: rel,
			Why: paasFiles[rel] + " describes a platform deploy, and there is no local equivalent of one. " +
				"A chart or a command has to be authored"})
	}
}

// refusals state what detection will not infer, so that its absence from the
// manifest is a statement rather than an omission (SPEC010).
func (d *detector) refusals() {
	d.add(Finding{Field: "deploy.dependencies", Confidence: Refused,
		Why: "a dependency role cannot be derived from a repository's files: the vocabulary lives in the " +
			"environment's catalogue. A wrong role marked required fails the whole ChangeSet, naming only the role"})
	d.add(Finding{Field: "deploy.resources", Confidence: Refused,
		Why: "a resource declaration needs a namespace, a definition name and a version from a catalogue " +
			"this repository has never referenced, and a malformed one is silently skipped rather than reported"})
	d.add(Finding{Field: "discovery", Confidence: Refused,
		Why: "discovery is a description of intent; a generated one reads as considered when it is not"})

	if _, err := os.Stat(filepath.Join(d.dir, "docker-compose.yml")); err == nil {
		d.add(Finding{Field: "deploy.default_configuration", Confidence: Refused, Where: "docker-compose.yml",
			Why: "which keys of a compose environment: block the platform should own, and which are the " +
				"app's own constants, cannot be told apart from the file"})
	}
}

// Apply writes what was decided, through the manifest store's own edit verbs.
//
// Exactly four members: name, description, the build mechanism, and the deploy
// command with its image. Nothing else, ever -- see the package comment.
type Writer interface {
	Init(name, description string) error
	SetMechanism(section, mechanism string) error
	SetExec(argv []string) error
	SetToolCommands(tools []string) error
	SetImage(ref string) error
}

// Apply reports the fields it wrote, in order.
//
// description overrides what was detected, and is required when nothing was: the
// BACON schema requires a non-empty one, so writing a manifest without it would
// produce a file that cannot validate -- which is a worse outcome than refusing
// and naming the flag.
func Apply(r Result, w Writer, description string) ([]string, error) {
	if r.AlreadyAClass {
		return nil, fmt.Errorf("this is already a repo class; read it with `nsctl repoclass describe` " +
			"and change it with the `nsctl repoclass` verbs")
	}
	name, ok := r.Decided("name")
	if !ok {
		return nil, fmt.Errorf("no name could be decided, so there is nothing to write; " +
			"`nsctl repoclass init <name>` takes one")
	}
	if description == "" {
		description, _ = r.Decided("description")
	}
	if strings.TrimSpace(description) == "" {
		return nil, fmt.Errorf("nothing in the repository states what it is in one line, and BACON " +
			"requires a description. Supply one with --description \"...\"")
	}
	if err := w.Init(name, description); err != nil {
		return nil, err
	}
	written := []string{"name", "description", "build"}

	switch r.Mechanism {
	case "external":
		if err := w.SetMechanism("build", "external"); err != nil {
			return nil, err
		}
		written = append(written, "build.mechanism")
		if argv := r.Argv(); len(argv) > 0 {
			if err := w.SetExec(argv); err != nil {
				return nil, err
			}
			written = append(written, "deploy.commands")
		}
	case "tool_set":
		if tools, ok := r.Decided("deploy.commands"); ok {
			if err := w.SetToolCommands(strings.Fields(tools)); err != nil {
				return nil, err
			}
			written = append(written, "deploy.commands")
		}
	}
	if image, ok := r.Decided("deploy.image"); ok {
		if err := w.SetImage(image); err != nil {
			return nil, err
		}
		written = append(written, "deploy.image")
	}
	return written, nil
}

// --- small readers -------------------------------------------------------

func (d *detector) glob(pattern string) []string {
	matches, err := filepath.Glob(filepath.Join(d.dir, pattern))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		rel, err := filepath.Rel(d.dir, m)
		if err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	return out
}

func (d *detector) jsonField(rel, key string) (string, string) {
	data, err := os.ReadFile(filepath.Join(d.dir, rel))
	if err != nil {
		return "", ""
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		return "", ""
	}
	if s, ok := doc[key].(string); ok {
		return s, rel
	}
	return "", ""
}

var tomlDescription = regexp.MustCompile(`^\s*description\s*=\s*["'](.*)["']\s*$`)

func (d *detector) tomlDescription() (string, string) {
	lines, err := readLines(filepath.Join(d.dir, "pyproject.toml"))
	if err != nil {
		return "", ""
	}
	for i, line := range lines {
		if m := tomlDescription.FindStringSubmatch(line); m != nil {
			return m[1], fmt.Sprintf("pyproject.toml:%d", i+1)
		}
	}
	return "", ""
}

var yamlNameLine = regexp.MustCompile(`^name:\s*(\S+)\s*$`)

func (d *detector) yamlName(rel string) (string, string) {
	lines, err := readLines(filepath.Join(d.dir, rel))
	if err != nil {
		return "", ""
	}
	for i, line := range lines {
		if m := yamlNameLine.FindStringSubmatch(line); m != nil {
			return m[1], fmt.Sprintf("%s:%d", filepath.ToSlash(rel), i+1)
		}
	}
	return "", ""
}

// readmeSentence takes the first sentence of the first prose paragraph, skipping
// headings, badges and blank lines.
func (d *detector) readmeSentence(rel string) (string, string) {
	lines, err := readLines(filepath.Join(d.dir, rel))
	if err != nil {
		return "", ""
	}
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "=") ||
			strings.HasPrefix(t, "-") || strings.HasPrefix(t, "[!") || strings.HasPrefix(t, "![") {
			continue
		}
		return firstSentence(t), fmt.Sprintf("%s:%d", filepath.ToSlash(rel), i+1)
	}
	return "", ""
}

func firstSentence(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, ". "); i > 0 {
		return text[:i+1]
	}
	return strings.TrimSuffix(text, "\n")
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// slugify makes a repo class name out of whatever the repository calls itself.
func slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	// An npm scope is not part of the name.
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = slugUnsafe.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}
