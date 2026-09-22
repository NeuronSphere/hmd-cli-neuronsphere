.. HMD hmd-cli-neuronsphere documentation master file

hmd-cli-neuronsphere
=========================
A CLI tool for controlling the Local NeuronSphere.

.. note::

   This page describes ``hmd neuronsphere``, the Python CLI, which is fully
   supported and is what you want if your project's workloads come from
   installed plugin packages.

   For a new install, :doc:`reference/commands` is the recommended way to run the local
   platform: a single Go binary whose only prerequisite is a container engine
   its ``docker`` CLI can reach. Both front
   ends operate on the same ``HMD_HOME`` and can be used interchangeably.

Additional Requirements
---------------------------------
- Docker Compose
- Docker

Host Setup
---------------------------------
The local NeuronSphere stack uses ``neuronsphere`` and
``neuronsphere-workload`` as the canonical hostnames for the local AWS
emulator. They are registered as Docker network aliases on the compose
services so they resolve automatically inside the
``neuronsphere_default`` network. The host must also resolve them to a
loopback address so presigned S3/API URLs returned by in-network
services work from ``hmd build``, ``push-artifact``, and other CLI
commands running on your machine.

Run this once on macOS, Linux, or WSL2::

    sudo sh -c 'echo "127.0.0.1 neuronsphere neuronsphere-workload" >> /etc/hosts'

Native Windows is not supported. Use WSL2 instead.

``hmd neuronsphere up`` runs a pre-flight check on every invocation and
will print this exact instruction (and abort) if the entry is missing.

Configuration
---------------------------------
Required Environment Variables:

- `HMD_HOME`: folder to store data and configuration for Local NeuronSphere
- `HMD_REPO_HOME`: folder containing NeuronSphere compliant projects, it is mounted into some services

Commands
---------------------------------

- `hmd neuronsphere up`: starts all enabled services in the Local NeuronSphere. On a
  restart it re-syncs the bootstrapped core Resources (idempotently) so newly-defined
  core Resources appear without a full re-bootstrap. Pass ``--upgrade`` to also repull the
  latest images before starting.
- `hmd neuronsphere down`: stops all enabled services in the Local NeuronSphere
- `hmd neuronsphere run`: run within a NeuronSphere Microservice project to run it locally for testing
- `hmd neuronsphere update-images`: pull down updated images to run

Included Services
---------------------------------
All services are enabled by default.

- Gateway Proxy Server: used to forward calls to running NeuronSphere Microservices
- Postgres Database: relational storage backend for running NeuronSphere Microservices
- DynamoDB: NoSQL storage backend for running NeuronSphere Microservices
- Jupyter Lab Server: used to run Jupyter Notebooks, mounts HMD_REPO_HOME for project access, located at `http://localhost:8888/`
- NeuronSphere Transform Service: a local instance of the NeuronSphere Transform service to test NeuronSphere Transforms locally
- Trino: a Trino database instance to use with local Transforms `http://localhost:8081`
- Airflow: an Airflow instance used by the Transform Service, located at `http://localhost:175/`
- Explorer Portal: an instance of the NeuronSphere Explorer portal for local dashboard development, located at `http://localhost:8088/`
- Minio Object Storage: `http://localhost:9001`
- SQS Queues: `http://localhost:9235`

The following environment variables disable certain services by setting them to `'false'`.
For example, `hmd configure set-env HMD_LOCAL_NEURONSPHERE_ENABLE_TRINO false` will disable Trino.

- `HMD_LOCAL_NEURONSPHERE_ENABLE_TRINO`: disables Trino services
- `HMD_LOCAL_NEURONSPHERE_ENABLE_AIRFLOW`: disables Airflow
- `HMD_LOCAL_NEURONSPHERE_ENABLE_TRANSFORM`: disables Transform Service
- `HMD_LOCAL_NEURONSPHERE_ENABLE_DYNAMODB`: disables DynamoDb
- `HMD_LOCAL_NEURONSPHERE_ENABLE_APACHE_SUPERSET`: disables Explorer portal

Disabled services are not started and removed the next time you run `hmd neuronsphere up`.
