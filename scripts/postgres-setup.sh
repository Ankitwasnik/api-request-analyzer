#!/usr/bin/env bash
# Creates the CDC user, publication and replication slot in Postgres. Idempotent.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

log "Postgres: wal_level"
[ "$(psql_admin -tAc 'SHOW wal_level')" = logical ] || die "wal_level is not logical; recreate the container from docker-compose.yml"

log "Postgres: CDC role, publication, slot"
psql_admin \
  -v user="$AIRBYTE_PG_USER" -v pass="$AIRBYTE_PG_PASSWORD" -v db="$POSTGRES_DB" \
  -v slot="$AIRBYTE_PG_SLOT" -v pub="$AIRBYTE_PG_PUBLICATION" <<'SQL'
SELECT format('CREATE ROLE %I WITH LOGIN PASSWORD %L', :'user', :'pass')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'user') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN PASSWORD %L REPLICATION', :'user', :'pass') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'db', :'user') \gexec
SELECT format('GRANT USAGE ON SCHEMA public TO %I', :'user') \gexec
SELECT format('GRANT SELECT ON public.api_requests TO %I', :'user') \gexec
ALTER TABLE public.api_requests REPLICA IDENTITY DEFAULT;
SELECT format('CREATE PUBLICATION %I FOR TABLE public.api_requests', :'pub')
 WHERE NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = :'pub') \gexec
SELECT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = :'slot') AS has_slot \gset
\if :has_slot
\else
  SELECT pg_create_logical_replication_slot(:'slot', 'pgoutput');
\endif
SQL
log "Postgres: done"
