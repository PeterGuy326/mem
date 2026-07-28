#!/usr/bin/env bash
# scripts/dev_up.sh — bring up the full mem stack on bare metal (NO Docker).
#
# Starts, in order, waiting for each to become healthy:
#   1. PostgreSQL (brew postgresql@NN + pgvector) — local cluster in .dev/pgdata
#   2. MinIO (S3-compatible) — .dev/bin/minio, data in .dev/miniodata, :9100
#   3. mem indexing worker (Python gRPC) — :50051, embedding/VLM/ASR/OCR
#   4. memd (Go HTTP API) — :8787, auto-migrates the DB on boot
#
# Idempotent: re-running detects already-running services and only (re)starts
# what is down. Runtime data + logs + pidfiles live under .dev/ (gitignored).
#
# Counterpart: scripts/dev_down.sh stops everything cleanly.
# Docs: docs/RUN_LOCAL.md
set -euo pipefail

# ---------------------------------------------------------------------------
# Paths & config
# ---------------------------------------------------------------------------
REPO_ROOT="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." && pwd )"
DEV_DIR="${REPO_ROOT}/.dev"
LOG_DIR="${DEV_DIR}/logs"
RUN_DIR="${DEV_DIR}/run"
BIN_DIR="${DEV_DIR}/bin"
PGDATA="${DEV_DIR}/pgdata"
MINIO_DATA="${DEV_DIR}/miniodata"

# Resolve brew prefix per-platform: /opt/homebrew (Apple Silicon),
# /usr/local (Intel mac), /home/linuxbrew/.linuxbrew (Linux). Prefer asking
# brew itself so this works on macOS, not just the Linux default.
BREW_PREFIX="${BREW_PREFIX:-$(command -v brew >/dev/null 2>&1 && brew --prefix || echo /home/linuxbrew/.linuxbrew)}"

# setsid detaches daemons from our session on Linux, but it does not exist on
# macOS. Use it when available; otherwise fall back to plain backgrounding (&),
# which is sufficient for a dev box.
SETSID="$(command -v setsid 2>/dev/null || true)"

# Service config (kept in sync with server/.env.example + worker/.env.example).
PG_PORT="${PG_PORT:-5432}"
PG_DB="${PG_DB:-mem}"
PG_USER="${PG_USER:-mem}"
PG_PASSWORD="${PG_PASSWORD:-mem}"
DB_URL="postgres://${PG_USER}:${PG_PASSWORD}@localhost:${PG_PORT}/${PG_DB}?sslmode=disable"

MINIO_ADDR="${MINIO_ADDR:-:9100}"
MINIO_CONSOLE_ADDR="${MINIO_CONSOLE_ADDR:-:9101}"
MINIO_ROOT_USER="${MINIO_ROOT_USER:-mem}"
MINIO_ROOT_PASSWORD="${MINIO_ROOT_PASSWORD:-mem-minio-password}"
MINIO_BUCKET="${MINIO_BUCKET:-mem}"

WORKER_PORT="${WORKER_PORT:-50051}"
MEMD_ADDR="${MEMD_ADDR:-:8787}"
OLLAMA_BASE_URL="${OLLAMA_BASE_URL:-http://localhost:11434}"

mkdir -p "$LOG_DIR" "$RUN_DIR" "$BIN_DIR" "$PGDATA" "$MINIO_DATA"

# ---------------------------------------------------------------------------
# Pretty logging
# ---------------------------------------------------------------------------
if [[ -t 1 ]]; then
  C_G=$'\033[32m'; C_Y=$'\033[33m'; C_R=$'\033[31m'; C_B=$'\033[34m'; C_0=$'\033[0m'
else C_G=""; C_Y=""; C_R=""; C_B=""; C_0=""; fi
log()  { echo "${C_B}==>${C_0} $*"; }
ok()   { echo "${C_G}OK ${C_0} $*"; }
warn() { echo "${C_Y}WARN${C_0} $*"; }
die()  { echo "${C_R}ERR${C_0}  $*" >&2; exit 1; }

port_busy() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && { exec 3>&- 3<&-; return 0; } || return 1; }

