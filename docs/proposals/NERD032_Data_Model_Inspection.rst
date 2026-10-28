.. NERD032 Data Model Inspection

NERD032 Data Model Inspection
=============================

.. req:: nsctl can inspect the logical data model a set of repositories describes
    :id: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    ``nsctl model inspect`` shall read one or more repositories, find the logical
    data structures they describe (nouns, their attributes, the relationships
    between them, and the physical tables, views and exports that carry
    them), and present them as one model. Every element of that model shall
    say where it was learned. Where two artifacts disagree about the same
    element, the disagreement shall be reported, not resolved silently.

    The model shall be shaped like the HMD language pack schema (``.hms``)
    and extend it only where an artifact carries something ``.hms`` cannot
    say. A noun the model learned from ``.hms`` alone shall export back to a
    valid ``.hms`` document.

    Successive inspections shall be storable, so that ``nsctl model diff``
    can describe a change semantically ("this attribute changed type"), not
    as a list of changed files.

    ``nsctl inspect`` shall run the ``inspect`` verb of every noun that has
    one (``repoclass``, ``instance``, ``model``) over the same repositories,
    and report one list of findings in one shape: what each repository is,
    where its repo classes are instantiated, and the data model they describe.

    ``nsctl`` shall keep the model of a whole workspace current in its store,
    with no setup beyond naming the repositories, answer questions about it by
    command (find, where, related, impact), and export it as a documented
    interchange document that tools outside ``nsctl`` consume.

    Given a change rather than an element, ``nsctl model impact`` shall say
    what the change did to the model, what that reaches in other repositories,
    and which disagreements it introduced, so the checks a change needs can be
    named before it merges.

    ``nsctl`` shall remember which repositories depend on which, including
    repositories the engineer does not have checked out. A repository's
    manifest shall be able to say which others a change across a seam needs.
    Neither shall be something a person has to keep up to date by hand.
    The repository graph shall be shareable through the control plane, and
    buildable in CI or from a code host without cloning repositories onto an
    engineer's machine.

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

*Perspective*
    Technology-specific metadata about a noun, defined by a perspective
    definition and stored beside the core ``.hms`` schema (SPEC007), as in
    the Modeler. ``trino``, ``dbt``, ``postgres-view`` and
    ``librarian-content`` are the perspectives this NERD needs.

*Binding*
    One physical realisation of a noun within a perspective: the final Trino
    table, the staging Trino table, a dbt model. A perspective whose
    definition names a ``binding_key`` allows several per noun.

*Disagreement*
    Two observations about the same element that cannot both be true, or a
    reference to something no inspected artifact produces.

Out of scope: a universal catalog; replacing dbt or a data catalog;
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
    * ``nsexport``: export producers (a Gremlin ``hasLabel(...).project(...)``
      uploaded under a literal Librarian content item type) and content item
      type entities. The content type is the join key from a graph noun to
      the transform that reads its export. Everything it reports is
      evidence, not a decision: it does not parse Python.

    A source is named by its directory, which is what a person opens. A BACON
    manifest whose ``name`` differs is reported, not used.

    A small DDL-subset parser (``CREATE SCHEMA|TABLE|VIEW``,
    ``INSERT … SELECT``, ``DROP``) is shared by inspectors. It is not a
    general SQL parser.

