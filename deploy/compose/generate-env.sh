#!/usr/bin/env sh
set -eu

output=${1:-.env}
if [ -e "$output" ]; then
  echo "refusing to overwrite existing $output" >&2
  exit 1
fi
if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl is required" >&2
  exit 1
fi

umask 077
postgres_password=$(openssl rand -hex 24)
redis_password=$(openssl rand -hex 24)
s3_access_key=$(openssl rand -hex 12)
s3_secret_key=$(openssl rand -base64 36 | tr -d '\n')
worker_auth_key=$(openssl rand -base64 32 | tr -d '\n')

{
  echo "MEM_IMAGE_TAG=local"
  echo "MEM_BIND_ADDRESS=127.0.0.1"
  echo "MEM_EDGE_PORT=8080"
  echo "MEM_MAX_BODY_SIZE=256m"
  echo "MEM_REGISTRATION_MODE=first_user"
  echo "MEM_LOG_LEVEL=info"
  echo "MEM_WORKER_GRPC_MAX_WORKERS=8"
  echo "MEM_WORKER_EXTRAS="
  echo "MEM_S3_BUCKET=mem"
  echo "MEM_POSTGRES_PASSWORD=$postgres_password"
  echo "MEM_REDIS_PASSWORD=$redis_password"
  echo "MEM_S3_ACCESS_KEY=$s3_access_key"
  echo "MEM_S3_SECRET_KEY=$s3_secret_key"
  echo "MEM_WORKER_AUTH_KEY_ID=memd-primary"
  echo "MEM_WORKER_AUTH_KEY_B64=$worker_auth_key"
} >"$output"

echo "wrote $output with mode 0600"
