echo '##HMD_STEP env=local instance=exec-probe rid=hmd_ms_deployment-local-reg1-rid-1809161200001'
echo  '####'
echo 'exec-probe:exec-probe: 0.1.0'
export HMD_REPO_INSTANCE_DEPLOYMENT_ID=hmd_ms_deployment-local-reg1-rid-1809161200001
hmd --debug  --repo-name exec-probe --repo-version 0.1.0  deploy   --instance-name exec-probe --environment local --account 000000000001 --deployment-id local --config-file STDIN <<'EOF'
{
  "instance_name": "exec-probe",
  "hmd_region": "reg1",
  "greeting": "$HOME has `backticks` and \\backslashes\\",
  "dependencies": {
    "database": {
      "instance_name": "environment-db",
      "repo_class_name": "hmd-postgres-rds",
      "hmd_resources": [
        {
          "resource_name": "environment-db",
          "resource_definition": {
            "resource_namespace": "aws.neuronsphere.io",
            "resource_definition_name": "aurora-postgres",
            "version": "0.1.0"
          },
          "output": {
            "host": "hmd_db-local",
            "port": 5432
          },
          "tags": []
        }
      ]
    },
    "cache": {
      "instance_name": "cache",
      "repo_class_name": "hmd-inf-redis",
      "hmd_resource_ref": {
        "repo_instance_deployment_id": "hmd_ms_deployment-local-reg1-rid-1809161200002"
      },
      "dependencies": {
        "cluster": {
          "instance_name": "eks-cluster",
          "repo_class_name": "hmd-inf-eks-cluster",
          "hmd_resource_ref": {
            "repo_instance_deployment_id": "hmd_ms_deployment-local-reg1-rid-1809161200003"
          }
        }
      }
    },
    "workers": [
      {
        "instance_name": "worker-a",
        "repo_class_name": "acme-worker",
        "hmd_resource_ref": {
          "repo_instance_deployment_id": "hmd_ms_deployment-local-reg1-rid-1809161200002"
        }
      },
      {
        "instance_name": "worker-b",
        "repo_class_name": "acme-worker",
        "hmd_resources": []
      }
    ]
  }
}
EOF


hmd_exit=$?
status="DEPLOYED"
if [ $hmd_exit != 0 ]; then
  status="FAILED"
fi

# if the status update failed, then fail...
if [ $? != 0 ]; then
    exit $?
fi
# otherwise use the status of the deployment command...
exit $hmd_exit
