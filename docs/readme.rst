.. HMD hmd-cli-neuronsphere documentation master file

hmd-cli-neuronsphere
=========================
A CLI tool for controlling the Local NeuronSphere.

Additional Requirements
---------------------------------
- Docker Compose
- Docker

Configuration
---------------------------------
Required Environment Variables:

- `HMD_HOME`: folder to store data and configuration for Local NeuronSphere
- `HMD_REPO_HOME`: folder containing NeuronSphere compliant projects, it is mounted into some services

Commands
---------------------------------

- `hmd neuronsphere up`: starts all enabled services in the Local NeuronSphere
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
