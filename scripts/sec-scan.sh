#!/usr/bin/env bash
# Security checks: Go vulnerability database (govulncheck), go vet, npm audit of the console dependencies,
# and a scan of the tracked files for secrets or private keys.
. "$(dirname "$0")/env.sh"

NODE_BIN="${MED_NODE_BIN:-/opt/homebrew/opt/node@22/bin}"
[ -x "$NODE_BIN/node" ] && export PATH="$NODE_BIN:$PATH"
fail=0

log "== govulncheck"
go -C "$MED_SRC" run golang.org/x/vuln/cmd/govulncheck@latest ./... || fail=1

log "== go vet"
go -C "$MED_SRC" vet ./... || fail=1

log "== npm audit (production dependencies)"
(cd "$MED_SRC/ui" && npm audit --omit=dev --audit-level=moderate) || fail=1

log "== secrets in tracked files"
if git -C "$MED_SRC" rev-parse >/dev/null 2>&1; then
  if git -C "$MED_SRC" grep -nE -- '-----BEGIN [A-Z ]*PRIVATE KEY-----|(PASSWORD|SECRET|TOKEN)=[A-Za-z0-9/+]{12,}' -- ':!scripts/sec-scan.sh'; then
    log "possible secret committed"
    fail=1
  fi
fi

[ "$fail" = 0 ] && log "security scan: clean" || die "security scan found problems"
