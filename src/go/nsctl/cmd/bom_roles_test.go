package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// required and optional are the two spellings BACON uses, as strings -- which
// is how every one of the 517 dependency blocks in a full workspace writes it.
func required(class string) map[string]any {
	return map[string]any{"repo_class_name": class, "required": "true"}
}

func optional(class string) map[string]any {
	return map[string]any{"repo_class_name": class, "required": "false"}
}

// requiredResource is a role that names a resource type rather than trusting
// the class name. It cannot be stubbed: hmd-ms-deployment validates the
// producer against what it really produces.
func requiredResource(class, namespace, definition string) map[string]any {
	return map[string]any{
		"repo_class_name": class, "required": "true",
		"resource": map[string]any{
			"resource_namespace": namespace, "resource_definition_name": definition,
		},
	}
}

// classArtifact is an artifact whose manifest declares dependency roles, which
// is what the closure reads to tell a required role from an optional one.
func classArtifact(t *testing.T, class, version string, deps map[string]any) []byte {
	t.Helper()
	body := map[string]any{"name": class}
	if deps != nil {
		body["deploy"] = map[string]any{"dependencies": deps}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string][]byte{
		"meta-data/manifest.json": encoded,
		"meta-data/VERSION":       []byte(version),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// roleBOM is NERD013's worked example, and it is shaped like the run that
// motivated the document: one workload whose roles are half required and half
// not, reaching things with a local answer and things without one.
//
//	cluster         eks-upg       required, resource-typed  -> substrate
//	librarian       artifact-lib  required, name-only       -> imported
//	default-compute node-group    required, resource-typed  -> imported
//	otel            otel          optional                  -> not followed
//	authorizer      private-ca    optional                  -> not followed
//	datadog-lambda  dd-lambda     required, name-only       -> not in the BOM
//
// otel's own required role reaches clickhouse, so that not following an
// optional role can be seen to prune a subtree rather than one instance.
func roleBOM() []msdeploy.BOMEntry {
	return []msdeploy.BOMEntry{
		{
			RepoInstanceName: "eks-upg", RepoClassName: "hmd-inf-eks-cluster",
			RepoClassVersion: "0.1.44", DeploymentID: "d", Status: "DEPLOYED",
		},
		{
			RepoInstanceName: "artifact-lib", RepoClassName: "hmd-ms-artifact-lib",
			RepoClassVersion: "0.4.12", DeploymentID: "d", Status: "DEPLOYED",
		},
		{
			// Wants the same absent thing ms-transform does, so that two roles
			// needing one stub can be seen to be reported twice.
			RepoInstanceName: "node-group", RepoClassName: "hmd-inf-eks-node-group",
			RepoClassVersion: "0.2.7", DeploymentID: "d", Status: "DEPLOYED",
			Dependencies: map[string]any{"datadog-lambda": "dd-lambda"},
		},
		{
			RepoInstanceName: "clickhouse", RepoClassName: "hmd-inf-clickhouse",
			RepoClassVersion: "0.9.1", DeploymentID: "d", Status: "DEPLOYED",
		},
		{
			RepoInstanceName: "otel", RepoClassName: "hmd-inf-otel-collector",
			RepoClassVersion: "0.3.3", DeploymentID: "d", Status: "DEPLOYED",
			Dependencies: map[string]any{"clickhouse": "clickhouse"},
		},
		{
			RepoInstanceName: "private-ca", RepoClassName: "hmd-inf-private-ca",
			RepoClassVersion: "0.1.88", DeploymentID: "d", Status: "DEPLOYED",
		},
		{
			RepoInstanceName: "ms-transform", RepoClassName: "hmd-ms-transform",
			RepoClassVersion: "0.5.201", DeploymentID: "d", Status: "DEPLOYED",
			Dependencies: map[string]any{
				"cluster":         "eks-upg",
				"librarian":       "artifact-lib",
				"default-compute": "node-group",
				"otel":            "otel",
				"authorizer":      "private-ca",
				"datadog-lambda":  "dd-lambda",
			},
		},
	}
}

// roleManifests is what each class in roleBOM declares about its roles.
var roleManifests = map[string]map[string]any{
	"hmd-ms-transform": {
		"cluster": requiredResource("hmd-inf-eks-cluster",
			"kubernetes.neuronsphere.io", "kubernetes-cluster"),
		"librarian": required("hmd-ms-artifact-lib"),
		"default-compute": requiredResource("hmd-inf-eks-node-group",
			"kubernetes.neuronsphere.io", "node-group"),
		"otel":           optional("hmd-inf-otel-collector"),
		"authorizer":     optional("hmd-inf-private-ca"),
		"datadog-lambda": required("hmd-inf-datadog-lambdas"),
	},
	"hmd-inf-otel-collector": {"clickhouse": required("hmd-inf-clickhouse")},
	"hmd-inf-eks-node-group": {"datadog-lambda": required("hmd-inf-datadog-lambdas")},
}

func newRoleWorld(t *testing.T) *bomWorld {
	t.Helper()
	home := t.TempDir()
	dep := newFakeDeployment(t)
	dep.boms["dev"] = roleBOM()

	lib := newFakeLibrarian(t)
	for _, e := range roleBOM() {
		lib.content[librarian.Spec{
			Name: e.RepoClassName, Version: e.RepoClassVersion, ItemType: "build",
		}.ContentPath()] = classArtifact(t, e.RepoClassName, e.RepoClassVersion, roleManifests[e.RepoClassName])
	}

	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "add", "scratch"); err != nil {
		t.Fatalf("env add: %v", err)
	}
	return &bomWorld{dep: dep, lib: lib, home: home}
}

func sortedDeclared(t *testing.T, home string) []string {
	t.Helper()
	got := instanceNames(loadEnv(t, home, "scratch"))
	sort.Strings(got)
	return got
}

// TestBOMImportFollowsOnlyRequiredRoles is NERD013 SPEC001. The optional roles
// are not followed, and not following one prunes what it reached in turn --
// clickhouse is in the BOM and arrives only through otel.
func TestBOMImportFollowsOnlyRequiredRoles(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "ms-transform")
	if err != nil {
		t.Fatalf("bom import: %v\n%s\n%s", err, out, errOut)
	}

	got := sortedDeclared(t, w.home)
	want := []string{"artifact-lib", "ms-transform", "node-group"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("declared %v, want %v -- only required roles should be followed", got, want)
	}
	for _, pruned := range []string{"otel", "private-ca", "clickhouse"} {
		if slices.Contains(got, pruned) {
			t.Errorf("%s was imported through an optional role", pruned)
		}
	}
	if !strings.Contains(errOut, "optional role") {
		t.Errorf("the roles that were not followed are not reported:\n%s", errOut)
	}
	for _, role := range []string{"ms-transform:otel", "ms-transform:authorizer"} {
		if !strings.Contains(errOut, role) {
			t.Errorf("%s is not named among the roles not followed:\n%s", role, errOut)
		}
	}
}

