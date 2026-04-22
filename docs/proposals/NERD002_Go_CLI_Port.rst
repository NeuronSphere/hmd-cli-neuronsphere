.. NERD002 Port hmd CLI Ecosystem from Python to Go

NERD002 Port hmd CLI Ecosystem from Python to Go
=================================================

.. req:: Eliminate Python runtime dependency and deliver hmd as a single cross-platform binary
    :id: HMD_CLI_NERD002
    :status: proposed

    The hmd CLI ecosystem (40 ``hmd-cli-*`` repositories, each a separate pip
    package discovered via setuptools ``entry_points``) shall be ported to Go.
    The result is a single statically-linked binary per platform that requires
    only Docker to be installed. The port preserves 100% command-line
    compatibility (same command names, flags, and exit codes) while
    incorporating NERD001's Floci/Deploy mode architecture natively. The
    ``nsplugin.json`` format is unchanged. Microservices (``hmd-ms-*``) remain
    Python.

Motivation
----------

1. **Installation friction.** Users must install Python 3.9+, pip, virtualenv,
   and ~40 pip packages with transitive dependencies (cement, boto3, pyyaml,
   jinja2, requests, kubernetes, pg8000, inquirerpy, colorlog, python-dotenv).
   Conflicts with system Python and other tools are common. A single binary
   eliminates this entirely -- the only prerequisite is Docker.

2. **Startup latency.** Python interpreter startup combined with importlib
   scanning of 40+ packages adds 1-3 seconds per invocation. Go binaries
   start in under 10 ms.

3. **Distribution complexity.** Each ``hmd-cli-*`` repo produces a pip package.
   Version alignment across 40 packages is fragile -- a single version mismatch
   can break the CLI. A monorepo producing a single binary eliminates this
   class of bugs.

4. **Ecosystem alignment.** Docker, Kubernetes, Terraform, Helm, and Argo are
   all written in Go. The hmd CLI shells out to all of these. Native Go
   libraries (Docker SDK, client-go, aws-sdk-go-v2) enable deeper integration
   and better error handling than subprocess calls.

5. **NERD001 synergy.** The Floci/Deploy mode architecture (NERD001) introduces
   new components (LocalWorkflowRunner, BOM generation, deployment graph
   seeding) that benefit from Go's concurrency primitives and fast startup for
   container orchestration tasks.

Scope
-----

**In scope (ported to Go):**

All 40 ``hmd-cli-*`` repositories are consolidated into a single Go monorepo.
The major command groups include:

- ``hmd-cli-app`` -- main entry point, controller discovery
- ``hmd-cli-neuronsphere`` -- local NeuronSphere orchestration, plugins, Floci
- ``hmd-cli-configure`` -- environment configuration
- ``hmd-cli-tools`` -- shared library (env, AWS, K8s, Okta, prompts,
  credentials, S3, version management)
- ``hmd-cli-build`` -- build orchestration
- ``hmd-cli-deploy`` -- deployment commands
- ``hmd-cli-docker``, ``hmd-cli-helm``, ``hmd-cli-cdktf`` -- tool wrappers
- ``hmd-cli-python``, ``hmd-cli-typescript`` -- language build tools
- ``hmd-cli-bender`` -- Robot Framework test runner
- ``hmd-cli-bartleby`` -- documentation builder
- ``hmd-cli-repo``, ``hmd-cli-login``, ``hmd-cli-secrets``, ``hmd-cli-debug``,
  ``hmd-cli-dbt``, ``hmd-cli-version``, ``hmd-cli-transform-can``,
  ``hmd-cli-transform-deploy``, and all remaining CLI repos

**Out of scope (remain Python):**

- All ``hmd-ms-*`` microservices (including ``hmd-ms-deployment``,
  ``hmd-ms-transform``, ``hmd-ms-naming``)
- All ``hmd-img-*`` container images (including ``hmd-img-projectbuilder``)
- All ``hmd-inf-*`` infrastructure repos (their ``src/local/`` artifacts are
  consumed unchanged by the Go CLI)
- All ``hmd-lib-*`` shared Python libraries (used by microservices, not CLI)
- Robot Framework test files (``.robot``) -- these invoke the CLI as a
  subprocess and are language-agnostic

**nsplugin.json format:** Unchanged. The Go implementation reads the same JSON
schema. Existing ``nsplugin.json`` files across all repos work without
modification.

Architecture Overview
---------------------

All 40 CLI packages are consolidated into a single Go module in a new
repository ``hmd-cli``. Each former ``hmd-cli-*`` package becomes a Go package
under ``internal/``.

