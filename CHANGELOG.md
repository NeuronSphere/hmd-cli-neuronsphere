# Changelog

## 2026-09-21

- feat: `nsctl lock --resolve` pins ranges to the newest published version,
  from the lock entry's OCI `source` first and the cloud librarian second,
  and a regenerate keeps each entry's `source`; `nsctl repoclass validate`
  checks a stack's lock coverage and that every role is bound, external or
  pinned; `nsctl repoclass local add|remove|bind|require|set-default-profiles|list`
  author the `local` section by verb (NERD019 SPEC005/006/008).
- feat: `nsctl stack init <name>` scaffolds a stack RepoClass with its CI
  workflow, and with `--from-env <env>` or `--from-bom <export>` plus
  `--select a,b` derives the `local` section and lock from a running
  environment's graph: the substrate becomes bound roles, instances other
  stacks declared become `external` roles with a suggestion, the rest are
  bundled at their running versions with each root in its own profile;
  working-tree instances are refused unless `--bundle-local`; `--dry-run`,
  `--diff` and `--update` serve the CI refresh loop. `--from-env` writes
  `meta-data/reference-bom.json`. The artifact cache now keeps each zip's
  original bytes so a derived stack builds offline byte-for-byte (NERD019).
- feat: a reusable `.github/actions/setup-nsctl` composite action installs
  a pinned or latest `nsctl` release in a workflow.
- feat: `nsctl stack add` composes with what the environment already has
  (NERD017 SPEC010): a dependency role is bound to an existing instance that
  produces its resource type, a companion already declared under the same
  name is shared, an unsatisfied role refuses naming the resource, the
  stack's `suggest`, and the `--name` remedy; a same-name class clash is
  refused. Records keep `declared` apart from bound, so `stack remove`
  never takes an instance the stack only used.
- feat: `nsctl stack build` writes the stack artifact as an OCI image layout
  under `build/stack`, offline and deterministically, taking each pinned zip
  from `--artifacts`, the artifact cache, the lock entry's OCI `source`, or
  the cloud librarian, in that order; `nsctl stack push --from <layout>
  [--bump]` publishes it, `--bump` choosing the next version from the
  registry's own tags and refusing to overwrite a published one (NERD019).
- feat: `nsctl artifact push <dir|zip> <ref>` publishes one RepoClass build
  zip as an OCI artifact, `nsctl artifact pull <oci-ref>` fetches one with no
  tenant, and a `neuronsphere.lock` entry may name such a `source`
  (NERD016 SPEC009).
- fix: `nsctl.toml` written by `plugin install` no longer carries an empty
  `default_profile`.
- test: `test/nsctl_cli.robot` gains the NERD017/NERD018 contract cases
  (path plugins run with argv and exit status, missing binaries name the
  install verb, reserved names warn, the GitHub scheme is refused, a closed
  registry port fails cleanly).
- feat: stacks (NERD017): `nsctl stack add|pull|list|remove|versions|push`.
  A stack is a RepoClass with a `local` section and a `neuronsphere.lock`,
  published as one OCI artifact (the lock as config, one layer per build zip).
  `stack add` fetches it anonymously from a public namespace (a bare name
  expands to `ghcr.io/hmdlabs/stacks`), verifies and caches every zip, and
  declares the instances through the `--from-repo` planner with the stack
  itself as `source: artifact`; its bindings live in a new `stacks:` record
  of the environment manifest. `stack push` publishes from a repository,
  taking companion zips from `--artifacts` or the cloud librarian and
  writing their digests into the lock. `nsctl lock` now records digests for
  cached artifacts.
- feat: CLI plugins (NERD018): `nsctl plugin install|remove|list|update|push`
  install a published plugin from an OCI registry (a bare name expands to
  `ghcr.io/hmdlabs/plugins`) into `$HMD_HOME/.cache/neuronsphere/plugins/`
  and declare it as `[plugin.<name>]` in `nsctl.toml`; a declared plugin runs
  as `nsctl <name> ...` with its arguments passed verbatim and its exit
  status returned unchanged. Nothing is scanned: a plugin exists because the
  file names it. `path = ...` declares a local dev build.
- feat: `internal/oci`, an OCI Distribution client for artifact sources
  (NERD016): anonymous and bearer pulls through the `WWW-Authenticate`
  challenge, digest-verified manifests and blobs, `tags/list` version
  enumeration, monolithic push with an `artifactType` retry, and SPEC006
  credential resolution (`--token`, `HMD_REGISTRY_TOKEN`, a profile's new
  `registry_url` with the login token, else anonymous). Ships with an
  in-process fake registry (`internal/oci/ocitest`) for consumers' tests.
- feat: `neuronsphere.lock` entries may carry an optional `digest` under
  schema version 1, and the artifact cache records each unpacked zip's digest
  (`artifact.Digest`) so a lock can be filled offline (NERD017 SPEC007).
- feat: `nserr.Silent` for an exit status that has already spoken for itself
  (NERD018 SPEC005).

## 2026-09-20

- fix: `--home` is authoritative for the control plane's compose interpolation.
  With `HMD_HOME` exported for another home in the same shell, `control-plane
  start --home X` mounted that other home's Floci data dir and joined its
  network, so a "fresh" control plane silently ran the other home's Floci.
- feat: `env start` orders and runs deploys client-side (NERD0015): entries are
  registered as DEPLOY_NEXT plans with `register_deployed_instance`, configured
  through `get_deployment_config`, and executed by the in-process runner, which
  now schedules independent nodes in parallel (`HMD_LOCAL_RUNNER_PARALLELISM`).
  No ChangeSet is created; the local control plane runs the deployment
  service's core image.
- feat: the control plane runs `hmd-ms-deployment-core` under the
  `hmd-ms-deployment` service name (image class vs service name), bundles the
  core descriptor, and pins with `HMD_LOCAL_VERSION_HMD_MS_DEPLOYMENT_CORE`.
- feat: `HMD_LOCAL_IMAGE_<SERVICE>` runs a full image reference for a
  foundation service (e.g. the premium `hmd-ms-deployment` image) locally.
- fix: a foundation-service image absent locally is pulled from the registry
  named by `HMD_CONTAINER_REGISTRY` / `HMD_LOCAL_NS_CONTAINER_REGISTRY` before
  giving up (the published registry is still never substituted for a named one).
- refactor: remove nsrunner, `nsctl runner serve`, `env attach`,
  `HMD_LOCAL_SERVICE_SUBMIT`, `HMD_WORKFLOW_RUNNER_URL`; the nsctl image
  survives as `hmd-img-nsctl` (`HMD_NSCTL_IMAGE`) for the identity provider.
  The runner is archived in `hmd-lib-nsrunner`.

## 2026-09-18

- feat(ci): release nsctl on every push to main with a BACON build number

  Release tags are now `MAJOR.MINOR.BUILD` with no `v`, the build number
  issued by hmd-ms-projects, so the binary carries the same version as every
  other NeuronSphere artifact. release.yml mints a per-run Okta service token
  (what `hmd login service` does), runs the checks, asks ms-projects for the
  build number (idempotent on the run id), tags, and runs GoReleaser in the
  same job. The repository moved to the neuronsphere org and VERSION is 1.0.

- fix(nsctl): retry service probes across Floci Lambda cold starts

  Floci evicts an idle Lambda container on its own timer and cold-starts a
  fresh one in under a second on the next request. A single probe landing in
  that gap read as "service down" in `nsctl status` and in the deployment
  client's reachability check. Both now try three times 300 ms apart before
  giving up.

- fix(nsctl): repopack never packs a repo class's service code

  `tools/repopack` packed all of `src/`, so the binary stayed free of
  `src/python` only because the published build artifacts happened not to
  contain it. The deploy descriptor is Apache 2.0 in every repo class but
  `src/python`, `src/typescript` and `src/docker` are BUSL 1.1 in
  hmd-ms-deployment, hmd-ms-librarian and hmd-app-neuronsphere, and one such
  file inside nsctl would make the Apache-licensed binary a mixed-licence
  artifact. Those directories are now skipped structurally, and a test in
  `internal/bundled` fails the build if a shipped archive contains them.

## 2026-09-17

- fix(nsctl): `repoclass … set-command exec` tolerates the global `--home` flag

  The exec verbs parse no flags of their own so the argv's flags stay the
  argv's, which also meant the root's `--home` reached them raw and was
  mistaken for the argv (`set-command takes exec <argv...>`). It is now
  consumed like `--path`, in both spellings.

- fix(nsctl): `env plan` clears same-plan producers by their declared produces

  A resource-typed dependency bound to an instance that is itself new in the
  plan was always flagged by `env plan`'s candidate check: `suggest_resource_dependencies`
  only enumerates instances that already have a deployment, while `apply_changeset`
  creates deployment records in changeset order and validates the consumer
  against the producer's declared produces. `CandidateWarnings` now reads the
  same `meta-data/resources` declarations (own type and parent) for a same-plan
  target, so a correctly typed producer no longer warns and a wrongly typed one
  still does. This was the `airflow role=trino` warning on the first live run.

- feat(nsctl): `env plan` previews what `env apply` would add or change (E5)

  `nsctl env plan [name] [--output text|json|md]` builds the same reconcile
  diff and catalog registrations `env apply` does, then POSTs the would-be
  ChangeSet definition to `hmd-ms-deployment`'s `validate_changeset` and warns
  on resource-typed dependencies `validate_changeset` accepts (the bound
  instance exists) but `apply_changeset` would reject (it does not produce the
  required resource type) -- a check that otherwise only runs at apply time.
  Never calls `apply_changeset`, never runs a node, never writes the
  environment's reconcile snapshot. `--output md` renders a pasteable review
  artifact for a pull request body. `bom.Seeder.Seed`'s changeset-independent
  catalog writes are now the reusable `RegisterCatalog`.

