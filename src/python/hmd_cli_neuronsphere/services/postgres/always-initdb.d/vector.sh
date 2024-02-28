#!/bin/bash
set -e

db_name='vector'

psql -v --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" -tc "SELECT 1 FROM pg_database WHERE datname = '$db_name'" | \
grep -q 1 || \
psql -v --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE USER $db_name WITH PASSWORD '$db_name';
    CREATE DATABASE $db_name;
    GRANT ALL PRIVILEGES ON DATABASE $db_name TO $db_name;
EOSQL
psql -v --username "$POSTGRES_USER" --dbname "$db_name" <<-EOSQL
    CREATE EXTENSION vector;
EOSQL