.. spec:: An observation carries provenance and authority
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    Every observation has a kind (noun, attribute, binding, lineage,
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

.. spec:: The canonical model is .hms, and everything else is a perspective
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    A noun has a ``namespace``, a ``name``, a ``metatype`` (``noun`` or
    ``relationship``), ``attributes`` keyed by name, and for a relationship
    ``ref_from``/``ref_to`` as fully qualified noun names: the ``.hms``
    shape. Attribute ``type``, ``description``, ``required`` and ``enum_def``
    carry their ``.hms`` meaning, and ``type`` is only ever an ``.hms`` type.
    The ``.hms`` aliases ``int``, ``boolean`` and ``enum`` (for ``enum_def``)
    are normalised on read. Keys the model does not interpret
    (``adornments``, ``business_id``, ``advisory_lock``, ``schema``, ``id``,
    ``position``, the ``ui`` extension) are kept verbatim so that a noun
    learned from ``.hms`` exports back to an equal document.

    The core schema language is not extended. What ``.hms`` cannot say about
    a noun is a **perspective** (SPEC007): technology-specific metadata kept
    beside the core schema, not inside it, following the perspectives of the
    Modeler (``hmd-ms-mickey``, ``hmd-tmpl-modeler``) and the sidecar layout
    proposed by ``hmd-lib-ns-model`` and ``hmd-lib-django-modeling`` NERD003.
    The first version of this spike added ``date`` and ``decimal`` as core
    logical types and "manifestations" as a model concept; both are now
    perspective values:

    * **Physical types.** An attribute's core type is the nearest ``.hms``
      type (``DATE`` → ``timestamp``, ``DECIMAL(10,2)`` → ``float``,
      ``VARCHAR`` → ``string``), chosen by the perspective definition's enum
      value (its ``hms_type``). The exact type is the perspective value
      ``datatype``: ``{"value": "date", "definition": "DATE"}``. Every noun
      therefore exports to valid ``.hms``, and nothing is lost.
    * **Physical bindings.** Where a noun lives (catalog, schema, table,
      format, partitions, external location, the run-time template of its
      name) are entity-level perspective values; per-column facts (type,
      nullability, partition key) are attribute-level values.
    * **Nullability.** ``is_nullable`` is a perspective value, as in the
      Modeler's ``ansi-sql`` perspective. The core ``required`` stays a
      tri-state: ``.hms`` ``required``, SQL ``NOT NULL`` and a dbt
      ``not_null`` test all set it, ``false`` is recorded only when ``.hms``
      says so, and absent means *unknown*.

    Two things the model carries are not schema at all and stay out of both
    the core and the perspectives: **provenance** (where each fact was
    learned) and **lineage** (which noun is derived from which, and how).
    They describe an inspection, not a noun, and live in the IR and the
    snapshot store only.

.. spec:: Perspective definitions and perspective sidecar files
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    A **perspective definition** uses the Modeler's shape unchanged:
    ``perspective_name``, ``perspective_display``, ``graph_display`` and
    ``entity_extensions``, ``noun_extensions``, ``relationship_extensions``,
    ``attribute_extensions``, each an array of single-key objects whose value
    has ``display``, ``description``, ``extension_type`` (``short_text``,
    ``bool``, ``enum``), ``default`` and, for an enum, ``enum_values`` of
    ``{id, description, definition, parameters}``. Two optional keys are
    added, both ignored by a reader that does not know them:

    * ``hms_type`` on an enum value: the core ``.hms`` type an attribute with
      that value has.
    * ``binding_key`` on the definition: the entity extension that names one
      of several bindings of a noun in the same technology. A noun with a
      source, a staging and a final Trino table has three bindings of the
      ``trino`` perspective, told apart by ``layer``. A definition without it
      allows one binding per noun.

    Definitions are files, ``src/perspectives/<name>.perspective.json`` (the
    layout ``hmd-lib-django-modeling`` NERD003 SPEC007 proposes). ``nsctl``
    embeds none: it reads them from the inspected repositories, or derives
    them from the files that realise a model and holds them until they are
    materialised into a repository (NERD033). The spike's embedded ``trino``,
    ``dbt``, ``postgres-view`` and ``librarian-content`` definitions are
    superseded by NERD033 SPEC001. ``nsctl`` does not copy the Modeler's
    ``ansi-sql`` or ``django-models``.

    **Perspective values** of a noun live in a sidecar file
    ``<name>.<perspective>.hms`` beside ``<name>.hms``, which
    ``hmd-schema-loader`` already merges into the noun's
    ``extensions[<perspective>]`` (as it does for ``.ui.hms``). A value has
    the Modeler's shape, ``{"value": ..., "definition": ..., "parameters":
    {...}}``. The sidecar file is::

        {
          "namespace": "ntc",
          "name": "ntc_instances_export",
          "bindings": [
            {
              "layer": {"value": "final"},
              "schema_name": {"value": "ntc_final"},
              "table_name": {"value": "ntc_instances_export"},
              "attributes": {
                "export_date": {"datatype": {"value": "date", "definition": "DATE"}}
              }
            }
          ]
        }

    For a perspective without ``binding_key`` the entity values and
    ``attributes`` sit at the top level instead of in ``bindings``.
    Attribute-level values are carried in the sidecar's ``attributes`` member,
    the placement ``hmd-lib-ns-model``'s spec leaves open; a loader that
    wants ``attributes.<attr>.extensions.<perspective>`` redistributes them.

    ``nsctl model inspect`` validates every perspective value against its
    definition, as ``hmd-lib-ns-model`` specifies: an unknown perspective, a
    key not declared at that attach point, or an enum value outside
    ``enum_values`` is a disagreement. The ``hms`` inspector reads existing
    sidecars of known perspectives at ``.hms`` authority, so a declared
    perspective value outranks one inferred from DDL, and the two can
    disagree.

    ``nsctl model inspect <noun> --hms`` prints the core document and one sidecar
    per perspective; ``--out <dir>`` writes them as files under ``<dir>``
    (never into an inspected repository).

