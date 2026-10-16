# Shared helpers. Source this file; do not run it.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Load .env as literal KEY=VALUE pairs (values may contain spaces, so `source` won't do).
# Variables already in the environment win; unset ones fall back to the defaults below.
load_env() {
  local file="$1" line key val
  [ -f "$file" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ''|'#'*) continue ;; esac
    key="${line%%=*}"; val="${line#*=}"
    if [ -z "${!key+x}" ]; then export "$key=$val"; fi
  done < "$file"
}
load_env "$ROOT/.env"
load_env "$ROOT/.env.example"

log()  { printf '\033[36m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "$1 is required but not installed"; }

dc() { docker compose "$@"; }
psql_admin() { dc exec -T postgres psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$@"; }
ch() { dc exec -T clickhouse clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" "$@"; }

wait_healthy() {
  local svc="$1" i
  for i in $(seq 1 60); do
    [ "$(docker inspect -f '{{.State.Health.Status}}' "$(dc ps -q "$svc")" 2>/dev/null)" = healthy ] && return 0
    sleep 2
  done
  die "$svc did not become healthy"
}
