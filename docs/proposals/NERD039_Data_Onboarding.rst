.. NERD039 Data Onboarding

NERD039 Data Onboarding
=======================

.. req:: A newcomer can explore their own data files with nsctl and nothing else
    :id: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data`` shall take a person who has never seen NeuronSphere from a
    folder of their own files (CSV, TSV, Parquet, JSON, NDJSON, Excel, or a zip
    of those) to answers in SQL. They need only ``nsctl`` and a container
    engine: no Python, no environment, no bucket, no account.

    Every step that decides something (which files form a table, what type a
    column is, which columns join) shall be done by a verb that emits JSON, so
    that a person, a bundled agent skill, and a test all get the same answer.

    The NeuronSphere model (``.hms`` per NEP051) shall be ``nsctl``'s internal
    representation of what it learned. The person shall never need to read or
    write it. They approve proposals in plain language and receive outputs they
    already understand: named views, a data dictionary, a relationship diagram
    and portable SQL.

    Storage, a NeuronSphere environment, Trino and the Librarian shall be
    opt-in steps taken after the first answer, never prerequisites for it.

Motivation
----------

``nsctl`` makes the local platform easy to start for people who already know
what a repo class is. A newcomer arrives with data, not repositories. Today
the guided first run deploys the ``analytics`` bundle, roughly thirty instances,
before it can show anything, and no ``nsctl`` verb reads, loads or queries a
data file. The bundled ``nsctl-onboard`` skill is entirely about repositories
and deploys.

The first thing a newcomer should see is their own data becoming easy to
explore. Platform concepts can follow, one at a time, as each starts to pay
for itself.

How this fits the other proposals
---------------------------------

* **NERD032 (data model inspection).** ``nsctl data`` is a new source for
  the same model. Its profiles enter as ``inferred`` observations through a
  new ``nsdata`` inspector, and a person's approvals as declared NEP051
  sources through a new ``modelsource`` inspector (SPEC007). NERD032 is
  amended in three places: an out-of-scope carve-out for profiling data
  files, two rows in its authority table, and one closed exception to its
  "generators live outside nsctl" rule (SPEC008).
* **NERD033 (perspective derivation).** Untouched. Perspectives appear only
  after ``nsctl data export``, through generators outside ``nsctl``.
* **NEP051 (model sources and perspective packs).** The internal model is
  NEP051 files. NEP051 gains a ``group:`` source kind for file groups.
* **NEP048 (query first) and NEP049 (adoption tiers).** ``nsctl data`` is
  NEP048's ladder (raw query, then an optional model) run locally with no
  platform. ``understand``/``approve`` is NEP049's Understand loop at tier 0.
* **Librarian NERD012 (content type schemas).** ``table.yaml`` uses
  NERD012's field names, so a group becomes a content item type by
  translation. A pinned group also supplies the column list the
  ``content_item_to_trino_table`` transform template needs.
