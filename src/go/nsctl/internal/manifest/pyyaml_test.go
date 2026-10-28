package manifest

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// The Python CLI writes environment manifests with PyYAML, which follows YAML
// 1.1 and quotes a string only when 1.1 would read it as something else. An
// account id with an 8 or 9 in it is not 1.1 octal, so it is written bare --
// and yaml.v3, reading YAML 1.2, made it the number 8. Account ids must come
// back exactly as written.
func TestParseReadsPyYAMLStringsAsStrings(t *testing.T) {
	t.Parallel()

	m, err := Parse([]byte(`
version: 1
name: dev8
repos:
  - instance_name: ext-secrets
    repo_class_name: hmd-inf-ext-secrets
    instance_configuration:
      clusterSecretStore:
        localAccessKeyId: 000000000008
        localSecretAccessKey: 000000000009
      extraEnv:
        - name: AWS_SECRET_ACCESS_KEY
          value: 000000000019
      exponentWithoutDot: 1e3
      replicas: 2
      ratio: 0.5
      octal: 0755
      quoted: "000000000012"
      flag: true
`), ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.Repos[0].InstanceConfiguration
	store := cfg["clusterSecretStore"].(map[string]any)
	for key, want := range map[string]string{"localAccessKeyId": "000000000008", "localSecretAccessKey": "000000000009"} {
		if got, ok := store[key].(string); !ok || got != want {
			t.Errorf("%s = %#v, want the string %q", key, store[key], want)
		}
	}
	env := cfg["extraEnv"].([]any)[0].(map[string]any)
	if env["value"] != "000000000019" {
		t.Errorf("extraEnv value = %#v, want the string", env["value"])
	}
	if cfg["exponentWithoutDot"] != "1e3" {
		t.Errorf("1e3 = %#v; PyYAML's float needs a dot, so it wrote a string", cfg["exponentWithoutDot"])
	}
	if cfg["quoted"] != "000000000012" {
		t.Errorf("quoted = %#v", cfg["quoted"])
	}
	// What YAML 1.1 does read as a number or a bool still is one.
	if cfg["replicas"] != 2 || cfg["ratio"] != 0.5 || cfg["flag"] != true {
		t.Errorf("numbers and bools changed type: %#v %#v %#v", cfg["replicas"], cfg["ratio"], cfg["flag"])
	}
	if _, isString := cfg["octal"].(string); isString {
		t.Errorf("0755 is a 1.1 octal int and was read as a string")
	}
}

// What nsctl itself writes reads back unchanged: yaml.v3 quotes a numeric-
// looking string, so this only ever widens what reads as a string.
func TestParseRoundTripsWhatSaveWrites(t *testing.T) {
	t.Parallel()

	in := map[string]any{"account": "000000000008", "n": 8, "f": 2.5}
	data, err := yaml.Marshal(map[string]any{
		"version": 1, "name": "x",
		"repos": []any{map[string]any{"instance_name": "a", "repo_class_name": "b", "instance_configuration": in}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data, ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	got := m.Repos[0].InstanceConfiguration
	if got["account"] != "000000000008" || got["n"] != 8 || got["f"] != 2.5 {
		t.Errorf("round trip changed values: %#v", got)
	}
}