::

    hmd-cli/
    +-- go.mod
    +-- go.sum
    +-- cmd/
    |   +-- hmd/
    |       +-- main.go              # Single entry point
    +-- internal/
    |   +-- app/
    |   |   +-- root.go              # Root cobra command
    |   |   +-- registry.go          # Compile-time command registration
    |   +-- neuronsphere/
    |   |   +-- controller.go        # "hmd neuronsphere" subcommands
    |   |   +-- orchestrator.go      # Start/stop/restart logic
    |   |   +-- floci_deployer.go    # Floci AWS resource provisioning
    |   |   +-- deploy_mode.go       # NERD001 Deploy mode startup
    |   |   +-- bom.go               # NERD001 BOM generation
    |   |   +-- display.go           # Startup/shutdown display
    |   |   +-- portcheck.go         # Port conflict detection
    |   +-- plugins/
    |   |   +-- interface.go         # Plugin interface definition
    |   |   +-- registry.go          # Compiled-in plugin registry
    |   |   +-- base.go              # Shared plugin helpers
    |   |   +-- main.go              # Main plugin (nginx, gateway, db, naming)
    |   |   +-- telemetry.go         # OpenTelemetry/Datadog plugin
    |   |   +-- graph.go             # JanusGraph plugin
    |   |   +-- ministack.go         # Floci/MiniStack plugin
    |   |   +-- airflow.go           # Airflow plugin
    |   |   +-- transform.go         # Transform plugin
    |   |   +-- trino.go             # Trino plugin
    |   |   +-- clickhouse.go        # ClickHouse plugin
    |   |   +-- hive_metastore.go    # Hive Metastore plugin
    |   |   +-- apache_superset.go   # Superset plugin
    |   |   +-- jupyter.go           # Jupyter plugin
    |   |   +-- dynamodb.go          # DynamoDB plugin
    |   |   +-- minio.go             # MinIO plugin
    |   +-- localplugin/
    |   |   +-- loader.go            # nsplugin.json discovery and loading
    |   |   +-- validator.go         # nsplugin.json validation
    |   |   +-- types.go             # LocalPluginInfo, ValidationResult
    |   +-- configure/               # "hmd configure" subcommands
    |   +-- build/                   # "hmd build" subcommands
    |   +-- deploy/                  # "hmd deploy" subcommands
    |   +-- docker/                  # "hmd docker" subcommands
    |   +-- helm/                    # "hmd helm" subcommands
    |   +-- cdktf/                   # "hmd cdktf" subcommands
    |   +-- login/                   # "hmd login" subcommands
    |   +-- secrets/                 # "hmd secrets" subcommands
    |   +-- bender/                  # "hmd bender" subcommands
    |   +-- bartleby/                # "hmd bartleby" subcommands
    |   +-- repo/                    # "hmd repo" subcommands
    |   +-- version/                 # "hmd version" subcommands
    |   +-- tools/                   # Shared utilities (hmd-cli-tools equiv)
    |   |   +-- env.go               # Environment loading and management
    |   |   +-- prompt.go            # Interactive prompts
    |   |   +-- aws.go               # AWS session factory (Floci-aware)
    |   |   +-- s3.go                # S3 operations
    |   |   +-- k8s.go               # Kubernetes operations
    |   |   +-- okta.go              # Okta OAuth2 integration
    |   |   +-- credentials.go       # System keyring management
    |   |   +-- rds.go               # RDS management
    |   |   +-- vpn.go               # VPN operations
    |   +-- skills/
    |       +-- loader.go            # AI skills loader
    +-- embed/
    |   +-- services/                # Docker Compose files (go:embed)
    |   +-- external/                # Pre-build artifacts (go:embed)
    |   +-- skills/                  # AI skill markdown files (go:embed)
    +-- meta-data/
    |   +-- VERSION
    |   +-- manifest.json
    +-- docs/
    +-- test/                        # Robot Framework tests (unchanged)
    +-- Makefile
    +-- goreleaser.yml               # Cross-platform release config

.. spec:: CLI framework -- Cobra replacing Cement
    :id: HMD_CLI_NERD002_SPEC001
    :links: HMD_CLI_NERD002
    :status: proposed

    Replace Cement 3.0.6 with `cobra <https://github.com/spf13/cobra>`_ and
    `viper <https://github.com/spf13/viper>`_.

    **Mapping from Cement to Cobra:**

    .. list-table::
       :header-rows: 1
       :widths: 40 60

       * - Cement Concept
         - Cobra Equivalent
       * - ``Controller`` class
         - ``cobra.Command`` with subcommands
       * - ``@ex()`` decorator on method
         - ``cobra.Command{Run: func}``
       * - ``stacked_type = "nested"``
         - Cobra parent/child command tree
       * - ``Meta.label``
         - ``cobra.Command.Use``
       * - ``self.app.pargs``
         - ``cmd.Flags()``
       * - ``handler/hook`` system
         - Go interfaces + init-time registration
       * - ``minimal_logger()``
         - ``log/slog`` or ``zerolog``
       * - ``shell.cmd()``
         - ``os/exec.Command``

    **Command registration (replacing entry_points):**

    The ``hmd_cli.controllers`` entry_point group currently discovers
    controllers at runtime via importlib. In Go, all commands are registered at
    compile time in ``internal/app/registry.go``:

    .. code-block:: go

        func RegisterAll(root *cobra.Command) {
            root.AddCommand(neuronsphere.NewCommand())
            root.AddCommand(configure.NewCommand())
            root.AddCommand(build.NewCommand())
            root.AddCommand(deploy.NewCommand())
            root.AddCommand(docker.NewCommand())
            root.AddCommand(helm.NewCommand())
            // ... all command groups
        }

    This eliminates runtime discovery overhead and ensures all commands are
    available immediately.

    **Trade-off:** Adding a new command group requires modifying
    ``registry.go`` and rebuilding. This is acceptable because new CLI command
    groups are infrequent (a few per year) and the monorepo makes this a
    single-PR change.

    **Version information:** Build-time ``-ldflags`` injection replaces runtime
    ``importlib.metadata.version()`` calls.

    **Configuration:** Viper handles ``$HMD_HOME/.config/hmd.env`` loading,
    replacing ``python-dotenv``. Environment variable precedence is preserved.

