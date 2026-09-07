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
curl -fsSL https://raw.githubusercontent.com/HMDLabs/hmd-cli-neuronsphere/main/install.sh | sh
```

Then:

```shell
export HMD_HOME=~/hmd          # never defaulted; nsctl refuses without it
nsctl control-plane start      # the shared control plane, one per HMD_HOME
nsctl env add dev
nsctl env start dev
```

A new environment is **empty but usable** — a k3s cluster, a Postgres, and the
External Secrets operator a cloud chart's secrets resolve through. Airflow,
Argo, Trino, Superset, transform and your own services are RepoClasses you add,
not things that arrive by default.

Full guide: [`docs/nsctl.rst`](docs/nsctl.rst).

### What nsctl does not do yet

Both front ends work against one `HMD_HOME`, so reaching for the Python CLI for
any of these costs nothing:

- **The parity harness covers `env start`, `env stop`, `status` and the
  registry.** Every other verb's parity is still checked by hand.
- **Plugin-contributed workloads need `nsctl repo import` once** per
  environment that was brought up with `hmd`.
- **The DAG runner is opt-in** and its first start builds a local image.

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

`hmd build` builds the Python package. The Go binary is not wired into BACON;
`make` and GoReleaser own it.

## Documentation

| Document | What it covers |
|---|---|
| [`docs/nsctl.rst`](docs/nsctl.rst) | The Go CLI: installing, commands, the environment manifest, the DAG runner |
| [`docs/readme.rst`](docs/readme.rst) | The Python CLI: host setup, configuration, included services |
| [`docs/modes.rst`](docs/modes.rst) | Extend mode and Platform mode |
| [`docs/environments.rst`](docs/environments.rst) | Multiple local environments on one machine |
| [`docs/proposals/`](docs/proposals/) | NERDs — the designs behind all of the above |