- feat(nsctl): redeploy foundation Lambdas without a full re-bootstrap (NERD014 SPEC010)

  `nsctl control-plane reset [repo-class...]` redeploys `hmd-ms-naming`,
  `hmd-ms-artifact-lib` and `hmd-ms-deployment` (or only the ones named) on a
  control plane that has already bootstrapped, re-resolving each one's
  version the way a first bootstrap does. Those three are only ever deployed
  from inside `Bootstrap`, which runs once per `HMD_HOME` -- `control-plane
  stop && start` never redeploys them, so there was no way to pick up a
  newer image short of a full purge. `reset` rediscovers every foundation
  service's API Gateway and rewrites+reloads the control-plane routes in
  full (`SetupService` rotates the gateway id on every call, so a partial
  rewrite would drop the untouched services' routes), removes the idle
  Lambda container left over from the last invocation so the next one is a
  genuine cold start, and polls the freshly-routed endpoint briefly before
  returning so a caller never races Floci's own propagation. Also bumps the
  bundled `hmd-ms-deployment` pin from `0.4.854` to `0.4.858`
  (`meta-data/manifest.json`), the default a brand-new control plane's first
  bootstrap resolves to.

  The `hmd manifest` and `hmd describe` verbs in Go, needing no HMD_HOME:
  `init`, `describe [--json]`, `validate [--strict] [--json]`, and the write
  verbs under `build`, `deploy`, `test` and `discovery` (`set-mechanism`,
  `add-command`, `set-command exec`, `set-image`, `add-dependency`,
  `add-resource`, `set-config`, `set-summary`, `add-entry-point`,
  `add-capability`, `add-related-doc` and their inverses). The store
  (`internal/bacon`) holds the document as an ordered map so unknown keys
  and key order survive a rewrite, writes JSON in `json.dump(indent=2)`'s
  layout so the Python front end sees no churn, reads a TOML tier for
  `describe`/`validate` and refuses to write it. `validate` hand-codes the
  BACON schema's shape plus SPEC011's error/warning/note rules. Authoring
  the motivating acme product manifest through the verbs reproduces the
  hand-written file byte-for-byte.

- feat(nsctl): selectable environment substrate (NERD014)

  `nsctl env start --substrate none|core|full` chooses how much infrastructure
  an environment runs -- nothing beyond the control plane, the database and
  graph without a cluster, or everything -- and records the choice as
  `substrate:` in `environments/<slug>.yaml`, where `env apply`, `env status`,
  `env list` and `repo list` read it. `none` is for NERD009 foreign-toolset
  repos, whose reset used to spend six of seven minutes on a cluster nothing
  scheduled onto. A bad value is refused before the control plane starts; a
  `none` environment binding a dependency to `local-neuronsphere` is refused
  naming the fix; raising the mode adds what is missing, lowering it destroys
  nothing. `Seed` no longer fails a changeset with no core instance on the
  core's missing RepoClassVersion. `bom.SubstrateFor`/`SubstrateNames`,
  `manifest.Substrate`, `environment.planFor`, `status.Reporter.Substrate`.


## 2026-09-16

- feat(nsctl): run a repo class's own exec command in its own image

  A manifest declaring `deploy.commands [["exec", ...]]` deploys under
  `nsctl env apply` by running that argv as the node, in `deploy.image` or
  projectbuilder when none is named, under NERD009 SPEC005's contract: no
  `--entrypoint`, no mounted script, no `/root` path, no `HMD_HOME` or dummy
  registry credentials, and exactly the injected set the spec lists
  (`KUBECONFIG` at `/etc/nsctl/kubeconfig`). `exec` outranks
  `src/local/deploy_local.sh`; two `exec` entries are refused;
  `HMD_REPO_VERSION` is new on both paths. `HMD_INSTANCE_CONFIG`, which
  carried `{}` for every service-generated node, is now lifted out of the
  generated script's heredoc with every `hmd_resource_ref` resolved through
  `get_deployment_resources`, so a foreign node may depend on a
  same-changeset instance. The runner creates `meta-data/resources_output/`
  for a foreign node. Documented in `docs/nsctl.rst`; NERD009 SPEC003/005
  implemented, SPEC004 partial (reference form only).

- fix(nsctl): write a relative `repo add --path` as an absolute path

  The manifest is read from wherever `env apply` runs and the path becomes a
  bind-mount source; `--path platform/warehouse` reached `docker run -v`
  verbatim and was refused as a volume name. Paths carrying a variable are
  left for `RepoPath` to expand.

- fix(nsctl): create resources_output before a foreign node runs

  The first live exec node failed on its first write because hmd-cli-helm,
  not the runner, had always created `meta-data/resources_output/`.

- feat(nsctl): read a repo class's deploy commands and image

  `repoclass.Manifest` gains `deploy.commands` and `deploy.image`;
  `ExecCommand` applies SPEC003's one-exec-per-phase rule.

- feat: both local seeders forward the manifest's BACON `discovery` block when
  registering a RepoClassVersion (hmd-ms-deployment NERD0013 SPEC0005).
  `bom_seeder.seed_bom` reads it via a new `_get_repo_discovery` from the same
  resolved manifest it takes dependencies from; `nsctl`'s `bom.Seeder.Seed`
  asks an optional `DiscoveryResolver` (`repoclass.Resolver.ResolveDiscovery`,
  sharing `Resolve`'s tree resolution) and warns rather than fails if the read
  errors. The key is omitted, not sent empty, when a manifest declares none.
  Until now every locally registered version had an empty discovery card in the
  Deployment GUI and nothing for its MCP `search_capabilities` to find.

- docs: close NERD013 on a live acceptance run against hmdtr1's dev

  Measured against the cloud BOM and applied to a fresh local environment.
  The closure of `ms-transform` is 24 instances of 109, against NERD012's 66;
  `private-ca` is never reached and `karpenter` is named as not followed. Two
  applies, both with the ChangeSet accepted -- the unmet-role failure the
  document exists to remove did not occur -- and on the second, three
  cloud-sourced workloads deployed at their cloud versions. Each attempt then
  stopped on a cloud-only class a required name-only role reaches (`core-acm`:
  no Route 53 zone; `base-datadog`: no `datadog-url` SSM parameter), which is
  the residue the document now records by name instead of predicting.

- feat(nsctl): `bom import --apply` deploys what it declared

  The round-trip from a cloud BOM to a running local instance was two
  commands, `bom import` then `env apply`. `--apply` runs the second from the
  first -- the same `environment.Apply` that `nsctl env apply` calls, against
  the same environment -- so NERD013's fourth acceptance criterion is one
  line. Refused with `--dry-run` and `--no-pull`; a partial import (an artifact
  that could not be fetched) is declared and deliberately not applied, keeping
  NERD012 SPEC006's non-zero exit and naming the apply to run by hand.

- fix(nsctl): report an unfollowed optional role whose target is here anyway as kept

  Found on the NERD013 acceptance run. Five of ms-transform's 22 optional roles
  point at instances the closure reached through someone else's required role
  or bound as substrate -- `ms-transform:ext-secrets` via airflow,
  `ms-transform:eks-cluster` as the substrate. The manifest kept those roles
  (it keeps every role whose target is present), but the report listed them
  under "not followed, so they were not imported", which was false. Two notes
  now: targets dropped, and roles kept because the target is here. Also fixes
  the `--exclude` refusal reading "it is ms-transform needs it for eks-alb".

- feat(nsctl): follow only the dependency roles a repo class marks required

  NERD013. `nsctl bom import` closed a selection under *every* role, because a
  BOM entry's dependencies are `{role: instance}` with no required flag --
  NERD012 SPEC005 recorded that and concluded required-only closure was not
  available. Both halves of that sentence are true and the conclusion does not
  follow: the flag lives in the repo class's own BACON manifest, which travels
  inside the artifact `import` already fetches, and which nsctl already reads
  and registers as the RepoClassVersion's dependencies. It is not merely
  available but authoritative -- the required set a local `env apply` validates
  against is the one nsctl itself registered, out of that same file.

  Roughly half of the 517 dependency roles in a full workspace are authored
  `required: "false"`, and not following one prunes whatever it reached in turn.
  Measured class-level from checkouts, the closure of `hmd-ms-transform` goes
  from 56 classes to 25, and `hmd-inf-private-ca` and `hmd-inf-karpenter` are
  among what goes.

  A class whose manifest cannot be read closes over all of its roles and the run
  says so, so the degradation is always toward the larger import: a closure that
  quietly shrank because a file was missing would drop a required role, and the
  failure would arrive at `env apply` naming the role rather than the read that
  failed. `bom show` therefore reads only the artifact cache unless `--resolve`
  is passed, which keeps `--dry-run`'s promise to fetch nothing.

  `--with <role|class>` follows an optional role anyway, and a `--with` that
  matches nothing is refused -- seed()'s rule, for its reason: a selector that
  silently contributes nothing has the shape of a successful smaller import.

- feat(nsctl): bind a required role nothing local fills, or refuse to

  NERD013 SPEC003. Required-only closure shrinks an import; it does not make
  every remaining role fillable, and a cloud-only class is usually reached by a
  role its consumer marks required. A required role whose target is not picked
  now resolves by what the role asks for: a **name-only** role binds to the core
  instance, because presence is the whole of what hmd-ms-deployment validates,
  and a **resource-typed** one is refused, naming the type, because the producer
  is validated against what it really produces.

  This generalises what `internal/bom`'s Substrate already did for two
  hard-coded roles -- `datadog-lambda` and `rds-loggroup` both point at the core
  instance -- into the rule they were instances of, read out of the manifest
  rather than from a list. No catalogue of cloud-only classes is introduced; that
  is what nsctl exists not to carry.

  Binding is a fiction: the core instance deploys no ACM. So it is never silent.
  Every bound role is reported with the class that was not deployed, and
  `--no-stub-roles` refuses instead.

  `--exclude` is relaxed to match. It refused anything the closure pulled in;
  the question is really whether the role can be filled another way once the
  target is gone, so it now refuses only a required resource-typed role, or one
  whose declarations this run could not read. That is what makes leaving out a
  cloud-only class an ordinary thing to do.

- feat(nsctl): narrow a cloud BOM import with a file instead of flags

  NERD013 SPEC004. `nsctl bom show <env> --save-selection <file>` writes what
  the closure resolved, one TOML block per instance; edit the `take` lines and
  `nsctl bom import <env> --selection <file>` takes it. A `take = true` becomes
  an explicit instance and a `take = false` becomes an exclusion, so an edited
  file needs no rules of its own -- including the refusal above, which is how a
  hand edit that would break a required role is caught here rather than at
  `env apply`.

  A file rather than an interactive picker: it is diffable, committable,
  reusable on another machine and usable from a script, and it needs no
  dependency. It is parsed strictly, because a file somebody edited by hand is
  exactly where `takes = true` happens, and a key quietly ignored there is an
  import smaller than intended.

  NERD013 is `partial`, not `implemented`. All six SPECs are built and the path
  is exercised by 14 tests against a fake deployment service and a fake
  librarian, but nothing here has been run against a cloud environment or
  applied to a local one -- so the claim that this makes an import *deployable*
  is reasoned rather than observed. The four acceptance criteria that would
  settle it are recorded as unmet.

## 2026-09-15

- fix(nsctl): bind a cloud BOM's substrate by repo class, not by instance name

  Found by running `nsctl bom show` against a real environment. hmdtr1's cloud
  `dev` runs `hmd-inf-eks-cluster` as an instance called `eks-upg` and
  `hmd-postgres-rds` as `core-rds`; every local environment deploys those same
  classes as `eks-cluster` and `environment-db`. Matching the substrate by
  instance name alone therefore recognised neither, and the dependency closure
  imported both as though they were workloads -- which locally would mean
  declaring a second EKS cluster and a second environment database inside the
  environment that already has them.

  The substrate is now matched by repo class as well, and a role pointing at the
  cloud's name for one is rewritten to this environment's name for it, so
  `ms-transform`'s `cluster` role comes across as `eks-cluster`. The run reports
  the rename rather than performing it quietly: "substrate, as eks-cluster" is
  the sort of thing a reader should be able to disagree with.

- feat(nsctl): cherry-pick a cloud environment's BOM into a local one

  NERD012 SPEC005, SPEC006 and SPEC007, which completes the document's code.
  `nsctl bom envs` lists a tenant's environments, `nsctl bom show <env>` prints
  what one is running -- every deployed instance, its concrete version, and
  whether this machine already holds that artifact -- and `nsctl bom import
  <env>` copies a selection down and declares it locally.

  A cloud environment is a known-good version set, which NERD010 already argues
  is the strongest thing to build a local environment from; until now
  reproducing one meant reading it by hand and retyping the versions.

  The selection is closed under its dependencies, which is the part worth
  explaining. `hmd-ms-deployment` fails a whole ChangeSet on a required role
  nothing fills, so importing `ms-transform` without whatever fills its roles
  would routinely produce a manifest that cannot deploy -- and the failure would
  arrive at `env apply` naming a role rather than the import that omitted it.
  Closing over *required* roles only would be better and is not available: a BOM
  entry's dependencies carry no required flag. So the closure covers every role,
  erring in the direction that costs a fetch rather than a deploy. Roles filled
  by the substrate are bound instead of imported, a target the BOM does not
  contain is named with the role that wanted it, and `--no-deps` takes the
  selection literally.

  Fetch happens before declare, always. An import must never leave behind a
  declaration whose artifact is not here, because that is a manifest that fails
  at apply for a reason the manifest does not mention; an artifact that cannot
  be fetched is reported, its instance is not declared, and the command exits
  non-zero after every other instance has been handled. Each declaration says
  `source: {type: artifact}` explicitly, so a version that came from a cloud
  librarian cannot silently resolve to whatever sits under `$HMD_REPO_HOME`.

  An empty selection is refused, and so is a selector matching nothing: `--all`
  is how you ask for a sixty-instance environment out loud, and `--instance
  ms-transfrom` contributing nothing has exactly the shape of a successful small
  import.

  `nsctl bom import` declares; `nsctl env apply` deploys. And nothing here
  writes to a cloud service -- a BOM is an input to a local manifest, never an
  output to a cloud deploy.

- feat(nsctl): read a cloud hmd-ms-deployment

  NERD012 SPEC002, SPEC003 and SPEC004. `internal/msdeploy` gains a second
  constructor, `NewCloud`, and with it the ability to talk to a tenant's own
  deployment service rather than only to the one on loopback: `DeploymentBOM`
  fetches an environment's Bill of Materials in a single request, and
  `Environments` lists what a service knows.

  A second constructor rather than a second package. The entity names, the
  filter shape, the base64 collection encoding and the error type are one
  service's protocol; the only differences between a control-plane client and a
  cloud one are an address and a credential, and splitting them would create two
  places for one fact.

  Three mechanical gaps had to close. The client sent no headers at all, and now
  sends `x-api-key` and a raw `Authorization` with no `Bearer` prefix -- which
  is not an oversight being copied but what every Python client sends and what
  the authorizer reads. `Reachable` and `ServiceVersion` build their own
  requests and were therefore unauthenticated; both now authenticate too, which
  matters most for `Reachable`, since it accepts anything below 500 and would
  have treated a 401 as proof the service was up. And `get_deployment_bom` is
  declared GET and answers with an array, where `APIOp` is POST-only and
  discards a non-object body into an empty map, so `APIOpGet` returns the body
  raw.

  `EnvironmentInstances` is untouched and stays the local path's tool. Against a
  cloud graph it would be the wrong one twice over: ten unfiltered whole-table
  searches joined on the client, and a different rule for picking an instance's
  current deployment than the server uses -- newest by `_created` here, the
  edge's `current` attribute there. The two agree most of the time, which is
  what makes the disagreement hard to notice.

  The BOM decoder's fixture is synthesised from the Python that emits the
  format, not captured from a live service. That is NERD012's first acceptance
  criterion and it is not yet met; the test says so where a reader will find it.

- feat(nsctl): name a tenant's endpoints in nsctl.toml

  NERD012 SPEC001. A `[profile.<name>]` table can now carry `customer_code`,
  `region`, `artifact_librarian_url` and `deployment_url` beside the `auth_url`
  it already had, so a profile describes a whole tenant rather than only where
  to sign in. `--profile` selects one on every `nsctl artifact` verb.

  Usually the two short keys are enough: both service hostnames are composed
  from a customer code and a region, exactly as `hmd_lib_librarian_client`,
  `hmd-cli-ns-bootstrap` and `hmd-cli-deploy` all compose them. Spelling out two
  long URLs that repeat the customer code twice is a worse way to say the same
  thing, so the explicit keys are there for a deployment that does not follow
  the convention and are expected to stay unset otherwise.

  One precedence rule now serves both services, in `internal/nsconfig`: `--url`,
  then the service's environment variable, then the profile's explicit URL, then
  composition from the profile's customer and region before the environment's,
  then a refusal that names all four. `librarian.endpoint` is gone rather than
  kept beside it, because two ladders resolving one address is how a client ends
  up talking to the wrong host.

  An environment variable still beats the file, which is the convention
  everywhere else. The case where that misleads gets a guard: if you name a
  profile and a variable overrides an address that profile also carries, the
  override wins and says so, naming both. Silently reaching production because
  of a variable you forgot was exported is the failure a tenant selector exists
  to prevent.

  Nothing an existing install reads has changed. A file containing only
  `auth_url` still parses, verbs configured entirely by environment variables
  keep working, and `--profile` on `nsctl env apply` still means NERD010's local
  profiles -- a different thing that has never shared a command with this one,
  and now cannot begin to by accident.

- refactor(nsctl): widen the login config into nsctl's config

  `internal/logincfg` becomes `internal/nsconfig`. No behaviour changes: the
  same `$HMD_HOME/.config/nsctl.toml`, the same `[profile.<name>]` tables, the
  same `HMD_LOCAL_NSCTL_CONFIG` override, the same link-time `DefaultAuthURL`.

  The rename is groundwork for NERD012, which puts a tenant's service addresses
  in the same profile that already names its issuer. A package called
  `logincfg` holding the URL of an Artifact Librarian is the kind of misnomer
  that costs the next reader ten minutes, and it is cheaper to fix before the
  keys land than after.

- feat(nsctl): choose a published version from the command line

  NERD011 SPEC005 and SPEC006, which completes the document.
  `nsctl artifact versions <repo-class>` lists what a repo class has published,
  newest first, and with `--spec` says what a BACON version specifier resolves
  to today; `nsctl artifact pull` takes the version optionally, resolving the
  newest published one -- or the newest satisfying a specifier written in its
  place -- and printing which it chose, because a command that quietly picked
  one has told you nothing you could check.

  Those are the consumers, and NERD011 named a different one: `nsctl lock`
  tier 3, resolving a manifest's ranges. That is the wrong home and the SPEC is
  amended to say so. `lock` snapshots an environment that has been stood up by
  hand, and a deployed instance already carries a concrete version -- so in the
  workflow the command exists to serve there is no range left to resolve by the
  time anybody runs it. Its existing refusal, naming `--pin` and `--from-env`,
  is honest advice rather than a missing feature. A version is *chosen* one step
  earlier, while adding repo classes, and one step later when bumping one, which
  is exactly what these two verbs answer. NERD010 SPEC003 stays partial, with
  tier 3 recorded as deliberately not built.

  Three failures are kept apart because their fixes are different: a repo class
  the librarian has never heard of, one present with no artifacts of the type
  asked for, and one present whose published versions none satisfy -- the last
  naming how many exist and which is newest. An ordered specifier is refused
  before anything is queried.

  `--offline` answers from the cache alone and says how old it is, refusing when
  nothing has ever asked. That is what makes the recorded query time load
  bearing: "this was the newest version when somebody last asked" is a different
  claim from "this is the newest version", and it is never a fresh query in
  disguise.

- feat(nsctl): enumerate what a repo class has published

  NERD011 SPEC001 and SPEC004. `internal/librarian` grows a search surface and
  `internal/versions` uses it to answer "what versions of this repo class exist",
  caching the answer under `$HMD_HOME/.cache/neuronsphere/versions`.

  Three requests, because the obvious routes do not work and most of them fail
  quietly. `/apiop/search` with `like`, `contains`, `startswith` or
  `begins_with` on a content path answers 500; any *filtered* CRUD search
  answers 500 while the unfiltered body succeeds; listing every content item
  answers 502. Worst of the four is
  `hmd_lang_artifact_librarian.repo_has_repo_version`, which is the
  relationship an implementer reaches for first and is declared in the language
  pack and never populated -- it returns an empty list rather than an error, so
  code built on it looks like a librarian with nothing published. All four are
  recorded in the package doc so they are not rediscovered.

  What does work: an unfiltered repo search (217 rows in 1.3 s, memoised on the
  client), the `content_item_has_repo` edges of the matching repo, and
  `get_by_nid` over those ids. The version and the item type are read out of the
  content path rather than fetched as entities, by parsing with the same
  `librarian.Spec` that generates those paths and verifying the parse by
  re-rendering it -- so there is one statement of the grammar rather than a
  generating half and a parsing half that can drift. A path that does not
  round-trip is skipped, because a librarian holds content items that are not
  build artifacts.

  The `get_by_nid` batch is chunked at a hundred ids, and the chunking lives in
  the client rather than in its caller. 392 ids in one request took 29.3 s,
  close enough to a Lambda timeout that a repo class with a longer history would
  simply fail, and a second caller should not have to rediscover that as a
  timeout. Progress is reported per chunk for the same reason: half a minute of
  silence reads as a hang.

  The cache is cache in NERD005 SPEC003's sense -- wholly reconstructible, so
  deleting it costs a re-query and nothing else, and purging an environment
  never reaches it. It records when the librarian was asked, which is what lets
  a reader tell "this is the newest version" from "this was the newest version
  when somebody last asked", and it is never refreshed implicitly: the command
  the user typed queries, everything else reads what is there.

- feat(nsctl): evaluate a version specifier the way the control plane does

  NERD011 SPEC002 and SPEC003. `internal/versionspec` answers which published
  versions a BACON `version_spec` admits and which of them is the highest --
  the operation nothing in the platform could do, which is why every range in
  every manifest is resolved today by a human reading git tags.

  It is a port of `hmd_ms_deployment.version.VersionSpecifier` made from
  *observed behaviour* rather than from reading the source, because
  hmd-ms-deployment re-validates every version it is handed: a drift between
  the two evaluators is a version nsctl picks and the control plane then
  rejects, and that is the highest risk in the document. So the contract is a
  golden table of 552 specifier/version pairs generated by running the Python
  itself, and the prose is commentary -- when they disagree, the table is
  regenerated rather than edited.

  The ordered operators are refused rather than implemented. `>`, `>=`, `<` and
  `<=` are broken upstream twice over: the evaluator asserts exactly three
  components, so all five real-world uses (`>=0.3`, `>=0.1`) raise on
  construction, and it compares every component instead of exiting at the first
  difference, so `< 0.3.0` rejects 0.2.9. Implementing them correctly would make
  nsctl pick versions the control plane refuses; implementing them as observed
  would copy a defect into a second codebase and make it a contract. The refusal
  names the remedy -- `~= 0.3` or `== 0.3.*` -- because a message that only says
  "not supported" leaves its reader nowhere.

  The ordering is defined here rather than borrowed, and that is deliberate:
  `sort_versions` is the only thing in the platform that sorts versions and it
  is not a version ordering. It applies three stable sorts in the order major,
  minor, patch, and a stable sort makes the last key primary, so it comes out
  patch-first and major-last. Its callers only produce a `latest_version` for
  display, so today that is a wrong "latest" in a GUI -- but the same algorithm
  reused for selection would be a wrong deploy. A test asserts this package does
  not reproduce its order, so a later simplification onto it fails.

  The package imports five standard-library packages and nothing else, enforced
  by a test, so callers can use the satisfaction test without wondering whether
  they just made a network call.

- fix(nsctl): do not advise generating a lock that already exists

  Found in the NERD010 acceptance run. A lock declaring a schema version this
  binary does not know was refused correctly -- "lock schema version 7 is not
  supported; this nsctl understands 1" -- and then told the reader to "generate
  one with `nsctl lock`", which is advice for a repository that has no lock at
  all. Following it would overwrite a file whose only problem is that nsctl is
  too old for it. The remedy is now appended only for the absent case.

- feat(nsctl): reconcile an environment from a repository

  NERD010 SPEC006 and SPEC008. `nsctl env apply --from-repo <path>` re-reads a
  repository's lock and reconciles an environment that already exists, then
  deploys it through the apply path that already existed. No new deployment
  mechanism appears anywhere in NERD010.

  The asymmetry with `env add` is the substance. This one is **offline by
  default**: an artifact nothing has fetched fails with NERD005 SPEC007's
  message, naming `nsctl artifact pull`, because an apply that reaches the
  internet unasked is an apply that behaves differently on an aeroplane. `--pull`
  fetches first, as a declared step that prints what it does.

  With no `--profile` it uses the profiles the environment recorded, and with no
  `--name` the instance names it already bound -- so a re-apply never duplicates
  an instance you renamed.

  Two kinds of instance stop being asked for and they are not the same request,
  which SPEC006 is amended to say. A renamed one is always undeclared: naming an
  instance something else is a request about that instance, and leaving both
  names would deploy the same thing twice into one environment. One dropped by
  deactivating a profile stays declared unless `--prune`, because narrowing the
  profile set says nothing about those instances. Neither is ever torn down, and
  `--prune` undeclares rather than destroys -- the promise `nsctl repo remove`
  already makes. An instance nobody declared through `--from-repo` is untouched
  either way.

  The acceptance test caught a bug the proposal did not predict, and it was this
  SPEC's own failure mode. "Use what the environment recorded" cannot be decided
  by looking at the recorded profile list, because a lean environment records an
  empty one and that is indistinguishable from an environment never created from
  a repository -- so a bare apply silently re-expanded a lean environment to the
  repository's `default_profiles`. The decision now keys off the `bindings` map,
  which a `--from-repo` run always writes.

- feat(nsctl): create an environment from a repository

  NERD010 SPEC005. `nsctl env add <name> --from-repo <path>` reads a
  repository's `local` section and its checked-in `neuronsphere.lock`, declares
  every activated entry as an artifact source at its pinned version, and
  declares the repository itself from its working tree -- which is what makes it
  the thing under test. `env add` previously wrote only the registry.

  It is the one command that fetches without being asked, and that is justified
  because this *is* first start: there is no environment yet, so there is no
  offline expectation to violate. `--no-pull` suppresses it. The lock is never
  resolved around: absent, it fails naming `nsctl lock`, because the whole point
  is that a fresh clone gets the same answer as the machine that wrote it.

  The activated profiles are recorded in the environment manifest. Without that
  a later bare `nsctl env apply` falls back to the repository's
  `default_profiles` and reconciles away instances the user explicitly asked for
  -- a delta-apply that is correct according to a file nobody re-read.

  What an instance is *called* turned out to be load bearing, and SPEC005 is
  amended with it. Two engineers may deploy one repo class at one locked version
  under different local instance names and must still resolve the same roles, so
  the name is defaulted but overridable and the lock carries none. The order is:
  a binding the environment manifest already records, then `--name
  <role-or-declared-name>=<instance>`, then the declaration's own
  `instance_name`, then the dependency role. Never the repo class -- one class
  routinely fills several roles. A new `bindings` map in the manifest is what
  makes a rename survive a re-apply instead of duplicating the instance.

  A want whose name is reserved for the substrate -- `base-vpc`, `eks-cluster`,
  `environment-db`, `local-neuronsphere` -- is not declared but is still bound
  to, printed as "provided by the substrate". Real manifests hit this at once:
  hmd-ms-transform and hmd-ms-artifact-lib both name base-vpc.

  The declaration and lock are read before the registry is written, so a broken
  manifest costs nothing rather than leaving an account and a port slot
  allocated to an environment that was never created.

- feat(nsctl): pin a repo's local environment in `neuronsphere.lock`

  NERD010 SPEC002, SPEC003 (tiers one and two), SPEC004 and SPEC007. `nsctl
  lock` reads a repository's `deploy.dependencies` and its `local` section and
  writes a generated, checked-in `neuronsphere.lock` at the repository root, so
  a fresh clone stands up the same platform as the machine that wrote it.

  The lock is keyed by **repo class** and its `satisfies` names dependency
  **roles**, never an instance. That is load bearing rather than an omission:
  two engineers may deploy the same class at the same locked version under
  different local instance names, and both must still resolve the same roles
  from one checked-in lock. A role is portable across machines; an instance name
  is not. It also makes `satisfies` a list, because one class routinely fills
  several roles -- `hmd-inf-credentials` fills three in `hmd-inf-trino`.

  Every profile's entries are pinned, not only the active ones. Activation is a
  read-time filter, and a lock covering only the profile in use when it was
  generated would force a re-resolve -- a network trip, and therefore a
  different answer -- the first time anybody switched.

  `content_path` is generated by `librarian.Spec.ContentPath()` and never by a
  second format string: BACON's `pre_build_artifacts` grammar and the
  librarian's content paths are one vocabulary, and this repository has already
  paid once for letting a private copy of a format exist. A lock declaring a
  schema version nsctl does not know is refused whole and never partially read,
  because a silently-ignored `resolved` entry is a missing instance nobody goes
  looking for.

  Resolving a specifier is the hard part, so the tiers run by how certain they
  are: `--from-env` pins what an environment is actually running, which is the
  only evidence a set of versions works together; `--pin` and any already-exact
  `version_spec` come next; and a range neither settles is reported with both
  remedies rather than guessed at. Resolving ranges against a librarian is a
  separate mechanism and stays NERD011's, which is why SPEC003 is partial.

  `lock --check` writes nothing and contacts nothing -- it is the hook a
  repository puts in pre-commit and CI, so it has to run on a dirty tree, on an
  aeroplane, and with no librarian credential. A declared want with no entry
  fails; an entry no longer declared is a warning, because a developer
  mid-refactor should not be blocked by it.

  Two things fell out of writing it. `--pin` beats `--from-env`, because an
  explicit answer should not lose to an ambient one. And an optional dependency
  the `local` section does not gate is never locked, since it is never deployed
  locally -- so a range on one does not block `nsctl lock`, and `--pin` naming
  one says "gate it" rather than "this repository does not declare it", which
  would send the reader to fix a spelling that is already right.

- feat(nsctl): read a repository's local section

  NERD010 SPEC001. A repository can now declare, in a file it checks in, the
  local environment it needs in order to be tested: profile-gated companions
  that are not dependencies, and profile-gating of the optional dependencies it
  already declares. `internal/localspec` reads it.

  Two things a BACON manifest cannot express are the whole reason the section
  exists. A service whose endpoints are called by a Transform which reads from a
  Librarian depends on neither -- the arrows point the other way -- yet locally
  they are exactly what is needed to see whether it works, and BACON describes a
  deployment graph rather than a test fixture. And `required: "false"` is one
  boolean evaluated the same way every time, where a developer needs lean while
  iterating and wide before a pull request, from one checkout, without editing
  anything.

  The semantics are `docker compose`'s and not a second grouping concept: an
  entry with a `profiles` key starts only when one of them is activated, an
  entry without one always starts, and **lean therefore needs no declaration at
  all** -- it is what activating nothing gives you, so no author has to remember
  to define it. A required dependency named under `local.dependencies` is
  refused rather than gated: ms-deployment fails the whole ChangeSet on an unmet
  required role and a SKIPPED status does not mock one, so gating one would
  produce a failure three layers from its cause.

  SPEC001 says the section is read through NERD009 SPEC006's manifest store.
  That store does not exist -- NERD009 is entirely proposed, `go-toml/v2` is
  linked in but `internal/logincfg` is its only importer, and nothing in the
  module reads a TOML manifest or a repo-root `neuronsphere.toml`. So this reads
  `meta-data/manifest.json` with `encoding/json`, the way `repoclass.Manifest`
  and `librarian.PreBuildArtifacts` already do, and the SPEC carries a dated
  amendment saying so. Implementing the store on the way past would have changed
  what forty Python packages write and left neither change reviewable.

- refactor(nsctl): seed a resolver from a manifest in one place

  Three call sites built a `repoclass.Resolver` out of a manifest's
  declarations: `env apply`, the control-plane extension resolver, and now
  `nsctl repo list`. The load-bearing line in all three is the same and is
  invisible when it is wrong -- an artifact instance goes into `Artifacts`,
  never into `Paths`, because an entry in `Paths` is tier two and taken without
  a stat, so folding them together reports an artifact's version with Source
  `working-tree`.

  `repoclass.Seed` is that one place, with `repoclass.Paths` (formerly
  `bom.RepoPaths`) beside it. Unifying them settled a disagreement the two
  existing sites had about which declared paths count as overrides: the
  environment path compared the resulting paths and dropped one equal to the
  `$HMD_REPO_HOME/<class>` convention, the control-plane path asked only whether
  `source.path` was set. The stricter rule is kept -- a manifest spelling the
  convention out is still naming the convention -- so a control-plane extension
  declaring it now resolves through a stat like every other checkout.

- feat(nsctl): say where each version came from in `nsctl repo list`

  NERD005 SPEC002's acceptance criterion -- "distinguishes '0.1.4 from an
  artifact' from '0.1.4 from a working tree'" -- held for the control plane and
  not for an environment. `nsctl repo list` carried two facts in one SOURCE
  column: where the *declaration* came from, beside the manifest's *unresolved*
  version, so an artifact-sourced instance and a checkout of the same version
  read identically.

  DECLARED keeps the three literals, FROM carries `repoclass.Resolution.Source`,
  and VERSION is now the resolved version. The listing builds a real resolver the
  way `env apply` does, so both read the same tiers; resolution is offline by
  construction and cannot make a listing fail, which `repo list` depends on since
  it already renders with no control plane running. A declared artifact whose
  bytes are absent reads `artifact (uncached)` and the listing names the `nsctl
  artifact pull` that fixes it.

- feat(nsctl): deploy a RepoClass from a versioned artifact

  NERD005 SPEC002. `repoclass.Resolver` gains an artifact tier, between the
  checkout a user asked for and the tree the binary carries. An instance
  declared `source: {type: artifact}` resolves from the librarian even when a
  checkout of that class is sitting in `$HMD_REPO_HOME`: the manifest asked for
  a version, a stale checkout is not that version, and substituting it produces
  a deploy that reports a version it did not use. A tree the user *explicitly*
  asked for still wins -- `source.path`, `=local`, `PREFER_LOCAL_VERSIONS` --
  because the whole purpose of `$HMD_REPO_HOME` is to run uncommitted changes,
  and `nsctl` now says so on the way past.

  Two things the code turned up that the proposal had not.

  `Resolver.Dir` must answer `""` for a declared artifact that is not cached
  rather than continuing down the tiers, and `Resolve` needs the same guard on
  its directory fallback. Falling through hands back the bundled tree or a
  checkout -- code that is not the version just reported -- silently.

  And an artifact tree reaches a deploy through `runner.Config.RepoPaths`, which
  until now meant "a checkout somebody declared", so the runner mounted it
  read-write and directly. An unpacked artifact is not that: it is keyed by
  version and shared by every environment on this `HMD_HOME`, so a deploy
  writing `meta-data/resources_output/` into it would hand the next environment
  this one's resources -- the exact hazard the bundled tier already isolates
  against. `Runner.repoPath` now asks whether a path lies under one of the cache
  roots instead of tracking the answer alongside it.

  The integration point is `internal/environment/apply.go`: `bom.RepoPaths`
  drops every artifact instance, because `manifest.Repo.RepoPath` answers `""`
  for a non-local source, so the artifact paths are merged in separately -- and
  deliberately not into `resolver.Paths`, where they would report an artifact's
  version with source `working-tree`. `checkArtifacts` runs before the plan, for
  the reason `EnsureBackendImages` does: a manifest naming three uncached
  versions should say so in seconds and name all three.

- feat(nsctl): add `nsctl artifact` -- pull, register, unpack, cache

  The four verbs that fill the control plane's Artifact Librarian, so a plugin
  is distributed by version number rather than by git remote.

  `pull` copies a published artifact from a cloud librarian into the control
  plane's *and* unpacks it, because resolution never fetches and a librarian
  holding bytes nothing has unpacked resolves to nothing. `register` stores a
  local `hmd build` output -- an explicit zip, a directory zipped on the fly, or
  the conventional path `hmd build` writes under `$HMD_BUILD_OUTPUT_DIR` -- and
  invalidates the unpacked copy first, so re-registering a version deploys the
  build that was just registered rather than the one before it. `unpack` is the
  repair path from the control plane alone: no cloud round trip, nothing
  rebuilt. `cache` copies a repo's declared `pre_build_artifacts` across, since
  the spec string BACON writes is already the librarian's own content-path
  grammar.

  `register` does not shell out to `hmd build`. The Python `push_artifact` does
  by default, and nsctl's premise is that Docker is the only host prerequisite:
  a register that needed the Python CLI installed would fail in exactly the
  environment nsctl exists to serve.

  The `--url` override layers over the injected lookup rather than exporting
  into the process, because every test in `cmd/` runs in parallel and `t.Setenv`
  panics there.

- refactor(nsctl): share the pre-build-artifacts parser with repopack

  `tools/repopack` held the only reader of `build.pre_build_artifacts`;
  `librarian.PreBuildArtifacts` is now that reader and both call it.

- feat(nsctl): upload an artifact to the control plane's librarian

  `librarian.Client.Put` is the three-leg exchange `put_file` performs:
  `/apiop/put` for presigned part URLs, a credential-free `PUT` per part, then
  `/apiop/close` with the ETags. It is the write direction NERD005 SPEC005 needs
  so a developer's own `hmd build` output becomes deployable by an
  artifact-sourced manifest with the cloud involved at no point.

  `librarian.NewLocal` deliberately does not go through `New`, which fails when
  neither an API key nor a token is configured. That is right for a cloud
  librarian and would make `register` impossible on the machine `nsctl` exists to
  serve, since the local librarian is anonymous.

  Multipart is refused loudly rather than implemented. Every artifact in scope is
  one part under the hundred-megabyte default, and the upstream client sends the
  content type *only* when there is exactly one part -- so the multipart header
  contract is unobserved and cannot be ported faithfully. More than one part
  returns a typed `ErrMultipartRequired` naming the size, because uploading the
  first part alone stores a corrupt zip that fails to unpack three commands
  later, with nothing in between to say why.

  Two Go-specific traps on the presigned leg, both discovered rather than
  designed. The request's content length is set explicitly, or Go sends a chunked
  body which a presigned PUT rejects -- the signature covers a content length.
  And no credential header travels, for the same reason `Fetch` sends none.

  Nothing is retried. Both API legs are loopback and the upload is a single
  buffered part, and a retry cannot be added later without also changing the
  body: replaying a request built over an `io.Reader` uploads zero bytes on the
  second attempt and reports success.

  SPEC010's boundary is unchanged -- this writes to the *local* librarian only.
  Publishing to a shared store remains `hmd build` with `HMD_AUTO_PUBLISH`.

- feat(nsctl): cache an unpacked artifact under `$HMD_HOME`

  `internal/artifact` unpacks a versioned librarian artifact to
  `$HMD_HOME/.cache/neuronsphere/artifacts/<class>@<version>/`, beside the trees
  `repotree` materialises out of the binary and for the same reason: a deploy
  node runs in a sibling container and bind-mounts host paths, so a zip has to
  become an ordinary directory before anything downstream can use it.

  The control flow is `repotree.Dir`'s -- stat the token-named directory, take a
  package-level mutex, stage, validate, rename, and re-stat on a lost rename race
  -- but none of its code: `repotree` reads a gzipped tar and artifacts are zips.
  Two deliberate differences. The token is the *version* rather than a content
  digest, because the version is what the manifest asked for and what `status`
  will report. And `Store` returns an error where `repotree.Dir` returns `""`:
  "the binary carries no tree" is a legitimate state to fall through on, but a
  failed unpack of a version a manifest named by number is not, and returning
  nothing for it is how a deploy comes to report a version it did not use.

  Validation requires `meta-data/manifest.json` and `meta-data/VERSION` and
  deliberately *not* `src/`, since a schema or configuration artifact
  legitimately has none. It checks the two paths directly rather than calling
  `repoclass.LoadManifest`, because `repoclass` will import this package.

  `Invalidate` is the half of re-registering that is easy to forget: overwriting
  a version in the librarian while an unpacked copy of it sits in the cache
  deploys the previous build under the new build's version.

  Nothing here fetches. The offline guarantee is a property of the package's
  imports -- `internal/manifest` and the standard library -- rather than of a
  runtime flag, so resolution cannot reach the network even by accident.

- refactor(nsctl): lift `repopack`'s zip extractor into `internal/artifact`

  `unzip` and `skipDirs` were already correct and zip-slip-safe in
  `tools/repopack`; both now live in `internal/artifact` and `repopack` calls
  them. A deletion rather than an addition, and one fewer place for the
  `resources_output` exclusion to be missed -- a copy of a deploy's own output
  carried in an artifact has every environment submitting someone else's
  resources.

- feat(nsctl): accept `source: {type: artifact}` in a manifest

  The kind was parsed and deliberately refused with "not supported yet". It is
  now validated: a version is required, because an artifact is addressed *by*
  version and there is no `meta-data/VERSION` to fall back on; `source.path` is
  rejected rather than ignored; and `source.artifact_type` defaults to `build`.

  Nothing resolves an artifact yet -- NERD005 SPEC002's resolution tier is the
  next step -- so this only widens what a manifest may say.

## 2026-09-14

- feat: sign in from the CLI with `nsctl login`

  `nsctl` could authenticate to nothing. The only login in the platform was the
  Python `hmd-cli-login`, whose shape is wrong for a CLI a new user has just
  installed: it is authorization-code plus PKCE through a Flask server on
  `localhost:8082`, signalled back by watching the token file's mtime for sixty
  seconds — so it needs a browser on the same host and works neither over SSH
  nor in a container. It also requires `HMD_NS_ISSUER` and `HMD_NS_CLIENT_ID` in
  `hmd.env` before the first command, and `hmd configure` — the command whose
  job is to write that file — never prompts for either. And it cannot refresh:
  the Okta app is provisioned without the grant and the flow never asks for
  `offline_access`, so every hour the browser dance repeats.

  `nsctl login` is the **OAuth 2.0 device authorization grant** (RFC 8628)
  against an endpoint found by OIDC discovery. It prints a code and a URL and
  polls; the browser can be on another machine or absent entirely. The endpoint
  comes from one key in `$HMD_HOME/.config/nsctl.toml`, which nsctl offers to
  write on a terminal and refuses with exit 2 without one — naming the path and
  printing the two lines to paste. Profiles are named, because an existing
  customer with two admin accounts needs more than one endpoint.

  **The user's machine carries no client secret.** It may carry a public
  `client_id`, which the device grant makes public by design. Where `auth_url`
  points is not the client's concern: a customer's own Okta or Auth0 issuer, the
  local mock, or anything else presenting the same endpoints — nsctl cannot tell
  them apart, and a release build may carry a default endpoint linked in with
  `-X ...logincfg.DefaultAuthURL=`, so an installed binary can sign in having
  read no configuration at all. No build sets it yet, so today it refuses and
  says what to write. `NERD008` SPEC009 records which population each of those
  serves.

  **The token extends `$HMD_HOME/.cache/tokens.yaml` rather than reshaping it.**
  That file is read by roughly forty Python packages through
  `hmd_cli_tools.okta_tools.get_auth_token` and reimplemented in Go by
  `internal/librarian`, all of which take `data["login"]["access_token"]`. The
  new fields — `refresh_token`, `expires_at`, `issuer`, `profile` — sit inside
  `login:`, where both readers ignore them, so authenticated artifact pulls
  start working in Go and Python alike and a golden test fails loudly if that
  ever stops being true. It is written `0600` through a temp-and-rename; the
  Python writer sets no mode, so the bearer token has been landing `0644`.

  `nsctl login` against a valid credential says so and does nothing; against an
  expired one it renews from the refresh token and never opens a browser.
  `nsctl whoami` decodes what is cached — and says it is decoded, not verified.

- feat: `nsctl authd` implements the device grant, and the refresh grant it had
  been advertising

  The mock identity provider gains `/v1/device/authorize`, a verification page,
  and the `device_code` grant, so the whole login flow is exercised by
  `make check` with no cloud, no credentials, no network and no Docker — the
  real client driving the real server in process. The verification page renders
  the *same* claims-choosing form the redirect flow uses: the groups decide
  every Rego decision, so a device path that logged in a fixed user would be
  testing something else.

  While wiring it up: the discovery document has listed `refresh_token` in
  `grant_types_supported` since this server's first version, and the token
  endpoint answered it with `unsupported_grant_type`. Nothing local held a
  refresh token, so nobody had found out. It is implemented now — issued when
  `offline_access` is requested, rotated and single use — which is what lets the
  renewal path above be proven against the mock rather than against a stub.

- docs: withdraw NERD008's admin-account broker

  SPEC007 specified a service in the customer's admin account presenting the RFC
  endpoints and holding the client registration. It is withdrawn:
  `hmd-ms-identity` stays out of the authentication path and fronts user
  management only (`hmd-ms-identity` NERD001), so `auth_url` names the
  customer's own provider.

  **No client code changed**, which is the property the indirection was drawn
  for. Two things in the original reasoning were wrong and are recorded rather
  than left to be re-derived: "the broker holds the client secret" was never the
  real justification, since the device grant is a public-client flow needing no
  secret; and the Okta gap was overstated — `HmdOktaClient.create_pkce_application`
  already registers a native application, so the real gap is the narrower one
  that `Okta.oauth_native_app` is unimplemented in `hmd-lib-cdktf-factories`
  while `Auth0` implements it. Until that is fixed an enterprise Okta customer
  provisions the application by hand.

  A broker could be revived with no client change, but would have to mint its
  own tokens rather than relay: publishing `issuer: <broker>` while returning
  the provider's tokens, which carry `iss: <provider>`, satisfies neither
  RFC 8414 nor `hmd_lib_auth.verify_token`.

- fix: `/dev/null` was mistaken for a terminal

  Found by running `nsctl login < /dev/null`, which is how a script says there
  is nobody here: it printed the prompt and *then* refused. `/dev/null` is a
  character device on every Unix, so a check for `os.ModeCharDevice` is true for
  it. The check asks the kernel for terminal attributes now
  (`golang.org/x/term`), which `/dev/null` does not have. The exit code was
  always right; the prompt in a CI log reads like a hang.

- fix: make a fresh install actually start

  Five defects, found by doing the thing the README promises: a new `HMD_HOME`
  with no repos under `HMD_REPO_HOME` and not one environment variable set.
  Each had been invisible because every machine that has ever run the platform
  already carries the state that hides it.

  **The default image registry named the wrong org.** `ghcr.io/neuronsphere`
  held only the released subset and had gone stale; `ghcr.io/hmdlabs` is where
  the images are built, tagged, and now public. Two of the three references
  Floci spawns its backends from existed under no tag at all, so an install with
  nothing set could bring up neither a graph nor a cluster, and the
  projectbuilder default — `ghcr.io/neuronsphere/hmd-img-projectbuilder:stable`,
  a `stable` tag neither org has ever had — failed the bootstrap's first deploy
  node. Every default now names hmdlabs and an explicit patch version.

  **The Engine API cannot pull.** `ImagePull` sends only the credentials in
  `PullOptions`; the daemon never reads `~/.docker/config.json`, which is the
  CLI's job. So the compose runner's pull was credential-less, and ghcr.io
  answers a credential-less request with `unauthorized` even for a public image.
  It pulls through `container.PullImage` now — the `docker pull` the rest of
  nsctl already uses, which resolves credential helpers and anonymous tokens
  exactly as the user's own would. `ImagePull` is gone from the narrowed
  `dockerAPI`, so the broken path cannot be reached back for.

  **`env start` refused on a fresh home, after bootstrapping it.** A synthesized
  registry holds no environments, so the default slug resolved to nothing — but
  only once `environment.Start` was reached, ten minutes past the control-plane
  bootstrap, with a fix the user had no way to know to run first. `env start`
  now settles which environment it is about before anything starts: it registers
  the first one on an empty registry, and refuses an unknown name immediately
  rather than after the bootstrap. The narrow guard is deliberate — creation
  only when nothing at all is registered, because the rule it bends exists to
  stop a typo becoming a second environment, and there is no typo to protect
  against when there is nothing to be confused with.

  **A stopped control plane blocked another `HMD_HOME` forever.** The ownership
  refusal counted containers that merely existed, and `control-plane stop` stops
  rather than removes — so following the message's own instruction left the
  names held and the next start refused again, with nothing further to try short
  of `docker rm`. Only running containers count now. Two homes still cannot run
  at once; they can take turns.

  **The Docker preflight was written and never called.** `Available` reports a
  missing binary or a daemon that will not answer, in the words someone chose
  for exactly that moment. Nothing called it, so the one prerequisite this CLI
  claims surfaced as whichever call happened to run first.

- fix: report a proxy that cannot start, instead of blaming Floci

  Floci publishes no host port of its own -- `:4566` is an nginx stream listener
  -- so its health check runs through `hmd_proxy`. When the proxy could not
  start, the start reported `Floci at http://localhost:4566 is not ready after
  5m0s`: five minutes spent on the wrong component, against a Floci that was
  healthy and logging normally throughout.

  `compose up` is no help here, because it reports success once a container has
  *started*; nginx exiting on a bad configuration a moment later is a restart
  loop, not a start failure. The start now checks the proxy is actually running
  before it waits on anything that goes through it, and hands back nginx's own
  words plus where the route fragments live.

  A stale fragment under `$HMD_HOME/.cache/nginx` is enough to cause it: one
  written by an older nsctl, referencing a variable this release no longer
  defines, bricked a platform exactly this way. The fragments are regenerated on
  the next `env start`, so the remedy the error names is to move them aside.

- fix: wait for CoreDNS to exist before rolling it

  k3s creates CoreDNS from an addon manifest *after* the node reports Ready, so
  on a cluster the deploy has just created, the records step can arrive first:
  `kubectl apply` succeeds, `rollout restart` fails with `deployments.apps
  "coredns" not found`, and `set -e` takes the whole step down. The records are
  already on disk by then, so the cost is the immediate reload -- but it is
  reported as a warning against a cluster that otherwise looks healthy, which is
  the least useful place to learn about it.

  The step now waits for the deployment to exist, which is the same shape as the
  node-Ready wait beside it and there for the same reason. Only a genuinely cold
  start reaches this: a cluster that already has CoreDNS never waits.

- fix: stop `env purge` destroying another HMD_HOME's data

  `nsctl env purge` with no name swept every Docker volume whose name began
  `floci-`, across the whole daemon. Floci volume names are global and carry
  nothing naming the HMD_HOME that created them, while the network, compose
  project and Floci data directory are all namespaced by one -- so a purge run
  from a throwaway HMD_HOME took a working platform's volumes with it. The
  sweep's own comment justified the breadth with "by this point every account is
  going anyway", which is true within one Floci and false across homes.

  It is not a hypothetical. Purging a scratch platform during the fresh-install
  verification destroyed the machine's real one: its control-plane database, the
  deployment graph for every environment, and both clusters' datastores.
  Authored state -- the registry, the environment manifests, `.config` -- was
  untouched, so the cost was a re-bootstrap rather than lost declarations, but
  nothing about the sweep made that the bounded outcome.

  It is scoped at two layers now, and the first attempt fixed only one of them.
  The volume sweep collects *dangling* volumes alone -- ones no container
  references -- which is what a genuine leftover looks like and what it was
  written to collect. That was necessary and, by itself, useless: the container
  sweep running just ahead of it matched every Floci-labelled container on the
  daemon, because accounts are allocated per registry and every HMD_HOME starts
  at `000000000001`, so it removed the other platform's containers and thereby
  dangled its volumes for the check to wave through. Re-running the purge with
  only that half in place took all seven volumes again.

  So the container sweep is scoped to the platform's Docker network, which is
  the one identifier that does carry the HMD_HOME hash and which a stopped
  container keeps. Each half is pinned by a test that fails when the other half
  is reverted.

- feat: refuse a start whose postgres image cannot read the data on disk

  `hmd-postgres-base` 0.3.12 is the first build to ship the PostgreSQL 14 its
  Dockerfile has specified since the bump was committed and never released, and
  the first published for arm64. That is a major version change, and Floci
  recreates an RDS container from the current image on every start while reusing
  the instance's volume — so a data directory written by 12 makes the 14 binary
  exit with `database files are incompatible with server` while Floci goes on
  calling the instance available.

  `internal/pgcheck` compares the configured image's `PG_MAJOR` against the
  `PG_VERSION` each volume holds, before compose starts Floci, and names both
  ways out: `hmd neuronsphere db upgrade`, or the tag to pin back to. It is the
  detector from the Python CLI's `pg_upgrade` and only the detector — migrating
  a volume belongs in one place, and both front ends address the same
  `HMD_HOME`. Volumes no Floci record references are ignored, because a purge is
  what orphans them and refusing over one would leave no way forward; everything
  indeterminate raises no alarm, because this gates a start.

## 2026-09-11

- feat: fetch the ten embedded repo trees instead of committing them

  `bundled-repos/` is gone: 247 files and 3.5 MB that were verbatim copies of
  other repositories, refreshed by hand. `tools/repopack` now takes every class
  from the `pre_build_artifacts` destination `hmd build` populates, then from
  `src/go/nsctl/.artifacts/<class>@<version>/`, and otherwise fetches the
  published `build` artifact from the artifact librarian itself. The new
  `internal/librarian` is that client — a port of `artifact_tools` and
  `okta_tools.get_auth_token`, which is two environment variables and the YAML
  file `hmd login` writes, not an Okta flow. CI passes
  `HMD_ARTIFACT_LIBRARIAN_API_KEY`; a developer needs nothing they have not
  already set up.

  The seven classes SPEC006 left unpinned are pinned. The `Unauthorized` that
  blocked them was a stale credential, not a missing publication — with a fresh
  login all seven resolved on the first attempt, each stamped to the version its
  latest pipeline tag names.

  The committed trees were not merely redundant. `bundled-repos/hmd-vpc` had
  drifted from its repository and was missing the NERD0004
  `add_resource_output` block, so a binary built from it deployed a VPC that
  emitted no Resource and handed `hmd-postgres-rds` no `db_subnet_group_name`.
  Every one of them also carried the MAJOR.MINOR stub in `meta-data/VERSION` —
  `0.4` for `hmd-ms-deployment`, not `0.4.854` — which is the version
  `ResolveVersion` then reported for a tree it had not used. The artifacts
  instead carry `meta-data/docker.ext_artifact.hmdentity`, naming the image the
  build actually produced, and drop `src/docker/` and `src/python/`, which no
  deploy reads. The ten archives fell from 382 KB to 222 KB.

  `goreleaser build --snapshot --clean` from a tree of tracked files alone —
  no committed trees, no `external/`, no cache — was the path the fallback
  existed to protect, and it is the path this was verified on. Without
  credentials it now fails naming the class, the version and every place looked,
  rather than at `go:embed`.

- fix: ship every `external/*/src/local/` subdirectory in the wheel

  `package_data` named `config/`, `templates/`, `scripts/` and
  `scripts/postgres/` one at a time while `services/`, `src/helm/` and
  `src/cdktf/` were recursive, so any other subdirectory an artifact shipped was
  present in an editable install and absent from the wheel. That is the bug the
  comment above `services/*` was written to memorialise, in the block directly
  below it.

- fix: pin the k3s wrapper to `hmd-img-k3s-floci:0.3.2`, the first build of that
  image its pipeline ever published

  The pin was `0.3`, a tag that exists in no registry — `ghcr.io/hmdlabs` has
  never served it, `ghcr.io/neuronsphere` serves nothing for this image at all,
  and the repo carries exactly one tag. What made a cluster come up anyway was a
  locally built `0.3` sitting in the developer's Docker cache, which is also how
  a machine could hold a wrapper from *before*
  `--disable-network-policy` and still report the pinned image as current: the
  name matched, the layers didn't. Floci recreates the k3s container from
  whatever the name resolves to, so on a warm start whose recorded node IP had
  churned off Docker's default bridge, kube-router aborted k3s startup with
  `failed to find interface with specified node ip`.

  `0.3.2` is an exact patch for the same reason the gremlin pin is: the tag has
  to name one image, not whichever build last moved a floating tag. The bump
  covers both compose files, `configured_k3s_wrapper_image`'s fallback (which
  reconstructs the compose default when Floci is down) and the expectations that
  pin them.

- feat: take `hmd-inf-neptune` from its published artifact instead of the
  committed tree

  `hmd-inf-neptune@0.3.36:build` is declared as a `pre_build_artifact`, so the
  graph's RepoClass reaches `bom_seeder` as a published version rather than as
  whatever is checked out: version resolution moves from the `local-fallback`
  tier — which warned `no bundled artifact and no declared version` on every
  `up` — to `bundled`, off the artifact the wheel now carries under
  `hmd_cli_neuronsphere/external/neptune`. `repopack` prefers the same artifact
  when a build has populated it; `bundled-repos/hmd-inf-neptune` is still what a
  release packs, since GoReleaser runs `make generate` with no credentials.

  It is the first of the eight committed trees to earn a pin, and only because
  the artifact was resolved first: `hmd build --prebuild-download-only` returned
  it with `meta-data/VERSION` stamped `0.3.36` and `src/cdktf/` plus
  `src/local/` byte-identical to the checkout. A pin that does not resolve
  breaks `hmd build` for everyone, so the other seven still travel as committed
  trees. `bundled-repos/hmd-inf-neptune` stays as the fallback `make generate`
  needs on a credential-free CI checkout.

- fix: stop the containers Floci spawned when the control plane stops

  Floci creates its RDS and Neptune backends imperatively through the host
  docker socket, so they are neither compose services nor carriers of the
  Adopted label — neither of `Stop`'s other two sweeps could see them. Left
  running, they orphan against the network the next start recreates.
  `stopFlociSpawned` finds them by the `floci` label, the same way `PurgeAll`'s
  catch-all sweep does, and warns rather than aborting when one refuses to stop:
  a container that is restarting must not leave the rest of the sweep undone.

## 2026-09-09

- fix: stop the Floci account selector and the caller's JWT sharing one header

  An environment route has to spend `Authorization` on Floci's SigV4 credential
  scope: one Floci serves every account, a 12-digit access key *is* the account,
  and for an API Gateway **v1** REST API — which is what
  `hmd-lib-cdktf-factories` deploys — that header is the only thing Floci
  resolves the owning account from. Its v2-only escape hatch, the
  `{apiId}.execute-api.{region}` virtual host that pins the account from the
  API's owner, does not apply.

  The scope used to be injected only into an *empty* `Authorization`, which made
  the two uses mutually exclusive and left no working combination for any
  environment whose account is not Floci's default: an unauthenticated call had
  the scope injected, carried it into the Lambda, and 500d when `hmd-lib-auth`
  handed it to the JWT parser; an authenticated call kept its bearer token, left
  Floci nothing to resolve the account from, and 404d with `Invalid API id
  specified`. Only the environment whose account happens to be `000000000000`
  could serve an authenticated `hmd-ms-*` request at all.

  The scope is now set unconditionally and the caller's token is relocated to
  `X-NS-Authorization`, which `hmd-lib-auth`'s `auth_token()` reads back.
  `$ns_auth`'s map and the `$ns_account` variable it needed are gone — both
  existed only to let a caller's `Authorization` win. Control-plane routes are
  untouched: the control plane *is* the default account, so an unsigned call
  already resolves there and a token arrives in `Authorization` unchanged.


- feat: write an extension's configuration back into hmd.env

  NERD004 SPEC010, the last unbuilt piece of the control-plane extension
  mechanism and the one thing NERD006 phase 1 was still doing by hand. An
  extension declares a `handback` list in `instance_configuration`, beside the
  `credentials` list and read the same way, and `control-plane start` and
  `control-plane apply` write the result into a delimited block at the end of
  `$HMD_HOME/.config/hmd.env`. From there nothing else changed: `hmd python
  login` writes `uv.toml` and `.pypirc`, `runner.Config.Extra` forwards the
  environment into every deploy node, and Docker builds pick it up as BuildKit
  secrets.

  **The rule the SPEC was missing.** "An explicit user value always wins" is
  wrong for every variable this has to write, which NERD006 SPEC008 found and
  said had to be settled before the writer existed. `PYTHON_REGISTRIES` is a
  JSON map, so replacing it deletes the JFrog index a user configured;
  `GOPROXY` is a delimited list, which is a third shape again. So "wins" is now
  three rules, declared per variable: a `scalar` the user set is not written at
  all, leaving theirs the only assignment; a `json-map` merges under its key and
  yields to a user key of the same name; a `list` appends after their entries
  and only when not already there.

  **The block goes last, and that is load bearing.** Both readers of this file
  are last-write-wins within it -- `hmdenv.Parse` by line order, python-dotenv
  by assignment order in `resolve_variables` -- so a trailing block outranks a
  line above it, which is what lets a merge carry the user's own value forward.
  It is relocated to the end on every write rather than replaced in place,
  because `hmd configure` appends new names and would otherwise silently
  outrank it.

  The writer splices raw text and never re-serialises `Parse`'s map, which
  would reformat a hand-maintained file: comments, blank lines, key order,
  `export ` prefixes and the original quoting all survive byte for byte.
  Written through a temporary sibling and renamed, preserving the file's mode
  and creating at 0600. A file whose block is unclosed or doubled is refused
  rather than guessed at, since guessing would swallow every variable below it.

  A handback carries no secret, and that is now enforced rather than asserted:
  a `${NS_SECRET_*}` reference is refused with the reason. A literal the
  manifest already carries is allowed -- the local index is anonymous on the
  loopback side by design, which is what lets `hmd.env` stay a plain file.

  Two things found while building it. `hmdenv.unquote` did not decode `\'`
  inside single quotes, so a value containing an apostrophe -- written by
  python-dotenv's own `set_key` -- read back with the backslash still in it;
  fixed to match python-dotenv's dialect exactly. And python-dotenv expands
  `$VAR` in every value whatever the quoting while `hmdenv.Parse` expands none,
  so a `$` surviving into a written value would mean two different things to
  the two readers; a contributed value that still names an unset variable is
  refused, and one that resolves to a literal `$` is too.

  `control-plane status` gains a handback section reporting each variable, its
  merge shape, its contributing instance, and whether the block carries it or
  it yielded to a value set outside. The last is the fact worth surfacing: a
  hand-set value is otherwise an invisible reason for the local index not being
  used.

  One gap is reported rather than closed. `cmd/root.go` loads `hmd.env` once at
  command start, so a handback written during a start is not visible to that
  start. Refreshing mid-run is worse -- the lookup is captured in every
  service's compose config hash -- so it says which command picks it up.

- feat: run a local PyPI registry as the first control-plane extension

  NERD006 phase 1. Two new RepoClasses, neither of them here: `hmd-img-devpi`
  wraps devpi-server (it publishes no official image), and
  `hmd-inf-local-registry` carries the
  `src/local/docker-compose.extension.yml` that NERD004 runs. `nsctl` needed
  no change at all to run them, which was the test of whether NERD004 produced
  a real extension surface rather than a second bundling mechanism.

  devpi is configured with a caching mirror of pypi.org and a hosted index
  inheriting it, so one index URL answers for an internal wheel and a public
  package alike. That settles the question everything else was contingent on:
  `uv`'s stricter PEP 503 parsing was reported to reject devpi's `+simple`
  pages, the report was closed without a reproduction, and against
  devpi-server 6.20.3 it does not reproduce. The pypiserver-plus-proxpi
  fallback is not needed.

  Served at the root of `registry.local.neuronsphere.io` rather than under a
  `/pypi/` prefix. NERD006 specified per-format path prefixes; NERD004's
  router has no such shape -- an extension declares one `url` and one
  `upstream`, and `namedVhostServer` emits a whole server block with a single
  `location /`. Adding route lists to `cpext` on behalf of one extension is
  the coupling NERD004 was built to avoid, so later formats take sibling
  hostnames instead. It also removes rather than solves the
  `--outside-url`-rewrites-absolute-links problem.

  Storage is `$HMD_HOME/registry/pypi`, which falls out of naming the instance
  `registry`: NERD004 SPEC008 puts an extension's durable state at
  `$HMD_HOME/<instance-name>/`. A hosted wheel exists nowhere else, so `env
  purge` leaves it alone.

  Wired into the toolchain by hand at first. NERD004 SPEC010's `hmd.env`
  handback now does it instead -- see the entry above, which also settles the
  rule the SPEC was missing.

- feat: extend the control plane with RepoClasses from `$HMD_HOME/.config/control-plane.yaml`

  NERD004's mechanism, built the way the doc's largest open question is now
  answered: an extension is a compose file the RepoClass carries at
  `src/local/docker-compose.extension.yml`, whose services join the control
  plane's own compose project. Not a BOM entry, not a ChangeSet, and never
  through `hmd-ms-deployment` -- which is what lets an extension be up *before*
  the deployment graph exists. That ordering is the whole point for the first
  consumer: a package registry has to serve `hmd build`, which runs long before
  anything has been deployed.

  `instance_configuration` reaches the file as `${NS_CONFIG_*}` variables and
  as compose profiles. There is no generator script; the withdrawn Python
  plugin model had one, and everything its implementations actually did --
  injecting an env var, switching a component off -- turns out to be
  interpolation and a profile. The manifest's own `NS_*` names resolve ahead of
  the process environment, so a stray export cannot silently beat a checked-in
  manifest.

  Four rules, three of which the existing code already enforced: no
  `container_name` (so the container is named from the project, which carries
  the `HMD_HOME` digest), no published ports (`CheckExclusivePublisher`), no
  named volumes (`parseVolumes`), and service keys prefixed by instance name.

- feat: `nsctl control-plane apply` and `control-plane repo add|remove|list`

  `start` applies too -- an extension is up whenever the control plane is --
  and `apply` is the fast path that converges extensions without the Floci
  health wait, the bootstrap or the route rewrite. Editing the manifest and
  running `apply` is the same operation as using the verbs. `control-plane
  status` gains an extensions section carrying each resolved version *and its
  source*, because "0.1.4 from a working tree" and "0.1.4 from a bundled tree"
  are different facts.

- fix: a failed control-plane extension no longer takes the control plane with it

  `compose.Runner.Up` iterates services in sorted key order and returns on the
  first error, which is right for the five bundled services and wrong the
  moment a sixth arrives that nobody vouched for: one unpullable extension
  image would have aborted `hmd_proxy`, and which containers survived would
  have depended on where the extension's name sorted. Extensions run through a
  new `UpEach` that attempts every service and collects failures; resolution
  failures are isolated the same way, one stage earlier. A control-plane
  manifest whose declared checkout is missing no longer fails validation, which
  would have taken every *other* extension down with it. `start` warns and
  succeeds -- `env start` returns its error, so an extension able to fail a
  start could fail every environment start on the machine -- while `apply`
  exits 4 so a script notices.

- feat: resolve control-plane extension credentials from the host keychain, in Go

  NERD004 SPEC009. `internal/keyring` is a port of `hmd-cli-tools`'
  `registry_tools.resolve_index_password` and `scrub_secrets`, rather than a
  call into it: calling it would mean a Python interpreter and an installed
  hmd-cli-tools on the host, against a binary whose whole premise is that its
  only prerequisite is Docker. It shells out to the platform's own client --
  `security` on macOS, `secret-tool` on Linux -- and takes no new module
  dependency.

  The SPEC's own objection to a second copy is answered rather than dismissed.
  The candidate order is pinned by a test quoting the Python it came from, and
  the port was checked against the real thing: a throwaway keychain item that
  both readers resolved to the same value. The Python's second probe leg is
  implemented per platform, which is faithfulness and not an omission --
  keyring 25.6.0's macOS backend does not override `get_credential`, so that
  leg returns None without touching the keychain there, and implementing it
  would make nsctl find credentials hmd-cli-tools cannot.

- feat: deliver a resolved credential into a container tmpfs, never to disk

  A `credentials:` block names where a credential lives and never holds one; a
  literal `password:` is refused rather than honoured, because a manifest is a
  file a developer may commit. The value is written to `/run/ns-secrets/<name>`
  in each of the extension's services that declares the tmpfs, mode 0600, on
  every apply -- a tmpfs is empty again after any restart, and a writer that
  ran only on create would leave the service anonymous in a way that reads as a
  missing package.

  Reporting says which references resolved, which did not, and *where each was
  looked for*: without the last part an unresolved credential surfaces as a 401
  from a cache, indistinguishable from an empty index.

- fix: do not deliver secrets with `docker cp` -- it writes them to disk

  `CopyToContainer` resolves its destination in the container rootfs as the
  daemon sees it, where a tmpfs mounted *inside* the container does not exist,
  so the write lands in the writable layer underneath the mount: invisible to
  the container, and on disk. `docker diff` reported `A
  /run/ns-secrets/upstream` on the first real run. Delivery now goes through an
  exec in the container's own mount namespace, with the value on standard input
  so it appears in no process listing. The acceptance gate is `docker diff` and
  a recursive search of `$HMD_HOME`, not a reading of the code.

  `compose` gains `tmpfs:` parsing, which is part of the hashed payload --
  hence `labelSchema` 3, so every existing container recreates once and a
  declared tmpfs actually takes effect.

- fix: `control-plane stop` and the purge sweep now take extensions with them

  By compose label rather than by re-resolving the manifest: a container whose
  repo class has since been deleted, or whose declaration has been removed,
  still has to stop, and it is exactly the one a name-driven sweep would miss
  and leave running against a network that is about to go away.

## 2026-09-08

- test: cover `env add`, `env delete` and the slug rules, and fail when a
  ConfigOverride field is added without being wired

  Two parity cases, and the point of them is that they were **run**. The first
  run failed: `nsctl env delete` requires `--yes`, refusing with exit 2 and
  naming the account and port slot it would free, so the delete was refused --
  and the teardown omitted the flag too and left the scratch environment
  registered. The right default for a verb that frees a slot another
  environment will later be given, and invisible to anyone who only reads the
  code. The case now asserts both halves, the refusal and the deletion.

  They are the cheapest parity claim in the suite. `nsctl env add` says of
  itself that it "only writes the registry", so both directions cost a registry
  write and no provisioning -- which makes them worth having rather than merely
  cheap, because the registry is SPEC003's whole subject and a slug one front
  end registers and the other cannot see is the failure SPEC003 exists to
  prevent. Nothing had asked.

  A scratch slug throughout, never the suite's `${ENV}`: a registry test able to
  unregister the environment the rest of the suite starts and stops is one that
  can take the suite with it, which is how its first complete run failed. The
  malformed-slug fixture carries an uppercase character and an underscore and
  deliberately no leading hyphen -- argparse would take one for an option and
  fail before the slug was validated at all, which is a nonzero exit for the
  wrong reason.

  `internal/nsrunner`'s new test is reflective over `ConfigOverride` on purpose.
  A test listing the fields by hand passes forever after someone adds the
  fifteenth, which is the entire failure mode: the CLI pins a value, the runner
  drops it, and the deploy runs against the wrong network or account with
  nothing to say so. Mutation-checked -- removing one `set()` fails it.

- fix: say what a differently-named environment now needs, rather than that it cannot work

  `hmd-lib-cdktf` gained `is_local_environment()` on 2026-09-08, so an
  environment not named `local` deploys -- verified by bringing up `dev2` and
  `dev3` and watching both substrates come up.

  The warning stays, reworded. The fix lives in a library that reaches a deploy
  only through the projectbuilder image, so until one ships carrying it a
  differently-named environment still fails exactly as before, and a removed
  warning would leave that unexplained. It now names what is needed instead of
  asserting the case is impossible.

  NERD0002 records the rest: item 17 and SPEC014's environment-name gap are
  closed, and the two-environment ext-secrets check that was blocked on them has
  been run -- `dev2` and `dev3` simultaneously, each operator on its own Floci
  account, checked both in the pod's environment and in the rendered
  ClusterSecretStore Secret, which is the one a `secretRef` store actually signs
  with. SPEC012's status field also said `implemented` while its own prose and
  the status table said it covers five verbs and no more; it now says
  `partly implemented`.

- feat: Serve the mock identity provider from the control plane

  `nsctl authd serve` runs as a fifth control-plane container, gated by
  `HMD_LOCAL_NEURONSPHERE_ENABLE_AUTH=true` exactly as the DAG runner is gated
  by its own flag. Off by default, and the default is load-bearing: turning it
  on is what makes an application demand a login and a service reject an
  unauthenticated call, so a default-on switch would break every local script,
  curl and Robot suite that carries no token today.

  It is the **same image** as the runner. The Dockerfile's ENTRYPOINT is
  `/nsctl` and its CMD is only a default, so a `command:` selects the other
  mode; nothing extra is built or published, and `EnsureRunnerImage` now runs
  for either service.

  **The issuer must be one string from three networks**, and that shapes the
  rest. It is the base URL every consumer appends to -- `{issuer}/v1/keys`,
  `{issuer}/v1/token` -- and it is stamped into every token's `iss`, so a
  consumer that fetched keys under one name and reads `iss` as another rejects
  every token, failing as a policy denial nowhere near the cause. So one
  hostname is made to resolve to `hmd_proxy` three ways: a Host-routed nginx
  vhost for the browser, a Docker network alias on `hmd_proxy` for Floci's
  Lambda containers, and a CoreDNS record for cluster pods. All three verified
  returning the same issuer.

  The vhost defers its upstream through a resolver variable rather than naming
  the container literally. nginx resolves a literal `proxy_pass` host once, at
  config load, so an absent container would fail `nginx -t` and have the whole
  reload rejected -- taking every other route down with it. An optional service
  that is off must cost its own 502 and nothing else.

  The `okta` secret is written to Floci on every start rather than at bootstrap,
  for the reason `syncRunnerSelection` runs on every start: the bootstrap
  happens once, and enabling the provider afterwards has to take effect without
  purging the control plane to get a second one.

- feat: A local mock identity provider, so Rego policies can be tested

  Nothing on the local platform has a token. Every app runs `AUTH_TYPE =
  AUTH_DB`, every microservice runs unauthenticated, and the consequence is that
  the Rego policies every service authorizes against are only ever exercised in
  the cloud. `nsctl authd` is the token source that makes them testable here.

  **Okta-shaped, because the consumers are.** Superset and Airflow build their
  endpoints as `OKTA_BASE_URL + "v1/token"`, and `hmd-lib-auth` hands an issuer
  to `AccessTokenVerifier`, which fetches `{issuer}/v1/keys`. None of that is
  configurable, so the paths are Okta's: `/oauth2/{ns,services}/v1/...`, with
  both discovery documents served because Superset and Airflow ask for
  `oauth-authorization-server` while Trino asks for `openid-configuration`.

  **Two authorization servers, not one.** `lambda_helper._get_issuer_and_audience`
  picks `services_issuer` with `api://neuronsphere-services` for a service
  account and `ns_issuer` with `api://neuronsphere` otherwise, and every Rego
  bundle's `token_is_valid` admits exactly those two. One server issuing both
  audiences would have made the distinction untestable, which is most of what
  there is to test.

  **No client registration.** A client's secret is its id — the convention local
  Postgres users already follow — so nothing has to call an admin API before it
  can authenticate.

  Signing is hand-rolled against `crypto/rsa` rather than adding a JWT
  dependency: only the minting direction is needed, and it is three standard
  library calls. Nothing here verifies. The key is persisted under `$HMD_HOME`
  because a container restart that rotated it would invalidate every live
  session and look like an authentication bug.

  Verified end to end before landing, with the platform's own pieces rather than
  stand-ins: PyJWT resolves the published JWKS and verifies a minted token's
  signature, issuer and audience; and the real `system/shared.rego` from
  hmd-inf-openpolicyagent, running in the OPA version and v0 mode the chart
  pins, allows `token_is_valid` for the right audience and leaves it undefined
  for the wrong one. That is the capability this exists for.

## 2026-09-07

- test: Two more things the parity suite found once it could run

  With the first three defects fixed the suite got further and found two more.

  **`hmd neuronsphere up` cannot finish inside any timeout tried.** Raising 20
  minutes to 45 did not help; the second run was SIGKILLed at 45 with pods still
  coming up. The two start verbs are not the same size: `nsctl env start` brings
  up the substrate and stops, because every workload above it is a RepoClass a
  user adds, while `hmd neuronsphere up` seeds its own plugin-derived BOM and
  deploys airflow, argo, the transform broker, cert-manager, trino and superset.
  Three and a half minutes against over forty-five.

  That test is opt-in now, behind `-v DEEP:True`, with the measurement in its own
  documentation. **It remains unproven, and that is the honest position** -- the
  case it covers is the one every existing user is in, and a suite that has never
  completed it should not be described as covering it.

  **`nsctl env purge` and `hmd neuronsphere down --purge --env` are not the same
  operation.** nsctl destroys the environment *and* unregisters it -- SPEC001
  made purge "the teardown `env delete` refuses to be". The Python destroys the
  state and keeps the registration, so another `up` brings it back. Both are
  defensible and they are not interchangeable; the test demanded a symmetry
  nobody had claimed. It checks the real contract now: the resources are gone
  from Docker, and both front ends read the same registry.

  The suite completes green: six passed, one skipped by design, exit 0.

- test: Fix the three things the parity suite's first complete run found in itself

  The suite had never finished a run. Finishing one produced four failures, and
  three were the suite's own.

  **A test asserted the opposite of its documentation.** "Both Front Ends Report
  The Same Environment Status" opens "Not a string comparison -- the two render
  differently on purpose", then asserted the environment's name appears in both
  outputs. The Python's extend-mode status renders `Mode: extend` and the
  registered HMDMS services, and never prints the environment name. nsctl's
  rendering is still checked for the name; the Python is checked for its verdict.

  **A purge assertion matched prose.** `Registry Should Not List` used a
  substring search, and the Python's empty-state message is "No **local**
  environments yet" -- so the default environment reads as still listed exactly
  when the listing is empty. Line-anchored now.

  **That failure took the next test with it.** The rebuild lived in the test
  body, so a mid-test failure skipped it and left the environment purged; the
  next test's `down --purge --env local` then correctly refused an environment
  that no longer existed. It is a `[Teardown]` now, so a destructive test
  restores the platform whether it passes or not.

  The fourth was not the suite's fault: Robot SIGKILLed `hmd neuronsphere up` at
  twenty minutes and reported rc -9, which reads as a parity failure and is a
  stopwatch. The timeout is 45 minutes and is a named variable rather than a
  literal repeated in two keywords.

- test: Cover `env purge` in the parity suite, in both directions

  SPEC012 stayed `partly implemented` and the core it covered was four verbs.
  `env purge` joins them: it is the newest verb, the one where a leftover is the
  entire failure mode, and the one whose first real run left seven containers,
  three volumes and the platform network behind.

  Both directions, because they are different claims -- nsctl purges and the
  Python must not list it; the Python purges with `down --purge --env` and nsctl
  must not, which is the case every existing user is actually in. The purge
  tests also ask Docker directly for containers, volumes and networks: what a
  purge leaves behind is not something either front end is in a position to
  report on.

  Each direction destroys the environment and rebuilds it, so the suite now
  costs two cold starts more than it did. That is recorded in the suite's own
  documentation and in the Makefile rather than discovered by whoever next
  wonders why it takes so long -- and the rebuild is itself the assertion, since
  a purge that left something behind surfaces as a bootstrap failure rather than
  silently.

- fix: Restart the graph onto the address aliasing gave it

  The sixth defect a cold start found, and the one that looked least like a
  defect: `docker ps` reported the control-plane graph healthy, its own log
  showed a working Gremlin server, and every client got connection refused. The
  artifact librarian answered 500 to every request.

  `EnsureNetworkAlias` attaches `global-graph` by disconnecting and reconnecting
  the container, because Docker will not add an alias to an existing endpoint.
  A reconnect assigns a **new IP**. The Gremlin server in
  `hmd-img-gremlin-server` binds the container's specific address rather than
  `0.0.0.0`, so it went on listening on `172.18.0.8` while `global-graph`
  resolved to `172.18.0.7` -- and `172.18.0.8` had since been handed to a
  passing Lambda container.

  Invisible on a warm platform, like the five before it: the alias is already
  attached, nothing reconnects, and the address the server bound at its last
  start is still its own. The function's comment said the disconnect "is safe
  because it is skipped entirely when the alias is already present" -- right
  about churn, and silent about the one start where it is not skipped.

  `EnsureNetworkAlias` reports whether it reconnected, and the graph is
  restarted when it did. The database is deliberately left alone: Postgres
  listens on `0.0.0.0`, so the address it was given is not the address it serves
  on, and restarting it mid-bootstrap would buy nothing.

- fix: Say why the inverted submit path times out, now that it has been run

  Phase 4's *Not done* named `call_deploy_change_set_deployment` -- "an async
  self-invoking Lambda whose behaviour under Floci is unverified" -- as the
  expected obstacle, and ada33cc made it reachable without reaching it. It has
  now been run.

  It is reached, and it fails, for a reason nobody had guessed.
  `apply_changeset` invokes the Lambda with a hand-built event whose headers are
  `{"Authorization": auth_token}`; locally there is no auth, so `auth_token` is
  `None`, and Mangum's API Gateway handler does

      [[k.encode(), v.encode()] for k, v in headers.items()]

  giving `AttributeError: 'NoneType' object has no attribute 'encode'`. The
  deploying invocation dies before it submits anything. Nothing surfaces:
  `apply_changeset` has already returned 200 and the self-invocation is an
  `Event` call, so the CLI waits out its full two minutes for a workflow that
  was never going to exist. A cloud caller passes a real token, so this is
  local-only, and the fix -- omitting the header when there is no token -- is in
  hmd-ms-deployment.

  What nsctl can fix is the message, which blamed the one thing nsctl
  guarantees: "the control plane may not be configured with
  HMD_WORKFLOW_RUNNER_URL". `syncRunnerSelection` reconciles that variable onto
  the deployed Lambda on every start, and this code path cannot be reached
  unless the runner is already answering. `NoWorkflowError` now names the real
  cause, the string to grep the Lambda log for, and what to do meanwhile.

- feat: Derive a librarian's BUCKET_NAME the way the cloud derives it

  SPEC014 recorded this as a gap only another repository could close, named the
  exact edit -- a literal `BUCKET_NAME` under `hmd-ms-artifact-lib`'s
  `deploy.default_configuration` -- and said plainly that "nothing in this one
  can close it". Both halves were wrong.

  In the cloud the name is never declared. `librarian_base.get_full_bucket_name`
  *derives* it from the librarian's own required `lib-repo` dependency on
  hmd-inf-s3bucket:

      f"{lib-repo.instance_name}-{lib-repo.repo_name}-{lib-repo.deployment_id}-"
      f"{environment}-{hmd_region}-{customer_code}"

  So a literal in `default_configuration` would have pinned a name the cloud
  computes, and drifted from the bucket hmd-inf-s3bucket actually creates --
  whose own local stack names it `self.base_name.replace("_", "-")`, which is
  `make_standard_name` of those same six parts. The declaration SPEC014 asked
  for would have been a second source of truth for a value that already has one.

  `floci.LibrarianBucketName` reads the dependency out of the manifest and
  derives the name through `tools.ResourceIdentifier`, which is
  `make_standard_name` -- so it matches the repo that *creates* the bucket. The
  two formulas agree below 64 characters and diverge above it, where
  make_standard_name shortens and get_full_bucket_name does not; matching the
  producer is the side worth being on. The bootstrap then creates the bucket
  with the `EnsureBucket` it already uses for the tfstate bucket, because
  ms-deployment is the last node of that bootstrap and a BOM entry is not yet
  possible.

  Where the instance name comes from is the one invented part, and it invents as
  little as it can: a declared `instance_name` wins, and failing that the
  dependency's own key. A librarian declaring no bucket dependency at all still
  gets the warning and no guess -- which is the thing SPEC014 was right about.

  Worth recording alongside it: the Python CLI does not set BUCKET_NAME in
  extend mode either. Its only assignment is in the platform-mode plugin loader,
  from `nsplugin.json`'s bare bucket name. The local artifact librarian has been
  equally unserved under both front ends.

- fix: Name the environment slug that cannot deploy, and the manifest a purge keeps

  Two things verification found that nsctl can report and not fix.

  **Only an environment named `local` can deploy anything.** ms-deployment
  passes an environment's own name as `hmd deploy --environment`, because a
  local Environment entity is typed by its slug -- deliberately, in both front
  ends, so identical instance names stay safe across environments. hmd-lib-cdktf
  then reads that same value as the AWS deployment *tier*:

      s3_use_path_style=True if self.environment == "local" else None
      use_path_style=True    if self.environment == "local" else None

  so any other name gets virtual-host S3 addressing against a Floci that
  publishes no per-bucket DNS, and the environment's first CDKTF node dies in
  `tofu init` with `dial tcp: lookup hmd.000000000001.reg1.tfstate.neuronsphere:
  no such host`. One name doing two jobs. The fix belongs in hmd-lib-cdktf,
  three lines under its own comment that "the endpoint itself comes from
  AWS_ENDPOINT_URL" -- the predicate should be "the endpoint is a local Floci",
  not "the tier is spelled local". Not introduced here and not fixable here: the
  Python seeds the identical Environment.type and fails identically. It had
  simply never been tried, because every local environment anyone has deployed
  was called `local`. `nsctl env add` and `env start` now say so, at both ends,
  rather than letting it surface 955 lines into Terraform output.

  **A purge keeps the environment manifest, and now says which one.** The
  manifest is the user's declaration, often version-controlled, and rebuilding
  from it is a common reason to purge -- so deleting it would be wrong. But it
  lives in `$HMD_HOME/environments/`, not the state directory a purge removes,
  and a purge also *unregisters* the environment: the file is orphaned and
  silently adopted again by `env add` under the same name. A purge announcing
  "none of it comes back" was followed by a start trying to deploy 28 workload
  repos nobody had declared in that session.

- fix: Three more defects only a cold start could find

  `The cold start had never run` recorded five defects that a warm deployment
  graph had hidden, and the lesson that "verified against a running platform"
  and "verified from nothing" are different claims. Verifying the six items of
  2026-09-07 from nothing found three more of exactly that shape.

  **The kubeconfig is written between the two changesets, not after both.**
  `Start` already knows the cluster "may only now exist" and re-runs
  `startCluster` for that case -- but after `Apply` returns, and the cluster is
  created inside Apply's *first* phase and consumed by its second. On a cold
  start Phase B's first Helm chart therefore ran against a kubeconfig nothing
  had written, Docker created the missing bind-mount source as a directory, and
  `ext-secrets-crds` died with `IsADirectoryError: [Errno 21] Is a directory:
  '/root/.kube/config'`. 538c10a taught `WriteKubeconfig` to replace such a
  directory, which is the right repair and did not help: nothing had called
  `WriteKubeconfig` at all. `provisionNewCluster` now runs between the phases.

  **A database Floci has no record of is deployed, whatever the graph says.**
  The deployment graph lives in the control plane's Postgres and outlives
  `nsctl env purge <name>`, which destroys the environment's resources and
  leaves every RepoInstanceDeployment reading DEPLOYED. So purge, `env add`,
  `env start` planned "3 unchanged" and never recreated the database -- two
  lines below its own warning that no database existed. `K3sMissing` already
  forces the cluster back into the plan for this exact reason; the database now
  does the same.

  **An uncached backend image is pulled rather than refused.** `CheckBackendImages`
  stopped the start whenever Floci's pinned Postgres or Gremlin image was not in
  the local cache. On the machine this proposal is about -- one whose only
  prerequisite is Docker -- nothing is cached, so the guard turned every
  genuinely fresh start into a hard stop at the control-plane database. It did
  exactly that. `EnsureBackendImages` pulls, and keeps the refusal for the case
  the message was written for: a reference that resolves nowhere. It now names
  `HMD_LOCAL_NS_CONTAINER_REGISTRY` too, which was the actual cause.

- fix: Purge what Floci spawned that a purge does not name

  The first real `nsctl env purge` -- the verb has been unit-tested and never
  run -- left seven containers, three volumes and the platform network behind,
  and the three failures were one failure.

  `floci-ecr-registry` is neither an eks, an rds nor a neptune container, and
  those are the only three services the purge names. It survived, holding
  `floci-ecr-registry-data` and a live endpoint on
  `neuronsphere_default-<hash>`, so the leftover-volume sweep and the network
  removal both failed behind it. Both sweeps are now by label rather than by
  enumerated service -- `io.floci.account` per environment, and a cross-account
  `floci` sweep between the control-plane teardown and the volume and network
  removals -- so a service Floci grows next needs no change here.

  `RemoveVolumes` returned on the first volume it could not remove, so the two
  `floci-rds-db-*` volumes queued behind the in-use ECR one survived too. A
  sweep whose stated purpose is "the volumes an earlier purge could not reach"
  cannot stop at the first volume it cannot reach; that is how forty-nine of
  them accumulated. It is best-effort per volume now and reports every failure
  at the end.

  `compose.Runner.Remove` was written for this -- "Only a purge does this" --
  and nothing ever called it. `PurgeAll` went through `controlplane.Stop`, so a
  full purge left `hmd_proxy`, `floci`, `hmd_deployment_gui` and `hmd_nsrunner`
  as Exited containers named after a platform whose network and every
  `$HMD_HOME` path they were configured from had just been deleted.
  `controlplane.Remove` is the purge's teardown; `Stop` stays what
  `control-plane stop` uses, because a stopped container restarting onto the
  same network is the whole of a warm start.

  The ordering claim the unit tests assert did hold: no purge warned
  `Could not connect to the endpoint URL "http://localhost:4566/"`, so Floci
  really is asked for its deletes before it stops.

- test: A parity suite for env start, stop, status and the registry

  SPEC012 proposed "a parallel suite parameterised on the binary under test" and
  it was never built, which is why docs/nsctl.rst and the README carried a "no
  automated parity harness" bullet instead of recommending nsctl outright.

  `test/nsctl_parity.robot` performs each operation with one front end and
  verifies it with the other, in both directions, for the four things SPEC012's
  acceptance test names. That is the whole of it: full parity across every verb
  is a much larger suite, and the SPEC now says only the core was built rather
  than recording the deliverable as met.

  It needs a real platform, so it sits with the Docker-dependent suites and is
  not wired into `make test-cli` or CI. `make test-parity` requires
  NSCTL_PARITY_ENV to be named: the suite starts and stops a real environment,
  and HMD_HOME alone is not consent, being set in every shell anyone works in.
  That guard exists because the suite was run at a live platform by reflex
  during its own development.

- fix: Name a cluster that predates the Traefik migration

  SPEC014 said two dropped host-only helm paths "must be detected and named, not
  silently mishandled", and neither was. One of the two turns out not to exist:
  the direct `helm upgrade --install` of ext-secrets is dead in the Python too,
  since ext-secrets became a BOM entry, and nsctl deploys it as a DAG node now.

  The other is real. A cluster or volume created before ingress was baked into
  the k3s image still carries the Helm release the Python installed at runtime,
  under the same name and namespace as the baked-in manifest, and the two
  collide -- with nothing telling its owner. `EnsureIngressController` looks for
  the release's own Secret (`owner=helm,name=traefik` in kube-system, which
  needs no helm binary) and names both fixes.

  Named rather than removed: uninstalling a release needs helm, which SPEC008
  keeps off the host deliberately, and deleting the Secret alone would leave the
  release's resources behind on a cluster that then looks migrated.

