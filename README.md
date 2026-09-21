# nsctl

`nsctl` runs a local NeuronSphere control plane and reproducible environments from a single Go binary. It starts the substrate your deployments need—rather than an opaque all-in-one development stack—then reconciles the workloads your repository declares.

## Prerequisites

Local development with `nsctl` is free. **All NeuronSphere cloud features
require paid NeuronSphere**, including cloud Artifact Librarian access,
published-version queries, and cloud BOM inspection or import. These features
also need access to your organisation's tenant; installing the CLI does not
provide it. See [product availability and licensing](docs/licensing.rst).

Docker must be running. On macOS or Linux (including WSL2), add the local host names once:

```shell
sudo sh -c 'echo "127.0.0.1 neuronsphere neuronsphere-workload" >> /etc/hosts'
```

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

From a checkout, `make install` builds and installs the binary. Then choose an explicit home and start your first environment:

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
ways to register the repository locally. No paid cloud account is needed.

To add a published set of RepoClasses in one go, add a stack -- free from a
public registry namespace, no tenant or token needed:

```shell
nsctl stack add observability
nsctl env apply
```

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
