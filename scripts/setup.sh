#!/usr/bin/env bash
# One-shot: start Postgres + ClickHouse, configure CDC users, set up Airbyte source/destination/connection.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
need docker

log "Starting containers"
dc up -d --build postgres clickhouse
wait_healthy postgres
wait_healthy clickhouse

"$ROOT/scripts/postgres-setup.sh"
"$ROOT/scripts/clickhouse-setup.sh"
"$ROOT/scripts/airbyte-setup.sh"

log "Setup complete. Run the producer, then trigger a sync: scripts/airbyte-sync.sh"