.. spec:: Consolidation is deterministic and explains every link
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    Identity:

    * A ``.hms`` noun is ``namespace.name``.
    * Any other noun's namespace is assigned by its inspector, which records
      why. ``nstransform`` strips a layer suffix (``_source``, ``_staging``,
      ``_final``, ``_ux``) from the Trino schema, so
      ``billing_staging.aws_billing`` is the ``staging`` binding of the
      ``trino`` perspective of ``billing.aws_billing``. ``dbt`` uses
      ``<project>.<model>``. Layer conventions live in the inspector, never
      in the model. NERD033 SPEC003 replaces the built-in layer suffixes
      with a name pattern derived from the files.

    Two perspective bindings belong to one noun when they share an
    identity, or the same physical ``schema.table`` (catalog ignored), or an
    inspector said so explicitly. Each merge records its reason.

    An attribute's logical type comes from its highest-authority
    observation. On a tie, the binding its inspector marked primary
    wins (``nstransform`` marks the final layer). Types that differ between
    layers are reported as information: casting between layers is the point
    of having layers.

    Disagreements are:

    * one binding described inconsistently (a ``CREATE TABLE`` and the
      ``INSERT`` into it list different column counts; a ``.hms`` noun and its
      generated view list different attributes);
    * a reference with no producer (a dbt source, a ``ref_to`` noun, a
      ``DROP`` target, a ``dep_tf_name``);
    * an inspector's own finding (an unused ``run_param``).

    Templated names: ``{{ ns_context['k'] }}`` is rendered from the
    transform's own ``run_params`` when the value is a literal (including
    ``.replace('-', '_')``). A value known only at run time becomes a
    ``{k}`` placeholder. A table name with placeholder segments is reduced to
    its stable stem; the pattern is kept as the binding's ``template``
    value. This keeps identities stable across runs.

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

.. spec:: nsctl model inspect and nsctl model diff
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    ``nsctl model inspect [path|noun]...`` inspects the named directories (default
    the current one). A directory that is not itself a repository stands for
    the repositories directly inside it. An argument that is not a directory
    filters the output to matching nouns. ``--json`` prints the model;
    ``--sources`` adds provenance and link reasons; ``--refresh`` inspects
    again and stores a new snapshot (otherwise the latest snapshot for the
    same roots is shown, taking one first if none exists).

    ``nsctl model diff [path...]`` compares the latest two snapshots for
    those roots (``--live``: the latest against the working tree) and prints
    each change under the semantic identity ``hmd-lib-ns-model`` defines:
    ``<ns>.<name>``, ``<ns>.<name>#<attribute>``, and a perspective value at
    ``...@<perspective>`` (``@<perspective>:<binding>`` for one of several
    bindings). Change kinds follow its names: ``ConceptAdded``,
    ``PropertyTypeChanged``, ``RequirednessChanged``,
    ``PerspectiveValueChanged`` and so on.

    Reads print a table, or JSON under ``--json``, as every other read in
    ``nsctl`` does (NERD009). ``inspect`` writes nothing in any repository.

    The model's verbs are under the ``model`` noun, as every ``nsctl``
    command is ``noun verb``: ``nsctl model inspect``, ``nsctl model diff``,
    ``nsctl model perspective`` (NERD033). ``nsctl inspect`` is the composite
    of SPEC008.

Inspecting everything at once
-----------------------------

A repository is several things at once to ``nsctl``: a repo class (what its
BACON manifest says it builds and deploys), the instances of that class
declared in environments, and the data model its files describe. Each has a
noun, and each noun answers "what is this, and what is wrong with it" in its
own words: ``repoclass validate`` findings, ``repoclass detect``'s undecided
questions, model disagreements. ``nsctl inspect`` asks all of them in one
pass, in one vocabulary, so that a person or an agent pointed at an unfamiliar
directory gets one answer, and so that a finding can relate two nouns (a
repository whose transforms build tables that a repo class it does not
declare a dependency on consumes).

.. spec:: nsctl inspect runs every noun's inspect
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    A noun that has an ``inspect`` verb contributes a **section** to
    ``nsctl inspect [path...]``: a summary, and findings. A finding has a
    severity (``error``, ``warning``, ``info``), a code, the subject it is
    about, a message, and where it was learned (repository, file, line). Each
    noun maps what it already reports onto that shape and nothing more:
    ``repoclass validate``'s error, warning and note become error, warning and
    info; ``repoclass detect``'s undecided questions are warnings and its
    refusals info; model disagreements keep their severities.

    ``nsctl inspect`` discovers repositories as ``nsctl model inspect`` does
    (a directory that is not a repository stands for the repositories in
    it), runs each section over them, and prints one section per noun, then
    a count of findings by severity. ``--only <noun>,...`` and ``--skip
    <noun>,...`` choose sections; ``--json`` prints one document, sections
    keyed by noun. A section that cannot run (``instance`` without
    ``HMD_HOME``) says why and the others still run.

    It exits 1 when any section reports an error, so it can gate a change,
    and 0 otherwise. It writes nothing in any repository. Each noun's own
    ``inspect`` verb prints that noun's section alone, in more detail.

    Adding a noun's section is a function and a line in the list of
    sections; the composite has no knowledge of any noun.

.. spec:: nsctl repoclass inspect
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    For a repository with a BACON manifest: its summary as ``repoclass
    describe`` gives it (name, description, build and deploy mechanism,
    dependencies), and ``repoclass validate``'s findings. For one without:
    ``repoclass detect``'s classification and its undecided and refused
    findings. ``nsctl repoclass inspect`` takes ``--path`` like the other
    ``repoclass`` verbs; the composite runs it on every repository found.

