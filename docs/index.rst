.. hmd-cli-neuronsphere Documentation

nsctl documentation
===================

.. include:: _includes/paid-cloud.txt

Run local NeuronSphere environments reproducibly with ``nsctl``. A container
engine your ``docker`` CLI can reach is the only prerequisite -- Docker
Desktop, Colima, OrbStack, Rancher Desktop or a Linux daemon -- and every
tutorial runs end to end on your machine. Start with a tutorial, use the how-to
guides for focused tasks, and use the reference for the exact current CLI
contract. If a command cannot reach your engine, ``nsctl doctor`` says what it
resolved and why; see :doc:`how-to/choose-a-container-engine`.

.. toctree::
   :maxdepth: 2
   :caption: Tutorials

   tutorials/first-environment
   tutorials/create-repository-manifest
   tutorials/adopt-repository

.. toctree::
   :maxdepth: 1
   :caption: How-to guides

   how-to/choose-a-container-engine
   how-to/manage-environments
   how-to/add-workloads
   how-to/artifacts-and-locks
   Import a cloud BOM (cloud tenants) <how-to/import-cloud-bom>
   how-to/test-local-authorization
   how-to/use-agent-skills
   how-to/use-stacks
   how-to/install-cli-plugins

.. toctree::
   :maxdepth: 1
   :caption: Reference

   reference/commands
   reference/manifests
   reference/bacon

.. toctree::
   :maxdepth: 1
   :caption: Explanation

   explanation/architecture
   explanation/reconciliation
   explanation/artifacts-offline
   explanation/oci-distribution
   explanation/repository-adoption
   explanation/python-compatibility

.. toctree::
   :maxdepth: 1
   :caption: Supporting material

   licensing
   plugins/index
   proposals/index

Indices and tables
==================

* :ref:`genindex`
* :ref:`modindex`
* :ref:`search`
