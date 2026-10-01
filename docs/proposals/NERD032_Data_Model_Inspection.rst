.. NERD032 Data Model Inspection

NERD032 Data Model Inspection
=============================

.. req:: nsctl can inspect the logical data model a set of repositories describes
    :id: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    ``nsctl inspect`` shall read one or more repositories, find the logical
    data structures they describe (nouns, their attributes, the relationships
    between them, and the physical tables, views and exports that carry
    them), and present them as one model. Every element of that model shall
    say where it was learned. Where two artifacts disagree about the same
    element, the disagreement shall be reported, not resolved silently.

    The model shall be shaped like the HMD language pack schema (``.hms``)
    and extend it only where an artifact carries something ``.hms`` cannot
    say. A noun the model learned from ``.hms`` alone shall export back to a
    valid ``.hms`` document.

    Successive inspections shall be storable, so that ``nsctl inspect diff``
    can describe a change semantically ("this attribute changed type"), not
    as a list of changed files.

    This NERD is the proposal behind an exploratory spike. Its status stays
    ``proposed`` until the spike report (``spikes/2026-10-01-nsctl-inspect.md``)
    has been reviewed.

Motivation
----------

``nsctl`` provisions and runs local infrastructure for a NeuronSphere
project. The longer-term workflow it is meant to support is
**inspect → run → verify**: understand what a project's data looks like,
run it locally, and decide whether a proposed change to that data is complete
and compatible. ``run`` exists. Neither ``inspect`` nor ``verify`` does, and
``verify`` cannot exist without a model to verify against.

Nobody should have to write that model. NeuronSphere repositories already
describe their data, in several overlapping places:

* ``.hms`` schemas in every ``hmd-lang-*`` repository: the authoritative
  graph model (``hmd_lang_transform.transform_instance``).
* Trino DDL inside transform YAML (``hmd-config-billing-transforms``,
  ``hmd-config-transform-reporting``): ``CREATE TABLE`` column lists,
  ``INSERT … SELECT`` statements that move data between layers, partitioning.
* dbt projects: sources, models, ``ref()``/``source()`` lineage, and column
  tests (``not_null``, ``unique``).
* Python producers (``hmd-tf-ntc-export``) whose output columns are implied
  by a query.

The same logical thing appears in several of these at once. The NTC export
is ``transform_instance`` in the graph, a parquet file in the Librarian, three
Trino tables (source, staging, final) and a dbt source feeding staging, dims
and facts. They drift: the NTC drop transform names a table its create
transform never created, and ``hmd-lang-nsreporting`` declares nouns its
generated Postgres views do not cover. Detecting that drift is the first
thing a ``verify`` step would need.

The BACON manifest is not where the model lives. For this corpus it declares
``build: transform`` and a dependency on ``hmd-ms-transform``. It is
provenance and context, not a model source.

Scope and terminology
---------------------

*Observation*
    One fact one inspector saw in one file, with its provenance. Observations
    do not decide truth.

*Consolidation*
    Turning observations into the canonical model. The word is deliberately
    not *reconcile*, which in ``nsctl`` already means desired environment
    state against the deployment graph (:doc:`/explanation/reconciliation`).

*Manifestation*
    A physical binding of a logical noun: a Trino table, a dbt model, a
    Postgres view, a parquet export.

*Disagreement*
    Two observations about the same element that cannot both be true, or a
    reference to something no inspected artifact produces.

Out of scope: a universal catalog; replacing dbt, DataHub or OpenMetadata;
LLM or statistical inference; a user-facing schema language; generating
downstream artifacts; querying live Trino, Hive or Postgres; a general SQL
parser.

.. spec:: Inspectors emit observations through one small interface
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    An inspector is a Go type with ``Name()``, ``CanInspect(ctx, source)``
    and ``Inspect(ctx, source) ([]Observation, error)``. A *source* is one
    repository: its root, its name and its git revision, read from
    ``.git`` directly rather than by running ``git``.

    The set of inspectors is a list built by the command, not a global
    registry. Adding an inspector is one new package and one line in that
    list; no change to the model or the store.

    As with ``repoclass detect`` (NERD009 SPEC009), not being able to decide
    is a result, not an error. An inspector that finds nothing returns no
    observations. A file it cannot parse becomes a *finding* observation,
    not a failure of the whole inspection.

    The first inspectors are:

    * ``hms``: ``.hms`` schemas and the Postgres views generated from them.
    * ``nstransform``: NeuronSphere transform YAML, Trino SQL in
      ``TrinoOperator`` parameters, Jinja rendering from ``run_params``,
      layer naming and the ``dep_tf_name`` chain.
    * ``dbt``: any dbt project. Nothing in it is NeuronSphere-specific.

    A small DDL-subset parser (``CREATE SCHEMA|TABLE|VIEW``,
    ``INSERT … SELECT``, ``DROP``) is shared by inspectors. It is not a
    general SQL parser.

.. spec:: An observation carries provenance and authority
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    Every observation has a kind (noun, attribute, manifestation, lineage,
    constraint, finding), a subject identity, a payload, and a provenance:
    inspector, repository, revision, ``file:line``, an *authority*, a
    *confidence*, and a sentence saying why.

    Authority orders sources when they speak about the same element:

    ==========================  =========
    Source                      Authority
    ==========================  =========
    ``.hms``                    100
    ``CREATE TABLE``/``VIEW``   80
    ``INSERT … SELECT`` target  60
    dbt ``schema.yml``          50
    dbt model SQL               40
    inferred                    10
    ==========================  =========

    Confidence reuses the ``repoclass detect`` vocabulary: ``decided`` when
    the artifact states it, ``evidence`` when it was inferred.