// TestBOMOptionalRoleToAPresentTargetIsKept: not following an optional role
// does not drop it when its target is here anyway, reached through someone
// else's required role -- and the report says so, rather than claiming the
// target was not imported. Observed live on hmdtr1's dev, where
// ms-transform:ext-secrets is optional and ext-secrets arrives via airflow.
func TestBOMOptionalRoleToAPresentTargetIsKept(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	// node-group gains an optional role to artifact-lib, which ms-transform
	// already requires.
	bom := roleBOM()
	for i := range bom {
		if bom[i].RepoInstanceName == "node-group" {
			bom[i].Dependencies["librarian"] = "artifact-lib"
		}
	}
	w.dep.boms["dev"] = bom
	deps := map[string]any{
		"datadog-lambda": required("hmd-inf-datadog-lambdas"),
		"librarian":      optional("hmd-ms-artifact-lib"),
	}
	w.lib.content[librarian.Spec{
		Name: "hmd-inf-eks-node-group", Version: "0.2.7", ItemType: "build",
	}.ContentPath()] = classArtifact(t, "hmd-inf-eks-node-group", "0.2.7", deps)

	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "ms-transform")
	if err != nil {
		t.Fatalf("bom import: %v\n%s\n%s", err, out, errOut)
	}
	ng, ok := loadEnv(t, w.home, "scratch").Repo("node-group")
	if !ok {
		t.Fatal("node-group was not declared")
	}
	if got := ng.Dependencies["librarian"]; got != "artifact-lib" {
		t.Errorf("node-group:librarian = %v, want artifact-lib -- the target is here, so the role is kept", got)
	}
	if !strings.Contains(errOut, "target is here anyway") || !strings.Contains(errOut, "node-group:librarian") {
		t.Errorf("the kept role is not reported as kept:\n%s", errOut)
	}
	// And it must not also be listed among the roles whose target was dropped.
	dropped := errOut[strings.Index(errOut, "not imported"):]
	dropped = dropped[:strings.Index(dropped, "target is here anyway")]
	if strings.Contains(dropped, "node-group:librarian") {
		t.Errorf("node-group:librarian is reported as not imported, but artifact-lib is:\n%s", errOut)
	}
}

