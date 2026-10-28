package environment

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

const analyticsBundleFile = `{
  "bundle_name": "analytics",
  "roles": {
    "compute": {"repo_class_name": "hmd-inf-eks-node-group", "required": "true", "provision": "false"},
    "airflow": {"repo_class_name": "hmd-app-airflow", "required": "true"},
    "superset": {"repo_class_name": "hmd-app-superset", "required": "false", "instance_name": "bi"}
  },
  "local": {"roles": {"compute": {"bind": "local-neuronsphere"}}}
}`

func cacheBundle(t *testing.T, home, class, version, name, body string) {
	t.Helper()
	dir := filepath.Join(artifact.Dir(home, class, version), "meta-data", "bundles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBundleRecordingTranslatesWantKeysBackToRoles(t *testing.T) {
	home := t.TempDir()
	cacheBundle(t, home, "hmd-stack-analytics", "0.1.4", "analytics", analyticsBundleFile)
	rec := manifest.BundleRecord{
		Name: "analytics", Version: "0.1.4", Class: "hmd-stack-analytics",
		Ref: "oci://ghcr.io/neuronsphere/stacks/analytics", Digest: "sha256:abc",
		Profiles: []string{"superset"},
		// Keyed by want: the companion renamed bi, the stack subject itself.
		Bindings: map[string]string{
			"compute": "local-neuronsphere", "airflow": "airflow", "bi": "bi-1",
			"hmd-stack-analytics": "analytics",
		},
		Declared: []string{"airflow", "bi-1"},
	}

	upsert, attach, ok, err := bundleRecording(home, "dev", rec)
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if upsert["source_repo_class_name"] != "hmd-stack-analytics" || upsert["source_version"] != "0.1.4" {
		t.Errorf("upsert = %v", upsert)
	}
	if bundles := upsert["bundles"].([]map[string]any); len(bundles) != 1 || bundles[0]["bundle_name"] != "analytics" {
		t.Errorf("upsert bundles = %v", upsert["bundles"])
	}
	wantBindings := map[string]string{"compute": "local-neuronsphere", "airflow": "airflow", "superset": "bi-1"}
	if !reflect.DeepEqual(attach["bindings"], wantBindings) {
		t.Errorf("bindings = %v, want %v", attach["bindings"], wantBindings)
	}
	if !reflect.DeepEqual(attach["declared"], []string{"airflow", "bi-1"}) {
		t.Errorf("declared = %v", attach["declared"])
	}
	for k, v := range map[string]any{
		"environment": "dev", "bundle_name": "analytics", "version": "0.1.4",
		"release_version": "0.1.4", "source_ref": rec.Ref, "digest": rec.Digest,
	} {
		if attach[k] != v {
			t.Errorf("attach[%s] = %v, want %v", k, attach[k], v)
		}
	}
}

func TestAStackWithNoBundleFileRecordsNothing(t *testing.T) {
	home := t.TempDir()
	rec := manifest.BundleRecord{Name: "obs", Version: "0.1.0", Class: "hmd-stack-obs"}
	_, _, ok, err := bundleRecording(home, "dev", rec)
	if err != nil || ok {
		t.Errorf("ok = %v, err = %v; a local.stack repository has no bundle to record", ok, err)
	}
}

func TestABundleAddedFromAWorkingTreeIsNotARelease(t *testing.T) {
	home := t.TempDir()
	cacheBundle(t, home, "hmd-stack-analytics", "0.1.4", "analytics", analyticsBundleFile)
	rec := manifest.BundleRecord{Name: "analytics", Version: "0.1.4", Class: "hmd-stack-analytics"}
	_, attach, ok, err := bundleRecording(home, "dev", rec)
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if _, has := attach["release_version"]; has {
		t.Errorf("no digest means no published artifact, so no release: %v", attach)
	}
}
