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
    With no definition at all, inspection still produces the core model and
    the format parsers' trees; ``nsctl inspect`` says that no perspective
    covers them and suggests ``nsctl inspect perspective derive``.

    This amends NERD032 SPEC007, which had ``nsctl`` embed the definitions its
    inspectors need. The four spike definitions become test fixtures, and the
    acceptance test of SPEC003.

.. spec:: Format parsers produce neutral syntax trees
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    A format parser reads one format and produces, per object it declares
    (a table, a view, a dbt model or source), a tree whose node paths come
    from the format's grammar: ``name.catalog``, ``name.schema``,
    ``name.table``, ``kind`` (``table``/``view``), ``with.<property>``,
    ``partitioned_by``, ``columns[].name``, ``columns[].type.base``,
    ``columns[].type.args[]``, ``columns[].not_null``. A parser names no
    perspective, assigns no namespace and applies no project convention.

    Inspectors keep what is not a perspective: discovering files, rendering
    the literal parts of templated SQL (NERD032 SPEC004), lineage, provide and
    reference observations, and core attributes (name, position, core type
    when a definition gives one, ``required`` from ``NOT NULL``).

    The first parsers are the existing SQL DDL subset
    (``internal/inspect/sqlddl``) and dbt ``schema.yml``.

.. spec:: Derivation from files alone
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    ``derive`` takes the trees of one format family from the inspected roots
    and proposes one perspective:

    #. **Name pattern.** Physical names are compared across objects. A
       segment that varies over a small set of values while the rest of
       the name stays fixed becomes a pattern variable: ``billing_final`` and
       ``billing_staging`` give ``{namespace}_{layer}``. The variable that
       distinguishes several objects of one noun becomes the definition's
       ``binding_key``; its values become binding names. The pattern replaces
       NERD032 SPEC004's built-in layer suffixes.
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
    #. **Kinds.** A key whose values are booleans is ``bool``. A key with few
       distinct values across many objects is ``enum``; its distinct values
       become ``enum_values``, and a type's arguments become ``parameters``
       in order. Anything else is ``short_text``. The thresholds are in the
       evidence, so a reviewer can see why a key is an enum.
    #. **Type mappings.** For each ``datatype`` enum value, ``hms_type`` is the
       core type of the ``.hms`` attributes it is paired with. A value seen
       with two core types, or with none, is flagged for review rather than
       guessed.
    #. **Existing definition.** If the registry (SPEC001) already has a
       definition the trees fit, ``derive`` maps into it instead of
       proposing a new one: it fills values, and reports enum values and keys
       the definition lacks.

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
      (``int`` → ``integer``).
    * ``position`` on a parameter: its order, since a JSON object has none
      (``decimal``: ``precision`` 1, ``scale`` 2).
    * ``name_pattern`` on a definition: the physical name as a sequence of
      parts, each a literal or a reference to the noun (``namespace``,
      ``name``) or to an entity key (``layer``):
      ``[{"ref": "namespace"}, {"lit": "_"}, {"ref": "layer"}]``.

    Strings that are themselves templates are replaced: a default of
    ``"{entity.name}"`` becomes ``{"ref": "name"}``. A run-time placeholder in
    a physical name (NERD032's ``template`` value) is written
    ``{"runtime": "<param>"}`` inside a ``name_pattern``, not as text.

    ``nsctl inspect <noun> --context`` prints, and ``--out <dir>`` writes, the
    merged context a generator reads: the ``.hms`` document with each
    perspective's values under ``extensions[<perspective>]``, as
    ``hmd-schema-loader`` merges sidecars. It is plain JSON, usable by any
    generator and as a ``hmd-cli-mickey`` context.

.. spec:: The perspective IR and materialisation
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    Derived perspectives live in the inspection store (NERD032 SPEC005) beside
    the snapshot they were derived from: definitions, values and evidence. A
    perspective in the IR is ``derived`` or ``edited``. ``edited`` covers renaming
    a key, merging two enum values, accepting a flagged ``hms_type``, or
    dropping a key. Edits are stored as operations on the derivation, so
    deriving again after the files change keeps them.

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

.. spec:: nsctl inspect perspective
    :id: HMD_CLI_NEURONSPHERE_NERD033_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD033
    :status: proposed

    * ``nsctl inspect perspective list [path...]`` lists the definitions in
      the registry with their origin (a repository file, or ``derived``).
    * ``nsctl inspect perspective derive [path...] [--format <family>]
      [--name <perspective>]`` derives (SPEC003), stores the result in the IR and
      prints the proposal with its review flags. ``--evidence`` adds the
      supporting objects per item.
    * ``nsctl inspect perspective show <perspective>`` prints a definition and its
      values; ``--json`` as everywhere.
    * ``nsctl inspect perspective edit <perspective> <operation>`` records an edit
      (SPEC005).
    * ``nsctl inspect perspective materialise <perspective> --to <repo>`` (SPEC005).

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
#. Do the ``name_pattern`` parts belong in the Modeler's shape upstream, so
   ``hmd-app-modeler`` can edit them?
