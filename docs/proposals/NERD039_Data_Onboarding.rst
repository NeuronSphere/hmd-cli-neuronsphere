.. NERD039 Data Onboarding

NERD039 Data Onboarding
=======================

.. req:: A newcomer can explore their own data files with nsctl and nothing else
    :id: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data`` shall take a person who has never seen NeuronSphere from a
    folder of their own files (CSV, TSV, Parquet, JSON, NDJSON, Excel, or a zip
    of those) to answers in SQL. They need only ``nsctl`` and a container
    engine: no Python, no environment, no bucket, no account, and no
    repository.

    Every step that decides something (which files form a table, what type a
    column is, which columns join) shall be done by a verb that emits JSON, so
    that a person, a bundled agent skill, and a test all get the same answer.

    What ``nsctl`` learns is kept in a workspace under ``HMD_HOME``. The
    NeuronSphere model (``.hms`` per NEP051) is part of that internal state.
    The person shall never need to read or write it. They approve proposals in
    plain language and receive only rendered outputs, in a folder they choose:
    a data dictionary, a relationship diagram, ``CREATE VIEW`` DDL, and
    questions with working SQL. Those outputs are rendered by Mickey.

    Turning a workspace into a repository, storage, a NeuronSphere
    environment, Trino and the Librarian shall be opt-in steps taken after the
    first answer, never prerequisites for it.

Motivation
----------

``nsctl`` makes the local platform easy to start for people who already know
what a repo class is. A newcomer arrives with data, not repositories. Today
the guided first run deploys the ``analytics`` bundle, roughly thirty instances,
before it can show anything, and no ``nsctl`` verb reads, loads or queries a
data file. The bundled ``nsctl-onboard`` skill is entirely about repositories
and deploys.

The first thing a newcomer should see is their own data becoming easy to
explore. Someone who is not using NeuronSphere needs the rendered results (the
DDL, the documentation), not model files, table definitions or a repository.
Platform concepts can follow, one at a time, as each starts to pay for
itself.

How this fits the other proposals
---------------------------------

* **NERD032 (data model inspection).** ``nsctl data`` is a new source for
  the same model. Its profiles enter as ``inferred`` observations through a
  new ``nsdata`` inspector, and a person's approvals as declared NEP051
  sources through a new ``modelsource`` inspector (SPEC007). NERD032 is
  amended in two places: an out-of-scope carve-out for profiling data files,
  and two rows in its authority table.
* **NERD032 SPEC015 and Mickey.** Rendering the outputs is a generator in
  NERD032's sense: a Mickey template set (``hmd-lib-mickey``) run over
  the NERD032 export. ``nsctl`` runs the ``hmd-tf-mickey`` transform image
  (SPEC002, SPEC008) and contains no renderer of its own.
* **NERD033 (perspective derivation).** Untouched. Perspectives appear only
  after a workspace is exported to a repository (SPEC011).
* **NEP051 (model sources and perspective packs).** The internal model is
  NEP051 files. NEP051 gains a ``group:`` source kind for file groups.
* **NEP048 (query first) and NEP049 (adoption tiers).** ``nsctl data`` is
  NEP048's ladder (raw query, then an optional model) run locally with no
  platform. ``understand``/``approve`` is NEP049's Understand loop at tier 0.
* **Librarian NERD012 (content type schemas).** ``table.yaml`` uses
  NERD012's field names, so a group becomes a content item type by
  translation. A pinned group also supplies the column list the
  ``content_item_to_trino_table`` transform template needs.
* **NEP050 (data products).** Saved queries are the natural input to
  promotion. Not in scope here.

Open questions
--------------

* **Semantic layer overlap.** The proposed semantic layer's portable core
  plans its own DuckDB-backed MCP server and manifest. That overlaps with
  ``data connect`` and the rendered outputs, and one of them should own it.
* **Type vocabulary.** ``table.yaml`` uses Arrow type names (NERD012), and
  NEP051 ``columns`` uses Trino types. NEP051 lists this as open.
