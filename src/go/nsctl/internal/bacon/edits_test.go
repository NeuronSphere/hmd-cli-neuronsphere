package bacon

import (
	"os"
	"strings"
	"testing"
)

func base() *Object {
	doc := NewObject()
	doc.Set("name", "n")
	doc.Set("description", "d")
	doc.Set("build", NewObject())
	return doc
}

func encoded(t *testing.T, doc *Object) string {
	t.Helper()
	data, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// SetLicence writes the declaration whole -- the object form, or the string
// shorthand when there is nothing to exclude -- and ClearLicence removes it;
// every other key keeps its place. NERD017 SPEC011.
func TestSetAndClearLicence(t *testing.T) {
	t.Parallel()
	doc := base()
	doc.Set("deploy", NewObject())

	key, err := SetLicence(doc, "Apache-2.0", []string{"src/python/", "src/docker"})
	if err != nil || key != "license" {
		t.Fatalf("SetLicence = %q, %v", key, err)
	}
	if !strings.Contains(encoded(t, doc), `"license": {
    "spdx": "Apache-2.0",
    "exclude": [
      "src/python",
      "src/docker"
    ]
  }`) {
		t.Errorf("object form not written:\n%s", encoded(t, doc))
	}
	if strings.Index(encoded(t, doc), `"deploy"`) > strings.Index(encoded(t, doc), `"license"`) {
		t.Error("license was not appended after the existing keys")
	}

	// Set again replaces, and the shorthand is used when nothing is excluded.
	if _, err := SetLicence(doc, "MIT", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded(t, doc), `"license": "MIT"`) || strings.Contains(encoded(t, doc), "exclude") {
		t.Errorf("shorthand not written:\n%s", encoded(t, doc))
	}

	for _, bad := range [][]string{{"", ""}, {"MIT", "/abs"}, {"MIT", "../x"}, {"MIT", "."}} {
		var ex []string
		if bad[1] != "" {
			ex = []string{bad[1]}
		}
		if _, err := SetLicence(doc, bad[0], ex); err == nil {
			t.Errorf("SetLicence(%q, %v) accepted", bad[0], ex)
		}
	}

	if key, ok := ClearLicence(doc); !ok || key != "license" {
		t.Errorf("ClearLicence = %q, %v", key, ok)
	}
	if strings.Contains(encoded(t, doc), "license") {
		t.Error("license survived ClearLicence")
	}
	if _, ok := ClearLicence(doc); ok {
		t.Error("clearing twice reported a change")
	}
}

// Every add-* is idempotent by key: run twice, one entry, same bytes.
func TestAddVerbsAreIdempotent(t *testing.T) {
	t.Parallel()

	doc := base()
	steps := []func() error{
		func() error { _, err := SetMechanism(doc, "build", "external"); return err },
		func() error { _, _, err := SetExecCommand(doc, "deploy", []string{"acme-deploy"}); return err },
		func() error { _, err := SetImage(doc, "acme/ci-tools:1"); return err },
		func() error {
			_, err := AddDependency(doc, "warehouse", Dependency{Required: true,
				ResourceNamespace: "acme.com", ResourceDefinitionName: "sql-warehouse", ResourceVersion: "0.1.0",
				Tags: map[string]string{"platform": "a"}})
			return err
		},
		func() error {
			_, err := AddResource(doc, "ds", Resource{Namespace: "data.acme.com", Name: "dataset", Version: "0.1.0"})
			return err
		},
		func() error { _, err := SetConfig(doc, "schema", "analytics"); return err },
		func() error { _, err := SetConfig(doc, "nested.replicas", 2); return err },
		func() error { _, _, err := SetExecCommand(doc, "test", []string{"make", "verify"}); return err },
		func() error { _, err := AddCommand(doc, "build", "python", []string{"--flag"}); return err },
		func() error { _, err := SetSummary(doc, "summary"); return err },
		func() error { _, err := AddEntryPoint(doc, "models/x.sql", "the model"); return err },
		func() error { _, err := AddCapability(doc, "cap", "operation", "does", "loc"); return err },
		func() error { _, err := AddRelatedDoc(doc, "README", "README.md"); return err },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	once := encoded(t, doc)
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d again: %v", i, err)
		}
	}
	if twice := encoded(t, doc); twice != once {
		t.Errorf("a second run changed the document:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}

	// And the shape is the Python's.
	if req, _ := doc.Lookup("deploy", "dependencies", "warehouse", "required"); req != "true" {
		t.Errorf("required = %v (%T), want the string \"true\"", req, req)
	}
	if v, _ := doc.Lookup("deploy", "dependencies", "warehouse", "resource", "tag_selector", "platform"); v != "a" {
		t.Errorf("tag_selector.platform = %v", v)
	}
	if v, _ := doc.Lookup("deploy", "default_configuration", "nested", "replicas"); v != 2 {
		t.Errorf("nested config = %v", v)
	}
	cmds, _ := doc.Lookup("deploy", "commands")
	if len(cmds.([]any)) != 1 {
		t.Errorf("deploy.commands = %v, want one exec", cmds)
	}
	caps, _ := doc.Lookup("discovery", "capabilities")
	if len(caps.([]any)) != 1 {
		t.Errorf("capabilities = %v", caps)
	}

	// Replacing keeps the key's position.
	if _, err := AddDependency(doc, "warehouse", Dependency{RepoClassName: "other", Required: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddDependency(doc, "runner", Dependency{Required: true, RepoClassName: "r"}); err != nil {
		t.Fatal(err)
	}
	deps, _ := doc.Lookup("deploy", "dependencies")
	if got := deps.(*Object).Keys(); !equalStrings(got, []string{"warehouse", "runner"}) {
		t.Errorf("dependency order = %v", got)
	}
	if req, _ := doc.Lookup("deploy", "dependencies", "warehouse", "required"); req != "false" {
		t.Errorf("optional required = %v", req)
	}
}

func TestRemoveVerbs(t *testing.T) {
	t.Parallel()

	doc := base()
	_, _ = AddDependency(doc, "db", Dependency{Required: true, RepoClassName: "x"})
	_, _ = AddResource(doc, "r", Resource{Namespace: "a", Name: "b", Version: "1"})
	_, _ = SetConfig(doc, "a.b", "v")
	_, _ = AddCommand(doc, "build", "python", nil)
	_, _ = AddCommand(doc, "build", "docker", nil)

	if _, ok := RemoveDependency(doc, "db"); !ok {
		t.Error("db not removed")
	}
	if _, ok := RemoveDependency(doc, "db"); ok {
		t.Error("db removed twice")
	}
	if _, ok := RemoveResource(doc, "r"); !ok {
		t.Error("r not removed")
	}
	if _, ok := UnsetConfig(doc, "a.b"); !ok {
		t.Error("a.b not removed")
	}
	if _, ok := doc.Lookup("deploy", "default_configuration", "a"); !ok {
		t.Error("unset removed the parent section")
	}
	if _, removed, _ := RemoveCommand(doc, "build", "python"); !removed {
		t.Error("python not removed")
	}
	cmds, _ := doc.Lookup("build", "commands")
	if len(cmds.([]any)) != 1 {
		t.Errorf("build.commands = %v", cmds)
	}
}

// set-command exec replaces whatever was there and reports it.
func TestSetExecCommandReportsWhatItReplaced(t *testing.T) {
	t.Parallel()

	doc := base()
	_, _ = AddCommand(doc, "deploy", "helm", nil)
	_, replaced, err := SetExecCommand(doc, "deploy", []string{"sh", "deploy.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 1 {
		t.Errorf("replaced = %v", replaced)
	}
	if _, _, err := SetExecCommand(doc, "deploy", nil); err == nil {
		t.Error("an empty exec was accepted")
	}
}

// A write refuses to clobber a user's scalar with a section.
func TestEditsRefuseToClobber(t *testing.T) {
	t.Parallel()

	doc := base()
	doc.Set("deploy", "not an object")
	if _, err := SetImage(doc, "x"); err == nil {
		t.Error("SetImage wrote under a string")
	}
	if _, err := AddCapability(doc, "c", "widget", "d", ""); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Errorf("a bad kind was accepted: %v", err)
	}
}

// Authoring the acme product manifest through the verbs reproduces the
// hand-written file exactly -- the round trip the demo depends on.
func TestVerbsReproduceTheAcmeManifest(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile("testdata/acme-customer-revenue.json")
	if err != nil {
		t.Fatal(err)
	}
	doc := NewObject()
	doc.Set("name", "acme-dp-customer-revenue")
	doc.Set("description", "Net revenue per customer per month: invoices plus add-on orders net of refunds.")
	doc.Set("build", NewObject())
	must := func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(SetMechanism(doc, "build", "external"))
	_, _, err = SetExecCommand(doc, "deploy", []string{"acme-deploy"})
	must("", err)
	must(SetImage(doc, "acme/ci-tools:1"))
	must(AddDependency(doc, "warehouse", Dependency{Required: true, ResourceNamespace: "acme.com", ResourceDefinitionName: "sql-warehouse", ResourceVersion: "0.1.0"}))
	must(AddDependency(doc, "runner", Dependency{Required: true, ResourceNamespace: "scheduler.acme.com", ResourceDefinitionName: "job-runner", ResourceVersion: "0.1.0"}))
	must(AddDependency(doc, "core-models", Dependency{Required: true, ResourceNamespace: "acme.com", ResourceDefinitionName: "dbt-models", ResourceVersion: "0.1.0"}))
	must(SetConfig(doc, "kind", "product"))
	must(SetConfig(doc, "schema", "analytics_customer_revenue"))
	must(SetConfig(doc, "schedule", "0 6 * * *"))
	_, _, err = SetExecCommand(doc, "test", []string{"make", "verify"})
	must("", err)
	must(SetSummary(doc, "Net revenue per customer per month: invoices plus add-on orders net of refunds. Table customer_revenue_monthly in schema analytics_customer_revenue, refreshed on schedule 0 6 * * *."))
	must(AddEntryPoint(doc, "products/customer-revenue/models/customer_revenue_monthly.sql", "The model"))
	must(AddCapability(doc, "customer_revenue_monthly", "operation", "Net revenue per customer per month: invoices plus add-on orders net of refunds.", "products/customer-revenue/models/customer_revenue_monthly.sql"))
	must(AddRelatedDoc(doc, "Acme data platform README", "README.md"))

	if got := encoded(t, doc); got != strings.TrimRight(string(want), "\n") {
		t.Errorf("verbs produced:\n%s\nwant:\n%s", got, want)
	}
}
