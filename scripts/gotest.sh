#!/usr/bin/env bash
# Runs "go test" with the throw-away test database dxmed_test (passwords stay in this process's environment).
# usage: scripts/gotest.sh [go test flags and packages]
. "$(dirname "$0")/env.sh"
start_pg >/dev/null
dsn() { # role
  local pw
  pw="$(med_secret "MED_$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')_PASSWORD" | sed 's/%/%25/g; s/@/%40/g; s/:/%3A/g; s/\//%2F/g')"
  printf 'postgres://med_%s:%s@%s:%s/dxmed_test?sslmode=verify-full&sslrootcert=%s' "$1" "$pw" "$MED_PG_HOST" "$MED_PG_PORT" "$MED_PG_CA"
}
MED_TEST_OWNER_DSN="$(dsn owner)" MED_TEST_APP_DSN="$(dsn app)" MED_TEST_REQUIRE_DB=1 \
  exec go -C "$MED_SRC" test "$@"
