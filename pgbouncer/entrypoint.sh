#!/bin/sh
# Writes pgbouncer.ini and userlist.txt from the same .db.env the database
# uses, then runs pgbouncer as an unprivileged user (it refuses to run as root).
set -eu

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"

CONF_DIR=/etc/pgbouncer-run
mkdir -p "$CONF_DIR"

# TLS for clients: the key is copied so it can be owned by the pgbouncer user
# with 0600, whatever its ownership on the host.
TLS_LINES=""
if [ -f /certs/server.crt ] && [ -f /certs/server.key ]; then
  cp /certs/server.crt /certs/server.key "$CONF_DIR/"
  chmod 600 "$CONF_DIR/server.key"
  TLS_LINES="client_tls_sslmode = ${CLIENT_TLS_SSLMODE:-require}
client_tls_cert_file = $CONF_DIR/server.crt
client_tls_key_file = $CONF_DIR/server.key"
else
  echo "WARNING: /certs/server.crt or /certs/server.key not found, clients connect WITHOUT TLS" >&2
fi

printf '"%s" "%s"\n' "$POSTGRES_USER" "$POSTGRES_PASSWORD" > "$CONF_DIR/userlist.txt"

cat > "$CONF_DIR/pgbouncer.ini" <<INI
[databases]
* = host=${DB_HOST:-database} port=${DB_PORT:-5432}

[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
auth_type = scram-sha-256
auth_file = $CONF_DIR/userlist.txt
admin_users = $POSTGRES_USER
stats_users = $POSTGRES_USER

; transaction: a server connection is lent per transaction, so many clients
; (serverless functions) share a few real Postgres connections. Clients must
; not rely on session state (prepared statements, SET, advisory locks).
pool_mode = ${POOL_MODE:-transaction}
max_client_conn = ${MAX_CLIENT_CONN:-1000}
default_pool_size = ${DEFAULT_POOL_SIZE:-20}
reserve_pool_size = ${RESERVE_POOL_SIZE:-5}
; Below Postgres' idle_session_timeout (5min), so pgbouncer closes idle
; server connections before Postgres kills them.
server_idle_timeout = 240
ignore_startup_parameters = extra_float_digits,options

$TLS_LINES
INI

chown -R bouncer:bouncer "$CONF_DIR"
exec su-exec bouncer pgbouncer "$CONF_DIR/pgbouncer.ini"