.. spec:: Compiled-in plugin system replacing entry_points
    :id: HMD_CLI_NERD002_SPEC002
    :links: HMD_CLI_NERD002
    :status: proposed

    The current neuronsphere plugin system uses four setuptools entry_point
    groups (``enabled``, ``prepare_hmd_home``, ``get_resources``,
    ``render_compose_yaml``), each with 13 registered functions.

    In Go, these become a single interface with a compile-time registry:

    .. code-block:: go

        type Plugin interface {
            Name() string
            Enabled(overrides map[string]bool) bool
            GetResources() map[string]interface{}
            PrepareHMDHome(hmdHome string, configs map[string]bool) error
            RenderComposeYAML(
                resources map[string]interface{},
                cacheDir string,
                configs map[string]bool,
            ) (string, error)
        }

        var bundledPlugins = []Plugin{
            &MainPlugin{},
            &TelemetryPlugin{},
            &GraphPlugin{},
            &MinistackPlugin{},
            // ... all 13 plugins
        }

    **Why not Go's ``plugin.Open()``:**

    - Requires ``-buildmode=plugin`` with identical Go version and build flags
      across host and plugin. Fragile across platforms.
    - Produces ``.so`` shared libraries, defeating the single-binary goal.
    - No macOS ARM64 support in many CI environments due to CGo dependency.

    **Why not subprocess plugins:**

    - 13+ subprocess invocations per ``hmd neuronsphere up`` adds latency.
    - Distributing 13+ separate binaries defeats the single-binary goal.

    **Decision: compiled-in plugins with interface dispatch.** This is the
    standard Go pattern. The ``nsplugin.json``-based local plugin system
    (SPEC003) handles the extensibility use case that entry_points served for
    external repos.

.. spec:: Local plugin system (nsplugin.json) preserved in Go
    :id: HMD_CLI_NERD002_SPEC003
    :links: HMD_CLI_NERD002
    :status: proposed

    The ``LocalPluginLoader`` is ported to Go as
    ``internal/localplugin/loader.go``. The ``nsplugin.json`` format is
    **unchanged** -- no schema changes.

    **Discovery mechanisms (preserved):**

    1. ``HMD_LOCAL_PLUGINS`` -- colon-separated paths to repos with
       ``src/local/nsplugin.json``
    2. ``HMD_LOCAL_PLUGINS_SCAN_REPO_HOME=true`` -- recursively scan
       ``HMD_REPO_HOME`` for repos with ``src/local/nsplugin.json``

    **Go implementation details:**

    - ``encoding/json`` parses nsplugin.json (replaces Python ``json``)
    - ``os.ReadDir`` scans directories (replaces ``pathlib.Path.iterdir``)
    - ``LocalPluginInfo`` Go struct mirrors the Python dataclass exactly
    - All plugin operations preserved: ``GetEnabledPlugins()``,
      ``GetPluginConfig()``, ``GetComposePath()``, ``GetDBInitCompose()``,
      ``GetTelemetryProfiles()``, ``GetEnvVars()``

    **Validator** (``internal/localplugin/validator.go``) implements the same
    checks as ``nsplugin_validator.py``:

    - JSON syntax validation
    - Required field checking (``plugin_name``, ``compose_file``)
    - Optional field type checking
    - Referenced file existence (compose files, configs, scripts, templates)
    - Docker Compose file YAML validity
    - Telemetry profile validation

    **The nsplugin.json system is the extensibility boundary.** Because Go
    compiles bundled plugins into the binary, external repos cannot add new
    bundled plugins without a CLI release. But any repo with
    ``src/local/nsplugin.json`` participates in the local NeuronSphere without
    touching the CLI binary -- this is the mechanism that matters for developer
    extensibility.

.. spec:: Template rendering -- Go text/template replacing Jinja2
    :id: HMD_CLI_NERD002_SPEC004
    :links: HMD_CLI_NERD002
    :status: proposed

    Jinja2 templates are used in two places:

    1. **Local plugin loader** (``_prepare_local_plugin``): Renders
       user-authored templates from ``src/local/templates/`` using a context of
       resources, plugin configs, and ``HMD_*`` environment variables.
    2. **Plugin base** (``render_templates``): Same mechanism for external
       artifact templates.

    **Current template usage is minimal.** Examining all bundled plugins and
    external artifacts (transform, trino, hive-metastore, airflow, clickhouse,
    otel-collector, superset, telemetry-debug): none define ``templates``
    entries in their ``nsplugin.json``. Templates are a capability for
    user-authored local plugins, not heavily used by bundled ones.

    **Migration approach:**

    - Implement a ``TemplateRenderer`` in ``internal/plugins/base.go`` using
      Go's standard ``text/template`` package.
    - The context dictionary (``resources``, ``configs``, ``env``) maps to
      Go struct or ``map[string]interface{}``.

    **Syntax differences for user-authored templates:**

    .. list-table::
       :header-rows: 1
       :widths: 50 50

       * - Jinja2
         - Go text/template
       * - ``{{ env.HMD_HOME }}``
         - ``{{ .Env.HMD_HOME }}``
       * - ``{% if configs.telemetry %}``
         - ``{{ if .Configs.telemetry }}``
       * - ``{% for db in resources.databases %}``
         - ``{{ range .Resources.Databases }}``
       * - ``{{ loop.index }}``
         - (use ``{{ $i }}`` with range index)

    **Fallback option:** If Jinja2 compatibility proves critical for existing
    user templates, `pongo2 <https://github.com/flosch/pongo2>`_ provides a
    Django/Jinja2-compatible Go template engine. This is a last resort -- Go
    native templates are preferred for maintainability.

    **Docker Compose files are NOT templates.** The compose YAML files use
    Docker Compose's native ``${VAR:-default}`` interpolation, which is handled
    by Docker Compose itself. The Go CLI passes them directly to
    ``docker compose``.

