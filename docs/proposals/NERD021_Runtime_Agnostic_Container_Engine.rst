.. NERD021 Runtime Agnostic Container Engine

NERD021 Runtime Agnostic Container Engine
=========================================

.. req:: Reach the container engine the user's own docker CLI reaches
    :id: HMD_CLI_NEURONSPHERE_NERD021
    :status: proposed

    ``nsctl`` shall address the container engine the user's own ``docker`` CLI
    addresses, whichever runtime provides it, and shall refuse -- naming the
    endpoint and where it came from -- rather than pass a preflight and then
    fail partway against a socket nobody selected.

Motivation
----------

``nsctl`` talks to the container engine two ways. ``internal/container`` shells
out to the ``docker`` CLI for inspection-shaped work; ``internal/compose`` uses
the Engine API where parsing CLI output would be fragile. The two resolve the
daemon differently, and only one of them is correct.

The ``docker`` CLI resolves ``DOCKER_HOST``, then ``DOCKER_CONTEXT``, then
``currentContext`` in ``~/.docker/config.json``. The Engine API Go client's
``client.FromEnv`` (``internal/compose/run.go:107``) reads ``DOCKER_HOST`` and
nothing else, and otherwise defaults to ``unix:///var/run/docker.sock``. The
context is never consulted anywhere in the tree.

This is not a gap in support for a new runtime. It is a latent defect that a
vendor-specific symlink has been hiding. On the Docker Desktop Mac this was
written on:

.. code-block:: console

    $ docker context inspect --format '{{.Name}}\t{{.Endpoints.docker.Host}}'
    desktop-linux	unix:///Users/aburg/.docker/run/docker.sock

    $ ls -la /var/run/docker.sock
    lrwxr-xr-x 1 root daemon 36 /var/run/docker.sock -> /Users/aburg/.docker/run/docker.sock

Docker Desktop's own context does **not** name ``/var/run/docker.sock``.
``nsctl``'s Engine API path works here only because Docker Desktop installs a
compatibility symlink at the legacy path. Colima installs no such symlink, so
the same code reaches nothing:

.. code-block:: text

    Validating ports...
    warning: port 19000 (proxy): already in use on this host by something that is not part of the local NeuronSphere
    warning: port 19001 (proxy): already in use on this host by something that is not part of the local NeuronSphere
    Error: creating the neuronsphere_default-9aec22a0 network: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?

All three lines are one cause, and the middle two are the interesting ones: they
are a *reporting* bug the connection bug merely exposed. The ports it warned
about were almost certainly ``nsctl``'s own proxy from a previous run.

The evidence this document is built on
--------------------------------------

Read from the code, not from the step names:

- ``internal/compose/run.go:106-112`` is the **only** Engine API construction
  site in the tree. The surface it feeds is the narrow ``dockerAPI`` interface
  (``run.go:60-95``) plus ``listAPI`` (``ports.go:214``), and ``compose``
  already has a complete fake for both. Injecting a differently-resolved client
  needs no new seam.
- ``internal/container/docker.go`` never sets ``cmd.Env``, so every shell-out
  inherits the user's environment and resolves the context itself. That half is
  already runtime-agnostic, and must stay that way (SPEC011).
- ``internal/compose/run.go:78-92`` is the precedent this document follows.
  ``PullImage`` deliberately shells out to ``docker pull`` because "the daemon
  never reads ``~/.docker/config.json``, which is the CLI's job". Endpoint
  resolution is the same class of fact: the CLI owns it, so ask the CLI.
- ``controlplane.go:202`` gates ``Start`` on ``container.New().Available(ctx)``
  -- the *CLI* path. On a machine whose context points elsewhere it returns a
  false all-clear, and the first real symptom arrives four steps later at
  ``EnsureNetwork`` (``run.go:168-177``).
- ``OurPorts`` (``internal/compose/ports.go:223-246``) swallows a
  ``ContainerList`` failure with ``continue`` and returns an empty set. An
  empty set means "none of these ports are ours", so every port the platform
  itself publishes is reported as a foreign process.
- ``compose.CheckOwnership`` (``internal/compose/ownership.go:54``) runs through
  the same client. Its per-container "unknown is not a conflict" doctrine is
  right; what was missing was the precondition that the engine is reachable at
  all. Without it the guard against recreating another ``HMD_HOME``'s
  containers degrades silently, which is the failure mode ``controlplane.go``
  already records as having happened once.

