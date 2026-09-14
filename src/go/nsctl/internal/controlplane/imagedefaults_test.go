package controlplane

import (
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
)

// Every image default has to name a registry that actually carries it.
//
// This is a spelling test on purpose. The defaults were once a single shared
// ${HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/neuronsphere}, and two of the three
// backend images had never been published there -- so a machine with nothing
// set, which is every fresh install, could not bring up a graph or a cluster
// and failed deep inside Floci rather than here. Asserting the literal strings
// is what makes re-unifying them a decision someone has to defend rather than a
// tidy-up that passes.
func TestComposeImageDefaultsNameThePublishingRegistry(t *testing.T) {
	t.Parallel()

	data, err := bundled.Read(bundled.ControlPlaneComposeFile)
	if err != nil {
		t.Fatal(err)
	}
	// No overrides bar HMD_HOME, which the bind mounts need a value for: the
	// image interpolations have to fall all the way through to their defaults,
	// which is the case under test.
	project, err := compose.Parse(data, "ns-test", func(k string) string {
		if k == "HMD_HOME" {
			return "/tmp/hmd"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}

	var floci *compose.Service
	for i := range project.Services {
		if project.Services[i].Key == "floci" {
			floci = &project.Services[i]
		}
	}
	if floci == nil {
		t.Fatal("the compose file has no floci service")
	}

	for _, tt := range []struct {
		key  string
		want string
	}{
		{"FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE", "ghcr.io/hmdlabs/hmd-postgres-base:"},
		{"FLOCI_SERVICES_NEPTUNE_DEFAULT_IMAGE", "ghcr.io/hmdlabs/hmd-img-gremlin-server:"},
		{"FLOCI_SERVICES_EKS_DEFAULT_IMAGE", "ghcr.io/hmdlabs/hmd-img-k3s-floci:"},
	} {
		got := floci.Environment[tt.key]
		if !strings.HasPrefix(got, tt.want) {
			t.Errorf("%s = %q, want it to start %q", tt.key, got, tt.want)
		}
		// `stable` and `latest` do not exist under hmdlabs for these, and a
		// floating tag makes "what was this built against" unanswerable.
		if strings.HasSuffix(got, ":stable") || strings.HasSuffix(got, ":latest") {
			t.Errorf("%s = %q, want an explicit version", tt.key, got)
		}
	}
}

// ConfiguredPostgresImage reads what Floci is actually handed, rather than
// rebuilding it from the environment -- the value sits behind two nested
// ${VAR:-default} expansions and a reconstruction that disagreed would check
// the wrong image.
func TestConfiguredPostgresImageReadsTheProject(t *testing.T) {
	t.Parallel()

	data, err := bundled.Read(bundled.ControlPlaneComposeFile)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HMD_HOME": "/tmp/hmd", "HMD_LOCAL_NS_CONTAINER_REGISTRY": "my.registry"}
	project, err := compose.Parse(data, "ns-test", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got := ConfiguredPostgresImage(project); !strings.HasPrefix(got, "my.registry/hmd-postgres-base:") {
		t.Errorf("ConfiguredPostgresImage = %q, want the override honoured", got)
	}
	if got := ConfiguredPostgresImage(nil); got != "" {
		t.Errorf("ConfiguredPostgresImage(nil) = %q, want empty", got)
	}
}

// The GUI pin and the published registry travel together; neither org tags this
// image `stable`, which is what the compose default used to ask for.
func TestGUIImageDefaultsAreExplicit(t *testing.T) {
	t.Parallel()

	if GUIPublishedRegistry != "ghcr.io/hmdlabs" {
		t.Errorf("GUIPublishedRegistry = %q, want ghcr.io/hmdlabs", GUIPublishedRegistry)
	}
	if PublishedRegistryDefault != "ghcr.io/hmdlabs" {
		t.Errorf("PublishedRegistryDefault = %q, want ghcr.io/hmdlabs", PublishedRegistryDefault)
	}
	for _, v := range []string{GUIImageVersion} {
		if v == "stable" || v == "latest" || v == "" {
			t.Errorf("version %q is not an explicit pin", v)
		}
	}
}
