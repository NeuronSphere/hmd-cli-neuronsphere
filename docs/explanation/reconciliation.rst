Desired-state reconciliation
============================

An environment manifest declares the instances you want. The deployment graph
reports what has been deployed. Reconciliation compares them so repeated
applies can leave current instances alone while deploying additions and
configuration changes.

What is compared
----------------

The CLI combines substrate entries with the manifest's workload entries.
For each instance, it considers the deployment record and a snapshot at
``<state_dir>/applied-changeset.json``. The snapshot records the definition
used by a successful deployment.

This is a comparison of deployment definitions and recorded state. It is not
a general detector of every change inside a running database, container, or
checkout. Editing source code without changing the recorded definition does
not necessarily result in a changed plan. Use the workload's development
workflow or an explicit redeploy when that is the intended operation.

.. list-table::
   :header-rows: 1

   * - Observed situation
     - Reconciliation behavior
   * - Desired instance is not deployed
     - Add it.
   * - Deployed definition differs from its snapshot
     - Deploy the changed definition.
   * - Deployed instance matches its snapshot
     - Leave it alone.
   * - Deployed instance has no snapshot
     - Ordinarily leave it alone; absence alone is not proof of a change.
   * - Instance is deployed but no longer declared
     - Report it as undeclared and leave it running.
   * - Deployment graph cannot be read
     - Treat desired entries as needing deployment and report degraded knowledge.

There is additional handling for state left after a purge so stale
``DEPLOYED`` records do not make absent resources look current.
``--force-full-redeploy`` bypasses normal change selection.

What planning does
------------------

``nsctl env plan`` builds the reconcile result and validates the proposed
definition through the local deployment service. It checks dependency
structure and reports resource-producer mismatches that a simple
instance-exists check would miss.

Planning does not run deployment nodes or update the applied snapshot.
It can perform idempotent catalog registrations and service calls, so it
requires a running control plane and is not a purely local file diff.

A plan is a preview of current inputs. JSON and Markdown output support
review, but neither is a saved execution artifact: ``env apply`` computes
again. Changes to the manifest or service state between those commands can
change what apply does.

How apply executes
------------------

The CLI orders instances by their dependency bindings. It registers the
instance deployment records with the service, obtains configuration and
resource references, then runs the deployment commands in containers.

Independent nodes can run concurrently. ``HMD_LOCAL_RUNNER_PARALLELISM``
controls concurrency and defaults to four; a value of one makes dispatch
sequential. A failure stops new dispatch while already running nodes finish.
Only settled or already-current instances contribute successful snapshot state.

This makes a retry useful after fixing a failed node: successful work can be
recognized rather than assuming the entire previous run succeeded. Inspect
verbose output and the next plan before forcing a full redeploy.

Removal is not teardown
-----------------------

``repo remove`` deletes an environment declaration. During repository
adoption, ``--prune`` removes declarations no longer requested. Neither action
destroys deployed resources. An undeclared result is an inventory discrepancy,
not an instruction that apply will delete something.

Use a workload's teardown procedure for individual resources, or a named
``env purge`` when all data and resources in that environment can be discarded.
See :doc:`../how-to/add-workloads` for the edit, plan, apply loop.
