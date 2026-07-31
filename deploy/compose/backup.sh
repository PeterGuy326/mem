#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
env_file=${MEM_COMPOSE_ENV_FILE:-"$script_dir/.env"}
compose_file="$script_dir/compose.yaml"
backup_root=${1:-"$script_dir/backups"}
mkdir -p "$backup_root"
backup_root=$(CDPATH='' cd -- "$backup_root" && pwd)
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
destination="$backup_root/.incomplete-$timestamp"
final_destination="$backup_root/$timestamp"

compose() {
  if [ -n "${MEM_COMPOSE_PROJECT_NAME:-}" ]; then
    docker compose -p "$MEM_COMPOSE_PROJECT_NAME" \
      --env-file "$env_file" -f "$compose_file" "$@"
  else
    docker compose --env-file "$env_file" -f "$compose_file" "$@"
  fi
}

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

if [ ! -f "$env_file" ]; then
  echo "missing environment file: $env_file" >&2
  exit 1
fi
if [ -e "$destination" ] || [ -e "$final_destination" ]; then
  echo "backup destination already exists for timestamp $timestamp" >&2
  exit 1
fi
mkdir -p "$destination/minio"
chmod 700 "$destination"

compose exec -T postgres pg_dump -U mem -d mem \
  --format=custom >"$destination/postgres.dump"

# The variables expand in the minio-client container, not in this host shell.
# shellcheck disable=SC2016
compose --profile tools \
  run --rm --no-deps -v "$destination/minio:/backup" minio-client \
  'mc alias set local http://minio:9000 "$MEM_S3_ACCESS_KEY" "$MEM_S3_SECRET_KEY"; mc mirror --overwrite "local/$MEM_S3_BUCKET" /backup'

(
  cd "$destination"
  checksum postgres.dump >SHA256SUMS
  find minio -type f -print | sort | while IFS= read -r object; do
    checksum "$object"
  done >>SHA256SUMS
)
mv "$destination" "$final_destination"
echo "backup written to $final_destination"
