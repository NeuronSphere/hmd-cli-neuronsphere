package hms

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

func run(t *testing.T, fixture string) (*model.Model, []model.Observation) {
	t.Helper()
	dir := filepath.Join("testdata", fixture)
	src := inspect.Source{Root: dir, Repo: fixture, FS: os.DirFS(dir)}
	ins := Inspector{}
	if !ins.CanInspect(context.Background(), src) {
		t.Fatalf("cannot inspect %s", fixture)
	}
	obs, reports := inspect.Run(context.Background(), []inspect.Source{src}, []inspect.Inspector{ins})
	if reports[0].Error != "" {
		t.Fatal(reports[0].Error)
	}
	return model.Consolidate(obs), obs
}

func codes(m *model.Model) map[string][]string {
	out := map[string][]string{}
	for _, d := range m.Disagreements {
		out[d.Code] = append(out[d.Code], d.Subject)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

// The real hmd-lang-nsreporting: signal_type and its relationship were added
// without regenerating the Postgres views or the packaged copies.
func TestNSReportingLanguagePack(t *testing.T) {
	t.Parallel()
	m, _ := run(t, "nsreporting")
	if len(m.Nouns) != 4 {
		t.Fatalf("nouns = %d", len(m.Nouns))
	}
	env := m.Noun(model.ID{Namespace: "hmd_lang_nsreporting", Name: "environment"})
	if !env.Authoritative || len(env.Attributes) != 1 || env.Attributes[0].Name != "type" {
		t.Fatalf("environment = %+v", env)
	}
	if len(env.Manifestations) != 1 || env.Manifestations[0].Tech != "postgres-view" ||
		env.Manifestations[0].Location.Table != "environment_hmd_lang_nsreporting" {
		t.Errorf("manifestations = %+v", env.Manifestations)
	}
	rel := m.Noun(model.ID{Namespace: "hmd_lang_nsreporting", Name: "content_item_has_environment"})
	if rel.Metatype != model.MetaRelationship || rel.RefFrom != "hmd_lang_librarian.content_item" {
		t.Errorf("relationship = %+v", rel)
	}

	got := codes(m)
	want := []string{"hmd_lang_nsreporting.content_item_has_signal_type", "hmd_lang_nsreporting.signal_type"}
	for _, code := range []string{"no-generated-view", "packaged-copy-missing"} {
		if len(got[code]) != 2 || got[code][0] != want[0] || got[code][1] != want[1] {
			t.Errorf("%s = %v, want %v", code, got[code], want)
		}
	}
	// The views that exist match their schemas.
	if len(got["view-attributes-differ"]) != 0 {
		t.Errorf("view-attributes-differ = %v", got["view-attributes-differ"])
	}
}

// The real transform_instance view selects created_at twice: the .hms
// attribute out of content, and the entity table's own column. Postgres
// refuses such a view.
func TestViewSelectingAColumnTwice(t *testing.T) {
	t.Parallel()
	m, _ := run(t, "transform")
	got := codes(m)
	if len(got["view-duplicate-column"]) != 1 || got["view-duplicate-column"][0] != "hmd_lang_transform.transform_instance" {
		t.Errorf("view-duplicate-column = %v", got["view-duplicate-column"])
	}
	// Every attribute is projected, so the attribute sets agree.
	if len(got["view-attributes-differ"]) != 0 {
		t.Errorf("view-attributes-differ = %v", m.Disagreements)
	}
	// A relationship has no attributes of its own, whatever its view selects.
	rel := m.Noun(model.ID{Namespace: "hmd_lang_transform", Name: "transform_has_transform_version"})
	if rel == nil || len(rel.Attributes) != 0 || len(rel.Manifestations) != 1 {
		t.Errorf("relationship = %+v", rel)
	}
}

func TestUIDisplayNamingNoAttribute(t *testing.T) {
	t.Parallel()
	m, _ := run(t, "librarian")
	got := codes(m)["ui-display-unknown"]
	if len(got) != 1 || got[0] != "hmd_lang_librarian.content_item" {
		t.Errorf("ui-display-unknown = %v (all: %v)", got, m.Disagreements)
	}
}

func TestPathMismatchAndRuntimeTypes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/schemas/ns/wrong.hms", `{"name":"right","namespace":"ns","metatype":"noun","attributes":{}}`)
	write("src/schemas/ns/typed.hms", `{"name":"typed","namespace":"ns","metatype":"noun","attributes":{
		"x":{"type":"int"},"y":{"type":"string","enum":["a"]}}}`)
	write("src/schemas/ns/broken.hms", `{"name":`)
	src := inspect.Source{Root: dir, Repo: "r", FS: os.DirFS(dir)}
	obs, _ := inspect.Run(context.Background(), []inspect.Source{src}, []inspect.Inspector{Inspector{}})
	got := codes(model.Consolidate(obs))
	if len(got["schema-path-mismatch"]) != 1 || len(got["runtime-type"]) != 2 || len(got["unparseable-schema"]) != 1 {
		t.Errorf("codes = %v", got)
	}
}