* **NEP050 (data products).** ``queries/*.sql`` is the natural input to
  promotion. Not in scope here.

Open questions
--------------

* **Semantic layer overlap.** The proposed semantic layer's portable core
  plans its own DuckDB-backed MCP server and manifest. That overlaps with
  ``data connect`` and ``out/``, and one of them should own it.
* **Type vocabulary.** ``table.yaml`` uses Arrow type names (NERD012), and
  NEP051 ``columns`` uses Trino types. NEP051 lists this as open.
* **Proposal quality.** Rules over profile evidence may propose poorly on
  real exports. The agent evaluation in Verification is the gate before
  ``understand`` ships.

Scope and terminology
---------------------

*Project*
    A folder the person owns, ideally in git, holding what ``nsctl data``
    learned and generated. It is not a repo class.

*Source*
    A file or directory the person pointed ``nsctl data`` at. Sources are
    read in place and never modified.

*Group*
    A set of files that form one table: one format, a compatible schema, and a
    path pattern (including hive-style partitions). Grouping is the one
    decision that cannot be inferred away; every later decision can stay
    inferred until something depends on it.

*Pin*
    Recording a group's inferred schema as the declared one. After a pin,
    drift is reported instead of silently absorbed.

*Engine*
    DuckDB, run from a pinned container image (SPEC002).

*Internal model*
    NEP051 concepts and sources (``.hms``, ``.source.yaml``) written under the
    project's ``.ns/model/``, with each source's provenance ``inferred`` (a
    proposal) or ``declared`` (a person approved it), as NEP051 defines them.

Out of scope: database dumps and live database connections (reported as
unsupported by ``scan``); cloud buckets; creating any cloud resource;
scheduling and refresh pipelines; a user-facing modeling language.

.. spec:: The project folder
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data init [dir]`` shall create, or adopt, this layout and nothing
    else::

        data.yaml                   # sources, optional storage target, progress
        tables/<table>.table.yaml   # group: path pattern, format, reader options, pinned schema
        queries/*.sql               # saved answers
        out/                        # generated outputs (SPEC008)
        .ns/model/                  # internal model (SPEC007); committed
        .ns/profile/<table>.json    # saved profiles (SPEC004); committed
        .ns/snapshot/               # optional Parquet copies (SPEC005); ignored by git
        .ns/catalog.duckdb          # generated views; ignored by git

    Everything under ``out/`` and ``.ns/catalog.duckdb`` is derived from
    ``tables/``, ``.ns/profile/`` and ``.ns/model/``, and can be deleted and
    regenerated. A
    project shall be usable on another machine from a clone plus the sources.

    ``data.yaml`` records how far each table has come
    (grouped, profiled, pinned, registered, understood, landed), so that
    ``nsctl data status`` and every bundled skill can resume where the last
    session stopped.

.. spec:: DuckDB runs in a pinned container, not in the binary
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl`` is built with ``CGO_ENABLED=0`` and keeps its 50 MB size
    budget (NERD002 SPEC013). DuckDB is a C++ library, so it shall not be
    linked into ``nsctl``. ``nsctl`` shall instead run the DuckDB CLI from an image
    (``hmd-img-duckdb``) pinned by digest (NERD030), with the extensions it
    needs (``httpfs``, ``excel``, ``ui``) installed in the image, so that no
    step downloads an extension at run time.

    ``nsctl`` generates every SQL statement in Go, runs it with ``duckdb
    -json``, and parses the result. Sources are mounted read-only and the
    project read-write, both at the same absolute paths they have on the host
    (as the deploy runner binds ``$HMD_HOME``, NERD002 SPEC010), so that views
    written into
    ``.ns/catalog.duckdb`` resolve unchanged from a DuckDB client on the host.

    Until storage is configured (SPEC009) the container has no network.

.. spec:: scan proposes groups
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data scan <path>...`` shall walk the sources, detect each file's
    format (by content, not only by extension), and propose groups: files
    whose schemas are compatible and whose paths share a pattern. Hive-style
    ``key=value`` directories become partition columns. Each sheet of an Excel
    workbook and each member of a zip is a candidate file of its own.

    A file it cannot read, or a format it does not support (for example an
    SQL dump), is listed with a reason. It is not an error.

    ``scan`` writes nothing unless ``--apply`` is given. ``--apply`` writes one
    ``tables/<table>.table.yaml`` per accepted group. Its fields are the ones
    Librarian NERD012 SPEC001 gives a content item type, so a group can later
    become a content item type by translation, not redesign::

        table: orders
        paths: ["exports/orders/month=*/*.csv"]   # the grouping
        format: csv                                # parquet | csv | json | xlsx
        reader_options: {delimiter: ",", header: true}
        schema_mode: infer                         # infer | declared
        schema:                                    # absent until pinned (SPEC005)
          - {name: order_id, type: utf8, nullable: false, description: ""}
        casts: {order_date: date32}                # fixes chosen in SPEC004

    Column types use Apache Arrow logical type names, as NERD012 does.
    ``xlsx`` is local only: Librarian NERD012 has no such format, so ``land``
    (SPEC009) always writes Parquet. A zip member is addressed as
    ``<zip>!<member>``. ``paths`` are relative to a source root named in
    ``data.yaml``, so a project is portable. A group can be renamed,
    split or merged by editing that file, or with ``--name``, ``--split`` and
    ``--merge``. A rescan reports new, changed and missing files against the
    existing groups instead of proposing them again.

.. spec:: profile reports what a person needs to decide
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data profile <table>`` shall report, per column: inferred type,
    null fraction, distinct count, minimum and maximum, and a small sample.
    Per table it reports row count and, across files, schema drift (a column
    missing, added or typed differently in some files). It also flags issues
    worth a decision, each with a suggested reader option or cast:

    * dates or numbers read as text;
    * identifiers that lose leading zeros when read as numbers;
    * header or footer junk rows, and encoding or byte-order-mark problems;
    * candidate keys (unique and non-null);
    * likely personal data (email, phone, names, national identifiers). The
      samples for those columns are redacted in the output.

    ``--relationships`` profiles every registered table together and reports
    candidate joins as evidence: for each column pair with compatible types,
    the fraction of one side's distinct values found in the other, and the
    cardinality of the match. It proposes nothing on its own. SPEC007 turns
    the evidence into proposals.

    ``profile`` reads samples only by default; ``--full`` scans everything.
    Every profile is saved to ``.ns/profile/<table>.json``, with
    relationship evidence in ``.ns/profile/_relationships.json``. These files
    are what the ``nsdata`` inspector reads (SPEC007). A profile is evidence
    and never a decision.

