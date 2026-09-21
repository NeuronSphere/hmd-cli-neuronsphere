package nsconfig

import (
	"strings"
	"testing"
)

// NERD018 SPEC001: [plugin.<noun>] tables in nsctl.toml, strictly parsed.

func TestPluginTablesParseUnderStrictDecoding(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(`default_profile = "acme"

[profile.acme]
auth_url = "https://auth.acme"
registry_url = "https://registry.acme"

[plugin.hello]
source  = "oci://ghcr.io/hmdlabs/plugins/hello"
version = "1.2.0"
digest  = "sha256:abc"

[plugin.scratch]
path = "/tmp/nsctl-scratch"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins) != 2 {
		t.Fatalf("Plugins = %v", cfg.Plugins)
	}
	hello := cfg.Plugins["hello"]
	if hello.Name != "hello" || hello.Source != "oci://ghcr.io/hmdlabs/plugins/hello" || hello.Version != "1.2.0" || hello.Digest != "sha256:abc" {
		t.Errorf("hello = %+v", hello)
	}
	if cfg.Plugins["scratch"].Path != "/tmp/nsctl-scratch" || !cfg.Plugins["scratch"].Dev() {
		t.Errorf("scratch = %+v", cfg.Plugins["scratch"])
	}
	if hello.Dev() {
		t.Error("a sourced plugin is not a dev build")
	}
	if got := cfg.PluginNames(); strings.Join(got, ",") != "hello,scratch" {
		t.Errorf("PluginNames = %v", got)
	}
	if cfg.Profiles["acme"].RegistryURL != "https://registry.acme" {
		t.Errorf("registry_url not parsed: %+v", cfg.Profiles["acme"])
	}
}

func TestPluginTableRefusals(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"unknown key":             "[plugin.hello]\nsource = \"oci://x/y\"\nversion = \"1\"\nsha = \"x\"\n",
		"bad noun":                "[plugin.Hello]\npath = \"/x\"\n",
		"noun with underscore":    "[plugin.my_thing]\npath = \"/x\"\n",
		"neither source nor path": "[plugin.hello]\nversion = \"1\"\n",
		"source without version":  "[plugin.hello]\nsource = \"oci://x/y\"\n",
	}
	for name, body := range cases {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: parsed, want refusal", name)
		}
	}
}

func TestSetAndRemovePluginRoundTrip(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.Set(Profile{Name: "acme", AuthURL: "https://auth.acme"})
	cfg.SetPlugin(Plugin{Name: "hello", Source: "oci://ghcr.io/hmdlabs/plugins/hello", Version: "1.0", Digest: "sha256:1"})
	cfg.SetPlugin(Plugin{Name: "hello", Source: "oci://ghcr.io/hmdlabs/plugins/hello", Version: "1.1", Digest: "sha256:2"})

	home := t.TempDir()
	lookup := func(string) string { return "" }
	if err := Save(home, lookup, cfg); err != nil {
		t.Fatal(err)
	}
	back, err := Load(home, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if back.Plugins["hello"].Version != "1.1" || back.Plugins["hello"].Name != "hello" {
		t.Errorf("plugin after round trip = %+v", back.Plugins["hello"])
	}
	if back.DefaultProfile != "acme" || back.Profiles["acme"].AuthURL != "https://auth.acme" {
		t.Errorf("profiles lost across a plugin write: %+v", back)
	}
	if !back.RemovePlugin("hello") || back.RemovePlugin("hello") {
		t.Error("RemovePlugin must report true once, then false")
	}
	if err := Save(home, lookup, back); err != nil {
		t.Fatal(err)
	}
	again, err := Load(home, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Plugins) != 0 {
		t.Errorf("Plugins after remove = %v", again.Plugins)
	}
}

func TestPluginNameGrammar(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"hello", "a", "my-thing", "x9"} {
		if err := ValidPluginName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Hello", "9x", "my_thing", "-x", "a b", "env"} {
		if err := ValidPluginName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