# Best-effort: echo the PID listening on a TCP port (so dev_down can stop a
# service that was already running before this dev_up — keeps pidfiles honest).
pid_on_port() {
  local p="$1" pid=""
  if command -v ss >/dev/null 2>&1; then
    pid=$(ss -ltnpH "sport = :$p" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1)
  fi
  if [[ -z "$pid" ]] && command -v lsof >/dev/null 2>&1; then
    pid=$(lsof -ti "tcp:$p" -sTCP:LISTEN 2>/dev/null | head -1)
  fi
  echo "$pid"
}

wait_for() { # wait_for <label> <max_sec> <cmd...>
  local label="$1" max="$2"; shift 2
  local i=0
  while (( i < max )); do
    if "$@" >/dev/null 2>&1; then ok "$label ready (${i}s)"; return 0; fi
    sleep 1; i=$((i+1))
  done
  die "$label did not become ready within ${max}s — see ${LOG_DIR}/"
}

# ---------------------------------------------------------------------------
# 1. PostgreSQL
# ---------------------------------------------------------------------------
detect_pg() {
  # Prefer the version pgvector was built for. brew pgvector ships .so files
  # under lib/postgresql@NN — pick a postgres keg that has both initdb and a
  # matching pgvector lib.
  # brew's pgvector bottle ships the module as vector.so on Linux but
  # vector.dylib on macOS — accept either so detection works cross-platform.
  local cand
  for cand in 17 18 16; do
    local kbin="${BREW_PREFIX}/opt/postgresql@${cand}/bin"
    if [[ -x "${kbin}/initdb" ]] && \
       ls "${BREW_PREFIX}"/Cellar/pgvector/*/lib/postgresql@${cand}/vector.so \
          "${BREW_PREFIX}"/Cellar/pgvector/*/lib/postgresql@${cand}/vector.dylib \
          >/dev/null 2>&1; then
      PG_BIN="$kbin"; PG_VER="$cand"; return 0
    fi
  done
  # Fall back to any postgres keg with initdb (pgvector may still resolve).
  for cand in 17 18 16; do
    local kbin="${BREW_PREFIX}/opt/postgresql@${cand}/bin"
    if [[ -x "${kbin}/initdb" ]]; then PG_BIN="$kbin"; PG_VER="$cand"; return 0; fi
  done
  die "no brew postgresql@NN with initdb found — run: brew install postgresql@17 pgvector"
}

ensure_pgvector_linked() { # make pgvector visible to the chosen postgres keg
  # Use pg_config to find the AUTHORITATIVE extension + library dirs (brew's
  # layout is share/postgresql@NN/extension, not share/postgresql/extension).
  local share_ext lib_dir
  share_ext="$("${PG_BIN}/pg_config" --sharedir)/extension"
  lib_dir="$("${PG_BIN}/pg_config" --pkglibdir)"
  local pgv_share pgv_lib
  pgv_share=$(ls -d "${BREW_PREFIX}"/Cellar/pgvector/*/share/postgresql@${PG_VER}/extension 2>/dev/null | head -1 || true)
  pgv_lib=$(ls -d "${BREW_PREFIX}"/Cellar/pgvector/*/lib/postgresql@${PG_VER} 2>/dev/null | head -1 || true)
  if [[ -d "$share_ext" && -n "$pgv_share" ]]; then
    cp -f "$pgv_share"/vector* "$share_ext"/ 2>/dev/null || true
  fi
  if [[ -d "$lib_dir" && -n "$pgv_lib" ]]; then
    # macOS bottle is vector.dylib, Linux is vector.so — copy whichever exists.
    cp -f "$pgv_lib"/vector.so "$pgv_lib"/vector.dylib "$lib_dir"/ 2>/dev/null || true
  fi
  if [[ -f "${share_ext}/vector.control" ]]; then
    ok "pgvector files linked into ${share_ext}"
  else
    warn "pgvector vector.control not found in ${share_ext} — CREATE EXTENSION may fail"
  fi
}

