package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// noHome is every repoclass test's environment: SPEC007 says no verb under
// the group needs HMD_HOME, and an empty lookup is how that is proven.
var noHome = fakeEnv(nil)

func rc(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	full := append([]string{"repoclass", "--path", dir}, args...)
	return run(t, noHome, full...)
}

func mustRepoclass(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, stderr, err := rc(t, dir, args...)
	if err != nil {
		t.Fatalf("nsctl repoclass %s: %v\nstderr: %s", strings.Join(args, " "), err, stderr)
	}
	return out
}

func readDoc(t *testing.T, dir string) *bacon.Object {
	t.Helper()
	s, err := bacon.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s.Doc
}

func TestRepoClassInitWritesAndRefusesTwice(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := mustRepoclass(t, dir, "init", "acme-api", "--description", "The Acme API")
	if !strings.Contains(out, "wrote "+filepath.Join("meta-data", "manifest.json")) {
		t.Errorf("init did not report its write: %q", out)
	}
	doc := readDoc(t, dir)
	if name, _ := doc.String("name"); name != "acme-api" {
		t.Errorf("name = %q", name)
	}
	version, _ := os.ReadFile(filepath.Join(dir, "meta-data", "VERSION"))
	if strings.TrimSpace(string(version)) != "0.1" {
		t.Errorf("VERSION = %q", version)
	}

	_, _, err := rc(t, dir, "init", "again")
	if err == nil || nserr.CodeOf(err) != nserr.Usage {
		t.Fatalf("second init = %v, want usage (2)", err)
	}
	if !strings.Contains(err.Error(), "nsctl repoclass describe") {
		t.Errorf("refusal does not name describe: %v", err)
	}
}

func TestRepoClassInitRefusesUnimplementedFormats(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"--format", "toml"}, {"--at", "root"}} {
		_, _, err := rc(t, t.TempDir(), append([]string{"init", "x"}, args...)...)
		if err == nil || nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("init %v = %v, want a usage refusal naming what is not implemented", args, err)
		}
	}
}

// A verb on a directory with no manifest is a usage error naming init.
func TestRepoClassVerbsNameInitWhenThereIsNoManifest(t *testing.T) {
	t.Parallel()

	_, _, err := rc(t, t.TempDir(), "describe")
	if err == nil || nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "nsctl repoclass init") {
		t.Errorf("describe on nothing = %v", err)
	}
}