.. spec:: AWS SDK -- aws-sdk-go-v2 replacing boto3
    :id: HMD_CLI_NERD002_SPEC005
    :links: HMD_CLI_NERD002
    :status: proposed

    ``ministack_deployer.py`` (renamed to ``floci_deployer.py`` per NERD001)
    uses boto3 for SQS, S3, DynamoDB, Lambda, and API Gateway operations.

    **Go equivalent using aws-sdk-go-v2:**

    .. code-block:: go

        func NewFlociClient(endpoint string) *FlociClient {
            cfg, _ := config.LoadDefaultConfig(context.TODO(),
                config.WithRegion(os.Getenv("AWS_REGION")),
                config.WithBaseEndpoint(endpoint),
                config.WithCredentialsProvider(
                    credentials.NewStaticCredentialsProvider(
                        "dummykey", "dummykey", ""),
                ),
            )
            return &FlociClient{
                sqs:        sqs.NewFromConfig(cfg),
                s3:         s3.NewFromConfig(cfg),
                dynamodb:   dynamodb.NewFromConfig(cfg),
                lambda_:    lambda_.NewFromConfig(cfg),
                apigateway: apigateway.NewFromConfig(cfg),
            }
        }

    **Key differences from boto3:**

    - boto3's high-level ``resource`` API has no Go equivalent. All calls use
      service clients directly (already the pattern in
      ``ministack_deployer.py``).
    - Error handling uses ``var apiErr smithy.APIError`` type assertions
      instead of ``ClientError.response["Error"]["Code"]`` string matching.
    - Pagination uses paginator helpers instead of manual iteration.
    - Context (``context.Context``) is threaded through all calls for proper
      cancellation and timeout handling.

    **Centralized session factory (NERD001 SPEC011):**
    ``internal/tools/aws.go`` provides ``NewAWSConfig()`` that automatically
    sets the Floci endpoint when ``HMD_ENVIRONMENT=local``.

.. spec:: Docker interaction -- subprocess exec preserved
    :id: HMD_CLI_NERD002_SPEC006
    :links: HMD_CLI_NERD002
    :status: proposed

    The current Python code shells out to ``docker compose`` via
    ``cement.utils.shell.cmd()``. The Go port **preserves this approach**.

    **Rationale:**

    - Docker Compose v2 is a standalone Go binary. Embedding the Compose
      library would add ~50 MB to the binary and couple the CLI to a specific
      Compose version.
    - The current CLI invokes ``docker compose up/down/pull`` with multiple
      ``-f`` flags. This is simple ``os/exec.Command`` in Go.
    - Docker Compose's own variable interpolation (``${VAR:-default}``) handles
      environment variable expansion in compose files. Reimplementing this in
      Go would be fragile.

    **Implementation:**

    .. code-block:: go

        func (o *Orchestrator) composeCommand(
            files []string, args ...string,
        ) *exec.Cmd {
            composeCMD := os.Getenv("DOCKER_COMPOSE_CMD")
            if composeCMD == "" {
                composeCMD = `["docker", "compose"]`
            }
            var base []string
            json.Unmarshal([]byte(composeCMD), &base)

            cmdArgs := append(base,
                "--project-directory", filepath.Join(o.hmdHome, ".cache"),
                "--project-name", "local_neuronsphere",
            )
            for _, f := range files {
                cmdArgs = append(cmdArgs, "-f", f)
            }
            cmdArgs = append(cmdArgs, args...)
            return exec.Command(cmdArgs[0], cmdArgs[1:]...)
        }

    **Selective Docker SDK usage:** For operations requiring programmatic
    control (inspecting container status, reading logs, health checks), use
    ``github.com/docker/docker/client``. For orchestration (up, down, pull),
    continue using subprocess exec.

    **Port validation** (``portcheck.go``): Reimplements ``port_validator.py``
    natively using ``net.DialTimeout`` for port probing.

.. spec:: YAML/JSON handling with native Go libraries
    :id: HMD_CLI_NERD002_SPEC007
    :links: HMD_CLI_NERD002
    :status: proposed

    .. list-table::
       :header-rows: 1
       :widths: 20 30 50

       * - Format
         - Go Library
         - Used For
       * - YAML
         - ``gopkg.in/yaml.v3``
         - Docker Compose files, connections.yml
       * - JSON
         - ``encoding/json`` (stdlib)
         - nsplugin.json, manifest.json, platform_bom.json, resources.json,
           config_local.json, SERVICE_CONFIG
       * - dotenv
         - ``github.com/joho/godotenv``
         - ``$HMD_HOME/.config/hmd.env``

    ``yaml.v3`` preserves comments and ordering when marshaling, which matters
    for Docker Compose files that are read, modified, and written to cache.

.. spec:: Interactive prompts replacing InquirerPy
    :id: HMD_CLI_NERD002_SPEC008
    :links: HMD_CLI_NERD002
    :status: proposed

    InquirerPy is used in two places:

    1. ``hmd neuronsphere configure`` -- checkbox prompt for plugin selection
    2. ``hmd configure`` (via ``hmd_cli_tools.prompt_tools``) -- text input
       prompts for HMD_HOME, HMD_REPO_HOME, etc.

    **Go replacement:** `huh <https://github.com/charmbracelet/huh>`_ from the
    Charm ecosystem. Provides text input with defaults, checkbox/multi-select,
    confirmation, and password prompts. Active maintenance, quality TUI
    rendering, and Bubble Tea integration for potential future TUI features.

    **``prompt_for_values`` mapping:**

    The Python ``prompt_for_values(questions)`` utility accepts a dictionary of
    ``{key: {prompt, default, hidden, required}}`` and returns results. The Go
    equivalent is a ``PromptForValues()`` function in ``internal/tools/prompt.go``
    that iterates over a ``[]PromptConfig`` slice and builds a ``huh.Form``.

