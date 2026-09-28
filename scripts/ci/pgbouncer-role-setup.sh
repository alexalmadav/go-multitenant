#!/usr/bin/env bash
# Starts PgBouncer on 127.0.0.1:6433 for the role isolation integration
# tests, in one of the two authentication modes the library supports:
#   auth_query  PgBouncer looks up each role's SCRAM verifier in PostgreSQL.
#   auth_file   the tests render the auth_file and reload PgBouncer.
# CI only: it expects PostgreSQL on 127.0.0.1:5432 as postgres/postgres.
set -euo pipefail

mode="${1:?usage: pgbouncer-role-setup.sh auth_query|auth_file}"
dir="${RUNNER_TEMP:-/tmp}/pgb"
mkdir -p "$dir"
export PGPASSWORD=postgres

command -v psql >/dev/null || { sudo apt-get update -q && sudo apt-get install -yq postgresql-client; }

# Fixed entries every mode needs: the admin console user.
printf '"postgres" "postgres"\n' > "$dir/userlist.base"

auth_lines=""
if [ "$mode" = auth_query ]; then
  psql -h 127.0.0.1 -U postgres -d test_multitenant -v ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE pgbouncer_auth LOGIN PASSWORD 'pgbouncer_auth';
CREATE FUNCTION public.pgbouncer_get_auth(p_usename text)
RETURNS TABLE (usename name, passwd text)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog AS
$$ SELECT usename, passwd FROM pg_catalog.pg_shadow WHERE usename = p_usename $$;
REVOKE ALL ON FUNCTION public.pgbouncer_get_auth(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.pgbouncer_get_auth(text) TO pgbouncer_auth;
SQL
  # PgBouncer logs in to PostgreSQL as auth_user itself, and cannot do that
  # from a verifier, so this entry must be plaintext.
  printf '"pgbouncer_auth" "pgbouncer_auth"\n' >> "$dir/userlist.base"
  auth_lines=$'auth_user = pgbouncer_auth\nauth_query = SELECT usename, passwd FROM public.pgbouncer_get_auth($1)'
elif [ "$mode" != auth_file ]; then
  echo "unknown mode: $mode" >&2
  exit 2
fi

cp "$dir/userlist.base" "$dir/userlist.txt"
cat > "$dir/pgbouncer.ini" <<EOF
[databases]
test_multitenant = host=127.0.0.1 port=5432 dbname=test_multitenant

[pgbouncer]
listen_addr = 127.0.0.1
listen_port = 6433
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/userlist.txt
$auth_lines
admin_users = postgres
pool_mode = transaction
max_client_conn = 2000
default_pool_size = 5
max_db_connections = 40
; A failed server login - a suspended or dropped role - makes PgBouncer refuse
; that role for this many seconds. The default of 15 would outlast the tests'
; retries after reactivating or repairing a role.
server_login_retry = 1
ignore_startup_parameters = extra_float_digits
EOF
chmod 644 "$dir"/*

# PgBouncer needs a descriptor per client and server connection; container
# defaults of 1,024 are too low for per-tenant pools.
docker run -d --name pgbouncer-role --network host --ulimit nofile=65536:65536 \
  -v "$dir:/etc/pgbouncer" edoburu/pgbouncer:latest >/dev/null

for _ in $(seq 1 30); do
  (echo > /dev/tcp/127.0.0.1/6433) >/dev/null 2>&1 && exit 0
  sleep 1
done
docker logs pgbouncer-role
echo "pgbouncer did not open port 6433" >&2
exit 1
