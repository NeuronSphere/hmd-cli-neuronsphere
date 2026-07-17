Developing Helm Chart Repos Against Local k3s
=============================================

NeuronSphere "Helm repo class" projects (``hmd-inf-*``, ``hmd-app-*``) deploy a
chart from ``src/helm`` onto an EKS cluster. Locally, **Floci** runs a real k3s
cluster (its EKS service with ``FLOCI_SERVICES_EKS_MOCK=false`` spawns a
privileged ``floci-eks-neuronsphere`` container), so you can iterate on chart
templates against a real Kubernetes API with close cloud parity **before**
building and deploying to the cloud.

The loop is::

    cd hmd-inf-clickhouse
    # edit src/helm/templates/...
    hmd helm build
    hmd helm deploy --local -cf meta-data/config_local.json \
        -in clickhouse -di local -e local -cc hmd -hr reg1
    kubectl -n clickhouse-local get pods -w

How the local path works
------------------------

``hmd helm deploy --local``:

1. **Finds the cluster automatically.** The kubeconfig is resolved in order:
   ``$KUBECONFIG`` → ``$HMD_LOCAL_K3S_KUBECONFIG`` →
   ``$HMD_HOME/.cache/k3s/kubeconfig`` (written by ``hmd neuronsphere up``) →
   ``~/.kube/config``. No export needed if the stack is up.
2. **Imports locally-built images into the cluster.** k3s reads images from its
   own containerd, not the host docker daemon. The chart is rendered with
   ``helm template`` and any referenced image that exists in the host docker
   cache is imported via
   ``docker save <img> | docker exec floci-eks-neuronsphere ctr -n k8s.io images import -``.
   Reference locally-built images by their ``<repo_name>:<version>`` tag and set
   ``image.pullPolicy: IfNotPresent`` (the CLI also ``--set``\ s this).
3. **Injects dummy standard values** (``account``, ``aws_region``, ALB/ACM/WAF
   placeholders) so AWS-oriented charts render, and resolves ``secret:``/``name:``
   config references from ``$HMD_HOME/.config/local-secrets.yaml``.
4. Runs ``helm upgrade --install --atomic`` into namespace
   ``<instance_name>-<deployment_id>``.

Kubernetes version parity
-------------------------

The local k3s tracks the **cloud EKS version** (``hmd-inf-eks-cluster``
``cluster_version``, currently ``1.34``). This matters: operator CRDs target the
cloud API (e.g. the External Secrets CRDs use ``selectableFields``, which requires
k8s >= 1.30) and will not install on an older cluster. The version is baked into
the ``hmd-img-k3s-floci`` wrapper image and requested via ``HMD_LOCAL_K3S_VERSION``
(default ``1.34``); keep the two in sync when the cloud bumps.

Pods reach Floci by hostname
----------------------------

Pods inside k3s can't resolve the ``neuronsphere`` Docker network alias on their
own, so ``provision_k3s_operators`` first installs a ``coredns-custom`` record
mapping ``neuronsphere`` / ``neuronsphere-workload`` to the Floci container IP.
Charts then use the **same in-network hostname as the cloud**
(``http://neuronsphere:4566``) — operators and workloads alike — for full parity.

Cluster operators (parity)
--------------------------

Charts render ``ExternalSecret`` / ``ScaledObject`` / ClickHouse operator
resources that require cluster operators. Those operators are themselves
NeuronSphere ``hmd-inf-*`` chart repos. Only External Secrets is installed
directly by ``hmd neuronsphere up`` right after the k3s cluster becomes ready
(``k3s_operators.provision_k3s_operators``), pulled as ``pre_build_artifacts``:

* ``hmd-inf-ext-secrets-crds`` then ``hmd-inf-ext-secrets`` — the External
  Secrets operator and the ``aws-secrets-manager`` ``ClusterSecretStore``. Locally
  the store authenticates with static credentials and the operator's
  ``AWS_ENDPOINT_URL`` points at Floci (``http://neuronsphere:4566``), so
  ``ExternalSecret`` resources sync **for real** from Floci Secrets Manager. The
  store name is identical to cloud, so consuming charts are unchanged.

The ClickHouse operator (CHOP), cert-manager, and KEDA instead deploy through the
real ms-deployment DAG as BOM entries contributed by the optional
``hmd-cli-plugin-ns-telemetry`` package — so they're only installed when that
plugin is present, not unconditionally.

Disable the directly-installed operators with
``HMD_LOCAL_NEURONSPHERE_ENABLE_K3S_OPERATORS=false``. Installation is
best-effort and never aborts ``up``.

Prerequisites for a chart deploy
--------------------------------

A chart's ``ExternalSecret`` / S3 config expect secrets and buckets to already
exist in Floci (integration suites seed their own fixtures; the CLI does not seed
them for you). For ``hmd-inf-clickhouse`` with the committed
``meta-data/config_local.json``:

.. code-block:: bash

    # Point awscli at Floci, dummy creds. NOTE: seed secrets in the SAME region the
    # ClusterSecretStore uses (aws_region=local), or ESO gets "Secret does not exist".
    export AWS_ENDPOINT_URL=http://localhost:4566 AWS_ACCESS_KEY_ID=test \
           AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=local

    # 1. Bucket the S3 disk / backups use
    aws --region local s3api create-bucket --bucket clickhouse-storage-local

    # 2. The users credentials secret the auth ExternalSecret reads
    #    (name = <user.instance>_<user.repo>_<user.did>_<env>_<region>_<customer>)
    aws --region local secretsmanager create-secret \
      --name clickhouse_hmd-inf-credentials_local_local_reg1_hmd \
      --secret-string '{"username":"default","password":"clickhouse"}'

    # 3. Build the image locally so it can be imported into k3s
    (cd ../hmd-img-clickhouse && hmd build)   # produces hmd-img-clickhouse:<version>

Single-node caveats
-------------------

The local k3s is a single node, so chart defaults that assume a multi-node/multi-AZ
cluster need local overrides in ``config_local.json`` (see ``hmd-inf-clickhouse``):

* **Topology spread / anti-affinity.** A zone-based ``topologySpreadConstraints``
  with ``whenUnsatisfiable: DoNotSchedule`` leaves pods ``Pending`` (the node has
  no ``topology.kubernetes.io/zone`` label). Setting it to ``[]`` may not help if
  the template falls back to a hardcoded default — supply a satisfiable
  hostname-based constraint with ``whenUnsatisfiable: ScheduleAnyway`` instead.
* **Replica counts** can be trimmed to 1 for a faster local loop.
* **helm timeout.** StatefulSets binding PVCs may need more than Helm's 5m default;
  set ``deploy.default_configuration.helm.timeout`` (or the instance config).

Known parity gaps
-----------------

* **No AWS Load Balancer Controller.** ``Ingress`` objects apply but get no ALB
  address; k3s Traefik serves ingress. Fine for template validation.
* **ACM / WAF / subnet values are dummies** — templates render but those
  AWS-specific fields are not functionally exercised locally.
* **S3 object-storage disks** (e.g. ClickHouse tiered storage against Floci S3)
  are an area still being ironed out; the ``hmd-inf-clickhouse`` local config runs
  on k3s ``local-path`` only for now.
