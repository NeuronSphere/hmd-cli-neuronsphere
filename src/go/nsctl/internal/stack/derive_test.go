package stack

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// NERD019 SPEC004: a fixture environment modelling the running platform.
//
//	local-neuronsphere (substrate)
//	environment-db     (substrate)
//	bucket             hmd-inf-s3bucket, declared by stack "storage"
//	metastore          hmd-inf-hive-metastore -> bucket, database=environment-db
//	trino              hmd-inf-trino -> metastore, compute=local-neuronsphere
//	superset           hmd-inf-superset -> query=trino (working tree!)
//	airflow            hmd-app-airflow -> query=trino, compute=local-neuronsphere
//	transform          hmd-ms-transform (unrelated)
func fixtureBOM() []msdeploy.BOMEntry {
	return []msdeploy.BOMEntry{
		{RepoInstanceName: "local-neuronsphere", RepoClassName: "hmd-cli-neuronsphere", RepoClassVersion: "1.0"},
		{RepoInstanceName: "environment-db", RepoClassName: "hmd-inf-rds", RepoClassVersion: "0.2"},
		{RepoInstanceName: "bucket", RepoClassName: "hmd-inf-s3bucket", RepoClassVersion: "0.1.13"},
		{RepoInstanceName: "metastore", RepoClassName: "hmd-inf-hive-metastore", RepoClassVersion: "0.4.2",
			Dependencies: map[string]any{"warehouse-bucket": "bucket", "database": "environment-db"}},
		{RepoInstanceName: "trino", RepoClassName: "hmd-inf-trino", RepoClassVersion: "0.3.12",
			Dependencies: map[string]any{"metastore": "metastore", "compute": "local-neuronsphere"}},
		{RepoInstanceName: "superset", RepoClassName: "hmd-inf-superset", RepoClassVersion: "0.5.1",
			Dependencies: map[string]any{"query": "trino"}},
		{RepoInstanceName: "airflow", RepoClassName: "hmd-app-airflow", RepoClassVersion: "0.4.344",
			Dependencies: map[string]any{"query": "trino", "compute": "local-neuronsphere"}},
		{RepoInstanceName: "transform", RepoClassName: "hmd-ms-transform", RepoClassVersion: "1.0.899"},
	}
}

func fixtureManifest() *manifest.Manifest {
	return &manifest.Manifest{Version: 1, Name: "local", Scope: manifest.ScopeEnvironment,
		Repos: []manifest.Repo{
			{InstanceName: "bucket", RepoClassName: "hmd-inf-s3bucket", Version: "0.1.13", Source: &manifest.Source{Type: manifest.SourceArtifact}},
			{InstanceName: "metastore", RepoClassName: "hmd-inf-hive-metastore", Version: "0.4.2", Source: &manifest.Source{Type: manifest.SourceArtifact},
				InstanceConfiguration: map[string]any{"warehouse_dir": "/Users/me/warehouse", "db_name": "metastore"}},
			{InstanceName: "trino", RepoClassName: "hmd-inf-trino", Version: "0.3.12", Source: &manifest.Source{Type: manifest.SourceArtifact}},
			{InstanceName: "superset", RepoClassName: "hmd-inf-superset", Source: &manifest.Source{Type: manifest.SourceLocal, Path: "/src/superset"}},
			{InstanceName: "airflow", RepoClassName: "hmd-app-airflow", Version: "0.4.344", Source: &manifest.Source{Type: manifest.SourceArtifact}},
		},
		Stacks: []manifest.StackRecord{{Name: "storage", Version: "0.1.0", Ref: "oci://ghcr.io/hmdlabs/stacks/storage", Declared: []string{"bucket"}}},
	}
}

func kinds(d *Derivation) map[string]Kind {
	out := map[string]Kind{}
	for _, r := range d.Rows {
		out[r.Instance] = r.Kind
	}
	return out
}

