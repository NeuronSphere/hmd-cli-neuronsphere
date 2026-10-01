package modelstore

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

func sample() ([]model.Observation, *model.Model) {
	id := model.ID{Namespace: "billing", Name: "aws_billing"}
	p := model.Provenance{Inspector: "t", Repo: "r", File: "f.yaml", Line: 3, Authority: model.AuthDDL}
	obs := []model.Observation{
		{Kind: model.KindManifestation, Subject: id, Provenance: p, Manifest: &model.ManifestObs{
			Key: "trino:billing_final.aws_billing", Tech: "trino-table", Layer: "final", Primary: true,
			Location: model.Location{Schema: "billing_final", Table: "aws_billing"}}},
		{Kind: model.KindAttribute, Subject: id, Provenance: p, Attr: &model.AttrObs{
			Name: "cost", Manifestation: "trino:billing_final.aws_billing", Position: 1, Type: model.Float, PhysicalType: "double"}},
		{Kind: model.KindReference, Provenance: p, Named: &model.NamedObs{Kind: "table", Key: "x.y"}},
	}
	return obs, model.Consolidate(obs)
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
	snap, err := s.Save(roots, map[string]string{"r": "abc"}, obs, m, time.Unix(100, 0))
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
	back, err := s.Observations(snap.ID)
	if err != nil || len(back) != len(obs) || back[1].Attr.PhysicalType != "double" {
		t.Errorf("observations = %+v, %v", back, err)
	}
	// The normalised columns are queryable.
	var typ string
	if err := s.db.QueryRow(`SELECT type FROM attribute WHERE noun_id = 'billing.aws_billing' AND name = 'cost'`).Scan(&typ); err != nil || typ != "float" {
		t.Errorf("attribute row = %q, %v", typ, err)
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
		if _, err := s.Save([]string{"/a"}, nil, obs, m, time.Unix(int64(i), 0)); err != nil {
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
	if _, err := s.Save([]string{"/a"}, nil, obs, m, time.Now()); err != nil {
		t.Errorf("save after rebuild: %v", err)
	}
}
