package data

import (
	"strings"
	"testing"
)

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC004
func TestCandidatePairsLookOnlyAtOtherTablesKeys(t *testing.T) {
	t.Parallel()
	profiles := []*Profile{
		{Table: "customers", Columns: []ColumnProfile{
			{Name: "customer_id", DuckType: "VARCHAR", CandidateKey: true},
			{Name: "signup", DuckType: "DATE", CandidateKey: true},
		}},
		{Table: "orders", Columns: []ColumnProfile{
			{Name: "order_id", DuckType: "BIGINT", CandidateKey: true},
			{Name: "cust_id", DuckType: "VARCHAR"},
			{Name: "qty", DuckType: "BIGINT"},
			{Name: "amount", DuckType: "DOUBLE"},
		}},
	}
	pairs, truncated := candidatePairs(profiles)
	var got []string
	for _, p := range pairs {
		got = append(got, p.from+"."+p.fromCol+">"+p.to+"."+p.toCol)
	}
	// Not a date key, not a float, not a number looked up in a text key (7
	// is never C007); text may hold a numeric key, as leading-zero ids do.
	want := "orders.cust_id>customers.customer_id customers.customer_id>orders.order_id"
	if truncated || strings.Join(got, " ") != want {
		t.Errorf("pairs = %v\nwant %s", got, want)
	}
}

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC004
func TestNamesMatch(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		from, key, table string
		want             bool
	}{
		{"customer_id", "customer_id", "customers", true},
		{"customer", "id", "customers", true},
		{"customer_ref", "id", "customers", true},
		{"category_id", "id", "categories", true},
		{"amount", "id", "customers", false},
	} {
		if got := namesMatch(c.from, c.key, c.table); got != c.want {
			t.Errorf("namesMatch(%s, %s, %s) = %v", c.from, c.key, c.table, got)
		}
	}
}