- feat: Bundle the repo trees a deploy needs, and declare External Secrets

  A Homebrew-installed nsctl could not deploy an environment. The substrate's
  first node failed with

      no working tree for hmd-vpc under <HMD_REPO_HOME>; clone it there, or
      declare an explicit source path in the environment manifest

  so "a single binary whose only prerequisite is Docker" was untrue of
  everything past `control-plane start`, and Phase 6's whole distribution effort
  shipped something that stopped there.

  Ten repo trees now travel in the binary: the control plane's own instances,
  the substrate's cluster, the four foundation services, and the External
  Secrets operator with its CRDs. 382 KB gzipped in total, which took the binary
  from 34.4 MB to 34.8 MB against a 50 MB target -- the size trade-off this was
  framed around does not exist. `make generate` prefers the pre_build_artifacts
  destination `hmd build` populates and falls back to trees committed under
  `bundled-repos/`, because it runs from GoReleaser's before.hooks on a clean CI
  checkout with no network and no credentials.

  A tree is materialised to `$HMD_HOME/.cache/neuronsphere/repos/<class>@<digest>`
  before use: projectbuilder is a sibling container, so the host daemon resolves
  its bind mounts and a tree inside the binary is not mountable. It is always
  copied before a deploy runs in it -- the workspace is read-write and deploys
  write `meta-data/resources_output/`.

  Version resolution gains the tier its own comment promised, and loses one it
  had wrong: a checkout used to beat a declared version unconditionally, where
  bom_seeder only prefers a tree when asked by a `=local` pin or
  HMD_LOCAL_NEURONSPHERE_PREFER_LOCAL_VERSIONS. Without that correction a stray
  checkout would silently shadow a bundled tree.

  Bundling alone would not have been enough. The two ext-secrets entries reached
  nsctl only through the environment manifest, which on a developer machine the
  Python CLI wrote -- so an environment created by `nsctl env add` alone got a
  cluster with no operator, and the first cloud chart rendering an
  ExternalSecret failed on the missing CRD. They are declared now, mirroring
  bom_seeder's values including the three that are not guessable: the local
  store overrides that replace IRSA, `dockerRepoSecret` targeting
  aws-parameter-store because create_secret() always writes to SSM, and no
  aws_region because hmd-cli-helm sets it with --set.

  The account is the trap. `localAccessKeyId` selects which emulated AWS account
  the operator's lookups resolve in, and one Floci serves them all, reading the
  account off the 12-digit key. Two environments sharing a key both read the
  same account's secrets and nothing fails, because the names are identical
  across environments. It is injected per environment, and injected before the
  reconcile plan is hashed as well as inside Seed -- change_set_builder does the
  same in both places and records why: "Omitting it left ext-secrets permanently
  drifted."

  This widens what "substrate" means. An environment is now a cluster, a
  database, and the operator that lets a cloud chart's secrets resolve; SPEC001,
  docs/nsctl.rst and the README said "a cluster and a database with nothing on
  them", which was a nicer sentence and not true of anything deployable.

