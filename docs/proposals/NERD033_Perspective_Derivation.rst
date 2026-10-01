.. NERD033 Perspective Derivation

NERD033 Perspective Derivation
==============================

.. req:: nsctl derives perspectives from the files that realise a model, and ships none
    :id: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    A perspective shall be data only: a definition (the keys a technology
    needs beside the core ``.hms`` schema, their kinds, enum values and type
    mappings) and per-noun values. It shall not contain, imply or require any
    particular code generator or template language.

    ``nsctl`` shall embed no perspective definition. Given files that already
    realise a model (SQL DDL, dbt projects, transform YAML), it shall derive the
    definition and the values a generator would need to produce those files
    again, keep them as working state beside the inspected model, and write
    them into a repository only when asked.

Motivation
----------

NERD032 moved everything ``.hms`` cannot say into perspectives. Its spike
then baked them into ``nsctl`` in three places:

* four definitions (``trino``, ``dbt``, ``postgres-view``,
  ``librarian-content``) compiled in with ``go:embed``
  (``internal/perspective/defs``);
* SQL type knowledge as Go tables: the positional order of type parameters
  and type aliases (``internal/perspective/sqltype.go``);
* in each inspector, the perspective's name, its key names, which syntax
  slot fills which key, and the ``_source``/``_staging``/``_final``/``_ux``
  layer rule.

The third is the real problem. Each inspector is a hand-written reader for a
perspective that exists only in Go. A new technology, a project with its own
conventions or a renamed key each needs an ``nsctl`` release, and nothing can
be learned about a project that ``nsctl``'s authors did not foresee.

Perspectives exist to drive generation. A template set (internally Jinja,
rendered by ``hmd-cli-mickey``) reads a noun's ``.hms`` attributes and its
``extensions[<perspective>]`` and writes a manifestation: Trino DDL, a dbt
model, a dashboard. The perspective carries only what the template needs and
the core schema lacks: a storage format, the physical data type of each
attribute, the table's schema name. ``hmd-tmpl-modeler``'s ``django-models``
template is the existing example.

Most projects start the other way round: the DDL and the dbt project exist,
no template does, and nobody has written a perspective. This NERD makes the
perspective something ``nsctl`` *derives* from those files, so a project can
adopt model-driven generation without first writing, by hand, a description
of what it already has.

Terminology
-----------

*Perspective definition*
    ``<name>.perspective.json`` in the Modeler shape (NERD032 SPEC007), plus
    the generator-agnostic keys of SPEC004 below.

*Perspective values*
    A noun's values for one perspective: the ``<noun>.<perspective>.hms``
    sidecar.

*Format parser*
    Code that turns one file format into a neutral syntax tree. It knows
    syntax, not meaning.

*Perspective IR*
    Derived definitions and values held in the inspection store, with the
    evidence for each, before anyone has written them anywhere.

*Materialise*
    Write a perspective from the IR into a repository.

.. spec:: Perspectives are data, and nsctl ships none
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    ``nsctl`` contains no perspective definition, no perspective name and no
    perspective key. The registry of definitions an inspection uses is, in
    order:

    #. ``src/perspectives/*.perspective.json`` in each inspected repository;
    #. perspectives in the perspective IR (SPEC005) for the same roots.

    A definition in a repository wins over a derived one of the same name.
    A perspective nothing declares is derived as part of every inspection
    (SPEC003), so ``nsctl model inspect`` shows the same model whether or not a
    person has looked at the derivation; ``nsctl model perspective derive``
    shows what was derived and why.

    This amends NERD032 SPEC007, which had ``nsctl`` embed the definitions its
    inspectors need. The four spike definitions become test fixtures, and the
    acceptance test of SPEC003.

.. spec:: Format parsers produce neutral syntax trees
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    A format parser reads one format and reports, per object it declares or
    names (a table, a view, an ``INSERT`` target), an *object observation*:
    the object's dialect, its physical location, and its properties keyed by
    the grammar's names, each with the class the grammar gives its value
    (identifier, keyword, text, list, flag or data type). For SQL these are
    ``catalog``, ``schema_name``, ``table_name``, ``table_type``, every
    ``WITH`` property under its own name (``format``, ``partitioned_by``,
    ``external_location``, ...), and per column ``datatype`` (parsed into its
    base type, arguments, the grammar's names for them and the type's SQL
    category), ``is_nullable`` (stated by ``NOT NULL``) and ``is_partition``.
    A select list's columns are reported separately, with names and order
    only. A parser names no perspective, assigns no namespace and applies no
    project convention. Grammar facts stay in the parser: SQL's own type
    synonyms (``int`` is ``integer``) and argument names (``DECIMAL(precision,
    scale)``).

    Inspectors keep what is not a perspective: discovering files, rendering
    the literal parts of templated SQL (NERD032 SPEC004), lineage, provide and
    reference observations, and core attributes (name, position, core type
    when a definition gives one, ``required`` from ``NOT NULL``).

    The first parser is the existing SQL DDL subset
    (``internal/inspect/sqlddl``), for Trino SQL in transforms. dbt
    ``schema.yml``, Postgres views and Librarian exports follow; until then
    their inspectors name their bindings themselves, and only their
    definitions are derived (from the values, SPEC003).

