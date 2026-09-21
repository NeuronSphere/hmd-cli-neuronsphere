.. NERD013 Role Activation on Import

NERD013 Role Activation on Import
=================================

.. include:: ../_includes/paid-cloud.txt

.. req:: Follow only the dependency roles a repo class marks required
    :id: HMD_CLI_NEURONSPHERE_NERD013
    :status: implemented

    When ``nsctl`` closes a selection of repo instances under its dependencies,
    it shall follow a role only when the repo class **declares that role
    required**, and shall offer an explicit way to follow an optional one.

    Where a required role cannot be filled by anything local, ``nsctl`` shall
    bind it to the environment's core instance when nothing but the role's
    presence is validated, and shall refuse -- naming the class and the
    resource type -- when the role is resource-typed and a real producer is
    required.

    Every decision shall be reported: which roles were not followed, which were
    followed because their requiredness could not be determined, and which were
    bound to the core instance rather than deployed.

    .. note::

        ``implemented`` as of 2026-09-16, on a live acceptance run against
        hmdtr1's cloud ``dev`` and a local environment. See `The acceptance
        run`_. All six SPECs are implemented and covered against a fake
        deployment service and a fake librarian; criteria 1 through 4 were
        then run for real. The closure of ``ms-transform`` is **24 instances,
        not 66**; no ChangeSet was refused and no failure named a role; three
        cloud-sourced workloads deployed locally at their cloud versions. What
        the run also showed is the residue this document does not remove --
        cloud-only classes that a required name-only role reaches -- and that
        is recorded there with two named instances rather than left as a
        prediction.

.. note::

    This document exists because ``NERD012``'s last open question -- *is a
    sixty-six-instance closure useful?* -- has an answer that is not specific to
    cloud BOMs. ``nsctl env from-repo`` already follows required roles and gates
    optional ones behind profiles, through ``internal/localspec``; ``nsctl bom
    import`` follows everything. The two disagree about the same graph, and
    that disagreement is the mechanism this document specifies. Per the house
    rule ``NERD011`` states, the capability gets its own document rather than
    being buried in its first consumer.

Motivation
----------

``NERD012``'s acceptance run selected the one ``hmd-ms-transform`` instance from
hmdtr1's cloud ``dev`` and closed over **66 of its 109 instances**. Much of that
-- ``hmd-inf-acm``, ``hmd-inf-wafv2``, ``hmd-inf-api-gateway``,
``hmd-inf-neptune``, ``hmd-inf-private-ca``, ``hmd-inf-karpenter`` -- has no
local counterpart. It fetches cleanly and then fails at ``nsctl env apply``.

The closure is not wrong. Those roles are declared, the cloud really did wire
them, and ``hmd-ms-deployment`` refuses a whole ChangeSet on a required role
nothing fills. But correct-and-unusable is still unusable, and the failure
arrives minutes later naming a role rather than the import that included it.

``NERD012`` SPEC005 recorded why it could not do better:

    It would be better to close over *required* roles only, and that is not
    available: a BOM entry's ``dependencies`` is ``{role: target}`` with **no
    required flag** -- the flag lives on the RepoClassVersion's declared
    dependencies, not on the deployed edge.

Both halves of that sentence are true and the conclusion does not follow. The
flag lives on the RepoClassVersion's declared dependencies, and **that is a file
``nsctl`` already reads**: ``deploy.dependencies.<role>.required``, in the repo
class's own BACON manifest, which travels inside the artifact ``bom import``
fetches. ``internal/repoclass`` reads it today and hands it to
``add_repo_class_version`` unexamined.

That makes it not merely available but *authoritative*. The required set a local
``env apply`` validates against is the one ``nsctl`` itself registered, out of
that same manifest. Reading it here and reading it at seed time are the same
read of the same bytes.

The evidence this document is built on
--------------------------------------

Measured across the 387 ``meta-data/manifest.json`` files in a full workspace and
against ``hmd-ms-deployment``'s source on 2026-09-16. The closure figures in
this table are **class-level, computed from checkouts**; the instance-level
figure measured against the live BOM the same day is **24 of 109**, and is in
`The acceptance run`_.

.. list-table::
   :header-rows: 1
   :widths: 45 55

   * - Question
     - Answer
   * - Is ``required`` a real BACON field?
     - **Yes.** ``hmd-docs-bacon``'s ``schema.rst`` types it as a *string* enum,
       ``"true"`` or ``"false"``, and ``spec/deploy.rst`` defines it as "whether
       deployment fails without this dependency".
   * - Is it actually authored?
     - **Yes, universally.** 517 dependency blocks, **367** ``"true"`` and
       **150** ``"false"``. None missing the key, none malformed. 53 repos
       declare at least one optional dependency.
   * - Does ``hmd-ms-deployment`` honour it?
     - **Yes**, at ``_do_add_repo_instance_deployment``: it builds
       ``required_roles`` from the class-version edges and raises only for a
       missing one. ``validate_changeset`` reports the two cases separately, as
       ``missing_required_role`` and ``missing_optional_role``.
   * - Why is it not in the BOM?
     - ``deploy_bom_creator`` builds ``dependencies`` from
       ``RepoInstanceReqRepoInstance`` edges, which carry ``role`` and nothing
       else. The flag is on the *class-version* edge and is dropped in the
       flattening.
   * - How much would required-only closure prune?
     - ``hmd-ms-transform`` **56 classes to 25**; ``hmd-app-airflow`` 52 to 23;
       ``hmd-inf-trino`` 46 to 12. The pruned set includes ``private-ca``,
       ``karpenter``, every ``clickhouse``, every ``trino`` and every ``opa``
       class.
   * - What is left that still has no local path?
     - 16 of the 25, of which **only two are resource-typed** --
       ``hmd-inf-eks-alb`` and ``hmd-inf-eks-node-group``. The other 14 are
       name-only roles, where presence is the whole of what is validated.
   * - Does a "cloud-only" repo class already say so?
     - **Two of the three consumers of** ``hmd-inf-private-ca`` **already mark it**
       ``required: "false"`` -- ``hmd-inf-opa-authorizer`` and ``hmd-ms-device``.
       The third, ``hmd-inf-cert-revoke``, is a Lambda whose entire job is
       ``acm-pca:RevokeCertificate``; it is genuinely required-and-cloud-only,
       and required-only closure never reaches it.

The last row is the one that decides this document. The platform has already
written down, in the existing schema and without being asked, most of what a new
"not available locally" flag would have had to be back-filled to say.

Why not a new manifest key
--------------------------

The obvious alternative is a per-repo-class flag -- ``local.supported: false``,
or similar -- and it is rejected for four reasons, in ascending order of weight.

It is expensive: a ``hmd-docs-bacon`` schema change, then edits across dozens of
``hmd-inf-*`` repos, then a rebuild and republish of each.

It has the wrong polarity. A flag must be set on every class that cannot run
locally, and it is absent by default, so the set is only ever as complete as the
last person to remember. ``required: "false"`` is set by the *consumer*, which
already knows, and nothing has to be enumerated.

It is the wrong predicate. "Available locally" is a property of the pair
(repo class, local substrate), not of the class. ``hmd-inf-eks-cluster`` is as
AWS-specific as anything in the platform and is precisely what ``nsctl`` deploys
locally, through its ``src/local/cdktf`` overlay. For the same reason the
presence of a ``src/local`` directory is not usable as the predicate either:
absence means "no overlay needed" for a pure-Helm class and "cannot run locally"
for a private CA, and nothing distinguishes them.

And it cannot help the case that motivates it. The flag would live in a
manifest, and a manifest is published inside a *version*. A cloud environment
runs versions that were published before the flag existed, so an import against
today's ``dev`` would see no flag at all -- for however many release cycles it
takes every producing repo to ship one.

Scope
-----

**In scope.**

- Following only required roles when closing a selection under its
  dependencies, for ``nsctl bom show`` and ``nsctl bom import``.
- Opting a named optional role, or every optional role of a class, back in.
- Binding a required role that nothing local will fill, where that is honest,
  and refusing where it is not.
- A generated, hand-editable selection file, so a closure can be reviewed and
  narrowed as a file rather than as a sequence of flags.

**Out of scope.**

- Writing anything to a cloud service. ``NERD012`` SPEC007 stands unchanged.
- Changing ``get_deployment_bom``. See `Open questions`_.
- Changing what ``nsctl env from-repo`` does. It already behaves this way; this
  document brings the other path into line with it, not the reverse.
- Making every cloud repo class deployable locally. Some are not, and this
  document's contribution is to say which, by name, before anything is fetched.

Reference: what already exists
------------------------------

.. list-table::
   :header-rows: 1
   :widths: 45 55

   * - Piece
     - Where it already is
   * - The closure, as a function over data
     - ``cmd/bom_select.go``'s ``selectionFlags.apply``
   * - Required-and-optional semantics, for the repo-rooted path
     - ``internal/localspec``'s ``Activate``
   * - Reading ``required`` in either spelling
     - ``internal/localspec``'s ``truthy``
   * - A repo class's ``deploy.dependencies``
     - ``internal/repoclass``'s ``Manifest`` and ``LoadManifest``
   * - Binding a role to the core instance
     - ``internal/bom``'s ``Substrate``, for ``datadog-lambda`` and ``rds-loggroup``
   * - Binding a substrate class under this environment's name for it
     - ``internal/bom``'s ``SubstrateInstanceFor``, and ``bomSelection.rename``
   * - Fetching and caching one artifact
     - ``cmd/artifact.go``'s ``storeArtifact``, ``internal/artifact``'s ``Cached``
   * - Reporting a decision rather than performing it quietly
     - ``bomSelection.report``

Nothing here is a new subsystem. It is one predicate threaded through an
existing traversal, one binding rule generalised from two roles to all of them,
and a file format.

.. spec:: Close over required roles only
    :id: HMD_CLI_NEURONSPHERE_NERD013_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD013
    :status: implemented

    When the closure reaches an instance and expands its roles, it shall follow
    a role's targets only if the instance's repo class declares that role
    ``required``.

    **The source of truth is the repo class's own BACON manifest**, at the
    version the BOM names: ``deploy.dependencies.<role>.required``, read from
    the artifact unpacked under ``artifact.Dir(home, class, version)``. Parsed
    with ``localspec``'s ``truthy``, which already accepts the string spelling
    every manifest in the workspace uses and the bool spelling the schema does
    not forbid. One implementation, exported rather than copied: two readings of
    ``required`` that disagree would be a defect nothing could observe until an
    apply failed.

    This is the *right* source, not merely an available one. The required set
    the local ``hmd-ms-deployment`` validates against is the one ``nsctl``
    registers through ``add_repo_class_version``, out of this same file. Reading
    it here and reading it at seed time are the same read.

    **A class whose manifest cannot be read closes over all of its roles**, and
    the run says how many entries that happened to. The degradation is always
    toward the larger import: a missing manifest that quietly produced a smaller
    closure would drop a required role and fail at apply, which is the exact
    failure this document exists to remove.

    Requiredness is a property of the class version, not of the deployed edge.
    An instance that appears in the BOM only because some *other* instance's
    optional role pointed at it is not reached, and is not an error.

.. spec:: Opting an optional role back in
    :id: HMD_CLI_NEURONSPHERE_NERD013_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD013
    :status: implemented

    ``--with <role>`` and ``--with <repo-class>``, repeatable on both ``show``
    and ``import``, shall follow the named optional role as though it were
    required -- transitively, since what it reaches is then closed over by
    SPEC001's ordinary rule.

    A ``--with`` matching no optional role in the closure shall be refused,
    listing the optional roles there are. This is ``seed``'s existing rule for
    ``--instance`` and ``--class`` and it exists for the same reason: a selector
    that silently contributes nothing has exactly the shape of a successful
    smaller import.

    ``--exclude`` shall be **relaxed**, and relaxed further than it first
    appears. It refuses today for anything the closure pulled in, because
    unfilling a role would reintroduce the failure the closure prevents. What
    actually decides the question is not whether the role is required but
    whether it can be filled some other way once the target is gone, which
    SPEC003 answers. So the refusal narrows to two cases:

    #. the role is required and **resource-typed** -- there is nothing to bind
       it to, and the refusal names the resource type;
    #. this run could not read the class's declarations, so it cannot tell --
       the refusal names the class and says how to make it readable.

    Everything else is excludable: an optional role because it is simply not
    declared, and a required **name-only** role because SPEC003 binds it.

    That second exception is the one that matters in practice, and it took
    writing the code to see it. A cloud-only class is usually *in* the BOM and
    is usually reached by a role its consumer marks required --
    ``hmd-inf-acm`` through ``hmd-inf-api-gateway``'s ``acm`` role -- so SPEC001
    does not prune it and SPEC003 never fires on its own. Excluding it is how it
    leaves, and the exclusion is safe only because the role it filled can be
    bound. The two specs are one mechanism seen from two ends.

    The semantics are otherwise ``internal/localspec``'s, which are
    deliberately docker compose's: what is required always activates, what is
    optional activates only when asked for, and **lean requires no declaration
    at all**. ``localspec`` refuses to profile-gate a required dependency,
    naming the ChangeSet failure as the reason; this document refuses the same
    thing in the same words, in the one case where it is still true.

.. spec:: Binding a required role nothing local will fill
    :id: HMD_CLI_NEURONSPHERE_NERD013_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD013
    :status: implemented

    SPEC001 shrinks the closure; it does not make every remaining role fillable.
    On today's data 16 of the 25 surviving classes have no local deploy path,
    reached through roles their consumers mark required.

    A required role whose target is neither picked nor bound shall resolve by
    what the *role* asks for, which the repo class's dependency block already
    says. A target comes to be unpicked in three ways: it is absent from the
    BOM, its last deployment was not ``DEPLOYED``, or **it was excluded** --
    which SPEC002 permits precisely because of this rule, and which is how a
    cloud-only class that a required role reaches is left out on purpose.

    #. **Name-only** -- the block declares a ``repo_class_name`` and no
       ``resource``. Nothing beyond the role's presence is validated, so it is
       bound to the environment's core instance, ``local-neuronsphere``.
    #. **Resource-typed** -- the block declares a ``resource``. The producer is
       validated against the resource type, so a binding would be a lie that
       fails later and further away. It is refused, naming the instance, the
       role, the class and the resource type.

    Binding to the core instance is not new and not a heuristic: ``Substrate``
    does exactly this for ``datadog-lambda`` and ``rds-loggroup``, with the
    comment that they "are name-only roles nothing validates beyond presence,
    and the overlay creates neither". This generalises those two hard-coded
    cases into the rule they were always instances of.

    **It is nonetheless a fiction** -- the core instance deploys no ACM -- so it
    is never silent. Every bound role is listed by ``report``, naming the class
    that was not deployed. ``--no-stub-roles`` refuses instead of binding, for
    anyone who would rather have the honest failure than the working import.

    **No catalogue.** ``internal/bom``'s package comment records that shipping
    the substrate and nothing above it "is the whole difference from
    ``bom_seeder.py``, which carries a built-in catalogue". A list of cloud-only
    repo classes would be that catalogue returning under a new name. The rule
    above reads the answer out of the manifest the class published, so a repo
    class nobody here has heard of is handled the same way as one everybody has.

.. spec:: The selection file
    :id: HMD_CLI_NEURONSPHERE_NERD013_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD013
    :status: implemented

    A closure of twenty-five entries is small enough to read and too large to
    narrow by typing flags. ``show`` shall be able to write what it resolved,
    and ``import`` shall be able to read it back:

    .. code-block:: text

        nsctl bom show dev --instance ms-transform --save-selection sel.toml
        $EDITOR sel.toml
        nsctl bom import dev --selection sel.toml

    TOML, one block per resolved instance, carrying ``take`` and enough context
    to decide it:

    .. code-block:: toml

        [[instance]]
        take       = true
        name       = "ms-transform"
        repo_class = "hmd-ms-transform"
        version    = "1.4.219"
        why        = "asked for"

        [[instance]]
        take       = false
        name       = "acm-main"
        repo_class = "hmd-inf-acm"
        version    = "0.1.88"
        why        = "api-gateway needs it for acm"

    ``--selection`` takes the ``take = true`` set **literally**, then re-runs
    SPEC001's closure over it to verify that no required role was left unfilled,
    and refuses naming the instance and the role that broke. A hand-edited file
    is exactly the input that can be wrong, and the whole point is that it is
    wrong here rather than at ``env apply``.

    A file, rather than an interactive picker. It is diffable, committable,
    reusable on another machine and usable from a script, and it needs no
    dependency: ``nsctl`` links no prompt library, and its only interactive
    prompts are two hand-rolled single-line reads. A picker, if it is ever
    wanted, is a thin layer over this and not a substitute for it.

.. spec:: The order ``import`` proceeds in
    :id: HMD_CLI_NEURONSPHERE_NERD013_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD013
    :status: implemented

    ``NERD012`` SPEC005 fixed the order as **select, filter, close over
    dependencies, fetch, declare**, and called it load-bearing. SPEC001 needs a
    class's manifest in order to expand that class's roles, and the manifest
    arrives with the artifact. So the order becomes **select, filter,
    close-while-fetching, declare**.

    The invariant that made the original order load-bearing is unchanged and in
    fact strengthened: nothing is written to the manifest until the artifact
    behind it is in the control plane. What changes is only that the fetch is
    interleaved with the traversal rather than following it -- and the traversal
    now fetches strictly less, because it expands strictly fewer roles.

    Failure handling keeps ``NERD012`` SPEC006's rules exactly. An artifact that
    cannot be fetched removes its instance from the import, every other instance
    is still attempted, and the command exits non-zero naming all of them. An
    entry whose artifact cannot be fetched cannot have its roles read either, so
    it contributes nothing to the closure and nothing is silently dropped on its
    behalf.

    ``nsctl bom show`` shall remain a preview of the ``import`` that follows it.
    ``--resolve`` fetches the manifests so the preview is exact. Without it,
    ``show`` uses whatever is already unpacked in the artifact cache and prints
    one line saying how many entries it therefore closed over conservatively --
    which is honest, and which is also why a ``show`` run after an ``import`` is
    exact for free.

.. spec:: What this is not
    :id: HMD_CLI_NEURONSPHERE_NERD013_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD013
    :status: implemented

    **Not a claim that the result reproduces the cloud environment.**
    ``NERD012`` SPEC007 said an import brings versions and configuration across
    and claims nothing more. Binding a role to the core instance makes that
    gap larger, not smaller, which is why every such binding is named.

    **Not a new manifest key**, and not a new field on the wire. Every input it
    reads is authored today, by repos that have been authoring it for years.

    **Not a dependency solver.** It follows fewer edges than before and reports
    what it did not follow. It reconciles nothing, chooses between no
    candidates, and consults no version specifier.

    **Not a write path to the cloud.** Unchanged.

    **Not a change to** ``nsctl env from-repo``. That path already activates
    required roles and profile-gated optional ones. This document is the other
    path catching up to it.

What writing it settled
-----------------------

Six things the proposal above did not state, each of which the code had an
opinion about.

**The fetch belongs in the injected predicate, not in the traversal.** SPEC005
moves the fetch inside the closure, which reads like a reason to make
``selectionFlags.apply`` do I/O. It is not: ``apply`` stays a function over data
-- a BOM and three predicates in, a decision out -- and the going-and-getting
lives in ``roleResolver``, which is what the ``roles`` predicate is. That is the
whole reason the closure is still testable without a server, and it is why the
existing tests kept passing unchanged rather than needing a network.

**A stub must not also be reported as a binding.** The first version recorded a
stubbed target in ``bound`` so that ``resolvedRoles`` would treat it as
resolvable, which printed it under "bound without importing" *and* under the
stub warning -- two accounts of one thing, which is the identical defect
``NERD012`` SPEC005's closure note had to fix once already. Stub targets are
now a set of their own.

**A stub is per role, not per target.** Two instances needing the same absent
thing is the ordinary case -- ``datadog-lambda`` is a required role on four
classes in the workspace -- and binding the second one silently, because the
target was already dealt with, under-reports exactly what the warning exists to
surface.

**The** ``--exclude`` **relaxation is wider than "optional only"**, and this is
the finding that matters most. A cloud-only class is usually *in* the BOM and
usually reached by a required role, so SPEC001 does not prune it. Excluding it
is how it leaves, and that is safe only because SPEC003 can bind the role it
filled. SPEC002 and SPEC003 read like two features and are one mechanism; the
proposal had them as separate ideas and they are not.

**A** ``--dry-run --resolve`` **cannot say "nothing was fetched".** It fetches
manifests, by definition. The sentence is now different in the two cases,
because a dry run is worth nothing if you cannot believe what it says about
itself.

**The class name for an absent target comes from the dependency block**, not
from the BOM. The BOM has no entry for a target it does not contain, so looking
the class up there yields an empty column -- on precisely the rows where naming
the class is the only useful thing left to say. It falls back to what the
consumer declared.

The acceptance run
------------------

Run on 2026-09-16 against ``https://ms-deployment-aaa-reg1.hmdtr1-admin-neuronsphere.io``
and its artifact librarian, with the token ``hmd login`` had cached, from a
freshly built ``nsctl`` (both installed binaries predated the ``bom`` family).
Every cloud step was a read. The local side was a dedicated environment,
``nsctl env add nerd013``, on the control plane already running in
``$HMD_HOME``, so the ``local`` fixture was untouched.

**Criterion 1 -- measured.** ``dev`` is running 109 instances.
``nsctl bom show dev --instance ms-transform --resolve`` selects **24 of
them** and binds 3 more to the substrate (``base-vpc``; ``core-rds`` as
``environment-db``; ``eks-upg`` as ``eks-cluster``), against ``NERD012``'s
**66**. ``--resolve`` read every one of the 24 manifests: no "could not read"
line. The class-level 56-to-25 estimate above was close, and is now replaced
by this figure.

**Criterion 2 -- holds.** 22 optional roles were not followed. Of those, 17
drop their target from the import -- ``karpenter`` (via
``eks-node-lin:karpenter``), ``otel``, ``trino``, ``ecr-cache``,
``pgbouncer``, ``ms-cluster``, ``broker``, ``authorizer``, both dispatchers and
three ``buckets`` among them. ``hmd-inf-private-ca`` is not in the selection
and is not even in the not-followed list: it is reached only through roles two
optional hops out, so the closure never sees it. The remaining 5 optional roles
point at a target that is in the environment anyway -- ``ms-transform:eks-cluster``
bound as substrate, ``ms-transform:ext-secrets`` and ``ms-transform:compute``
picked through ``airflow``'s required roles -- and the role is kept. The report
said "not imported" for those five too, which was false; it now prints them
as a separate note. See `What the run found`_.

**Criterion 3 -- holds, and was vacuous as written.**
``nsctl bom import dev --instance ms-transform --env nerd013 --dry-run --resolve``
declares 24, binds nothing to the core instance and refuses nothing, because
every required target in this closure is ``DEPLOYED`` in ``dev`` and is picked.
So the two mechanisms were exercised by exclusion instead:

- ``--exclude core-acm`` (a required *name-only* role, ``api-gateway:acm``)
  declares 23 and prints the SPEC003 warning, naming ``api-gateway:acm wanted
  core-acm (hmd-inf-acm)``.
- ``--exclude eks-alb`` (a required *resource-typed* role,
  ``ms-transform:eks-alb``) is refused, naming
  ``kubernetes.neuronsphere.io/ingress-controller`` as the type nothing local
  produces.

**Criterion 4 -- see below.** Run as
``nsctl bom import dev --instance ms-transform --env nerd013 --apply -V``.

**Criterion 4 -- holds, twice, and what it holds is worth stating precisely.**

*First attempt*, the 24 as selected. The local ``hmd-ms-deployment`` accepted
the ChangeSet -- ``Plan: 23 to deploy, 1 changed, 6 unchanged`` -- and the DAG
ran. Its first node, ``core-acm`` (``hmd-inf-acm@0.1.6``), failed in
``tofu apply``: ``no matching Route 53 Hosted Zone found`` for
``hmdtr1-nerd013.neuronsphere.io``. Not a role. The class is an ACM
certificate and there is no local counterpart; it is in the closure because
``api-gateway`` marks its ``acm`` role required.

*Second attempt*, ``--exclude core-acm --apply``, which SPEC002 permits
because ``acm`` is name-only and SPEC003 binds it to ``local-neuronsphere``.
Again accepted -- ``Plan: 24 to deploy, 0 changed, 5 unchanged`` -- and this
time three imported instances **deployed, at the versions the cloud runs**:
``argo-logs`` and ``airflow-logs`` (``hmd-inf-s3bucket@0.1.11``) and
``airflow-efs`` (``hmd-inf-efs@0.1.3``). Then ``base-datadog``
(``hmd-inf-datadog@0.1.22``) failed: ``ParameterNotFound ... Parameter
datadog-url not found``, an SSM parameter the tenant's account has and Floci
does not. Not a role either. It is in the closure because
``datadog-lambda:datadog`` is required and ``ms-dbaccount:datadog-lambda`` is
required.

So the thing this document set out to remove is gone: no ChangeSet was refused,
no failure named a role, and the first cloud-sourced workloads have deployed
into a local environment. What the criterion's wording lets through, and what
the run makes plain, is that the residue is now **cloud-only classes reached
through required name-only roles** -- ``acm``, ``datadog``, and behind them
``api-gateway``, ``wafv2``, ``neptune``, the two ``eks-node-group`` instances,
``eks-alb``. Required-only closure does not prune them, because their
consumers are telling the truth: in the cloud they are needed. Each is
excludable one at a time, and each exclusion is discovered by a failed apply.
The bounded retry the run allowed itself was one; the whack-a-mole is not
this document's to play, and ends in the same place as the open question
below on selecting a cloud-only class outright.

The practical answer that already exists is SPEC004: write the selection
out, flip the cloud-only entries to ``take = false`` in one edit, and import
the file. ``--exclude core-acm`` was refused for nothing and stubbed one role;
a file that flipped seven would stub seven, and the report would list all of
them before anything deployed. What does not exist is a way to know *which*
seven without either domain knowledge or a failed apply, and that is the
catalogue this document declined to carry. It is left open, and now it is
left open with two named instances and a measured cost.

The ``nerd013`` environment was left running with the second attempt's
manifest, so the run can be repeated or continued by hand.

What the run found
~~~~~~~~~~~~~~~~~~

**An unfollowed optional role is not always a dropped one.** Five of the 22
optional roles pointed at instances the closure reached anyway, through
another instance's required role or the substrate binding. ``resolvedRoles``
already kept those roles -- it keeps every role whose target is present -- but
the report listed them under "not followed, so they were not imported", which
misdescribed the manifest it had just written. The report now separates the
two: roles whose target is gone, and roles kept because the target is here.
This is a defect in the reporting, not the closure, and it was only visible
against a BOM with enough shared targets to produce it.

**The first High risk has a name.** ``ms-transform:buckets`` is authored
``required: "false"`` and points at three ``hmd-inf-s3bucket`` instances --
``trino-storage``, ``device-lib``, ``nsreporting-lib``. Those are the
``*_BUCKET`` values the service resolves from its librarians, and the import
drops all three. Whether ``ms-transform`` runs usefully without them is exactly
the question the risk table says this document cannot answer from a manifest;
``--with buckets`` is the escape hatch it names.

**The refusal's grammar.** ``--exclude eks-alb: it is ms-transform needs it
for eks-alb`` -- the closure's *why* string dropped into an "it is" template.
Fixed in passing.

**The** ``nerd013`` **name warning is stale.** ``nsctl env add`` still warns
that a non-``local`` slug fails in ``tofu init`` unless the projectbuilder
image carries ``hmd-lib-cdktf``'s ``is_local_environment``. The default image
(``0.5.388``) ships ``hmd-lib-cdktf 0.2.81``, which has it. The warning, its
callers and the doc bullets can go; not done here.

Risks
-----

.. list-table::
   :header-rows: 1

   * - Risk
     - Severity
     - Mitigation
   * - A role marked optional by its consumer is in practice needed for the
       instance to work, so the import succeeds and the workload misbehaves
     - **High**
     - This is the one risk the document cannot remove, because it is a claim
       about a manifest being wrong. ``--with`` is the escape hatch; every
       unfollowed optional role is reported by name so the list is visible
       before ``env apply`` rather than discovered afterwards.
   * - Binding a name-only role to the core instance produces an instance that
       deploys and then fails at runtime for want of the thing that was stubbed
     - **High**
     - Every binding is named in the report, with the class that was not
       deployed. ``--no-stub-roles`` refuses instead. This is a worse failure
       than an unmet role and a better one than a missing import, and it is the
       judgement call in this document most worth disagreeing with.
   * - The requiredness read here disagrees with what the local control plane
       registers
     - Low
     - They are the same read of the same file at the same version. A test
       pins that ``internal/repoclass`` is the only reader.
   * - The closure shrinks so far that a genuinely required instance is missed
     - Low
     - Unknown requiredness closes over everything; a fetch failure contributes
       nothing rather than assuming; a selection file is re-validated on import.
   * - ``--resolve`` on ``show`` makes a preview as slow as an import
     - Low
     - It is opt-in, it is bounded by the same artifacts the import would fetch,
       and a cached artifact is not fetched again.

Acceptance criteria
-------------------

Criteria 1 through 4 were met on the live run of 2026-09-16 -- see `The
acceptance run`_ for the measured figures and for what criterion 4's wording
lets through. Criteria 5 through 9 are met by tests against a fake deployment
service and a fake librarian.

#. The closure size for ``--instance ms-transform`` against hmdtr1's cloud
   ``dev`` is measured before and after, at instance level, and recorded here
   against ``NERD012``'s 66. The class-level 56-to-25 figure above is replaced
   by the measured one.
#. ``nsctl bom show dev --instance ms-transform --resolve`` names every optional
   role it did not follow, and ``hmd-inf-private-ca`` and ``hmd-inf-karpenter``
   are not in the selection.
#. ``nsctl bom import dev --instance ms-transform --dry-run`` names every role
   it would bind to the core instance, and every resource-typed role it cannot
   fill.
#. ``nsctl bom import dev --instance ms-transform --apply`` -- the import
   followed by ``nsctl env apply``, as one command per ``NERD012`` SPEC005's
   amendment -- deploys, or fails for a reason that is not an unmet role.
   **This is the criterion the document exists for**; the rest are how it is
   reached.
#. ``--with`` on an optional role re-adds it and what it transitively requires.
#. ``--exclude`` on an optional closure member succeeds; on a required one it
   still refuses, naming what wanted it.
#. A class whose artifact cannot be fetched closes over all its roles, is
   reported, and is not declared.
#. ``--save-selection`` round-trips: the file ``show`` writes, imported
   unedited, selects exactly what ``show`` displayed.
#. A hand-edited selection file that removes a required target is refused at
   import, naming the instance and the role.

Open questions
--------------

**Should** ``get_deployment_bom`` **carry** ``required`` **per role?** It is one
attribute already on the class-version edge, and it would let ``show`` be exact
with no fetch at all. It is not proposed as the mechanism here for two reasons:
it is a cloud service change, so no local ``nsctl`` could rely on it until every
tenant had it; and it reports the *cloud's* view of requiredness, where a local
apply validates the artifact manifest ``nsctl`` itself registers. As an
optimisation for ``show`` it is worth having. As the source of truth it is
worse than the file.

**Should the two resource-typed residues get local producers?**
``hmd-inf-eks-alb`` produces ``aws.neuronsphere.io/aws-load-balancer-controller``,
parented on ``kubernetes.neuronsphere.io/ingress-controller`` -- and the local
k3s cluster genuinely has an ingress controller, since Traefik is already
emulated in the ``alb`` class's place. Declaring that the core instance produces
the parent type would let the ``eks-alb`` role bind the way ``eks-cluster``
does, which is better than stubbing it. The compute type
(``hmd-inf-eks-node-group``) has the same shape. Both are substrate changes with
their own evidence to gather and neither is in this document.

**What should happen when someone selects a cloud-only class outright?**
``hmd-inf-cert-revoke`` is required-and-unsatisfiable: its entire job is
``acm-pca:RevokeCertificate``, and nothing local can stand in. Required-only
closure never reaches it, so the case only arises from an explicit
``--instance``. The honest answer is a refusal at import rather than a
successful fetch and a failed apply -- but making that refusal *general* is the
new manifest key this document declined to add, and making it specific is the
catalogue ``internal/bom`` declined to carry. It is left open deliberately.
