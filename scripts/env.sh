# Shared settings for the DxMed local runtime scripts (source this file). bash 3.2+ (macOS default).
set -euo pipefail

MED_SCRIPTS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MED_SRC="$(cd "$MED_SCRIPTS/.." && pwd)"
MRT="${MED_RUNTIME:-$HOME/med-runtime}"
MED_SECRETS="$MRT/secrets.env"
MED_BIN="$MRT/bin"
MED_LOGS="$MRT/logs"
MED_RUN="$MRT/run"
export MED_RUNTIME="$MRT"

# DxMed shares the DxPS PostgreSQL cluster (decision D2); its helpers are used, never modified.
DXPS_SCRIPTS="${DXPS_SCRIPTS:-$HOME/DxPS/dxps/scripts}"
[ -f "$DXPS_SCRIPTS/env.sh" ] || { echo "error: DxPS scripts not found at $DXPS_SCRIPTS (set DXPS_SCRIPTS)" >&2; exit 1; }
# shellcheck source=/dev/null
. "$DXPS_SCRIPTS/env.sh"

MED_PG_HOST=localhost
MED_PG_PORT=5433
MED_PG_DB="${MED_PG_DB:-dxps}"
MED_PG_CA="$DXPS_RUNTIME/pki/ca.crt"

MED_COMPONENTS="med-engine med-collector med-api med-ui"

med_port_of() {
  case "$1" in
    med-engine) echo 9301 ;; med-collector) echo 9302 ;; med-api) echo 8543 ;; med-ui) echo 8090 ;;
  esac
}

# med_secret KEY - value from the DxMed secrets file (never echoed by the scripts).
med_secret() { sed -n "s/^$1=//p" "$MED_SECRETS" | head -n 1; }

# med_env ROLE - exports the database settings for a component connecting as med_<ROLE>.
med_env() {
  local up
  up="$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')"
  export MED_DB_HOST="$MED_PG_HOST" MED_DB_PORT="$MED_PG_PORT" MED_DB_NAME="$MED_PG_DB" MED_DB_CA="$MED_PG_CA"
  export MED_DB_USER="med_$1"
  MED_DB_PASSWORD="$(med_secret "MED_${up}_PASSWORD")"
  export MED_DB_PASSWORD
}