start_pg() {
  log "PostgreSQL (port ${PG_PORT})"
  detect_pg
  ok "using postgresql@${PG_VER} at ${PG_BIN}"
  ensure_pgvector_linked

  if [[ ! -f "${PGDATA}/PG_VERSION" ]]; then
    log "initdb -> ${PGDATA}"
    "${PG_BIN}/initdb" -D "$PGDATA" -U "$PG_USER" --auth=trust --encoding=UTF8 >"${LOG_DIR}/initdb.log" 2>&1 \
      || { cat "${LOG_DIR}/initdb.log"; die "initdb failed"; }
  fi

  if "${PG_BIN}/pg_ctl" -D "$PGDATA" status >/dev/null 2>&1; then
    ok "postgres already running"
  else
    log "starting postgres"
    # Unix socket in PGDATA to avoid /run permission issues; listen on localhost.
    "${PG_BIN}/pg_ctl" -D "$PGDATA" -l "${LOG_DIR}/postgres.log" \
      -o "-p ${PG_PORT} -k ${PGDATA} -c listen_addresses=localhost" start \
      || { tail -20 "${LOG_DIR}/postgres.log"; die "pg_ctl start failed"; }
  fi
  echo "$PG_BIN" > "${RUN_DIR}/pg_bin.path"
  echo "$PGDATA" > "${RUN_DIR}/pg_data.path"

  wait_for "postgres accepting connections" 30 \
    "${PG_BIN}/pg_isready" -h localhost -p "$PG_PORT" -U "$PG_USER"

  # Role + DB + extension (idempotent).
  local psql="${PG_BIN}/psql"
  "$psql" -h localhost -p "$PG_PORT" -U "$PG_USER" -d postgres -tAc \
    "SELECT 1 FROM pg_roles WHERE rolname='${PG_USER}'" | grep -q 1 || \
    "$psql" -h localhost -p "$PG_PORT" -U "$PG_USER" -d postgres -c \
      "CREATE ROLE ${PG_USER} LOGIN SUPERUSER PASSWORD '${PG_PASSWORD}'" 2>/dev/null || true
  # Set the password even if the role already exists (initdb -U created it).
  "$psql" -h localhost -p "$PG_PORT" -U "$PG_USER" -d postgres -c \
    "ALTER ROLE ${PG_USER} WITH LOGIN SUPERUSER PASSWORD '${PG_PASSWORD}'" >/dev/null 2>&1 || true
  "$psql" -h localhost -p "$PG_PORT" -U "$PG_USER" -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname='${PG_DB}'" | grep -q 1 || \
    "$psql" -h localhost -p "$PG_PORT" -U "$PG_USER" -d postgres -c "CREATE DATABASE ${PG_DB}" >/dev/null
  "$psql" -h localhost -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c \
    "CREATE EXTENSION IF NOT EXISTS vector" >/dev/null 2>&1 \
    || die "CREATE EXTENSION vector failed — pgvector not visible to postgresql@${PG_VER}"

  local ext
  ext=$("$psql" "$DB_URL" -tAc "SELECT extname FROM pg_extension WHERE extname='vector'")
  [[ "$ext" == "vector" ]] && ok "pgvector extension present (db=${PG_DB})" || die "pgvector extension missing"
}

# ---------------------------------------------------------------------------
# 2. MinIO
# ---------------------------------------------------------------------------
find_minio() { # echo path to a minio binary, or empty
  if [[ -x "${BIN_DIR}/minio" ]]; then echo "${BIN_DIR}/minio"; return; fi
  if [[ -x "${BREW_PREFIX}/bin/minio" ]]; then echo "${BREW_PREFIX}/bin/minio"; return; fi
  command -v minio 2>/dev/null || true
}
find_mc() { # echo path to an mc binary, or empty
  if [[ -x "${BIN_DIR}/mc" ]]; then echo "${BIN_DIR}/mc"; return; fi
  if [[ -x "${BREW_PREFIX}/bin/mc" ]]; then echo "${BREW_PREFIX}/bin/mc"; return; fi
  command -v mc 2>/dev/null || true
}

