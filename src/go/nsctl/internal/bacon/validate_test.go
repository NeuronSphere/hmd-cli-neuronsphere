package bacon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// classDir writes a repo class from its manifest text plus optional extra
// files (path -> content) and opens it.
func classDir(t *testing.T, manifest string, files map[string]string) *Store {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meta-data", "manifest.json"), manifest)
	for rel, content := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(rel)), content)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func messages(findings []Finding, sev Severity) string {
	var out []string
	for _, f := range findings {
		if f.Severity == sev {
			out = append(out, f.String())
		}
	}
	return strings.Join(out, "\n")
}

// The seven acme manifests are the motivating case: no errors, no warnings,
// the one inert note about exec.
func TestValidateAcceptsAForeignToolsetClass(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/describe-input.json")
	if err != nil {
		t.Fatal(err)
	}
	s := classDir(t, string(data), map[string]string{
		"meta-data/VERSION":                "0.1\n",
		"meta-data/resources/dataset.yaml": "resource_namespace: data.acme.com\nresource_definition_name: dataset\nversion: 0.1.0\n",
	})
	findings := Validate(s, Known{})
	e, w, n := Summary(findings)
	if e != 0 || w != 0 || n != 1 {
		t.Errorf("errors=%d warnings=%d notes=%d, want 0/0/1:\n%v", e, w, n, findings)
	}
	if !strings.Contains(messages(findings, Note), "exec") {
		t.Errorf("the note should be about exec: %v", findings)
	}
}

// One fixture per SPEC011 rule.
func TestValidateRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		manifest string
		files    map[string]string
		known    Known
		severity Severity
		want     string
	}{
		{"missing name and build", `{"description": "d"}`, nil, Known{}, Error, "name is required"},
		{"missing build", `{"name": "n", "description": "d"}`, nil, Known{}, Error, "build is required"},
		{"bad mechanism", `{"name":"n","description":"d","build":{"mechanism":"magic"}}`, nil, Known{}, Error, "build.mechanism"},
		{"deploy with nothing", `{"name":"n","description":"d","build":{},"deploy":{}}`, nil, Known{}, Error, "declares no commands"},
		{"empty exec", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec"]]}}`, nil, Known{}, Error, "exec has no command"},
		{"two execs", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec","a"],["exec","b"]]}}`, nil, Known{}, Error, "more than one exec"},
		{"exec beside helm", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec","a"],["helm"]]}}`, map[string]string{"src/helm/Chart.yaml": ""}, Known{}, Error, "exec beside another tool"},
		{"helm without chart", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["helm"]]}}`, nil, Known{}, Error, "src/helm/Chart.yaml"},
		{"docker without dockerfile", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["docker"]]}}`, nil, Known{}, Error, "src/docker/Dockerfile"},
		{"required with nothing to resolve", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec","a"]],"dependencies":{"db":{"required":"true"}}}}`, nil, Known{}, Error, "deploy.dependencies.db"},
		{"required as boolean", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec","a"]],"dependencies":{"db":{"required":true,"repo_class_name":"x"}}}}`, nil, Known{}, Warning, "JSON boolean"},
		{"required as garbage", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec","a"]],"dependencies":{"db":{"required":"yes","repo_class_name":"x"}}}}`, nil, Known{}, Error, "db.required"},
		{"resource missing identity", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec","a"]],"dependencies":{"db":{"required":"true","resource":{"resource_namespace":"x"}}}}}`, nil, Known{}, Error, "resource_definition_name"},
		{"deploy.resources missing version", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec","a"]],"resources":{"r":{"resource_namespace":"x","resource_definition_name":"y"}}}}`, nil, Known{}, Error, "deploy.resources.r.version"},
		{"capability kind", `{"name":"n","description":"d","build":{},"discovery":{"capabilities":[{"name":"c","kind":"widget","description":"d"}]}}`, nil, Known{}, Error, "capabilities[0].kind"},
		{"entry point without description", `{"name":"n","description":"d","build":{},"discovery":{"entry_points":[{"path":"p"}]}}`, nil, Known{}, Error, "entry_points[0].description"},
		{"bad version", `{"name":"n","description":"d","build":{}}`, map[string]string{"meta-data/VERSION": "0.1.0\n"}, Known{}, Warning, "not MAJOR.MINOR"},
		{"resource yaml without identity", `{"name":"n","description":"d","build":{}}`, map[string]string{"meta-data/VERSION": "0.1\n", "meta-data/resources/x.yaml": "version: 0.1.0\n"}, Known{}, Warning, "resource_namespace"},
		{"bundled collision", `{"name":"hmd-vpc","description":"d","build":{}}`, map[string]string{"meta-data/VERSION": "0.1\n"}, Known{BundledClasses: []string{"hmd-vpc"}}, Warning, "bundled"},
		{"reserved collision", `{"name":"environment-db","description":"d","build":{}}`, map[string]string{"meta-data/VERSION": "0.1\n"}, Known{ReservedNames: []string{"environment-db"}}, Warning, "reserved"},
		{"inert mechanism", `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec","a"]],"mechanism":"external"}}`, nil, Known{}, Note, "deploy.mechanism"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := classDir(t, c.manifest, c.files)
			got := messages(Validate(s, c.known), c.severity)
			if !strings.Contains(got, c.want) {
				t.Errorf("%s findings lack %q:\n%s\nall: %v", c.severity, c.want, got, Validate(s, c.known))
			}
		})
	}
}

// Absent VERSION is a warning, and errors sort first.
func TestValidateOrdersErrorsFirst(t *testing.T) {
	t.Parallel()

	s := classDir(t, `{"name":"n","description":"d","build":{},"deploy":{"commands":[["exec","a"]],"dependencies":{"db":{"required":true}}}}`, nil)
	findings := Validate(s, Known{})
	if len(findings) < 3 {
		t.Fatalf("findings = %v", findings)
	}
	if findings[0].Severity != Error {
		t.Errorf("first finding is %s, want error: %v", findings[0].Severity, findings)
	}
	if !strings.Contains(messages(findings, Warning), "VERSION") {
		t.Errorf("no VERSION warning: %v", findings)
	}
}

// A deploy_local.sh is a resolvable command; no error for a missing
// deploy.commands then.
func TestValidateAcceptsALocalDeployScript(t *testing.T) {
	t.Parallel()

	s := classDir(t, `{"name":"n","description":"d","build":{},"deploy":{}}`, map[string]string{"src/local/deploy_local.sh": "#!/bin/sh\n", "meta-data/VERSION": "0.1\n"})
	if e, _, _ := Summary(Validate(s, Known{})); e != 0 {
		t.Errorf("errors: %v", Validate(s, Known{}))
	}
}
