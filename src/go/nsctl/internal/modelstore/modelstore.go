// Package modelstore keeps nsctl inspect's snapshots in a local SQLite
// database (NERD032 SPEC005): each snapshot's observations and the model
// consolidated from them, keyed by the set of inspected roots.
//
// The store is derived state, like everything else under
// $HMD_HOME/.cache: deleting it loses only history, and a schema version it
// does not recognise is dropped and rebuilt rather than migrated. Its format
// is not a compatibility contract. Whole nouns are kept as JSON beside a few
// normalised columns, which is enough to query it with sqlite3 and to read a
// model back without a mapping layer.
package modelstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	// Pure Go: nsctl builds with CGO_ENABLED=0.
	_ "modernc.org/sqlite"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// schemaVersion is PRAGMA user_version. Bump it with any table change; an
// older database is dropped and rebuilt.
const schemaVersion = 1

// Keep is how many snapshots per scope survive a save.
const Keep = 20

// Path is the store's location under HMD_HOME.
func Path(home string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "inspect", "model.db")
}

// Store is an open database.
type Store struct {
	db *sql.DB
}

// Snapshot describes one stored inspection.
type Snapshot struct {
	ID        int64             `json:"id"`
	ScopeKey  string            `json:"scope_key"`
	Roots     []string          `json:"roots"`
	CreatedAt time.Time         `json:"created_at"`
	Revisions map[string]string `json:"revisions,omitempty"`
}

// ScopeKey identifies a set of inspected roots, independent of order.
func ScopeKey(roots []string) string {
	r := append([]string(nil), roots...)
	sort.Strings(r)
	sum := sha256.Sum256([]byte(strings.Join(r, "\n")))
	return hex.EncodeToString(sum[:8])
}

const ddl = `
CREATE TABLE snapshot (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope_key TEXT NOT NULL,
  roots TEXT NOT NULL,
  created_at TEXT NOT NULL,
  revisions TEXT NOT NULL
);
CREATE INDEX snapshot_scope ON snapshot(scope_key, id);
CREATE TABLE observation (
  snapshot_id INTEGER NOT NULL REFERENCES snapshot(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL,
  subject TEXT NOT NULL,
  inspector TEXT NOT NULL,
  repo TEXT NOT NULL,
  file TEXT NOT NULL,
  line INTEGER NOT NULL,
  authority INTEGER NOT NULL,
  body TEXT NOT NULL
);
CREATE TABLE noun (
  snapshot_id INTEGER NOT NULL REFERENCES snapshot(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  metatype TEXT NOT NULL,
  authoritative INTEGER NOT NULL,
  body TEXT NOT NULL
);
CREATE TABLE attribute (
  snapshot_id INTEGER NOT NULL REFERENCES snapshot(id) ON DELETE CASCADE,
  noun_id TEXT NOT NULL,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  physical_type TEXT NOT NULL,
  required INTEGER,
  partition_key INTEGER NOT NULL
);
CREATE TABLE manifestation (
  snapshot_id INTEGER NOT NULL REFERENCES snapshot(id) ON DELETE CASCADE,
  noun_id TEXT NOT NULL,
  key TEXT NOT NULL,
  tech TEXT NOT NULL,
  location TEXT NOT NULL,
  layer TEXT NOT NULL
);
CREATE TABLE lineage (
  snapshot_id INTEGER NOT NULL REFERENCES snapshot(id) ON DELETE CASCADE,
  from_id TEXT NOT NULL,
  to_id TEXT NOT NULL,
  via TEXT NOT NULL,
  body TEXT NOT NULL
);
CREATE TABLE disagreement (
  snapshot_id INTEGER NOT NULL REFERENCES snapshot(id) ON DELETE CASCADE,
  severity TEXT NOT NULL,
  code TEXT NOT NULL,
  subject TEXT NOT NULL,
  body TEXT NOT NULL
);
`

var tables = []string{"disagreement", "lineage", "manifestation", "attribute", "noun", "observation", "snapshot"}

// Open opens or creates the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("model store %s: %w", path, err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v == schemaVersion {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range tables {
		if _, err := tx.Exec("DROP TABLE IF EXISTS " + t); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ddl); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// Save stores one inspection and prunes the scope to the newest Keep.
func (s *Store) Save(roots []string, revisions map[string]string, obs []model.Observation, m *model.Model, now time.Time) (Snapshot, error) {
	snap := Snapshot{ScopeKey: ScopeKey(roots), Roots: roots, CreatedAt: now.UTC(), Revisions: revisions}
	tx, err := s.db.Begin()
	if err != nil {
		return snap, err
	}
	defer tx.Rollback()
	rootsJSON, _ := json.Marshal(roots)
	revJSON, _ := json.Marshal(revisions)
	res, err := tx.Exec(`INSERT INTO snapshot(scope_key, roots, created_at, revisions) VALUES (?, ?, ?, ?)`,
		snap.ScopeKey, string(rootsJSON), snap.CreatedAt.Format(time.RFC3339Nano), string(revJSON))
	if err != nil {
		return snap, err
	}
	if snap.ID, err = res.LastInsertId(); err != nil {
		return snap, err
	}
	if err := insertAll(tx, snap.ID, obs, m); err != nil {
		return snap, err
	}
	if _, err := tx.Exec(`DELETE FROM snapshot WHERE scope_key = ? AND id NOT IN
		(SELECT id FROM snapshot WHERE scope_key = ? ORDER BY id DESC LIMIT ?)`, snap.ScopeKey, snap.ScopeKey, Keep); err != nil {
		return snap, err
	}
	return snap, tx.Commit()
}