.. spec:: nsctl instance inspect
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC010
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    For each repo class among the repositories, every instance of it that an
    environment manifest under ``HMD_HOME`` declares (and the control-plane
    manifest): the environment, the instance name, the version, where it
    deploys from (a checkout or an artifact), and its dependency wiring. The
    findings are about the declarations, read from files only, without the
    control plane:

    * a dependency role wired to an instance its manifest does not declare
      and that is not substrate (``error``: a deploy would fail on it);
    * a repo class among the repositories that no environment declares
      (``info``);
    * an instance deploying from a checkout that is not the repository
      inspected, when a repository of the same repo class was inspected
      (``info``: which working tree is live is not obvious).

    ``nsctl instance inspect [path...] [--env <name>]`` prints the section
    alone. Without ``HMD_HOME`` the section says there are no environments to
    read.

Workspace, queries and export
-----------------------------

``nsctl model inspect <noun>`` shows one noun and one hop of lineage, and the
store keys snapshots by the exact directories inspected, so inspecting one
repository and the directory that holds it give unrelated models. The
questions people ask are searches and traversals over everything: *what is*
``ntc_final.ntc_instances_export``; *where else* does ``export_date`` live;
*what reads* this table two hops on; *what breaks* if this column changes.

Data catalogs offer discovery, ownership and runtime lineage built from
deployed systems after the fact; some represent one logical model with
several physical children, others attach a table to the file that defines it.
None derives the logical model from the repositories before a change merges.
That is what ``nsctl`` adds, and the way to add it to a catalog is to hand the
catalog the model, through an export no consumer is baked into.

*Workspace*
    The repositories one ``HMD_HOME`` models together. Each ``HMD_HOME`` is
    one workspace, as it is one set of environments; ``--home`` selects
    another, as everywhere in ``nsctl``.

