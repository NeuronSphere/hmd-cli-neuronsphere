.. NERD022 Rootless Container Engines

NERD022 Rootless Container Engines
===================================

.. req:: Drive a rootless container engine
    :id: HMD_CLI_NEURONSPHERE_NERD022
    :status: proposed

    ``nsctl`` shall run the local platform against a rootless container
    engine, by deriving every docker-socket bind-mount source from the
    endpoint it resolved rather than naming ``/var/run/docker.sock``, and
    shall say plainly when it cannot.

NERD021 made ``nsctl`` address whatever engine the user's own ``docker`` CLI
addresses, and it reaches a rootless daemon correctly today: the endpoint
resolves, the Engine API answers, and ``nsctl doctor`` identifies the engine as
rootless and warns. SPEC011 of that document decided the rootless socket would
be a warning rather than a code change, on the reasoning that the path is right
for every rootful engine and ``nsctl`` cannot know what a given rootless daemon
put its socket at.

The warning is not sufficient, and this is measured rather than argued. Run
against ``docker:27-dind-rootless`` on 2026-09-22:

.. code-block:: text

    daemon           uid 1000, Alpine Linux v3.21, Docker 27.5.1
    SecurityOptions  name=seccomp,profile=builtin / name=rootless / name=cgroupns
    socket           /run/user/1000/docker.sock
    /var/run/docker.sock   does not exist on the daemon at all
    DockerRootDir    /home/rootless/.local/share/docker
    CgroupDriver     none          CgroupVersion 2

and mounting each candidate source into a container on that engine:

.. code-block:: text

    -v /var/run/docker.sock:/var/run/docker.sock        -> an empty DIRECTORY
    -v /run/user/1000/docker.sock:/var/run/docker.sock  -> works: server 27.5.1

A container handed the hardcoded source does not fail to find the socket. The
daemon invents a directory at the missing source, exactly as it does for a
missing kubeconfig, and every call through it fails later and somewhere else.

What makes this fatal rather than degrading is *which* container. The
``floci`` service mounts that path
(``internal/bundled/services/docker-compose.control-plane.yml``), and Floci
spawns the whole substrate through it -- RDS, Neptune, the k3s cluster. On a
rootless engine Floci starts, holds a directory where its socket should be, and
can spawn nothing. Everything else is downstream, so the platform does not get
as far as the deploy nodes that carry the same defect at
``internal/runner/runner.go`` (the native and foreign invocations both).

.. spec:: Derive the deploy node's socket source from the resolved endpoint
    :id: HMD_CLI_NEURONSPHERE_NERD022_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD022
    :status: proposed

    Where ``runner`` mounts the engine socket into a deploy node -- the native
    invocation and the foreign one -- the *source* shall come from the resolved
    endpoint when that endpoint names a unix socket, and the *target* shall
    remain ``/var/run/docker.sock`` so that nothing inside the image has to
    change.

    ``runner.Config`` does not carry the endpoint today; threading it there is
    the substance of this spec. It is the same seam NERD021 SPEC003 opened when
    it made the proven endpoint travel from the preflight to the compose
    runner, extended one step further.

    A source that is not an existing socket shall not be mounted at all, on the
    rule NERD021 established for the kubeconfig: a path the daemon invents is
    worse than an absence, because an absence can be reported.

.. spec:: Interpolate the control-plane compose mount
    :id: HMD_CLI_NEURONSPHERE_NERD022_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD022
    :status: proposed

    The ``floci`` service's socket mount shall be interpolated from the same
    resolved endpoint rather than written literally. ``ComposeEnv`` already
    interpolates that file, so this is a variable with the present path as its
    default, and the control plane behaves identically on every rootful engine.

    This spec is the one that decides whether the platform starts at all. It
    can land without SPEC001 and be worth having; SPEC001 without it is not.

.. spec:: A non-unix endpoint is a refusal, not a guess
    :id: HMD_CLI_NEURONSPHERE_NERD022_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD022
    :status: proposed

    There is no socket to derive when the resolved endpoint is ``tcp://`` or
    ``https://``: the daemon is not on this machine's filesystem, and a
    sibling container cannot be handed a path to it. Such an engine shall be
    refused by name at the point a deploy node would need the socket, with the
    reason given, rather than mounting something that will not work.

    Telling siblings ``DOCKER_HOST`` instead is the obvious alternative and is
    deliberately *not* specified here: it changes what credentials and TLS
    material every deploy node needs, which is a larger question than this
    document, and NERD021 SPEC010 holds that ``nsctl`` does not inject
    ``DOCKER_HOST`` into what it runs. ``ssh://`` remains refused at resolution
    by NERD021 SPEC004.

.. spec:: Doctor stops warning about what works
    :id: HMD_CLI_NEURONSPHERE_NERD022_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD022
    :status: proposed

    The ``engine socket`` check added for NERD021 SPEC011 warns that deploy
    nodes will need the socket path adjusted. Once SPEC001 and SPEC002 adjust
    it, that warning shall say what is actually true of the engine -- naming
    the socket it resolved -- rather than predicting a failure that no longer
    happens. A warning that outlives its cause teaches people to ignore
    warnings.

.. spec:: The cgroup question is answered by a run
    :id: HMD_CLI_NEURONSPHERE_NERD022_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD022
    :status: proposed

    Fixing the socket is necessary and is not known to be sufficient. The
    rootless daemon measured above reports ``CgroupDriver: none``, and the k3s
    node the substrate runs is privileged and expects cgroup v2 delegation
    that a rootless daemon may not have.

    So this requirement is met by a ``control-plane start`` and an ``env
    start`` completing on a rootless engine, not by the mounts being right.
    Until that run exists, the documentation shall not claim rootless support
    -- the same discipline NERD021 applied to Colima, where the claim waited
    for the run, and the run found two defects that unit tests could not.

    If k3s proves impossible on a rootless daemon, the honest outcome is a
    substrate-level refusal naming the reason, with ``--substrate none``
    environments still supported. That is a real and useful subset: it is the
    mode NERD014 added, and it needs no cluster.