.. spec:: External artifacts and go:embed
    :id: HMD_CLI_NERD002_SPEC009
    :links: HMD_CLI_NERD002
    :status: proposed

    The ``manifest.json`` ``pre_build_artifacts`` system fetches build archives
    from 9 external repos and unpacks them into the ``external/`` directory.
    These artifacts contain ``src/local/nsplugin.json``, Docker Compose files,
    configuration files, and initialization scripts.

    **In Go, external artifacts are embedded at build time:**

    .. code-block:: go

        //go:embed embed/external/transform/src/local/*
        //go:embed embed/external/trino/src/local/*
        //go:embed embed/external/airflow/src/local/*
        //go:embed embed/external/clickhouse/src/local/*
        //go:embed embed/external/telemetry/src/local/*
        //go:embed embed/external/hive-metastore/src/local/*
        //go:embed embed/external/apache_superset/src/local/*
        //go:embed embed/external/hyperdx/src/local/*
        //go:embed embed/external/telemetry-debug/src/local/*
        var externalArtifacts embed.FS

    **Build process:**

    1. ``pre_build_artifacts`` downloads and unpacks external archives into
       ``embed/external/`` (same mechanism as today, different destination).
    2. ``go build`` embeds these files via ``//go:embed`` directives.
    3. At runtime, plugin ``base.go`` reads from the embedded filesystem first,
       then falls back to the local plugin loader for user overrides.

    **manifest.json compatibility:** The ``pre_build_artifacts`` format is
    unchanged. A new ``go`` build command type is added alongside the existing
    ``python``, ``docker``, ``cdktf``, and ``helm`` types.

    **File operations:** Functions like ``copy_configs``,
    ``copy_postgres_scripts``, and ``create_required_dirs`` in Python's
    ``base.py`` become Go functions that read from ``embed.FS`` and write to
    ``$HMD_HOME`` using ``io.Copy`` and ``os.MkdirAll``.

.. spec:: hmd-cli-tools shared Go module
    :id: HMD_CLI_NERD002_SPEC010
    :links: HMD_CLI_NERD002
    :status: proposed

    ``hmd-cli-tools`` provides shared utilities used by all CLI repos. In the
    Go monorepo, these become packages under ``internal/tools/``:

    .. list-table::
       :header-rows: 1
       :widths: 30 30 40

       * - Python Module
         - Go Package
         - Notes
       * - ``hmd_cli_tools.py`` (load/set env)
         - ``tools/env.go``
         - ``godotenv`` for hmd.env
       * - ``build_tools.py``
         - ``build/tools.go``
         - Manifest parsing, build commands
       * - ``credential_tools.py``
         - ``tools/credentials.go``
         - ``go-keyring`` for system keyring
       * - ``core_s3_tools.py``
         - ``tools/s3.go``
         - aws-sdk-go-v2/service/s3
       * - ``k8s_tools.py``
         - ``tools/k8s.go``
         - ``client-go`` (official K8s Go client)
       * - ``okta_tools.py``
         - ``tools/okta.go``
         - ``golang.org/x/oauth2``
       * - ``prompt_tools.py``
         - ``tools/prompt.go``
         - ``charmbracelet/huh``
       * - ``rds_tools.py``
         - ``tools/rds.go``
         - aws-sdk-go-v2/service/rds
       * - ``vpn_tools.py``
         - ``tools/vpn.go``
         - Subprocess exec (openvpn/wireguard)
       * - ``cdktf_tools.py``
         - ``cdktf/tools.go``
         - Subprocess exec (cdktf CLI)

    **AWS client factory** (``tools/aws.go``): Provides ``NewAWSConfig()`` that
    automatically sets the Floci endpoint when ``HMD_ENVIRONMENT=local``. This
    implements NERD001 SPEC011 natively in Go and eliminates per-CLI-repo
    endpoint switching.

.. spec:: NERD001 Floci integration in Go
    :id: HMD_CLI_NERD002_SPEC011
    :links: HMD_CLI_NERD002
    :status: proposed

    NERD001 specifies 12 specs for Floci/Deploy mode. The Go port implements
    all CLI-side specs natively rather than porting from Python:

    **NERD001 SPEC001 (Floci replaces MiniStack):**
    ``internal/neuronsphere/floci_deployer.go`` implements the deployer using
    aws-sdk-go-v2. The Python ``_get_client()`` pattern becomes a
    ``FlociClient`` struct with typed service clients.

    **NERD001 SPEC002 (Admin control plane):**
    ``internal/neuronsphere/deploy_mode.go`` implements
    ``StartDeployMode()`` which starts ``docker-compose.admin.yml`` and waits
    for health checks on Floci, PostgreSQL, ms-naming, and ms-deployment.

    **NERD001 SPEC003 (local_overrides.json):**
    Embedded in the binary via ``//go:embed``. Parsed into a
    ``LocalOverrides`` struct.

    **NERD001 SPEC004 (Platform BOM):**
    ``internal/neuronsphere/bom.go`` implements BOM generation by walking
    ``manifest.json`` dependency trees. BOM is embedded at build time via
    ``//go:embed embed/platform_bom.json``.

    **NERD001 SPEC005 (Seed deployment graph):**
    HTTP client calls to local ms-deployment API using ``net/http``.

    **NERD001 SPEC006 (LocalWorkflowRunner):**
    This lives in ``hmd-ms-deployment`` (Python), not in the CLI. The CLI's
    role is to trigger changeset application via the ms-deployment API. The
    runner itself remains Python inside the ms-deployment container.

    **NERD001 SPEC007-008 (User-extensible deployments, Legacy mode):**
    Implemented directly in the Go command handlers.

    **NERD001 SPEC009-010 (nsplugin unchanged, plugin discovery):**
    Covered by SPEC002 and SPEC003 of this NERD.

    **NERD001 SPEC011 (CLI tools target Floci):**
    Covered by SPEC010 ``tools/aws.go`` session factory.

    **NERD001 SPEC012 (Phased migration):**
    The Go port supersedes the Python-first migration. Phase 0 (Floci drop-in
    replacement) can still be done in Python as a quick win while the Go port
    is underway. Phases 1-4 are implemented directly in Go.

