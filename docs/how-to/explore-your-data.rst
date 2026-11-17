Explore your own data
=====================

Use this guide to query a folder of your own files (CSV, Parquet, JSON or
NDJSON, Excel) with SQL. You need ``nsctl`` and a running container engine,
and nothing else: no environment, no bucket, no account and no repository.
Your files are read where they are and never changed, and nothing leaves this
machine. The design is :doc:`/proposals/NERD039_Data_Onboarding`.

DuckDB runs from a pinned container image, so the first command that needs it
pulls the image once.

With an agent
-------------

nsctl bundles skills that walk you through the steps below in conversation.
You say what you want to know and where the files are; the agent runs the
commands, puts each decision to you as a question about your data, and
answers with the SQL shown. Install them into the folder you work from::

   nsctl agent skills install ns-data-start ns-data-inventory ns-data-profile \
       ns-data-register ns-data-query ns-data-understand ns-data-store \
       ns-data-next --host all --path .

Then ask your agent to explore your data, or to use ``ns-data-start``. Every
skill begins from ``nsctl data status``, so a later session carries on where
the last one stopped. The rest of this guide is the same path by hand.

Start a workspace
-----------------

A workspace is where nsctl keeps what it learns about your data, under
``$HMD_HOME/data/<workspace>/``. Name it and point it at one or more
folders::

   nsctl data init shop ~/Downloads/shop-exports

The folder becomes a *source*. ``nsctl data list`` shows your workspaces and
``nsctl data use <workspace>`` switches between them; every other ``data``
command acts on the current one, or the one ``--workspace`` names.

Group files into tables
-----------------------

::

   nsctl data scan

``scan`` decides each file's format from its content and proposes tables:
files that share a format, a folder pattern and most of their columns form
one table. Hive-style ``key=value`` folders (``orders/month=2026-01/``) become
a column, and each sheet of an Excel workbook is a table of its own. Files it
cannot read are listed with the reason; zip archives and database dumps are
among them in this release.

Nothing is written until you accept the proposals::

   nsctl data scan --apply
   nsctl data scan --apply --name messy=regional_report   # rename one

Each table is a small definition in the workspace's ``tables/`` folder. Its
fields are the ones a Librarian content item type has, so the same table can
later move into a Librarian unchanged.

See what needs a decision
-------------------------

::

   nsctl data profile orders

``profile`` reports each column's type, nulls, distinct values and range, and
flags what is worth deciding, each with a suggested fix:

* dates or numbers that were read as text;
* identifiers that would lose their leading zeros if read as numbers;
* lines above the header (a report title, notes);
* columns that differ between files;
* columns that look like keys;
* columns that look like personal data. Their samples are redacted.

To apply a fix, edit the table definition (``casts`` or ``reader_options``).
Statistics come from a sample of up to 100,000 rows unless you pass
``--full``.

Make the tables queryable
-------------------------

::

   nsctl data register

``register`` builds the workspace catalog, a DuckDB database with one view per
table over your files. Ask it questions::

   nsctl data sql "SELECT customer_id, sum(amount) FROM orders GROUP BY ALL"
   nsctl data sql --save top_customers "SELECT ..."   # keep a query
   nsctl data sql                                      # interactive shell

``nsctl data register --pin`` records each table's current columns and types.
After that, a file that no longer matches is reported as drift instead of
silently changing the table.

If your files are still changing and you want answers to stay put, take a
snapshot. It copies the table to Parquet in the workspace, and ``status``
tells you when the files have moved on::

   nsctl data snapshot orders
   nsctl data register
   nsctl data status

Open it from your own tools
---------------------------

::

   nsctl data connect

prints the catalog's path and how to open it from the DuckDB CLI or UI,
DBeaver, or an agent. The views read your files at their real paths, so any
DuckDB of the version ``connect`` names, or newer, opens the catalog
directly. Open it read-only so nsctl can keep updating it.

Say what the data means
-----------------------

nsctl can propose what each table is and how the tables join, and ask you
about it::

   nsctl data profile --relationships
   nsctl data understand

``profile --relationships`` measures, for every column that could refer to
another table's key, how many of its values that key holds.
``understand`` turns the profiles into questions, each with an id and its
evidence::

   orders
     Is each row of orders one order, identified by order_id?
   orders.customer_id=customers.customer_id
     100% of orders.customer_id values (4 of 4) appear in customers.customer_id.
     Does each order row belong to one customer?