func TestDeriveClassifiesEveryRow(t *testing.T) {
	t.Parallel()
	g := GraphFromBOM(fixtureBOM(), fixtureManifest())
	d, err := Derive(g, Options{Roots: []string{"trino", "airflow"},
		ResourceOf: func(class string) string {
			if class == "hmd-inf-s3bucket" {
				return "storage.neuronsphere.io/bucket"
			}
			return ""
		}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Kind{
		"trino": KindRoot, "airflow": KindRoot,
		"metastore":          KindCompanion,
		"bucket":             KindCrossStack,
		"local-neuronsphere": KindSubstrate, "environment-db": KindSubstrate,
	}
	got := kinds(d)
	for name, k := range want {
		if got[name] != k {
			t.Errorf("%s: kind %q, want %q", name, got[name], k)
		}
	}
	if _, reached := got["transform"]; reached {
		t.Error("an unrelated instance must not be reached")
	}
	if _, reached := got["superset"]; reached {
		t.Error("nothing depends on superset; it must not be reached")
	}

	// The rendered section.
	raw, err := json.Marshal(map[string]any{"name": "hmd-stack-x", "deploy": map[string]any{"dependencies": d.Dependencies}, "local": d.Local})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		`"default_profiles":["airflow","trino"]`,
		`"instance_name":"trino","profiles":["trino"]`,
		`"instance_name":"metastore"`, `"version_spec":"== 0.4.2"`,
		`"warehouse_dir":"/Users/me/warehouse"`,
		`"warehouse-bucket":{"bind":"`, // no: bucket is cross-stack, see below
	} {
		if want == `"warehouse-bucket":{"bind":"` {
			if strings.Contains(text, want) {
				t.Errorf("bucket is another stack's, not substrate: %s", text)
			}
			continue
		}
		if !strings.Contains(text, want) {
			t.Errorf("rendered section lacks %s:\n%s", want, text)
		}
	}
	// Cross-stack: an external role with the resource type and the suggestion,
	// and metastore's dependency rewritten to the role key.
	for _, want := range []string{
		`"warehouse-bucket":{"external":true,"suggest":"oci://ghcr.io/hmdlabs/stacks/storage"}`,
		`"resource":{"resource_definition_name":"bucket","resource_namespace":"storage.neuronsphere.io"}`,
		`"dependencies":{"database":"environment-db","warehouse-bucket":"warehouse-bucket"}`,
		`"compute":{"bind":"local-neuronsphere"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered section lacks %s:\n%s", want, text)
		}
	}
	// Host-specific configuration is flagged, not silently copied.
	for _, r := range d.Rows {
		if r.Instance == "metastore" && strings.Join(r.HostSpecific, ",") != "warehouse_dir" {
			t.Errorf("HostSpecific = %v", r.HostSpecific)
		}
	}

	// And localspec accepts what was rendered, with the external role.
	m, err := localspec.Parse(raw)
	if err != nil {
		t.Fatalf("localspec refuses the derived manifest: %v", err)
	}
	var external, bound int
	for _, w := range m.Wants() {
		if w.External {
			external++
		}
		if w.Bind != "" {
			bound++
		}
	}
	if external != 1 || bound != 2 {
		t.Errorf("external=%d bound=%d wants=%+v", external, bound, m.Wants())
	}
}

func TestDeriveRefusesAWorkingTreeUnlessBundled(t *testing.T) {
	t.Parallel()
	g := GraphFromBOM(fixtureBOM(), fixtureManifest())
	d, err := Derive(g, Options{Roots: []string{"superset"}})
	if err == nil || !strings.Contains(err.Error(), "--bundle-local superset") {
		t.Fatalf("err = %v", err)
	}
	if kinds(d)["superset"] != KindLocal {
		t.Errorf("rows = %+v", d.Rows)
	}
	d, err = Derive(g, Options{Roots: []string{"superset"}, BundleLocal: []string{"superset"}})
	if err != nil {
		t.Fatal(err)
	}
	if kinds(d)["superset"] != KindRoot || kinds(d)["trino"] != KindCompanion {
		t.Errorf("kinds = %v", kinds(d))
	}
}

func TestDeriveIncludeProvidedBundlesTheOtherStacksInstance(t *testing.T) {
	t.Parallel()
	g := GraphFromBOM(fixtureBOM(), fixtureManifest())
	d, err := Derive(g, Options{Roots: []string{"trino"}, IncludeProvided: true})
	if err != nil {
		t.Fatal(err)
	}
	if kinds(d)["bucket"] != KindCompanion {
		t.Errorf("bucket = %q", kinds(d)["bucket"])
	}
}

func TestDeriveRefusals(t *testing.T) {
	t.Parallel()
	g := GraphFromBOM(fixtureBOM(), nil)
	if _, err := Derive(g, Options{}); err != ErrNoRoots {
		t.Errorf("no roots: %v", err)
	}
	if _, err := Derive(g, Options{Roots: []string{"nope"}}); err == nil || !strings.Contains(err.Error(), "airflow, bucket") {
		t.Errorf("unknown root must list the environment: %v", err)
	}
}

func TestLocalDiffers(t *testing.T) {
	t.Parallel()
	g := GraphFromBOM(fixtureBOM(), fixtureManifest())
	d, err := Derive(g, Options{Roots: []string{"airflow"}})
	if err != nil {
		t.Fatal(err)
	}
	same, err := d.ManifestJSON("hmd-stack-x", "x")
	if err != nil {
		t.Fatal(err)
	}
	if differs, _, err := d.LocalDiffers(same); err != nil || differs {
		t.Errorf("identical must not differ: %v %v", differs, err)
	}
	if differs, why, _ := d.LocalDiffers([]byte(`{"local":{"version":1,"repos":[]}}`)); !differs || why == "" {
		t.Error("a different section must differ and say so")
	}
}

func TestBOMFromInstancesRoundTrips(t *testing.T) {
	t.Parallel()
	entries := BOMFromInstances([]msdeploy.DeployedInstance{
		{Name: "b", RepoClassName: "c", RepoClassVersion: "1", Dependencies: map[string][]string{"r": {"a"}, "s": {"x", "y"}}},
		{Name: "a", RepoClassName: "c", RepoClassVersion: "1"},
	})
	if entries[0].RepoInstanceName != "a" || entries[1].Targets("s")[1] != "y" || entries[1].Targets("r")[0] != "a" {
		t.Errorf("entries = %+v", entries)
	}
}
