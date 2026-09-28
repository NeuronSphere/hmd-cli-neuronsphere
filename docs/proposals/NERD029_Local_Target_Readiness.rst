.. NERD029 Local Target Readiness

NERD029 Local Target Readiness
==============================

.. req:: A deploy that cannot succeed on this environment shall be reported before it starts
    :id: HMD_CLI_NEURONSPHERE_NERD029
    :status: proposed

    ``nsctl`` shall report what it can know about a deploy's fitness for the
    environment it is aimed at, before a node runs.

    It shall report a pin that no configured librarian publishes, before the
    substrate is built.

    It shall report a pin whose class declares local support only from a later
    version than the one pinned. A class that declares nothing shall read as
    *unknown*, never as unsupported, and shall not warn.

    It shall report, from the rendered manifests and the cluster as it stands,
    a chart that references what the cluster does not provide.

    It shall not infer a class's fitness from the shape of its source tree.

Motivation
----------

Standing one application's transform pipeline up on a purged environment took
three hours across fourteen distinct failures. Five of them were a pinned
artifact that could not deploy here, and every one surfaced mid-apply -- after
the cluster was built, as an error naming something other than the cause:

- ``hmd-inf-neptune@0.3.31``, the version the cloud environment runs, predates
  the Floci-safe local deploy added in ``0.3.33``. The tenant librarian
  publishes ``0.3.31``, ``0.3.30`` and ``0.3.23`` and nothing newer, so the
  pin that would work cannot be fetched at all -- discovered by an apply that
  had already provisioned the environment.
- ``hmd-app-argo@3.44.112`` predates SSO-less local support, so
  ``okta_oauth: false`` still emitted an Okta secret and ``tofu apply`` died
  several hundred lines in on malformed HCL inside a ``jsonencode``.
- ``hmd-inf-transform-broker@0.1.36`` hardcodes ``storageClassName: "default"``
  in its StatefulSet; ``0.1.38`` templates it from
  ``.Values.persistence.storageClassName``, which the manifest already set.
  k3s offers ``local-path`` and no ``default``, so the PVC sat ``Pending`` and
  helm reported only ``context deadline exceeded`` before rolling back and
  deleting the evidence.

Two more were the same requirement on the other side of an apply. A chart
referencing a ServiceAccount that only ``hmd-ms-cluster`` creates left three
Deployments at ``ReplicaFailure`` with no pods and **no event on the
Deployment** -- the reason is on the ReplicaSet, which nothing directs you to.
Okta secrets marked ``Optional: false`` with Okta out of play failed every
worker with ``CreateContainerConfigError``.

``env plan`` already exists to close this class of gap before a reviewer
merges, and ``doctor`` already reports a substrate service whose route answers
5xx where a healthy one answers 404. Neither yet asks these questions.

Why not "does it have ``src/local``"
------------------------------------

The obvious check is the wrong one, and its own evidence says so. Scored
against the five failures above, a warning on a pin whose artifact carries no
``src/local`` fires **once**:

.. list-table::
   :header-rows: 1
   :widths: 30 25 45

   * - Failure
     - Carries ``src/local``
     - Would the check fire
   * - ``hmd-inf-neptune@0.3.31``
     - no
     - yes, correctly
   * - ``hmd-app-argo@3.44.112``
     - **yes** (``nsplugin.json``, ``install_argo.sh``)
     - no -- the missing piece was the cdktf overlay
   * - ``hmd-inf-transform-broker@0.1.36``
     - not relevant
     - no -- a hardcoded value in the chart
   * - ``hmd-ms-transform`` overlay
     - not relevant
     - no -- a library version inside the runner image
   * - ``ev-ms-third-party-lib`` image
     - not relevant
     - no -- the image's language pack

It also fires on classes that are fine. ``hmd-database-account@0.1.3``,
``hmd-inf-redis@0.1.42`` and ``hmd-inf-s3bucket@0.1.13`` were pulled from the
librarian and deployed unchanged in that same run. An overlay is what a class
needs when its cloud stack assumes something local does not have; plenty of
classes assume nothing of the kind, and their cloud cdktf works here as it
stands.

