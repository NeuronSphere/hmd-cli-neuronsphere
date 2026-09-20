package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// fakeDeployment is a cloud hmd-ms-deployment holding one BOM per environment.
type fakeDeployment struct {
	URL  string
	boms map[string][]msdeploy.BOMEntry
	// calls counts BOM fetches, so a listing that quietly fetched one per
	// environment would be caught.
	bomCalls int
}

func newFakeDeployment(t *testing.T) *fakeDeployment {
	t.Helper()
	f := &fakeDeployment{boms: map[string][]msdeploy.BOMEntry{}}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	f.URL = srv.URL
	t.Cleanup(srv.Close)

	mux.HandleFunc("/apiop/get_deployment_bom/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		f.bomCalls++
		env := strings.TrimPrefix(r.URL.Path, "/apiop/get_deployment_bom/")
		bom, ok := f.boms[env]
		if !ok {
			// What get_valid_environment's assertion looks like over the wire.
			http.Error(w, `{"detail":"Environment, `+env+`, not found."}`, http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(bom)
	})

	mux.HandleFunc("/api/hmd_lang_deployment.environment", func(w http.ResponseWriter, r *http.Request) {
		rows := []map[string]any{}
		for _, env := range sortedKeysOf(f.boms) {
			rows = append(rows, map[string]any{
				"type": env, "account_number": "123456789012", "hmd_region": "reg1",
			})
		}
		_ = json.NewEncoder(w).Encode(rows)
	})

	return f
}

// devBOM is the worked example: a substrate role, an already-local role, a
// multi-target role, an image-only entry and a FAILED one.
func devBOM() []msdeploy.BOMEntry {
	return []msdeploy.BOMEntry{
		{
			// Deliberately NOT called eks-cluster. hmdtr1's dev environment
			// runs hmd-inf-eks-cluster as "eks-upg", and matching the substrate
			// by instance name alone imported it as though it were a workload.
			RepoInstanceName: "eks-upg", RepoClassName: "hmd-inf-eks-cluster",
			RepoClassVersion: "0.1.44", DeploymentID: "d", Status: "DEPLOYED",
		},
		{
			RepoInstanceName: "artifact-lib", RepoClassName: "hmd-ms-artifact-lib",
			RepoClassVersion: "0.4.12", DeploymentID: "d", Status: "DEPLOYED",
		},
		{
			RepoInstanceName: "device-lib", RepoClassName: "hmd-ms-device-lib",
			RepoClassVersion: "0.4.3", DeploymentID: "d", Status: "DEPLOYED",
		},
		{
			RepoInstanceName: "transform-image", RepoClassName: "hmd-img-transform-base",
			RepoClassVersion: "1.0.42", DeploymentID: "d", Status: "DEPLOYED", ImageOnly: true,
		},
		{
			RepoInstanceName: "ms-transform", RepoClassName: "hmd-ms-transform",
			RepoClassVersion: "0.5.201", DeploymentID: "d", Status: "DEPLOYED",
			InstanceConfiguration: map[string]any{"replicas": float64(2)},
			Dependencies: map[string]any{
				"database-instance": "environment-db",
				"librarian":         []any{"artifact-lib", "device-lib"},
				"cluster":           "eks-upg",
			},
		},
		{
			RepoInstanceName: "trino", RepoClassName: "hmd-inf-trino",
			RepoClassVersion: "0.2.5", DeploymentID: "d", Status: "FAILED",
			Dependencies: map[string]any{"cluster": "eks-upg"},
		},
		{
			RepoInstanceName: "superset", RepoClassName: "hmd-inf-superset",
			RepoClassVersion: "0.3.9", DeploymentID: "d", Status: "DEPLOYED",
			Dependencies: map[string]any{"warehouse": "a-cloud-only-thing"},
		},
	}
}

// bomWorld is a cloud deployment service, a cloud librarian holding every
// artifact the BOM names, and a registered local environment.
type bomWorld struct {
	dep  *fakeDeployment
	lib  *fakeLibrarian
	home string
}

func newBOMWorld(t *testing.T) *bomWorld {
	t.Helper()
	home := t.TempDir()
	dep := newFakeDeployment(t)
	dep.boms["dev"] = devBOM()
	dep.boms["prod"] = devBOM()[:1]

	lib := newFakeLibrarian(t)
	for _, e := range devBOM() {
		lib.content[librarian.Spec{
			Name: e.RepoClassName, Version: e.RepoClassVersion, ItemType: "build",
		}.ContentPath()] = artifactZip(t, e.RepoClassName, e.RepoClassVersion, "from-the-cloud")
	}

	if _, _, err := run(t, fakeEnv(map[string]string{"HMD_HOME": home}), "env", "add", "scratch"); err != nil {
		t.Fatalf("env add: %v", err)
	}
	return &bomWorld{dep: dep, lib: lib, home: home}
}

func (w *bomWorld) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	full := append([]string{args[0], args[1], "--home", w.home,
		"--url", w.dep.URL, "--librarian-url", w.lib.URL, "--local-url", w.lib.URL}, args[2:]...)
	return run(t, fakeEnv(map[string]string{
		librarian.APIKeyEnv:           "k",
		msdeploy.AuthTokenEnv:         "t",
		"HMD_LOCAL_MS_DEPLOYMENT_URL": "http://127.0.0.1:1",
	}), full...)
}

