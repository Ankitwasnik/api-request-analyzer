#!/usr/bin/env bash
# Prepares both databases for the benchmark (scenario A: raw tables). Safe to re-run.
#  - ClickHouse: builds api_requests_bench (typed, sorted for time-series queries), keeps it fed
#    from Airbyte's api_requests with a materialized view, and backfills existing rows.
#  - Postgres: VACUUM ANALYZE so the planner has fresh stats and index-only scans work.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
need docker

log "ClickHouse: rebuilding api_requests_bench"
ch --multiquery <<SQL
DROP VIEW IF EXISTS ${CLICKHOUSE_DB}.api_requests_bench_mv;
DROP TABLE IF EXISTS ${CLICKHOUSE_DB}.api_requests_bench;

CREATE TABLE ${CLICKHOUSE_DB}.api_requests_bench (
    request_id          UUID,
    tenant_id           Int32,
    user_id             Int64,
    request_time        DateTime64(6, 'UTC'),
    method              LowCardinality(String),
    uri                 String,
    route_template      LowCardinality(String),
    status_code         Int16,
    response_time_ms    Int32,
    request_size_bytes  Int32,
    response_size_bytes Int32,
    service_name        LowCardinality(String),
    trace_id            UUID,
    error_type          LowCardinality(Nullable(String)),
    client_ip_hash      String,
    user_agent_family   LowCardinality(String),
    is_retry            Bool
) ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMM(request_time)
ORDER BY (tenant_id, toStartOfHour(request_time), request_time, request_id);

-- Created before the backfill so no row is missed; overlap is collapsed by ReplacingMergeTree.
CREATE MATERIALIZED VIEW ${CLICKHOUSE_DB}.api_requests_bench_mv TO ${CLICKHOUSE_DB}.api_requests_bench AS
SELECT
    toUUID(request_id)                  AS request_id,
    toInt32(assumeNotNull(tenant_id))   AS tenant_id,
    assumeNotNull(user_id)              AS user_id,
    assumeNotNull(request_time)         AS request_time,
    assumeNotNull(method)               AS method,
    assumeNotNull(uri)                  AS uri,
    assumeNotNull(route_template)       AS route_template,
    toInt16(assumeNotNull(status_code)) AS status_code,
    toInt32(assumeNotNull(response_time_ms))    AS response_time_ms,
    toInt32(assumeNotNull(request_size_bytes))  AS request_size_bytes,
    toInt32(assumeNotNull(response_size_bytes)) AS response_size_bytes,
    assumeNotNull(service_name)         AS service_name,
    toUUID(assumeNotNull(trace_id))     AS trace_id,
    error_type                          AS error_type,
    assumeNotNull(client_ip_hash)       AS client_ip_hash,
    assumeNotNull(user_agent_family)    AS user_agent_family,
    assumeNotNull(is_retry)             AS is_retry
FROM ${CLICKHOUSE_DB}.api_requests
WHERE _ab_cdc_deleted_at IS NULL;

INSERT INTO ${CLICKHOUSE_DB}.api_requests_bench
SELECT
    toUUID(request_id), toInt32(assumeNotNull(tenant_id)), assumeNotNull(user_id),
    assumeNotNull(request_time), assumeNotNull(method), assumeNotNull(uri),
    assumeNotNull(route_template), toInt16(assumeNotNull(status_code)),
    toInt32(assumeNotNull(response_time_ms)), toInt32(assumeNotNull(request_size_bytes)),
    toInt32(assumeNotNull(response_size_bytes)), assumeNotNull(service_name),
    toUUID(assumeNotNull(trace_id)), error_type, assumeNotNull(client_ip_hash),
    assumeNotNull(user_agent_family), assumeNotNull(is_retry)
FROM ${CLICKHOUSE_DB}.api_requests FINAL
WHERE _ab_cdc_deleted_at IS NULL;

OPTIMIZE TABLE ${CLICKHOUSE_DB}.api_requests_bench FINAL;
SQL

log "Postgres: VACUUM ANALYZE"
psql_admin -c "VACUUM (ANALYZE, PARALLEL 0) api_requests"

log "Row counts"
printf 'postgres    %s\n' "$(psql_admin -tA -c 'SELECT count(*) FROM api_requests')"
printf 'clickhouse  %s\n' "$(ch --query "SELECT count() FROM ${CLICKHOUSE_DB}.api_requests_bench")"
