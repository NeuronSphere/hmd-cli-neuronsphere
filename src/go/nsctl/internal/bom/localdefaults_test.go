package bom

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// writeConfigLocal lays a repo working tree down with the given
// meta-data/config_local.json and returns the tree's path.
func writeConfigLocal(t *testing.T, body string) string {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(repo, "meta-data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config_local.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func lookupRepoHome(home string) manifest.Lookup {
	return func(key string) string {
		if key == "HMD_REPO_HOME" {
			return home
		}
		return ""
	}
}

// declaredLocal declares one instance of class from a working tree whose
// config_local.json is body, and returns the BOM entry nsctl would deploy.
func declaredLocal(t *testing.T, body string, config map[string]any) (Entry, []string) {
	t.Helper()
	repo := writeConfigLocal(t, body)
	var warnings []string
	entries := DeclaredWithLocalDefaults(
		Environment{DeploymentID: "local"},
		[]manifest.Repo{{
			InstanceName:          "hive-metastore",
			RepoClassName:         "hmd-inf-hive-metastore",
			Source:                &manifest.Source{Type: manifest.SourceLocal, Path: repo},
			InstanceConfiguration: config,
		}},
		lookupRepoHome(""),
		func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) },
	)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	return entries[0], warnings
}

func TestConfigLocalBecomesTheLocalDefaults(t *testing.T) {
	t.Parallel()

	entry, _ := declaredLocal(t, `{"platformSecretStore": "parameterStoreSecretStore", "replicaCount": 1}`, nil)

	want := map[string]any{"platformSecretStore": "parameterStoreSecretStore", "replicaCount": float64(1)}
	if !reflect.DeepEqual(entry.InstanceConfiguration, want) {
		t.Errorf("configuration = %v, want %v", entry.InstanceConfiguration, want)
	}
}

func TestIdentityAndWiringKeysAreStrippedBeforeTheMerge(t *testing.T) {
	t.Parallel()

	// What a repo wrote for a direct `hmd deploy --local`: it names the
	// instance and its dependencies itself. Merged, these would rename the
	// instance and replace the dependencies nsctl bound.
	entry, _ := declaredLocal(t, `{
		"instance_name": "hive",
		"deployment_id": "local",
		"dependencies": {"ext-secrets": {"instance_name": "hive"}},
		"details": {"hive": {"clusterSecretStore": {"name": "aws-secrets-manager"}}},
		"platformSecretStore": "parameterStoreSecretStore"
	}`, nil)

	want := map[string]any{"platformSecretStore": "parameterStoreSecretStore"}
	if !reflect.DeepEqual(entry.InstanceConfiguration, want) {
		t.Errorf("configuration = %v, want only %v: identity and wiring keys leaked through", entry.InstanceConfiguration, want)
	}
	if len(entry.Dependencies) != 0 {
		t.Errorf("dependencies = %v, want the declaration's own (none)", entry.Dependencies)
	}
}

func TestTheInstancesOwnConfigurationWinsAndMapsMergeDeeply(t *testing.T) {
	t.Parallel()

	entry, _ := declaredLocal(t, `{
		"platformSecretStore": "parameterStoreSecretStore",
		"resources": {"requests": {"cpu": "500m", "memory": "2Gi"}, "limits": {"cpu": "2"}}
	}`, map[string]any{
		"platformSecretStore": "clusterSecretStore",
		"resources":           map[string]any{"requests": map[string]any{"memory": "4Gi"}},
	})

	want := map[string]any{
		"platformSecretStore": "clusterSecretStore",
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "500m", "memory": "4Gi"},
			"limits":   map[string]any{"cpu": "2"},
		},
	}
	if !reflect.DeepEqual(entry.InstanceConfiguration, want) {
		t.Errorf("configuration = %v, want %v", entry.InstanceConfiguration, want)
	}
}

func TestMergingDoesNotMutateTheDeclaration(t *testing.T) {
	t.Parallel()

	declared := map[string]any{"resources": map[string]any{"requests": map[string]any{"memory": "4Gi"}}}
	declaredLocal(t, `{"resources": {"requests": {"cpu": "500m"}}}`, declared)

	if _, leaked := declared["resources"].(map[string]any)["requests"].(map[string]any)["cpu"]; leaked {
		t.Error("the manifest's own instance_configuration was modified by the merge")
	}
}

func TestNoConfigLocalLeavesTheDeclarationAlone(t *testing.T) {
	t.Parallel()

	repos := []manifest.Repo{
		{
			InstanceName:          "no-file",
			RepoClassName:         "hmd-ms-nofile",
			Source:                &manifest.Source{Type: manifest.SourceLocal, Path: t.TempDir()},
			InstanceConfiguration: map[string]any{"replicas": 2},
		},
		{
			InstanceName:  "artifact",
			RepoClassName: "hmd-ms-artifact",
			Source:        &manifest.Source{Type: manifest.SourceArtifact},
		},
	}
	var warnings []string
	entries := DeclaredWithLocalDefaults(Environment{DeploymentID: "local"}, repos, lookupRepoHome(""),
		func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) })

	if !reflect.DeepEqual(entries[0].InstanceConfiguration, map[string]any{"replicas": 2}) {
		t.Errorf("no-file configuration = %v, want the declaration's own", entries[0].InstanceConfiguration)
	}
	if len(entries[1].InstanceConfiguration) != 0 {
		t.Errorf("artifact configuration = %v, want none: an artifact has no working tree to read", entries[1].InstanceConfiguration)
	}
	if len(warnings) != 0 {
		t.Errorf("warned %v for an absent file; absent is normal", warnings)
	}
}

func TestAnUnreadableConfigLocalIsWarnedAboutNotSilentlyDropped(t *testing.T) {
	t.Parallel()

	entry, warnings := declaredLocal(t, `{not json`, map[string]any{"replicas": 2})

	if !reflect.DeepEqual(entry.InstanceConfiguration, map[string]any{"replicas": 2}) {
		t.Errorf("configuration = %v, want the declaration's own", entry.InstanceConfiguration)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "config_local.json") {
		t.Errorf("warnings = %v, want one naming config_local.json", warnings)
	}
}

func TestTheRepoIsFoundByConventionWhenNoSourcePathIsDeclared(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	tree := filepath.Join(home, "hmd-inf-hive-metastore", "meta-data")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "config_local.json"), []byte(`{"replicaCount": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := DeclaredWithLocalDefaults(Environment{DeploymentID: "local"},
		[]manifest.Repo{{InstanceName: "hive-metastore", RepoClassName: "hmd-inf-hive-metastore"}},
		lookupRepoHome(home), func(string, ...any) {})

	if got := entries[0].InstanceConfiguration["replicaCount"]; got != float64(1) {
		t.Errorf("replicaCount = %v, want 1 from $HMD_REPO_HOME/hmd-inf-hive-metastore", got)
	}
}
