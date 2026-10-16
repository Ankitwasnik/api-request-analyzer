# CDC: Postgres → ClickHouse with Airbyte

`api_requests` is replicated from Postgres to ClickHouse with Airbyte (log-based CDC), so both databases hold the same rows for the benchmark. Everything below is automated by `scripts/`; this page explains what they do and the caveats.

```
raw-data-producer ──COPY──▶ Postgres (logical WAL) ──▶ Airbyte (CDC source) ──▶ ClickHouse destination ──▶ api_requests
                                                                                                   │ materialized view
                                                                                                   ▼
                                                                                          api_requests_bench
```

## Run it

| Step | Command | Effect |
|---|---|---|
| 1 | `scripts/setup.sh` | Starts Postgres and ClickHouse, then runs the three scripts below |
| | `scripts/postgres-setup.sh` | Creates the CDC role, publication and `pgoutput` replication slot (idempotent) |
| | `scripts/clickhouse-setup.sh` | Creates the Airbyte user with LZ4 compression (idempotent) |
| | `scripts/airbyte-setup.sh` | Installs Airbyte with `abctl` if absent, then creates or updates the source, destination and connection through the public API |
| 2 | `cd raw-data-producer && go run .` | Inserts rows |
| 3 | `scripts/airbyte-sync.sh` | Triggers a sync and waits for it to finish |
| 4 | `scripts/bench-prep.sh` | Builds `api_requests_bench` in ClickHouse and runs `VACUUM ANALYZE` on Postgres |

All names, credentials, hosts, the sync mode and the schedule come from `.env` (defaults in `.env.example`).

## Behaviour and caveats

- **Batch, not streaming.** Airbyte syncs on `AIRBYTE_SCHEDULE` (default every 15 minutes) or on demand. Run benchmarks only after the last sync has finished.
- **Primary key.** `api_requests` has `PRIMARY KEY (request_id)`, which CDC incremental sync and deduplication need. An existing table created without it needs `ALTER TABLE api_requests ADD PRIMARY KEY (request_id);`.
- **Insert-only workload.** The producer never updates or deletes. Airbyte captures `INSERT`, `UPDATE` and `DELETE`, but not `TRUNCATE` or `ALTER`.
- **Airbyte's table is not tuned.** Airbyte creates `api_requests` with its own engine, sort key and widened types (integers as `Int64`, timestamps as `DateTime64(3)`), plus metadata columns (`_airbyte_*`, `_ab_cdc_*`). The benchmark therefore reads `api_requests_bench`, which `bench-prep.sh` creates with proper types, `ReplacingMergeTree`, partitioning by month and `ORDER BY (tenant_id, toStartOfHour(request_time), request_time, request_id)`. A materialized view keeps it fed from `api_requests`, and the script backfills existing rows with `FINAL`. Re-run the script after a large load to rebuild it.
- **`FINAL`.** Deduplicated modes use `ReplacingMergeTree`; query `api_requests` with `FINAL`. The benchmark's row-count check does this.
- **Timestamps.** The destination truncates `request_time` to milliseconds. `api_requests_bench` is populated from it, so it carries millisecond precision despite the `DateTime64(6)` column.
- **One slot, one destination.** Do not point two connections at `AIRBYTE_PG_SLOT`.
- **WAL retention.** Postgres keeps WAL for the slot until Airbyte consumes it. If syncs stop while the producer keeps writing, the disk can fill; if the slot is invalidated (`wal_status = lost`), drop and recreate it and reset the connection. Check with:

  ```sql
  SELECT slot_name, active, wal_status,
         pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) AS retained_wal
  FROM pg_replication_slots;
  ```
- **Resources.** Airbyte (`abctl`) runs a Kubernetes cluster inside Docker: 4 CPUs / 8 GB recommended. Set `AIRBYTE_LOW_RESOURCE=1` for 2 CPUs / 8 GB. The first install can take about 30 minutes.

## Verify

```sql
-- Postgres
SELECT count(*), sum(response_time_ms), count(DISTINCT request_id) FROM api_requests;
-- ClickHouse
SELECT count(), sum(response_time_ms), uniqExact(request_id) FROM app_db.api_requests_bench;
```

`bench-prep.sh` prints both row counts. Before benchmarking, also check that ClickHouse has no merges running:

```sql
SELECT count() FROM system.merges;
```

## Teardown

```bash
docker exec api-request-analyzer-postgres psql -U postgres -d app_db \
  -c "SELECT pg_drop_replication_slot('airbyte_slot');"
abctl local uninstall --persisted    # --persisted also deletes Airbyte's data
docker compose down -v
```

## References

- Airbyte Postgres source (CDC): https://docs.airbyte.com/integrations/sources/postgres
- Airbyte ClickHouse destination: https://docs.airbyte.com/integrations/destinations/clickhouse
- Airbyte OSS quickstart (`abctl`): https://docs.airbyte.com/platform/using-airbyte/getting-started/oss-quickstart