// The seven-command authoring of an acme product, through the CLI, with every
// verb run twice: one entry each, one line per write, and the file the demo
// commits.
func TestRepoClassAuthorsAProductAndIsIdempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustRepoclass(t, dir, "init", "acme-dp-customer-revenue", "--description", "Net revenue per customer per month: invoices plus add-on orders net of refunds.")
	steps := [][]string{
		{"build", "set-mechanism", "external"},
		{"deploy", "set-command", "exec", "acme-deploy"},
		{"deploy", "set-image", "acme/ci-tools:1"},
		{"deploy", "add-dependency", "warehouse", "--resource-namespace", "acme.com", "--resource-definition-name", "sql-warehouse", "--resource-version", "0.1.0"},
		{"deploy", "add-dependency", "runner", "--resource-namespace", "scheduler.acme.com", "--resource-definition-name", "job-runner", "--resource-version", "0.1.0"},
		{"deploy", "add-dependency", "core-models", "--resource-namespace", "acme.com", "--resource-definition-name", "dbt-models", "--resource-version", "0.1.0"},
		{"deploy", "set-config", "kind", "product"},
		{"deploy", "set-config", "schema", "analytics_customer_revenue"},
		{"deploy", "set-config", "schedule", "0 6 * * *"},
		{"test", "set-command", "exec", "make", "verify"},
		{"discovery", "set-summary", "Net revenue per customer per month: invoices plus add-on orders net of refunds. Table customer_revenue_monthly in schema analytics_customer_revenue, refreshed on schedule 0 6 * * *."},
		{"discovery", "add-entry-point", "products/customer-revenue/models/customer_revenue_monthly.sql", "--description", "The model"},
		{"discovery", "add-capability", "customer_revenue_monthly", "--kind", "operation", "--description", "Net revenue per customer per month: invoices plus add-on orders net of refunds.", "--location", "products/customer-revenue/models/customer_revenue_monthly.sql"},
		{"discovery", "add-related-doc", "Acme data platform README", "README.md"},
	}
	for round := 0; round < 2; round++ {
		for _, step := range steps {
			out := mustRepoclass(t, dir, step...)
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) != 1 || !strings.HasPrefix(lines[0], "wrote ") {
				t.Errorf("%v printed %q, want one `wrote` line", step, out)
			}
		}
	}
	want, err := os.ReadFile(filepath.Join("..", "internal", "bacon", "testdata", "acme-customer-revenue.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "meta-data", "manifest.json"))
	if string(got) != strings.TrimRight(string(want), "\n") {
		t.Errorf("the verbs produced:\n%s\nwant the hand-written acme manifest:\n%s", got, want)
	}

	// And it validates clean, with the one inert note.
	out := mustRepoclass(t, dir, "validate")
	if !strings.Contains(out, "validate: ok -- 0 errors, 0 warnings, 1 note") {
		t.Errorf("validate: %q", out)
	}
}

func TestRepoClassSetConfigTypesValues(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustRepoclass(t, dir, "init", "x", "--description", "d")
	mustRepoclass(t, dir, "deploy", "set-config", "replicas", "2")
	mustRepoclass(t, dir, "deploy", "set-config", "nested.flag", "true")
	mustRepoclass(t, dir, "deploy", "set-config", "profile", "minimal")
	doc := readDoc(t, dir)
	if v, _ := doc.Lookup("deploy", "default_configuration", "replicas"); v.(interface{ String() string }).String() != "2" {
		t.Errorf("replicas = %v (%T)", v, v)
	}
	if v, _ := doc.Lookup("deploy", "default_configuration", "nested", "flag"); v != true {
		t.Errorf("nested.flag = %v (%T)", v, v)
	}
	if v, _ := doc.Lookup("deploy", "default_configuration", "profile"); v != "minimal" {
		t.Errorf("profile = %v", v)
	}
	mustRepoclass(t, dir, "deploy", "unset-config", "nested.flag")
	if _, ok := readDoc(t, dir).Lookup("deploy", "default_configuration", "nested", "flag"); ok {
		t.Error("unset-config left the key")
	}
	_, stderr, err := rc(t, dir, "deploy", "unset-config", "nested.flag")
	if err != nil || !strings.Contains(stderr, "nothing to remove") {
		t.Errorf("a second unset should note, not fail: %v %q", err, stderr)
	}
}

func TestRepoClassRemoveVerbs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustRepoclass(t, dir, "init", "x", "--description", "d")
	mustRepoclass(t, dir, "deploy", "add-dependency", "db", "--repo-class-name", "hmd-postgres-rds", "--optional")
	if req, _ := readDoc(t, dir).Lookup("deploy", "dependencies", "db", "required"); req != "false" {
		t.Errorf("--optional wrote required=%v", req)
	}
	mustRepoclass(t, dir, "deploy", "remove-dependency", "db")
	if _, ok := readDoc(t, dir).Lookup("deploy", "dependencies", "db"); ok {
		t.Error("dependency not removed")
	}
	mustRepoclass(t, dir, "deploy", "add-resource", "ds", "--resource-namespace", "data.acme.com", "--resource-definition-name", "dataset", "--version", "0.1.0", "--produces", "--role", "dataset")
	if v, _ := readDoc(t, dir).Lookup("deploy", "resources", "ds", "produces"); v != true {
		t.Errorf("produces = %v", v)
	}
	mustRepoclass(t, dir, "deploy", "remove-resource", "ds")
	mustRepoclass(t, dir, "build", "add-command", "python")
	mustRepoclass(t, dir, "build", "add-command", "docker", "--no-cache")
	mustRepoclass(t, dir, "build", "remove-command", "python")
	cmds, _ := readDoc(t, dir).Lookup("build", "commands")
	if list := cmds.([]any); len(list) != 1 || list[0].([]any)[0] != "docker" || len(list[0].([]any)) != 2 || list[0].([]any)[1] != "--no-cache" {
		t.Errorf("build.commands = %v, want [[docker --no-cache]] with the tool's flag kept", cmds)
	}
	_, _, err := rc(t, dir, "deploy", "add-dependency", "x", "--required", "--optional")
	if err == nil || nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("--required --optional = %v", err)
	}
}

// set-command exec replaces tool set commands and says so on stderr.
func TestRepoClassSetCommandReplacesAndSays(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustRepoclass(t, dir, "init", "x", "--description", "d")
	mustRepoclass(t, dir, "deploy", "add-command", "helm")
	_, stderr, err := rc(t, dir, "deploy", "set-command", "exec", "make", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "replaced helm") {
		t.Errorf("stderr = %q", stderr)
	}
	_, _, err = rc(t, dir, "deploy", "set-command", "helm", "x")
	if err == nil || nserr.CodeOf(err) != nserr.Usage || !strings.Contains(err.Error(), "add-command") {
		t.Errorf("set-command helm = %v", err)
	}
}