start_minio() {
  log "MinIO (${MINIO_ADDR})"
  local MINIO_BIN; MINIO_BIN=$(find_minio)
  [[ -n "$MINIO_BIN" ]] || die "minio binary not found (.dev/bin/minio, brew, or PATH) — install: brew install minio"
  ok "using minio at ${MINIO_BIN}"

  if port_busy "${MINIO_ADDR#:}"; then
    ok "minio already listening on ${MINIO_ADDR}"
    local existing; existing=$(pid_on_port "${MINIO_ADDR#:}")
    [[ -n "$existing" ]] && echo "$existing" > "${RUN_DIR}/minio.pid"
  else
    log "starting minio"
    MINIO_ROOT_USER="$MINIO_ROOT_USER" MINIO_ROOT_PASSWORD="$MINIO_ROOT_PASSWORD" \
      $SETSID "$MINIO_BIN" server "$MINIO_DATA" \
      --address "$MINIO_ADDR" --console-address "$MINIO_CONSOLE_ADDR" \
      >"${LOG_DIR}/minio.log" 2>&1 < /dev/null &
    echo $! > "${RUN_DIR}/minio.pid"
  fi

  wait_for "minio health" 30 \
    curl -fsS "http://localhost:${MINIO_ADDR#:}/minio/health/live"

  # Create the bucket via mc if available (memd also auto-creates it on boot,
  # so mc is optional — this just makes the bucket exist before memd starts).
  local MC_BIN; MC_BIN=$(find_mc)
  if [[ -n "$MC_BIN" ]]; then
    local MC=( env MC_CONFIG_DIR="${DEV_DIR}/mc" "$MC_BIN" )
    "${MC[@]}" alias set memlocal "http://localhost:${MINIO_ADDR#:}" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1 || true
    "${MC[@]}" mb --ignore-existing "memlocal/${MINIO_BUCKET}" >/dev/null 2>&1 || true
    ok "bucket ensured via mc: ${MINIO_BUCKET}"
  else
    warn "mc client not found; memd will auto-create bucket '${MINIO_BUCKET}' on boot"
  fi
}

# ---------------------------------------------------------------------------
# 3. Worker (Python gRPC)
# ---------------------------------------------------------------------------
start_worker() {
  log "mem worker (gRPC :${WORKER_PORT})"
  # Ensure the CLIP extra (open_clip_torch + torch CPU) is installed so the
  # visual embedder can run — without it, image indexing degrades to a
  # caption-text vector that the 512-d visual guard rejects, leaving
  # embeddings_visual empty and "image search" effectively disabled.
  if command -v uv >/dev/null 2>&1; then
    log "syncing worker deps (uv sync --extra clip --extra test --extra face)"
    # --extra test keeps pytest in the venv across restarts; without it a sync
    # would prune the test deps and `python -m pytest` breaks after dev_up.
    # --extra face keeps insightface (buffalo_l face detection) installed;
    # without it a sync would prune insightface and the Faces feature breaks.
    ( cd "${REPO_ROOT}/worker" && uv sync --extra clip --extra test --extra face >"${LOG_DIR}/worker_uv_sync.log" 2>&1 ) \
      || warn "uv sync --extra clip --extra test --extra face failed — see ${LOG_DIR}/worker_uv_sync.log; image search may be unavailable"
  else
    warn "uv not on PATH; skipping clip dependency sync (image search needs: cd worker && uv sync --extra clip)"
  fi
  local py="${REPO_ROOT}/worker/.venv/bin/python"
  [[ -x "$py" ]] || die "worker venv missing — run: (cd worker && uv sync --extra clip)"

  if port_busy "$WORKER_PORT"; then
    ok "worker already listening on :${WORKER_PORT}"
    local existing; existing=$(pid_on_port "$WORKER_PORT")
    [[ -n "$existing" ]] && echo "$existing" > "${RUN_DIR}/worker.pid"
  else
    log "starting worker"
    ( cd "${REPO_ROOT}/worker" && \
      env MEM_WORKER_GRPC_HOST=127.0.0.1 \
          MEM_WORKER_GRPC_PORT="$WORKER_PORT" \
          MEM_S3_ENDPOINT="http://localhost:${MINIO_ADDR#:}" \
          MEM_S3_BUCKET="$MINIO_BUCKET" \
          MEM_S3_ACCESS_KEY="$MINIO_ROOT_USER" \
          MEM_S3_SECRET_KEY="$MINIO_ROOT_PASSWORD" \
          MEM_S3_USE_SSL=false \
          OLLAMA_BASE_URL="$OLLAMA_BASE_URL" \
          OLLAMA_TIMEOUT=300 \
          OPENAI_BASE_URL="${OPENAI_BASE_URL:-}" \
          OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
          MEM_DEFAULT_EMBEDDING="${MEM_DEFAULT_EMBEDDING:-ollama:nomic-embed-text}" \
          MEM_DEFAULT_VISUAL_EMBEDDING="${MEM_DEFAULT_VISUAL_EMBEDDING:-clip:ViT-B-32}" \
          MEM_DEFAULT_VLM="${MEM_DEFAULT_VLM:-ollama:minicpm-v}" \
          MEM_LOG_LEVEL=INFO \
          $SETSID "$py" -m mem_worker.server >"${LOG_DIR}/worker.log" 2>&1 < /dev/null & \
      echo $! > "${RUN_DIR}/worker.pid" )
  fi

  wait_for "worker gRPC port" 40 bash -c "exec 3<>/dev/tcp/127.0.0.1/${WORKER_PORT}"
  # Confirm SERVING via a real gRPC HealthCheck.
  if "$py" - "$WORKER_PORT" >/dev/null 2>&1 <<'PY'
import sys, grpc
from mem_worker.proto import processor_pb2 as pb, processor_pb2_grpc as pbg
ch = grpc.insecure_channel(f"localhost:{sys.argv[1]}")
grpc.channel_ready_future(ch).result(timeout=10)
resp = pbg.ProcessorServiceStub(ch).HealthCheck(pb.HealthCheckRequest(), timeout=10)
sys.exit(0)
PY
  then ok "worker HealthCheck OK"; else warn "worker HealthCheck probe failed (port open; check ${LOG_DIR}/worker.log)"; fi
}

