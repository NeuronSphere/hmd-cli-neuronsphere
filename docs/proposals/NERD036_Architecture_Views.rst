.. NERD036 Architecture Views

NERD036 Architecture Views
==========================

.. req:: Architecture diagrams are rendered by the deployment service from a neutral model, through deployable templates
    :id: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    NeuronSphere shall derive an *architecture model* (repo classes or
    instances, the roles that bind them, and the resources each produces and
    consumes) for any chosen set of roots, from the same BACON manifests that
    build and deploy those repos and from what the deployment service records
    about them.

    The model shall be a neutral, versioned JSON document. Rendering it
    (PlantUML, RST tables, C4, anything else) shall be the job of *rendering
    templates*: repo classes that are deployed into a deployment-service
    instance, so several can be installed side by side and chosen at render
    time.

    A named *architecture view* (roots, scope, template, parameters) shall be
    saved in the deployment service. A documentation repository shall refer to
    a view by name and fetch its renderings from the deployment API each time
    its documentation is built, so the published document stays current with
    no manual step. The view and its renderings live outside the repositories
    they describe.

Motivation
----------

Customers of a NeuronSphere service ask the same question every time: what
does it depend on, how does traffic reach it, and where does it keep data.
For ``hmd-ms-device-lib`` (a librarian whose image is
``FROM hmd-ms-librarian``) the answer is "client → API Gateway REST API
(OPA authorizer) → Lambda → DynamoDB entity table, S3 content bucket and
Neptune graph; it depends on ``ms-device``, ``ms-case``, the VPC and the OTEL
collector, and it retries through an SQS queue". Today that answer is drawn
by hand and goes stale.

Most of it is already declared. ``deploy.dependencies`` names every role and
the class or resource that fills it, and the deployment service stores those
edges (``repo_class_version_req_repo_class``,
``repo_class_version_req_resource_definition``,
``repo_class_version_produces_resource_definition``,
``repo_instance_req_repo_instance``). What is missing is everything a
service creates *for itself*: the REST API, the Lambda function, the DynamoDB
table, the SQS queues. Those exist only in ``hmd-lib-cdktf-factories``
(``service_base.py``, ``api_gateway.py``, ``librarian_base.py``); no manifest
declares them and no resource definition exists for them. ``lib-repo`` names
``hmd-inf-s3bucket`` by class only, and ``hmd-inf-s3bucket`` declares
nothing it produces.

This NERD closes that gap with real resource declarations rather than a
diagram-only side channel, so a diagram and a deploy read the same facts and
cannot disagree. It then separates three concerns that a one-off diagram
generator would tangle together:

* **what exists** -- the model (SPEC001), produced by the deployment service
  from its registry and by ``nsctl`` from manifests on disk;
* **how it looks** -- rendering templates (SPEC003, SPEC004), deployed like
  any other repo class;
* **where it is published** -- documentation repositories that reference a
  saved view (SPEC005) and fetch renderings at build time (SPEC008).

The template mechanism is general; the librarian diagram is its first
consumer. It relates to NERD032 SPEC014/SPEC015 (a JSON export consumed by
``hmd-cli-mickey`` templates) and reuses the resource model of NERD0004
(``hmd-docs-bacon/docs/spec/resources.rst``).

Terminology
-----------

*Architecture model*
    The JSON document of SPEC001. Neutral: no audience, layout or styling.

*Rendering template*
    A repo class carrying a Mickey template pack and a ``template.json``
    descriptor (SPEC003). It turns a model into named outputs.

*Architecture view*
    A saved, named request in the deployment service: roots, scope,
    environment, template and parameters (SPEC005).

*Rendering*
    The outputs of applying a view's template to the model the view's scope
    selects, at the moment of the request.

