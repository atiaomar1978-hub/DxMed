#!/usr/bin/env bash
# Runs every test with the coverage gates (decision D5): 100% on the engine packages, >= 95% Go overall,
# >= 90% for the console. Reports land in coverage/.
. "$(dirname "$0")/env.sh"

NODE_BIN="${MED_NODE_BIN:-/opt/homebrew/opt/node@22/bin}"
[ -x "$NODE_BIN/node" ] && export PATH="$NODE_BIN:$PATH"
mkdir -p "$MED_SRC/coverage"

go -C "$MED_SRC" vet ./...
"$MED_SCRIPTS/gotest.sh" -count=1 -p 1 -race -coverpkg=./internal/... -coverprofile=coverage/all.out ./...
go -C "$MED_SRC" run ./tools/covergate -profile coverage/all.out -min 95 | tee "$MED_SRC/coverage/go-summary.txt"
go -C "$MED_SRC" tool cover -html=coverage/all.out -o coverage/go.html

(
  cd "$MED_SRC/ui"
  [ -d node_modules ] || npm ci --no-audit --no-fund
  npx tsc --noEmit
  npx vitest run --coverage
)
log "tests: all gates passed (reports in $MED_SRC/coverage and ui/coverage)"
