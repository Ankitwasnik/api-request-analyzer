#!/usr/bin/env bash
# Installs Airbyte (abctl) if needed, then creates or updates the Postgres source,
# ClickHouse destination and connection through the Airbyte public API. Idempotent.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
need curl; need jq; need abctl

if ! curl -fs -m5 "$AIRBYTE_URL/api/public/v1/health" >/dev/null; then
  log "Airbyte not reachable at $AIRBYTE_URL; running abctl local install (first run can take ~30 min)"
  flags=(); [ "${AIRBYTE_LOW_RESOURCE:-0}" = 1 ] && flags+=(--low-resource-mode)
  abctl local install ${flags[@]+"${flags[@]}"}
fi

if [ -z "${AIRBYTE_CLIENT_ID:-}" ] || [ -z "${AIRBYTE_CLIENT_SECRET:-}" ]; then
  creds="$(abctl local credentials 2>&1 | sed 's/\x1b\[[0-9;]*m//g')"
  AIRBYTE_CLIENT_ID="$(awk '/Client-Id/{print $NF}' <<<"$creds")"
  AIRBYTE_CLIENT_SECRET="$(awk '/Client-Secret/{print $NF}' <<<"$creds")"
fi
[ -n "$AIRBYTE_CLIENT_ID" ] && [ -n "$AIRBYTE_CLIENT_SECRET" ] || die "could not read Airbyte client credentials"

API="$AIRBYTE_URL/api/public/v1"
TOKEN="$(curl -fsS -X POST "$AIRBYTE_URL/api/public/v1/applications/token" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg i "$AIRBYTE_CLIENT_ID" --arg s "$AIRBYTE_CLIENT_SECRET" \
        '{client_id:$i,client_secret:$s,"grant-type":"client_credentials"}')" | jq -r .access_token)"

# api METHOD PATH [JSON body]; prints the body, fails with the API's error message on non-2xx.
api() {
  local out code
  out="$(curl -sS -w '\n%{http_code}' -X "$1" "$API$2" -H "Authorization: Bearer $TOKEN" \
        -H 'Content-Type: application/json' ${3:+-d "$3"})"
  code="${out##*$'\n'}"; out="${out%$'\n'*}"
  [[ "$code" == 2* ]] || die "$1 $2 -> HTTP $code: $out"
  printf '%s' "$out"
}

# find_id KIND NAME ID_FIELD: id of the resource called NAME in the workspace, or empty.
find_id() { api GET "/$1?workspaceIds=$WORKSPACE_ID&limit=100" | jq -r --arg n "$2" --arg f "$3" '.data[]|select(.name==$n)|.[$f]' | head -1; }

# upsert KIND NAME ID_FIELD BODY: create, or update in place when NAME already exists.
upsert() {
  local id; id="$(find_id "$1" "$2" "$3")"
  if [ -n "$id" ]; then
    log "Updating $1 '$2'"
    api PATCH "/$1/$id" "$(jq 'del(.workspaceId)' <<<"$4")" | jq -r --arg f "$3" '.[$f]'
  else
    log "Creating $1 '$2'"
    api POST "/$1" "$4" | jq -r --arg f "$3" '.[$f]'
  fi
}

WORKSPACE_ID="$(api GET /workspaces | jq -r '.data[0].workspaceId')"

SOURCE_ID="$(upsert sources "$AIRBYTE_SOURCE_NAME" sourceId "$(jq -n \
  --arg ws "$WORKSPACE_ID" --arg name "$AIRBYTE_SOURCE_NAME" --arg host "$AIRBYTE_SOURCE_HOST" \
  --argjson port "$POSTGRES_PORT" --arg db "$POSTGRES_DB" --arg user "$AIRBYTE_PG_USER" \
  --arg pass "$AIRBYTE_PG_PASSWORD" --arg slot "$AIRBYTE_PG_SLOT" --arg pub "$AIRBYTE_PG_PUBLICATION" '{
    workspaceId:$ws, name:$name,
    configuration:{
      sourceType:"postgres", host:$host, port:$port, database:$db, username:$user, password:$pass,
      ssl_mode:{mode:"disable"}, tunnel_method:{tunnel_method:"NO_TUNNEL"},
      replication_method:{method:"CDC", replication_slot:$slot, publication:$pub}
    }}')")"

DESTINATION_ID="$(upsert destinations "$AIRBYTE_DESTINATION_NAME" destinationId "$(jq -n \
  --arg ws "$WORKSPACE_ID" --arg name "$AIRBYTE_DESTINATION_NAME" --arg host "$AIRBYTE_DESTINATION_HOST" \
  --arg db "$CLICKHOUSE_DB" --arg user "$AIRBYTE_CH_USER" --arg pass "$AIRBYTE_CH_PASSWORD" '{
    workspaceId:$ws, name:$name,
    configuration:{
      destinationType:"clickhouse", host:$host, port:"8123", protocol:"http", database:$db,
      username:$user, password:$pass, enable_json:false, tunnel_method:{tunnel_method:"NO_TUNNEL"}
    }}')")"

# Accept the UI-style "Every N MINUTES|HOURS" and convert it to the cron the create API requires.
if [[ "$AIRBYTE_SCHEDULE" =~ ^[Ee]very\ ([0-9]+)\ ([Mm][Ii][Nn][Uu][Tt][Ee][Ss]?|[Hh][Oo][Uu][Rr][Ss]?)$ ]]; then
  n="${BASH_REMATCH[1]}"
  case "$(tr '[:upper:]' '[:lower:]' <<<"${BASH_REMATCH[2]}")" in
    minute*) AIRBYTE_SCHEDULE="0 0/$n * * * ?" ;;
    hour*)   AIRBYTE_SCHEDULE="0 0 0/$n * * ?" ;;
  esac
fi

if [ "$AIRBYTE_SCHEDULE" = manual ]; then
  schedule='{"scheduleType":"manual"}'
else
  schedule="$(jq -n --arg c "$AIRBYTE_SCHEDULE" '{scheduleType:"cron",cronExpression:$c,cronTimeZone:"UTC"}')"
fi

CONNECTION_ID="$(upsert connections "$AIRBYTE_CONNECTION_NAME" connectionId "$(jq -n \
  --arg name "$AIRBYTE_CONNECTION_NAME" --arg src "$SOURCE_ID" --arg dst "$DESTINATION_ID" \
  --arg mode "$AIRBYTE_SYNC_MODE" --argjson schedule "$schedule" '{
    name:$name, sourceId:$src, destinationId:$dst, schedule:$schedule,
    namespaceDefinition:"destination", nonBreakingSchemaUpdatesBehavior:"propagate_columns",
    configurations:{streams:[{name:"api_requests", syncMode:$mode}]}
  }')")"

log "Airbyte: connection $CONNECTION_ID ready ($AIRBYTE_URL)"
echo "$CONNECTION_ID" > "$ROOT/.airbyte-connection-id"