// read is for the verbs that take no librarian flags.
func (w *bomWorld) read(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	full := append([]string{args[0], args[1], "--home", w.home, "--url", w.dep.URL}, args[2:]...)
	return run(t, fakeEnv(map[string]string{
		librarian.APIKeyEnv:   "k",
		msdeploy.AuthTokenEnv: "t",
	}), full...)
}

// TestBOMEnvsListsInOneRequest is acceptance criterion 2. A listing that
// fetched a BOM per environment would look identical and cost a great deal
// more, so the request count is the assertion.
func TestBOMEnvsListsInOneRequest(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	out, _, err := w.read(t, "bom", "envs")
	if err != nil {
		t.Fatalf("bom envs: %v", err)
	}
	for _, want := range []string{"dev", "prod", "123456789012", "reg1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing does not mention %q:\n%s", want, out)
		}
	}
	if w.dep.bomCalls != 0 {
		t.Errorf("bom envs fetched %d BOMs; it should fetch none", w.dep.bomCalls)
	}
}

// TestBOMShowReportsVersionsAndWhatIsHeld is acceptance criterion 3.
func TestBOMShowReportsVersionsAndWhatIsHeld(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	// One artifact already unpacked here, so the CACHED column has both answers
	// to give.
	if _, err := artifact.Store(w.home, "hmd-inf-trino", "0.2.5", artifactZip(t, "hmd-inf-trino", "0.2.5", "held")); err != nil {
		t.Fatal(err)
	}

	out, _, err := w.read(t, "bom", "show", "dev")
	if err != nil {
		t.Fatalf("bom show: %v", err)
	}
	if !strings.Contains(out, "0.5.201") || !strings.Contains(out, "hmd-ms-transform") {
		t.Errorf("the concrete version is not reported:\n%s", out)
	}
	if !strings.Contains(out, "is running 7 instances") {
		t.Errorf("the instance count is not reported:\n%s", out)
	}
	// The trino row is cached; the transform row is not.
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "trino") && !strings.HasSuffix(strings.TrimSpace(line), "yes"):
			t.Errorf("a held artifact is not reported as cached: %q", line)
		case strings.HasPrefix(line, "ms-transform") && !strings.HasSuffix(strings.TrimSpace(line), "no"):
			t.Errorf("an absent artifact is not reported as missing: %q", line)
		}
	}
}

// TestBOMShowPreviewsTheSelection is the promise that a show and an import
// agree: the same flags, the same closure, the same rows.
func TestBOMShowPreviewsTheSelection(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	out, errOut, err := w.read(t, "bom", "show", "dev", "--instance", "ms-transform")
	if err != nil {
		t.Fatalf("bom show: %v", err)
	}
	for _, want := range []string{"ms-transform", "artifact-lib", "device-lib"} {
		if !strings.Contains(out, want) {
			t.Errorf("the closure is not previewed; %q is missing:\n%s", want, out)
		}
	}
	if strings.Contains(out, "superset") {
		t.Errorf("an instance nobody selected was previewed:\n%s", out)
	}
	if !strings.Contains(errOut, "substrate") {
		t.Errorf("the substrate binding is not reported:\n%s", errOut)
	}
}