Measured on the same machine, because it decides SPEC002:

.. code-block:: console

    $ DOCKER_HOST=unix:///tmp/fake.sock docker context inspect --format '{{.Name}}\t{{.Endpoints.docker.Host}}'
    default	unix:///tmp/fake.sock

    $ time docker context inspect --format '{{.Endpoints.docker.Host}}'
    0.013 total

One ``docker context inspect`` implements the entire precedence chain by
construction, including the ``DOCKER_HOST`` case, and costs 13 ms -- the same
order as the ``docker version`` call ``Available`` already makes.

And a second defect, found while checking the first, which must ship with it:

- ``runner.Config.WorkDir`` (``internal/runner/runner.go:130-137``) is **never
  assigned anywhere**. It is the parent of the generated deploy script
  (``runner.go:229``) and the overlay workspace (``workspace.go:154``), both of
  which are bind-mounted (``runner.go:348-356``). Its own comment states the
  rule it was never given a value to honour: these "are bind-mount sources
  resolved by the *host* daemon ... they have to name a directory that exists
  at the same absolute path on both sides." Empty means the system temp dir,
  which on macOS is under ``/var/folders``.
- ``internal/k3s/kube.go:176`` writes the container-facing kubeconfig with
  ``os.CreateTemp("", ...)`` -- system temp again -- and
  ``internal/environment/apply.go`` feeds it to ``runner.Config.Kubeconfig``,
  bind-mounted at ``runner.go:341``.

A bind-mount source the daemon cannot see is created by the daemon as an empty
directory. That is exactly the scar already written down at
``internal/k3s/kube.go:140-148``: ``IsADirectoryError: [Errno 21] Is a
directory: '/root/.kube/config'``. Fixing only the socket gets a Colima user
past ``EnsureNetwork`` and straight into that, every ``env apply``, which would
read as "the fix did not work".

Scope
-----

In scope: endpoint resolution and the one client built from it; the preflight
gate; honest port reporting; ``nsctl doctor``; the temp-directory default and
bind-mount visibility; the docs that claim Docker is the only prerequisite.