// A TOML tier reads for describe and validate and refuses every write with
// the file named.
func TestRepoClassTOMLIsReadOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "meta-data", "manifest.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("name = \"t\"\ndescription = \"d\"\n[build]\nmechanism = \"external\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustRepoclass(t, dir, "describe")
	if !strings.Contains(out, "manifest.toml") || !strings.Contains(out, "name") {
		t.Errorf("describe: %q", out)
	}
	if _, _, err := rc(t, dir, "validate"); err != nil {
		t.Errorf("validate on toml: %v", err)
	}
	_, _, err := rc(t, dir, "deploy", "set-image", "x")
	if err == nil || nserr.CodeOf(err) != nserr.Fail || !strings.Contains(err.Error(), path) {
		t.Errorf("a TOML write = %v, want a failure naming %s", err, path)
	}
}

func TestRepoClassDescribeJSONAndTable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustRepoclass(t, dir, "init", "x", "--description", "the description")
	mustRepoclass(t, dir, "deploy", "add-dependency", "warehouse", "--resource-namespace", "acme.com", "--resource-definition-name", "sql-warehouse", "--resource-version", "0.1.0")
	mustRepoclass(t, dir, "discovery", "add-capability", "cap", "--kind", "operation", "--description", "does a thing")

	asJSON := mustRepoclass(t, dir, "describe", "--json")
	for _, want := range []string{`"name": "x"`, `"required": true`, `"resource_definition_name": "sql-warehouse"`, `"capabilities"`} {
		if !strings.Contains(asJSON, want) {
			t.Errorf("--json lacks %s:\n%s", want, asJSON)
		}
	}
	table := mustRepoclass(t, dir, "describe")
	for _, want := range []string{"Dependencies:", "warehouse", "acme.com/sql-warehouse 0.1.0", "Capabilities:", "cap"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %s:\n%s", want, table)
		}
	}
}

func TestRepoClassValidateExitsOnErrorsAndStrictWarnings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustRepoclass(t, dir, "init", "x", "--description", "d")
	// A required dependency naming nothing is an error.
	mustRepoclass(t, dir, "deploy", "set-command", "exec", "true")
	if err := os.WriteFile(filepath.Join(dir, "meta-data", "manifest.json"), []byte(`{"name":"x","description":"d","build":{},"deploy":{"commands":[["exec","true"]],"dependencies":{"db":{"required":true,"repo_class_name":"r"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := rc(t, dir, "validate")
	if err != nil {
		t.Fatalf("a warning-only manifest failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning: deploy.dependencies.db.required") || !strings.Contains(out, "validate: ok") {
		t.Errorf("output: %s", out)
	}
	_, _, err = rc(t, dir, "validate", "--strict")
	if err == nil || nserr.CodeOf(err) != nserr.Fail {
		t.Errorf("--strict = %v, want failure", err)
	}
	asJSON, _, _ := rc(t, dir, "validate", "--json")
	if !strings.Contains(asJSON, `"warnings": 1`) {
		t.Errorf("--json: %s", asJSON)
	}

	if err := os.WriteFile(filepath.Join(dir, "meta-data", "manifest.json"), []byte(`{"name":"x","description":"d","build":{},"deploy":{"commands":[["exec"]]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err = rc(t, dir, "validate")
	if err == nil || nserr.CodeOf(err) != nserr.Fail || !strings.Contains(out, "validate: failed") {
		t.Errorf("an error-carrying manifest = %v\n%s", err, out)
	}
}

// --path is relative to the working directory, and a write names the file
// relative to the repo.
func TestRepoClassPathIsRelativeToCwd(t *testing.T) {
	// Not parallel: it changes the working directory.
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	if err := os.MkdirAll("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, noHome, "repoclass", "--path", "sub", "init", "x", "--description", "d")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "meta-data", "manifest.json")); err != nil {
		t.Errorf("init did not write under sub: %v", err)
	}
	if !strings.Contains(out, "wrote meta-data/manifest.json") {
		t.Errorf("out = %q", out)
	}
}

// set-command exec parses no flags of its own, so the argv's flags stay the
// argv's -- which means the root's --home reaches it raw too. It is a
// documented global flag on every nsctl invocation and must be picked off
// like --path, in either spelling, wherever it sits before the argv.
func TestRepoClassSetCommandToleratesTheGlobalHomeFlag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustRepoclass(t, dir, "init", "x", "--description", "d")
	for _, args := range [][]string{
		{"--home", t.TempDir(), "repoclass", "--path", dir, "deploy", "set-command", "exec", "echo", "deployed", "x"},
		{"repoclass", "--home=" + t.TempDir(), "--path", dir, "deploy", "set-command", "exec", "echo", "deployed", "x"},
	} {
		if _, stderr, err := run(t, noHome, args...); err != nil {
			t.Fatalf("nsctl %s: %v\nstderr: %s", strings.Join(args, " "), err, stderr)
		}
	}
	cmds, _ := readDoc(t, dir).Lookup("deploy", "commands")
	if got := fmt.Sprint(cmds); !strings.Contains(got, "exec echo deployed x") {
		t.Errorf("deploy.commands = %v, want the exec argv", cmds)
	}
}
