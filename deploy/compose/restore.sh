#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
env_file=${MEM_COMPOSE_ENV_FILE:-"$script_dir/.env"}
compose_file="$script_dir/compose.yaml"
backup_dir=${1:-}
confirmation=${2:-}

compose() {
  if [ -n "${MEM_COMPOSE_PROJECT_NAME:-}" ]; then
    docker compose -p "$MEM_COMPOSE_PROJECT_NAME" \
      --env-file "$env_file" -f "$compose_file" "$@"
  else
    docker compose --env-file "$env_file" -f "$compose_file" "$@"
  fi
}

if [ -z "$backup_dir" ] || [ "$confirmation" != "--confirm-empty-target" ]; then
  echo "usage: $0 BACKUP_DIR --confirm-empty-target" >&2
  exit 2
fi
if [ ! -f "$env_file" ]; then
  echo "missing environment file: $env_file" >&2
  exit 1
fi
if [ ! -f "$backup_dir/postgres.dump" ] || [ ! -d "$backup_dir/minio" ]; then
  echo "backup must contain postgres.dump and minio/" >&2
  exit 1
fi
backup_dir=$(CDPATH='' cd -- "$backup_dir" && pwd)
if [ ! -f "$backup_dir/SHA256SUMS" ]; then
  echo "backup must contain SHA256SUMS" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$backup_dir" && sha256sum -c SHA256SUMS)
else
  (cd "$backup_dir" && shasum -a 256 -c SHA256SUMS)
fi

compose up -d --wait postgres redis minio

table_count=$(compose exec -T postgres psql -U mem -d mem -Atc \
  "select count(*) from pg_catalog.pg_tables where schemaname = 'public';")
if [ "$table_count" != "0" ]; then
  echo "restore target is not empty; refusing to replace existing PostgreSQL data" >&2
  exit 1
fi

compose exec -T postgres pg_restore -U mem -d mem \
  --exit-on-error <"$backup_dir/postgres.dump"

# The variables expand in the minio-client container, not in this host shell.
# shellcheck disable=SC2016
compose --profile tools \
  run --rm --no-deps -v "$backup_dir/minio:/backup:ro" minio-client \
  'mc alias set local http://minio:9000 "$MEM_S3_ACCESS_KEY" "$MEM_S3_SECRET_KEY"; mc mb --ignore-existing "local/$MEM_S3_BUCKET"; mc mirror --overwrite /backup "local/$MEM_S3_BUCKET"'

echo "restore completed; start the full stack with docker compose up -d"