Out of scope: supporting an engine that offers neither the Docker Engine API nor
a ``docker`` CLI (Podman's native API, containerd) -- a different document;
adding ``github.com/docker/cli`` as a dependency (SPEC002); changing
``internal/container``'s shell-out doctrine; teaching ``nsctl`` to *start* a
runtime, which is the user's business; the Python front end, which keeps its own
behaviour.

Reference: what already exists
------------------------------

- ``internal/compose/run.go:106-112`` -- ``NewRunner``, the single Engine API
  construction site; ``:60-95`` the ``dockerAPI`` interface; ``:168-177``
  ``EnsureNetwork``.
- ``internal/container/docker.go:55-64`` -- ``Available``, the CLI probe, and
  its two callers (``controlplane.go:202``, ``reset.go:47``).
- ``internal/controlplane/controlplane.go:195-308`` -- the preflight sequence:
  Docker, hosts entries, legacy Floci state, ownership, then the three port
  checks.
- ``internal/compose/ports.go:223-246`` -- ``OurPorts`` and its swallowed error.
- ``internal/runner/runner.go:130-137`` -- ``Config.WorkDir`` and the bind-mount
  rule it states.
- ``internal/k3s/kube.go:140-148`` -- the recorded cost of an unusable
  kubeconfig bind mount.
- ``.goreleaser.yaml:93-94`` -- the Homebrew cask already tells Colima users to
  run ``colima start``. This document makes the rest of ``nsctl`` consistent
  with a promise packaging already made.

.. spec:: One resolved endpoint, the docker CLI's own
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC001
    :status: proposed

    A new package ``internal/dockerhost`` shall resolve the Engine endpoint
    exactly as the ``docker`` CLI does, and every Engine API client shall be
    built from that endpoint. The precedence -- ``DOCKER_HOST``, then
    ``DOCKER_CONTEXT``, then ``currentContext``, then the platform default
    socket -- shall be obtained by asking the CLI, not by reimplementing it.

    The resolved value shall carry where it came from, not only what it is: the
    failure this package exists for is one where the host looked plausible and
    the route to it was wrong, so every message shall be able to name both.

    Resolution shall happen at most once per process.

.. spec:: No new dependency, and no second implementation of the context store
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC002
    :status: proposed

    Resolution shall shell out to ``docker context inspect``. It shall not add
    ``github.com/docker/cli``, and it shall not parse
    ``~/.docker/contexts/meta/<sha256>/meta.json``.

    The ``docker`` CLI is already a hard prerequisite: ``Available`` refuses
    without it, ``PullImage`` needs it for credential helpers, the deploy runner
    runs ``docker run`` and every k3s step runs ``docker exec``. Shelling out
    adds no requirement that is not already there, and one call implements the
    whole chain *by definition* -- including ``DOCKER_HOST``, which reports as
    context ``default`` with the environment's value, and ``DOCKER_CONFIG``
    relocation, which a hand-written resolver would have to re-derive and would
    drift from.

    The context store layout is easy to parse and was confirmed while writing
    this. It is rejected anyway: it duplicates a format ``nsctl`` does not own,
    and its only advantage -- working when the ``docker`` binary is absent -- is
    worth nothing here, because that case is already a refusal one step earlier.

.. spec:: The preflight gate is the Engine API, not the CLI
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC003
    :status: proposed

    ``container.New().Available(ctx)`` shall remain -- it is what catches "no
    docker installed" -- and shall be followed by a reachability check against
    the resolved endpoint through the Engine API. A start shall not proceed past
    the preflight on an endpoint ``nsctl`` cannot reach.

    The command that proves the endpoint shall build its runner from the same
    resolved endpoint, so the gate and the work can never address different
    daemons.

.. spec:: An endpoint nsctl cannot dial is refused before anything is created
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC004
    :status: proposed

    ``ssh://`` shall be refused by name, with the remedy, because reaching it
    needs the CLI's own connection helper, which lives in the dependency
    SPEC002 declines. ``tcp://`` shall honour ``DOCKER_TLS_VERIFY`` and
    ``DOCKER_CERT_PATH`` and the context's own ``SkipTLSVerify``. ``unix://``
    and ``npipe://`` shall be passed through unchanged.

    Today ``ssh://`` is refused accidentally and late, by dialling the SSH port
    and speaking HTTP at it. A named refusal is the whole improvement.

.. spec:: Port reporting says when it does not know
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC005
    :status: proposed

    ``OurPorts`` shall report whether it could ask the engine at all, and the
    caller shall say once that its port findings are unreliable rather than
    print warnings that name ports as foreign when it has no way to tell.

    The empty set stays the safe answer; what changes is that "I could not ask"
    stops being spelled the same way as "none of them are ours".

.. spec:: Every bind-mount source lives where the daemon can see it
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC006
    :status: proposed

    ``nsctl``'s own temporary material that is bind-mounted -- the generated
    deploy script, the overlay workspace, and the container-facing kubeconfig --
    shall be created under ``$HMD_HOME``, not the system temp directory.

    ``HMD_HOME`` is a path the user already chose and that every runtime shares
    by default; ``/var/folders`` is shared by Docker Desktop and by
    approximately nothing else. ``runner.Config.WorkDir`` shall be given a
    value, and its comment corrected: "empty means the system temp dir, which is
    right for the CLI" was true only under Docker Desktop.

    The ``MkdirTemp`` at ``internal/k3s/kube.go:82`` shall **not** move: it is
    ``docker cp``'d, which streams through the API and needs no visibility. It
    is named here so the next reader does not "fix" it.

.. spec:: A VM-backed daemon is detected and its mount rule stated
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC007
    :status: proposed

    When the daemon's ``OSType`` differs from ``runtime.GOOS``, the engine runs
    in a virtual machine and a host path is visible to it only if it was shared
    into that VM. ``HMD_HOME``, ``HMD_REPO_HOME`` and the work directory shall
    be checked against that rule and warned about by path.

    The test shall be "a different operating system", never a runtime name.
    Docker Desktop, Colima, Rancher Desktop and a remote Linux engine are all a
    Linux daemon under a macOS or Windows CLI and all share the rule.

.. spec:: Daemon capacity is reported, and a small one warns
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC008
    :status: proposed

    The engine's CPU and memory shall be read and a warning issued below a
    threshold, naming the numbers and a runtime-appropriate remedy.

    Warn only, never refuse. The thresholds are a proposal, not a measurement,
    and a number nobody has measured must not be able to stop a start. The
    acceptance run sets them.

.. spec:: nsctl doctor
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC009
    :status: proposed

    A read-only ``nsctl doctor`` shall print what ``nsctl`` resolved and what
    the daemon reports, and shall exit ``2`` when something needs fixing and
    ``0`` otherwise, including when checks merely warn. It shall create, start,
    pull and remove nothing.

    It shall share its checks with the start preflight, so the gate and the
    diagnostic cannot drift. It shall also compare the endpoint the ``docker``
    CLI resolves with the one ``nsctl`` resolved and warn when they differ:
    they can only differ through a bug here, and this whole document exists
    because nobody could see that they had.

.. spec:: nsctl never overrides the user's endpoint for child processes
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC010
    :status: proposed

    Shelled-out ``docker`` commands and the containers ``nsctl`` runs shall keep
    inheriting the user's environment. ``nsctl`` shall not inject
    ``DOCKER_HOST`` into them.

    The CLI already resolves correctly, and injecting would break the
    ``ssh://`` and credential-helper flows it handles and ``nsctl`` does not.

.. spec:: The docker.sock bind mounts are correct and stay
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC011
    :status: proposed

    The ``/var/run/docker.sock`` bind mounts at ``internal/runner/runner.go:340``
    and ``:421`` and in the control-plane compose file shall not change.

    The mount *source* is resolved by the daemon, so under a VM-backed runtime
    it names the socket inside the VM, where dockerd's socket genuinely is.
    ``runner.go:419-420`` already says this; it is restated here with the VM
    case named so it is not re-litigated. The one exception, a rootless daemon
    whose socket is under ``/run/user/<uid>``, shall be a ``doctor`` warning
    rather than a code change.

.. spec:: The kubeconfig repoint and the hosts check are unchanged
    :id: HMD_CLI_NEURONSPHERE_NERD021_SPEC012
    :status: proposed

    ``PointKubeconfigAtHost``'s ``https://127.0.0.1:<hostPort>``
    (``internal/floci/k3s.go:387-409``) shall not change: it names a *published*
    port, and every engine in scope forwards published ports to host loopback.
    ``internal/k3s/kube.go:166``'s ``https://<container>:6443`` is in-network and
    runtime-independent.

    ``CheckHostsEntries`` (``controlplane.go:119-141``) shall not change: it
    resolves names on the host via ``net.LookupIP`` and nothing about it is
    runtime-sensitive.

    Both are stated so the next reader does not have to re-derive that they are
    already correct.

Risks
-----

- The capacity thresholds in SPEC008 are unmeasured, which is why they only
  warn.
- SPEC004 turns an accidental ``ssh://`` failure into an explicit refusal. A
  user relying on the shell-out-only commands (``env status``) is unaffected,
  because the gate runs only where an Engine API connection is genuinely
  required.
- Resolution is memoized per process (SPEC001), so a user who changes context
  mid-process keeps the old endpoint until the next command. Accepted.

Open questions
--------------

Answered by the acceptance run, not here:

- Colima's actual default mount set and its rootless socket path.
- What ``OperatingSystem``/``OSType`` each engine reports, which only affects
  the text of the SPEC007 warning, not its logic.
- Whether Colima's port forwarder handles the ``19000-19079`` band without lag.
- The right numbers for SPEC008.

The acceptance run
------------------

Not yet done. Until this section records one, treat every SPEC as verified by
unit tests only -- and SPEC007, SPEC008 and SPEC011's rootless case as not
verified at all, since no Colima machine was available where this was written.

The run shall cover, on a Colima Mac:

.. code-block:: console

    $ colima start --cpu 4 --memory 12 --disk 100
    $ docker context use colima
    $ nsctl doctor
    $ nsctl control-plane start
    $ nsctl env start <slug>
    $ nsctl env apply

``control-plane start`` must connect and must not warn about ports that are its
own; ``env apply`` is what exercises SPEC006, and is the step that would
otherwise reproduce the ``kube.go:140-148`` scar.

And on a Docker Desktop machine, both refusals: ``DOCKER_HOST`` unset (which
must behave exactly as before), and ``DOCKER_HOST`` pointing at a dead socket
(which must be refused by the preflight, naming the endpoint and its source,
rather than failing at ``EnsureNetwork``).