.. spec:: Build system -- manifest.json compatibility
    :id: HMD_CLI_NERD002_SPEC012
    :links: HMD_CLI_NERD002
    :status: proposed

    The current build system uses ``manifest.json`` with
    ``"commands": [["python"]]`` which triggers ``hmd python build``.

    **New manifest.json for the Go CLI:**

    .. code-block:: json

        {
            "name": "hmd-cli",
            "description": "NeuronSphere CLI",
            "build": {
                "pre_build_artifacts": [
                    ["hmd-ms-transform@1.0.811:build",
                     "embed/external/transform"],
                    ["hmd-inf-trino@0.1.202:build",
                     "embed/external/trino"],
                    ["hmd-inf-hive-metastore@0.2.70:build",
                     "embed/external/hive-metastore"],
                    ["hmd-app-airflow@0.4.315:build",
                     "embed/external/airflow"],
                    ["hmd-inf-clickhouse@0.1.23:build",
                     "embed/external/clickhouse"],
                    ["hmd-inf-otel-collector@0.1.158:build",
                     "embed/external/telemetry"],
                    ["hmd-inf-hyperdx@0.1.4:build",
                     "embed/external/hyperdx"],
                    ["hmd-inf-superset@0.5.228:build",
                     "embed/external/apache_superset"],
                    ["hmd-ms-telemetry-debug@0.1.5:build",
                     "embed/external/telemetry-debug"]
                ],
                "commands": [["go"]]
            }
        }

    **``hmd go build``** is a new build command type:

    1. Run ``pre_build_artifacts`` to populate ``embed/external/``.
    2. Run ``go build -ldflags "-X main.version=${VERSION}" -o target/hmd ./cmd/hmd/``
    3. For releases, use GoReleaser for cross-compilation.

    **Bootstrap strategy:** During the transition period, the Python
    ``hmd build`` command gains a ``go`` handler that runs ``go build``. Once
    the Go CLI is self-hosting, it builds itself.

.. spec:: Distribution -- single binary via multiple channels
    :id: HMD_CLI_NERD002_SPEC013
    :links: HMD_CLI_NERD002
    :status: proposed

    **Primary distribution channels:**

    1. **GitHub Releases** -- GoReleaser produces binaries for:

       - ``darwin/amd64``, ``darwin/arm64`` (macOS Intel + Apple Silicon)
       - ``linux/amd64``, ``linux/arm64``
       - ``windows/amd64``

    2. **Homebrew tap** -- ``neuronsphere/tap/hmd``:

       .. code-block:: ruby

           class Hmd < Formula
             desc "NeuronSphere Data Platform CLI"
             homepage "https://github.com/neuronsphere/hmd-cli"
             # GoReleaser auto-updates the formula
           end

    3. **Docker image** -- ``ghcr.io/neuronsphere/hmd-cli:latest`` for CI
       environments and users who prefer containerized tools.

    4. **Install script** -- A curl-pipe-sh installer that detects platform
       and downloads the correct binary from GitHub Releases.

    **Self-update:** A new ``hmd self-update`` command checks GitHub Releases
    for newer versions and replaces the binary in-place.

    **Binary size target:** Under 50 MB including all embedded assets (compose
    files, configs, skill markdown files). For reference: ``kubectl`` is ~49 MB,
    ``helm`` is ~46 MB, ``terraform`` is ~85 MB.

.. spec:: Backwards compatibility and migration path
    :id: HMD_CLI_NERD002_SPEC014
    :links: HMD_CLI_NERD002
    :status: proposed

    **100% command-line compatibility.** Every ``hmd <command> <subcommand>
    <flags>`` invocation must produce the same behavior. Robot Framework tests
    (``test/01__smoke_tests.robot``, ``test/02__integration_tests.robot``) are
    the compatibility specification -- they invoke the CLI as a subprocess and
    verify output and exit codes.

    **Config file compatibility -- all formats unchanged:**

    - ``$HMD_HOME/.config/hmd.env``
    - ``nsplugin.json``
    - ``manifest.json`` (for service repos; CLI repo gets new ``go`` command)
    - ``connections.yml``
    - ``meta-data/config_local.json``
    - ``docker-compose.*.yml`` (consumed by Docker Compose, not the CLI)

    **Environment variable compatibility -- all preserved:**

    - All ``HMD_*`` variables
    - All ``HMD_LOCAL_NEURONSPHERE_ENABLE_*`` variables
    - ``DOCKER_COMPOSE_CMD``
    - ``HMD_LOCAL_PLUGINS``, ``HMD_LOCAL_PLUGINS_SCAN_REPO_HOME``

    **Migration path for users:**

    1. Replace ``pip install hmd-cli-*`` with single binary installation
       (brew, curl, or GitHub Release).
    2. No configuration changes required.
    3. No nsplugin.json changes required.
    4. No Docker Compose file changes required.

    **Migration path for hmd-cli-* developers:**

    1. Port controller code from Python/Cement to Go/Cobra package.
    2. Register command in ``internal/app/registry.go``.
    3. Add Go tests.
    4. Archive the old ``hmd-cli-*`` Python repo.

    **Parallel operation during transition:** The Go binary can coexist with
    the Python CLI by using a different name (``hmd2``) until cutover.

