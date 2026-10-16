#!/usr/bin/env bash
# Triggers a sync of the connection and waits for it to finish.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
need curl; need jq; need abctl
[ -f "$ROOT/.airbyte-connection-id" ] || die "run scripts/airbyte-setup.sh first"
CONNECTION_ID="$(cat "$ROOT/.airbyte-connection-id")"

creds="$(abctl local credentials 2>&1 | sed 's/\x1b\[[0-9;]*m//g')"
CID="${AIRBYTE_CLIENT_ID:-$(awk '/Client-Id/{print $NF}' <<<"$creds")}"
CS="${AIRBYTE_CLIENT_SECRET:-$(awk '/Client-Secret/{print $NF}' <<<"$creds")}"
API="$AIRBYTE_URL/api/public/v1"
TOKEN="$(curl -fsS -X POST "$API/applications/token" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg i "$CID" --arg s "$CS" '{client_id:$i,client_secret:$s,"grant-type":"client_credentials"}')" | jq -r .access_token)"
auth=(-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')

JOB_ID="$(curl -fsS -X POST "$API/jobs" "${auth[@]}" -d "$(jq -n --arg c "$CONNECTION_ID" '{connectionId:$c,jobType:"sync"}')" | jq -r .jobId)"
log "Sync job $JOB_ID started"
while :; do
  status="$(curl -fsS "$API/jobs/$JOB_ID" "${auth[@]}" | jq -r .status)"
  case "$status" in
    succeeded) log "Sync succeeded"; exit 0 ;;
    failed|cancelled|incomplete) die "sync $status (job $JOB_ID)" ;;
  esac
  sleep 5
done
