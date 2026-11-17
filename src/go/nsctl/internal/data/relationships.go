package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// Relationship evidence (NERD039 SPEC004): for each column that could refer
// to another table's key, how many of its distinct values the key holds. It
// is evidence only; understand (SPEC007) decides what to ask about.

// RelationshipsFile is the evidence's name under profile/.
const RelationshipsFile = "_relationships.json"

// maxPairs bounds one relationships run. A folder of exports has a few
// tables; past this the pairs are dropped and the report says so.
const maxPairs = 2000

// Join is one candidate: From's values looked up in To, a key of its table.
type Join struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
	// FromDistinct is the distinct non-null values of From; Matched is how
	// many of them To holds.
	FromDistinct int64   `json:"from_distinct"`
	Matched      int64   `json:"matched"`
	Inclusion    float64 `json:"inclusion"`
	// Cardinality is many_to_one, or one_to_one when From is unique too.
	Cardinality string `json:"cardinality"`
	// NameMatch is true when the column names say the same thing: equal,
	// or From named after To's table.
	NameMatch bool `json:"name_match"`
}

// Relationships is profile/_relationships.json.
type Relationships struct {
	ProfiledAt string   `json:"profiled_at"`
	Tables     []string `json:"tables"`
	Joins      []Join   `json:"joins"`
	Truncated  bool     `json:"truncated,omitempty"`
}

// pair is a join to measure.
type pair struct {
	from, fromCol, to, toCol string
	fromUnique, nameMatch    bool
}

// typeFamily groups the types a join can compare: values are matched as
// text, so a string key and an integer column can meet.
func typeFamily(duck string) string {
	switch duck {
	case "VARCHAR", "UUID":
		return "text"
	case "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT",
		"UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT":
		return "integer"
	}
	return ""
}

// candidatePairs lists every column that could refer to another table's
// candidate key. It is pure.
func candidatePairs(profiles []*Profile) (pairs []pair, truncated bool) {
	for _, to := range profiles {
		for _, key := range to.Columns {
			if !key.CandidateKey || typeFamily(key.DuckType) == "" {
				continue
			}
			for _, from := range profiles {
				if from.Table == to.Table {
					continue
				}
				for _, c := range from.Columns {
					ff, kf := typeFamily(c.DuckType), typeFamily(key.DuckType)
					// A number never refers to a text key: "7" and "C007" do
					// not meet, and small integers overlap by chance.
					if ff == "" || (ff == "integer" && kf == "text") {
						continue
					}
					if len(pairs) == maxPairs {
						return pairs, true
					}
					pairs = append(pairs, pair{
						from: from.Table, fromCol: c.Name, to: to.Table, toCol: key.Name,
						fromUnique: c.CandidateKey,
						nameMatch:  namesMatch(c.Name, key.Name, to.Table),
					})
				}
			}
		}
	}
	return pairs, false
}

// namesMatch: customer_id and customer_id; cust_id or customer for a key of
// customers.
func namesMatch(from, key, toTable string) bool {
	from, key = strings.ToLower(from), strings.ToLower(key)
	if from == key {
		return true
	}
	stem := singular(strings.ToLower(toTable))
	return from == stem || strings.HasPrefix(from, stem+"_")
}

// singular is a plain English singular, good enough for table names.
func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "sses"), strings.HasSuffix(s, "xes"), strings.HasSuffix(s, "ches"), strings.HasSuffix(s, "shes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 1:
		return s[:len(s)-1]
	}
	return s
}

// pairSQL measures one pair as one row.
func pairSQL(p pair) string {
	f, fc, t, tc := ident(p.from), ident(p.fromCol), ident(p.to), ident(p.toCol)
	return fmt.Sprintf(`SELECT %s AS from_table, %s AS from_column, %s AS to_table, %s AS to_column,
  (SELECT count(DISTINCT CAST(%s AS VARCHAR)) FROM %s WHERE %s IS NOT NULL) AS from_distinct,
  (SELECT count(*) FROM (SELECT DISTINCT CAST(%s AS VARCHAR) AS v FROM %s WHERE %s IS NOT NULL) a
     SEMI JOIN (SELECT DISTINCT CAST(%s AS VARCHAR) AS v FROM %s) b USING (v)) AS matched`,
		lit(p.from), lit(p.fromCol), lit(p.to), lit(p.toCol),
		fc, f, fc,
		fc, f, fc,
		tc, t)
}