// TestBOMImportFollowsEveryRoleWhenNoneCanBeRead is SPEC001's fallback, and it
// is the property that makes required-only closure safe to default to: a run
// that cannot read a declaration behaves exactly as it did before NERD013.
func TestBOMImportFollowsEveryRoleWhenNoneCanBeRead(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	// --dry-run without --resolve fetches nothing, so nothing can be read.
	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "ms-transform", "--dry-run")
	if err != nil {
		t.Fatalf("bom import --dry-run: %v\n%s\n%s", err, out, errOut)
	}
	for _, name := range []string{"otel", "private-ca", "clickhouse"} {
		if !strings.Contains(out, name) {
			t.Errorf("%s was pruned by a run that could read no declarations:\n%s", name, out)
		}
	}
	if !strings.Contains(errOut, "could not read the role declarations") {
		t.Errorf("the run did not say that it read nothing:\n%s", errOut)
	}
}

// TestBOMWithFollowsAnOptionalRole is SPEC002, including that what the role
// reaches is then closed over by the ordinary rule.
func TestBOMWithFollowsAnOptionalRole(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "ms-transform", "--with", "otel")
	if err != nil {
		t.Fatalf("bom import --with: %v\n%s\n%s", err, out, errOut)
	}

	got := sortedDeclared(t, w.home)
	for _, name := range []string{"otel", "clickhouse"} {
		if !slices.Contains(got, name) {
			t.Errorf("--with otel did not bring %s: %v", name, got)
		}
	}
	if slices.Contains(got, "private-ca") {
		t.Errorf("--with otel also followed an optional role nobody asked for: %v", got)
	}
}

// TestBOMWithRefusesWhenItMatchesNothing is SPEC002's typo guard, and it is
// seed()'s rule for the same reason: a selector that quietly contributes
// nothing has exactly the shape of a successful smaller import.
func TestBOMWithRefusesWhenItMatchesNothing(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	_, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "ms-transform", "--with", "otle")
	if err == nil {
		t.Fatal("a --with matching nothing was accepted")
	}
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("exit code = %v, want a usage error", nserr.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "otel") {
		t.Errorf("the refusal does not list the optional roles there are: %v\n%s", err, errOut)
	}
}

// TestBOMStubsARequiredNameOnlyRole is SPEC003. Nothing here fills
// datadog-lambda -- it is not even in the BOM -- and nothing but the role's
// presence is validated, so it binds to the core instance rather than leaving a
// ChangeSet that hmd-ms-deployment refuses.
func TestBOMStubsARequiredNameOnlyRole(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "ms-transform")
	if err != nil {
		t.Fatalf("bom import: %v\n%s\n%s", err, out, errOut)
	}

	transform, ok := loadEnv(t, w.home, "scratch").Repo("ms-transform")
	if !ok {
		t.Fatal("ms-transform was not declared")
	}
	if transform.Dependencies["datadog-lambda"] != bom.CoreInstanceName {
		t.Errorf("the datadog-lambda role = %v, want it bound to %s",
			transform.Dependencies["datadog-lambda"], bom.CoreInstanceName)
	}
	// Never silently: the core instance deploys no datadog lambda, and a reader
	// has to be able to see that from the run.
	if !strings.Contains(errOut, "datadog-lambda") || !strings.Contains(errOut, "NOT deployed") {
		t.Errorf("the stub was not reported as a stub:\n%s", errOut)
	}
}

