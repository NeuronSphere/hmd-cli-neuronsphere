# Spike report: `nsctl inspect`, data-model inspection and change detection

- **Date:** 2026-10-01
- **Branch:** `feat/nsctl-inspect-spike` (not pushed; a push to `main` tags a release)
- **Proposal:** [NERD032 Data Model Inspection](../docs/proposals/NERD032_Data_Model_Inspection.rst)

## Summary

`nsctl` can derive a useful logical model from the NeuronSphere reporting and billing corpus. It needs no input beyond the repositories, and it does so deterministically. It took 0.05 s over 13 repositories.

The model:

- **is plain `.hms`, not an extension of it.** It round-trips every real `.hms` fixture unchanged.
  - Everything `.hms` cannot say is a **perspective**: physical types, tables and views, dbt models, exports and nullability. Perspectives use the Modeler's definitions (`hmd-ms-mickey`), stored as `<name>.<perspective>.hms` sidecar files.
  - Exporting any inspected noun, then inspecting the export, gives back the same noun with the same perspective values.
- links one logical thing across five technologies:
  - the graph noun;
  - a Librarian parquet export;
  - three Trino layers;
  - a dbt source;
  - dbt staging, dims and facts, and views in another repository;
- surfaces 34 disagreements in the corpus, of which:
  - 1 is a schema defect nobody knew about: a generated Postgres view that cannot be created;
  - 15 are warnings;
  - 18 are informational.

Identities were stable under re-inspection, including the Jinja-templated table names. A semantic diff of three edits reported exactly those changes, as core changes and perspective-value changes, plus the disagreements each edit introduced. Those new disagreements are the change-verification signal the spike was looking for. A perspective sidecar that declares a type is checked against the DDL that realises it. That makes the first declared-vs-observed check in the system.

The architecture held up. The core has no NeuronSphere knowledge beyond `.hms` and the perspective value shape. Five inspectors sit behind a three-method interface: `.hms`, NS transforms, generic dbt, NS exports, and a shared DDL-subset parser. Adding the fifth (`nsexport`) late in the spike changed nothing in the core. Moving types and bindings into perspectives later changed the model's own types but no inspector boundary.

## Orientation

`nsctl repoclass describe --json` describes hmd-cli-neuronsphere as the local control plane and environment substrate. `nsctl` is the Go binary in `src/go/nsctl` (cobra, `cmd/root.go`), and the manifest carries no data-model signal.

The existing code had nothing for schemas, SQL, dbt or `.hms`, and no SQL driver. The relevant precedents were:

- **`internal/detect` (NERD009):** `Finding{Field, Confidence, Value, Where, Why}`. Its rule that an undecided result is an outcome, not an error, became the provenance and confidence vocabulary here.
- **`internal/reconcile`:** in nsctl, "reconcile" already means desired environment state against the deployment graph. This spike's merge step is therefore called **consolidate**, so the word keeps one meaning.
- **State location:** state lives under `$HMD_HOME/.cache/neuronsphere/…`. There is no per-repository `.nsctl` directory, so the store went there too.
- **Command conventions:** `newXCommand(opts)`, `--json`, `tabwriter`, `nserr` exit codes, `builtinNouns`, and the generated command reference.

## What was built

| Commit | What |
|---|---|
| `c0da889` docs | NERD032, written before any code |
| `99ea828` feat(model) | IR, observations, consolidation, `.hms` codec and round trip |
| `b62fd31` feat(inspect) | `Inspector`/`Source`/`Run`/`Discover`, git revision without `git`, `sqlddl` |
| `21052ae` feat(inspect) | `hms` inspector: schemas, generated Postgres views, packaged copies, ui display |
| `82f18dd` feat(inspect) | `nstransform`: transform YAML, Jinja rendering, layers, `dep_tf_name` |
| `2f2eb1a` feat(model) | reference-only manifestations: they link identities without providing a table |
| `de23df0` feat(inspect) | generic `dbt` inspector, `sqlddl.FinalSelect` |
| `6002f32` feat(modelstore) | SQLite snapshots (`modernc.org/sqlite`) |
| `8eeb33c` fix(inspect) | fixes from the first corpus run (below) |
| `238b37f` feat(cmd) | `nsctl inspect` |
| `89dfbd7` feat(model) | semantic diff, `nsctl inspect diff`, Robot contract tests |
| `c541b66` fix(model) | disagreement identity ignores line numbers |
| `b716c64` fix(inspect) | repositories named by directory; a mismatched manifest `name` is reported |
| `45d5a64` feat(inspect) | `nsexport`: graph noun → Librarian content type → consumer |
| `cd43efc` docs | first version of this report |
| `60fe665` docs | NERD032 amended: `.hms` stays unextended; types and bindings become perspectives (SPEC007) |
| `85501b5` refactor(inspect) | perspectives: definitions, sidecars, validation, semantic-id diff, store v2 |

