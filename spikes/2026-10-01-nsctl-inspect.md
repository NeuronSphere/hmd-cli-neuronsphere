# Spike report: `nsctl inspect`, data-model inspection and change detection

- **Date:** 2026-10-01
- **Branch:** `feat/nsctl-inspect-spike` (not pushed; a push to `main` tags a release)
- **Proposal:** [NERD032 Data Model Inspection](../docs/proposals/NERD032_Data_Model_Inspection.rst)

## Summary

`nsctl` can derive a useful logical model from the NeuronSphere reporting and billing corpus. It needs no input beyond the repositories, and it does so deterministically. It took 0.05 s over 13 repositories.

The model:

- is `.hms`-shaped, and round-trips every real `.hms` fixture unchanged;
- links one logical thing across five technologies:
  - the graph noun;
  - a Librarian parquet export;
  - three Trino layers;
  - a dbt source;
  - dbt staging, dims and facts, and views in another repository;
- surfaces 38 disagreements in the corpus, of which:
  - 1 is a schema defect nobody knew about: a generated Postgres view that cannot be created;
  - 19 are warnings;
  - 18 are informational.

Identities were stable under re-inspection, including the Jinja-templated table names. A semantic diff of three edits reported exactly those three changes, plus the disagreements each edit introduced. That second part is the change-verification signal the spike was looking for.

The architecture held up. The core has no NeuronSphere knowledge beyond `.hms` concepts. Five inspectors sit behind a three-method interface: `.hms`, NS transforms, generic dbt, NS exports, and a shared DDL-subset parser. Adding the fifth (`nsexport`) late in the spike changed nothing in the core.

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

Each commit is independently droppable. The model, inspect boundary and store do not depend on any inspector.

### Package layout

```
internal/model/                IR, observations, Consolidate, Diff, ParseHMS/ExportHMS
internal/inspect/              Inspector, Source, Run, Discover, Revision, Walk
internal/inspect/sqlddl/       CREATE SCHEMA|TABLE|VIEW, INSERT…SELECT, DROP, FinalSelect
internal/inspect/hms/          language packs                       (NS conventions)
internal/inspect/nstransform/  transform YAML + Trino SQL           (NS conventions)
internal/inspect/nsexport/     producers + content item types       (NS conventions)
internal/inspect/dbt/          any dbt project                      (generic)
internal/modelstore/           SQLite snapshots
cmd/inspect.go, inspect_diff.go
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
| attribute | logical and physical type, required, `enum_def`, description |
| manifestation | the physical binding |
| lineage | from, to, via |
| constraint | a test or constraint on an attribute |
| provide / reference | a named thing, which turns unresolved references into disagreements |
| binding | sets the schema of a dbt project's models |
| same_as | declares two identities one noun |
| finding | an inspector's local finding |

Authority runs `.hms` 100 > DDL 80 > INSERT…SELECT 60 > dbt YAML 50 > dbt SQL 40 > inferred 10. Confidence is `decided` or `evidence`.

## The IR, and how it differs from `.hms`

The IR is `.hms`: `namespace.name` identity, `metatype` noun|relationship, `ref_from`/`ref_to` as fully qualified names, and attributes with `type`, `description`, `required` and `enum_def`. Keys it does not interpret are kept verbatim, so every real `.hms` fixture exports back to a JSON-equal document. That covers 11 files, including the `int` and `boolean` aliases, the stray `enum` key, `adornments`, `business_id` and `advisory_lock` (`TestHMSRoundTrip`).

Extensions, each optional:

| Extension | Why the corpus needed it |
|---|---|
| Provenance on every element | `.hms` cannot say where a fact came from; every line of `--sources` output depends on it |
| Manifestations (tech, location, layer, format, partitions, template, per-column physical type) | The same noun is three Trino tables, a dbt source and a parquet export |
| Logical types `date`, `decimal` | `export_date DATE` and `p_iso_date DATE` in the NTC tables. `double` maps to `.hms` `float`; `decimal` was kept apart to stay exact (not exercised by this corpus). `uuid` was not needed. |
| Lineage edges | dbt `ref`/`source`, INSERT…SELECT, graph export, content-type consumption |
| Required as a tri-state | `.hms` `required`, SQL `NOT NULL` and dbt `not_null` are one concept: a value is always present. Absence of a constraint is not proof that nulls are allowed, so unset means *unknown*. `false` is recorded only when `.hms` states it. |

Export of a noun with an extension type refuses and names the attributes (`export_date (date)`), unless `--lossy` downgrades them to `string`.

The prompt listed `object`, `array`, `items` and `definition` as `.hms` keys in use. A parse of all 33 language packs (322 files) found none of them as attribute keys. Those hits came from JSON Schemas embedded under `schema` and from attributes literally named `type`.

## Results on the corpus

### NTC chain: one logical thing, five technologies

```
$ nsctl inspect <corpus> ntc_instances_export ntc_export_parquet --sources
librarian.ntc_export_parquet  (noun)
  ~ librarian-content librarian:ntc_export_parquet (parquet, primary)  hmd-tf-ntc-export/src/python/hmd_tf_ntc_export/hmd_tf_ntc_export.py:185
    nid  transform_name  transform_version  created_at  … _updated    (unknown type; from the Gremlin project())