.. spec:: register makes tables queryable in place
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data register`` shall (re)build ``.ns/catalog.duckdb`` with one
    view per table, reading the files in place with the reader options and
    casts in its ``table.yaml``. ``--pin`` writes the current inferred schema
    into ``schema``. As with NERD012's ``infer`` mode, a recorded schema never
    changes on its own after that: a file that no longer matches is reported
    by ``status`` and ``register`` instead of changing the view's type.
    ``schema_mode: declared`` means a person wrote or edited the schema, and
    ``register`` refuses a file that does not match it.

    ``nsctl data snapshot [<table>]`` copies a table to Parquet under
    ``.ns/snapshot/`` and points its view there, so later edits to the sources
    do not change answers. The snapshot records the content hash of every
    source file it was taken from.

.. spec:: sql, connect and status
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data sql [<statement>]`` runs one statement against the catalog
    and prints the result, or opens an interactive DuckDB shell when given
    none. ``--json`` returns rows as JSON. The catalog is read-only to
    ``sql`` unless ``--write`` is given.

    ``nsctl data connect`` prints, for this project, how to open the catalog
    from the DuckDB CLI, the DuckDB UI, DBeaver, and an agent through an MCP
    server configuration. It prints instructions only and installs nothing.

    ``nsctl data status`` reports each table's progress (SPEC001), drift
    since its pin, sources changed since its snapshot, and the next suggested
    command.

.. spec:: understand proposes meaning; a person approves it
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    Meaning is learned the way NERD032 learns any model: by inspecting files
    and consolidating observations. ``nsctl data`` writes no model element
    except through that path. Two inspectors are added to NERD032 SPEC001:

    * ``nsdata`` reads the project's ``tables/`` and ``.ns/profile/``. Groups
      become bindings, pinned schemas become attributes at the authority of
      a pinned table, and everything else (inferred types, candidate keys,
      value overlap) is ``inferred`` observations with ``evidence``
      confidence. It works the same way NERD032 SPEC002 treats any
      inference.
    * ``modelsource`` reads NEP051 ``*.source.yaml`` files at an authority set
      by their ``provenance``. It is not specific to ``nsctl data``: any
      model repository's sources are read the same way.

    ``nsctl data understand`` shall run ``nsctl model inspect`` over the
    project and turn each element whose best authority is ``inferred`` into a
    proposal: an entity (one or more groups standing for one business thing),
    its name and description, a key, or a relationship. It writes each
    proposal as NEP051 files under ``.ns/model/`` with ``provenance:
    inferred``: a concept ``.hms`` and a source whose ``from`` is ``group:
    <table>`` (the source kind NEP051 gains for this case) and whose
    ``evidence`` holds the profile figures. Nothing uses machine learning. An
    agent skill may suggest better names, and ``nsctl`` records whatever a
    person approves.

    Each proposal in the JSON output carries an identifier, the evidence
    behind it, and a yes/no question phrased without modeling vocabulary, for
    example: *"98% of orders.cust_id values appear in customers.id. Is every
    order placed by one customer?"*

    ``nsctl data approve <id> [--name <name>] [--description <text>]`` rewrites
    the proposal's files with ``provenance: declared``. Inspection then ranks
    them at NEP051-declared authority, above everything but ``.hms``.
    ``nsctl data reject <id>`` deletes the files and records the rejection in
    ``data.yaml``, so ``understand`` does not ask again. After each decision
    the project is inspected again. A disagreement NERD032 reports (for
    example, an approved key that the data contradicts) is shown to the
    person, never resolved silently.

    Because the internal model is ordinary NEP051 files read by ordinary
    inspection, ``nsctl model diff``, ``impact`` and ``export`` work on a data
    project with no extra code. No new ``nsctl model`` verb is introduced.

.. spec:: docs generates the outputs a person reads
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data docs`` shall regenerate, from the project's NERD032 export
    and its profiles:

    * ``out/DATA.md``: a data dictionary. Every table and named view, its
      columns with type, meaning and quality notes, and where it came from.
    * ``out/relationships.md``: a relationship diagram (PlantUML) and a table
      of joins with their evidence.
    * ``out/views.sql``: ``CREATE VIEW`` statements for every declared entity.
      They are plain SQL that runs unchanged in any DuckDB, and they name no
      NeuronSphere concept.
    * ``out/questions.md``: questions the data can answer, each with SQL that
      runs.

    It also creates the same named views in ``.ns/catalog.duckdb``.
    Proposals that are still only ``inferred`` appear marked as unconfirmed;
    rejected ones do not appear.

    These four outputs are the one closed exception to NERD032 SPEC015
    (generators live outside ``nsctl``). They are rendered in Go, need no
    template repository, no Python and no network, and target no catalog. No
    other output format is added to ``nsctl data``.

    ``nsctl data export <dir>`` hands the internal model to a team that wants
    it. It copies ``.ns/model/`` as a NEP051 model repository and writes the
    NERD032 export document beside it. A dbt project, Trino DDL, or the output
    of a perspective pack (NERD033) is produced from there by a generator
    under NERD032 SPEC015, never by ``nsctl data``.