// TestBOMNoStubRolesRefusesToInvent is SPEC003's escape hatch, for anyone who
// would rather have the honest failure than the working import.
func TestBOMNoStubRolesRefusesToInvent(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "ms-transform", "--no-stub-roles")
	if err != nil {
		t.Fatalf("bom import --no-stub-roles: %v\n%s\n%s", err, out, errOut)
	}

	transform, _ := loadEnv(t, w.home, "scratch").Repo("ms-transform")
	if _, bound := transform.Dependencies["datadog-lambda"]; bound {
		t.Errorf("--no-stub-roles still bound the role: %v", transform.Dependencies)
	}
	if !strings.Contains(errOut, "datadog-lambda") {
		t.Errorf("the unfilled role was not reported:\n%s", errOut)
	}
}

// TestBOMExcludeRefusesAResourceTypedRequiredRole is SPEC002's narrowed
// refusal. A resource-typed role is validated against what the producing
// instance really produces, so there is nothing to bind it to.
func TestBOMExcludeRefusesAResourceTypedRequiredRole(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	_, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "ms-transform", "--exclude", "node-group")
	if err == nil {
		t.Fatal("excluding a resource-typed required role was accepted")
	}
	if !strings.Contains(err.Error(), "kubernetes.neuronsphere.io/node-group") {
		t.Errorf("the refusal does not name the resource type: %v", err)
	}
}

// TestBOMExcludeStubsARequiredNameOnlyRole is the pair to the above, and it is
// how a cloud-only class is left out on purpose: excluding it is legal because
// the role it filled can be bound instead.
func TestBOMExcludeStubsARequiredNameOnlyRole(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "ms-transform", "--exclude", "artifact-lib")
	if err != nil {
		t.Fatalf("bom import --exclude: %v\n%s\n%s", err, out, errOut)
	}

	got := sortedDeclared(t, w.home)
	if slices.Contains(got, "artifact-lib") {
		t.Errorf("the excluded instance was imported anyway: %v", got)
	}
	transform, _ := loadEnv(t, w.home, "scratch").Repo("ms-transform")
	if transform.Dependencies["librarian"] != bom.CoreInstanceName {
		t.Errorf("the librarian role = %v, want it bound to %s once its target was excluded",
			transform.Dependencies["librarian"], bom.CoreInstanceName)
	}
}

// TestBOMSelectionFileRoundTrips is NERD013 SPEC004's first property: the file
// a show writes, imported unedited, selects exactly what the show displayed.
func TestBOMSelectionFileRoundTrips(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	path := filepath.Join(t.TempDir(), "sel.toml")
	_, _, err := w.run(t, "bom", "show", "dev", "--instance", "ms-transform",
		"--resolve", "--save-selection", path)
	if err != nil {
		t.Fatalf("bom show --save-selection: %v", err)
	}

	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--selection", path)
	if err != nil {
		t.Fatalf("bom import --selection: %v\n%s\n%s", err, out, errOut)
	}
	got := sortedDeclared(t, w.home)
	want := []string{"artifact-lib", "ms-transform", "node-group"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("declared %v, want %v -- the file should select what the show displayed", got, want)
	}
}

// TestBOMSelectionFileWidensAndNarrows is the point of the file: an optional
// role is opted into by flipping a line, and a required name-only target is
// dropped by flipping another -- whose role is then bound rather than left
// unfilled.
func TestBOMSelectionFileWidensAndNarrows(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	path := filepath.Join(t.TempDir(), "sel.toml")
	if _, _, err := w.run(t, "bom", "show", "dev", "--instance", "ms-transform",
		"--resolve", "--save-selection", path); err != nil {
		t.Fatalf("bom show --save-selection: %v", err)
	}

	// The file must offer the optional instances as something to turn on.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ms-transform", "node-group", "otel", "private-ca"} {
		if !strings.Contains(string(raw), `"`+name+`"`) {
			t.Errorf("the selection file does not list %s:\n%s", name, raw)
		}
	}

	edited := strings.Replace(string(raw), "take       = false\nname       = \"otel\"",
		"take       = true\nname       = \"otel\"", 1)
	edited = strings.Replace(edited, "take       = true\nname       = \"artifact-lib\"",
		"take       = false\nname       = \"artifact-lib\"", 1)
	if edited == string(raw) {
		t.Fatalf("the edit matched nothing; the file format changed:\n%s", raw)
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--selection", path)
	if err != nil {
		t.Fatalf("bom import --selection: %v\n%s\n%s", err, out, errOut)
	}
	got := sortedDeclared(t, w.home)
	if !slices.Contains(got, "otel") {
		t.Errorf("turning otel on did not import it: %v", got)
	}
	if slices.Contains(got, "artifact-lib") {
		t.Errorf("turning artifact-lib off did not drop it: %v", got)
	}
	transform, _ := loadEnv(t, w.home, "scratch").Repo("ms-transform")
	if transform.Dependencies["librarian"] != bom.CoreInstanceName {
		t.Errorf("the librarian role = %v, want it bound once its target was turned off",
			transform.Dependencies["librarian"])
	}
}