ntc.ntc_instances_export  (noun)
  ~ dbt-source hive.ntc_final.ntc_instances_export (reference)  hmd-config-transform-reporting/…/models/schema.yml:8
  ~ trino-table ntc_final.ntc_instances_export (final, parquet, primary)  …/nsreporting_ddl03_create_ntc_final_table.yaml:12
  ~ trino-table ntc_source.ntc_instances_export_{iso_date|replace(-,_)}_{environment} (source, parquet)  …/nsreporting_01_create_ntc_source_table.yaml:13
  ~ trino-table ntc_staging.ntc_instances_export (staging, parquet)  …/nsreporting_ddl02_create_ntc_staging_table.yaml:12
  = ntc_final.ntc_instances_export: same physical table ntc_final.ntc_instances_export (…/models/schema.yml:8)
    identifier         string     VARCHAR
    created_at         timestamp  TIMESTAMP
    export_date        date       DATE       not an .hms type
    p_iso_date         date       DATE       partition, not an .hms type
    p_environment      string     VARCHAR    partition
    …

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

The **billing** chain becomes one noun, `billing.aws_billing`, with four manifestations (`source` templated, `staging`, `final` primary, `ux` view). It has 171 attributes: 146 string, 21 float and 4 timestamp, with `year` and `month` as partitions.

### Disagreements found (38)

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
| warning | constraint-unknown-attribute ×4 | `hmd-tf-transform-reporting` scaffold | Negative case: `not_null` and `unique` on `id` in a `select *` model |
| info | unused-run-param ×3 | billing `ddl_02a`, `ddl_02b`; reporting `05` (`bucket_name`) | Two known, **one new** |
| info | type-word-alias ×2 | billing `02`, `03` (167 and 169 items) | Known |
| info | undocumented-model ×6 | four reporting models, two views | Known |
| info | layer-type-change ×6 | NTC timestamps, string in source and timestamp after | Expected casts, shown as information |
| info | cit-mime-type | `ntc_export_parquet` is `text/plain` | Known |