- fix: Refuse to recreate another HMD_HOME's control-plane containers

  `hmd_proxy`, `floci`, `hmd_deployment_gui` and `hmd_nsrunner` are fixed
  `container_name:` values, while the network, the compose project and the Floci
  data directory are all namespaced by an HMD_HOME hash. So two HMD_HOMEs get
  distinct everything except the containers -- and `upService` inspects by name
  and compares only its own config hash, which cannot match across homes because
  the bind paths differ. A control plane started in a second HMD_HOME therefore
  force-removed and recreated the first one's containers, pointed at the new
  home, and interrupting that start left the first with no proxy at all. It
  destroyed a running platform during this work.

  `CheckOwnership` reads back the `com.docker.compose.project` label nsctl has
  been writing on every container it creates and never once consulted, and
  `control-plane start` refuses (exit 3) before creating anything, naming both
  containers and both HMD_HOMEs. A container that cannot be inspected, or that
  carries no project label, is unknown rather than a conflict: this decides
  whether to refuse, so a daemon hiccup must not invent an owner.

  This detects the collision; it does not fix it. Two HMD_HOMEs still cannot run
  control planes concurrently. See SPEC014 for why the rename that would fix it
  is a breaking change across the compose aliases, the nginx fragments, the
  Robot suites and the Python CLI -- and why Floci's `neuronsphere` alias cannot
  move regardless.

