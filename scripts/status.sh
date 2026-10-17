#!/usr/bin/env bash
# Shows the DxMed components, their ports and the processing summary from medctl status.
. "$(dirname "$0")/env.sh"

for c in $MED_COMPONENTS; do
  port="$(med_port_of "$c")"
  state="stopped"
  if [ -f "$MED_RUN/$c.pid" ] && kill -0 "$(cat "$MED_RUN/$c.pid")" 2>/dev/null; then state="running (pid $(cat "$MED_RUN/$c.pid"))"; fi
  if test_port "$port"; then up="listening"; else up="closed"; fi
  printf '%-14s %-24s port %-5s %s\n' "$c" "$state" "$port" "$up"
done
if test_port 5433 && [ -x "$MED_BIN/medctl" ]; then
  med_env app
  "$MED_BIN/medctl" status
fi
