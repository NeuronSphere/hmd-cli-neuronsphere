
echo '##HMD_STEP env=local instance=exec-probe rid=hmd_ms_deployment-local-reg1-none-1879956740813'
echo  '####'
echo 'exec-probe:exec-probe: 0.1'
export HMD_REPO_INSTANCE_DEPLOYMENT_ID=hmd_ms_deployment-local-reg1-none-1879956740813
hmd --debug  --repo-name exec-probe --repo-version 0.1  deploy   --instance-name exec-probe --environment local  --deployment-id local --config-file STDIN <<'EOF'
{
  "dependencies": {
    "database": {
      "dependencies": {
        "base-vpc": {
          "dependencies": {},
          "instance_name": "base-vpc",
          "version": "0.2.41",
          "deployment_id": "local",
          "repo_name": "hmd-vpc",
          "hmd_resources": [
            {
              "resource_name": "base-vpc_hmd-vpc_local_local_reg1_none",
              "resource_definition": {
                "resource_namespace": "network.neuronsphere.io",
                "resource_definition_name": "vpc",
                "version": "0.1.0"
              },
              "output": {
                "cidr_block": "10.100.0.0/16",
                "db_subnet_group_name": "hmd-local-db-subnets",
                "subnet_ids": [
                  "subnet-6097e3ef",
                  "subnet-9fa02bc9"
                ],
                "vpc_id": "vpc-51902764"
              },
              "tags": []
            }
          ]
        },
        "datadog-lambda": {
          "dependencies": {},
          "instance_name": "local-neuronsphere",
          "version": "0.1.0",
          "deployment_id": "local",
          "repo_name": "hmd-cli-neuronsphere",
          "hmd_resources": [
            {
              "resource_name": "neuronsphere_default-94d83e98",
              "resource_definition": {
                "resource_namespace": "network.neuronsphere.io",
                "resource_definition_name": "docker-network",
                "version": "0.1.0"
              },
              "output": {
                "driver": "bridge",
                "network_name": "neuronsphere_default-94d83e98"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "platform",
                  "value": "local"
                }
              ]
            },
            {
              "resource_name": "ns-local-94d83e98-compute",
              "resource_definition": {
                "resource_namespace": "compute.neuronsphere.io",
                "resource_definition_name": "compute-node",
                "version": "0.1.0"
              },
              "output": {
                "node_group_name": "ns-local-94d83e98-compute"
              },
              "tags": [
                {
                  "key": "cluster_type",
                  "value": "k3s"
                },
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                }
              ]
            },
            {
              "resource_name": "ns-local-94d83e98-traefik",
              "resource_definition": {
                "resource_namespace": "kubernetes.neuronsphere.io",
                "resource_definition_name": "ingress-controller",
                "version": "0.1.0"
              },
              "output": {
                "ingress_class": "alb",
                "name": "traefik",
                "namespace": "kube-system"
              },
              "tags": [
                {
                  "key": "cluster_type",
                  "value": "k3s"
                },
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                }
              ]
            },
            {
              "resource_name": "local-service-hmd_ms_dbaccount",
              "resource_definition": {
                "resource_namespace": "application.neuronsphere.io",
                "resource_definition_name": "microservice",
                "version": "0.1.0"
              },
              "output": {
                "api_base_url": "http://localhost/local/hmd_ms_dbaccount"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "repo_class",
                  "value": "hmd-ms-dbaccount"
                }
              ]
            },
            {
              "resource_name": "local-service-hmd_ms_deployment",
              "resource_definition": {
                "resource_namespace": "application.neuronsphere.io",
                "resource_definition_name": "microservice",
                "version": "0.1.0"
              },
              "output": {
                "api_base_url": "http://localhost/hmd_ms_deployment"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "repo_class",
                  "value": "hmd-ms-deployment"
                }
              ]
            },
            {
              "resource_name": "local-service-hmd_ms_naming",
              "resource_definition": {
                "resource_namespace": "application.neuronsphere.io",
                "resource_definition_name": "microservice",
                "version": "0.1.0"
              },
              "output": {
                "api_base_url": "http://localhost/hmd_ms_naming"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "repo_class",
                  "value": "hmd-ms-naming"
                }
              ]
            },
            {
              "resource_name": "local-service-hmd_ms_artifact_lib",
              "resource_definition": {
                "resource_namespace": "application.neuronsphere.io",
                "resource_definition_name": "microservice",
                "version": "0.1.0"
              },
              "output": {
                "api_base_url": "http://localhost/hmd_ms_artifact_lib"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "repo_class",
                  "value": "hmd-ms-artifact-lib"
                }
              ]
            }
          ]
        },
        "rds-loggroup": {
          "dependencies": {},
          "instance_name": "local-neuronsphere",
          "version": "0.1.0",
          "deployment_id": "local",
          "repo_name": "hmd-cli-neuronsphere",
          "hmd_resources": [
            {
              "resource_name": "neuronsphere_default-94d83e98",
              "resource_definition": {
                "resource_namespace": "network.neuronsphere.io",
                "resource_definition_name": "docker-network",
                "version": "0.1.0"
              },
              "output": {
                "driver": "bridge",
                "network_name": "neuronsphere_default-94d83e98"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "platform",
                  "value": "local"
                }
              ]
            },
            {
              "resource_name": "ns-local-94d83e98-compute",
              "resource_definition": {
                "resource_namespace": "compute.neuronsphere.io",
                "resource_definition_name": "compute-node",
                "version": "0.1.0"
              },
              "output": {
                "node_group_name": "ns-local-94d83e98-compute"
              },
              "tags": [
                {
                  "key": "cluster_type",
                  "value": "k3s"
                },
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                }
              ]
            },
            {
              "resource_name": "ns-local-94d83e98-traefik",
              "resource_definition": {
                "resource_namespace": "kubernetes.neuronsphere.io",
                "resource_definition_name": "ingress-controller",
                "version": "0.1.0"
              },
              "output": {
                "ingress_class": "alb",
                "name": "traefik",
                "namespace": "kube-system"
              },
              "tags": [
                {
                  "key": "cluster_type",
                  "value": "k3s"
                },
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                }
              ]
            },
            {
              "resource_name": "local-service-hmd_ms_dbaccount",
              "resource_definition": {
                "resource_namespace": "application.neuronsphere.io",
                "resource_definition_name": "microservice",
                "version": "0.1.0"
              },
              "output": {
                "api_base_url": "http://localhost/local/hmd_ms_dbaccount"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "repo_class",
                  "value": "hmd-ms-dbaccount"
                }
              ]
            },
            {
              "resource_name": "local-service-hmd_ms_deployment",
              "resource_definition": {
                "resource_namespace": "application.neuronsphere.io",
                "resource_definition_name": "microservice",
                "version": "0.1.0"
              },
              "output": {
                "api_base_url": "http://localhost/hmd_ms_deployment"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "repo_class",
                  "value": "hmd-ms-deployment"
                }
              ]
            },
            {
              "resource_name": "local-service-hmd_ms_naming",
              "resource_definition": {
                "resource_namespace": "application.neuronsphere.io",
                "resource_definition_name": "microservice",
                "version": "0.1.0"
              },
              "output": {
                "api_base_url": "http://localhost/hmd_ms_naming"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "repo_class",
                  "value": "hmd-ms-naming"
                }
              ]
            },
            {
              "resource_name": "local-service-hmd_ms_artifact_lib",
              "resource_definition": {
                "resource_namespace": "application.neuronsphere.io",
                "resource_definition_name": "microservice",
                "version": "0.1.0"
              },
              "output": {
                "api_base_url": "http://localhost/hmd_ms_artifact_lib"
              },
              "tags": [
                {
                  "key": "deployment_id",
                  "value": "local"
                },
                {
                  "key": "environment",
                  "value": "local"
                },
                {
                  "key": "repo_class",
                  "value": "hmd-ms-artifact-lib"
                }
              ]
            }
          ]
        }
      },
      "instance_name": "environment-db",
      "version": "0.8.51",
      "deployment_id": "local",
      "repo_name": "hmd-postgres-rds",
      "hmd_resources": [
        {
          "resource_name": "environment-db_hmd-postgres-rds_local_local_reg1_none",
          "resource_definition": {
            "resource_namespace": "database.neuronsphere.io",
            "resource_definition_name": "postgres",
            "version": "0.1.0"
          },
          "output": {
            "engine_version": "17.9",
            "host": "hmd_db-local",
            "port": 5432,
            "secret_name": "environment-db_hmd-postgres-rds_local_local_reg1_none_db-secret"
          },
          "tags": []
        }
      ]
    }
  },
  "instance_name": "exec-probe",
  "version": "0.1",
  "deployment_id": "local",
  "repo_name": "exec-probe",
  "capture": 1,
  "details": {
    "base-vpc": {
      "instance_name": "base-vpc",
      "version": "0.2.41",
      "deployment_id": "local",
      "repo_name": "hmd-vpc",
      "azs": [
        "us-west-2a",
        "us-west-2b"
      ],
      "cidr": "10.100.0.0/16",
      "database_subnets": [
        "10.100.64.0/20",
        "10.100.80.0/20"
      ],
      "expose_db": "true",
      "private_subnets": [
        "10.100.0.0/20",
        "10.100.16.0/20",
        "10.100.32.0/20",
        "10.100.48.0/20"
      ],
      "public_subnets": [
        "10.100.100.0/24",
        "10.100.101.0/24"
      ]
    },
    "local-neuronsphere": {
      "instance_name": "local-neuronsphere",
      "version": "0.1.0",
      "deployment_id": "local",
      "repo_name": "hmd-cli-neuronsphere"
    },
    "environment-db": {
      "instance_name": "environment-db",
      "version": "0.8.51",
      "deployment_id": "local",
      "repo_name": "hmd-postgres-rds",
      "allow_major_version_upgrade": false,
      "cluster_parameter_group": {
        "family": "aurora-postgresql17",
        "parameter_values": []
      },
      "db_engine": "aurora-postgresql",
      "db_parameter_group": {
        "family": "aurora-postgresql17",
        "parameter_values": []
      },
      "db_username": "postgres",
      "engine_mode": "provisioned",
      "engine_version": "17.9",
      "instance_type": "db.r7g.large",
      "publicly_accessible": true,
      "upgrade_config": {
        "check_collation_drift": true,
        "pause_on_instance_change": false,
        "pgbouncer": null,
        "row_count_tolerance_pct": 0,
        "run_post_upgrade_analyze": true,
        "skip_upgrade_safety": false,
        "snapshot_wait_timeout": 1800,
        "verify_row_counts": true
      },
      "db_host": "hmd_db-local",
      "db_port": 5432,
      "db_subnet_group_name": "hmd-local-db-subnets"
    },
    "exec-probe": {
      "instance_name": "exec-probe",
      "version": "0.1",
      "deployment_id": "local",
      "repo_name": "exec-probe",
      "capture": 1
    }
  }
}
EOF
