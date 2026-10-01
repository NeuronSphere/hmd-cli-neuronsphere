// Package modelstore keeps nsctl inspect's snapshots in a local SQLite
// database (NERD032 SPEC005): each snapshot's observations, the model
// consolidated from them and the perspectives derived for it (the
// perspective IR, NERD033 SPEC005), keyed by the set of inspected roots.
//
// Snapshots are derived state, like everything else under
// $HMD_HOME/.cache: deleting them loses only history, and a schema version
// the store does not recognise drops and rebuilds them rather than migrating.
// Their format is not a compatibility contract. Perspective edits are the
// exception: they are a person's work, kept across rebuilds in a table of
// their own whose shape does not change with the snapshot schema. Whole nouns are kept as JSON beside a few
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
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

// schemaVersion is PRAGMA user_version. Bump it with any table change; an
// older database is dropped and rebuilt.
const schemaVersion = 3

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
  required INTEGER
);
CREATE TABLE binding (
  snapshot_id INTEGER NOT NULL REFERENCES snapshot(id) ON DELETE CASCADE,
  noun_id TEXT NOT NULL,
  perspective TEXT NOT NULL,
  name TEXT NOT NULL,
  location TEXT NOT NULL
);
CREATE TABLE perspective_value (
  snapshot_id INTEGER NOT NULL REFERENCES snapshot(id) ON DELETE CASCADE,
  noun_id TEXT NOT NULL,
  perspective TEXT NOT NULL,
  binding TEXT NOT NULL,
  attribute TEXT NOT NULL,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  definition TEXT NOT NULL
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
CREATE TABLE perspective (
  snapshot_id INTEGER NOT NULL REFERENCES snapshot(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  status TEXT NOT NULL,
  definition TEXT NOT NULL,
  evidence TEXT NOT NULL
);
`

// editsDDL is the one table kept across schema versions.
const editsDDL = `
CREATE TABLE IF NOT EXISTS perspective_edit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope_key TEXT NOT NULL,
  created_at TEXT NOT NULL,
  body TEXT NOT NULL
);
`

// tables lists every table any schema version had, so a rebuild removes them all.
var tables = []string{"perspective", "disagreement", "lineage", "perspective_value", "binding", "manifestation", "attribute", "noun", "observation", "snapshot"}

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
		_, err := s.db.Exec(editsDDL)
		return err
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
	if _, err := tx.Exec(ddl + editsDDL); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// Save stores one inspection, with the perspectives derived for it, and
// prunes the scope to the newest Keep.
func (s *Store) Save(roots []string, revisions map[string]string, obs []model.Observation, m *model.Model,
	derived []*perspective.Derivation, now time.Time) (Snapshot, error) {
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
	for _, dv := range derived {
		ev, _ := json.Marshal(dv.Evidence)
		if _, err := tx.Exec(`INSERT INTO perspective VALUES (?, ?, ?, ?, ?)`, snap.ID, dv.Definition.Name, dv.Status,
			string(dv.Definition.Marshal()), string(ev)); err != nil {
			return snap, err
		}
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
	at, err := stmt(`INSERT INTO attribute VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer at.Close()
	bd, err := stmt(`INSERT INTO binding VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer bd.Close()
	pv, err := stmt(`INSERT INTO perspective_value VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer pv.Close()
	for _, n := range m.Nouns {
		body, _ := json.Marshal(n)
		nid := n.ID.String()
		if _, err := nn.Exec(id, nid, string(n.Metatype), n.Authoritative, string(body)); err != nil {
			return err
		}
		for _, a := range n.Attributes {
			var req any
			if a.Required != nil {
				req = *a.Required
			}
			if _, err := at.Exec(id, nid, a.Name, string(a.Type), req); err != nil {
				return err
			}
		}
		for _, b := range n.Bindings {
			if _, err := bd.Exec(id, nid, b.Perspective, b.Name, b.Location.String()); err != nil {
				return err
			}
			// One row per perspective value, entity-level (attribute "") and
			// attribute-level, so `where key = 'datatype' and value = 'date'`
			// answers "which columns are DATE" directly.
			put := func(attr string, values map[string]model.Value) error {
				for k, v := range values {
					if _, err := pv.Exec(id, nid, b.Perspective, b.Name, attr, k, v.String(), v.Definition); err != nil {
						return err
					}
				}
				return nil
			}
			if err := put("", b.Values); err != nil {
				return err
			}
			for _, c := range b.Columns {
				if err := put(c.Name, c.Values); err != nil {
					return err
				}
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

// Derivations reads back the perspectives derived for a snapshot.
func (s *Store) Derivations(id int64) ([]*perspective.Derivation, error) {
	rows, err := s.db.Query(`SELECT status, definition, evidence FROM perspective WHERE snapshot_id = ? ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*perspective.Derivation
	for rows.Next() {
		var status, def, ev string
		if err := rows.Scan(&status, &def, &ev); err != nil {
			return nil, err
		}
		d, err := perspective.Parse([]byte(def))
		if err != nil {
			return nil, err
		}
		d.Origin = perspective.OriginDerived
		dv := &perspective.Derivation{Definition: d, Status: status}
		_ = json.Unmarshal([]byte(ev), &dv.Evidence)
		out = append(out, dv)
	}
	return out, rows.Err()
}

// AddEdit records an edit to a scope's perspectives.
func (s *Store) AddEdit(scopeKey string, e perspective.Edit, now time.Time) error {
	body, _ := json.Marshal(e)
	_, err := s.db.Exec(`INSERT INTO perspective_edit(scope_key, created_at, body) VALUES (?, ?, ?)`,
		scopeKey, now.UTC().Format(time.RFC3339Nano), string(body))
	return err
}

// Edits returns a scope's edits in the order they were made.
func (s *Store) Edits(scopeKey string) ([]perspective.Edit, error) {
	rows, err := s.db.Query(`SELECT body FROM perspective_edit WHERE scope_key = ? ORDER BY id`, scopeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []perspective.Edit
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var e perspective.Edit
		if err := json.Unmarshal([]byte(b), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
