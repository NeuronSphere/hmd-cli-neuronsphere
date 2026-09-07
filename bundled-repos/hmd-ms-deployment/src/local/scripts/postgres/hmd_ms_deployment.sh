#!/bin/bash
set -e

# Create user and database for hmd-ms-deployment
psql -h db -U postgres <<-EOSQL
    CREATE USER hmd_ms_deployment WITH PASSWORD 'hmd_ms_deployment';
    CREATE DATABASE hmd_ms_deployment;
    GRANT ALL PRIVILEGES ON DATABASE hmd_ms_deployment TO hmd_ms_deployment;
EOSQL

# Connect to the new database to grant schema permissions
psql -h db -U postgres -d hmd_ms_deployment <<-EOSQL
    GRANT ALL ON SCHEMA public TO hmd_ms_deployment;
EOSQL

echo "Database 'hmd_ms_deployment' initialized successfully"