.. spec:: Derivation from files alone
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    ``derive`` takes the trees of one format family from the inspected roots
    and proposes one perspective:

    #. **Name pattern.** Physical names are compared across objects. Objects
       with the same table name (run-time ``{placeholder}`` segments removed),
       in schemas that differ only in their last ``_``-separated segment, are
       one noun's objects: ``billing_staging.aws_billing`` and
       ``billing_final.aws_billing`` give ``<namespace>_<layer>``. The varying
       segment becomes the definition's ``binding_key`` (named ``layer``; a
       person may rename it), an enum whose values are in the order data flows
       between them, read from ``INSERT ... SELECT`` lineage between two
       objects of one noun. A noun's primary binding is its most downstream
       one whose columns are declared (a view's are only selected). One table
       alone shows no varying segment, so it binds without a layer, in its own
       schema's namespace. The pattern replaces NERD032 SPEC004's built-in
       layer suffixes.
    #. **Align.** Each object is aligned to a noun: the ``.hms`` noun of the
       same identity when one exists, otherwise the identity the name pattern
       gives.
    #. **Subtract the core.** Paths that the core model already explains
       (the noun's name, attribute names, ``required``) are removed. The
       remaining paths are the perspective's material.
    #. **Keys.** Each remaining path becomes a key at its attach point
       (``entity`` for object paths, ``attribute`` for ``columns[]`` paths),
       named after the path's last element(s): ``with.format`` → ``format``,
       ``columns[].type`` → ``datatype``, ``columns[].not_null`` →
       ``is_nullable`` (inverted). A path's naming is a parser fact, so a
       format family always derives the same key names.
    #. **Kinds.** A flag is ``bool``, with the default the grammar leaves
       unstated. A keyword is an ``enum``. A data type is an ``enum`` with one
       value per base type, its arguments as ``parameters`` with their
       ``position``, and the spellings seen as ``aliases``. Text is an
       ``enum`` when it has at most six distinct bare-word values and each is
       seen twice on average; otherwise, like an identifier or a list, it is
       ``short_text``. An identifier that equals the noun's name in most
       objects defaults to ``{"ref": "name"}``. The thresholds are in the
       evidence, so a reviewer can see why a key is an enum.
    #. **Type mappings.** For each ``datatype`` enum value, ``hms_type`` is the
       core type of the ``.hms`` attributes it is paired with (same noun, same
       attribute name). A value paired with two core types takes the majority
       and is flagged for review, with the others as exceptions. A value paired
       with none takes the nearest ``.hms`` type of its SQL category
       (character → ``string``, exact and approximate numerics → ``integer`` /
       ``float``, ``DATE`` → ``timestamp``), and an unknown type is flagged for
       review rather than guessed.
    #. **Existing definition.** If the registry (SPEC001) already has a
       definition of the dialect's name, nothing is derived: properties map
       into it by key name or by a key's ``aliases`` (a renamed key keeps its
       old name as one), its ``name_pattern`` decides identity, and a property
       it has no key for is reported once, as information, and given no value.
       Enum values outside it are reported by validation.

    Bindings an inspector still names itself (SPEC002) get a definition
    derived from their values the same way: the keys seen, each key's kind
    from its values.

    Every derived key, enum value, pattern and mapping carries its evidence:
    the objects that support it, the exceptions, and the rule that produced
    it. Derivation is deterministic: the same files give the same proposal.

    Acceptance: deriving from ``hmd-config-transform-reporting`` and
    ``hmd-config-billing-transforms`` gives a definition equivalent to the
    spike's embedded ``trino`` (``layer`` as ``binding_key``, the datatype
    enum and its ``hms_type`` values) and the same values the spike's
    inspector produced.