.. spec:: The architecture model JSON
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    The model is a JSON document with schema identifier
    ``architecture.neuronsphere.io/v1``. Its JSON Schema is canonical in
    ``hmd-docs-bacon/docs/reference/architecture-model.schema.json``; both
    producers depend on that schema rather than on each other.

    .. code-block:: json

        {
          "schema": "architecture.neuronsphere.io/v1",
          "scope": {"roots": ["instance:c0007/device"], "depth": 1,
                    "include": [], "exclude": [],
                    "source": "control-plane", "environment": "c0007"},
          "nodes": [
            {"id": "rc:hmd-ms-device-lib", "kind": "repo_class",
             "label": "hmd-ms-device-lib", "repo_class": "hmd-ms-device-lib",
             "version": "0.1.89", "group": null, "attributes": {}},
            {"id": "res:hmd-ms-device-lib/api", "kind": "resource",
             "label": "api", "resource_definition": "aws.neuronsphere.io/api-gateway-rest-api@0.1.0",
             "provider": "aws", "group": "rc:hmd-ms-device-lib", "attributes": {}}
          ],
          "edges": [
            {"from": "rc:hmd-ms-device-lib", "to": "rc:hmd-inf-s3bucket",
             "kind": "depends_on", "role": "lib-repo", "required": true,
             "status": "resolved"},
            {"from": "res:hmd-ms-device-lib/api", "to": "res:hmd-ms-device-lib/function",
             "kind": "flows_to"}
          ],
          "findings": [{"severity": "warning", "code": "unresolved-role",
                        "subject": "hmd-ms-device-lib", "message": "..."}]
        }

    * ``kind`` of a node is one of ``repo_class``, ``instance``,
      ``resource`` or ``external`` (for example the client a customer view
      adds in front of an API).
    * ``kind`` of an edge is one of ``depends_on`` (a role), ``produces``,
      ``consumes``, ``flows_to`` (SPEC002) or ``isa`` (resource definition
      ancestry, emitted only when a template asks for it).
    * ``status`` of a ``depends_on`` edge is ``resolved``, ``candidate`` (a
      resource requirement more than one producer could satisfy, typically
      because a tag selector cannot be evaluated) or ``unresolved``.
    * Node ids are stable across producers: ``rc:<class>``,
      ``inst:<environment>/<instance>``, ``res:<owner>/<declaration name>``,
      ``ext:<name>``.
    * Nodes and edges are sorted by id so two models of the same scope are
      byte-identical and can be compared by digest.

.. spec:: Services declare the AWS resources they create, and how traffic flows between them
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    New base resource definitions are added beside the existing ones (the
    deployment core's ``base_resources/`` and ``nsctl``'s
    ``SeedBaseResourceDefinitions``):

    .. list-table::
        :header-rows: 1

        * - Definition
          - Parent
        * - ``aws.neuronsphere.io/s3-bucket``
          - ``storage.neuronsphere.io/object-store`` (exists)
        * - ``aws.neuronsphere.io/api-gateway-rest-api``
          - ``network.neuronsphere.io/api-gateway`` (new)
        * - ``aws.neuronsphere.io/lambda-function``
          - ``compute.neuronsphere.io/function`` (new)
        * - ``aws.neuronsphere.io/dynamodb-table``
          - ``database.neuronsphere.io/key-value-table`` (new)
        * - ``aws.neuronsphere.io/sqs-queue``
          - ``messaging.neuronsphere.io/queue`` (new)

    A repo class declares what it creates in ``deploy.resources``. A resource
    declaration's ``metadata.connects_to`` lists where traffic or data goes
    from it: the name of another of the class's own declarations, or
    ``dep:<role>`` for whatever fills one of its dependency roles. The
    ``metadata`` key is already non-matching (``resources.rst``), so flow
    hints never change dependency resolution.

    For the librarian slice:

    * ``hmd-inf-s3bucket`` declares ``bucket`` (``s3-bucket``).
    * ``hmd-ms-librarian`` and ``hmd-ms-device-lib`` each declare ``api``
      (``connects_to: [function]``), ``function``
      (``connects_to: [entity-table, dep:lib-repo, dep:neptune-db]``),
      ``entity-table`` and ``retry-queue``; their ``lib-repo`` dependency
      gains a ``resource`` block for ``s3-bucket`` and keeps
      ``repo_class_name`` as the fallback.
    * Neither declares ``application.neuronsphere.io/microservice``: that
      would add producers existing microservice consumers bind against.

    Edits are made with ``nsctl repoclass deploy add-resource`` and
    ``add-dependency`` and checked with ``nsctl repoclass validate``; a
    missing verb is a defect in ``nsctl``, not a reason to hand-edit.

    BACON gains no ``extends``. A class that builds on another's image
    declares its own resources, as it already declares its own dependencies.