// TestBOMShowJSONIsTheServicesOwnAnswer keeps the scripting path honest: --json
// is the BOM, not a rendering of it.
func TestBOMShowJSONIsTheServicesOwnAnswer(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	out, _, err := w.read(t, "bom", "show", "dev", "--json")
	if err != nil {
		t.Fatalf("bom show --json: %v", err)
	}
	var bom []msdeploy.BOMEntry
	if err := json.Unmarshal([]byte(out), &bom); err != nil {
		t.Fatalf("--json did not print a BOM: %v\n%s", err, out)
	}
	if len(bom) != 7 {
		t.Errorf("--json printed %d entries, want the whole BOM", len(bom))
	}
}

// TestBOMShowRefusesAnUnknownEnvironment turns a server-side assertion into a
// message about a typo, listing what is there.
func TestBOMShowRefusesAnUnknownEnvironment(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	_, _, err := w.read(t, "bom", "show", "staging")
	if err == nil {
		t.Fatal("bom show succeeded against an environment that does not exist")
	}
	if !strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "prod") {
		t.Errorf("error = %v, want one listing the environments there are", err)
	}
	if nserr.CodeOf(err) != nserr.Usage {
		t.Errorf("exit code = %d, want Usage for a name that matches nothing", nserr.CodeOf(err))
	}
}

// TestBOMImportClosesOverDependencies is acceptance criterion 5, and the reason
// the closure exists: ms-deployment fails a whole ChangeSet on a required role
// nothing fills.
func TestBOMImportClosesOverDependencies(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "ms-transform")
	if err != nil {
		t.Fatalf("bom import: %v\n%s\n%s", err, out, errOut)
	}

	m := loadEnv(t, w.home, "scratch")
	got := instanceNames(m)
	sort.Strings(got)
	want := []string{"artifact-lib", "device-lib", "ms-transform"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("declared %v, want %v -- the closure should add what fills the roles", got, want)
	}

	transform, ok := m.Repo("ms-transform")
	if !ok {
		t.Fatal("ms-transform was not declared")
	}
	if transform.Version != "0.5.201" {
		t.Errorf("version = %q, want the cloud's", transform.Version)
	}
	if transform.SourceType() != manifest.SourceArtifact {
		t.Errorf("source = %q, want an artifact source: a cloud version must not resolve to a checkout",
			transform.SourceType())
	}
	// The cluster is substrate, under whatever name the cloud gave it, and is
	// bound rather than declared -- declaring it would make it removable by
	// deleting a line, and deploying it would put a second cluster inside the
	// first.
	for _, name := range []string{"eks-upg", "eks-cluster"} {
		if _, declared := m.Repo(name); declared {
			t.Errorf("the substrate was declared as %q", name)
		}
	}
	if transform.Dependencies["cluster"] != "eks-cluster" {
		t.Errorf("the cluster role = %v, want it renamed to this environment's substrate instance",
			transform.Dependencies["cluster"])
	}
	if transform.Dependencies["database-instance"] != "environment-db" {
		t.Errorf("the database role was not bound: %v", transform.Dependencies)
	}
	// A multi-target role keeps its list.
	librarians, _ := transform.Dependencies["librarian"].([]any)
	if len(librarians) != 2 {
		t.Errorf("the multi-target role was not preserved: %v", transform.Dependencies["librarian"])
	}

	// And the artifacts came down, so what was declared can actually deploy.
	for _, cv := range [][2]string{
		{"hmd-ms-transform", "0.5.201"}, {"hmd-ms-artifact-lib", "0.4.12"}, {"hmd-ms-device-lib", "0.4.3"},
	} {
		if !artifact.Cached(w.home, cv[0], cv[1]) {
			t.Errorf("%s@%s was declared but not fetched", cv[0], cv[1])
		}
	}
	if !strings.Contains(errOut, "pulled in by the closure") {
		t.Errorf("the closure was not reported apart from the selection:\n%s", errOut)
	}
}