// TestBOMSelectionFileRefusesAMisspeltKey is SPEC004's strictness. A file
// somebody edited by hand is exactly where a typo happens, and `takes = true`
// silently ignored would be an import quietly smaller than intended.
func TestBOMSelectionFileRefusesAMisspeltKey(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	path := filepath.Join(t.TempDir(), "sel.toml")
	body := "[[instance]]\ntakes      = true\nname       = \"ms-transform\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--selection", path)
	if err == nil {
		t.Fatal("a misspelt key was accepted")
	}
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("exit code = %v, want a usage error", nserr.CodeOf(err))
	}
}

// TestBOMSelectionFileRefusesMixingWithFlags keeps the two ways of saying which
// instances to take from arguing about it.
func TestBOMSelectionFileRefusesMixingWithFlags(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	path := filepath.Join(t.TempDir(), "sel.toml")
	if _, _, err := w.run(t, "bom", "show", "dev", "--instance", "ms-transform",
		"--resolve", "--save-selection", path); err != nil {
		t.Fatalf("bom show --save-selection: %v", err)
	}

	_, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--selection", path, "--instance", "private-ca")
	if err == nil {
		t.Fatal("--selection and --instance were accepted together")
	}
	if !strings.Contains(err.Error(), "--selection cannot be combined") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
}

// TestBOMStubReportsEveryRoleItBound is the under-reporting guard. Two
// instances need the same absent thing; binding the second silently would hide
// exactly what this warning exists to surface.
func TestBOMStubReportsEveryRoleItBound(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "ms-transform")
	if err != nil {
		t.Fatalf("bom import: %v\n%s\n%s", err, out, errOut)
	}
	for _, role := range []string{"ms-transform:datadog-lambda", "node-group:datadog-lambda"} {
		if !strings.Contains(errOut, role) {
			t.Errorf("%s was bound without being reported:\n%s", role, errOut)
		}
	}

	m := loadEnv(t, w.home, "scratch")
	for _, name := range []string{"ms-transform", "node-group"} {
		r, ok := m.Repo(name)
		if !ok {
			t.Fatalf("%s was not declared", name)
		}
		if r.Dependencies["datadog-lambda"] != bom.CoreInstanceName {
			t.Errorf("%s's datadog-lambda role = %v, want %s",
				name, r.Dependencies["datadog-lambda"], bom.CoreInstanceName)
		}
	}
	// And it is never listed as an ordinary binding as well: two accounts of
	// one thing is what the NERD012 closure note already had to fix once.
	if strings.Contains(errOut, "dd-lambda                      stubbed") {
		t.Errorf("a stub was reported both as a binding and as a stub:\n%s", errOut)
	}
}

// TestBOMDryRunSaysWhatResolveFetched keeps --dry-run believable. It promises
// to fetch nothing, and --resolve makes it fetch manifests; a run that said
// "nothing was fetched" while filling the artifact cache would be the one kind
// of dishonesty a dry run cannot afford.
func TestBOMDryRunSaysWhatResolveFetched(t *testing.T) {
	t.Parallel()

	w := newRoleWorld(t)
	out, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "ms-transform", "--dry-run", "--resolve")
	if err != nil {
		t.Fatalf("bom import --dry-run --resolve: %v", err)
	}
	if strings.Contains(out, "nothing was fetched") {
		t.Errorf("--resolve fetched artifacts and the run denied it:\n%s", out)
	}
	if !strings.Contains(out, "Nothing was written") {
		t.Errorf("the dry run did not say it wrote nothing:\n%s", out)
	}
	if !artifact.Cached(w.home, "hmd-ms-transform", "0.5.201") {
		t.Error("--resolve did not fetch what it needed to read the roles")
	}
}