Each commit before `85501b5` is independently droppable. `85501b5` replaces the "manifestation" concept and the `date`/`decimal` core types throughout, so it is the one commit to keep or drop as a whole. The model, inspect boundary and store do not depend on any inspector.

### Package layout

```
internal/model/                IR, observations, Consolidate, Diff, ParseHMS/ExportHMS, sidecar codec
internal/perspective/          perspective definitions (repositories only, none embedded), derivations, edits, validation
internal/derive/               perspectives derived from the files: identity, bindings, definitions, evidence (NERD033)
internal/inspect/              Inspector, Source, Run, Discover, Revision, Walk
internal/inspect/sqlddl/       CREATE SCHEMA|TABLE|VIEW, INSERT…SELECT, DROP, FinalSelect; SQL types (grammar facts only)
internal/inspect/hms/          language packs                       (NS conventions)
internal/inspect/nstransform/  transform YAML + Trino SQL           (NS conventions)
internal/inspect/nsexport/     producers + content item types       (NS conventions)
internal/inspect/dbt/          any dbt project                      (generic)
internal/modelstore/           SQLite snapshots
cmd/inspect.go, inspect_diff.go, inspect_perspective.go
```

### The inspector boundary

```go
type Inspector interface {
    Name() string
    CanInspect(ctx context.Context, src Source) bool
    Inspect(ctx context.Context, src Source) ([]model.Observation, error)
}
```

- The registry is one line in `cmd/inspect.go`.
- `Run` stamps repository, revision and inspector onto every observation.
- An inspector error becomes a finding, and the other sources are still inspected.

### Observations

An observation has a `Kind`, a subject `ID`, one typed payload and a `Provenance`. Provenance records the inspector, repo, revision, file, line, authority, confidence and a reason.

| Kind | Payload |
|---|---|
| noun | metatype, description, `ref_from`/`ref_to`, extra `.hms` keys |
| attribute | core `.hms` type, required, `enum_def`, description; or, under a binding, its attribute-level perspective values |
| binding | one perspective binding of a noun: perspective, binding name, entity-level values |
| lineage | from, to, via |
| constraint | a test or constraint on an attribute |
| provide / reference | a named thing, which turns unresolved references into disagreements |
| scope | sets the schema of a dbt project's models |
| same_as | declares two identities one noun |
| finding | an inspector's local finding |

Authority runs `.hms` 100 > DDL 80 > INSERT…SELECT 60 > dbt YAML 50 > dbt SQL 40 > inferred 10. Confidence is `decided` or `evidence`.

## The IR: `.hms` plus perspectives

The core is exactly `.hms`: `namespace.name` identity, `metatype` noun|relationship, `ref_from`/`ref_to` as fully qualified names, and attributes with `type`, `description`, `required` and `enum_def`. An attribute's `type` is only ever an `.hms` type. Keys it does not interpret are kept verbatim, so every real `.hms` fixture exports back to a JSON-equal document. That covers 11 files, including the `int` and `boolean` aliases, the stray `enum` key, `adornments`, `business_id` and `advisory_lock` (`TestHMSRoundTrip`).

### Design history: from extensions to perspectives

The first version of the spike *extended* the core in two ways. It added `date` and `decimal` as logical types, and a "manifestation" concept for physical bindings. Review pointed at prior art that already solves this: the Modeler's **perspectives**. These are `perspective_name` plus `entity_`, `noun_`, `relationship_` and `attribute_extensions` definitions (`hmd-ms-mickey/src/python/hmd_ms_mickey/model_perspectives.py`, served at `/apiop/perspectives/*`, edited by `hmd-app-modeler`). Three proposals already specify how perspective values are stored, but none implements it:

- `hmd-lib-ns-model` (spec, uncommitted);
- `hmd-lib-django-modeling` NERD003 SPEC007/008;
- `hmd-tmpl-modeler/docs/perspectives.rst`.

Values go in a sidecar `<name>.<perspective>.hms`, which `hmd-schema-loader` already merges into `extensions[<perspective>]` (as `.ui.hms` is today). Nothing writes or reads such sidecars yet. The `django-models` template is the only consumer of perspective values, and it reads them inline from the deprecated `.ns-model.json`.

The spike now follows that design (NERD032 SPEC007). It does not duplicate mickey.