// TestBOMImportNoDepsTakesTheSelectionLiterally is the other half of criterion
// 5, and shows what the closure is protecting against.
func TestBOMImportNoDepsTakesTheSelectionLiterally(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	_, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "ms-transform", "--no-deps")
	if err != nil {
		t.Fatalf("bom import --no-deps: %v", err)
	}
	if got := instanceNames(loadEnv(t, w.home, "scratch")); len(got) != 1 {
		t.Errorf("declared %v, want ms-transform alone", got)
	}
	if !strings.Contains(errOut, "librarian role wants") {
		t.Errorf("the roles left unfilled were not warned about:\n%s", errOut)
	}
}

// TestBOMImportDryRunWritesAndFetchesNothing is acceptance criterion 4.
func TestBOMImportDryRunWritesAndFetchesNothing(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	out, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "superset", "--dry-run")
	if err != nil {
		t.Fatalf("bom import --dry-run: %v", err)
	}
	if !strings.Contains(out, "Nothing was written and nothing was fetched") {
		t.Errorf("a dry run did not say it wrote nothing:\n%s", out)
	}
	if artifact.Cached(w.home, "hmd-inf-superset", "0.3.9") {
		t.Error("a dry run fetched an artifact")
	}
	if _, err := os.Stat(manifest.DefaultPath(w.home, "scratch")); err == nil {
		t.Error("a dry run wrote the manifest")
	}
	// superset's role points at something the BOM does not contain, which is
	// what a cloud-only dependency looks like.
	if !strings.Contains(errOut, "a-cloud-only-thing") || !strings.Contains(errOut, "not in the BOM") {
		t.Errorf("a target outside the BOM was not named:\n%s", errOut)
	}
}

// TestBOMImportRefusesAnEmptySelection: importing a whole cloud environment is
// a thing to ask for out loud.
func TestBOMImportRefusesAnEmptySelection(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	_, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch")
	if err == nil {
		t.Fatal("bom import with no selection succeeded")
	}
	if !strings.Contains(err.Error(), "--all") || !strings.Contains(err.Error(), "--instance") {
		t.Errorf("error = %v, want one naming the ways to select", err)
	}
}

// TestBOMImportSkipsWhatIsNotDeployed: a FAILED instance is in the BOM and is
// not a thing to reproduce by default.
func TestBOMImportSkipsWhatIsNotDeployed(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	_, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--class", "hmd-inf-trino")
	if err != nil {
		t.Fatalf("bom import: %v", err)
	}
	if !strings.Contains(errOut, "failed") {
		t.Errorf("the FAILED instance was not reported as skipped:\n%s", errOut)
	}
	if _, err := os.Stat(manifest.DefaultPath(w.home, "scratch")); err == nil {
		if got := instanceNames(loadEnv(t, w.home, "scratch")); len(got) != 0 {
			t.Errorf("declared %v, want nothing: the only match was FAILED", got)
		}
	}

	// --include-failed asks for it anyway.
	if _, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--class", "hmd-inf-trino", "--include-failed"); err != nil {
		t.Fatalf("bom import --include-failed: %v", err)
	}
	if got := instanceNames(loadEnv(t, w.home, "scratch")); len(got) != 1 || got[0] != "trino" {
		t.Errorf("declared %v, want trino", got)
	}
}

// TestBOMImportLeavesNothingDeclaredWhenTheFetchFails is acceptance criterion 6.
// A declaration whose artifact is not here is a manifest that fails at apply
// for a reason the manifest does not mention.
func TestBOMImportLeavesNothingDeclaredWhenTheFetchFails(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	// Never published, which is what a repo class built only in-house looks
	// like from a cloud librarian.
	delete(w.lib.content, librarian.Spec{
		Name: "hmd-inf-superset", Version: "0.3.9", ItemType: "build",
	}.ContentPath())

	_, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "superset", "--no-deps")
	if err == nil {
		t.Fatal("bom import succeeded with an artifact that could not be fetched")
	}
	if !strings.Contains(err.Error(), "hmd-inf-superset") {
		t.Errorf("error = %v, want one naming what could not be fetched", err)
	}
	if _, statErr := os.Stat(manifest.DefaultPath(w.home, "scratch")); statErr == nil {
		if _, declared := loadEnv(t, w.home, "scratch").Repo("superset"); declared {
			t.Error("an instance whose artifact could not be fetched was declared anyway")
		}
	}
}