* **Proposal quality.** Rules over profile evidence may propose poorly on
  real exports. The agent evaluation in Verification is the gate before
  ``understand`` ships.
* **The Mickey image.** ``hmd-tf-mickey`` builds from ``python:3.9``, which is
  past end of life, and needs a supported base. Whether its image is
  published to the public namespace, and whether it renders local templates
  with no network given ``MICKEY_SKIP_DEPS``, must be confirmed by a spike
  before SPEC008 is built.

Scope and terminology
---------------------

*Workspace*
    The state ``nsctl data`` keeps for one body of data, under
    ``$HMD_HOME/data/<workspace>/``. It is ``nsctl``'s, not the person's: it
    is not a repository and not a repo class. SPEC011 turns one into a
    repository on request.

*Source*
    A file or directory the person pointed ``nsctl data`` at. Sources are
    read in place and never modified.

*Outputs*
    The rendered files a person reads (SPEC008), written to a folder they
    choose. Outputs contain no NeuronSphere files.

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
    NEP051 concepts and sources (``.hms``, ``.source.yaml``) in the
    workspace's ``model/``, with each source's provenance ``inferred`` (a
    proposal) or ``declared`` (a person approved it), as NEP051 defines them.

Out of scope: database dumps and live database connections (reported as
unsupported by ``scan``); cloud buckets; creating any cloud resource;
scheduling and refresh pipelines; a user-facing modeling language.

