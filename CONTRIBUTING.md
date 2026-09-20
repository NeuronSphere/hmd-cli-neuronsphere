# Contributing

Thanks for helping with nsctl and the local NeuronSphere control plane.

## Licence of contributions

This repository is licensed under the Apache License, Version 2.0 (see
`LICENSE.txt`), and contributions are accepted under the same licence. There
is no Contributor License Agreement. Instead we use the Developer Certificate
of Origin (https://developercertificate.org/): by adding a `Signed-off-by`
line to each commit you certify that you wrote the change or otherwise have
the right to submit it under Apache 2.0.

    git commit -s -m "feat(nsctl): ..."

Commit messages follow Conventional Commits; the pre-commit hooks enforce it.

## The licence boundary

`nsctl` embeds the *deploy descriptors* of several repo classes
(`meta-data/`, `src/cdktf/`, `src/helm/`, `src/local/`, `src/opa-bundles/`),
all Apache 2.0. Three of those repo classes -- `hmd-ms-deployment`,
`hmd-ms-librarian`, `hmd-app-neuronsphere` -- keep their service code
(`src/python/`, `src/typescript/`, `src/docker/`) under the Business Source
License 1.1. `tools/repopack` never packs those directories and
`internal/bundled` has a test that fails if one gets in. Keep it that way:
the binary must stay a single-licence artifact. See `docs/licensing.rst`.
