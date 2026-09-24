# nsctl

`nsctl` runs a local NeuronSphere control plane and reproducible environments from a single Go binary. It starts the substrate your deployments need—rather than an opaque all-in-one development stack—then reconciles the workloads your repository declares.

## Prerequisites

`nsctl` and local development with it are free and need only a container engine
your `docker` CLI can reach. Docker Desktop, Colima, OrbStack, Rancher Desktop
and a plain Linux daemon all work: `nsctl` resolves the engine exactly as
`docker` does, from `DOCKER_HOST`, `DOCKER_CONTEXT` or your current
`docker context`. Commands that talk to a hosted NeuronSphere (the cloud
Artifact Librarian, published-version queries, cloud BOM inspection or import)
connect to your organisation's NeuronSphere cloud tenant; see
[product availability and licensing](docs/licensing.rst).

The engine must be running, and `nsctl doctor` reports what it resolved and
whether anything needs fixing. Two things are worth knowing on macOS, where the
engine runs inside a VM:

- Give it enough room. The local platform runs Floci, Postgres, a graph and a
  k3s cluster; below 4 CPUs and 8 GiB it starts slowly and services may be
  OOM-killed. On Colima: `colima start --cpu 4 --memory 12 --disk 100`.
- Keep `HMD_HOME` and your repository checkouts under your home directory.
  `nsctl` bind-mounts them, and a path the VM does not share mounts as an empty
  directory rather than failing.

Starting a platform and deploying to it need nothing else configured. `nsctl`
needs no entry in `/etc/hosts` and never asks for `sudo`: the host names Floci
stamps into its URLs are dialled on loopback by `nsctl` itself, and the ports
it publishes are chosen around whatever else is already running.

Opening things by name is one step, once. A user interface, the identity
provider and the package registry are all reached at
`*.local.neuronsphere.io`, and `nsctl dns install` prints the single command
that makes every name under it resolve -- including names not yet deployed.
`127.0.0.1 neuronsphere neuronsphere-workload` in `/etc/hosts` remains the
older, narrower alternative.

Native Windows is not supported.

## Install and start

Install with Homebrew:

```shell
brew install neuronsphere/tap/nsctl
```

or use the release installer on macOS or Linux:

```shell
curl -fsSL https://raw.githubusercontent.com/neuronsphere/hmd-cli-neuronsphere/main/install.sh | sh
```

From a checkout, `make install` builds and installs the binary. Then let the
guided first run take it from there:

```shell
nsctl quickstart
```

It runs the host checks, settles `HMD_HOME`, starts your first environment and
offers to adopt your own repository, naming every command before it runs it. It
needs a terminal; with stdin closed it prints the sequence and runs nothing.

To do it by hand, choose an explicit home and start your first environment:

```shell
export HMD_HOME="$HOME/hmd"
nsctl env start
```

`env start` brings up the control plane and creates the first environment when needed. Its useful lifecycle companions are:

```shell
nsctl env status
nsctl env stop
nsctl env plan
nsctl env apply
nsctl env purge
```

New environments contain a cluster, database, and required operators. Add application workloads by adopting a repository or importing RepoClasses; they are intentionally not installed by default.

To add your own checkout, follow
[Create a BACON manifest and add your repository](docs/tutorials/create-repository-manifest.rst).
It includes a complete manifest, deploy script, validation commands, and both
ways to register the repository locally. No account is needed.

To add a published set of RepoClasses in one go, add a stack -- free from a
public registry namespace, no tenant or token needed:

```shell
nsctl stack versions observability   # does this reference resolve for you?
nsctl stack add observability
nsctl env apply
```

`observability` is an illustrative name. Which stacks exist is decided by what a
registry serves, not by anything `nsctl` carries.

`nsctl` can also be extended with CLI plugins (`nsctl plugin install <name>`),
executables that add a top-level noun. See
[Use stacks](docs/how-to/use-stacks.rst) and
[Install and write CLI plugins](docs/how-to/install-cli-plugins.rst).

## Documentation

The hosted documentation is published at https://neuronsphere.github.io/hmd-cli-neuronsphere/. Start with the [first-environment tutorial](docs/tutorials/first-environment.rst), then see the [repository-adoption tutorial](docs/tutorials/adopt-repository.rst), [how-to guides](docs/how-to/), and generated [command reference](docs/reference/commands.rst).

The Python `hmd neuronsphere` CLI shares the local registry and manifests. It remains supported for plugin-driven workflows; see the compatibility explanation in the hosted documentation. Design proposals remain available under [`docs/proposals/`](docs/proposals/) but are not the new-user path.

## Development

```shell
make check
make docs-reference-check
make docs-reference
```

`make docs-reference` regenerates the Cobra-derived CLI reference. Documentation builds use Bartleby and publish to GitHub Pages from `main`.