.. spec:: Gaps, risks, and mitigations
    :id: HMD_CLI_NERD002_SPEC015
    :links: HMD_CLI_NERD002
    :status: proposed

    **HIGH RISK: Scope and timeline.**

    Porting 40 CLI repos is a large undertaking. The phased approach (SPEC016)
    mitigates this by delivering incremental value, but the total effort is
    substantial. If full parity is not achievable, Phase 1 (neuronsphere
    commands) delivers the highest-value subset.

    **MEDIUM RISK: Cement handler/hook system.**

    Cement's ``handler`` and ``hook`` systems allow plugins to register
    callbacks. The current codebase uses hooks minimally -- the primary
    extension mechanism is entry_points, not hooks. The Go port replaces both
    with interface-based dispatch and init-time registration. Each CLI repo
    must be audited for hook usage during porting.

    **MEDIUM RISK: boto3 high-level abstractions.**

    Some CLI repos (``hmd-cli-deploy``, ``hmd-cli-cdktf``) may use boto3's
    ``resource`` API or session abstractions not directly available in
    aws-sdk-go-v2. Each repo must be audited and ported to service clients.

    **MEDIUM RISK: Python-specific libraries.**

    .. list-table::
       :header-rows: 1
       :widths: 30 30 40

       * - Python
         - Go Replacement
         - Risk Notes
       * - ``pg8000``
         - ``pgx``
         - API differs; connection pooling model different
       * - ``kubernetes``
         - ``client-go``
         - Go client is better maintained (canonical)
       * - ``colorlog``
         - ``zerolog`` + ``lipgloss``
         - Output format may differ slightly
       * - ``requests``
         - ``net/http``
         - Standard library, lower risk

    **LOW RISK: Jinja2 template syntax differences.**

    No bundled plugins currently use Jinja2 templates. User-authored local
    plugins with templates will need syntax updates. A migration guide and
    optional ``pongo2`` fallback mitigate this.

    **LOW RISK: Robot Framework test compatibility.**

    Tests invoke the CLI as a subprocess. The binary name remains ``hmd``.
    Command names and flags are preserved. Tests should pass without
    modification once the Go binary is on PATH.

    **LOW RISK: Build system bootstrap.**

    The Python CLI must build the first Go binary. After that, the Go CLI is
    self-hosting. During the transition, both CLIs coexist.

    **GAP: ``hmd-cli-bender`` Robot Framework integration.**

    ``hmd-cli-bender`` wraps Robot Framework (a Python tool). The Go port of
    bender shells out to ``robot`` or ``pabot`` as a subprocess, same as today.
    Robot Framework itself is not ported -- users still need Python installed
    if they run tests. However, running tests is a developer activity, not an
    end-user activity, so this does not conflict with the "Docker + single
    binary" goal for end users.

    **GAP: ``hmd-cli-bartleby`` Sphinx documentation.**

    ``hmd-cli-bartleby`` wraps Sphinx (a Python tool). Same situation as
    bender: the Go CLI shells out to Sphinx. Developers building docs still
    need Python. Again, this is a developer activity.

    **GAP: ``hmd-cli-python`` build commands.**

    ``hmd python build`` runs ``setup.py bdist_wheel``. The Go CLI shells out
    to Python for this. Python repos still need Python to build -- only the
    CLI itself becomes Python-free for end users.

    **GAP: Cross-repo pre_build_artifacts.**

    The ``pre_build_artifacts`` system downloads archives from a build service.
    The Go build must run this step before ``go build`` so that ``//go:embed``
    can include the artifacts. The artifact download logic must be available as
    a standalone script or early-phase Go tool to avoid a chicken-and-egg
    problem.

    **GAP: Third-party CLI tool dependencies.**

    The hmd CLI shells out to many tools beyond Docker: ``terraform``,
    ``cdktf``, ``helm``, ``kubectl``, ``robot``, ``sphinx-build``,
    ``python``, ``node``, ``npm``. These remain external dependencies. The
    single-binary goal applies to the ``hmd`` binary itself, not to the
    entire tool ecosystem. The Docker image distribution channel
    (SPEC013) can bundle all tools for CI environments.

