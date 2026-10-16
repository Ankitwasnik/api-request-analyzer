#!/usr/bin/env bash
# Creates the Airbyte user in ClickHouse. Idempotent. Does not create api_requests (Airbyte owns it).
set -euo pipefail
source "$(dirname "$0")/lib.sh"

log "ClickHouse: Airbyte user"
# LZ4 is required: the Airbyte destination can't decode the ZSTD default of recent ClickHouse versions.
ch --multiquery <<SQL
CREATE USER IF NOT EXISTS ${AIRBYTE_CH_USER} IDENTIFIED BY '${AIRBYTE_CH_PASSWORD}'
    SETTINGS async_insert = 0, network_compression_method = 'LZ4';
ALTER USER ${AIRBYTE_CH_USER} IDENTIFIED BY '${AIRBYTE_CH_PASSWORD}'
    SETTINGS async_insert = 0, network_compression_method = 'LZ4';
GRANT CREATE DATABASE, CREATE TABLE, DROP TABLE, ALTER, TRUNCATE, INSERT, SELECT
    ON ${CLICKHOUSE_DB}.* TO ${AIRBYTE_CH_USER};
SQL
log "ClickHouse: done"