Answer each one::

   nsctl data approve orders --name order --description "One order a customer placed."
   nsctl data approve customers --name customer
   nsctl data approve orders.customer_id=customers.customer_id
   nsctl data reject budget_actuals.region=budget_plan.region

A join is approved after both of its tables. A rejected proposal is not asked
again, and running ``understand`` later asks only about what is new.

Get plain documents
-------------------

::

   nsctl data docs --out ./shop-docs

writes five files you can read and share without nsctl:

``DATA.md``
    Every table and its columns, with what they mean, their ranges and the
    quality notes from the profiles. Likely personal data is marked and its
    values are left out.
``relationships.md`` and ``relationships.puml``
    How the tables join, with the evidence, as a table and as a PlantUML
    diagram.
``views.sql``
    ``CREATE VIEW`` statements for every table and every name you approved.
    They run in any DuckDB.
``questions.md``
    Your saved queries and suggested ones, each runnable.

Anything you have not confirmed is marked *unconfirmed*. ``docs`` also creates
the named views in the catalog, so ``nsctl data sql 'SELECT * FROM "order"'``
works. It replaces only files it wrote and nobody has edited since, and never
deletes anything; ``--force`` replaces edited ones.

The rendering runs in a second pinned image with no network.

What nsctl learned is kept in the workspace's ``model/`` folder as NeuronSphere
model files. You never need to open them, but ``nsctl model inspect
$HMD_HOME/data/<workspace>`` shows them.

Keep the tables in local storage (optional)
------------------------------------------

Nothing above needed an environment. If you want the data somewhere other
than your folder -- to share it, or to use it from other NeuronSphere
services -- land it in the S3 of a local environment::

   nsctl env add dev && nsctl env start dev
   nsctl data storage --env dev

``storage`` prints the environment's S3 endpoint (from this machine and from
inside the environment), its access key, the buckets it has, and AWS CLI and
Terraform snippets to create one with your own tools. The secret is withheld
unless ``--reveal``. Then::

   nsctl data land --env dev --to s3://my-data

Or have a bucket set up for you, and name it by its instance instead of
typing its name::

   nsctl bundle add storage --env dev
   nsctl env apply dev
   nsctl data land --env dev --to instance:storage

``land`` writes each table as Parquet under ``<target>/raw/<table>/`` and
points its view there. The workspace remembers the target: ``nsctl data
land`` alone lands what changed since, and ``--force`` writes everything
again. From then on the views read the bucket, so the environment must be
running to query the workspace; ``nsctl data connect`` prints the secret a
DuckDB on this machine needs.

Keep the work in a repository
-----------------------------

When you or a team want the work in git, export it::

   nsctl data export --repo ~/src/shop-data

This is the one step that makes a repository. It holds:

``docs/data/``
    The documents ``nsctl data docs`` writes.
``src/schemas/`` and ``src/sources/``
    What you approved and what is still a proposal, as NeuronSphere model
    files.
``src/data/tables/`` and ``src/data/queries/``
    The table definitions and your saved queries.
``src/model/export.json``
    The model as one JSON document, for tools that generate other forms from
    it (dbt, a catalog, a warehouse model). Those tools live outside nsctl.
``meta-data/manifest.json``
    A NeuronSphere repository manifest with nothing to build;
    ``nsctl repoclass validate`` accepts it.

``export`` refuses a folder with anything in it (except ``.git``) that it did
not export. Keep exploring in the workspace and run ``nsctl data export``
again to refresh the repository: the manifest is never rewritten, a file you
edited is not replaced unless ``--force``, and nothing is deleted.
Committing and pushing is up to you.

When files change
-----------------

``nsctl data status`` lists source files that changed since a snapshot or a
landing. For new files, scan again (files a table already reads are not
proposed twice), profile and register the new tables, and run ``nsctl data
understand``: it asks only about what is new. ``nsctl data land`` copies only
the tables whose files changed, and ``nsctl data docs`` and ``nsctl data
export`` refresh the documents and the repository.

Start over
----------

``nsctl data delete <workspace> --yes`` removes everything nsctl learned.
Your files are not touched.