.. spec:: The perspective data shape is generator-agnostic
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    Nothing in a definition or a value is written for a particular template
    language. Knowledge the spike kept in Go becomes data, as optional keys a
    Modeler reader ignores:

    * ``aliases`` on an enum value: other spellings that mean it
      (``int`` → ``integer``); and on an extension, other names of the key
      (a property renamed by an edit keeps the parser's name as an alias).
    * ``position`` on a parameter: its order, since a JSON object has none
      (``decimal``: ``precision`` 1, ``scale`` 2).
    * ``name_pattern`` on a definition: the physical name as a sequence of
      parts, each a literal or a reference to the noun (``namespace``,
      ``name``) or to an entity key (``layer``):
      ``[{"ref": "namespace"}, {"lit": "_"}, {"ref": "layer"}]``.

    Strings that are themselves templates are replaced: a default of
    ``"{entity.name}"`` becomes ``{"ref": "name"}``. A run-time placeholder in
    a physical name (NERD032's ``template`` value) is to be written
    ``{"runtime": "<param>"}`` inside a ``name_pattern``, not as text; the
    first slice still carries the per-binding ``template`` value as the text
    NERD032 renders, and that remains to be done.

    ``nsctl model inspect <noun> --context`` prints, and ``--out <dir>`` writes as
    ``<namespace>.<name>.json``, the merged context a generator reads: the ``.hms`` document with each
    perspective's values under ``extensions[<perspective>]``, as
    ``hmd-schema-loader`` merges sidecars. It is plain JSON, usable by any
    generator and as a ``hmd-cli-mickey`` context.

.. spec:: The perspective IR and materialisation
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    Derived perspectives live in the inspection store (NERD032 SPEC005) beside
    the snapshot they were derived from: definitions and evidence (the values
    are the snapshot's model). A perspective in the IR is ``derived`` or
    ``edited``. The edits are ``rename`` (the perspective), ``rename-key``,
    ``drop-key`` and ``hms-type`` (set the core type of a data type value,
    which also settles a review flag). Edits are stored per set of inspected
    roots as operations on the derivation, so deriving again after the files
    change keeps them; an edit that no longer applies is reported as stale,
    and one that does not apply when it is made is refused. Unlike snapshots,
    edits are a person's work: a store rebuild for a new schema version keeps
    them.

    ``materialise <perspective> --to <repo>`` writes
    ``src/perspectives/<perspective>.perspective.json`` and one
    ``<noun>.<perspective>.hms`` sidecar per noun beside its ``.hms`` (or into
    ``src/schemas/<namespace>/`` when the noun has none) in the named
    repository. It is the only command in this NERD that writes into a
    repository, and it never writes into one it was not named.

    Once materialised, the repository copy is the authority (SPEC001).
    Inspection reads its sidecars at ``.hms`` authority, so a later change to
    the files that realise it shows up as a disagreement between a declared
    and an observed value. That is the drift check NERD032 demonstrated.

.. spec:: nsctl model perspective
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    * ``nsctl model perspective list [path...]`` lists the definitions in
      the registry with their origin (a repository file, or ``derived``).
    * ``nsctl model perspective derive [path...]`` inspects again, derives
      (SPEC003), stores the result in the IR and prints the proposal with its
      review flags. ``--evidence`` adds the rule, support and examples per
      item; ``--json`` prints the derivations.
    * ``nsctl model perspective show <perspective> [path...]`` prints a
      definition as its file would be.
    * ``nsctl model perspective edit <perspective> <op> <arg>... [--path
      <dir>]`` records an edit (SPEC005). It needs ``HMD_HOME``.
    * ``nsctl model perspective materialise <perspective> [path...] --to
      <repo>`` (SPEC005).

    ``nsctl model perspectives`` is an alias of ``nsctl model inspect
    perspective``. ``nsctl model inspect --hms`` and ``--out`` also write each
    perspective's definition under ``src/perspectives/``, so an export reads
    back without the repositories it came from.

.. spec:: Render-and-compare, when a generator exists
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    When a project has a generator for a perspective, ``derive --check
    <command>`` runs it on the merged contexts (SPEC004) written to a
    scratch directory, parses its output with the same format parser, and
    compares the trees with the originals. Comparison is structural:
    whitespace, keyword case and identifier quoting are ignored. The
    residue is reported per object: a path in the original that the output
    lacks means the perspective or the template does not cover something; a
    path only in the output means the template emits something the files do
    not have.

    The generator is any command that reads a context directory and writes
    an output directory. ``hmd mickey build`` with the project's templates is
    the internal one. ``nsctl`` does not render templates itself.

Open questions
--------------

#. Where should perspectives shared across projects (a company's ``trino``)
   live: a perspective-pack repository distributed like stacks and plugins
   (NERD016–018), or the Modeler (``hmd-ms-mickey``) once NERD003 of
   ``hmd-lib-django-modeling`` makes its perspectives Git artifacts?
#. Do the ``name_pattern`` parts and key ``aliases`` belong in the Modeler's
   shape upstream, so ``hmd-app-modeler`` can edit them?
#. A dropped key survives materialisation only as an information finding per
   property. Should a definition be able to say "ignore this property"
   explicitly?