.. spec:: The workspace lives in HMD_HOME
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data init <workspace> [<source>...]`` shall create
    ``$HMD_HOME/data/<workspace>/`` with this layout and nothing else::

        data.yaml                 # sources, outputs folder, storage target, progress
        tables/<table>.table.yaml # group: path pattern, format, reader options, pinned schema
        profile/<table>.json      # saved profiles (SPEC004)
        model/                    # internal model (SPEC007)
        queries/*.sql             # saved answers
        snapshot/                 # optional Parquet copies (SPEC005)
        catalog.duckdb            # generated views (SPEC005)

    Every other ``nsctl data`` verb acts on the current workspace, which is
    the one most recently initialised or selected with ``nsctl data use
    <workspace>``. ``--workspace`` overrides it for one command. ``nsctl data
    list`` shows the workspaces, and ``nsctl data delete <workspace>`` removes
    one. It never touches sources or outputs.

    ``catalog.duckdb`` and ``snapshot/`` are derived and can be deleted and
    regenerated. ``data.yaml`` records how far each table has come (grouped,
    profiled, pinned, registered, understood, rendered, landed), so that
    ``nsctl data status`` and every bundled skill can resume where the last
    session stopped.

    A workspace is local to one ``HMD_HOME``. Sharing the work means sharing
    the outputs (SPEC008) or exporting a repository (SPEC011).

.. spec:: The engines run in pinned containers, not in the binary
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl`` is built with ``CGO_ENABLED=0`` and keeps its 50 MB size
    budget (NERD002 SPEC013). It shall link neither DuckDB, a C++ library, nor
    a template engine. It runs two images, each pinned by digest (NERD030):

    * ``hmd-img-duckdb``: the DuckDB CLI with the extensions it needs
      (``httpfs``, ``excel``, ``ui``) installed, so that no step downloads an
      extension at run time. ``nsctl`` generates every SQL statement in Go,
      runs it with ``duckdb -json``, and parses the result.
    * The Mickey image (SPEC008): ``hmd-tf-mickey`` with the outputs'
      template set installed, run with ``MICKEY_SKIP_DEPS`` so that rendering
      needs no network.

    Sources are mounted read-only, the workspace read-write, and the outputs
    folder read-write, each at the same absolute path it has on the host (as
    the deploy runner binds ``$HMD_HOME``, NERD002 SPEC010). Views written
    into ``catalog.duckdb`` therefore resolve unchanged from a DuckDB client on
    the host. Until storage is configured (SPEC009) neither container has a
    network.

    .. warning::

       **Implemented, with one extension dropped.** ``ui`` is not in the
       image: DuckDB's UI extension fetches its web assets from the internet
       when it starts, so it cannot work offline from a baked image.
       ``nsctl data connect`` points to ``duckdb -ui`` on the host instead.
       The image carries ``httpfs``, ``excel``, ``json`` and ``parquet``.

.. spec:: scan proposes groups
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data scan [<path>...]`` shall walk the workspace's sources (and
    any paths given, which it adds to them), detect each file's format (by
    content, not only by extension), and propose groups: files whose schemas
    are compatible and whose paths share a pattern. Hive-style ``key=value``
    directories become partition columns. Each sheet of an Excel workbook and
    each member of a zip is a candidate file of its own.

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
    ``<zip>!<member>``. ``paths`` are relative to a source root recorded in
    ``data.yaml``. A group can be renamed, split or merged with ``--name``,
    ``--split`` and ``--merge``. A rescan reports new, changed and missing
    files against the existing groups instead of proposing them again.

    .. warning::

       **Implemented in part.** Zip archives are listed as unsupported, with
       "extract it and scan the folder", rather than read member by member.
       ``--split`` and ``--merge`` are not built; a group is split or merged by
       editing its ``table.yaml``. A rescan reports files no table reads and
       tables that match no file, but not changed files; snapshots report
       those (SPEC005).

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
    Every profile is saved to ``profile/<table>.json``, with relationship
    evidence in ``profile/_relationships.json``. These files are what the
    ``nsdata`` inspector reads (SPEC007). A profile is evidence and never a
    decision.

    .. warning::

       **Implemented, with these choices.** Candidate pairs are every column
       of another table whose type can hold the key: text or integer columns
       against a text or integer candidate key, except an integer column
       against a text key (``7`` is never ``C007``). Values are compared as
       text. A table's own columns are not compared with its key. A run
       measures at most 2,000 pairs and says when it stopped. Each join also
       records ``name_match``: the column names agree, or the column is named
       after the key's table.

.. spec:: register makes tables queryable in place
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data register`` shall (re)build ``catalog.duckdb`` with one view
    per table, reading the files in place with the reader options and casts in
    its ``table.yaml``. ``--pin`` writes the current inferred schema into
    ``schema``. As with NERD012's ``infer`` mode, a recorded schema never
    changes on its own after that: a file that no longer matches is reported
    by ``status`` and ``register`` instead of changing the view's type.
    ``schema_mode: declared`` means a person wrote or edited the schema, and
    ``register`` refuses a file that does not match it.

    ``nsctl data snapshot [<table>]`` copies a table to Parquet under
    ``snapshot/`` and points its view there, so later edits to the sources do
    not change answers. The snapshot records the content hash of every source
    file it was taken from.

.. spec:: sql, connect and status
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data sql [<statement>]`` runs one statement against the catalog
    and prints the result, or opens an interactive DuckDB shell when given
    none. ``--json`` returns rows as JSON. The catalog is read-only to
    ``sql`` unless ``--write`` is given. ``--save <name>`` keeps the statement
    in ``queries/``, where ``docs`` includes it.

    ``nsctl data connect`` prints the catalog's path and how to open it from
    the DuckDB CLI, the DuckDB UI, DBeaver, and an agent through an MCP server
    configuration. It prints instructions only and installs nothing.

    ``nsctl data status`` reports each table's progress (SPEC001), drift
    since its pin, sources changed since its snapshot, whether the outputs are
    older than the model, and the next suggested command.

.. spec:: understand proposes meaning; a person approves it
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    Meaning is learned the way NERD032 learns any model: by inspecting files
    and consolidating observations. ``nsctl data`` writes no model element
    except through that path. Two inspectors are added to NERD032 SPEC001:

    * ``nsdata`` reads a workspace's ``tables/`` and ``profile/``. Groups
      become bindings, pinned schemas become attributes at the authority of
      a pinned table, and everything else (inferred types, candidate keys,
      value overlap) is ``inferred`` observations with ``evidence``
      confidence. It works the same way NERD032 SPEC002 treats any
      inference.
    * ``modelsource`` reads NEP051 ``*.source.yaml`` files at an authority set
      by their ``provenance``. It is not specific to ``nsctl data``: any
      model repository's sources are read the same way.

    ``nsctl data understand`` shall run ``nsctl model inspect`` over the
    workspace and turn each element whose best authority is ``inferred`` into
    a proposal: an entity (one or more groups standing for one business
    thing), its name and description, a key, or a relationship. It writes each
    proposal as NEP051 files under ``model/`` with ``provenance: inferred``: a
    concept ``.hms`` and a source whose ``from`` is ``group: <table>`` (the
    source kind NEP051 gains for this case) and whose ``evidence`` holds the
    profile figures. Nothing uses machine learning. An agent skill may suggest
    better names, and ``nsctl`` records whatever a person approves.

    Each proposal in the JSON output carries an identifier, the evidence
    behind it, and a yes/no question phrased without modeling vocabulary, for
    example: *"98% of orders.cust_id values appear in customers.id. Is every
    order placed by one customer?"*

    ``nsctl data approve <id> [--name <name>] [--description <text>]`` rewrites
    the proposal's files with ``provenance: declared``. Inspection then ranks
    them at NEP051-declared authority, above everything but ``.hms``.
    ``nsctl data reject <id>`` deletes the files and records the rejection in
    ``data.yaml``, so ``understand`` does not ask again. After each decision
    the workspace is inspected again. A disagreement NERD032 reports (for
    example, an approved key that the data contradicts) is shown to the
    person, never resolved silently.

    .. warning::

       **Implemented, with these choices.**

       * The workspace's ``model/`` folder is laid out as a NEP051 model
         repository (``src/sources``, ``src/schemas/<workspace>``). The
         ``nsdata`` inspector also inspects that folder with the ``hms`` and
         ``modelsource`` inspectors, so ``nsctl model inspect <workspace>`` is
         the whole model. Concepts are in a namespace named after the
         workspace.
       * A table proposal is ``src/sources/<table>.source.yaml``, yielding one
         concept named after the table with every column. A join proposal is
         its own source, ``<from>__<column>__<to>.source.yaml``, so it can be
         approved or rejected on its own. Approving writes the concept's
         ``.hms`` (the key as ``business_id``) and turns the source
         ``declared``. A join is approved only after both of its tables, and
         renaming a table rewrites the joins still waiting to use the new
         name.
       * A source that yields exactly one noun binds it to its group's
         location, which is how consolidation links an approved concept to
         the ``nsdata`` table it came from, under any name. A source that
         yields several nouns from one row binds none of them by location.
       * What ``understand`` asks about is decided from these files and the
         evidence (a table or likely join with no source is new; a source
         that is ``inferred`` is still open; ``rejected`` in ``data.yaml`` is
         never asked again). The NERD032 inspection runs after each decision
         for its disagreements, together with one check of its own:
         ``key-contradicted``, an approved key the latest profile finds
         repeated or empty.
       * A join is proposed when at least 90% of the column's distinct values
         (and at least two) are in the key, and either the names agree or
         there are at least three distinct values.
       * Columns use Trino type names (NEP051), mapped from the Arrow types
         of the table definition. An Arrow type with no Trino equivalent is
         kept as it is.

    Because the internal model is ordinary NEP051 files read by ordinary
    inspection, ``nsctl model diff``, ``impact`` and ``export`` work on a
    workspace with no extra code. No new ``nsctl model`` verb is introduced.

.. spec:: docs renders the outputs with Mickey
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data docs [--out <dir>]`` shall render the outputs into ``<dir>``,
    by default ``./ns-data-out/`` under the directory it is run from. The
    folder is remembered in ``data.yaml`` for the next run. The outputs are:

    * ``DATA.md``: a data dictionary. Every table and named view, its columns
      with type, meaning and quality notes, and the files it came from.
    * ``relationships.md`` and ``relationships.puml``: a relationship diagram
      and a table of joins with their evidence.
    * ``views.sql``: ``CREATE VIEW`` statements for every declared entity.
      They are plain SQL that runs unchanged in any DuckDB, and they name no
      NeuronSphere concept.
    * ``questions.md``: the saved queries and questions the data can answer,
      each with SQL that runs.

    Proposals that are still only ``inferred`` appear marked as unconfirmed;
    rejected ones do not appear.

    ``nsctl`` renders nothing itself. It writes a Mickey context (the
    workspace's NERD032 export, its profiles and its saved queries) and runs
    the Mickey image (SPEC002) with that context and the template set. The
    template set lives in its own repository, ``hmd-tmpl-data-docs``, and is
    installed in the image, which is pinned by digest. This is a generator
    under NERD032 SPEC015, and a new output format is a template, not a
    change to ``nsctl``.

    ``docs`` then runs ``views.sql`` against ``catalog.duckdb``, so the named
    views a person queries are the ones the outputs document.

    ``docs`` writes only the files above, and it overwrites only files it
    wrote before: it records their content hashes in ``data.yaml`` and refuses
    to replace a file that has since been edited, unless ``--force`` is given.
    It never deletes anything in the folder.

    .. warning::

       **Implemented, with these differences.**

       * The renderer image is ``hmd-tmpl-data-docs``, built ``FROM`` the
         published ``hmd-tf-mickey:1.0.91`` with the template set at
         ``/opt/hmd-tmpl-data-docs``. It is not published yet, and so not yet
         pinned by digest. ``HMD_DATA_DOCS_IMAGE`` names a local build.
         ``hmd-tf-mickey`` is built on ``python:3.9``, which is past end of
         life, and is about 1.9 GB on disk; both need fixing before this
         ships to newcomers.
       * The outputs folder is not mounted into the container. Mickey renders
         into a staging folder inside the workspace (a new one for every run,
         removed afterwards: a bind mount of a folder deleted and re-created
         at the same path can reach the container stale), and nsctl copies the
         results across after the edit check. A refused run therefore changes
         nothing in the outputs folder.
       * ``views.sql`` holds a view per table, reading the files where they
         are, then a view per approved table renamed by the person. It runs
         in any DuckDB. A table approved under its own name needs no second
         view.
       * ``questions.md`` holds the saved queries, then one suggested query
         per approved join.

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
    <name>`` resolves it anonymously. ``nsctl data`` never deploys anything
    itself.

    While a target is configured, the engine container joins that
    environment's network.

    .. warning::

       **Implemented, with these specifics.**

       * Every local environment is an account of the one Floci, chosen by
         the access key (the environment's account id; the secret is fixed).
         ``storage`` therefore shows the host endpoint, the in-network
         endpoint ``http://neuronsphere:4566`` and the control plane's
         network, and lists buckets with nsctl's own S3 client, so no AWS CLI
         is needed. The secret is shown only with ``--reveal``.
       * ``land`` writes ``<target>/raw/<table>/data.parquet``, one object per
         table, and records the source hashes in ``data.yaml`` under
         ``landed``. ``--force`` writes it anyway. The target is remembered
         under ``storage``. A landed table's view reads the bucket, so the
         environment must be running to query the workspace, and ``status``
         reports a landed table's changed source files as it does a
         snapshot's.
       * The engine gets the credentials as a session-scoped DuckDB secret on
         stdin, never on a command line and never in the catalog; ``connect``
         prints the same secret for a DuckDB on the host.
       * ``instance:<name>`` reads ``bucket_name`` from the instance's
         recorded resource output. ``hmd-inf-s3bucket`` emits it from 0.1.23,
         so the ``storage`` bundle (repository ``hmd-stack-storage``) pins
         ``== 0.1.23``, as one instance named ``storage``.
       * Verified end to end on an environment started with
         ``--substrate none``: the bundle pulled anonymously from an OCI
         registry, ``env apply`` deployed the bucket from the bundled
         artifact, and ``land --to instance:storage`` wrote into it. The
         bundle is not published to ``ghcr.io/neuronsphere/stacks/storage``
         yet, and the run used an existing ``HMD_HOME``, not an empty one.

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
        ``connect``; answers the first question with visible SQL; saves it
        with ``sql --save``.
    ``ns-data-understand``
        ``profile --relationships``, ``understand``, ``approve``/``reject``,
        then ``docs``, asking where the outputs should go. Uses no modeling
        vocabulary unless asked.
    ``ns-data-store``
        Opt-in. An environment, then the person's choice: their own bucket via
        ``storage``, or ``bundle add storage``; then ``land``.
    ``ns-data-next``
        Pointers to ``export --repo``, refresh, Trino and the Librarian.

    ``nsctl-onboard`` shall route a person who says they have data to explore
    to ``ns-data-start`` before anything about repositories.

    .. warning::

       **Partly implemented.** ``ns-data-start``, ``ns-data-inventory``,
       ``ns-data-profile``, ``ns-data-register`` and ``ns-data-query`` are
       bundled, and ``nsctl-onboard`` routes to ``ns-data-start``.
       ``ns-data-understand`` is bundled with SPEC007 and SPEC008, and
       ``ns-data-store`` with SPEC009; ``ns-data-next`` waits for the verbs it
       orchestrates. With no ``--split``/``--merge`` (SPEC003),
       ``ns-data-inventory`` splits or merges a group by editing ``paths`` in
       the table definitions, and ``ns-data-profile`` records a fix by merging
       the profile's suggestion into ``casts`` or ``reader_options``.

.. spec:: export turns a workspace into a repository
    :id: HMD_CLI_NEURONSPHERE_NERD039_SPEC011
    :links: HMD_CLI_NEURONSPHERE_NERD039
    :status: proposed

    ``nsctl data export --repo <dir>`` is the one step that makes a
    repository, for a person or team that wants the work in git and in
    NeuronSphere. It writes a repo class:

    * a BACON manifest created as ``nsctl repoclass init`` creates one, with
      ``build.commands`` empty, since nothing in it is built;
    * the internal model as a NEP051 model repository: concepts, and sources
      under ``src/sources/``;
    * the table definitions and saved queries;
    * the NERD032 export document, for generators;
    * the outputs, rendered into the repository.

    It refuses a non-empty directory it did not create. It records the
    repository in ``data.yaml`` and re-exports into it on request, so a person
    can keep exploring in the workspace and export again. Perspectives
    (NERD033), dbt projects and other manifestations are generated from the
    exported repository by generators under NERD032 SPEC015. ``nsctl data``
    does not produce them.

Verification
------------

* Unit tests over a fixture corpus: a clean CSV; a messy CSV (byte-order
  mark, header junk, leading-zero identifiers); partitioned Parquet; NDJSON
  with drift; a two-sheet workbook; a zip. Golden JSON for groups, types,
  issues and relationships. The engine runner is faked in unit tests and real
  in one integration test.
* A spike, before SPEC008 is built: the Mickey image on a supported base
  renders a template set baked into the image from a context file, with
  ``MICKEY_SKIP_DEPS`` and the network disabled.
* A Robot Framework suite with only a container engine present runs ``init``
  → ``scan`` → ``profile`` → ``register`` → ``sql`` → ``understand`` →
  ``approve`` → ``docs``. It checks:

  * row counts match the sources;
  * the internal model passes ``nsctl model inspect``;
  * the outputs folder holds only the rendered files;
  * ``views.sql`` runs unchanged in a plain DuckDB;
  * no source file changed.
* ``export --repo`` produces a repository that ``nsctl repoclass validate``
  accepts and whose model ``nsctl model inspect`` reads with no
  disagreements.
* A second suite creates an environment and runs ``land`` twice: into a
  bucket created with the AWS CLI from ``storage`` output, and into a bucket
  from ``bundle add storage``, starting from an empty ``HMD_HOME`` with no
  repository checkouts.
* The bundled skills pass ``TestBundledSkillCommandPathsExist``, and an agent
  evaluation compares skills plus verbs, verbs alone, and neither, on
  correctness of answers, groups and types, and turns to a first answer.
