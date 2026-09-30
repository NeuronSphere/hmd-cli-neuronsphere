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

// NERD031 SPEC003: a plugin installed from an artifact records the items it
// placed, and a declaration with none is one binary item named by its key.

func TestPluginItemsRoundTrip(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(`[plugin.hmd-cli-toolchain]
source  = "librarian:hmd-cli-toolchain"
version = "1.4.12"
digest  = "sha256:abc"

  [[plugin.hmd-cli-toolchain.item]]
  kind    = "command"
  runtime = "python"
  noun    = "hmd"
  summary = "The Python hmd toolset"
  path    = "/h/.cache/neuronsphere/installs/hmd-cli-toolchain@1.4.12/hmd"
  target  = "/h/.cache/neuronsphere/installs/hmd-cli-toolchain@1.4.12/hmd/env/bin/python"
  args    = ["-c", "launch"]

  [[plugin.hmd-cli-toolchain.item]]
  kind  = "agent-skills"
  paths = ["/u/.claude/skills/transform-author"]

  [[plugin.hmd-cli-toolchain.item]]
  kind  = "docs"
  title = "Design notes"
  path  = "/repos/design-notes"
  clone = true
`))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Plugins["hmd-cli-toolchain"]
	if len(p.Items) != 3 {
		t.Fatalf("items = %+v", p.Items)
	}
	cmds := p.Commands()
	if len(cmds) != 1 || cmds[0].Noun != "hmd" || cmds[0].Args[1] != "launch" {
		t.Errorf("Commands() = %+v", cmds)
	}
	if !p.Items[2].Clone {
		t.Error("a clone must be recorded as one, or remove would delete someone's working tree")
	}

	home := t.TempDir()
	if err := Save(home, noEnv, cfg); err != nil {
		t.Fatal(err)
	}
	back, err := Load(home, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Plugins["hmd-cli-toolchain"]; len(got.Items) != 3 || got.Items[0].Target != p.Items[0].Target {
		t.Errorf("round trip lost items: %+v", got.Items)
	}
}

func TestADeclarationWithoutItemsIsOneBinaryCommand(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte("[plugin.hello]\nsource = \"oci://x/hello\"\nversion = \"1.2.0\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	cmds := cfg.Plugins["hello"].Commands()
	if len(cmds) != 1 || cmds[0].Noun != "hello" || cmds[0].Runtime != RuntimeBinary {
		t.Errorf("Commands() = %+v", cmds)
	}
}

func TestPluginItemRefusals(t *testing.T) {
	t.Parallel()
	head := "[plugin.kit]\nsource = \"librarian:kit\"\nversion = \"1.0.0\"\n"
	cases := map[string]string{
		"unknown kind":         "[[plugin.kit.item]]\nkind = \"exec\"\n",
		"command without noun": "[[plugin.kit.item]]\nkind = \"command\"\nruntime = \"scripts\"\n",
		"reserved noun":        "[[plugin.kit.item]]\nkind = \"command\"\nruntime = \"binary\"\nnoun = \"env\"\n",
		"noun twice":           "[[plugin.kit.item]]\nkind = \"command\"\nruntime = \"binary\"\nnoun = \"a\"\n[[plugin.kit.item]]\nkind = \"command\"\nruntime = \"binary\"\nnoun = \"a\"\n",
		"unknown item key":     "[[plugin.kit.item]]\nkind = \"docs\"\nwhatever = 1\n",
	}
	for name, body := range cases {
		if _, err := Parse([]byte(head + body)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestNounOwnerSpansPlugins(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.SetPlugin(Plugin{Name: "hello", Source: "oci://x/hello", Version: "1"})
	cfg.SetPlugin(Plugin{Name: "kit", Source: "librarian:kit", Version: "1",
		Items: []PluginItem{{Kind: KindCommand, Runtime: RuntimeScripts, Noun: "ops"}}})
	if owner, ok := cfg.NounOwner("ops"); !ok || owner != "kit" {
		t.Errorf("ops owner = %q %v", owner, ok)
	}
	if owner, ok := cfg.NounOwner("hello"); !ok || owner != "hello" {
		t.Errorf("hello owner = %q %v", owner, ok)
	}
	if _, ok := cfg.NounOwner("kit"); ok {
		t.Error("a plugin with items answers only to its items' nouns, not its class name")
	}
}