- feat: Purge an environment, deleting Floci's resources before Floci

  `nsctl env purge [name]` is the last verb SPEC001 named and the only teardown
  either front end has. `env delete` is a registry edit and says so; until now
  `hmd neuronsphere down --purge` was the only way to actually destroy an
  environment.

  It is not a transliteration of the Python's, because the Python's has the one
  bug a purge cannot afford. `stop_neuronsphere_extend` removes the `floci`
  container and only then asks Floci to delete the control plane's RDS instance
  and graph, so both calls fail:

      Could not delete the control-plane RDS instance: Could not connect to the
      endpoint URL "http://localhost:4566/"

  and their containers and volumes stay. Forty-nine stale `floci-*` volumes had
  accumulated that way. Here everything Floci spawned is deleted through Floci
  while Floci is still running, the control plane stops afterwards, and a name
  sweep then clears whatever earlier purges left behind.

  Three other things a purge has to reach and nothing did. A projectbuilder from
  an interrupted deploy holds the network open and cannot be found -- it exits
  with a Docker-generated name and no label -- so the runner now labels every
  container it starts with the environment it is deploying. The historical
  shared kubeconfig at `$HMD_HOME/.cache/k3s/kubeconfig` was cleaned up by
  neither implementation. And the registry entry goes last, because an
  unregistered environment whose containers still run is exactly the state
  `env delete` refuses to create.

  A `legacy_layout` environment is refused rather than half-purged: it shares
  the control plane's database and graph, so these rules would destroy state
  that is not its own.

- fix: Give the control plane somewhere to submit deployment workflows

  `hmd-ms-deployment` resolves its workflow runner from
  `HMD_WORKFLOW_RUNNER_URL`, and nothing set it -- not `floci.ServiceEnv`, not
  the compose file, not the Python CLI. So `HMD_LOCAL_SERVICE_SUBMIT=1` seeded a
  changeset, waited its two minutes for a workflow nobody had been asked to
  submit, and failed "no workflow appeared". Phase 4 recorded the inversion as a
  met deliverable; half of it had never been built.

  `ServiceEnv` sets it now, for `hmd-ms-deployment` alone and only when the
  runner is enabled. Both halves matter. It goes in `ServiceEnv` rather than at
  a call site because that function builds every foundation Lambda's
  environment, so the "this service only" rule cannot be forgotten by a new
  caller; and it is gated because a control plane told to submit to a runner
  that is not running fails *every* deploy, where an unset variable falls back
  to executing the deployment in-process.

  The address is `http://hmd_nsrunner:8080`, not `http://localhost/nsrunner`.
  The consumer is a Lambda inside Floci, where the host route through hmd_proxy
  resolves to its own container. `RunnerInternalURL` is now the one place that
  string lives; `RunnerRoute` had been a second copy of it.

  The bootstrap deploys the foundation Lambdas once and a warm start skips it,
  so this alone would only ever have reached a control plane that had never been
  started. `syncRunnerSelection` reconciles the variable onto the deployed
  function on every start, in both directions.

- fix: Replace the directory Docker leaves where the kubeconfig belongs

  Docker creates a missing bind-mount source as a directory. A deploy that
  mounts the environment's kubeconfig before the cluster has written it
  therefore leaves a directory in its place, after which every write fails
  (`is a directory`) and every later deploy mounts the directory instead of a
  config. projectbuilder then dies with `IsADirectoryError: [Errno 21] Is a
  directory: '/root/.kube/config'` -- naming the container's path and nothing
  that leads back to the host file. A directory is never a valid kubeconfig, so
  WriteKubeconfig now removes one before writing.

- fix: Never deploy two instances of one repo class at once

  They share a working tree, and so share its `cdktf.out`. Three
  hmd-inf-s3bucket nodes dispatched together and one node's
  `rmtree("cdktf.out")` deleted the provider directory another was walking:

      FileNotFoundError: [Errno 2] No such file or directory: 'linux_arm64'

  A manifest with six hmd-inf-s3bucket instances and four hmd-database-account
  ones hits this every run. The dependency graph cannot express it, because it
  is not a dependency -- it is a shared resource -- so the scheduler tracks
  which classes are deploying and skips over a ready node whose class is busy.
  Skips rather than stops: with six buckets ready, stopping would idle every
  worker behind the first, and unrelated repos still overlapping is the whole
  point of SPEC011. A failed node frees its class the same as a successful one.

- feat: Apply the substrate and the manifest as two changesets, with core resources between

  A dependency carrying a `tag_selector` is matched against concrete Resources,
  not against produced types. hmd-database-account's `create-service` role asks
  for an `application.neuronsphere.io/microservice` tagged
  `repo_class=hmd-ms-dbaccount`, so declaring that the core instance produces
  `microservice` was never enough, and one changeset holding all 32 instances
  failed at apply:

      For RepoInstance, airflow-db-account, role, create-service: supplied
      instance, local-neuronsphere, satisfies neither the required resource
      type ... nor a suggested repo_class.

  Those Resources can only be attached once `local-neuronsphere` has a
  RepoInstanceDeployment, which a changeset is what creates -- hence two
  phases. The substrate applies and deploys, its Resources are submitted, then
  everything the manifest declares is seeded. `bom.LocalCoreResources` builds
  the Docker network, the cluster's compute node and Traefik-as-ingress-
  controller, and one tagged microservice Resource per foundation service;
  `SubmitResources` attaches them, per-resource best-effort. On a restart the
  deployment is recovered from the graph rather than re-seeded, so a running
  environment picks up newly-defined core Resources without a rebuild.

  Verified on a cold start against a purged graph: four substrate instances
  deployed, seven core Resources submitted, and the declared changeset applied
  where it had been failing.

- fix: Stop nginx truncating a runner submission at 1 MB

  A submission carries the whole workflow manifest -- every node's script,
  configuration and dependency edges -- and 28 instances exceed nginx's
  default `client_max_body_size`. The proxy answered 413 before the runner saw
  the request, so `the runner answered 413: <html>` was all any layer could
  report. The passthrough location no longer caps it.

- fix: Say which toggle is missing when the runner is unreachable

  `hmd_nsrunner` has `restart: unless-stopped`, so it survives a control-plane
  start that did not enable it -- but the proxy route is only written when it
  is enabled, leaving the container up and unreachable. The error said only
  that nothing answered, which points at the container rather than the flag.

- fix: Register resource types and a deployment set's definition on a cold start

  Two seeding steps nsctl never performed, both invisible until the first
  `env start` against a graph nothing had seeded before.

  `declare_produces_resource_definition` was called for types that were never
  registered, so a purged graph answered `HTTP 400: ResourceDefinition
  {'resource_definition_name': 'aurora-postgres', ...} not found`. Seed now
  calls `seed_base_resource_definitions` for the bundled supertypes and upserts
  each producing repo's own `meta-data/resources/*.yaml` before declaring
  anything -- a repo's definition parents onto a base type, so the order is the
  contract, not a preference. Per-repo upserts warn rather than fail, because
  the declaration that follows is what actually needs the type and says so
  precisely.

  `EnsureDeploymentSet` sent `{name, environment}`. The entity has no
  `environment` field and requires `definition`, so a create answered `HTTP
  422: {"loc":["body","definition"],"msg":"Field required"}`. It now sends the
  base64-JSON definition naming the environment by slug, and repairs a row that
  names a different one -- which the function's own comment already promised
  and did not do. Only a cold start reached either: on a graph that already had
  the row, the find-first guard returned before the PUT.

- fix: Point every `nsctl ...` suggestion at a command that exists

  The error you hit with no environments registered said to run `nsctl env add
  --name local`. There is no `--name` flag; `env add` takes a positional, so
  following the advice produced a second error. Four printed strings had it,
  plus a comment naming `nsctl env down --purge`, which has never been a
  command.

  `cmd/suggestions_test.go` now walks the module for backticked `nsctl ...`
  strings -- messages and comments alike -- and resolves each against the live
  cobra tree: subcommands must exist, and every `--flag` must exist on the
  command it is attached to.

- fix: Stop the DAG runner on `down`, which left its container running

  `hmd neuronsphere down --purge` set `COMPOSE_PROFILES` to the Deployment GUI
  profile alone, so `docker compose down` never saw the `nsrunner` service and
  `hmd_nsrunner` survived a full purge. Verified against the compose file: with
  `deployment-gui` active, `config --services` lists proxy, floci and the GUI;
  the runner appears only once its own profile is named.

  A teardown now activates *every* profile the compose file defines rather than
  the ones this CLI would have started. The two sets are not the same -- nsctl
  starts the runner under a profile the Python never activates -- and the same
  asymmetry bites within one front end: start with the GUI on, unset the
  variable, stop, and its container survives. `up` is unchanged and still
  activates only what is enabled, because the runner image may not exist yet
  and starting it is nsctl's decision.

  On the Go side `compose.Runner.Stop` took an `active map[string]bool` it
  never read. It stopped every service already, so nsctl had no bug -- but a
  parameter that looks like a profile filter and is not is how this returns.
  Removed, with the reasoning recorded where the next reader will need it.

- feat: Ship nsctl through Homebrew, an install script and GitHub Releases

  `.goreleaser.yaml` and a tag-triggered `release` workflow build
  darwin/{amd64,arm64} and linux/{amd64,arm64} -- 33.5 MB (linux/arm64) to
  38.9 MB (darwin/amd64), against SPEC013's 50 MB target -- and push a cask to
  `neuronsphere/homebrew-tap`, so
  `brew install neuronsphere/tap/nsctl` works. `install.sh` is the
  platform-detecting curl-pipe-sh for everyone else; it verifies the download
  against the release's `checksums.txt` and refuses to install on a mismatch.

  `windows/amd64` is dropped rather than published untested. nsctl shells out
  to `docker`, mounts `/var/run/docker.sock`, and composes host absolute paths
  a sibling container must resolve identically; none of that has a Windows
  equivalent, so the archive would have installed and then failed. WSL2 users
  take linux/amd64.

  `-s -w` is carried across from the Makefile rather than left to GoReleaser's
  defaults: it is 15 MB, not tidiness.

- feat: Build the DAG-runner image on demand instead of pulling one

  The compose file defaulted `HMD_NSRUNNER_IMAGE` to
  `ghcr.io/neuronsphere/hmd-img-nsrunner:stable`, a tag nothing publishes,
  which is why the runner was off by default. Nothing publishes it now either:
  `make generate` packs this module's sources into a deterministic tarball,
  nsctl embeds it, and `control-plane start` unpacks it and builds the image
  when the runner is enabled and the image is absent. A Homebrew install can
  therefore enable the runner, which a published tag would not have allowed for
  a locally-modified runner anyway.

  The image tag carries the sources' digest, so it is rebuilt exactly when they
  change. `ENTRYPOINT` is split from `CMD`, which leaves serving the default
  and makes the same image usable as a CLI -- SPEC013's fourth channel.

- feat: Report the ms-deployment version from `nsctl version`

  SPEC013 asked for it and the comment in `version.go` said it was waiting on a
  client that landed in Phase 3. It reads `info.version` from the service's
  OpenAPI schema, which hmd-base-service builds from `HMD_REPO_VERSION` -- the
  same variable nsctl sets when it deploys the Lambda. Its own two-second
  budget, not the client's ninety: this is the command you run to check an
  install, so it answers on a machine with no platform at all.

- test: Add the CLI contract suite `make test-cli` was pointing at

  The target invoked `test/nsctl_cli.robot`, which did not exist. It does now:
  seven cases covering the version lines, help, usage exit codes and the
  missing-HMD_HOME refusal, none of which need Docker. Also the CI workflow
  NERD002's Phase 0 claimed had landed -- the repository had no `.github/` at
  all -- running fmt, vet, tests, the race detector and this suite.