A check that is right one time in five and wrong on every well-behaved class is
worse than no check: it teaches people to skip the output that also carries the
true warnings. Hence the last clause of the requirement -- fitness is
**declared by the class**, not inferred from its file layout.

Specifications
--------------

.. spec:: A pin shall be fetchable before the substrate is built
    :id: HMD_CLI_NEURONSPHERE_NERD029_SPEC001
    :status: proposed

    ``lock`` and ``env plan`` shall verify that every resolved pin exists in a
    configured source, and name the ones that do not together with the versions
    that source does publish.

    Today this surfaces at ``env apply``, after the environment exists:
    ``Error: repository:/hmd-inf-neptune/0.3.33/... : not published``. The
    versions that *are* published take a second query that the operator then
    runs by hand.

.. spec:: Local support is declared, not inferred
    :id: HMD_CLI_NEURONSPHERE_NERD029_SPEC002
    :status: proposed

    A repo class may declare, in its manifest, that it supports deployment onto
    a local environment. ``nsctl`` shall warn only when a class declares
    support and the pinned version precedes the first version declaring it.

    Absence of a declaration is *unknown*. It shall produce no warning, so that
    adding this check does not make every class that never needed an overlay
    noisy on day one.

    The declaration is what makes the ``hmd-app-argo`` case legible: the
    artifact's shape said nothing useful, but a class that knows it gained
    SSO-less local support in some version can say so.

.. spec:: The rendered chart shall be checked against the cluster
    :id: HMD_CLI_NEURONSPHERE_NERD029_SPEC003
    :status: proposed

    ``env plan`` shall render each helm-deployed instance and report references
    the cluster cannot satisfy:

    - a ``storageClassName`` no ``StorageClass`` provides,
    - a ``serviceAccountName`` that neither the chart nor the cluster creates,
    - an ``ExternalSecret`` whose ``ClusterSecretStore`` holds no such key.

    This needs no per-class knowledge and holds for any chart, whether or not
    the class has ever heard of a local environment. It is the part of this
    proposal that catches the most and assumes the least.

.. spec:: ``doctor`` shall report the same, after the fact
    :id: HMD_CLI_NEURONSPHERE_NERD029_SPEC004
    :status: proposed

    A Deployment at ``ReplicaFailure`` shall be reported with the ReplicaSet's
    reason, not left to be found. The same for a PVC ``Pending`` past a grace
    period and an ExternalSecret in ``SecretSyncedError``.

    ``doctor`` already walks the substrate and reports what does not answer.
    These are three more questions on a walk it already makes.

Alternatives considered
-----------------------

**Infer from the artifact's file layout.** Scored above: one hit in five, and
false on every class that needs no overlay. Rejected.

**Maintain a list in nsctl of which classes need local overlays.** A second
place to be wrong, updated by whoever last got burned, and stale by
construction. The class knows; nsctl should ask it.

**Attempt the deploy and report better.** This is worth doing regardless, and
SPEC004 does part of it. It does not replace the pre-flight: the broker's
failure destroyed its own evidence on rollback, and the neptune failure had
already built an environment.

Out of scope
------------

Two of the five failures are named here as motivation and deliberately not
covered by any specification, because ``nsctl`` cannot see them:

- ``hmd-ms-transform``'s overlay imported a symbol released in
  ``hmd-lib-cdktf-factories@0.2.391`` while ``hmd-img-projectbuilder:0.5.389``
  ships ``0.2.389``. That belongs to whatever builds the runner image.
- ``ev-ms-third-party-lib``'s image carried a language pack lacking an entity
  its own ``content_path_configs`` names, which took the whole service down at
  startup rather than failing that one root. That is a self-consistency check
  the librarian should make about itself.

Claiming either here would be the same overreach as inferring from file layout,
pointed in a different direction.
