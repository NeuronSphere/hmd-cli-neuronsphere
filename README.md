# hmd-cli-neuronsphere

The local NeuronSphere: a control plane and the environment substrate your
deployments sit on, running on your machine with Docker as the only
prerequisite.

This repository ships two front ends to the same platform. They read and write
the same environment registry, address the same containers, and can be used
against the same `HMD_HOME` in either order.

## nsctl — start here

`nsctl` is a single Go binary. It needs Docker and nothing else — no Python
environment, no `hmd` install, no plugin packages.

```shell
brew install neuronsphere/tap/nsctl
```

or, on macOS and Linux:

```shell
curl -fsSL https://raw.githubusercontent.com/neuronsphere/hmd-cli-neuronsphere/main/install.sh | sh
```

Then:

```shell
export HMD_HOME=~/hmd          # never defaulted; nsctl refuses without it
nsctl env start                # control plane, then an environment
```

That is the whole first run. `env start` brings up the control plane if it is
down, and on an `HMD_HOME` with nothing registered it registers the first
environment itself — there is no separate `env add` step to discover. Name it if
you would rather: `nsctl env start dev`. Once an environment exists, adding
another is explicit, with `nsctl env add`.

A new environment is **empty but usable** — a k3s cluster, a Postgres, and the
External Secrets operator a cloud chart's secrets resolve through. Airflow,
Argo, Trino, Superset, transform and your own services are RepoClasses you add,
not things that arrive by default.

Two things the host has to provide, both of which `nsctl` checks before it does
anything: Docker, and `neuronsphere` plus `neuronsphere-workload` resolving to
loopback. The second is one line in `/etc/hosts`, and the refusal prints it.

Full guide: [`docs/nsctl.rst`](docs/nsctl.rst).

### What nsctl does not do yet

Both front ends work against one `HMD_HOME`, so reaching for the Python CLI for
any of these costs nothing:

- **The parity harness covers `env start`, `env stop`, `status`, `env purge` and
  the registry.** Every other verb's parity is still checked by hand, and one
  test inside it is opt-in and unproven: reading an environment
  `hmd neuronsphere up` created, which deploys its whole default BOM and took
  over 45 minutes against `nsctl env start`'s three and a half.
- **`nsctl env purge` also unregisters the environment; `hmd neuronsphere down
  --purge --env` does not.** Both destroy the same resources. Only one leaves
  the name behind for another `up`.
- **An environment not named `local` needs a projectbuilder image carrying
  hmd-lib-cdktf's 2026-09-08 fix.** Before it, path-style S3 addressing was
  gated on the environment's *name* while ms-deployment passes its slug, so any
  other name failed its first CDKTF node. `nsctl` warns until the image has it.
- **Only one control plane can run at a time per machine.** The five
  control-plane services pin `container_name`, so those names are global while
  the network, compose project and Floci data directory are namespaced per
  `HMD_HOME`. Two homes can take turns — `nsctl control-plane stop --home <the
  other one>` releases the names — but they cannot both be up.
- **nsctl detects a PostgreSQL major-version bump; it does not migrate one.**
  Floci reuses an RDS instance's volume across starts, so a data directory
  written by an older major makes the new binary refuse it. `nsctl` checks
  before it starts anything and names both remedies; the migration itself is
  `hmd neuronsphere db upgrade`, which works because both front ends address the
  same `HMD_HOME`.
- **Plugin-contributed workloads need `nsctl repo import` once** per
  environment that was brought up with `hmd`.
- **The DAG runner is opt-in** and its first start builds a local image.
- **The local identity provider is opt-in.** `HMD_LOCAL_NEURONSPHERE_ENABLE_AUTH=true`
  adds a mock Okta to the control plane, so Rego policies and group-to-role
  mapping can be tested against realistic tokens instead of only in the cloud.
  `nsctl authd token --group '...' --claim k=v` mints one. It does not yet
  satisfy `hmd-lib-auth.verify_token`, which demands an `https://` issuer, so
  the OPA authorizer end to end still needs a certificate nothing here issues.

## hmd neuronsphere — the Python CLI

The original front end, and still fully supported. It resolves workloads from
whichever plugin packages are pip-installed, which is what you want if your
project depends on that.

```shell
hmd neuronsphere up
```

See [`docs/readme.rst`](docs/readme.rst) for host setup and configuration, and
[`docs/modes.rst`](docs/modes.rst) for Extend versus Platform mode.

## Development

```shell
make build        # src/go/nsctl/build/nsctl
make check        # gofmt, vet, unit tests
make test-race    # unit tests under the race detector
make test-cli     # the contract suite; needs no Docker
make image        # the DAG-runner image, from the working tree
```

This repository has no BACON build command: the Python package is not
published by the pipeline any more, and the Go binary is built and released by
`make` and GoReleaser from GitHub Actions on every push to `main`, tagged with
a build number from hmd-ms-projects like every other NeuronSphere artifact.
`meta-data/manifest.json` still pins the repo trees the binary embeds as
`pre_build_artifacts`.

## Documentation

| Document | What it covers |
|---|---|
| [`docs/nsctl.rst`](docs/nsctl.rst) | The Go CLI: installing, commands, the environment manifest, the DAG runner |
| [`docs/readme.rst`](docs/readme.rst) | The Python CLI: host setup, configuration, included services |
| [`docs/modes.rst`](docs/modes.rst) | Extend mode and Platform mode |
| [`docs/environments.rst`](docs/environments.rst) | Multiple local environments on one machine |
| [`docs/proposals/`](docs/proposals/) | NERDs — the designs behind all of the above |

## Licence

`nsctl` and this repository are Apache 2.0. The three services it runs —
the Deployment engine, the Artifact Librarian and the NeuronSphere GUI — are
Business Source License 1.1 and become Apache 2.0 four years after each
release. Local, evaluation and development use is free at any scale; see
[`docs/licensing.rst`](docs/licensing.rst) for what production use requires,
and [`TRADEMARKS.md`](TRADEMARKS.md) for the names.