- feat: Schedule DAG nodes on their dependencies instead of one at a time

  Every node ms-deployment emits carries `dependencies` -- the instance's real
  edges, transitively reduced and filtered to this changeset -- and until now
  nothing read the field. The runner walked the list. It now builds an
  in-degree map over those edges, dispatches every in-degree-zero node to a
  bounded pool, and releases successors as each lands. Six independent nodes
  take 8s at the default concurrency against 21s sequentially.

  Concurrency defaults to 4, which is what the cloud path already runs at
  (Argo's `spec.parallelism`). A submission may pin its own;
  `HMD_LOCAL_RUNNER_PARALLELISM=1` is the way back to a sequential deploy.

  Fail-fast stops dispatch rather than cancelling the context, because node
  execution runs `docker run` under `exec.CommandContext` and cancelling it
  would kill deploys mid-`terraform apply`. Nodes already in flight finish and
  report; a node that succeeds after a sibling failed still counts as landed,
  because the reconcile snapshot must name exactly what is deployed.

  Concurrent nodes share one log, and that log is what `env start` and
  `env attach` show, so output is prefixed per instance -- and only when more
  than one node can be in flight.

## 2026-09-06

- feat: Add the DAG-runner service, and submit deployments to it

  The CLI used to pull. It asked ms-deployment for a node list and executed
  that list in its own process, so a deployment lived and died with the command
  that started it: a closed terminal killed a half-finished deploy, a second
  `env start` raced the first, and the control plane had no idea a workflow
  engine existed. In the cloud the same changeset becomes an Argo Workflow and
  the CLI does not run it at all.

  `nsctl runner serve` inverts the local path to match, and it runs as a
  control-plane container. `nsctl env attach` joins a deployment already in
  flight -- from another terminal, or after the first was closed.

  The routes are Argo's, because ms-deployment already speaks them, so
  selecting a runner is a client choice rather than a second protocol. What
  travels inside the envelope is not: `workflow` carries the node manifest
  `generate_local_deployment` already produces, since there is no argoproj.io
  CRD locally.

  Node execution is not reimplemented -- `internal/runner` already did it and is
  used verbatim. Only two seams were added to it, a configurable work dir and
  exported status flips. That status already flows over REST rather than
  through the CLI's memory is what keeps the change this small.

  Off by default. The image is not published yet, and the in-process path stays
  a live code path regardless: the control-plane bootstrap deploys
  ms-deployment as its last node, so routing it through a service that is part
  of the control plane would be circular.

- feat: Let the control plane submit the workflow

  `skip_async` stops being hardcoded. It was the local marker -- the flag whose
  only job was to stop ms-deployment building an Argo workflow -- and with
  `HMD_WORKFLOW_RUNNER_URL` the control plane has somewhere real to submit.
  `HMD_LOCAL_SERVICE_SUBMIT=1` selects the fully inverted path; it is opt-in
  because it routes through an async self-invoking Lambda whose behaviour under
  Floci is unverified.

- fix: Give the runner image a docker CLI

  `internal/container` drives Docker by shelling out to the CLI rather than
  through the Engine API, so the binary has to be on PATH. On the scratch image
  the service started, served, and accepted submissions perfectly -- and every
  deploy node died with `exec: "docker": executable file not found in $PATH`.

  No unit test could see it: the Docker seam is an interface every test fakes,
  which is exactly what makes it fast and exactly what makes it blind here. It
  took running a real node in a real container to find.

- fix: Collapse duplicate binds when materialising a container

  Docker rejects a duplicate mount point outright, so a container that was
  merely over-specified failed to be created at all. The runner mounts
  `HMD_HOME` and `HMD_REPO_HOME` at their own paths, which are the same
  directory whenever repos live under the home.

## 2026-09-05

- fix: Rewrite ALB wildcard Ingress paths so Traefik can match them

  Cloud charts spell "everything under this host" the way ALB does, as
  `path: /*`. Traefik reads that literally, as ``PathPrefix(`/*`)``, so every
  request 404s -- the UI is running, routed, and reachable on its NodePort, yet
  dead through its own hostname. `hmd-inf-trino` ships exactly this.

  `NormalizeIngressPaths` rewrites the wildcard to a prefix and moves
  `pathType` to `Prefix` alongside it, batched into one exec like every other
  Kubernetes step. Absorbing the ALB dialect here is the same move as pointing
  the `alb` IngressClass at Traefik: it is what lets cloud charts deploy
  unmodified, where editing them to say `/` would break the real ALB.

  Unlike the Traefik Deployment, these Ingresses belong to Helm releases rather
  than a k3s Addon, so nothing reverts the patch -- but a chart redeploy
  reintroduces the ALB syntax, and the next start normalizes it again. The
  rewrite is idempotent: a rewritten path has no wildcard left to match.

- fix: Keep the k3s container off Docker's default bridge, and stop reporting a dead cluster as a successful start

  `nsctl env start` printed a page of "container is not running" warnings and
  then `Ready.` with exit code 0, while the k3s container had exited eleven
  seconds in with `Failed to start networking: ... failed to find interface
  with specified node ip`.

  Floci's EKS spawner attaches the k3s container to Docker's default bridge in
  addition to the network `FLOCI_SERVICES_EKS_DOCKER_NETWORK` names -- every
  other container it spawns (RDS, Neptune, Lambda) gets the configured network
  alone. k3s takes eth0, the bridge, for its node IP, and the bridge hands out
  addresses by start order, so the address churns between runs while the
  datastore in `/var/lib/rancher/k3s` keeps the previous one. kube-router then
  finds no interface holding the recorded address and aborts k3s. It is the
  same reused-volume/churned-identity failure the wrapper image's pinned
  `--node-name` already fixes, one field over.

  `EnsureK3sRunning` now takes the container off the default bridge before
  starting it, and bounces a running container that is still dual-homed.

  Reporting was the other half. `PrepareNode` polls `kubectl get nodes`, and
  during the seconds the API server is alive the *stale* Node object still
  reads `Ready`, so the probe passed on persisted state; every later step is a
  best-effort `docker exec` that only warns. Now `WaitForK3sAPI` short-circuits
  as soon as the container exits instead of burning its whole budget, an
  already-running container is probed rather than trusted, the cluster is
  re-checked after provisioning, and a dead one omits the `k3s localhost:<port>`
  line, skips the deploy and exits non-zero.

- fix: Judge k3s image staleness against the pin Floci was actually started with

  `expectedK3sImage` returned `""` unless `HMD_LOCAL_K3S_WRAPPER_IMAGE` was
  set, so a wrapper-image bump never marked the old container stale and nsctl
  restarted it forever. It now reads `FLOCI_SERVICES_EKS_DEFAULT_IMAGE` off the
  running Floci container -- the only place the effective pin exists, since the
  compose value resolves through two nested defaults. Reconstructing it from
  the process environment is what made the Python declare healthy clusters
  stale and destroy them.

- feat: Port `reconcile_k3s_container` to Go as `ReconcileK3sCluster`

  Closes the `K3sStale` dead end, which previously only warned "use `hmd
  neuronsphere up`". Terraform reconciles the cluster *record*, so a record
  Floci still reports as ACTIVE is "no changes" however dead the container is;
  clearing the record is what makes the next deploy rebuild it.

  Two cases are deliberately stricter than the Python: Floci failing to answer
  never leads to a delete, and a container whose image cannot be read is left
  alone. Unlike the Python it also keeps the `/var/lib/rancher/k3s` volume by
  default -- that drop existed to avoid the ghost-node failure the pinned
  `--node-name` now prevents, and preserving the datastore is what makes a
  wrapper-image bump a repair rather than a full BOM redeploy.

  Adds `github.com/aws/aws-sdk-go-v2/service/eks` (nsctl's first EKS calls),
  which costs 3.3 MB of binary (45.2 -> 48.5 MB) -- well under the 24 MB that
  got EC2 declined.

- fix: Redeploy the cluster instance when its container is gone

  The reconcile clearing a cluster record is invisible to the deploy plan: the
  graph still calls `eks-cluster` DEPLOYED and the snapshot still matches, so
  the plan reported "32 unchanged" and skipped it -- leaving a cluster deleted
  and nothing rebuilding it. A missing k3s container now forces that instance
  back into the plan through `reconcile.Compute`'s existing `missingReleases`
  seam, and a run that ends with no container fails instead of printing
  `Ready.`. The cluster the deploy creates is also provisioned in the same run
  rather than waiting for a second `nsctl env start` nobody knows to run.

- fix: Pin the k3s wrapper image to `hmd-img-k3s-floci:0.3`, which disables the
  NetworkPolicy controller that reads the stale node IP. This is what revives an
  existing cluster in place, without dropping its datastore or the Helm releases
  on it.

## 2026-09-04

- fix: Restart the control-plane graph on a warm start

  Floci stops the containers it spawned and does not bring them back, and only
  the database was being restarted. The graph was left down with `global-graph`
  resolving to nothing, which surfaced several layers away as Trino
  crashlooping on "global-graph: Name or service not known".

  The lookup used the *environment's* graph identifier, which finds nothing for
  the control plane and reported no error, so the code read as correct.

- fix: Make the cold bootstrap actually work

  Six defects found by bootstrapping a fresh HMD_HOME end to end: the
  control-plane account had no VPC so the database had nowhere to go; Floci
  state left by a failed bootstrap was read as a completed one; a failed node
  reported no diagnosis; the foundation services' SERVICE_CONFIG was read from
  the wrong place; and the RepoClass's cloud references -- secret names,
  pgbouncer, Neptune, DynamoDB tables -- were passed through unresolved.

- feat: Bootstrap a control plane that has never been bootstrapped

  `nsctl control-plane start` no longer refuses a fresh `HMD_HOME`. The
  control-plane Postgres and graph are deployed by the same RepoClasses the
  cloud uses, through the same projectbuilder path, then the core databases
  are created directly and the three foundation Lambdas are deployed with
  ms-deployment last.

  Unlike the Python, this runs only when the control plane has not been
  bootstrapped. A warm start recovers the gateway ids by listing them instead
  of redeploying Postgres through Terraform on every `up`.

- feat: Add `nsctl repo import`

  Writes an environment's deployed instances into its manifest, closing the
  migration gap from the Python CLI: an environment brought up by `hmd
  neuronsphere up` gets its workloads from installed plugin packages, which
  nsctl does not read, so they showed as "deployed but undeclared" on every
  apply. Importing the live `local` environment turned 28 such warnings into
  "0 deployed but undeclared".

- fix: Send an unfiltered entity search as `{}`

  The zero `Filter` encoded as `{"attribute": "", ...}`, and ms-deployment
  answers that with a 500 rather than every row -- so `repo list` and the
  reconcile plan could not read the deployment graph at all.

- fix: Resolve repo versions before computing the reconcile plan

  The version is part of an entry's digest, and it was being filled in by the
  seeder after the plan was computed. Every recorded digest covered an empty
  version, so a version bump was invisible as drift and the digest disagreed
  with the one the Python front end writes for the same entry.

- feat: Add `nsctl env add` and `nsctl env delete`

  Three error messages already told users to run `nsctl env add`, which did
  not exist. It registers an environment and allocates its account, port slot
  and names, deriving every one the same way `env_registry._build_environment`
  does so an environment created by either front end is startable by the
  other. `env delete` unregisters, refusing while the environment's containers
  are still running.

- feat: Read what a RepoClass produces from its own resource declarations

  `meta-data/resources/*.yaml` is where a repo already states what it emits,
  and it is what the cloud deploy path reads. nsctl carried a parallel
  hardcoded table, which had already drifted: it named
  `network.neuronsphere.io/vpc` while hmd-vpc's own file said
  `aws.neuronsphere.io/vpc`, so a consumer requiring the former resolved only
  because the table was preferred. The table survives as a fallback for a repo
  whose working tree is not on this machine, and a test fails if the two ever
  disagree again.

- feat: Deploy only what is new or has drifted

  `env apply` diffs the desired definition against the deployment graph and
  the last-applied snapshot, and deploys the difference. Digests are
  byte-compatible with `change_set_builder.entry_hash`, verified against golden
  vectors generated by calling CPython's implementation -- a digest that
  disagreed would turn a user's first `nsctl env apply` into a full redeploy of
  a working environment.

  Fail-safe throughout: an unreadable graph deploys everything rather than
  skipping silently, a missing snapshot means "no information" rather than
  "everything changed", and an instance that is deployed but no longer
  declared is reported and left running. `--force-full-redeploy` is the escape
  hatch.

- feat: Add `nsctl repo add`, `repo remove`, `repo list` and `env apply`

  The daily-usage surface. `repo add`/`remove` edit the environment manifest
  and nothing else; `env apply` deploys the substrate plus everything the
  manifest declares. Editing the file by hand is equivalent -- the verbs are
  wrappers, not a second source of truth.

  `repo list` shows declarations beside the deployment graph, including
  instances deployed but declared nowhere, which is what a deleted line leaves
  behind. It degrades to the manifest half when the deployment service is
  unreachable rather than failing.

- feat: Resolve an environment's deployed instances from the graph

  `InstanceStatus` joins repo_instance, repo_instance_deployment and the edge
  between them, scoped by deployment_id. The status is not on repo_instance,
  so filtering that entity for it returns a 500.

- feat: Build BOM entries from an environment manifest

  `bom.Declared` turns a manifest's declarations into changeset entries that
  deploy alongside the substrate, so a declared instance can depend on
  `eks-cluster` or `environment-db` by name. Nothing is auto-wired.

- fix: Honour a manifest's pinned repo version

  `Resolve` took no declared version, so a `version:` in a manifest was
  discarded whenever a working tree existed -- which is always, locally.

- fix: Accept a list of instance names as a dependency

  The change_set schema types a dependency value as a string *or* an array of
  them. Entries modelled only the string, so the topological sort could not
  see a dependency expressed as a list and was free to order a producer after
  its consumer.

- feat: Read and write the environment manifest

  `$HMD_HOME/environments/<slug>.yaml` is the desired state an environment
  reconciles to, in the format `env_manifest.py` already defines, so either
  front end can read what the other wrote. `plugins` and `plugin_config`
  configure a discovery mechanism nsctl does not implement; they survive a
  round trip untouched and are reported rather than silently obeyed.

  An environment with no manifest is not an error -- it is the empty one, and
  it gets the substrate and nothing else.

- feat: Install the binary with `make install`

  `make install` builds and copies `nsctl` to `$(PREFIX)`, defaulting to
  `~/.local/bin` -- on PATH for a normal login shell and writable without sudo.
  `make uninstall` removes it. This is the interim answer until Phase 6 ships
  GoReleaser and a Homebrew tap.

- feat: Deploy the environment's network instead of working around its absence

  `base-vpc` is a substrate BOM entry now, deployed from `hmd-vpc`, and
  `environment-db` and `eks-cluster` take their `base-vpc` dependency from it
  rather than from the core instance. The core instance stops declaring
  `network.neuronsphere.io/network` and `/vpc`, which it never created.

  This removes a workaround rather than adding a deploy. nsctl used to create
  the VPC, subnets and DB subnet group directly through the EC2 API, because
  Floci's `Ec2Service.ensureDefaultResources` guards on region alone while its
  VPC storage is per-account -- so every account after the first has no default
  VPC and RDS fails with `InvalidVPCNetworkStateFault`. That bug is still real
  (verified against an empty account), but Floci 2.0.1's EC2 is real enough to
  deploy the repo that owns VPCs, which `bom_seeder` had assumed was "never
  applicable locally". Dropping the EC2 SDK took the binary from 55 MB to 31 MB
  -- 24 MB for four calls.

  `hmd-inf-eks-cluster`'s local overlay had documented its dependency on
  `ensure_rds_subnet_group` running first; that is now a declared BOM edge
  rather than an ordering accident.

- feat: Deploy the environment's dbaccount service

  `hmd-ms-dbaccount` is substrate rather than a user-added RepoClass: every
  RepoClass wanting a database depends on `hmd-database-account`, whose deploy
  calls this service, and nothing needing a database can deploy until it serves.
  It cannot be deployed through the graph either -- its own manifest's required
  dependencies would never resolve locally.

  With it, all five target endpoints answer: the transform API returns a JSON
  body, Airflow and Superset redirect to their logins, Argo serves, and a Trino
  query returns `[[42]]`.

  `nsplugin.json` turned out to contribute almost nothing here -- a Lambda name
  and a repo class, both derivable -- so dropping it cost nothing. The service
  config comes from the repo's own `meta-data/config_local.json`.

  Ordering matters and the code now says why: `WriteEnvRoutes` is the bulk
  writer and rewrites the environment's fragment wholesale, so it has to run
  before the DAG routes are spliced in. Running it afterwards erased the
  transform route that had just been restored.

- feat: nsctl starts and stops an environment

  NERD002 Phase 2c. `nsctl env start <name> --no-deploy` brings up an
  environment's database, graph, k3s cluster, cluster operators and every route;
  `nsctl env stop` stops it without tearing anything down, so the next start
  restarts in place and keeps the cluster's datastore. The control plane starts
  implicitly and is left running.

  Verified against the environment torn down with `hmd neuronsphere down`: a
  full stop/start round trip takes 37 seconds, the k3s container is started in
  place, and Argo, Trino, the ingress vhosts and the DAG-deployed service routes
  all come back.

  Two things nsctl now says rather than leaving to be discovered. An RDS
  instance Floci failed to bring back is reported as `failed` with the redeploy
  that fixes it, instead of the misleading "no database container" -- which
  reads as "nothing was ever deployed". And recreating the Floci container warns
  first, because Floci supervises the containers backing every account's
  databases and its recovery of them is not reliable.

- feat: Provision the k3s cluster from the node container, not projectbuilder

  NERD002 Phase 2b. CoreDNS canonical-name records, the Traefik-as-alb ingress
  patch, node topology labels, stale-node and orphaned-PV reaping, and the
  cluster reads behind `cluster_incarnation_id` and `live_helm_releases`.

  SPEC008 proposed batching every kubectl call into `hmd-img-projectbuilder`,
  on the premise that it is already version-matched to the cluster. The premise
  is wrong: **no projectbuilder image ships kubectl** -- verified against
  `:stable`, `:0.5`, `:0.5.383` and `:localdev`, none of which have it. The k3s
  node container does, at `/bin/kubectl`, matched to the cluster by
  construction, reading its own kubeconfig with nothing to mount, and already
  running whenever there is Kubernetes work to do. So steps `docker exec` into
  it and a full provisioning run launches no containers at all, rather than the
  twenty-nine the host-kubectl version issues or the six SPEC008 hoped for. It
  also drops projectbuilder as a prerequisite of cluster provisioning, which
  SPEC008 had accepted as a real cost.

  Also fixes a latent bug shared with the Python: Docker renders a stopped
  container's empty IPAddress as the literal string `invalid IP`, which is
  truthy, so `invalid IP global-graph` was written into a CoreDNS server block.
  `_resolve_floci_ip` has the same hole and is simply never reached, because
  `floci_container_name` only returns running containers.

  Verified against a live cluster: a pod resolved `neuronsphere` through the
  applied records and fetched Floci's health endpoint over it.

- feat: nsctl starts and stops the control plane

  `nsctl control-plane start` brings up hmd_proxy, Floci and the Deployment GUI
  through the Docker Engine API, starts the control-plane database and aliases
  it as `hmd_db`, provisions the account's Floci resources, discovers the API
  Gateway ids and writes the nginx routes. `control-plane stop` is the only
  command that stops it, and refuses with exit 3 while any environment is
  running unless `--force` -- a lifecycle the Python CLI does not have, where the
  control plane is only ever stopped as a side effect of a bare `down`.

  Two fixes came out of verifying it against a real platform rather than only
  unit tests:

  `com.docker.compose.config-hash` has to be present or `docker compose` ignores
  the container entirely -- `ps`, `stop` and `down` all skip it even when every
  other label matches. Without it `hmd neuronsphere down` would have silently
  stopped nothing. nsctl writes its own hash there rather than reproducing
  compose's algorithm, so the two front ends recreate each other's containers
  once; the label set is now part of the hash, so a future change to it forces
  that recreate instead of leaving containers on a stale set.

  Floci leaves its RDS container stopped on shutdown and nothing restarts it:
  the Python relies on the bootstrap DAG's Terraform apply, and
  `wait_for_rds_instance` only polls. So after a `down` the database stays down
  while its instance still reports `available`, and ms-deployment answers 500 on
  every request. `EnsureRDSRunning` starts it and re-attaches the alias.

  Not ported yet: the control-plane bootstrap DAG, which the Python runs on
  every start. An already-bootstrapped HMD_HOME recovers its gateway ids by
  listing them from Floci instead; a never-bootstrapped one is refused by name.

- feat: Parse the control-plane compose file without docker compose

  The first half of dropping `docker compose` as a host dependency. In extend
  mode compose orchestrates exactly three containers -- hmd_proxy, floci and the
  Deployment GUI -- while adding a toolchain dependency and, through
  `_get_base_command`, a `pip config get` shell-out on every invocation. The
  per-environment file declares `services: {}` and exists only to give compose a
  project to attach, so nsctl skips it entirely.

  `internal/compose` parses the YAML rather than transcribing it into Go
  structs: 227 lines with roughly forty `${VAR:-default}` entries copied by hand
  is a drift hazard. The interpolator matches braces rather than running a
  regexp, because the bundled file nests its defaults --
  `${HMD_LOCAL_K3S_WRAPPER_IMAGE:-${HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/neuronsphere}/hmd-img-k3s-floci:0.2}`.

  The compose file is staged into the Go module by `make generate` rather than
  copied into the repo twice. The Python package reads the same file, and two
  copies would be one copy and one stale copy.

- feat: nsctl reads and reports a local NeuronSphere

  NERD002 Phase 1: `nsctl env list`, `nsctl env status` and
  `nsctl control-plane status` (alias `cp`), read-only and verified against a
  platform the Python CLI brought up.

  `internal/registry` reads `environments.json` rather than re-deriving it, and
  its regression fixture is the literal JSON the Python emits -- the test
  asserts a save reproduces it byte for byte, which a round-trip fixture could
  never do.

  `env status` corrects a real inaccuracy rather than porting it.
  `environments.environment_status` inspects `env.db_container` and
  `env.graph_container` by name, but both are Docker network *aliases* attached
  to Floci-spawned containers whose real names are opaque -- as
  `floci_deployer.floci_container_name` says outright, "docker inspect on the
  DNS alias we later attach does not work, because an alias is not an object".
  So the Python reports the database and graph stopped even when they are
  running. nsctl resolves them by Floci's `io.floci.{service,account,resource-id}`
  label triple, checked against the labels a live Floci actually wrote, and
  reports a container that was never created ("absent") separately from one that
  `env stop` stopped.

- feat: Scaffold nsctl, the Go CLI for the local NeuronSphere

  NERD002 delivers extend mode as a single Go binary whose only host
  prerequisite is Docker. This is Phase 0: the module at `src/go/nsctl`, a
  repository-root `Makefile` that injects `meta-data/VERSION` through
  `-ldflags`, the cobra root with `--home`, and `nsctl version`.

  Two pieces carry decisions worth naming. `internal/hmdenv` parses
  `$HMD_HOME/.config/hmd.env` -- ported from hmd-cli-bartleby, which already
  reproduces the Python's `export ` prefixes, quoting and inline comments --
  but returns a map instead of calling `os.Setenv`. SPEC004 requires per-command
  state to arrive through constructor closures, because `t.Setenv` panics under
  `t.Parallel()`; a loader that mutated the process environment would put the
  precedence rule back out of a test's reach. `internal/nserr` carries SPEC004's
  exit codes (1 error, 2 usage, 3 in use, 4 deploy node failed), which the
  Python CLI has no equivalent for.

  HMD_HOME is not defaulted. Guessing it would put nsctl's containers, volumes
  and network under a name no other HMD tool computes.

- fix: Reach the k3s API through hmd_proxy, not Floci's published port

  A second `up` left the cluster unreachable with `Unable to connect to the
  server: net/http: TLS handshake timeout`, while `curl` against the same
  address answered fine.

  The cluster was healthy throughout. `down` stops the Floci-spawned
  `floci-eks-*` container and `up` starts it again, and Docker re-creates the
  host port forward for its published port on every start. A re-created forward
  silently truncates any write past roughly one MTU: the first ~1440 bytes
  arrive and the rest never do. kubectl (Go >= 1.24) and OpenSSL >= 3.5 default
  to the `X25519MLKEM768` post-quantum key exchange, which makes their TLS 1.3
  ClientHello 1449 bytes -- just over the line -- so the apiserver waited
  forever for the rest of a hello that never landed. A TLS 1.2 hello is 163
  bytes and connected instantly, which is why macOS `curl` (LibreSSL, no
  ML-KEM) made the API look reachable. Container-to-container traffic never
  touched that forward and was unaffected at any size.

  Every `kubectl` the CLI runs goes through the same kubeconfig, so the whole
  second `up` failed with it: the CoreDNS record, the ingress class, the
  Traefik patch and every Helm deploy.

  The API is now streamed through `hmd_proxy`, restoring the invariant
  `nginx_router` already documents -- hmd_proxy is the only container that
  publishes host ports. Each environment gets a `k3s_port` in a band above its
  slot ports (`19064 + slot`), so no existing environment's Floci, Trino, graph
  or spare port is renumbered; the published range widens to `19000-19079`,
  which recreates hmd_proxy once. `env_stream_entries` re-resolves the k3s
  upstream on every rewrite, so deploying Trino later cannot drop the route.
  Single-stack platform mode still uses Floci's published port -- it has no
  env-scoped stream fragments to point at.

- fix: Keep DAG-deployed API Gateways across a restart

  `up` dropped every `apigateway-*.json` from Floci's data dir before starting
  it, on the grounds that Floci persisted v1 gateway records with all-null
  fields and rehydrated them as undeletable ghosts, and that `setup_service`
  recreates each gateway anyway. That second half only ever covered the
  gateways this CLI creates. A service deployed through the deployment DAG
  (`hmd deploy --local`) owns a **CDKTF-managed** gateway, and a `down`/`up`
  takes the fast path that redeploys nothing -- so the wipe destroyed it with
  nothing left to recreate it. The Lambda survived, the gateway did not, and
  `nginx_router.refresh_deployed_service_routes` found nothing to route:
  `http://localhost/local/transform/...` fell through nginx's catch-all and
  answered `{"error": "no route defined"}`.

  `clear_apigateway_state` is now `prune_apigateway_ghosts`, which rewrites the
  stores in place instead of deleting them: records with no `id`/`name` go,
  real gateways stay, and resources/stages/deployments hanging off a dropped
  gateway go with it. Against Floci 2.0.1, which persists real values, it is a
  no-op.

- fix: Restart the control-plane graph container on `up`

  `down` gracefully stops the Floci-spawned gremlin-server container, and Floci
  never brings it back. `hmd-inf-neptune`'s deploy is idempotent, so the next
  `up` reports the graph node deployed without spawning anything, while the
  cluster record keeps reporting `available`. The `control-plane-graph-alias`
  node then waited out its full 300s timeout on a container that would never be
  running, failed, and took ms-naming, artifact-lib and ms-deployment down with
  it -- the whole control plane, on every `down`/`up`.

  `floci_deployer.ensure_neptune_running` replaces the status-only wait: it
  starts a container Floci left stopped, and returns immediately for the two
  states that can never resolve (no cluster record, or a container that will
  not start) rather than polling them for five minutes. Both graph paths now
  use it -- the control plane's via a new module-level
  `_ensure_control_plane_graph`, and an environment's via `_alias_environment_graph`.

- fix: Probe the address Gremlin Server actually binds

  `_gremlin_port_open` checked `127.0.0.1`, but a running hmd-img-gremlin-server
  has a single listener on `::ffff:<container ip>:8182` and nothing on loopback.
  The probe was therefore refused however long the JVM had been up, so
  `start_neptune_container` reported every healthy restarted graph as one that
  never came up -- making the restart path a no-op wherever it was already
  wired in. It now probes the container's own hostname, which is the address
  consumers reach through the DNS alias.

- fix: Stop `deploy_local.sh` overlays writing into the developer's checkout

  Only a cdktf/helm/config overlay got a temp workspace; a `deploy_local.sh`
  override had the real repo mounted read-write, so it wrote its produced
  Resources to `meta-data/resources_output/` there. Two such files ended up
  committed in `hmd-inf-neptune`, and since the runner submits *every*
  `resources_output/*.json` in the workspace, each of its two instances
  (`control-plane-graph` and `global-graph`) republished the other's Resource --
  including a stale `ws://neuronsphere:8183/gremlin` pointing at Floci's Gremlin
  proxy, the exact endpoint the `graph_host` design exists to avoid. Outputs are
  now also excluded from the overlay copy, so a prior run's cannot become the
  next run's inputs.

## 2026-09-03

- feat: Drop the control-plane graph entirely

  It ran unconditionally alongside the control plane and nothing read it:
  ms-deployment, ms-naming, artifact-lib and dbaccount all declare a single
  `postgres` engine. That is a JVM removed from every install. An existing
  `global-graph` container is left in place and pointed out rather than deleted
  -- nothing in the platform starts it any more, but it is the user's container,
  and its data is a bind mount that survives either way.

- fix: Restart the graph container on the fast path

  Floci stops its Neptune container on shutdown and, unlike RDS, never brings it
  back -- the cluster keeps reporting `available` while nothing answers on 8182.
  A reconciling `up` runs no DAG, so nothing would otherwise start it.

- feat: Move the graph from a compose JanusGraph to a lazily-provisioned Floci Neptune

  Every environment ran a JVM graph container unconditionally, whether or not
  anything used it. The graph is now a Floci Neptune cluster deployed by the
  real `hmd-inf-neptune` repo class, and only when something in the BOM declares
  a `database.neuronsphere.io/graph-database` dependency -- so a default `up`
  runs no graph at all.

  Floci spawns `hmd-img-gremlin-server` (rebuilt on TinkerPop for this role) as
  the backend. Consumers are unaffected: the CLI aliases that container as
  `global-graph-<slug>` and CoreDNS maps the canonical `global-graph` to it, so
  unmodified cloud charts and `ws://global-graph:8182/gremlin` keep working.

  Three Floci behaviours shape the lifecycle, all found by experiment:

  - Its Gremlin proxy is **not restored after a Floci restart**, so the recorded
    endpoint is the container alias on 8182, never `DBCluster["Endpoint"]`.
  - Floci **stops the container but never restarts it**, unlike RDS, so
    `start_neptune_container` exists.
  - Floci mounts **no volume**; the graph lives in the container's writable
    layer and is written by a shutdown hook, so `stop_neptune_container` stops
    gracefully with a 60s timeout -- a kill loses the graph silently -- and
    `down --purge` removes the container deliberately.

  `graph-database` is no longer a core-produced type and the hand-seeded
  `global-graph` Resource is gone: a real producer emits it. Consumer roles
  (`graph-db`, `neptune-db`) that installed plugins pin to the core instance are
  normalised in the assembled BOM, the same way `database-instance` is, so no
  coordinated plugin release is needed.

## 2026-09-02

- fix: Actually apply the Traefik manifest patch, so the UIs are reachable

  `superset.local.neuronsphere.io` and `airflow.local.neuronsphere.io` were
  unreachable because the ingress controller was stuck `Pending`:

      0/1 nodes are available: 1 node(s) didn't have free ports for the
      requested pod ports

  k3s ServiceLB creates `svclb-*` pods for every `type: LoadBalancer` service
  and those are `system-node-critical`, so the OTEL collector gateway's port 443
  permanently preempts Traefik off that host port. The patch that strips
  Traefik's unnecessary `hostPort: 80`/`443` (it is reached by NodePort) already
  existed for exactly this reason -- it just never ran. It resolved the k3s
  container *without* `env`, producing the unqualified name, which does not
  exist for an environment's cluster; every `docker exec` failed and the return
  codes were discarded.

  The same silence hid the missing `alb` ingress-class arg, so airflow's
  `ingressClassName: alb` was never served either. Both edits now report failure
  and name the consequence.

- fix: Resolve the database container by label, so `hmd_db` reaches CoreDNS

  The database is the one canonical name that is not a Docker *container* name:
  Floci spawns RDS backends opaquely and the CLI gives them a network alias. But
  `docker inspect` resolves container names, never aliases, so the CoreDNS pass
  asked for `hmd_db-local`, got "no such object", and skipped the record without
  a word. The live ConfigMap held `neuronsphere`, `global-graph` and `hmd_proxy`
  -- all real container names -- and no database entry, so:

      nc: getaddrinfo for host "hmd_db-local" port 5432: Name or service not known

  Resolved through Floci's own labels (`io.floci.account` +
  `io.floci.resource-id`), the same lookup the rest of the CLI already uses, and
  registered under *both* names the database is addressed by: `hmd_db` for
  unmodified cloud charts, `hmd_db-<slug>` for what the connection secrets
  carry. A database that still cannot be resolved is now a warning naming the
  consequence, not silence.

- fix: Sweep every remaining place that picked a Floci account by position

  One Floci serves every account and resolves which one a caller means from the
  12-digit access key. A placeholder (`test`, `dummykey`) is not one, so it
  resolves to the *default* account -- and since resource names are identical
  across accounts, the failure is silent until something is missing. Four
  independent instances were found by sweeping rather than by the next failed
  `up`:

  - **Every environment's service Lambda** signed as `dummykey`, so its S3,
    Secrets Manager and DynamoDB calls read the control plane's account.
    `_apply_env_overrides` rewrote the endpoint, `HMD_DID` and DB hosts but not
    the account -- and with one Floci serving all accounts, the endpoint no
    longer distinguishes them, so nothing did.
  - **The ext-secrets operator install** (`k3s_operators`) set neither store's
    `localAccessKeyId`. It runs on every `up`, including the restart fast path
    that skips the BOM, so it would have reverted the previous fix and brought
    back "Secret does not exist" after any restart.
  - **The k3s chart-plugin Floci client** seeded buckets and secrets into the
    control plane's account for charts running in an environment's cluster.
    (Opt-in path, so latent rather than live.)
  - **Control-plane Lambdas** read an ambient `$AWS_ACCESS_KEY_ID`, so a
    developer with real AWS credentials exported would have redirected them into
    a third account.

  All now go through `floci_deployer.account_access_key(env)`.

- test: Guard against placeholder and ambient AWS credentials

  An AST check over the package that fails on any credential set to a
  placeholder or read from the ambient environment, with the deliberate
  exception documented inline. It caught two sites the manual sweep had missed.

- fix: Give the RDS deploys an endpoint and engine ms-dbaccount can use

  The admin secret carried Floci's RDS proxy endpoint, which does not survive a
  Floci restart, and an engine string ms-dbaccount silently refuses to act on.
  Both planes now pass the DNS alias the CLI creates -- `hmd_db` for the control
  plane, `hmd_db-<slug>` per environment -- and port 5432, into the deploy
  configuration that `hmd-postgres-rds`'s local overlay records.

- fix: Register the database in CoreDNS once it exists

  `provision_k3s_operators` writes the in-cluster canonical-name records long
  before Phase A creates the RDS instance, and skips any name that does not
  resolve on the Docker network at that moment. So `hmd_db` was absent from the
  live `coredns-custom` ConfigMap entirely, and every chart addressing the
  database by that name failed to resolve it. The records are re-applied once
  the alias is in place.

- fix: Sign External Secrets lookups as the environment's own Floci account

  The operator authenticated as the chart's default `test`, which Floci resolves
  to the default account, so every environment-scoped lookup failed against a
  secret that existed all along in the environment's own account:

      error processing spec.data[0] (key: broker_hmd-inf-transform-broker_local_
      local_reg1_hmdtr1), err: Secret does not exist

  The access key appears in two places in the chart's values and only one is
  operative. `extraEnv` sets `AWS_ACCESS_KEY_ID` in the operator pod, which
  `_inject_floci_account` already rewrote -- but an AWS `ClusterSecretStore`
  using `secretRef` auth reads its credentials from the Kubernetes Secret the
  chart renders from `clusterSecretStore.localAccessKeyId`, and never consults
  the pod environment. Both stores now carry the account.

  Also copies `instance_configuration` before mutating it. `EXT_SECRETS_BOM`
  holds one shared object built at import, so writing through it gave every
  environment whichever account was seeded last and permanently rewrote the
  shipped default -- silent, because the secret names are identical across
  environments.

- fix: Compare against the PostgreSQL image the platform will actually run

  The pre-flight compatibility check reconstructed the image from
  `HMD_LOCAL_NS_CONTAINER_REGISTRY` and `HMD_POSTGRES_BASE_VERSION`. The first
  is a cement config value, not an ambient environment variable, so it silently
  fell back to `ghcr.io/neuronsphere` while Floci was configured with
  `ghcr.io/hmdlabs`. The check then blocked `up` citing "image ships 14" for an
  image that existed nowhere on the machine, against volumes whose containers
  were up and serving on 12.

  Both halves are now resolved the way compose resolves them, falling back to
  the running Floci's own `FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE` -- the one
  place the substituted values are recorded -- rather than to a guess. An
  explicit `HMD_POSTGRES_BASE_VERSION` still wins, because the hazard being
  detected is precisely a *pending* version change.

  Each volume's `PG_VERSION` is now read with the image that wrote it, found
  from the container mounting it, so the check needs no image that is not
  already pulled.

- fix: Say which images disagree, and offer a remedy that keeps the data

  The error named only major versions, so a wrong-registry comparison was
  indistinguishable from a real incompatibility. It now names the incoming
  image and the image that wrote each data directory.

  Since the usual cause is a floating tag moving under an unchanged config --
  `:0.2` advanced from PostgreSQL 12 to 14 while `:0.2.11` stayed on 12 -- the
  first remedy offered is now pinning the image that wrote the data, ahead of
  migrating or purging.

- fix: Name the owning Floci account on every environment API route

  One Floci serves every account and resolves which one a call belongs to from
  the SigV4 credential scope -- the 12-digit access key *is* the account. But
  nginx's `proxy_pass` issues an **unsigned** request, so there was nothing to
  resolve from and every environment's API Gateway invocation landed in the
  *default* account, where that REST API does not exist. Floci answers 404,
  which is indistinguishable from a route that was never wired:

      404 for http://hmd_proxy/local/hmd_ms_dbaccount/api/create_db_account

  with `/local/hmd_ms_dbaccount/` present in the config the whole time and the
  dbaccount Lambda up. Verified against a running stack: the same invocation
  404s unsigned and reaches the Lambda 18/18 times with a credential scope,
  whose date and region are structural filler -- only the account is read.

  Environment routes now carry a static credential-scoped `Authorization`
  header, injected through a `map` that fills only an *empty* one, so a caller
  supplying its own bearer token is not silently rewritten. The control plane
  owns the default account, so its routes are deliberately unchanged. Both
  writers are covered: `write_env_routes` and the DAG-discovery path
  (`_upsert_service_route`), which is how transform and Trino get theirs.

- fix: Give Floci a healthcheck that can actually run

  The probe shelled out to `python`, which Floci 2.0's Java/Quarkus image does
  not ship, so the container sat permanently `unhealthy` while serving
  normally. Uses `curl` (present in the image) instead.

- fix: Address the account-qualified k3s container everywhere, not just on the
  kubeconfig path

  Floci 2.0 names the container and volume it spawns
  `floci-eks-<account>.<cluster>` for every account but the default one, and
  each named environment is its own account. Seven Docker call sites still
  resolved the name without a target, so they defaulted to the control plane's
  account and addressed a container that does not exist:

  - `write_kubeconfig`'s `docker exec` fell through to a synthesized config
    carrying `token: floci-local`, so every later kubectl call failed with a
    bare `401 Unauthorized` — surfacing as an unrelated-looking cert-manager
    install failure in `hmd_cli_helm.create_namespace_if_not_exists`;
  - `stop_k3s_cluster`/`start_k3s_container` silently no-opped, so `down` left
    the container running and `up` could not restart it in place, forcing a
    full BOM redeploy;
  - `purge_k3s_container_and_volume` left both behind, and the adopted
    datastore made the next cluster register as a second, permanently NotReady
    Node;
  - `_floci_eks_ip` returned None, so host ingress and Trino routes were never
    wired.

  `purge` now names both volume forms rather than guessing, since
  `docker volume rm` on a missing name is a no-op and a missed volume is the
  costlier error.

- fix: Repair a NameError on the k3s "cluster already exists" path

  `_k3s_container_image`, `_k3s_container_running` and `_k3s_host_port`
  referenced a `target` they had no parameter for. That path runs on every
  restart. It escaped the tests because they patched the three helpers
  wholesale; `test_k3s_container_naming.py` now exercises them for real against
  a mocked `docker`.

- fix: Resolve the k3s container's account-qualified name (Floci 2.0)

  Floci 2.0 names an EKS cluster's container `floci-eks-<account>.<cluster>` for
  every account except the default one, so an environment's k3s container is not
  `floci-eks-<cluster>` any more. Every place that name was hardcoded broke for
  environments -- which, since Phase 1, is where all workloads run.

  The visible symptom was misleading. `write_kubeconfig` reads the real
  kubeconfig by `docker exec` on that name; when it failed, the function fell
  through to a synthesized config carrying a placeholder token, so the node
  never became Ready and every kubectl call failed with "the server has asked
  for the client to provide credentials" -- which reads like a broken cluster,
  not a name lookup. It also silently broke the CoreDNS records that map
  `hmd_db` and `global-graph` inside the cluster, the Traefik ingress-class
  patch, and NodePort routing.

  `floci_deployer.k3s_container_name` now resolves it once for everyone, by
  looking at which container actually exists rather than by rule alone -- a
  cluster created under the pre-2.0 name keeps working, matching how Floci
  itself claims a legacy container when its `io.floci.account` label agrees.

  The placeholder-kubeconfig fallback now warns loudly and names the container
  it expected, so if this class of mismatch recurs it says so at the point of
  failure instead of one layer away.

- fix: Point `database-instance` dependencies at the real Postgres producer

  Removing `postgres` from the core RepoClass's produced types broke every
  plugin BOM that maps `database-instance` to `CORE_INSTANCE_NAME` -- correct
  while the core stood in for the always-on `hmd_db` container, wrong now that a
  real `hmd-postgres-rds` deploy produces it. `up` failed with "supplied
  instance, local-neuronsphere, satisfies neither the required resource type ...
  nor a suggested repo_class."

  Normalised in the assembled BOM rather than fixed in each plugin: plugins ship
  as independent pip packages, so editing them would make the local platform
  require a coordinated release across all of them, and an older installed
  plugin would still break.

- fix: Stop the bootstrap replay claiming a recording it did not make

  Bootstrap-DAG nodes carry locally generated ids, which no ms-deployment entity
  corresponds to, so replaying their statuses 404s and records nothing. It
  logged eight ERRORs and then reported "recorded 8 bootstrap event(s)".

  `replay_into` now returns how many calls actually landed, and a 404 during
  replay is logged at debug as the known gap it is rather than as a failure of
  the run. The claim that the control plane appears in its own graph is
  withdrawn from the module docstring until the entities are really registered
  -- which needs a control-plane BOM seeded and applied the way an
  environment's is.

- fix: Create a DB subnet group per account, working around a Floci bug

  `CreateDBInstance` failed for every environment with
  "InvalidVPCNetworkStateFault: No subnets available for DB subnet group
  default". Floci's `Ec2Service.ensureDefaultResources` seeds a region's default
  VPC and subnets, but guards on a `Set<String> seededRegions` keyed by *region
  alone* while writing into storage namespaced per *account* -- so the first
  account to touch EC2 in a region marks it seeded and every other account is
  skipped, left with no default VPC. Phase 1 made that the common case by
  putting every environment in one Floci.

  Creating our own VPC cannot fix the implicit path, since Floci resolves the
  default VPC by a fixed id (`vpc-default-<region>`) we cannot assign. So
  `provision_resources` now creates a VPC, two subnets and a named DB subnet
  group per account, and both RDS deploys -- the control plane's bootstrap node
  and each environment's BOM entry -- place their instance in it explicitly.

  Best-effort: if the group cannot be ensured the deploy still runs and fails
  with Floci's own error, which says more than one invented here would.

- fix: Stop an orphaned RDS volume from permanently blocking `up`

  The PostgreSQL-compatibility check scanned every `floci-rds-*` volume,
  including ones no instance references. That made the error a dead end: its own
  suggested remedy, `down --purge`, discards Floci's instance records and so
  *orphans* the volume by definition, leaving `up` refusing to start over data
  nothing would ever mount again. Reported from a real install stuck on a
  PostgreSQL 12 volume left over from an old experiment.

  Two fixes. The check now ignores volumes no recorded instance would mount,
  read from Floci's persisted `rds-instances.json` before it starts; an
  unreadable state file treats everything as live, since a wrong "orphan" would
  skip a real incompatibility. And `down --purge` now sweeps `floci-rds-*`
  volumes by name rather than only deleting instances it can derive an
  identifier for -- the volumes most needing removal are precisely those whose
  instance record is already gone.

  The error message also names the volume outright (`docker volume rm <name>`),
  because both remedies it offered can fail to apply: `db upgrade` is wasted
  work on data nobody wants, and `down --purge` may already have run.

- fix: Supply every required dependency role on the environment-db BOM entry

  ms-deployment validates required *roles* when a changeset is applied, not when
  the deploy runs, so `hmd-postgres-rds`'s cloud dependencies had to be declared
  even though the local overlay references none of them. Without them `up`
  failed at Phase A with "For RepoInstance, environment-db, required role,
  rds-loggroup, not provided."

  `base-vpc` is resource-typed, so presence in the map is not enough -- the
  supplied instance is validated against what it actually produces. The core
  RepoClass already declared producing `network.neuronsphere.io/network` for
  repos like hmd-inf-hive-metastore, but hmd-postgres-rds names the `vpc`
  subtype specifically, and producing a parent type does not satisfy a
  requirement for its child. The core now declares both; Docker networking
  substitutes for a VPC locally either way.

  `datadog-lambda` and `rds-loggroup` are name-only roles that nothing validates
  beyond presence -- there is no CloudWatch or Datadog locally.

  Two regression tests read the real manifest: one asserts every required role
  is supplied, the other that any resource-typed role pointed at the core
  instance names a type the core declares producing.

- fix: Create the CDKTF state bucket before the DAG's first CDKTF deploy

  `provision_resources` creates the `hmd.<account>.<region>.tfstate` bucket the
  CDKTF S3 backend uses, and it had been folded into the DAG node that runs
  *after* the Postgres deploy. `tofu init` fails outright against a bucket that
  does not exist, so the control plane's first real deploy could never succeed:
  "Failed to get existing workspaces: S3 bucket does not exist."

  It now runs before the DAG. Nothing in it needs the database: the admin secret
  records `hmd_db` as the host, which is the alias the instance is given once it
  is up.

  A regression test pins the bucket name, which is derived twice -- here and by
  `hmd_lib_cdktf`'s S3Backend inside the projectbuilder container. A divergence
  produces the same "bucket does not exist" error, which reads like a
  provisioning failure rather than a naming one.

- fix: Generate a deploy command `hmd deploy` actually accepts

  The bootstrap DAG's Postgres node invented a positional tool name
  (`hmd deploy --instance-name ... cdktf`). `hmd deploy` has no such positional
  -- its only one is `status`, and it reads `manifest.json`'s `deploy.commands`
  to know which tool to run -- so `up` failed inside projectbuilder with
  "argument command: invalid choice: 'cdktf'".

  The command now mirrors what ms-deployment's `deploy_base.deploy_node`
  emits: repo identity on *global* `hmd` flags ahead of the subcommand
  (`--repo-name`, `--repo-version`, `--hmd-region`), and the instance
  configuration on stdin as a quoted heredoc. Two omissions are deliberate,
  both because ms-deployment does not exist yet while this runs: no
  `--register`, and no `HMD_REPO_INSTANCE_DEPLOYMENT_ID` export. The runner
  records both itself, buffered until the replay.

  The configuration must be passed explicitly rather than left to the manifest:
  its `default_configuration` describes Aurora (`engine_version: 17.9`,
  `instance_type: db.r7g.large`) and would otherwise override the local
  overlay's defaults with values a plain `aws_db_instance` cannot use.
  `engine_version` is read from the image Floci will actually spawn, so the
  declared version cannot drift from the binary that initialises the data
  directory.

  New tests check the generated command against `hmd deploy`'s real argument
  surface -- every flag it uses, and that it contains no positional -- since
  the original bug was inventing a CLI rather than reading it.

- feat: Detect a PostgreSQL major-version bump before it breaks start-up

  Floci recreates an RDS instance's container from the *current* postgres image
  on every start while reusing the instance's volume, so bumping the major
  version in `hmd-postgres-base` leaves the old data directory behind and the
  new binary refuses it. Nothing reported that at the point of the change --
  Floci still calls the instance `available` -- so the first symptom was
  whatever connected next failing, a layer removed from the cause.

  `up` now compares the image's `PG_MAJOR` against the `PG_VERSION` file
  postgres writes into its own data directory. Both are readable without
  starting anything, so the check is cheap and runs before Floci can spawn a
  container that crash-loops. On a mismatch it names the volumes and offers both
  ways out. Anything indeterminate (image not pulled, Docker unavailable, volume
  not yet initialised) yields no mismatch rather than a false alarm, because
  this gates `up`.

  `hmd neuronsphere db upgrade` migrates in place -- Floci looks for the volume
  by the name it derived when it spawned the container, so a differently-named
  copy would be ignored. It dumps with a stock `postgres:<old>-alpine` (the
  configured image cannot read that data directory, which is the whole problem),
  clears the volume so the new image runs `initdb`, and restores. The old data
  is copied to `hmd-pgbackup-<volume>-pg<major>` first and never deleted.

  That backup name deliberately sits outside Floci's `floci-rds-` namespace: a
  backup inside it would be rescanned by the check and, holding the old data
  directory by definition, reported as a mismatch forever -- leaving `up`
  blocked after a migration that had already succeeded.

- feat: Deploy each environment's Postgres as a Floci RDS instance too

  Completes the migration the control plane started. `hmd-postgres-rds` is now
  the first real node of every environment's core changeset, so a
  `database-instance` dependency -- `hmd-database-account`'s above all --
  resolves against a Resource a real deploy produced.

  The hand-seeded `hmd_db` Resource and the `database.neuronsphere.io/postgres`
  entry in `CORE_PRODUCED_DEFINITIONS` are therefore **deleted**, not
  re-pointed. Keeping them would give an environment two producers of the same
  type, and a dependency could resolve to the hand-written record describing a
  container that no longer exists.

  The container is aliased `hmd_db-<slug>` between the two changeset phases --
  after Phase A creates it, before any Phase-B entry addresses it -- and CoreDNS
  maps plain `hmd_db` to it inside the environment's cluster, so cloud charts
  still run unmodified. `down --purge` now deletes the RDS instance and its
  volume in both scopes: the volume deliberately survives a plain restart
  (`FLOCI_STORAGE_PRUNE_VOLUMES_ON_DELETE` is pinned false), which is exactly
  what a purge has to undo.

  The `db` service is gone from the environment compose file, and the pre-DAG
  database wait with it: there is nothing to wait for before the changeset that
  creates the database has run.

- feat: Bootstrap the control plane with a DAG whose last node is ms-deployment

  The control plane's Postgres is now deployed by the real `hmd-postgres-rds`
  RepoClass as a Floci RDS instance, through the same projectbuilder path every
  other deploy takes -- so its `database.neuronsphere.io/postgres` Resource is
  genuinely produced and read from `meta-data/resources_output/`, rather than a
  record hand-seeded to describe a container compose happened to start.

  That is possible because `LocalWorkflowRunner.run` takes a plain ordered list
  of node dicts and never asks the deployment service for them. Its only
  coupling is three callbacks, so a new `tracking=False` buffers them and
  `replay_into()` flushes them once ms-deployment is serving. The control plane
  therefore ends up recorded in the graph it just deployed, which it never was
  before.

  A node may now carry a `handler` callable instead of a deploy script,
  generalising the existing `CORE_REPO_CLASS` no-op special case. The nodes
  after the database (core databases, ms-naming, artifact-lib, ms-deployment)
  use it: they provision what the deployment service needs and so cannot be
  deployed *through* it. Each closure is the step that used to run inline in
  `ensure_control_plane`, unchanged. Moving one onto the projectbuilder path
  later is a per-node change, and the ordering it participates in is already
  right.

- feat: Keep `hmd_db` as the canonical hostname now that Floci owns the container

  Floci names the RDS backend it spawns opaquely
  (`floci-rds-db-<HEX>-<suffix>`) and finds it by label, not by name. Rather
  than rewrite every consumer -- compose peers (the Deployment GUI, Hive
  metastore, Trino, Airflow, Superset), `_psql`, and cloud Helm charts running
  unmodified in k3s -- `ensure_rds_network_alias` gives the container the
  `hmd_db` alias on the NeuronSphere network. Docker refuses to add an alias to
  an existing endpoint, so it disconnects and reconnects; that is safe because
  it runs immediately after creation, before anything has connected, and is
  skipped when the alias is already present.

  This also keeps the port at 5432 rather than routing through Floci's
  7001-7099 RDS proxy -- which matters because that proxy is not re-established
  after a Floci restart.

  The `db` service is gone from the control-plane compose file. The Deployment
  GUI loses its `depends_on`, which it did not really need: its own database
  was created after `compose up` returned even when `db` was a service, so its
  migrate retry loop was always what waited.

  Note the environment compose file still runs a `db` service. The
  per-environment migration to RDS needs its own BOM entry and is not done yet;
  removing the service first would leave an environment with no database.

- feat: Collapse the per-environment Flocis into one multi-account Floci

  An environment used to run its own Floci container alongside its Postgres,
  JanusGraph and k3s cluster -- roughly 1.5-2.5 GB each, the footprint
  limitation `docs/environments.rst` already called out. Floci isolates accounts
  within a single container, resolving which one a request belongs to from the
  SigV4 access key id it is signed with (a 12-digit AKID *is* the account) and
  namespacing every storage-backed service beneath it. So the `floci` service is
  gone from `docker-compose.environment.yml`, and an environment is now an
  account inside the control plane's Floci.

  `FlociTarget` gains `access_key_id`, and that field alone selects the account:
  `env_target()` returns the same endpoint, container and alias as the control
  plane, differing only in the account. Because every caller already went
  through `_get_client`, the existing call sites became account-correct without
  being touched.

  `_get_client` now signs with `target.access_key_id` rather than
  `$AWS_ACCESS_KEY_ID`. An ambient key was harmless while accounts were
  separated by endpoint; now it would silently route an environment's calls into
  whichever account that key names. The same fix is threaded into the two places
  that build credentials by hand: the projectbuilder containers running deploy
  nodes, and the External Secrets operator's chart values (a new
  `_inject_floci_account`, mirroring `_inject_docker_credentials`) -- otherwise
  every environment's operator authenticates as one account and resolves the
  control plane's secrets instead of its own, silently, since the secret names
  are identical across environments.

  Also removed as a consequence: `floci_container`/`floci_alias` as persisted
  registry fields (now derived constants, so stale per-environment values in an
  existing registry fall away on load), the per-environment Floci stream
  listener in nginx, the per-environment Floci health wait, the now-dead
  `_wait_for_container_health`, and the `FLOCI` column in `env list`, which
  pointed at a port nothing listens on. The port-slot layout is deliberately
  unchanged -- `trino_port`/`graph_port`/`spare_port` are offsets from the slot
  base, and slot 0's spare port is the Deployment GUI's published 19003, so
  reclaiming one unused port would move every environment's Trino and the GUI.

- feat: Refuse to start over pre-collapse per-environment Floci state

  That state cannot be migrated: Floci keys persisted records by an account
  prefix whose on-disk format it does not document. Ignoring it would be worse
  than failing -- an environment's Lambdas, gateways, buckets and secrets would
  appear to have vanished while `up` reported success. `up` now names the
  environments involved and points at `down --purge`. Legacy-layout
  environments are exempt: their "own" Floci data dir *is* the control plane's.

- fix: Upgrade Floci 1.7.0 -> 2.0.1

  The only breaking change across the 2.0 boundary is a Step Functions JSONata
  fix, and `stepfunctions` is not in `FLOCI_SERVICES`. 2.0.0 lands several
  things the local platform wants directly: EKS cluster restoration after
  restart, Lambda ARN/function-URL invocations resolving in the owning account,
  API Gateway v2 routing requests to the API-owning account (all three
  load-bearing for multi-account), and RDS matching AWS's real defaults so a
  second `terraform plan` reports no changes.

## 2026-09-01

- fix: Pin Floci at 1.7.0 so a plain `down` preserves the k3s cluster's volume

  Every prior Floci version's `ContainerLifecycleManager` removed the spawned
  `floci-eks-<cluster>` container **and its named volume** whenever the parent
  `floci`/`floci-<env>` container stopped -- so even though a plain `down`
  only stops containers (never removes them) and `stop_k3s_cluster` explicitly
  preserves the k3s cluster's own container, the cluster's datastore was still
  destroyed as a side effect, and every `up` got a new `kube-system` UID.
  `_bootstrap_environment` read that as "the cluster was replaced" and
  redeployed every k8s-backed instance, even on an otherwise-unchanged
  restart.

  Floci 1.7.0 re-adopts a still-recorded cluster's container/volume on
  restart instead of tearing it down, controlled by two new env vars now
  pinned explicitly in all three Floci compose files:
  `FLOCI_SERVICES_EKS_KEEP_RUNNING_ON_SHUTDOWN=false` (the CLI still stops the
  k3s container itself) and `FLOCI_STORAGE_PRUNE_VOLUMES_ON_DELETE=false` (its
  volume is no longer pruned as a side effect). The image tag is now
  `floci/floci:${HMD_LOCAL_FLOCI_VERSION:-1.7.0}`, matching every other
  bundled image's override-var convention, where it was previously hardcoded.

  A `down` then `up` cycle keeps the same `kube-system` UID end to end, so
  `up` now takes the true restart fast-path with no k8s-instance redeploy.
  The Helm-release cross-check that narrows a redeploy to just the
  k8s-backed instances remains as a safety net for the cases where the
  cluster genuinely is replaced (a Docker daemon restart evicting the
  container, a wrapper-image mismatch forcing a recreate, or an environment
  bootstrapped before release tracking existed). `down --purge` is
  unaffected -- it already force-removes the container and volume directly.

- feat: Mint and print an MCP API key when the control plane starts

  The Deployment GUI serves a read-only MCP endpoint at `/mcp/`, and the control
  plane already ran it with `MCP_API_KEYS_ENABLED` -- Okta is not emulated
  locally, so a platform API key is the only credential it accepts. Nothing ever
  created one. `up` therefore brought up an MCP server that answered 401 to every
  caller, and the only way in was knowing to run a management command inside the
  container by hand.

  `environments.ensure_mcp_api_key` now runs at the end of `ensure_control_plane`,
  just before the GUI URL is printed. It waits for the GUI's `/health/` to answer
  200 -- a stronger signal than the container's healthcheck, whose interval is
  30s, and the point at which the container's `until migrate` loop has finished --
  reads back whether MCP came up at all, and execs
  `create_mcp_api_key --if-not-exists` for the local superuser. Only the SHA-256
  of a key is stored, so the plaintext exists for exactly one moment; it is
  printed there, padded and unindented so it does not read as one more status
  line.

  `--if-not-exists` keys off the key's name, so a second `up` prints nothing and
  rotates nothing, and a client configured once keeps working. Every failure mode
  -- an unready GUI, an image predating the command, docker unavailable, MCP
  switched off -- is a warning and never a failed `up`. `MCP_ENABLED` in the
  compose file becomes `${HMD_LOCAL_GUI_MCP_ENABLED:-true}`, one switch for both
  the server and the minting step, and `hmd neuronsphere env status` gains a
  `deployment_gui_mcp` route. The key itself is deliberately not recoverable
  there: `docs/modes.rst` documents minting a replacement under a different name.

- chore: Pin the Deployment GUI at 0.1.74 and ms-deployment at 0.1.848

  `MS_DEPLOYMENT_VERSION` is new: the control plane fell back to the floating
  `stable` tag on a machine with no `$HMD_REPO_HOME/hmd-ms-deployment` checkout,
  so `up` ran whatever was published last. A checked-out working tree and
  `HMD_MS_DEPLOYMENT_VERSION` still win — only the fallback changed.

- refactor: Run the Deployment GUI as a control-plane container instead of a k3s workload

  `hmd-app-neuronsphere` was in the local BOM, so every `up` deployed it through
  the full DAG — CDKTF, Helm into the environment's k3s, the Traefik-as-`alb`
  patch, an ext-secrets dependency, a NodePort, an Ingress-host rewrite, an
  ms-dbaccount round trip and a private-registry image import into containerd —
  before the GUI could serve a page. That chain was blocking `up`.

  The GUI is the control plane's own management surface, not a platform
  workload, so it now runs as the `deployment-gui` service in
  `docker-compose.control-plane.yml` beside `hmd_db` and `floci`, and its
  database is one of `floci_deployer.CORE_DATABASES` (created directly by
  `psql`, like `hmd_ms_naming` and `hmd_ms_deployment`). `hmd_proxy` serves it at
  the same `http://localhost:19003/` as before, now proxying straight to the
  container — no Ingress, so no Host rewrite or `proxy_redirect` pair.

  Nothing of `hmd-app-neuronsphere` is bundled into this CLI any more: its
  `pre_build_artifacts` entry is dropped, since the container runs from the app's
  published image rather than from its Helm chart. The version is a pin
  (`environments.GUI_IMAGE_VERSION`), still overridable with
  `HMD_LOCAL_VERSION_HMD_APP_NEURONSPHERE`.

  `bom_seeder.gui_bom` and both of its BOM appends are gone;
  `HMD_LOCAL_NEURONSPHERE_ENABLE_GUI=false` still opts out, now via a compose
  profile. `gui_port()` lost its `env` argument — the GUI is one control-plane
  singleton serving every environment rather than one instance per environment —
  and `HMD_LOCAL_GUI_HOST_PORT` overrides it. An existing install still carrying
  the old Helm release sees the removed entries as "declared no longer; destroy"
  on the next reconcile.

## 2026-08-31

- fix: Make a non-purge `down` preserve the k3s cluster so `up` takes the restart fast-path

  `stop_environment` deleted the environment's k3s cluster on every `down`, not
  just under `--purge`. Floci's `delete_cluster` drops the cluster's
  `floci-eks-<name>` volume along with it, so the next `up` got a brand-new
  cluster with a new `kube-system` UID — which `_bootstrap_environment` reads as
  "the cluster was replaced since the last bootstrap" and answers by redeploying
  the entire BOM. The documented restart fast-path was therefore never taken.

  A plain `down` now stops the k3s container, stops (rather than removes) the
  containers, and leaves the Docker network in place; `ensure_k3s_cluster`
  restarts a stopped container on the expected image instead of recreating it,
  falling back to a recreate only when the start fails. `--purge` keeps the old
  destructive behavior.

  Floci's `ContainerLifecycleManager` still removes the `floci-eks-<cluster>`
  container and its volume when the environment's `floci-<env>` container stops,
  so the cluster's datastore does not survive a `down` today. What did change is
  that the cluster *record* now persists (`eks-clusters.json` reloads it rather
  than coming back empty), and the redeploy is no longer all-or-nothing:

- fix: Redeploy only the k8s instances when the k3s cluster is replaced

  A mismatched `kube-system` UID forced `_run_full_bootstrap` — the entire BOM —
  even though only the instances deployed onto k3s were actually lost. S3
  buckets, cdktf-to-Floci stacks and Lambdas live in Floci, whose state persists
  across the restart. `up` now reconciles instead, and the release cross-check
  turns exactly the k8s-backed entries into additions. With no recorded release
  information it still falls back to the full bootstrap, since narrowing without
  that record would be a guess.

- fix: Cross-check Helm releases against the deployment graph during reconcile

  ms-deployment records intent and cannot see the cluster, so an instance whose
  release was uninstalled stayed `DEPLOYED` and `up` reported "matches its
  declared state" over a missing workload. The applied-changeset snapshot now
  records the Helm release each entry installed, and `compute_plan` proposes a
  redeploy when that release is gone. Entries that install no release are
  unaffected, and an unreadable cluster is never mistaken for an empty one.

- feat: Add a `get_post_deploy_notices` plugin entry point

  There was no way for an installed local-BOM plugin to surface anything in
  `up`'s "Ready" summary short of hardcoding plugin-specific knowledge into
  this repo — the summary's per-environment block was a fixed list of core
  URLs. `hmd_cli_neuronsphere.get_post_deploy_notices` is a new entry-point
  group, collected the same best-effort way as the existing
  `get_local_bom_entries`/`get_resources` hooks: each installed contributor is
  a callable `(env) -> List[str]`, and a broken contributor logs a warning and
  is skipped rather than breaking `up` for everyone else. `start_neuronsphere_
  extend` prints whatever lines every contributor returns after the
  environment's URL block. First consumer:
  `hmd-cli-plugin-ns-visualization` reports its Superset admin login this way
  instead of requiring a manual `aws secretsmanager get-secret-value` call.

- feat: Expose `floci_deployer.get_client` as a public Floci client factory

  Plugins implementing `get_post_deploy_notices` (or any future entry point)
  need to read their own secrets/resources back from an environment's Floci,
  but the existing `_get_client` is private and has ~20 internal call sites
  not worth touching. `get_client(service, env)` is a thin public wrapper
  around it for exactly this kind of cross-package use.

## 2026-08-28

- fix: Address Floci by its network alias, never the `floci` compose service key

  Docker Compose registers every *service key* as a network alias, so the `floci`
  key shared by the control-plane and environment compose files resolved
  round-robin to both containers. Control-plane API Gateway and Lambda calls
  landed in environment accounts at random, and `hmd neuronsphere up --upgrade`
  on a running platform failed with `Invalid API id specified`. The same
  collision on `db` pointed the Hive metastore at whichever Postgres won the
  coin flip.

  **On upgrade:** the control-plane Floci container is recreated on the next `up`
  (~30s) so its gateway table starts clean. An environment that was bootstrapped
  before this fix also ran its BOM/DAG through the ambiguous route, so its
  deployment records may be inconsistent; `hmd neuronsphere env delete <slug>
  --purge && hmd neuronsphere up` is the clean path. That also destroys the
  environment's Postgres and JanusGraph data, so it is not done automatically.
- fix: Resolve container CLI dynamically instead of hardcoding; improve image caching logic
- feat: Implement host-side staging for local Lambda images and enhance image resolution logic
- feat: Update ingress routing for local environments
- fix: Update version of ext-secrets dependency in manifest.json
- fix: Scope `up --upgrade` image pulls to Extend mode's own compose files

## 2026-08-27

- feat: Add support for additional cdktf file paths in setup configuration
- feat: Add support for cdktf files in external local directory
- fix: Resolve artifact version conflicts and improve error reporting in `up` command
- feat: Add comprehensive tests for nginx router, port validation, and repo version resolution

## 2026-07-28

- test: update local runner fixture to quoted heredoc delimiter

## 2026-07-24

- feat: expose local k3s Trino on host :18080 via nginx stream (no port-forward)
- fix: drop the k3s container and volume on down --purge
- fix: source local DB-secret customer_code from hmd.env, default none
- feat: resolve global-graph via k3s CoreDNS for Trino graph catalog

## 2026-07-22

- feat: local-deploy fixes for analytics-engines bring-up

## 2026-07-21

- refactor: replace strategy-based local-overrides with two-phase BOM/changeset bootstrap

## 2026-07-17

- feat: Enable ext-secrets by default in local NeuronSphere
- refactor: Remove KEDA from hardcoded k3s core-operator install path
- fix: resolve local ClickHouse/otel-collector deploy failures on k3s

## 2026-07-16

- refactor: Update NeuronSphere CLI with plugin architecture and remove deprecated components
- refactor: Remove ClickHouse and Telemetry plugins along with related configurations
- feat: Add local Postgres DB provisioning and registration commands

## 2026-07-14

- feat: complete the local hmd deploy DAG loop (Resource-driven cluster, cdktf-local)
- feat: opt-in local ext-secrets dev-deploy loop (single-Floci branch checkpoint)

## 2026-07-06

- feat: Enhance local k3s deployment for NeuronSphere

## 2026-05-04

- feat: Enhance Docker Compose configurations and introduce customer-derived librarians

## 2026-04-27

- feat: Enhance Floci deployment with local image resolution and API Gateway improvements
- feat: Add Argo plugin and extend mode tests

## 2026-04-22

- feat: replace MiniStack with Floci and add legacy/deploy mode switching

## 2026-04-15

- feat: add MiniStack integration replacing MinIO and DynamoDB plugins

## 2026-03-13

- fix: skip port-in-use warnings for existing NeuronSphere containers

## 2026-03-03

- fix: update version numbers for pre_build_artifacts in manifest.json

## 2026-02-27

- feat: add enabled_by_default to nsplugin.json spec for plugin default state

## 2026-02-25

- fix: update version numbers for airflow, clickhouse, and otel-collector in manifest

## 2026-02-24

- fix: ensure .env-non-dev is packaged and available for superset startup
- feat: add aws-secretsmanager-caching to requirements
- feat: update plugin configurations and add interactive selection for enabling local plugins
- feat: add clean startup/shutdown output with service URL summary
- feat: add port conflict detection for local NeuronSphere startup
- feat: add clickhouse, hive-metastore plugin wrappers and superset pre-build artifact

## 2026-02-20

- feat: update pre-build artifacts to latest versions for consistency
- feat: add telemetry profile seeding from local plugin nsplugin.json

## 2026-02-19

- fix: resolve network and db-init issues for local plugin containers

## 2026-02-18

- fix: telemetry plugin now uses base.py helpers for local plugin support

## 2026-02-17

- feat: add pre-build artifacts for airflow, clickhouse, and otel-collector plugins

## 2026-02-10

- fix: local plugin support for neuronsphere down/restart commands

## 2026-02-06

- feat: auto-generate postgres init containers from nsplugin.json
- fix: use cement minimal_logger to fix namespace field error
- feat: make local plugin discovery explicit via HMD_LOCAL_PLUGINS
- fix: include directory-based skills in package_data
- fix: support directory-based skills in SkillsLoader
- feat: update init-ns-local skill to directory format with manifest analysis
- feat: add configurable plugin support via config_local.json

## 2026-02-05

- feat: add pre_build_artifact for hmd-ms-transform in manifest.json
- feat: add init-plugin command to scaffold local plugin structure
- feat: add validate-plugin command for nsplugin.json validation
- feat: register handler with hmd_cli.controllers entry point
- feat: add local filesystem plugin support from HMD_REPO_HOME
- feat: add AI skills support with init-ns-local skill
- feat: add support for external Docker Compose artifacts from other repos

## 2025-12-16

- fix: add AWS credentials to Jupyter docker-compose configuration

## 2025-10-24

- fix: update python-dotenv version to 1.1.1 in requirements.in

## 2025-08-01

- fix: update DynamoDB local image version and remove unnecessary startup option

## 2025-07-30

- fix: ensure cache directory is created if it doesn't exist and handle missing databases gracefully

## 2025-07-14

- fix: update docker-compose file path and environment variables for transform service

## 2025-05-07

- fix: update bucket name retrieval in start_neuronsphere function

## 2025-04-21

- fix: remove deprecated requirements.txt file

## 2025-03-17

- fix: adds HMD_REPO_HOME and updates docker-compose configurations

## 2025-03-03

- fix: bumps hmd-cli-app version
- fix: fixes some minor initial config bugs

## 2025-02-17

- fix: fixes restarting local cached services

## 2025-01-03

- fix: fixes local encryption key

## 2025-01-02

- fix: fixes quotes

## 2024-12-21

- fix: fixes error on missing query config
- fix: made compose cmd configurable

## 2024-12-19

- feat: adds gozer

## 2024-11-14

- fix: pins PyYAML

## 2024-08-21

- fix: fixes update images

## 2024-08-07

- feat: adds Jaeger to telemetry plugin

## 2024-07-19

- feat: registers services w/ ms-naming on up

## 2024-07-18

- feat: adds restart command

## 2024-07-01

- fix: fixes update-images cmd

## 2024-04-03

- fix: fixes minio plugin

## 2024-03-12

- fix: fixes SECRET_KEY
- fix: temporarily removes prev secret key

## 2024-03-05

- fix: creates missing neuronsphere_default network

## 2024-02-28

- fix: adds missing hadoop env
- fix: adds trino plugin

## 2024-02-16

- feat: adds instance name to local svc yaml
- fix: adds missing service files

## 2024-02-15

- feat: adds telemetry containers

## 2024-02-14

- feat: adds otel collector

## 2024-02-13

- feat:  implements plugin architecture
- feat: adds naming service

## 2024-02-07

- fix: fixes running local services

## 2023-11-14

- fix: removes dependencies on nginx

## 2023-09-08

- feat: configures for airflow img seq tf

## 2023-08-29

- fix: adds correct gremlin config

## 2023-08-15

- fix: adds healthchecks to depends on

## 2023-08-14

- fix: removes print statement

## 2023-08-11

- fix: fixes overwrite conn for airflow scheduler
- fix: adds correct session properties to trino overwrite

## 2023-08-09

- feat: saves addtl local services for restart

## 2023-08-04

- fix: adds missing data files
- feat: connects transform svc to graph and queues

## 2023-08-03

- fix: uses HMD_LOCAL_NS registry

## 2023-07-12

- fix: changes env var for container registry

## 2023-06-09

- fix: adds nginx conf to setup.py

## 2023-06-07

- feat: adds Nginx reverse proxy

## 2023-06-06

- fix: makes superset more configurable

## 2023-06-02

- feat: makes db conns configurable

## 2023-05-10

- feat: adds MinIO container

## 2023-04-25

- fix: fixes missing db_init key
- feat: adds override arguments for starting services
- feat: adds local graph db

## 2023-04-11

- fix: fixes defaulting postgres version
- fix: add quotes to file paths
- fix: typo

## 2023-04-05

- fix: fixes versioned export on superset

## 2023-04-04

- fix: adds back init scripts mount

## 2023-03-07

- fix: removes warnings about unset vars

## 2023-03-06

- fix: adds full hdfs config for metastore

## 2023-03-01

- fix: bumps deps
- fix: fixes postgres init scripts
- fix: adds hive files to package

## 2023-02-28

- fix: switches jupyter img to env var
- feat: updates Trino images to HMD builds

## 2023-02-27

- feat: converts trino img to HMD built one
- fix: removes mounting superset scripts
- fix: removes NB_USER env var
- fix: fixes postgres init scripts dir

## 2023-02-26

- fix: fixes missing HMD_DID
- fix: removes booleans
- fix: fixes remaining file versions
- fix: updates docker-compose version
- fix: fixes docker-compose version of superset

## 2023-02-24

- feat: adds TRINO_BUCKET envvar to transform

## 2023-02-22

- fix: fixes projects mount on jupyter

## 2023-02-21

- fix: fixes default for aws region
- fix: removes --quiet-pull from run

## 2023-02-17

- fix: adds defaults to env vars
- fix: :bug: fixes bug with missing .aws folder

## 2023-02-15

- fix: conditionally copies trino config
- fix: :bug: adds missing package data
- fix: adds trino config
- fix: :bug: fixes missing required dirs

## 2023-02-14

- fix: enables all techs by default
- feat: adds update-images command

## 2023-02-10

- fix: fixes mounting projects

## 2023-02-06

- feat: updates docker-compose ymls

## 2023-01-31

- fix: bumps app and tool versions

## 2023-01-30

- feat: allows specifying local config in meta-data

## 2023-01-27

- fix: :bug: fixes default compose for running ms

## 2023-01-25

- feat: adds mount pkgs opt to run command

## 2022-11-15

- feat: adds run cmd for local svc dev

## 2022-11-14

- fix: bumps superset version

## 2022-10-13

- fix: fixes local transform issue

## 2022-08-18

- feat: adds support for disabling local project service

## 2022-08-16

- fix: fixes diagram location

## 2022-07-21

- feat: adds dag_generators mount and new trino conn

## 2022-07-15

- fix: fixes trino data location

## 2022-07-13

- fix: removes volumes for trino
- fix: makes volumes more portable

## 2022-07-08

- fix: removes unused api key refs

## 2022-07-07

- feat: adds required directory on start

## 2022-07-06

- feat: update transform service
- feat: update transform service

## 2022-07-05

- feat: adds core hmd home files

## 2022-07-01

- feat: added mount for project

## 2022-06-29

- fix: use build_repo
- feat: adds local_transform_projects path to transform compose

## 2022-06-06

- fix: fixes mount target
- feat: adds aws mount to jupyter

## 2022-05-17

- feat: adds git env vars

## 2022-03-31

- feat: tweak mount path
- feat: tweak mount path
- feat: tweak mount path

## 2022-03-30

- fix: fixes env vars
- fix: adds necessary package data

## 2022-03-29

- feat: adds postgres startup scripts to ensure dbs and users are created

## 2022-03-17

- feat: finishes airflow and ms-transform implementation

## 2022-03-15

- feat: updates to new image and config
- fix: fixes datadog config
- feat: adds ns env file

## 2022-03-07

- feat: adds build commands

## 2022-03-04

- feat: runs jupyter server as root to alleviate some permissions issues
- feat: makes airflow dags persistent

## 2022-03-03

- fix: fixes volume generation
- feat: add all dotfiles
- feat: adds env files
- fix: fixes package data paths
- fix: fixes package data to include other resources
- feat: adds superset to edge

## 2022-03-02

- feat: nest airflow folders under transform folder
- feat: adds full transform service to edge

## 2022-03-01

- feat: adds airflow support

## 2022-02-23

- feat: adds project mount

## 2022-02-15

- fix: adds repo version back in
- fix: adds hostname and removes unused variable

## 2022-02-04

- feat: streamlines folder creation

## 2022-02-02

- feat: moves image versions to env vars

## 2022-01-27

- feat: bumps project version

## 2022-01-12

- feat: bumps local project version
- feat: bumps local projects version

## 2021-12-20

- fix: disables xray globally
- fix: fixes datadog issues

## 2021-12-07

- feat: adds hmd_home env var to proxy for future local ns checks

## 2021-12-03

- fix: bump project service version

## 2021-12-01

- feat: bumping image versions to latest

## 2021-11-29

- feat: updates project and postgres image versions

## 2021-11-10

- fix: fixes docker-compose reference
- feat: tuning order of operations
- feat: adds support for local repos not existing
- feat: initial commit of cli

## 2021-11-09

- feat: :tada: generate initial repo structure
