#!/usr/bin/env bash
# Stops the DxMed components (SIGTERM, then SIGKILL after 15 s). PostgreSQL is shared with DxPS and left running.
. "$(dirname "$0")/env.sh"

pids=""
for c in $MED_COMPONENTS; do
  f="$MED_RUN/$c.pid"
  [ -f "$f" ] || continue
  pid="$(cat "$f")"
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    pids="$pids $pid"
    log "$c: stopping (pid $pid)"
  fi
  rm -f "$f"
done
for _ in $(seq 1 15); do
  left=""
  for pid in $pids; do kill -0 "$pid" 2>/dev/null && left="$left $pid"; done
  [ -z "$left" ] && { log "dxmed: stopped"; exit 0; }
  sleep 1
done
for pid in $left; do kill -9 "$pid" 2>/dev/null || true; done
log "dxmed: stopped (forced:$left)"