.. spec:: A rendering template is a repo class
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    A rendering template is a repo class (convention: ``hmd-tmpl-arch-*``)
    containing:

    * ``src/mickey/templates/<package>/`` -- Jinja templates rendered with
      the model as context and the request's parameters as ``vars``, by
      Mickey's rules (SPEC006). Macros are defined in the file that uses
      them: Mickey names a template ``<project>/<package>/<file>`` and the
      deployment service names it ``<file>``, so an ``import`` path cannot
      resolve in both;
    * ``template.json``, JSON so the deploy step needs only Python's standard
      library:

      .. code-block:: json

          {
            "name": "aws-customer",
            "title": "AWS customer architecture",
            "requires_schema": "architecture.neuronsphere.io/v1",
            "package": "aws_customer",
            "outputs": [
              {"path": "architecture.puml", "media_type": "text/x-plantuml", "primary": true},
              {"path": "components.rst", "media_type": "text/x-rst"}
            ],
            "parameters": {
              "type": "object",
              "properties": {
                "audience": {"type": "string", "enum": ["customer", "internal"], "default": "customer"},
                "show_dependencies": {"type": "boolean", "default": true}
              }
            },
            "scope_defaults": {"depth": 1}
          }

      ``parameters`` is a JSON Schema; the GUI builds its form from it;
    * a BACON manifest whose deploy registers the template (SPEC004) with
      ``["exec", "python3", "scripts/register_template.py"]`` in a bare
      Python ``deploy.image``. nsctl runs ``exec``; ``hmd deploy`` does not
      yet (NERD009 SPEC012), so in the cloud the script is run by hand until
      it does;
    * a Robot suite showing that ``hmd mickey build`` renders the same bytes
      as the deployment service's rules.

    A template receives only the model and its parameters. It shall not call
    out to any service, so the same model and parameters always render the
    same outputs.

    Starter templates: ``aws-customer`` (PlantUML stdlib ``awslib20`` icons,
    the first use case, ``hmd-tmpl-arch-aws-customer``), ``dependency-graph``, ``resource-inventory`` and
    ``c4-container``.

.. spec:: Templates are deployed into a deployment-service instance
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    ``hmd-lang-deployment`` gains the noun ``rendering_template``
    (``template_name`` and ``version`` identify it; ``title``,
    ``description``, ``descriptor`` mapping, ``files`` mapping,
    ``artifact_location``) and the relationship
    ``repo_instance_provides_rendering_template``.

    Deploying a template repo instance calls
    ``POST /apiop/register_rendering_template`` with its descriptor and its
    template files, by path, as text. Carrying the files inline means a render
    needs nothing outside the deployment service: fetching a bundle from the
    artifact librarian would need a service-account token on every render.
    ``artifact_location`` records where the bundle was published, for
    provenance only. Undeploying the instance removes the registration. Any number
    of templates, and any number of versions of one template, may be
    registered at once. ``GET /apiop/list_rendering_templates`` and
    ``GET /apiop/get_rendering_template/<name>/<version>`` read them.

    Locally a template is added to an environment like any other repo class
    and deployed with ``hmd deploy --local``.

.. spec:: Architecture views are saved in the deployment service
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    ``hmd-lang-deployment`` gains the noun ``architecture_view``: ``name``
    (unique), ``title``, ``roots``, ``depth``, ``include``, ``exclude``,
    ``environment`` (optional; without it the view is class-level),
    ``template_name``, ``template_version_spec`` and ``params``.

    ``POST /apiop/upsert_architecture_view`` and
    ``GET /apiop/list_architecture_views`` manage them. The same shape,
    written as ``*.arch.yaml``, is what ``nsctl architecture`` reads and
    saves (SPEC009):

    .. code-block:: yaml

        architecture: v1
        name: c0007-device-librarian
        title: Device Librarian -- customer architecture
        scope: {roots: [hmd-ms-device-lib], depth: 1, exclude: [hmd-inf-datadog-lambdas]}
        environment: c0007
        template: {name: aws-customer, version: "~0.1", params: {audience: customer}}