# ---------------------------------------------------------------------------
# 4. memd (Go HTTP API)
# ---------------------------------------------------------------------------
start_memd() {
  log "memd (HTTP ${MEMD_ADDR})"
  local memd_bin="${REPO_ROOT}/bin/memd"
  if [[ ! -x "$memd_bin" ]]; then
    log "building memd (bin/memd missing)"
    ( cd "${REPO_ROOT}/server" && go build -o "${REPO_ROOT}/bin/memd" ./cmd/memd ) || die "go build memd failed"
  fi

  if port_busy "${MEMD_ADDR#:}"; then
    ok "memd already listening on ${MEMD_ADDR}"
    local existing; existing=$(pid_on_port "${MEMD_ADDR#:}")
    [[ -n "$existing" ]] && echo "$existing" > "${RUN_DIR}/memd.pid"
  else
    log "starting memd (auto-migrates DB on boot)"
    env MEM_HTTP_ADDR="$MEMD_ADDR" \
        MEM_DB_URL="$DB_URL" \
        MEM_REDIS_URL="" \
        MEM_S3_ENDPOINT="http://localhost:${MINIO_ADDR#:}" \
        MEM_S3_BUCKET="$MINIO_BUCKET" \
        MEM_S3_ACCESS_KEY="$MINIO_ROOT_USER" \
        MEM_S3_SECRET_KEY="$MINIO_ROOT_PASSWORD" \
        MEM_S3_REGION="us-east-1" \
        MEM_WORKER_GRPC="localhost:${WORKER_PORT}" \
        MEM_LOG_LEVEL="info" \
        $SETSID "$memd_bin" >"${LOG_DIR}/memd.log" 2>&1 < /dev/null &
    echo $! > "${RUN_DIR}/memd.pid"
  fi

  wait_for "memd /healthz" 40 curl -fsS "http://localhost:${MEMD_ADDR#:}/healthz"
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
echo "${C_B}mem · bare-metal dev_up${C_0}  (repo: ${REPO_ROOT})"
start_pg
start_minio
start_worker
start_memd

echo
ok "stack is up:"
echo "    postgres : ${DB_URL}"
echo "    minio    : http://localhost:${MINIO_ADDR#:}  (console http://localhost:${MINIO_CONSOLE_ADDR#:}, user ${MINIO_ROOT_USER})"
echo "    worker   : localhost:${WORKER_PORT} (gRPC)"
echo "    memd     : http://localhost:${MEMD_ADDR#:}  (healthz OK)"
echo "    logs     : ${LOG_DIR}/"
echo
echo "Next: bash scripts/seed_demo_data.sh   # seed demo data + 6 search assertions (text · PDF · audio · CLIP image)"
echo "Stop: bash scripts/dev_down.sh"