// TestBOMImportLeavesExistingDeclarationsAlone: a hand-edited declaration is
// never overwritten, which is the rule `nsctl repo import` already follows.
func TestBOMImportLeavesExistingDeclarationsAlone(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	if _, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "artifact-lib"); err != nil {
		t.Fatalf("first import: %v", err)
	}
	before := loadEnv(t, w.home, "scratch")
	first, _ := before.Repo("artifact-lib")

	out, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "artifact-lib")
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !strings.Contains(out, "already declares") {
		t.Errorf("a repeated import did not say there was nothing to do:\n%s", out)
	}
	after, _ := loadEnv(t, w.home, "scratch").Repo("artifact-lib")
	if after.Version != first.Version {
		t.Errorf("a repeated import rewrote the declaration: %q -> %q", first.Version, after.Version)
	}
}

// TestBOMImportRefusesToExcludeWhatTheClosureNeeds: silently unfilling a role
// would reintroduce exactly the failure the closure exists to prevent.
func TestBOMImportRefusesToExcludeWhatTheClosureNeeds(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	_, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "ms-transform", "--exclude", "artifact-lib")
	if err == nil {
		t.Fatal("an exclusion that unfills a role was accepted")
	}
	if !strings.Contains(err.Error(), "--no-deps") {
		t.Errorf("error = %v, want one naming the flag that takes the selection literally", err)
	}
}

// TestBOMImportRefusesASelectorThatMatchesNothing: finding out at the end that
// an import was smaller than intended is much worse than finding out now.
func TestBOMImportRefusesASelectorThatMatchesNothing(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	for _, args := range [][]string{
		{"--instance", "ms-transfrom"},
		{"--class", "hmd-inf-nothing"},
	} {
		_, _, err := w.run(t, append([]string{"bom", "import", "dev", "--env", "scratch"}, args...)...)
		if err == nil {
			t.Fatalf("%v matched nothing and was accepted", args)
		}
		if nserr.CodeOf(err) != nserr.Usage {
			t.Errorf("%v: exit code = %d, want Usage", args, nserr.CodeOf(err))
		}
	}
}

// TestBOMImportNoPullNamesThePulls: a declaration without its artifact is
// deliberate here, so the remaining work is spelled out.
func TestBOMImportNoPullNamesThePulls(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	out, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "artifact-lib", "--no-pull")
	if err != nil {
		t.Fatalf("bom import --no-pull: %v", err)
	}
	if !strings.Contains(out, "nsctl artifact pull hmd-ms-artifact-lib@0.4.12") {
		t.Errorf("the pulls to run were not named:\n%s", out)
	}
	if artifact.Cached(w.home, "hmd-ms-artifact-lib", "0.4.12") {
		t.Error("--no-pull fetched an artifact")
	}
}

// TestBOMImportKeepsAnImageOnlyEntryBare: an image_only entry carries no
// configuration and no dependencies, and inventing empty ones would change what
// it says about itself.
func TestBOMImportKeepsAnImageOnlyEntryBare(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	if _, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
		"--instance", "transform-image"); err != nil {
		t.Fatalf("bom import: %v", err)
	}
	r, ok := loadEnv(t, w.home, "scratch").Repo("transform-image")
	if !ok {
		t.Fatal("the image-only instance was not declared")
	}
	if len(r.Dependencies) != 0 || len(r.InstanceConfiguration) != 0 {
		t.Errorf("an image-only entry gained a configuration or dependencies: %+v", r)
	}
	if r.Version != "1.0.42" {
		t.Errorf("version = %q, want 1.0.42", r.Version)
	}
}