.. spec:: Phased implementation strategy
    :id: HMD_CLI_NERD002_SPEC016
    :links: HMD_CLI_NERD002
    :status: proposed

    **Phase 0: Foundation (Weeks 1-4)**

    - Create ``hmd-cli`` monorepo with Go module structure.
    - Implement ``cmd/hmd/main.go`` with Cobra root command.
    - Implement ``internal/tools/env.go`` (env loading, hmd.env management).
    - Implement ``internal/tools/prompt.go`` (interactive prompts).
    - Port ``hmd configure`` (simplest controller, validates the framework).
    - Port ``hmd version``.
    - Set up GoReleaser for cross-platform builds.
    - Set up CI pipeline (build, test, lint).
    - **Deliverable:** ``hmd configure`` and ``hmd version`` work from the Go
      binary.

    **Phase 1: Core neuronsphere -- Legacy mode (Weeks 5-12)**

    - Implement ``internal/plugins/interface.go`` and plugin registry.
    - Port all 13 bundled plugins to Go.
    - Implement ``internal/localplugin/loader.go`` (LocalPluginLoader).
    - Implement ``internal/localplugin/validator.go``.
    - Implement ``internal/neuronsphere/orchestrator.go`` (start/stop/restart).
    - Implement ``internal/neuronsphere/floci_deployer.go`` (NERD001 SPEC001).
    - Embed Docker Compose files and service configs via ``//go:embed``.
    - Implement ``pre_build_artifacts`` download in Go build pipeline.
    - Port all ``hmd neuronsphere`` subcommands: ``up``, ``down``, ``restart``,
      ``configure``, ``validate-plugin``, ``init-plugin``,
      ``list-local-plugins``, ``update-images``, ``run``.
    - **Deliverable:** Full ``hmd neuronsphere`` Legacy mode works from Go
      binary. Robot Framework smoke tests pass.

    **Phase 2: Floci Deploy mode (Weeks 13-18)**

    - Implement ``internal/neuronsphere/deploy_mode.go`` (NERD001 SPEC002).
    - Implement ``internal/neuronsphere/bom.go`` (NERD001 SPEC004).
    - Implement deployment graph seeding (NERD001 SPEC005).
    - Implement ``local_overrides.json`` handling (NERD001 SPEC003).
    - Integrate with ``hmd-ms-deployment`` API for changeset application.
    - **Deliverable:** ``hmd neuronsphere up`` with
      ``HMD_LOCAL_NEURONSPHERE_MODE=deploy`` works end-to-end.

    **Phase 3: Build and deploy tooling (Weeks 19-26)**

    - Port ``hmd build`` (``hmd-cli-build``).
    - Port ``hmd deploy`` (``hmd-cli-deploy``).
    - Port ``hmd docker``, ``hmd helm``, ``hmd cdktf``.
    - Port ``hmd python``, ``hmd typescript`` (build tool wrappers).
    - Implement ``internal/tools/aws.go`` session factory (NERD001 SPEC011).
    - **Deliverable:** ``hmd build`` and ``hmd deploy`` work from Go binary.
      The Go CLI can build itself.

    **Phase 4: Supporting commands (Weeks 27-32)**

    - Port ``hmd login``, ``hmd secrets``.
    - Port ``hmd repo`` (including plugin hooks for remotes).
    - Port ``hmd bender`` (Robot Framework test runner wrapper).
    - Port ``hmd bartleby`` (Sphinx documentation wrapper).
    - Port ``hmd debug``, ``hmd dbt``.
    - Port ``hmd transform-can``, ``hmd transform-deploy``.
    - Port remaining CLI repos.
    - **Deliverable:** All command groups ported. Full feature parity.

    **Phase 5: Cutover and deprecation (Weeks 33-36)**

    - Run both Python and Go CLIs in parallel with integration test
      comparison.
    - Distribute Go binary via Homebrew, GitHub Releases, install script.
    - Announce deprecation of Python pip packages.
    - Update all documentation.
    - Archive individual ``hmd-cli-*`` Python repos.
    - **Deliverable:** Go binary is the sole distribution. Python packages
      archived.

    **Total estimated timeline: ~36 weeks.** Each phase is independently
    shippable. The Go binary coexists with the Python CLI during transition
    by using a different binary name (e.g., ``hmd2``) until Phase 5 cutover.

Dependency Map
--------------

.. list-table::
   :header-rows: 1
   :widths: 25 30 45

   * - Python Package
     - Go Replacement
     - Notes
   * - ``cement``
     - ``spf13/cobra`` + ``spf13/viper``
     - CLI framework + configuration
   * - ``boto3`` / ``botocore``
     - ``aws/aws-sdk-go-v2``
     - AWS client (service-specific packages)
   * - ``pyyaml``
     - ``gopkg.in/yaml.v3``
     - YAML parsing
   * - ``json`` (stdlib)
     - ``encoding/json`` (stdlib)
     - JSON parsing
   * - ``jinja2``
     - ``text/template`` (stdlib)
     - Template rendering
   * - ``requests``
     - ``net/http`` (stdlib)
     - HTTP client
   * - ``kubernetes``
     - ``k8s.io/client-go``
     - Kubernetes client (canonical Go lib)
   * - ``pg8000``
     - ``github.com/jackc/pgx``
     - PostgreSQL client
   * - ``inquirerpy``
     - ``github.com/charmbracelet/huh``
     - Interactive prompts
   * - ``colorlog``
     - ``log/slog`` + ``lipgloss``
     - Structured colored logging
   * - ``python-dotenv``
     - ``github.com/joho/godotenv``
     - .env file loading
   * - ``importlib_metadata``
     - (not needed)
     - Replaced by compile-time registration
   * - ``pathlib`` (stdlib)
     - ``path/filepath`` (stdlib)
     - Path manipulation
   * - ``shutil`` (stdlib)
     - ``io``, ``os`` (stdlib)
     - File copy operations

Risk Assessment
---------------

.. list-table::
   :header-rows: 1
   :widths: 10 35 55

   * - Level
     - Risk
     - Mitigation
   * - High
     - Scope: 40 CLI repos is a large porting effort
     - Phased delivery; Phase 1 (neuronsphere) covers the highest-value
       subset. Each phase is independently shippable.
   * - Medium
     - boto3 abstractions differ from aws-sdk-go-v2
     - Audit each CLI repo for boto3 usage patterns before porting.
       ``ministack_deployer.py`` already uses low-level client calls.
   * - Medium
     - Cement handler/hook system has no direct Go equivalent
     - Hooks are used minimally. Audit and replace with interface dispatch.
   * - Medium
     - Jinja2 template syntax incompatibility
     - No bundled templates exist. Provide migration guide. Offer pongo2
       fallback if user-authored templates are widespread.
   * - Low
     - Robot Framework test compatibility
     - Tests invoke CLI as subprocess. Binary name and flags preserved.
   * - Low
     - Build system bootstrap (Go needs Python CLI to build first)
     - Standalone Makefile + ``go build`` for initial bootstrap. Python CLI
       gains ``hmd go build`` handler.
   * - Low
     - Developer tool dependencies (bender needs Python, bartleby needs
       Sphinx)
     - These are developer activities, not end-user. Docker image bundles
       all tools for CI.