// ProfileRelationships measures every candidate join between the registered
// tables, profiling any table that has no saved profile first, and saves the
// evidence.
func (w *Workspace) ProfileRelationships(ctx context.Context, eng Engine) (*Relationships, error) {
	tables, err := w.Tables()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(w.Path(CatalogFile)); err != nil {
		return nil, nserr.New(nserr.Usage, "relationships are measured over the catalog; run `nsctl data register` first")
	}
	var profiles []*Profile
	rep := &Relationships{ProfiledAt: time.Now().UTC().Format(time.RFC3339)}
	for _, t := range tables {
		if !w.Has(t.Table, StepRegistered) {
			continue
		}
		p, err := w.SavedProfile(t.Table)
		if err != nil {
			if p, err = w.ProfileTable(ctx, eng, t.Table, false); err != nil {
				return nil, err
			}
		}
		profiles = append(profiles, p)
		rep.Tables = append(rep.Tables, t.Table)
	}
	pairs, truncated := candidatePairs(profiles)
	rep.Truncated = truncated
	rep.Joins = []Join{}
	if len(pairs) > 0 {
		parts := make([]string, len(pairs))
		for i, p := range pairs {
			parts[i] = pairSQL(p)
		}
		rows, _, err := w.Query(ctx, eng, strings.Join(parts, "\nUNION ALL\n"), false)
		if err != nil {
			return nil, err
		}
		byKey := map[string]pair{}
		for _, p := range pairs {
			byKey[p.from+"."+p.fromCol+"="+p.to+"."+p.toCol] = p
		}
		for _, r := range rows {
			j := Join{FromTable: str(r["from_table"]), FromColumn: str(r["from_column"]),
				ToTable: str(r["to_table"]), ToColumn: str(r["to_column"]),
				FromDistinct: num(r["from_distinct"]), Matched: num(r["matched"])}
			if j.Matched == 0 || j.FromDistinct == 0 {
				continue
			}
			p := byKey[j.ID()]
			j.Inclusion = float64(j.Matched) / float64(j.FromDistinct)
			j.NameMatch = p.nameMatch
			j.Cardinality = "many_to_one"
			if p.fromUnique {
				j.Cardinality = "one_to_one"
			}
			rep.Joins = append(rep.Joins, j)
		}
	}
	sort.Slice(rep.Joins, func(a, b int) bool {
		if rep.Joins[a].Inclusion != rep.Joins[b].Inclusion {
			return rep.Joins[a].Inclusion > rep.Joins[b].Inclusion
		}
		return rep.Joins[a].ID() < rep.Joins[b].ID()
	})
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFile(w.Path(ProfileDir, RelationshipsFile), b); err != nil {
		return nil, err
	}
	return rep, nil
}

// ID names the join: from_table.from_column=to_table.to_column.
func (j Join) ID() string {
	return j.FromTable + "." + j.FromColumn + "=" + j.ToTable + "." + j.ToColumn
}

// SavedProfile reads profile/<table>.json.
func (w *Workspace) SavedProfile(table string) (*Profile, error) {
	b, err := os.ReadFile(w.Path(ProfileDir, table+".json"))
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", w.Path(ProfileDir, table+".json"), err)
	}
	return &p, nil
}

// SavedRelationships reads profile/_relationships.json.
func (w *Workspace) SavedRelationships() (*Relationships, error) {
	b, err := os.ReadFile(w.Path(ProfileDir, RelationshipsFile))
	if err != nil {
		return nil, err
	}
	var r Relationships
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", w.Path(ProfileDir, RelationshipsFile), err)
	}
	return &r, nil
}