.. spec:: Storage is opt-in and local
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    No step before this one needs an environment. Storage is always the S3
    service of a local NeuronSphere environment.

    ``nsctl data storage --env <name>`` prints that environment's S3 endpoint,
    both as reached from the host and from inside the environment's network.
    It also prints the environment's local credentials (as ``nsctl env
    credentials`` does, without ``--reveal`` unless asked), the buckets that
    exist, and ready-to-run snippets for creating a bucket with the AWS CLI
    and with Terraform's AWS provider pointed at that endpoint. A person may
    create their bucket with any tool they like.

    ``nsctl data land <table> --env <name> --to <target>`` writes the table
    as Parquet under ``<target>/raw/<table>/`` and points its view there. It
    is idempotent by source content hash. The target is either:

    * ``s3://<bucket>[/<prefix>]``, a bucket that exists in that
      environment; or
    * ``instance:<name>``, a deployed instance of ``hmd-inf-s3bucket``.
      Its bucket name is read from the instance's outputs, never typed.

    For the second form, ``hmd-inf-s3bucket`` reaches a person with no
    checkout and no tenant as a published bundle, ``storage``. That is a bundle
    repository whose ``meta-data/bundles/storage.json`` and lock name only
    that class, published with ``nsctl bundle build`` and ``nsctl bundle
    push`` to the default namespace, so ``nsctl bundle add storage --env
    <name>`` resolves it anonymously. ``nsctl data``
    never deploys anything itself.

    While a target is configured, the engine container joins that
    environment's network.

.. spec:: Bundled skills guide the ladder
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC010
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl`` shall bundle these agent skills (NERD015). Each orchestrates
    the verbs above and explains their results. None of them computes
    anything a verb could. All of them start from ``nsctl data status``,
    explain and confirm a command that writes before running it, and put only
    profile samples into the conversation:

    ``ns-data-start``
        Asks what the person wants to know and where the files are, checks the
        container engine, runs ``init``, and routes to the next skill.
    ``ns-data-inventory``
        ``scan``: presents groups in plain language; the person confirms,
        splits or renames them.
    ``ns-data-profile``
        ``profile``: surfaces the decisions, records fixes, chooses whether to
        pin.
    ``ns-data-register``
        ``register`` and, if the sources are still changing, ``snapshot``.
        Checks row counts.
    ``ns-data-query``
        ``connect``; answers the first question with visible SQL; saves it to
        ``queries/``.
    ``ns-data-understand``
        ``profile --relationships``, ``understand``, ``approve``/``reject``,
        ``docs``. Uses no modeling vocabulary unless asked.
    ``ns-data-store``
        Opt-in. An environment, then the person's choice: their own bucket via
        ``storage``, or ``bundle add storage``; then ``land``.
    ``ns-data-next``
        Pointers to export, refresh, Trino and the Librarian.

    ``nsctl-onboard`` shall route a person who says they have data to explore
    to ``ns-data-start`` before anything about repositories.

Verification
------------

* Unit tests over a fixture corpus: a clean CSV; a messy CSV (byte-order
  mark, header junk, leading-zero identifiers); partitioned Parquet; NDJSON
  with drift; a two-sheet workbook; a zip. Golden JSON for groups, types,
  issues and relationships. The engine runner is faked in unit tests and real
  in one integration test.
* A Robot Framework suite with only a container engine present runs ``init``
  → ``scan`` → ``profile`` → ``register`` → ``sql`` → ``understand`` →
  ``approve`` → ``docs``. It checks row counts against the sources, that the
  internal model passes ``nsctl model inspect``, and that ``out/views.sql``
  runs unchanged in a plain DuckDB.
* A second suite creates an environment and runs ``land`` twice: into a
  bucket created with the AWS CLI from ``storage`` output, and into a bucket
  from ``bundle add storage``, starting from an empty ``HMD_HOME`` with no
  repository checkouts.
* The bundled skills pass ``TestBundledSkillCommandPathsExist``, and an agent
  evaluation compares skills plus verbs, verbs alone, and neither, on
  correctness of answers, groups and types, and turns to a first answer.