// TestBOMImportUsesACachedArtifactRatherThanRefetching: these are
// multi-megabyte zips, and one already unpacked here is the same bytes.
func TestBOMImportUsesACachedArtifactRatherThanRefetching(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	if _, err := artifact.Store(w.home, "hmd-ms-artifact-lib", "0.4.12",
		artifactZip(t, "hmd-ms-artifact-lib", "0.4.12", "already-here")); err != nil {
		t.Fatal(err)
	}
	out, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "artifact-lib")
	if err != nil {
		t.Fatalf("bom import: %v", err)
	}
	if !strings.Contains(out, "already here") {
		t.Errorf("a cached artifact was not reported as held:\n%s", out)
	}
	// The unpacked copy is the one that was already there.
	marker, readErr := os.ReadFile(filepath.Join(
		artifact.Dir(w.home, "hmd-ms-artifact-lib", "0.4.12"), "src", "cdktf", "stack.py"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(marker) != "already-here" {
		t.Errorf("the cached artifact was replaced: %q", marker)
	}
}

// TestBOMImportBindsSubstrateByClassNotJustName is the case a live BOM exposed.
//
// hmdtr1's dev environment runs hmd-inf-eks-cluster as "eks-upg". Matching the
// substrate by instance name alone treated it as a workload: selecting
// ms-transform imported the cloud's EKS cluster, which locally would mean
// deploying a second cluster inside the one the environment already has.
func TestBOMImportBindsSubstrateByClassNotJustName(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	_, errOut, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "ms-transform")
	if err != nil {
		t.Fatalf("bom import: %v", err)
	}
	if got := instanceNames(loadEnv(t, w.home, "scratch")); slices.Contains(got, "eks-upg") {
		t.Errorf("declared %v, which imports the cloud's cluster as a workload", got)
	}
	if !strings.Contains(errOut, "substrate, as eks-cluster") {
		t.Errorf("the rename was not reported:\n%s", errOut)
	}
}

// TestBOMImportApplyDeclaresThenApplies pins the order --apply works in. The
// world's control plane is unreachable by construction, so the apply fails
// with the "not answering" refusal -- and the manifest must already hold the
// declarations by then, because an apply that ran before the declare would
// deploy nothing new and an import that failed to save would not apply at all.
func TestBOMImportApplyDeclaresThenApplies(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	out, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "ms-transform", "--apply")
	if err == nil {
		t.Fatal("bom import --apply succeeded with no control plane to apply against")
	}
	if !strings.Contains(err.Error(), "not answering") {
		t.Errorf("error = %v, want the apply's own refusal, so the apply was reached", err)
	}
	if !strings.Contains(out, "Applying scratch") {
		t.Errorf("the apply was not announced:\n%s", out)
	}
	if strings.Contains(out, "Run `nsctl env apply") {
		t.Errorf("--apply still told the user to run the apply themselves:\n%s", out)
	}
	got := instanceNames(loadEnv(t, w.home, "scratch"))
	sort.Strings(got)
	if want := "artifact-lib,device-lib,ms-transform"; strings.Join(got, ",") != want {
		t.Errorf("declared %v before applying, want %s", got, want)
	}
}

// TestBOMImportApplyRefusesItsContradictions: a dry run deploys nothing and an
// apply refuses an uncached artifact, so both combinations are usage errors
// before anything is fetched.
func TestBOMImportApplyRefusesItsContradictions(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	for _, other := range []string{"--dry-run", "--no-pull"} {
		_, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch",
			"--instance", "ms-transform", "--apply", other)
		if err == nil {
			t.Errorf("--apply %s was accepted", other)
			continue
		}
		if nserr.CodeOf(err) != nserr.Usage {
			t.Errorf("--apply %s: exit code = %d, want Usage", other, nserr.CodeOf(err))
		}
		if !strings.Contains(err.Error(), "--apply") {
			t.Errorf("--apply %s: error = %v, want one naming the flags", other, err)
		}
	}
	if w.dep.bomCalls != 0 {
		t.Errorf("a refused flag combination still fetched %d BOMs", w.dep.bomCalls)
	}
}

