# api-request-analyzer

Compares time-series analytics queries on Postgres and ClickHouse. A producer inserts synthetic API request logs into Postgres, Airbyte replicates them to ClickHouse with CDC, and a benchmark runs the same queries on both.

```
raw-data-producer ──COPY──▶ Postgres ──CDC (Airbyte)──▶ ClickHouse
                                │                           │
                                └────── benchmark ◀─────────┘
```

## Layout

| Path | Purpose |
|---|---|
| `raw-data-producer/` | Go data generator (`once` backfill or `continuous` mode); `schema.sql` defines the table |
| `benchmark/` | Go benchmark: `run` writes `results/<label>.json`, `report` builds `report.html` |
| `scripts/` | Setup: containers, CDC users, Airbyte source/destination/connection, sync, benchmark prep |
| `docs/cdc-clickhouse.md` | CDC runbook and caveats |
| `docker-compose.yml`, `postgres/` | Postgres 16 (logical WAL) and ClickHouse, with identical CPU/memory limits |

## Quick start

```bash
cp .env.example .env                       # optional; the scripts fall back to .env.example
scripts/setup.sh                           # containers, CDC users, Airbyte (needs docker, abctl, curl, jq)
(cd raw-data-producer && go run .)         # insert rows into Postgres
scripts/airbyte-sync.sh                    # replicate to ClickHouse
scripts/bench-prep.sh                      # build the tuned ClickHouse table, VACUUM ANALYZE Postgres
(cd benchmark && go run . run && go run . report)
```

Producer settings come from flags or `PRODUCER_*` variables (see `go run . -h` and `.env.example`).

## Table: `api_requests`

| Column | Type | Purpose |
|---|---|---|
| request_id | UUID (primary key) | Unique request identifier |
| tenant_id | INT | Tenant isolation and traffic distribution |
| user_id | BIGINT | User-level behavior analysis |
| request_time | TIMESTAMPTZ | Event timestamp |
| method | VARCHAR(10) | GET, POST, PUT, DELETE, PATCH |
| uri | TEXT | Actual request path |
| route_template | VARCHAR(255) | Normalized route, e.g. `/api/orders/{id}` |
| status_code | SMALLINT | HTTP response status |
| response_time_ms | INT | End-to-end latency |
| request_size_bytes | INT | Request payload size |
| response_size_bytes | INT | Response payload size |
| service_name | VARCHAR(64) | Backend service |
| trace_id | UUID | Distributed trace correlation |
| error_type | VARCHAR(64), nullable | Timeout, validation, dependency failure, etc. |
| client_ip_hash | VARCHAR(64) | Synthetic client identifier without real IPs |
| user_agent_family | VARCHAR(32) | browser, mobile, sdk, bot |
| is_retry | BOOLEAN | Whether the request is a retry |
