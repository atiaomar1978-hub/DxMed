#!/usr/bin/env bash
# Prepares DxMed inside the DxPS PostgreSQL cluster: roles med_owner/med_app/med_ops/med_reader, schema med in
# database dxps, the throw-away test database dxmed_test, generated secrets, runtime directories and dev TLS
# certificates. Idempotent; existing passwords are kept.
. "$(dirname "$0")/env.sh"

mkdir -p "$MRT" "$MED_BIN" "$MED_LOGS" "$MED_RUN" "$MRT/pki" "$MRT/in/msc" "$MRT/in/pgw" "$MRT/out" "$MRT/archive"
chmod 700 "$MRT"

gen() { openssl rand -base64 48 | tr -d '/+=\n' | cut -c1-32; }
umask 077
touch "$MED_SECRETS"
for k in MED_OWNER_PASSWORD MED_APP_PASSWORD MED_OPS_PASSWORD MED_READER_PASSWORD MED_ADMIN_PASSWORD MED_SESSION_KEY; do
  if [ -z "$(med_secret "$k")" ]; then printf '%s=%s\n' "$k" "$(gen)" >>"$MED_SECRETS"; fi
done
chmod 600 "$MED_SECRETS"
umask 022
log "secrets: $MED_SECRETS (mode 600)"

start_pg
wait_port 5433 60

role_sql() { # name password
  printf "DO \$\$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN CREATE ROLE %s LOGIN; END IF; END \$\$;\n" "$1" "$1"
  printf "ALTER ROLE %s WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '%s';\n" "$1" "$2"
}

{
  echo "SET log_statement = 'none';"
  role_sql med_owner "$(med_secret MED_OWNER_PASSWORD)"
  role_sql med_app "$(med_secret MED_APP_PASSWORD)"
  role_sql med_ops "$(med_secret MED_OPS_PASSWORD)"
  role_sql med_reader "$(med_secret MED_READER_PASSWORD)"
  echo "GRANT med_reader TO med_ops;"
  echo "SELECT 'CREATE DATABASE dxmed_test OWNER med_owner' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'dxmed_test') \\gexec"
} | psql_as_super postgres

for db in "$MED_PG_DB" dxmed_test; do
  psql_as_super "$db" <<SQL
CREATE SCHEMA IF NOT EXISTS med AUTHORIZATION med_owner;
REVOKE ALL ON SCHEMA med FROM PUBLIC;
GRANT CONNECT ON DATABASE $db TO med_owner, med_app, med_ops, med_reader;
GRANT USAGE ON SCHEMA med TO med_app, med_ops, med_reader;
SQL
done
log "postgres: roles, schema med in $MED_PG_DB, database dxmed_test ready"

# Development TLS certificate for med-api and the UI, signed by a DxMed CA (not the DxPS CA).
if [ ! -f "$MRT/pki/server.crt" ]; then
  (
    cd "$MRT/pki"
    openssl ecparam -name prime256v1 -genkey -noout -out ca.key 2>/dev/null
    openssl req -x509 -new -key ca.key -sha256 -days 825 -subj "/CN=DxMed Dev CA" -out ca.crt 2>/dev/null
    openssl ecparam -name prime256v1 -genkey -noout -out server.key 2>/dev/null
    openssl req -new -key server.key -subj "/CN=localhost" -out server.csr 2>/dev/null
    printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' >server.ext
    openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -sha256 \
      -extfile server.ext -out server.crt 2>/dev/null
    rm -f server.csr server.ext
    chmod 600 ca.key server.key
  )
  log "pki: dev certificates in $MRT/pki"
fi

# Schema migrations as the owner, then the admin user and the example configuration.
( cd "$MED_SRC" && go build -o "$MED_BIN/medctl" ./cmd/medctl )
med_env owner
"$MED_BIN/medctl" migrate
med_env app
MED_ADMIN_PASSWORD="$(med_secret MED_ADMIN_PASSWORD)" "$MED_BIN/medctl" bootstrap --admin admin
"$MED_BIN/medctl" seed --root "$MRT"
log "dxmed: infrastructure ready (admin user 'admin'; password: med_secret MED_ADMIN_PASSWORD in $MED_SECRETS)"
