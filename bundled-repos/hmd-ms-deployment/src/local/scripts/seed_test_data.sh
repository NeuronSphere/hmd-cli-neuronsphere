#!/bin/bash
# Seed test data for the deployment service
#
# This script creates test entities in the deployment service database
# for local development and testing.
#
# Usage: ./seed_test_data.sh [API_URL]

set -e

API_URL="${1:-http://localhost:8080}"
GATEWAY_URL="${API_URL}/hmd_ms_deployment"

echo "Seeding deployment service test data..."
echo "API URL: $GATEWAY_URL"
echo ""

# Check if service is running
echo "Checking deployment service health..."
if ! curl -sf "$GATEWAY_URL/health" > /dev/null 2>&1; then
    echo "✗ Deployment service not responding at $GATEWAY_URL"
    echo "  Make sure local NeuronSphere is running with deployment plugin enabled"
    exit 1
fi
echo "✓ Deployment service is healthy"
echo ""

# The deployment service uses GraphQL for entity creation.
# Here we provide example mutations for seeding data.

echo "To seed data, run GraphQL mutations against $GATEWAY_URL/graphql"
echo ""
echo "Example mutations:"
echo ""

cat << 'EOF'
# 1. Create RepoClass
mutation {
  createRepoClass(input: {
    name: "hmd-ms-example"
    description: "Example microservice for testing"
  }) {
    nid
    name
  }
}

# 2. Add RepoClassVersion
mutation {
  addRepoClassVersion(input: {
    repoClassName: "hmd-ms-example"
    version: "0.1.1"
    dependencies: []
  }) {
    nid
    version
  }
}

# 3. Create Environment
mutation {
  createEnvironment(input: {
    type: "dev"
    accountNumber: "123456789012"
    hmdRegion: "us-west-2"
  }) {
    nid
    type
  }
}

# 4. Create DeploymentSet
mutation {
  createDeploymentSet(input: {
    name: "default-deployment-set"
    environments: [
      {environmentType: "dev", deploymentGate: null}
      {environmentType: "test", deploymentGate: {approvalRequired: true}}
    ]
  }) {
    nid
    name
  }
}

# 5. Create RepoInstance
mutation {
  createRepoInstance(input: {
    name: "example-instance"
    environmentType: "dev"
    repoClassName: "hmd-ms-example"
    repoClassVersion: "0.1.1"
    configuration: {}
  }) {
    nid
    name
  }
}
EOF

echo ""
echo "You can run these mutations using curl:"
echo ""
echo 'curl -X POST \\'
echo '  -H "Content-Type: application/json" \\'
echo '  -d '\''{"query": "mutation { ... }"}'\'' \\'
echo "  $GATEWAY_URL/graphql"
echo ""
echo "Or use the Django management command in the GUI container:"
echo "  docker exec neuronsphere-gui python manage.py seed_local_data"