*Element*
    Anything a query can name: a noun (``ns.name``), an attribute
    (``ns.name#attr``), a binding (``ns.name@perspective:binding``), a column
    of a binding (``ns.name#attr@perspective:binding``), or a physical
    location (``schema.table``). These are the semantic identities of SPEC006.
    A repository, named by its repo class, is an element too (SPEC018).

*Edge*
    A typed connection between two elements: ``binds`` (a noun and its
    binding), ``column`` (an attribute and a binding's column of it),
    ``lineage`` (derived from), ``relationship`` (an ``.hms`` relationship's
    ``ref_from`` and ``ref_to``), ``alias`` (two identities consolidation made
    one noun, with its reason), and between repositories ``reads`` (SPEC018).

.. spec:: One model per workspace, refreshed incrementally
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC011
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    An ``HMD_HOME`` keeps one current workspace model, laid out as the rest of
    ``nsctl``'s state:

    * **What is in it is configuration**, in ``$HMD_HOME/.config/nsctl.toml``
      under ``[model]``: ``roots``, a list of directories, each a repository
      or a directory of repositories as ``nsctl model inspect`` accepts.
      ``nsctl model add <path>...`` and ``nsctl model remove <path>...`` edit
      the list; ``nsctl model roots`` prints it with the repositories each
      root expands to. A root that no longer exists is reported and skipped,
      not removed.
    * **The model itself is derived state**, in the inspection store at
      ``$HMD_HOME/.cache/neuronsphere/inspect/model.db`` (SPEC005),
      beside the per-scope snapshots ``diff`` uses. Deleting it loses only
      what a refresh rebuilds. Perspective edits (NERD033 SPEC005) for the
      workspace are kept with it, across rebuilds, as edits already are.

    Without ``HMD_HOME`` there is no workspace; ``nsctl model`` says so and
    ``nsctl model inspect <path>`` still works as before.

    Refreshing a workspace re-inspects only the repositories whose state
    changed since the last refresh: a different git revision, or for a
    working tree with changes, a different digest of the files its
    inspectors read. Observations are kept per repository, so a refresh
    replaces one repository's observations, then derives (NERD033) and
    consolidates the whole workspace again. Derivation and consolidation see
    every repository, so identities, layers and lineage are the same as
    inspecting the whole workspace at once.

    A query refreshes first unless ``--no-refresh`` is given, and says how
    many repositories it re-inspected. ``nsctl model refresh`` refreshes
    alone; ``--full`` re-inspects everything. Adding or removing a root
    refreshes the repositories it adds or drops.

    A refresh that changes anything keeps the model it replaced, one
    generation, as the baseline of ``impact --changed`` (SPEC017). (Amended
    2026-10-01.)

.. spec:: Query commands
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC012
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    Under ``nsctl model``:

    * ``find <text>`` searches element names, physical locations and
      descriptions, and ranks exact identities, then exact physical names,
      then prefixes, then substrings. A physical name finds its noun:
      ``find ntc_final.ntc_instances_export`` answers
      ``ntc.ntc_instances_export@trino:final``.
    * ``show <element>`` prints one element: for a noun, the view of SPEC006; for
      an attribute, its core type and every column that carries it, with each
      column's perspective values and source.
    * ``where <attribute>`` lists every manifestation of an attribute: each
      binding's column of it, across perspectives and layers, with physical
      type and source file and line.
    * ``related <element> [--depth N] [--via <edge kind>,...] [--direction
      up|down|both]`` walks edges from the element and prints what it reaches,
      each with the path that reached it. The default is depth 1, every edge
      kind, both directions.
    * ``impact <element>`` is ``related --via lineage,column --direction
      down`` with no depth limit, at column level when the element is an
      attribute: every binding, column and downstream noun a change to it
      reaches. Where column-level lineage is unknown (a ``select *``, an
      untyped dbt column), the reach widens to the whole binding and says so.
      ``impact`` also takes a change instead of an element (SPEC017).

    Every result carries provenance (repository, file, line, revision) and,
    where a disagreement concerns it, the disagreement. A query never writes
    into a repository.

.. spec:: A stable machine contract, for scripts and agents
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC013
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    Every ``nsctl model`` command takes ``--json``. Its output is a
    documented, versioned schema (``schema_version`` in every document);
    a change that removes or renames a field is a new major version.
    Elements are always given by their semantic identity, so the output of
    one query is valid input to the next.

    ``nsctl model mcp`` serves the same queries over the Model Context
    Protocol on stdio, one tool per command, with the same JSON schema as
    results. An agent asking "what breaks if I change this column" calls
    ``impact``, rather than reading repositories.

.. spec:: The impact of a change
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC017
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    ``nsctl model impact`` takes a change as well as an element:

    * ``--since <ref>`` compares each workspace repository at ``<ref>`` with
      its working tree. A repository where ``<ref>`` does not resolve counts
      as unchanged. The model at ``<ref>`` is inspected from the revision's
      files, read from git without checking anything out, and is cached by
      revision.
    * ``--changed`` compares the workspace with the baseline its last
      refresh kept (SPEC011). It is the default when no element is given,
      and is the form a watcher calls after each refresh.
    * A file path given as the element stands for every element observed in
      that file.

    Both sides are derived and consolidated as whole workspaces. They are
    then compared with ``nsctl model diff``'s change kinds, so a change is
    described semantically, as a diff is (SPEC006).

    How far each change reaches depends on its kind:

    * **Changes that reach downstream** are walked as ``impact <element>``
      walks: removals, type, requiredness and enumeration changes, metatype
      and relationship endpoint changes, removed bindings, and changed or
      removed perspective values. A lineage change reaches downstream of the
      binding whose inputs changed.
    * **Additions** reach only the noun's other bindings, which may need to
      carry what was added, and not downstream lineage.

    A change the model does not see (a comment, a description, a file no
    inspector reads) reaches nothing, so an edit that leaves the model
    unchanged produces an empty result.

    The result has three parts:

    * **changes**, each with provenance on the side where it occurred;
    * **findings**: the disagreements the change introduced and the ones it
      resolved. These need nothing run to find, for example an ``.hms``
      attribute whose type changed while the Trino column carrying it did
      not;
    * **reach**: each reached element grouped by the repository that
      defines it, with the edge path from the change. Wherever the reach
      widened is listed as unknown reach, never omitted. Examples are a
      ``select *``, unknown column lineage, or a binding whose inspector
      still names its own identities;
    * **frontier**: every reached binding with a physical location. Things
      outside the workspace (dashboards, ad hoc queries, other teams'
      pipelines) may read any of them, and ``nsctl`` cannot see what does.
      Each entry gives its dialect and its location as separate parts
      (catalog, schema, table) instead of one dotted string. It also gives
      the external identifiers known for it (SPEC016). Leaves come first:
      bindings nothing in the workspace reads. The frontier is where a tool
      that sees deployed systems, such as a data catalog, picks up the
      traversal. ``nsctl`` hands it over and never calls such a tool itself.

    For example:

    .. code-block:: text

       changed   ntc.instance#status  PropertyTypeChanged string -> integer
                 (hmd-lang-ntc  src/schemas/ntc/instance.hms:14)
       findings
         warning attribute-type  ntc.instance#status@trino:final is varchar (string)
                 (hmd-config-transform-reporting  src/transforms/load.yaml:42)
       reach
         hmd-config-transform-reporting
           ntc.instance@trino:final  column status
         hmd-tf-ntc-export
           ntc.ntc_instances_export  lineage from ntc.instance@trino:final
       unknown reach
         billing.invoice_view@postgres-view  select * from ntc_final.instances
       frontier
         leaf  trino  <catalog> ntc_final ntc_instances_export   <system>: <id>
               trino  <catalog> ntc_final instances

    That example needs one disagreement consolidation does not report yet.
    ``attribute-type`` (``warning``, like ``column-type``) is a binding
    column whose physical type maps (by its perspective's ``hms_type``,
    NERD033) to a core type other than the attribute's. Today, consolidation
    compares column types only between two sources of the same binding.

    An edit that changes both sides of a seam consistently, for example the
    ``.hms`` and the DDL together, introduces no disagreement. Its reach
    still names the downstream consumers.

    ``impact`` exits 1 when the change introduced an ``error`` finding, as
    ``nsctl inspect`` does. Reach alone never fails it. ``--json`` follows
    SPEC013.

    This NERD stops at naming repositories and reasons. Which commands check
    a change in a reached repository is that repository's manifest's
    business: a separate proposal declares named commands in the BACON
    manifest. Running those commands, and a watch mode, belong to that
    proposal as well. A watcher is a refresh on file events followed by
    ``impact --changed``, with the reach handed to the command runner or to
    an agent.

.. spec:: Repositories the workspace does not hold
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC018
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    In a repository-per-component setup, a seam usually crosses into a
    repository that is not checked out. Without help, ``impact`` then goes
    quiet where it should say "and something you do not have". This spec
    keeps what ``nsctl`` learns about other repositories, and lets a
    manifest carry the part a fresh clone needs. It asks a person only to
    confirm guesses.

    **The repository graph.** The inspection store (SPEC005) keeps a graph
    of repositories beside the workspace model:

    * **Nodes** are repositories, named by repo class. A node records where
      the repository is checked out, if anywhere, and its known sources:
      git URLs or artifacts, each with the evidence for it.
    * **Edges** are ``reads``: a repository's files read a location, noun or
      attribute that another repository defines. An edge records the elements
      it carries, its evidence, its first-seen and last-seen revisions, and a
      status:

      * ``derived`` when inspection resolved both ends;
      * ``proposed`` when the other end is a guess;
      * ``confirmed`` when a person or a manifest stated it;
      * ``stale`` when the last inspection of the reading repository no
        longer shows it.

    ``nsctl`` learns the graph as a side effect of work it already does:

    * every inspection, whether a workspace refresh, a one-off
      ``nsctl model inspect <path>``, or an ``impact --since`` baseline;
    * environment and control-plane manifests, whose instances name their
      repo class and source;
    * BACON ``deploy.dependencies``;
    * ``related`` sections of manifests it reads;
    * outside observations (SPEC016).

    Removing a root or deleting a checkout does not forget what was learned
    from it. Its edges keep their last-seen revision. The graph lives in the
    store under ``.cache`` because all of it can be learned again. The part
    worth keeping is also in manifests, so losing the store loses only
    memory of repositories no longer at hand.

    **Unresolved references.** A binding that reads a location nothing in
    the workspace defines produces an ``info`` finding,
    ``reference-unresolved``. If the graph knows a repository that defines
    that location, because it was inspected once even if it is not checked
    out now, the finding names it. If not, ``nsctl`` proposes a source,
    strongest evidence first:

    #. an instance in an environment or control-plane manifest whose repo
       class defines that location;
    #. an external identifier whose catalog entry names its defining
       repository (SPEC016);
    #. ``neuronsphere.lock``;
    #. the naming convention: the reading repository's ``origin`` with its
       last path segment replaced by the candidate repo class. This guess is
       labelled as a guess.

    **Queries.** A repository is an element, so ``nsctl model related
    <repo class> [--direction up|down]`` answers from the graph.

    * **Upstream** is "what does this read".
    * **Downstream** is "who reads this". Locally, the graph knows a reader
      only if some inspection on this machine has seen it. With a shared
      graph, it knows the readers that have been published (SPEC019). A
      downstream answer therefore gives when each reader was last seen and
      what the answer covers, and never claims to be complete.

    ``impact`` uses the graph too. Reach into a repository that is not
    checked out is reported, with its source and the command that fetches
    it, rather than stopping.

    **The manifest's** ``related`` **section.** This is a list of the
    repositories a change across this repository's seams needs:

    .. code-block:: json

       "related": [
         { "repo_class": "hmd-lang-ntc",
           "source": { "git": "git@github.com:hmdlabs/hmd-lang-ntc.git", "ref": "main" },
           "reason": "model",
           "elements": ["ntc.instance"] }
       ]

    ``source`` takes the forms an install item's ``source`` takes in BACON
    (git, or artifact). ``reason`` is ``model`` (it reads this repository's
    data) or ``deploy`` (a deploy dependency, for when the source is
    wanted). ``elements`` is optional and informational.

    Entries are upstream only, since a repository can state what it reads
    but not who reads it. The field belongs in the BACON specification
    (``hmd-docs-bacon``), and ``nsctl`` reads it before that is published.

    The section is written by ``nsctl``. People only answer its questions:

    * ``nsctl repoclass related sync [path]`` writes every ``derived`` and
      ``confirmed`` upstream edge of the repository into its manifest. It
      adds new ones and reports stale ones. It removes an entry only with
      ``--prune``.
    * ``nsctl repoclass related confirm <repo class> [--source <url>]``
      turns a ``proposed`` edge into a ``confirmed`` one, optionally
      correcting the source.
    * ``nsctl repoclass related list [path]`` prints the entries with each
      one's status in the graph.
    * Hand edits are allowed. An entry a person wrote counts as
      ``confirmed``.

    ``nsctl repoclass validate`` checks the entries' shape. ``nsctl inspect``
    reports ``related-stale`` (``warning``) for an entry no current edge
    supports, and ``related-missing`` (``info``) for a ``derived`` edge the
    manifest lacks. With those findings and an agent able to run ``sync``,
    keeping the section current is a review step, not authoring.

    **Fetching.** ``nsctl model fetch [<repo class>...] [--related] [--edit]``
    gets repositories the workspace needs. ``--related`` fetches every
    ``related`` entry of the workspace's repositories. The fetch has two
    modes:

    * **For reading**, the default: a partial clone into
      ``$HMD_HOME/.cache/neuronsphere/inspect/sources/``. It is inspected at
      a ref, as ``impact --since`` reads revisions, and is never edited or
      shown as a working tree. It joins the workspace as a read-only member,
      and a refresh fetches it again. An artifact source is inspected from
      the artifact.
    * **For editing**, with ``--edit``: a clone into the user's repositories
      folder under BACON's rules for git sources. ``nsctl`` uses the user's
      own credentials and stores none, never fetches into or resets an
      existing clone, and refuses a directory that is a clone of another
      URL. The clone is then added to ``[model] roots``.

    By default ``fetch`` follows one hop. It lists the ``related`` entries
    of what it fetched without following them, unless ``--depth`` is given.

.. spec:: The repository graph is shared through the control plane
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC019
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    One machine's graph only knows the readers that machine has inspected.
    The deployment control plane already knows every repo class an
    organisation deploys, so it is where the graph is shared.

    ``nsctl model publish [path...]`` sends the control plane one **slice**
    per repository. A slice is the repository's observations at one revision,
    in the shape outside observations take (SPEC016), plus its repository
    node and its ``reads`` edges. A slice carries:

    * the repo class;
    * the repository's sources (its ``origin`` URL, its ``related`` entries);
    * the ref and revision.

    A newer revision of the same ref replaces the older one. ``publish``
    refuses a working tree with uncommitted changes, because a slice has to
    name a revision others can read.

    **A slice references source code and never holds it.** It carries:

    * the model's structure: identities, attributes and columns, their types
      and requiredness, enumerations, lineage, relationships and
      disagreements, plus descriptions an ``.hms`` gives;
    * perspective values whose key the definition declares as ``enum``,
      ``bool`` or ``short_text``, and their parameters;
    * for everything else, a reference instead: repository, path, line
      range, revision and the file's blob digest. That covers SQL
      statements, view and model bodies, templates, expressions, text
      literals, and anything an inspector keeps as extra raw data.

    The digest tells a reader whether a reference is still current without
    sending the file. Whoever needs the code reads it from the repository
    with their own access (SPEC018's read mode). The control plane is
    therefore never a way to read code someone has no access to. What
    anyone with control-plane access can see is the structure: names,
    columns, types and lineage.

    ``publish`` builds a slice by this rule, not by trusting each inspector.
    A slice that would carry a value outside it is refused with the
    offending element named. ``scan --publish`` (SPEC020) uses the same
    builder. (Amended 2026-10-01.)

    Every workspace that can reach the control plane receives the published
    slices of repositories it does not hold. It receives them as read-only
    members, ingested as SPEC016 ingests any observations file, and refresh
    re-reads them. Each element they contribute carries provenance naming the
    control plane, the revision and when it was published. A checked-out
    repository always takes precedence over its published slice.

    This makes ``related <repo class> --direction down`` and ``impact``
    organisation-wide for every published repository. They still say
    which repositories their answer covers.

    ``nsctl`` uses the control-plane login it already has (NERD008) and adds
    no credential. A local control plane gives a team nothing, so
    publishing is for a shared or cloud control plane, selected like any
    other control-plane command.

    This spec fixes only the slice and the commands. How the control plane
    stores slices and answers for them is designed in ``hmd-ms-deployment``,
    in a proposal of its own. The control plane can then also answer "who
    lists me in ``related``" without being sent any edges.

.. spec:: Building the graph in CI or from a code host, without clones
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC020
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    No engineer should need every repository checked out to see across a
    seam. There are two ways to fill the shared graph, and an organisation
    may use either or both.

    **Each repository's CI publishes itself.** A job on the default branch
    runs ``nsctl model publish``. The repository is already checked out
    there, so nothing else is cloned.

    On a pull request, CI runs ``nsctl model impact --since <base>``. The
    workspace is the one checked-out repository plus every published slice,
    so the reach crosses the organisation without cloning anything. The
    result is the usual ``--json``. Commenting on the pull request, failing
    the check, or both, is the CI wrapper's decision.

    A GitHub Action (or the equivalent for another CI system) that wraps
    these two calls is a separate deliverable in its own repository. It is
    not part of ``nsctl``, just as a catalog generator is not (SPEC015).

    **A scan of a code host, for repositories whose CI does not publish.**
    ``nsctl model scan <host>/<organisation>``:

    #. lists the organisation's repositories that contain a BACON manifest;
    #. reads each manifest;
    #. reads only the files that manifest's inspectors need.

    It never makes a working tree, and it writes the results into the
    local graph. With ``--publish``, it publishes each repository's slice as
    ``publish`` does.

    How files are read:

    * Listing repositories needs the host's API. GitHub is the first
      supported host, using the user's existing ``gh`` login or
      ``GITHUB_TOKEN``, never a stored credential.
    * Reading files uses git itself: a partial, sparse fetch of the needed
      paths into the cache (SPEC018's read mode). Supporting another host
      then means only another way to list repositories.

    Run on a schedule in CI, ``scan --publish`` gives an organisation a
    shared graph with nothing added to any repository except the manifest.
    A repository whose own CI publishes is skipped when its published
    revision is current.

    A scan reads what is on the default branch. A pull request's change is
    seen only by ``impact`` in that pull request's CI, or on an engineer's
    machine.

.. spec:: Export as a documented interchange document
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC014
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    ``nsctl model export [--out <file>]`` writes the workspace model as one
    JSON document, versioned like SPEC013, holding:

    * **logical entities**: each noun with its metatype, attributes and their
      core types, requiredness, enumerations and descriptions, and for a
      relationship its ``ref_from`` and ``ref_to``;
    * **physical bindings**: each binding with its perspective, binding name,
      physical location, whether it is primary or a reference, its
      perspective values, and its columns, each aligned to the logical
      attribute it carries (by name today; an alignment carries the rule that
      made it, so a future rename-aware alignment can say so);
    * **perspective definitions** in effect, declared or derived (NERD033);
    * **lineage**, at binding level and at column level where known, with how
      each edge was learned;
    * **provenance** for every element: repository, file, line, revision,
      and the authority and confidence of the source;
    * **disagreements**, with their severity, code and subject.

    The export names no consumer and has no consumer-specific fields. It is
    the contract every consumer builds on, and its schema is published with
    the command reference.

    ``nsctl model inspect <noun> --context`` (NERD033 SPEC004) remains the
    per-noun form of the same information, for generators that work one noun
    at a time.

.. spec:: Consumers are generators outside nsctl
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC015
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    Turning the export into a particular catalog's entities is a generator:
    a template set rendered by ``hmd-cli-mickey`` over the export, a program
    generated from the export's schema, or a script. It lives in its own
    repository, versioned with the catalog it targets, never in ``nsctl``.
    Publishing (calling a catalog's API) is the generator's job as well.

    A catalog's mapping is expected to follow one shape regardless of
    target: a logical noun becomes the catalog's closest logical or business
    construct; each physical binding becomes, or links to, the catalog's
    physical asset for that location; columns are linked to the logical
    attribute they carry where the catalog can say so; lineage and
    provenance (a link to the defining file and line) are carried across;
    disagreements become whatever the catalog uses for quality findings.
    Where a catalog lacks a construct (a logical entity with typed
    attributes, a column-level mapping), the generator degrades, and that is
    its decision, not ``nsctl``'s.

    The first generator is a separate deliverable from this NERD. ``nsctl``
    is done when the export carries everything the mapping above needs.

.. spec:: The workspace model can also take observations from outside
    :id: HMD_CLI_NEURONSPHERE_NERD032_SPEC016
    :links: HMD_CLI_NEURONSPHERE_NERD032
    :status: proposed

    The reverse direction uses the same boundary: an inspector that reads a
    file of observations in the export's shape (for example, the deployed
    schemas a catalog has ingested, produced by a generator outside
    ``nsctl``) adds them at their own authority. A deployed table that
    differs from the repository's DDL is then one more disagreement. This
    NERD reserves the shape; the inspector is later work.

    Such a file is named under ``[model] ingest`` in ``nsctl.toml``. It is
    read again on a refresh whenever its digest changes.

    A file of outside observations can also carry **external identifiers**
    for physical locations. Each entry has these fields:

    * ``dialect`` and the location parts (catalog, schema, table), which
      locate the binding;
    * ``system``, a label the generator chooses;
    * ``id``, an opaque string;
    * optionally ``environment``, since a catalog can hold the same table
      once per environment.

    ``nsctl`` interprets neither ``system`` nor ``id``. It attaches each
    entry to the binding with that location as an ``alias`` edge, and
    carries it in ``show``, ``where``, ``impact`` (its frontier) and the
    export. ``find`` accepts an external identifier and answers its binding.

    External identifiers never take part in identity or consolidation, so a
    wrong mapping cannot merge or split nouns. An entry whose location
    matches no binding is an ``info`` finding (``external-id-unmatched``).

    The knowledge of how one catalog forms its identifiers (a platform, a
    service name, an environment) stays in the generator that writes the
    file. Results then carry the catalog's own identifiers for an agent to
    pass to that catalog's tools, while ``nsctl`` knows no catalog's format.
    (Amended 2026-10-01.)

Open questions
--------------

#. Should the per-scope snapshots of SPEC005 become snapshots of the
   workspace model (``diff`` between two workspace refreshes), or stay
   separate?
#. Which column alignments beyond same-name are worth deriving (a column
   renamed between layers by an ``INSERT ... SELECT`` alias), and should
   they be edits like NERD033's?
#. Does ``nsctl model mcp`` belong in ``nsctl``, or in a separate binary that
   reads the same store?
#. Should ``nsctl model inspect <path>`` offer to add what it inspected to the
   workspace, so the first inspection is also the setup?
#. Should an additive change reach downstream after all where a consumer is
   known to break on new columns, for example an ``INSERT`` with no column
   list that a new column shifts?
#. Should ``impact --since`` take a ref per repository, for a change that
   spans branches named differently in each repository?
#. Should an edge whose source was found in an environment manifest count as
   ``derived`` rather than ``proposed``, so ``sync`` writes it without a
   confirmation?
