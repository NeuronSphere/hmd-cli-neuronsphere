package modelstore

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

func sample() ([]model.Observation, *model.Model) {
	id := model.ID{Namespace: "billing", Name: "aws_billing"}
	p := model.Provenance{Inspector: "t", Repo: "r", File: "f.yaml", Line: 3, Authority: model.AuthDDL}
	obs := []model.Observation{
		{Kind: model.KindBinding, Subject: id, Provenance: p, Binding: &model.BindingObs{
			Perspective: "trino", Name: "final", Primary: true,
			Values: map[string]model.Value{"schema_name": model.V("billing_final"), "table_name": model.V("aws_billing")}}},
		{Kind: model.KindAttribute, Subject: id, Provenance: p, Attr: &model.AttrObs{
			Name: "cost", Binding: "trino:final", Position: 1, Type: model.Float,
			Values: map[string]model.Value{"datatype": {Value: "double", Definition: "DOUBLE"}}}},
		{Kind: model.KindReference, Provenance: p, Named: &model.NamedObs{Kind: "table", Key: "x.y"}},
	}
	return obs, model.Consolidate(obs)
}

func derived() []*perspective.Derivation {
	d := &perspective.Definition{Name: "trino", BindingKey: "layer", Origin: perspective.OriginDerived}
	d.Declare(perspective.Entity, "layer", perspective.Extension{ExtensionType: "enum"})
	return []*perspective.Derivation{{Definition: d, Status: perspective.StatusDerived,
		Evidence: []perspective.Evidence{{Item: "binding_key", Rule: "r", Support: 2}}}}
}

func TestSaveAndReadBack(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "model.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	obs, m := sample()
	roots := []string{"/b", "/a"}
	snap, err := s.Save(roots, map[string]string{"r": "abc"}, obs, m, derived(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := s.Snapshots(ScopeKey([]string{"/a", "/b"}), 5)
	if err != nil || len(snaps) != 1 || snaps[0].ID != snap.ID || snaps[0].Revisions["r"] != "abc" {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}
	got, err := s.Model(snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Errorf("model round trip differs\n%s\n%s", a, b)
	}
	dvs, err := s.Derivations(snap.ID)
	if err != nil || len(dvs) != 1 || dvs[0].Definition.BindingKey != "layer" || dvs[0].Evidence[0].Support != 2 ||
		dvs[0].Definition.Origin != perspective.OriginDerived {
		t.Errorf("derivations = %+v, %v", dvs, err)
	}
	back, err := s.Observations(snap.ID)
	if err != nil || len(back) != len(obs) || back[1].Attr.Values["datatype"].Definition != "DOUBLE" {
		t.Errorf("observations = %+v, %v", back, err)
	}
	// The normalised columns are queryable: the core type, and the
	// perspective value beside it.
	var typ, def string
	if err := s.db.QueryRow(`SELECT type FROM attribute WHERE noun_id = 'billing.aws_billing' AND name = 'cost'`).Scan(&typ); err != nil || typ != "float" {
		t.Errorf("attribute row = %q, %v", typ, err)
	}
	if err := s.db.QueryRow(`SELECT definition FROM perspective_value WHERE perspective = 'trino' AND binding = 'final'
		AND attribute = 'cost' AND key = 'datatype'`).Scan(&def); err != nil || def != "DOUBLE" {
		t.Errorf("perspective_value row = %q, %v", def, err)
	}
}

func TestSavePrunesToKeep(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "model.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	obs, m := sample()
	for i := 0; i < Keep+3; i++ {
		if _, err := s.Save([]string{"/a"}, nil, obs, m, nil, time.Unix(int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	snaps, _ := s.Snapshots(ScopeKey([]string{"/a"}), 100)
	if len(snaps) != Keep {
		t.Errorf("snapshots kept = %d", len(snaps))
	}
	var orphans int
	_ = s.db.QueryRow(`SELECT count(*) FROM noun WHERE snapshot_id NOT IN (SELECT id FROM snapshot)`).Scan(&orphans)
	if orphans != 0 {
		t.Errorf("pruning left %d noun rows behind", orphans)
	}
}

func TestUnknownSchemaVersionIsRebuilt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "model.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE snapshot(junk TEXT); PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	obs, m := sample()
	if _, err := s.Save([]string{"/a"}, nil, obs, m, nil, time.Now()); err != nil {
		t.Errorf("save after rebuild: %v", err)
	}
}

// Edits are a person's work: they survive the rebuild an unknown schema
// version causes, which drops every snapshot.
func TestEditsSurviveARebuild(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "model.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e := perspective.Edit{Perspective: "trino", Op: "rename-key", Key: "format", To: "storage_format"}
	if err := s.AddEdit("scope", e, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEdit("other", perspective.Edit{Perspective: "x", Op: "rename", To: "y"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Edits("scope")
	if err != nil || len(got) != 1 || got[0] != e {
		t.Errorf("edits = %+v, %v", got, err)
	}
}
