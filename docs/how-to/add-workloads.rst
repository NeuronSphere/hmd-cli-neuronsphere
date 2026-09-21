Add workloads
=============

Use this guide to add a checkout or a pinned artifact to an existing local
environment. Start it first, for example with ``nsctl env start local``.
``repo`` commands edit environment declarations; ``repoclass`` commands
inspect or author metadata in a repository.

If your repository has no BACON manifest yet, start with
:doc:`../tutorials/create-repository-manifest`. It provides a complete working
example; ``repo add`` alone does not create deployment metadata or a script.

.. include:: ../_includes/paid-cloud.txt

Deploy a checkout
-----------------

The example below assumes a repository named ``hmd-ms-myapi`` with a local
deploy definition and the two dependency roles shown. Substitute your
repository's actual class, path, and roles.

Inspect its metadata::

   nsctl repoclass --path /work/hmd-ms-myapi describe
   nsctl repoclass --path /work/hmd-ms-myapi validate

Declare an instance::

   nsctl repo add hmd-ms-myapi --env local --name my-api \
       --path /work/hmd-ms-myapi \
       --depends eks-cluster=eks-cluster \
       --depends database-instance=environment-db \
       --config replicas=2

``--depends`` maps a role in the class manifest to an instance in the
environment. A role name and an instance name need not be equal. The
configuration value ``2`` is parsed as JSON and stays a number; values that
do not parse as JSON stay strings.

``--path`` selects the checkout explicitly. Otherwise, a local source looks
under ``$HMD_REPO_HOME/<repo-class>``. Without ``--name``, the instance name
is the class name with the ``hmd-`` prefix removed. Choose another name to run
two instances of one class; adding a duplicate name is refused.

Review and deploy::

   nsctl repo list --env local
   nsctl env plan local
   nsctl env apply local
   nsctl repo list --env local

The add command writes ``$HMD_HOME/environments/local.yaml`` and prints its
location. Applying performs the deployment. A dependency validation error
usually means a role is missing, names an absent instance, or points to an
instance that does not produce the required resource type.

Deploy a published artifact
---------------------------

For your own build, register it with the local librarian using
``nsctl artifact register /path/to/build.zip``. This local workflow does not
require paid NeuronSphere.

Alternatively, **paid NeuronSphere customers** can pull a concrete version
from their cloud Artifact Librarian::

   nsctl artifact pull hmd-inf-local-registry@0.1.4

That class and version illustrate the artifact syntax; use the class and
version for your workload. In the environment manifest, an artifact source
looks like::

   version: 1
   name: local
   repos:
     - instance_name: my-api
       repo_class_name: hmd-ms-myapi
       version: "0.3.12"
       source:
         type: artifact
         artifact_type: build
       dependencies:
         eks-cluster: eks-cluster
         database-instance: environment-db

Merge the entry into the existing ``repos`` list rather than replacing other
declarations. A ``version`` on a ``repo add`` command alone does not select
``source.type: artifact``; specify the source in the manifest or use the
repository-adoption or BOM-import workflow, which writes it for you.

Update and remove a declaration
-------------------------------

Edit configuration, dependencies, or a version in the environment manifest,
then plan and apply again. To stop asking for an instance::

   nsctl repo remove my-api --env local
   nsctl env plan local

Removal edits the declaration; it does not tear down the deployed instance.
The plan reports it as undeclared. Use the workload's teardown procedure, or
purge the named environment if you intend to remove all its resources.

Import workloads created by the Python CLI
------------------------------------------

For an environment already populated through ``hmd neuronsphere``, inspect
what would be copied from the deployment graph::

   nsctl repo import --env local --dry-run
   nsctl repo import --env local
   nsctl repo list --env local

This imports deployed instances into the local manifest. It does not import
arbitrary repository directories. ``--all`` also includes instances that
are not currently deployed.

Shared control-plane workloads
------------------------------

A package registry or another service that must outlive individual
environments belongs in the control-plane manifest. Use
``nsctl control-plane repo add`` and ``nsctl control-plane apply``.
See :doc:`../plugins/development` for the Compose extension contract and
:doc:`../reference/manifests` for both manifest locations.