| What `.hms` cannot say | Where it lives now |
|---|---|
| Exact physical type (`DATE`, `DECIMAL(10,2)`, `VARCHAR(255)`) | `datatype` value of the `trino` perspective, at `#attr@trino:<layer>`. The core type is the nearest `.hms` type, which the definition's enum value names with `hms_type` (`DATE` → `timestamp`, `DECIMAL` → `float`). |
| Where the noun lives (catalog, schema, table, template, format, partitions, external location) | entity-level values of a `trino`, `dbt`, `postgres-view` or `librarian-content` binding |
| Several tables of one noun (source / staging / final) | several **bindings** of one perspective, named by the definition's `binding_key` (`layer`) |
| Nullability, partition key | `is_nullable` and `is_partition` attribute values (mickey's `ansi-sql` has `is_nullable` too) |
| Provenance, lineage | not schema at all: they describe the inspection, so they stay in the IR and the store, never in a sidecar |
| Required | still the core tri-state: `.hms` `required`, `NOT NULL` and `not_null` set it; unset means unknown |

Additions to the Modeler's definition shape (both optional, ignored by a Modeler reader):

- `hms_type` on an enum value;
- `binding_key` on a definition.

Attribute-level values sit under the sidecar's `attributes` member. This settles the placement `hmd-lib-ns-model`'s spec leaves open: its loader merges a sidecar only at object level.

The four definitions were first embedded in `nsctl`. They are now test fixtures (`internal/perspective/testdata/spike-*.perspective.json`): `nsctl` embeds no perspective, and derives the ones no repository declares (see *Perspectives derived, not embedded* below). The `trino` datatype enum mirrored mickey's `ansi-sql` `datatype` entries, plus `hms_type`. A test parses mickey's real `ansi-sql` definition (converted to JSON) to prove the shape is the Modeler's.

Consequences:

- `--lossy` is now needed only for attributes no artifact types (dbt SQL columns). `DATE` no longer blocks a valid `.hms` export.
- `nsctl inspect <noun> --out <dir>` writes the core `.hms` file plus one sidecar per perspective. Inspecting that directory gives the same noun with the same perspective values (`TestInspectExportRoundTripsThroughSidecars`).
- A sidecar is read at `.hms` authority. A **declared** perspective value therefore outranks one inferred from DDL, and a contradiction between them is a disagreement (see *Declared perspectives against the DDL* below).
- Every value is validated against its definition, by `hmd-lib-ns-model`'s rules:
  - an unknown perspective is an error;
  - a key undeclared at its attach point is an error;
  - an enum value outside `enum_values` is an error;
  - an undeclared parameter is a warning.

  On the real corpus every inferred value validates: zero perspective disagreements.

The prompt listed `object`, `array`, `items` and `definition` as `.hms` keys in use. A parse of all 33 language packs (322 files) found none of them as attribute keys. Those hits came from JSON Schemas embedded under `schema` and from attributes literally named `type`.

## Results on the corpus

### NTC chain: one logical thing, five technologies

```
$ nsctl inspect <corpus> ntc_instances_export
ntc.ntc_instances_export  (noun)
  @dbt:source:ntc_final hive.ntc_final.ntc_instances_export (reference)
  @trino:final ntc_final.ntc_instances_export (table, parquet, primary)
  @trino:source ntc_source.ntc_instances_export_{iso_date|replace(-,_)}_{environment} (table, parquet)
  @trino:staging ntc_staging.ntc_instances_export (table, parquet)
    identifier         string     @trino:final VARCHAR
    transform_name     string     @trino:final VARCHAR
    created_at         timestamp  @trino:final TIMESTAMP
    status             string     @trino:final VARCHAR
    export_date        timestamp  @trino:final DATE
    p_iso_date         timestamp  @trino:final DATE       partition
    p_environment      string     @trino:final VARCHAR    partition
    …
```

The second column is the core `.hms` type, and the third is the `trino` perspective value it came from. `--sources` adds, per binding and attribute, the file and line plus the link reason: `= ntc_final.ntc_instances_export: same physical table … (models/schema.yml:8)`. The producer side (`librarian.ntc_export_parquet`, a `librarian-content` binding with the 10 projected columns) and the lineage:

```
Lineage:
  hmd_lang_transform.transform_instance  -> librarian.ntc_export_parquet   graph-export            hmd-tf-ntc-export/…/hmd_tf_ntc_export.py:138
  librarian.ntc_export_parquet           -> ntc.ntc_instances_export       librarian-content-type  …/nsreporting_01_create_ntc_source_table.yaml:13
  ntc.ntc_instances_export               -> hmd_config_transform_reporting.staging_transform_instance  dbt-source
  hmd_config_transform_reporting.staging_transform_instance -> …dim_transform | …dim_environment | …dim_status | …fact_transform_instance_count  dbt-ref
  hmd_config_transform_reporting.fact_transform_instance_count -> hmd_config_transform_reporting_views.monthly_success  dbt-source
```

Each link has a stated, deterministic reason:

- **Layers to one noun:** `ntc_<layer>` schemas fold into namespace `ntc`. This is an NS convention, applied in `nstransform`.
- **Trino table to dbt source:** they share the physical `schema.table`. Catalog is ignored.
- **Reporting project to the views repository:** the dbt transform's `run_params.schema: ntc` binds `dbt:hmd_config_transform_reporting` to schema `ntc`. The views repository's `source('ntc', …)` then resolves to the reporting project's models, across two repositories.
- **Graph noun to export to source table:** the Librarian content item type `ntc_export_parquet` is the join key. The producer uploads it; transform 01 queries `item_type: ntc_export_parquet` and creates an external table.

The **billing** chain becomes one noun, `billing.aws_billing`, with four `trino` bindings:

- `source`, whose name is templated;
- `staging`;
- `final`, which is primary;
- `ux`, a view.

It has 171 attributes: 146 string, 21 float and 4 timestamp, with `year` and `month` marked `is_partition` on the final binding.

The store answers perspective questions in plain SQL. The query below asks which columns are physically `DATE` or `DECIMAL`:

```
sqlite> select noun_id, binding, attribute, definition from perspective_value
        where perspective='trino' and key='datatype' and value in ('date','decimal');
ntc.ntc_instances_export|final|export_date|DATE
ntc.ntc_instances_export|final|p_iso_date|DATE
ntc.ntc_instances_export|staging|export_date|DATE
ntc.ntc_instances_export|staging|p_iso_date|DATE
```

### Disagreements found (34)

| Sev | Code | Subject | Status |
|---|---|---|---|
| error | view-duplicate-column | `hmd_lang_transform.transform_instance` | **New.** The generated view selects `created_at` twice: `content -> 'created_at'` and the entity table's own column. Postgres refuses to create it. |
| warning | no-generated-view ×2 | `hmd_lang_nsreporting.signal_type`, `.content_item_has_signal_type` | Known: DDL not regenerated |
| warning | packaged-copy-missing ×2 | the same two | **New:** `src/python/.../schemas` lacks them as well |
| warning | unresolved-reference | drop of `ntc_source.ntc_instances_export_{iso_date}_{environment}` | Known: the DROP lacks the create's `.replace('-','_')`; caught by comparing placeholder filters |
| warning | tf-name-mismatch ×2 | billing `01`, reporting `05` | Known |
| warning | unresolved-reference | content type `cur_export_parquet` | Known: no CIT declares it |
| warning | manifest-name-mismatch ×2 | `hmd-tf-ntc-export`, `hmd-tf-cur-export` | **New:** BACON `name` is still the template's `repo_name` |
| warning | ui-display-unknown | `hmd_lang_librarian.content_item` | Known: `display: title` |
| warning | runtime-type | `hmd_lang_transform.entity_query_run.status` | Known: `enum` instead of `enum_def`, so not enforced |
| warning | unresolved-reference ×3 | `hmd-config-pipeline-reporting` example | Known: the stray quote in `{{ ns_context['schema_name''] }}` renders as `{expr~63c5}`, and the example's tables and content type exist nowhere |
| info | unused-run-param ×3 | billing `ddl_02a`, `ddl_02b`; reporting `05` (`bucket_name`) | Two known, **one new** |
| info | type-word-alias ×2 | billing `02`, `03` (167 and 169 items) | Known |
| info | undocumented-model ×6 | four reporting models, two views | Known |
| info | layer-type-change ×6 | NTC timestamps, string in source and timestamp after | Expected casts, shown as information |
| info | cit-mime-type | `ntc_export_parquet` is `text/plain` | Known |

The billing CREATE and INSERT…SELECT column lists agree on all 171 columns, so no column-count disagreement is reported for billing. This negative result was checked, not assumed.

The first version reported four `constraint-unknown-attribute` warnings on the `hmd-tf-transform-reporting` scaffold: `not_null` and `unique` tests on `id` in a `select *` model. Those were false positives. Columns that `schema.yml` documents now attach to the model as `dbt` perspective values (`data_type`, `tests`), so `id` exists and its tests apply.

Not detected:

- **The implicit casts in NTC `02`:** `from_iso8601_timestamp` returns timestamp with time zone, and it is inserted into `TIMESTAMP`. Detecting this needs expression typing, which a DDL-subset parser rightly does not do.
- **The copy-pasted source descriptions in the views repository:** nothing compares descriptions.

### Change detection (scratch copies in `$TMPDIR`)

Two `--refresh` runs with no edits: `No model changes (snapshot #1 -> #2).` This includes the templated NTC source table and the billing table named by a `macros.dateutil` expression. Then three edits were made:

1. In billing `ddl_02b`, `line_item_unblended_rate` changed from `varchar` to `double`.
2. In billing `ddl_02a`, a `cost_center` column was added. It was **not** added to the `02` INSERT.
3. In `hmd_lang_nsreporting/environment.hms`, a `region` attribute was added.

```
$ nsctl inspect diff /tmp/…/scratch
8 model changes (snapshot #2 -> #3):

billing.aws_billing#line_item_unblended_rate
  PropertyTypeChanged: string -> float
billing.aws_billing#line_item_unblended_rate@trino:final
  PerspectiveValueChanged datatype: VARCHAR -> DOUBLE
billing.aws_billing#cost_center@trino:staging
  PerspectiveValueAdded datatype: VARCHAR
hmd_lang_nsreporting.environment#region
  PropertyAdded: string, not required
error billing_staging.aws_billing [column-count] billing/…/ddl_02a-billing-create-table-staging.yaml lists 172 columns, billing/…/02-billing-source-to-staging-aws_billing.yaml lists 171
  DisagreementAdded
error hmd_lang_nsreporting.environment [view-attributes-differ] view environment_hmd_lang_nsreporting projects [type] but the schema declares [region type]
  DisagreementAdded
info billing.aws_billing#line_item_unblended_rate [layer-type-change] float in trino:final; string in trino:source, trino:staging
  DisagreementAdded
warning hmd_lang_nsreporting.environment [packaged-copy-stale] packaged copy lists attributes [type:string], the schema [type:string region:string]
  DisagreementAdded
```

Change identities use `hmd-lib-ns-model`'s semantic ids, and kinds use its names. The type edit shows twice, once per layer of the model:

- **core:** `#line_item_unblended_rate` changed from `string` to `float`;
- **perspective:** its `trino:final` `datatype` changed from `VARCHAR` to `DOUBLE`.

A column added only to the staging table touches no core attribute, so it appears only as a perspective value. The four `DisagreementAdded` lines say the second and third edits are incomplete changes: a column the INSERT does not fill, and an attribute whose generated view and packaged copy were not regenerated. That is `verify` in embryo.

The first version of the diff matched disagreements including their line numbers. An unrelated `unused-run-param` finding moved down one line and showed as removed and re-added. This is fixed in `c541b66`.

### Declared perspectives against the DDL

The test of the perspective design was whether a perspective *declared* in a sidecar can be checked against the artifacts that realise it. The experiment:

1. Export `ntc.ntc_instances_export` from the real reporting repository with `--out`. This writes `ntc_instances_export.hms`, `.trino.hms` and `.dbt.hms` into a scratch language pack.
2. In the `trino` sidecar, declare the final layer's `export_date` as `VARCHAR`, and `status` as `text_blob`, which is not in the definition.
3. Inspect the scratch pack together with the unmodified reporting repository:

```
Disagreements: 1 error, 2 warning, 7 info
  error   ntc.ntc_instances_export#status@trino:final  [perspective-enum-value]
          trino datatype is "text_blob", which is not one of its enum_values
  warning ntc.ntc_instances_export#export_date@trino:final  [perspective-value-conflict]
          datatype is "date" in hmd-config-transform-reporting/src/transforms/nsreporting_ddl03_create_ntc_final_table.yaml:13 but "varchar" in scratch-lang/src/schemas/ntc/ntc_instances_export.trino.hms:1
  warning ntc.ntc_instances_export#status@trino:final  [perspective-value-conflict]
          …
```

The exported `.hms` makes the noun authoritative. The sidecar's declared values outrank what the DDL implies, and every contradiction names both files. This is the shape of the "declared model versus implementation" check that `verify` needs.

### Perspectives derived, not embedded (NERD033)

Embedding the definitions was not the whole problem. Each inspector was a hand-written reader for a perspective that existed only in Go: its name, its key names, which DDL slot fills which key, the `_source/_staging/_final/_ux` layer rule and the "final is primary" rule. NERD033 makes a perspective data only, which `nsctl` derives from the files that realise a model and never ships:

- **Parsers report neutral objects.** `nstransform` reports each table, view and `INSERT` target with its properties keyed by the SQL grammar's names (`format`, `partitioned_by`, `datatype`, ...), and a parsed type (base, arguments, SQL category). It names no perspective, namespace or layer.
- **`internal/derive` decides the rest**, from the whole corpus:
  - identity, from tables with the same name in schemas that differ only in their last segment;
  - the binding key and its order (`source -> staging -> final -> ux`), from `INSERT ... SELECT` lineage;
  - the primary binding (the most downstream one with declared columns);
  - each key's kind (bool, enum or text) from its values;
  - each type's `hms_type`, from pairing with `.hms` attributes, else the SQL category.

  Every piece carries evidence: the rule, the support, examples, exceptions and a review flag.
- **A declared definition wins.** If a repository has `src/perspectives/trino.perspective.json`, nothing is derived. Its `name_pattern` decides identity, and properties map through key names or `aliases`.
- **The IR is the store.** Derivations are kept per snapshot, and edits (`rename`, `rename-key`, `drop-key`, `hms-type`) are kept per scope, surviving store rebuilds. `nsctl inspect perspective materialise trino --to <repo>` writes the definition and one sidecar per noun into that repository only.
- **dbt, postgres-view and librarian-content** inspectors still name their bindings. Only their definitions are derived (from their values). Turning them into neutral parsers is the next step.

The test of the design: the model derived from the billing and reporting fixtures is **identical** to the one the hand-written inspector and the embedded definition produced. That covers every noun, binding, value, column type, lineage edge and disagreement (`TestDerivationReproducesTheSpikesModel`, against dumps frozen before the change). The derived `trino` definition agrees with the embedded one on every key, kind, enum value and `hms_type` the files mention (`TestDerivedTrinoAgreesWithTheSpikeDefinition`). Run over the real repositories, `nsctl inspect perspective derive` prints:

```
trino  (derived)
  binding key  layer: source -> staging -> final -> ux
  name pattern <namespace>_<layer>.<name>
  entity     format             enum parquet
  entity     table_type         enum table, view
  attribute  datatype           enum date→timestamp, double→float, timestamp→timestamp, varchar→string
  attribute  is_partition       bool
  ...
```

`catalog` and `is_nullable` are absent because no file in the corpus states them. A derived perspective describes what the files say, not what the technology could say.

## Per-repository detection

| Repository | Inspector | What it yielded | Accuracy and limits |
|---|---|---|---|
| hmd-lang-transform | hms | 9 nouns, 19 relationships, 28 `postgres-view` bindings | All 28 schemas and views. Found the `created_at` view defect. |
| hmd-lang-nsreporting | hms | 2 nouns, 2 relationships, 2 views | Complete. Two missing views and two missing packaged copies. |
| hmd-lang-librarian | hms | 9 nouns, 6 relationships | All 15 schemas. `ui.hms` is used only for the display check. |
| hmd-config-billing-transforms | nstransform | 1 noun, 4 layers, 171 attributes | Exact. Types and partitions come from rendered Jinja. Placeholder names are stable. |
| hmd-config-transform-reporting | nstransform, dbt | NTC noun (3 layers), 8 dbt models, binding, lineage | Exact for the DDL. dbt column types are unknown because SQL output columns are untyped. `not_null` makes dims' keys required. |
| hmd-config-transform-reporting-views | dbt | 2 view models, 6 sources | Sources resolve to the reporting models through the binding. View output columns are read from SQL. |
| hmd-tf-ntc-export | nsexport | export noun with 10 columns, `graph-export` lineage | Evidence-level. Literal Gremlin and assignment shapes, not Python parsing. |
| hmd-tf-cur-export | nsexport | content type reference | No columns: they come from CUR at runtime, so they are not discoverable. |
| hmd-config-nsreporting-cit | nsexport | `ntc_export_parquet` provided | Complete for this one entity. |
| hmd-config-transform-export | nstransform | 2 transform names | No model signal, as expected (image_sequence wiring only). |
| hmd-config-pipeline-reporting | nstransform | an example noun plus 3 unresolved references | Examples only. The broken Jinja is visible but not named as broken. |
| hmd-tf-transform-reporting | dbt | 2 scaffold models | Negative case confirmed: no model signal beyond the dbt init example. |
| hmd-ms-nsreporting-lib | (none) | nothing | No model artifacts. It consumes hmd-lang-nsreporting at runtime. |

## Answers to the spike's questions

1. **Can we derive a useful logical model?** Yes. The model has 64 nouns, every link is explained, and it found one real, previously unknown defect plus three other new findings.
2. **Which artifacts give the strongest signal?**
   - `.hms` is complete and authoritative.
   - Trino DDL in transforms is exact for columns, types and partitions once Jinja is rendered.
   - INSERT…SELECT is excellent for completeness checks and lineage, though weak on types.
   - dbt `schema.yml` is good where present, which is rare. dbt SQL gives lineage and column names but no types.
   - Python producers give evidence only.
   - CIT entities are join keys, not models.
   - BACON manifests give provenance only. They carry no model information, but they did contain a defect: the `repo_name` placeholder.
3. **What is deterministic, what is inferred, and what is undiscoverable?**
   - Deterministic: `.hms`, DDL, layer folding, dbt lineage, Jinja from literal run_params, and references.
   - Inferred (evidence): producer columns and content-type links.
   - Undiscoverable statically: CUR columns, dbt column types, and the values of runtime placeholders.
   - The sources disagree in 34 places (table above).
4. **How well does `.hms` serve as the IR basis?** Very well, once it is not extended.
   - The core model is exactly `.hms`, and the round trip is exact for every real fixture.
   - Physical types, bindings and nullability are perspective values in sidecars, following the Modeler's perspectives. Every inspected noun exports to valid `.hms` plus sidecars and reads back unchanged.
   - Only provenance and lineage sit outside both. They describe the inspection, not the schema.
   - Two optional keys were added to the Modeler's definition shape: `hms_type` and `binding_key`. The open placement of attribute-level sidecar values was settled.
5. **Is observation → consolidation → model practical?** Yes. Consolidation is about 700 lines, order-independent (tested), and every merge records why.
6. **Can SQLite persist it?** Yes. One snapshot of the corpus is about 1.8 MB with full observations, including a `perspective_value` table that answers "which columns are DATE" in one query, and each scope keeps its newest 20 snapshots. `modernc.org/sqlite` is pure Go, so the `CGO_ENABLED=0` build still works. Binary size (unstripped) went from 64.7 MB to 71.4 MB, about 10%. The stripped build is 49.8 MB. Normalised tables plus JSON bodies were enough to answer every statistic in this report with `sqlite3`. A JSON-file store would have been adequate for the diff alone, but not for ad-hoc queries.
7. **Is provenance strong enough?** Yes, for every element: file, line, inspector, authority, confidence and reason, plus link reasons for merges. `--json` carries all of it, at 1.1 MB for the corpus.
8. **Are identities stable enough?** Yes, under re-inspection and under templating:
   - Runtime placeholders keep their filters, so `{iso_date|replace(-,_)}` is a different identity from `{iso_date}`. That difference is how the broken drop was caught.
   - Complex expressions get a readable label plus a digest (`{time.year~a7c1}`).
   - Identity of non-`.hms` nouns depends on the namespace rule. For example, a schema renamed from `ntc_*` to `nsr_*` would read as remove plus add.
9. **How much NS knowledge leaks into the core?** None beyond `.hms` and the Modeler's perspective value shape.
   - The core does have generic notions that NS happens to use: scopes for dbt, reference-only bindings, provide/reference kinds, and the convention that `catalog`/`schema_name`/`table_name` values locate a binding.
   - What a perspective key means lives in its definition file, not in Go.
   - All layer, Jinja, CIT and generated-view conventions live in the `hms`, `nstransform` and `nsexport` packages.
10. **What would an external inspector look like?** For plain Postgres DDL:
    - a `postgres` perspective definition, a JSON file whose `datatype` enum names `hms_type`s;
    - a package that walks `*.sql`, calls `sqlddl.Parse` and `perspective.SQLDatatype`, and emits bindings and attributes. That is about 80 lines with no core change.

    JSON Schema would follow the same pattern with a `json-schema` perspective.
11. **What metadata would help?**
    - `.hms` plus perspective sidecars for reporting tables, which `nsctl inspect <noun> --out` now writes as a starting point. This would make `ntc.ntc_instances_export` authoritative, with every DDL change checked against the declared `trino` perspective.
    - `data_type` and tests on every dbt model.
    - A declared content type for each producer, which `cur_export_parquet` lacks.
    - A `layer` annotation, instead of relying on schema suffixes.
12. **Does this complement nsctl's role or create awkward coupling?** It complements it. The feature shares conventions (`--json`, `HMD_HOME/.cache`, detect vocabulary) but no runtime code with environment management. It writes nothing outside the store. The coupling to note is the binary-size cost of SQLite.
13. **Smallest next increment for change verification:** turn the three disagreements the diff surfaced into named verification rules that run over a diff:
    - a CREATE/INSERT column-count mismatch;
    - an `.hms` change without view and packaged-copy regeneration;
    - a type change in a non-primary layer with no downstream cast;
    - a declared perspective value the implementation contradicts (`perspective-value-conflict`). This already works.

    Then a change is "complete" when `nsctl inspect diff --live` introduces no error-level disagreement. A good first CI candidate is `hmd-config-billing-transforms`.

## Limitations and open questions

- dbt column types are unknown. Getting them needs `data_type` in YAML or a catalog, and there is no live warehouse by design.
- A noun's attributes come from its primary layer. Non-primary layer differences show only as column-level diffs and `layer-type-change` info.
- Namespaces of non-`.hms` nouns come from Trino schemas and dbt project names. Whether a reporting table should instead be a `.hms` noun (`hmd_lang_nsreporting.ntc_instances_export`) is a modelling decision for the owners.
- The `nsexport` regexes match the shapes the two producers use today. Another producer would need either the same literal style or a declaration.
- `--sources` output is long. The text UX was deliberately not polished.
- Disagreements are not acknowledged or suppressed yet. A verification gate would need that.
- **Perspectives are shared with the Modeler but not yet with its code.**
  - Definitions follow mickey's shape. `hms_type`, `binding_key`, `name_pattern`, parameter `position` and value and key `aliases` are additions to it, so mickey or `hmd-lib-ns-model` should adopt or reject them.
  - Mickey still serves its own Python-literal definitions. `hmd-lib-django-modeling` NERD003 SPEC007 proposes moving them to `src/perspectives/*.perspective.json`; once that lands, `nsctl` reads the same files, as it does any repository's.
- The per-binding `template` value is still a text string with `{placeholders}`. NERD033 SPEC004 wants it as structured `name_pattern` parts.
- A dropped key survives materialisation only as one information finding per property. A definition cannot yet say "ignore this property".
  - The `hmd-lib-ns-model` spec this design leans on is uncommitted in its repository.
- `hmd-schema-loader` merges a whole sidecar into `extensions[<perspective>]`, so the `attributes` member does not reach `attributes.<attr>.extensions`. That is harmless, but a loader that wants per-attribute placement must redistribute it.
- Validation of attribute values applies to every binding column, including columns a binding lists but the core noun does not have (an INSERT's select list). A stricter rule could reject sidecar values for attributes the core noun lacks.

## Verification performed

- Unit tests:
  - IR, consolidation (order-independent, layers, links, constraints, duplicates);
  - `.hms` round trip over 11 real fixtures;
  - `sqlddl`;
  - every inspector over verbatim corpus fixtures;
  - store (round trip, pruning, rebuild on version mismatch);
  - diff, including perspective values and parameters;
  - perspectives:
    - embedded definitions use the Modeler's shape, and mickey's real `ansi-sql` parses;
    - repository overrides;
    - SQL datatype values and core types;
    - validation rules;
    - sidecar export and read-back;
    - declared values outranking inferred ones;
  - commands (`inspect`, `--json`, `--hms`, `--out`, round trip through exported files, `inspect perspectives`, snapshots, the diff loop).
- Full suites:
  - `gofmt -l`: clean.
  - `go vet ./...`: clean.
  - `go test ./...` and `go test -race -count=1 ./...`: green. The known `internal/runner` TempDir flake did not occur.
  - `test/nsctl_cli.robot`: 55/55 against a `CGO_ENABLED=0` build, including two new `nerd032` contract tests.
  - The command reference is current.
- **No local platform deploy was done.** `inspect` touches no runtime path (no containers, no control plane, no Floci), so an `nsctl env start` round trip would exercise nothing this branch changed.
- Make targets were run as their parts: `make generate` fetches from the Librarian, so `generate-local` plus the already-fetched bundled repositories were used.

## Recommendation

What to keep:

- Keep the model, the perspective package, the inspect boundary, the `hms` and `dbt` inspectors, `sqlddl` and the diff.
- Keep `nstransform` and `nsexport` as the NS-specific layer.
- Keep SQLite if the 10% binary-size cost is acceptable. Otherwise swap in JSON snapshots behind the same `Save`/`Snapshots`/`Model` methods.
- Propose `hms_type` and `binding_key`, and the sidecar `attributes` placement, to the owners of `hmd-lib-ns-model` and `hmd-lib-django-modeling`. One shared set of `*.perspective.json` files should then serve mickey, the modeling app and `nsctl`.

Next experiment: the verification rules in question 13, run in CI over `hmd-config-billing-transforms` and `hmd-lang-nsreporting` pull requests. Seed it by committing an exported `.hms` plus `trino` sidecar for `billing.aws_billing`, so that DDL edits are checked against a declared perspective.
