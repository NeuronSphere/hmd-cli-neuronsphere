package nsexport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// Fixtures are verbatim copies of the producers in hmd-tf-ntc-export and
// hmd-tf-cur-export, and of hmd-config-nsreporting-cit's entities.
func TestExportsLinkGraphNounsToContentTypes(t *testing.T) {
	t.Parallel()
	var srcs []inspect.Source
	for _, f := range []string{"ntc", "cur", "cit"} {
		dir := filepath.Join("testdata", f)
		srcs = append(srcs, inspect.Source{Root: dir, Repo: f, FS: os.DirFS(dir)})
	}
	obs, _ := inspect.Run(context.Background(), srcs, []inspect.Inspector{Inspector{}})
	m := model.Consolidate(obs)

	ntc := m.Noun(ContentTypeID("ntc_export_parquet"))
	if ntc == nil || len(ntc.Attributes) != 10 || ntc.Attributes[0].Name != "nid" || ntc.Attributes[9].Name != "_updated" {
		t.Fatalf("ntc export = %+v", ntc)
	}
	if ntc.Manifestations[0].Sources[0].Confidence != model.Evidence {
		t.Errorf("a producer read without parsing Python is evidence, not a decision")
	}
	var edge bool
	for _, e := range m.Lineage {
		if e.From == "hmd_lang_transform.transform_instance" && e.To == "librarian.ntc_export_parquet" && e.Via == "graph-export" {
			edge = true
		}
	}
	if !edge {
		t.Errorf("lineage = %+v", m.Lineage)
	}

	// The CUR producer uploads a content type no entity declares; the NTC
	// type is declared, with a mime type that is not parquet.
	var unresolved, mime []string
	for _, d := range m.Disagreements {
		switch d.Code {
		case "unresolved-reference":
			unresolved = append(unresolved, d.Subject)
		case "cit-mime-type":
			mime = append(mime, d.Subject)
		}
	}
	if strings.Join(unresolved, ",") != "cur_export_parquet" {
		t.Errorf("unresolved = %v", unresolved)
	}
	if strings.Join(mime, ",") != "librarian.ntc_export_parquet" {
		t.Errorf("mime = %v", mime)
	}
}
