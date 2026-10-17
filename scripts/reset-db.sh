#!/usr/bin/env bash
# Development only: stops DxMed, drops schema med in the DxPS database (DxPS tables are not touched), clears the
# DxMed spool directories and re-runs infra-setup.sh (migrate, admin user, example configuration).
. "$(dirname "$0")/env.sh"

[ "${1:-}" = "--yes" ] || die "this deletes every DxMed file, record, user and configuration in $MED_PG_DB; re-run with --yes"

"$MED_SRC/scripts/stop.sh"
start_pg
wait_port 5433 60
psql_as_super "$MED_PG_DB" <<'SQL'
DROP SCHEMA IF EXISTS med CASCADE;
SQL
log "postgres: schema med dropped in $MED_PG_DB"
for d in "$MRT/in" "$MRT/out" "$MRT/archive"; do
  [ -d "$d" ] && find "$d" -mindepth 1 -delete
done
log "spool: $MRT/in, $MRT/out and $MRT/archive cleared"
"$MED_SRC/scripts/infra-setup.sh"
"$MED_BIN/medctl" gen msc --dir "$MRT/in/msc" --files 3 --records 500 --dups 5
"$MED_BIN/medctl" gen pgw --dir "$MRT/in/pgw" --files 3 --records 50
log "spool: sample MSC and PGW files written; start with scripts/run-all.sh"