.. spec:: The canonical model is .hms with named extensions
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    A noun has a ``namespace``, a ``name``, a ``metatype`` (``noun`` or
    ``relationship``), ``attributes`` keyed by name, and for a relationship
    ``ref_from``/``ref_to`` as fully qualified noun names: the ``.hms``
    shape. Attribute ``type``, ``description``, ``required`` and ``enum_def``
    carry their ``.hms`` meaning. The ``.hms`` aliases ``int``, ``boolean``
    and ``enum`` (for ``enum_def``) are normalised on read. Keys the model
    does not interpret (``adornments``, ``business_id``, ``advisory_lock``,
    ``schema``, ``id``, ``position``, the ``ui`` extension) are kept verbatim
    so that a noun learned from ``.hms`` exports back to an equal document.

    The model adds, and only adds:

    1. **Provenance** on every noun, attribute and manifestation. ``.hms``
       has no notion of where a fact came from.
    2. **Manifestations**: technology, physical location
       (catalog/schema/table), layer label, format, partition columns, the
       per-column physical type, and, for a name only known at run time, the
       template it was rendered from.
    3. **Two logical types**: ``date`` and ``decimal``. Physical types map
       to logical ones explicitly (``varchar``→``string``,
       ``double``/``real``→``float``, ``bigint``/``int``→``integer``,
       ``timestamp(n)``→``timestamp``, ``boolean``→``bool``). The physical
       type is always kept.
    4. **Lineage**: directed edges between nouns with the mechanism that
       produced them (``insert-select``, ``dbt-ref``, ``dbt-source``).
    5. **Required as a tri-state.** ``.hms`` ``required``, SQL ``NOT NULL``
       and a dbt ``not_null`` test all assert that a value is always present,
       so they set the same flag. ``false`` is recorded only when ``.hms``
       says so. Absent means *unknown*, because the absence of a constraint
       in DDL is not proof that nulls are allowed.

    Exporting a noun whose attributes use an extension type refuses and
    lists those attributes, unless asked to downgrade them.

.. spec:: Consolidation is deterministic and explains every link
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    Identity:

    * A ``.hms`` noun is ``namespace.name``.
    * Any other noun's namespace is assigned by its inspector, which records
      why. ``nstransform`` strips a layer suffix (``_source``, ``_staging``,
      ``_final``, ``_ux``) from the Trino schema, so
      ``billing_staging.aws_billing`` is the ``staging`` manifestation of
      ``billing.aws_billing``. ``dbt`` uses ``dbt_<project>.<model>``. Layer
      conventions live in the inspector, never in the model.

    Two manifestations belong to one noun when they share an identity, or
    the same physical ``schema.table`` (catalog ignored), or an inspector
    said so explicitly. Each merge records its reason.

    An attribute's logical type comes from its highest-authority
    observation. On a tie, the manifestation its inspector marked primary
    wins (``nstransform`` marks the final layer). Types that differ between
    layers are reported as information: casting between layers is the point
    of having layers.

    Disagreements are:

    * one manifestation described inconsistently (a ``CREATE TABLE`` and the
      ``INSERT`` into it list different column counts; a ``.hms`` noun and its
      generated view list different attributes);
    * a reference with no producer (a dbt source, a ``ref_to`` noun, a
      ``DROP`` target, a ``dep_tf_name``);
    * an inspector's own finding (an unused ``run_param``).

    Templated names: ``{{ ns_context['k'] }}`` is rendered from the
    transform's own ``run_params`` when the value is a literal (including
    ``.replace('-', '_')``). A value known only at run time becomes a
    ``{k}`` placeholder. A table name with placeholder segments is reduced to
    its stable stem and marked templated; the pattern is kept on the
    manifestation. This keeps identities stable across runs.

.. spec:: Inspections are snapshots in a local SQLite store
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    The store is ``$HMD_HOME/.cache/neuronsphere/inspect/model.db``, derived
    state like the rest of ``.cache``. It holds snapshots keyed by the set of
    inspected roots, each with its observations and the consolidated model.
    A schema version mismatch drops and rebuilds it; its format is not a
    compatibility contract. ``.hms`` export is a view of the model, never the
    store.

    The driver is ``modernc.org/sqlite``: ``nsctl`` builds with
    ``CGO_ENABLED=0``, which rules out ``mattn/go-sqlite3``.

    Without ``HMD_HOME`` inspection still works; nothing is stored and
    ``diff`` reports a usage error.

.. spec:: nsctl inspect and nsctl inspect diff
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    ``nsctl inspect [path|noun]...`` inspects the named directories (default
    the current one). A directory that is not itself a repository stands for
    the repositories directly inside it. An argument that is not a directory
    filters the output to matching nouns. ``--json`` prints the model;
    ``--sources`` adds provenance and link reasons; ``--refresh`` inspects
    again and stores a new snapshot (otherwise the latest snapshot for the
    same roots is shown, taking one first if none exists).

    ``nsctl inspect diff [path...]`` compares the latest two snapshots for
    those roots (``--live``: the latest against the working tree) and prints
    changes per noun and attribute: added, removed, type, required,
    manifestations.

    Reads print a table, or JSON under ``--json``, as every other read in
    ``nsctl`` does (NERD009). ``inspect`` writes nothing in any repository.
