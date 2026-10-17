#!/usr/bin/env bash
# Builds and starts DxMed locally: med-engine, med-collector, med-api (HTTPS 8543) and the console (HTTPS 8090).
# Requires scripts/infra-setup.sh to have run once. Idempotent: running components are left alone.
# usage: scripts/run-all.sh [--no-build]
. "$(dirname "$0")/env.sh"

NODE_BIN="${MED_NODE_BIN:-/opt/homebrew/opt/node@22/bin}"
[ -x "$NODE_BIN/node" ] && export PATH="$NODE_BIN:$PATH"
command -v node >/dev/null || die "Node.js 22+ not found (set MED_NODE_BIN)"
[ -s "$MED_SECRETS" ] || die "run scripts/infra-setup.sh first"

mkdir -p "$MED_BIN" "$MED_LOGS" "$MED_RUN"
start_pg
wait_port 5433 60

if [ "${1:-}" != "--no-build" ]; then
  version="$(git -C "$MED_SRC" describe --always --dirty 2>/dev/null || echo dev)"
  go -C "$MED_SRC" build -trimpath -ldflags "-s -w -X dxmed/internal/app.Version=$version" -o "$MED_BIN/" ./cmd/...
  log "go: binaries in $MED_BIN ($version)"
  (
    cd "$MED_SRC/ui"
    [ -d node_modules ] || npm ci --no-audit --no-fund
    NEXT_TELEMETRY_DISABLED=1 npx next build >"$MED_LOGS/ui-build.log" 2>&1 || { tail -30 "$MED_LOGS/ui-build.log"; exit 1; }
  )
  log "ui: production build ready"
fi

running() { [ -f "$MED_RUN/$1.pid" ] && kill -0 "$(cat "$MED_RUN/$1.pid")" 2>/dev/null; }

start() { # name command...
  local name="$1"; shift
  if running "$name"; then log "$name: already running (pid $(cat "$MED_RUN/$name.pid"))"; return 0; fi
  # own session: closing the starting terminal does not stop the component
  if command -v setsid >/dev/null; then
    nohup setsid "$@" >>"$MED_LOGS/$name.log" 2>&1 </dev/null &
  else
    nohup perl -MPOSIX -e 'POSIX::setsid(); exec @ARGV or die "exec: $!"' "$@" >>"$MED_LOGS/$name.log" 2>&1 </dev/null &
  fi
  echo $! >"$MED_RUN/$name.pid"
}

med_env app
export MED_LOG_LEVEL="${MED_LOG_LEVEL:-info}"
start med-engine "$MED_BIN/med-engine"
start med-collector "$MED_BIN/med-collector"
start med-api env MED_TLS_CERT="$MRT/pki/server.crt" MED_TLS_KEY="$MRT/pki/server.key" MED_TRUST_PROXY=1 "$MED_BIN/med-api"
# The console never sees database credentials.
unset MED_DB_PASSWORD
(
  cd "$MED_SRC/ui"
  start med-ui env MED_UI_CERT="$MRT/pki/server.crt" MED_UI_KEY="$MRT/pki/server.key" MED_API_URL="https://localhost:8543" \
    MED_API_CA="$MRT/pki/ca.crt" NODE_ENV=production node server.mjs
)

for c in $MED_COMPONENTS; do
  wait_port "$(med_port_of "$c")" 60
  log "$c: up on port $(med_port_of "$c") (pid $(cat "$MED_RUN/$c.pid"), log $MED_LOGS/$c.log)"
done
log "dxmed: console https://localhost:8090 (user admin; password: grep MED_ADMIN_PASSWORD $MED_SECRETS)"
log "dxmed: drop CDR files into $MRT/in/msc and $MRT/in/pgw (medctl gen msc|pgw --dir ...)"
