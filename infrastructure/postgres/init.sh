#!/bin/sh
set -eu
# psql variables safely quote the generated password as a SQL literal.
psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --set=app_password="$APP_DB_PASSWORD" --set=ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE hr_app LOGIN PASSWORD :'app_password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS;
SQL