func insertAll(tx *sql.Tx, id int64, obs []model.Observation, m *model.Model) error {
	stmt := func(q string) (*sql.Stmt, error) { return tx.Prepare(q) }
	ob, err := stmt(`INSERT INTO observation VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ob.Close()
	for i, o := range obs {
		body, _ := json.Marshal(o)
		p := o.Provenance
		if _, err := ob.Exec(id, i, string(o.Kind), o.Subject.String(), p.Inspector, p.Repo, p.File, p.Line, int(p.Authority), string(body)); err != nil {
			return err
		}
	}
	nn, err := stmt(`INSERT INTO noun VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer nn.Close()
	at, err := stmt(`INSERT INTO attribute VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer at.Close()
	mf, err := stmt(`INSERT INTO manifestation VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer mf.Close()
	for _, n := range m.Nouns {
		body, _ := json.Marshal(n)
		if _, err := nn.Exec(id, n.ID.String(), string(n.Metatype), n.Authoritative, string(body)); err != nil {
			return err
		}
		for _, a := range n.Attributes {
			var req any
			if a.Required != nil {
				req = *a.Required
			}
			if _, err := at.Exec(id, n.ID.String(), a.Name, string(a.Type), a.PhysicalType, req, a.Partition); err != nil {
				return err
			}
		}
		for _, x := range n.Manifestations {
			if _, err := mf.Exec(id, n.ID.String(), x.Key, x.Tech, x.Location.String(), x.Layer); err != nil {
				return err
			}
		}
	}
	for _, e := range m.Lineage {
		body, _ := json.Marshal(e)
		if _, err := tx.Exec(`INSERT INTO lineage VALUES (?, ?, ?, ?, ?)`, id, e.From, e.To, e.Via, string(body)); err != nil {
			return err
		}
	}
	for _, d := range m.Disagreements {
		body, _ := json.Marshal(d)
		if _, err := tx.Exec(`INSERT INTO disagreement VALUES (?, ?, ?, ?, ?)`, id, string(d.Severity), d.Code, d.Subject, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// Snapshots returns a scope's snapshots, newest first, at most limit.
func (s *Store) Snapshots(scopeKey string, limit int) ([]Snapshot, error) {
	rows, err := s.db.Query(`SELECT id, scope_key, roots, created_at, revisions FROM snapshot
		WHERE scope_key = ? ORDER BY id DESC LIMIT ?`, scopeKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var snap Snapshot
		var roots, created, revs string
		if err := rows.Scan(&snap.ID, &snap.ScopeKey, &roots, &created, &revs); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(roots), &snap.Roots)
		_ = json.Unmarshal([]byte(revs), &snap.Revisions)
		snap.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, snap)
	}
	return out, rows.Err()
}

// Model reads a snapshot's model back.
func (s *Store) Model(id int64) (*model.Model, error) {
	m := &model.Model{}
	if err := s.scanJSON(`SELECT body FROM noun WHERE snapshot_id = ? ORDER BY id`, id, func(b []byte) error {
		var n model.Noun
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		m.Nouns = append(m.Nouns, &n)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.scanJSON(`SELECT body FROM lineage WHERE snapshot_id = ? ORDER BY rowid`, id, func(b []byte) error {
		var e model.Edge
		if err := json.Unmarshal(b, &e); err != nil {
			return err
		}
		m.Lineage = append(m.Lineage, e)
		return nil
	}); err != nil {
		return nil, err
	}
	err := s.scanJSON(`SELECT body FROM disagreement WHERE snapshot_id = ? ORDER BY rowid`, id, func(b []byte) error {
		var d model.Disagreement
		if err := json.Unmarshal(b, &d); err != nil {
			return err
		}
		m.Disagreements = append(m.Disagreements, d)
		return nil
	})
	return m, err
}

// Observations reads a snapshot's observations back, in stored order.
func (s *Store) Observations(id int64) ([]model.Observation, error) {
	var out []model.Observation
	err := s.scanJSON(`SELECT body FROM observation WHERE snapshot_id = ? ORDER BY seq`, id, func(b []byte) error {
		var o model.Observation
		if err := json.Unmarshal(b, &o); err != nil {
			return err
		}
		out = append(out, o)
		return nil
	})
	return out, err
}

func (s *Store) scanJSON(q string, id int64, fn func([]byte) error) error {
	rows, err := s.db.Query(q, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return err
		}
		if err := fn(b); err != nil {
			return err
		}
	}
	return rows.Err()
}