// TestBOMImportApplySkipsAPartialImport: an import that could not fetch every
// artifact is declared and exits non-zero naming the failures (NERD012
// SPEC006); with --apply it additionally does not apply, and says so, because
// deploying a knowingly incomplete import is the quiet failure that rule
// exists to prevent.
func TestBOMImportApplySkipsAPartialImport(t *testing.T) {
	t.Parallel()

	w := newBOMWorld(t)
	delete(w.lib.content, librarian.Spec{
		Name: "hmd-ms-device-lib", Version: "0.4.3", ItemType: "build",
	}.ContentPath())

	_, _, err := w.run(t, "bom", "import", "dev", "--env", "scratch", "--instance", "ms-transform", "--apply")
	if err == nil {
		t.Fatal("bom import --apply succeeded with an artifact that could not be fetched")
	}
	if !strings.Contains(err.Error(), "hmd-ms-device-lib") {
		t.Errorf("error = %v, want one naming what could not be fetched", err)
	}
	if !strings.Contains(err.Error(), "Not applied") {
		t.Errorf("error = %v, want it to say the apply was skipped", err)
	}
	if strings.Contains(err.Error(), "not answering") {
		t.Errorf("error = %v -- the apply ran against a partial import", err)
	}
	got := instanceNames(loadEnv(t, w.home, "scratch"))
	sort.Strings(got)
	if want := "artifact-lib,ms-transform"; strings.Join(got, ",") != want {
		t.Errorf("declared %v, want %s -- what was fetched is declared, what was not is not", got, want)
	}
}

// TestBOMEntryInvariantsObservedLive records what a real 109-instance BOM from
// hmdtr1's dev environment showed on 2026-09-15, decoded with
// DisallowUnknownFields so a field this struct lacks would have failed.
//
// The response itself is deliberately NOT checked in. It carries a real
// tenant's infrastructure -- customer_ips, hmd_ips, assume_principals,
// proxy_secret, account numbers, and instance names containing a person's name
// -- and none of that belongs in a repository. What is preserved here is what
// the response proved about the *shape*, which is all this decoder needs.
func TestBOMEntryInvariantsObservedLive(t *testing.T) {
	t.Parallel()

	// Observed: 109 entries, every one carrying repo_instance_name,
	// repo_class_name, repo_class_version, deployment_id, status and
	// auto_deploy; 69 with dependencies, 57 with instance_configuration, 3 with
	// hmd_region, 1 with image_only. No entry lacked a version.
	bom := devBOM()

	var sawImageOnly, sawStringRole, sawListRole bool
	for _, e := range bom {
		if e.RepoClassVersion == "" {
			t.Errorf("%s has no version; live data had none such", e.RepoInstanceName)
		}
		if e.ImageOnly {
			sawImageOnly = true
			// Observed live: the one image_only entry carried neither
			// dependencies nor instance_configuration, matching the Python's
			// branch.
			if e.Dependencies != nil || e.InstanceConfiguration != nil {
				t.Errorf("%s: image_only with config or deps", e.RepoInstanceName)
			}
		}
		for _, role := range e.Roles() {
			if _, isList := e.Dependencies[role].([]any); isList {
				sawListRole = true
			} else {
				sawStringRole = true
			}
			if len(e.Targets(role)) == 0 {
				t.Errorf("%s: role %q has no targets", e.RepoInstanceName, role)
			}
		}
	}
	if !sawImageOnly || !sawStringRole || !sawListRole {
		t.Error("the fixture no longer covers every shape live data contained")
	}

	// Observed live: auto_deploy is the STRING "true"/"false" on all 109
	// entries, never a bool. Decoding it as a bool would have failed on the
	// whole response, so the field's type is the assertion.
	var probe msdeploy.BOMEntry
	if err := json.Unmarshal([]byte(`{"auto_deploy":"false"}`), &probe); err != nil {
		t.Fatalf("auto_deploy no longer decodes from the string spelling: %v", err)
	}
	if probe.AutoDeploy != "false" {
		t.Errorf("auto_deploy = %#v, want the string live data carries", probe.AutoDeploy)
	}
}
