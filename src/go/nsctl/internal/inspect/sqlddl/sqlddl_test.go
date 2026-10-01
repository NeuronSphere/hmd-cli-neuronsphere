package sqlddl

import (
	"reflect"
	"testing"
)

// Shapes copied from hmd-config-billing-transforms and
// hmd-config-transform-reporting, after Jinja rendering.

func TestCreateTableWithPartitions(t *testing.T) {
	t.Parallel()
	sts := Parse(`
      CREATE TABLE IF NOT EXISTS ntc_staging.ntc_instances_export (
        identifier VARCHAR,
        created_at TIMESTAMP(3) NOT NULL,
        amount decimal(10, 2),
        export_date DATE,
        p_iso_date DATE,
        p_environment VARCHAR
      ) WITH (
        format = 'PARQUET',
        partitioned_by = ARRAY['p_iso_date', 'p_environment']
      )`)
	if len(sts) != 1 {
		t.Fatalf("statements = %d", len(sts))
	}
	st := sts[0]
	if st.Kind != CreateTable || st.Name.Schema() != "ntc_staging" || st.Name.Table() != "ntc_instances_export" {
		t.Fatalf("statement = %+v", st)
	}
	want := []Column{
		{"identifier", "VARCHAR", false, 3},
		{"created_at", "TIMESTAMP(3)", true, 4},
		{"amount", "decimal(10,2)", false, 5},
		{"export_date", "DATE", false, 6},
		{"p_iso_date", "DATE", false, 7},
		{"p_environment", "VARCHAR", false, 8},
	}
	if !reflect.DeepEqual(st.Columns, want) {
		t.Errorf("columns = %+v", st.Columns)
	}
	if st.With["format"] != "PARQUET" || !reflect.DeepEqual(st.Partitions, []string{"p_iso_date", "p_environment"}) {
		t.Errorf("with = %v partitions = %v", st.With, st.Partitions)
	}
}

func TestBillingStyleFirstColumnOnCreateLine(t *testing.T) {
	t.Parallel()
	st := Parse(`CREATE TABLE if not exists billing_staging.aws_billing ("identity_line_item_id" varchar ,
"line_item_unblended_rate" double ,
"year" varchar
) with (
  format = 'parquet',
  partitioned_by = ARRAY[ 'year', 'month' ]
)`)[0]
	if len(st.Columns) != 3 || st.Columns[0].Name != "identity_line_item_id" || st.Columns[1].Type != "double" {
		t.Fatalf("columns = %+v", st.Columns)
	}
	if !reflect.DeepEqual(st.Partitions, []string{"year", "month"}) {
		t.Errorf("partitions = %v", st.Partitions)
	}
}

func TestInsertSelectNames(t *testing.T) {
	t.Parallel()
	st := Parse(`insert into billing_staging.aws_billing (
  select
  "identity_line_item_id" varchar ,
  case
          when product_servicename = '' then line_item_line_item_type
          else product_servicename
        end product_servicename ,
  nid as identifier,
  coalesce(try(from_iso8601_timestamp(created_at)), null) as created_at,
  t.status,
  '{year}' as "year"
  from billing_source.aws_billing_{year}_{month} s
  join other.t on s.x = t.x
)`)[0]
	if st.Kind != Insert || st.Name.String() != "billing_staging.aws_billing" {
		t.Fatalf("statement = %+v", st)
	}
	var names []string
	for _, it := range st.Select {
		names = append(names, it.Name)
	}
	if want := []string{"varchar", "product_servicename", "identifier", "created_at", "status", "year"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v", names)
	}
	if !st.Select[0].TypeWordAlias || st.Select[0].Source != "identity_line_item_id" {
		t.Errorf("type-word alias not recognised: %+v", st.Select[0])
	}
	if st.Select[2].Source != "nid" || st.Select[2].TypeWordAlias {
		t.Errorf("item 3 = %+v", st.Select[2])
	}
	if len(st.From) != 2 || st.From[0].String() != "billing_source.aws_billing_{year}_{month}" || st.From[1].String() != "other.t" {
		t.Errorf("from = %v", st.From)
	}
}

func TestInsertWithColumnList(t *testing.T) {
	t.Parallel()
	st := Parse(`INSERT INTO s.t (a, "b") SELECT x, y FROM s.u`)[0]
	if !reflect.DeepEqual(st.InsertCols, []string{"a", "b"}) || len(st.Select) != 2 {
		t.Errorf("statement = %+v", st)
	}
}

func TestSchemaViewDropAndComments(t *testing.T) {
	t.Parallel()
	sts := Parse(`
-- create the schema
CREATE SCHEMA IF NOT EXISTS ntc_source WITH (location = '{bucket_name}/ntc/source/');
/* a view */
create or replace view billing_ux.aws_billing as select * from billing_final.aws_billing;
DROP TABLE IF EXISTS ntc_source.ntc_instances_export_{iso_date}_{environment};
GRANT SELECT ON x TO y`)
	if len(sts) != 4 {
		t.Fatalf("statements = %d", len(sts))
	}
	if sts[0].Kind != CreateSchema || sts[0].Name.String() != "ntc_source" || sts[0].With["location"] != "{bucket_name}/ntc/source/" {
		t.Errorf("schema = %+v", sts[0])
	}
	if sts[1].Kind != CreateView || sts[1].Name.String() != "billing_ux.aws_billing" ||
		len(sts[1].From) != 1 || sts[1].From[0].String() != "billing_final.aws_billing" || sts[1].Select[0].Name != "*" {
		t.Errorf("view = %+v", sts[1])
	}
	if sts[2].Kind != Drop || sts[2].Object != "TABLE" || !sts[2].IfExists ||
		sts[2].Name.Table() != "ntc_instances_export_{iso_date}_{environment}" || sts[2].Line != 6 {
		t.Errorf("drop = %+v", sts[2])
	}
	if sts[3].Kind != Other {
		t.Errorf("grant = %+v", sts[3])
	}
}

func TestFinalSelectSkipsCTEs(t *testing.T) {
	t.Parallel()
	st, ok := FinalSelect(`
WITH base AS (
    SELECT a, COUNT(*) AS n FROM s.t GROUP BY a
),
joined AS (SELECT b.a, b.n FROM base b)
SELECT
    __jinja__ AS transform_id,
    j.a,
    n instance_count
FROM joined j`)
	if !ok {
		t.Fatal("no select")
	}
	var names []string
	for _, it := range st.Select {
		names = append(names, it.Name)
	}
	if !reflect.DeepEqual(names, []string{"transform_id", "a", "instance_count"}) || st.From[0].String() != "joined" {
		t.Errorf("names = %v from = %v", names, st.From)
	}
	if _, ok := FinalSelect("-- nothing here"); ok {
		t.Error("found a select in a comment")
	}
}

func TestPlaceholderWithArgumentsStaysOneName(t *testing.T) {
	t.Parallel()
	st := Parse(`CREATE TABLE ntc_source.ntc_instances_export_{iso_date|replace(-,_)}_{environment} (nid VARCHAR)`)[0]
	if st.Name.Table() != "ntc_instances_export_{iso_date|replace(-,_)}_{environment}" || len(st.Columns) != 1 {
		t.Errorf("statement = %+v", st)
	}
}