The billing CREATE and INSERT…SELECT column lists agree on all 171 columns, so no column-count disagreement is reported for billing. This negative result was checked, not assumed.

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
7 model changes (snapshot #2 -> #3):

billing.aws_billing.line_item_unblended_rate
  type: string -> float
billing.aws_billing@billing_staging.aws_billing.cost_center
  added: string (varchar)
hmd_lang_nsreporting.environment.region
  added: string, not required
disagreement error billing_staging.aws_billing [column-count] …ddl_02a-billing-create-table-staging.yaml lists 172 columns, …02-billing-source-to-staging-aws_billing.yaml lists 171
  added
disagreement error hmd_lang_nsreporting.environment [view-attributes-differ] view environment_hmd_lang_nsreporting projects [type] but the schema declares [region type]
  added
disagreement info billing.aws_billing.line_item_unblended_rate [layer-type-change] float in final; string in source, staging
  added
disagreement warning hmd_lang_nsreporting.environment [packaged-copy-stale] packaged copy lists attributes [type:string], the schema [type:string region:string]
  added
```

The last four lines say the second and third edits are incomplete changes: a column the INSERT does not fill, and an attribute whose generated view and packaged copy were not regenerated. That is `verify` in embryo.

The first version of the diff matched disagreements including their line numbers. An unrelated `unused-run-param` finding moved down one line and showed as removed and re-added. This is fixed in `c541b66`.

## Per-repository detection

| Repository | Inspector | What it yielded | Accuracy and limits |
|---|---|---|---|
| hmd-lang-transform | hms | 9 nouns, 19 relationships, 28 generated views | All 28 schemas and views. Found the `created_at` view defect. |
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
| hmd-tf-transform-reporting | dbt | 2 scaffold models | Negative case confirmed. Their tests reference a `select *` column. |
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
   - The sources disagree in 38 places (table above).
4. **How well does `.hms` serve as the IR basis?** Well. Five additive extensions were needed. The round trip is exact for nouns learned from `.hms`. For other nouns it fails on `date` and `decimal` unless `--lossy` is given.
5. **Is observation → consolidation → model practical?** Yes. Consolidation is about 700 lines, order-independent (tested), and every merge records why.
6. **Can SQLite persist it?** Yes. One snapshot of the corpus is about 1.9 MB with full observations, and each scope keeps its newest 20 snapshots. `modernc.org/sqlite` is pure Go, so the `CGO_ENABLED=0` build still works. Binary size (unstripped) went from 64.7 MB to 71.4 MB, about 10%. The stripped build is 49.8 MB. Normalised tables plus JSON bodies were enough to answer every statistic in this report with `sqlite3`. A JSON-file store would have been adequate for the diff alone, but not for ad-hoc queries.
7. **Is provenance strong enough?** Yes, for every element: file, line, inspector, authority, confidence and reason, plus link reasons for merges. `--json` carries all of it, at 1.1 MB for the corpus.
8. **Are identities stable enough?** Yes, under re-inspection and under templating:
   - Runtime placeholders keep their filters, so `{iso_date|replace(-,_)}` is a different identity from `{iso_date}`. That difference is how the broken drop was caught.
   - Complex expressions get a readable label plus a digest (`{time.year~a7c1}`).
   - Identity of non-`.hms` nouns depends on the namespace rule. For example, a schema renamed from `ntc_*` to `nsr_*` would read as remove plus add.
9. **How much NS knowledge leaks into the core?** None beyond `.hms` concepts. The core does have generic notions that NS happens to use: Scope/Binding for dbt, Reference-only manifestations, and provide/reference kinds. All layer, Jinja, CIT and generated-view conventions live in the `hms`, `nstransform` and `nsexport` packages.
10. **What would an external inspector look like?** For plain Postgres DDL: a package that walks `*.sql`, calls `sqlddl.Parse`, and emits manifestations and attributes, about 80 lines with no core change. JSON Schema would follow the same pattern, mapping `type`/`format` through a small table to logical types.
11. **What metadata would help?**
    - `.hms` (or a `model` block in BACON) for reporting tables. This would make `ntc.ntc_instances_export` authoritative instead of DDL-derived.
    - `data_type` and tests on every dbt model.
    - A declared content type for each producer, which `cur_export_parquet` lacks.
    - A `layer` annotation, instead of relying on schema suffixes.
12. **Does this complement nsctl's role or create awkward coupling?** It complements it. The feature shares conventions (`--json`, `HMD_HOME/.cache`, detect vocabulary) but no runtime code with environment management. It writes nothing outside the store. The coupling to note is the binary-size cost of SQLite.
13. **Smallest next increment for change verification:** turn the three disagreements the diff surfaced into named verification rules that run over a diff:
    - a CREATE/INSERT column-count mismatch;
    - an `.hms` change without view and packaged-copy regeneration;
    - a type change in a non-primary layer with no downstream cast.

    Then a change is "complete" when `nsctl inspect diff --live` introduces no error-level disagreement. A good first CI candidate is `hmd-config-billing-transforms`.

## Limitations and open questions

- dbt column types are unknown. Getting them needs `data_type` in YAML or a catalog, and there is no live warehouse by design.
- A noun's attributes come from its primary layer. Non-primary layer differences show only as column-level diffs and `layer-type-change` info.
- Namespaces of non-`.hms` nouns come from Trino schemas and dbt project names. Whether a reporting table should instead be a `.hms` noun (`hmd_lang_nsreporting.ntc_instances_export`) is a modelling decision for the owners.
- The `nsexport` regexes match the shapes the two producers use today. Another producer would need either the same literal style or a declaration.
- `--sources` output is long. The text UX was deliberately not polished.
- Disagreements are not acknowledged or suppressed yet. A verification gate would need that.

## Verification performed

- Unit tests:
  - IR, consolidation (order-independent, layers, links, constraints, duplicates);
  - `.hms` round trip over 11 real fixtures;
  - `sqlddl`;
  - every inspector over verbatim corpus fixtures;
  - store (round trip, pruning, rebuild on version mismatch);
  - diff;
  - commands (`inspect`, `--json`, `--hms`, snapshots, the diff loop).
- Full suites:
  - `gofmt -l`: clean.
  - `go vet ./...`: clean.
  - `go test ./...` and `go test -race -count=1 ./...`: green. The known `internal/runner` TempDir flake did not occur.
  - `test/nsctl_cli.robot`: 55/55 against a `CGO_ENABLED=0` build, including two new `nerd032` contract tests.
  - The command reference is current.
- **No local platform deploy was done.** `inspect` touches no runtime path (no containers, no control plane, no Floci), so an `nsctl env start` round trip would exercise nothing this branch changed.
- Make targets were run as their parts: `make generate` fetches from the Librarian, so `generate-local` plus the already-fetched bundled repositories were used.

## Recommendation

Keep the model, inspect boundary, `hms` and `dbt` inspectors, `sqlddl` and the diff. Keep `nstransform` and `nsexport` as the NS-specific layer. Keep SQLite if the 10% binary-size cost is acceptable; otherwise swap in JSON snapshots behind the same `Save`/`Snapshots`/`Model` methods.

Next experiment: the verification rules in question 13, run in CI over `hmd-config-billing-transforms` and `hmd-lang-nsreporting` pull requests.