.. spec:: The deployment service builds models and renders them
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    ``hmd-ms-deployment-core`` gains:

    * ``POST /apiop/get_architecture_model`` (``roots``, ``depth``,
      ``include``, ``exclude``, ``environment``) -- the class closure from the
      requirement and produces edges, resolving resource requirements to
      producers through ``get_producers`` and ``get_ancestry``; with an
      environment, instances and their produced ``Resource`` records overlay
      the classes;
    * ``POST /apiop/render_architecture`` (a scope or a model, a template
      name and version spec, parameters) -- returns ``{model, outputs}``;
    * ``GET /apiop/render_architecture_view/<name>`` -- renders a saved view
      and returns ``{view_name, outputs, model, model_digest, etag,
      template_name, template_version, rendered_at}``. ``model_digest`` is the
      SHA-256 of the model's canonical JSON; ``etag`` changes exactly when the
      model or the template version does.

    Rendering applies the rules of ``hmd_lib_mickey``'s render phase to the
    registered files: every file is a Jinja template named by its path; a file
    whose name or stem starts with ``_`` or ``.`` is a partial; a ``.j2`` or
    ``.jinja`` suffix is dropped and the output path is itself rendered; the
    context is the model's top-level keys plus ``vars`` (the parameters) and
    ``mickey`` (Mickey's folder defaults). The environment is sandboxed and no
    Mickey hook runs, because a registered template runs inside the service.
    Mickey's library itself is not used: its dependency phases need git and a
    remote-dependency client the service image does not carry. Each template
    repository checks that ``hmd mickey build`` produces the same outputs.

    ``nsctl`` builds the same model from manifests on disk (SPEC009). One
    golden fixture -- the librarian slice's manifests and the model expected
    from them -- is checked by both producers' unit tests.

.. spec:: The deployment GUI selects a root and a template
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    ``hmd-app-neuronsphere-core`` gains an ``/architecture/`` page:

    #. pick an environment and a **root instance** (from the environment's
       BOM), or a repo class and version;
    #. pick a **template** from the registered templates; its parameter
       schema generates the parameter form;
    #. preview: an interactive Cytoscape graph of the model (reusing the BOM
       DAG's cytoscape-dagre setup) and each template output as source;
    #. act: download the model or an output, **Save view**, or **Export docs
       repo** (SPEC008).

    Pages that show a class version or a BOM node link to the page with that
    root selected. The MCP server gains ``list_templates``, ``get_model``,
    ``render`` and ``render_view`` tools.

.. spec:: Documentation repositories fetch renderings at build time
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    The ``hmd-tf-bartleby`` image gains a Sphinx extension,
    ``ns_architecture``, providing:

    .. code-block:: rst

        .. ns-architecture:: c0007-device-librarian
           :output: architecture.puml
           :include: components.rst

    At build time the directive calls ``render_architecture_view``, writes the
    primary (or named) PlantUML output into the build and renders it through
    the existing ``sphinxcontrib.plantuml`` node; ``:include:`` inlines an RST
    output. A caption records the view, template version, model digest and
    render time.

    * The endpoint comes from ``HMD_DEPLOYMENT_API_URL`` and an optional
      bearer token from ``HMD_DEPLOYMENT_API_TOKEN``; the ``bartleby`` binary
      passes both into its container.
    * Each successful fetch is written to
      ``.ns-architecture-cache/<view>/``. When the API cannot be reached the
      directive uses the cache with a warning; with no cache the build fails,
      unless ``ns_architecture_offline = "warn"``.
    * Any repository, or a folder in no repository, may use the directive;
      the Go ``bartleby`` builds a folder with no BACON manifest.

    The document is current whenever its build runs: in that repository's
    CI, on a schedule, or on a workstation. Nothing in this path runs
    ``nsctl``.

    The GUI's **Export docs repo** saves the view if needed and downloads a
    starter folder (``docs/index.rst`` with the directive, a README naming
    the build command). Pushing to a git remote is out of scope: it would
    put git credentials in the deployment service.

.. spec:: nsctl authors and previews views
    :id: HMD_CLI_NEURONSPHERE_NERD036_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD036
    :status: proposed

    ``nsctl architecture`` provides:

    * ``model [spec] [--source manifests|control-plane] [--out FILE]`` --
      the SPEC001 model, built offline from manifests (through the repo-class
      resolver) or fetched from the control plane;
    * ``validate [spec]`` -- the spec, plus unresolved roles and dependencies
      on classes that declare no resources, as ``nsctl inspect`` findings;
    * ``render [spec]`` -- renders through the control plane;
    * ``template list|show`` -- the registered templates;
    * ``view save [spec]`` -- ``upsert_architecture_view``.

    From manifests alone a tag selector cannot be evaluated, so a resource
    requirement with more than one possible producer yields ``candidate``
    edges and a finding, and ``repo_class_name`` decides when present.

    These verbs exist to write and check views. Documentation never depends
    on them (SPEC008).

Out of scope
------------

* Emitting real names and ARNs from ``ServiceCdkTfStack`` and
  ``LibrarianHmdCdkTfStack`` through ``add_resource_output``. The model shows
  them once they exist; this NERD does not depend on them.
* Pushing exported documentation to a git remote.
* Notifying documentation pipelines when a view's scope is redeployed.
