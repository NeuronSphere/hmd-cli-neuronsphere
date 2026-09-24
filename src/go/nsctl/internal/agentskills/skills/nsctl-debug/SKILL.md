---
name: nsctl-debug
description: Diagnose a local NeuronSphere problem from status, plans, and logs without destructive repair. Use when a local environment, control plane, RepoClass, or deployment has failed or behaves unexpectedly.
---

# Diagnose before changing state

Gather the smallest useful evidence: the user's error, `nsctl` version,
environment state, deployment plan or status, and relevant logs. Separate
observed facts from likely causes and name the command or evidence behind each
conclusion.

Do not purge an environment, tear down the control plane, change credentials,
or modify manifests merely to test a hypothesis. Present those actions as
explicit options with their effect and obtain confirmation before running them.

# A local failure is never an authentication failure

No local command sends a credential. The local deployment-service and librarian
clients are anonymous by construction, deploy containers carry no token, and
local services authorize nothing. So `nsctl login`, `nsctl logout` and
`nsctl whoami` are neither diagnostic steps nor repairs here: never propose
signing in to fix a local environment, deploy or service, and read "not signed
in" as the ordinary state rather than as a finding. Sign-in matters only for a
hosted NeuronSphere tenant or a private registry, and those paths say so in the
error they raise.

# Start with nsctl doctor

`nsctl doctor` is read-only and reports the host, the container engine and each
environment's substrate. Run it before forming a hypothesis.

A service that returns a bare `5xx` through `http://localhost/<env>/<service>/`
is almost always a stale service image, not a broken request: a healthy service
answers `404` there, because it registers only `/api/...` and `/apiop/...`.
`nsctl doctor` names the deployed version and the repair. The usual repair is
`nsctl env start <env>`, which redeploys the substrate at the version the
installed `nsctl` resolves -- `nsctl env apply` alone does not refresh an
environment that an older `nsctl` started.

# nsctl-command: version
# nsctl-command: doctor
# nsctl-command: env
# nsctl-command: control-plane
# nsctl-command: repoclass validate
