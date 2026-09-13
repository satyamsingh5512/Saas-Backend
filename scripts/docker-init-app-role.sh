#!/bin/sh
set -eu

# The official PostgreSQL image runs files in this directory as the bootstrap
# database user, before the application migrations run. Create the restricted
# runtime role here so migrations can grant it privileges through their normal
# default-privilege path.
if [ "${APP_DB_USER:-app_user}" != "app_user" ]; then
  echo "APP_DB_USER must remain app_user; the role-provisioning SQL is intentionally fixed to that name" >&2
  exit 1
fi

: "${APP_DB_PASSWORD:?APP_DB_PASSWORD must be set}"

psql \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set ON_ERROR_STOP=1 \
  --set app_user_password="$APP_DB_PASSWORD" \
  --file /docker-entrypoint-initdb.d/provision_app_role.sql
