#!/usr/bin/env bash
# scripts/dev_down.sh — stop the bare-metal mem stack started by dev_up.sh.
#
# Stops memd, worker, MinIO, and PostgreSQL (in reverse start order). Runtime
# DATA is preserved (.dev/pgdata, .dev/miniodata) so the next dev_up.sh is fast
# and the demo data survives. Pass --purge to also delete the data dirs.
set -uo pipefail

REPO_ROOT="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." && pwd )"
DEV_DIR="${REPO_ROOT}/.dev"
RUN_DIR="${DEV_DIR}/run"
PGDATA="${DEV_DIR}/pgdata"
MINIO_DATA="${DEV_DIR}/miniodata"

PURGE=0
[[ "${1:-}" == "--purge" ]] && PURGE=1

if [[ -t 1 ]]; then C_G=$'\033[32m'; C_Y=$'\033[33m'; C_B=$'\033[34m'; C_0=$'\033[0m'
else C_G=""; C_Y=""; C_B=""; C_0=""; fi
log()  { echo "${C_B}==>${C_0} $*"; }
ok()   { echo "${C_G}OK ${C_0} $*"; }
warn() { echo "${C_Y}WARN${C_0} $*"; }

pid_on_port() { # echo PID listening on a TCP port (best effort)
  local p="$1" pid=""
  if command -v ss >/dev/null 2>&1; then
    pid=$(ss -ltnpH "sport = :$p" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1)
  fi
  if [[ -z "$pid" ]] && command -v lsof >/dev/null 2>&1; then
    pid=$(lsof -ti "tcp:$p" -sTCP:LISTEN 2>/dev/null | head -1)
  fi
  echo "$pid"
}

stop_pid() { # stop_pid <name> <pid>
  local name="$1" pid="$2"
  log "stopping ${name} (pid ${pid})"
  # dev_up.sh launches services under `setsid`, so each becomes a process-group
  # leader (PGID == PID). Signal the whole group (negative pid) to also reap
  # child workers (e.g. the Python gRPC pool), then fall back to the bare pid.
  kill -TERM "-${pid}" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
  for _ in 1 2 3 4 5; do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
  kill -KILL "-${pid}" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
  ok "${name} stopped"
}

kill_pidfile() { # kill_pidfile <name> <pidfile> [port]
  local name="$1" pf="$2" port="${3:-}"
  local pid=""
  if [[ -f "$pf" ]]; then
    pid=$(cat "$pf" 2>/dev/null || true)
    rm -f "$pf"
  fi
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    stop_pid "$name" "$pid"
    return
  fi
  # Fallback: no/stale pidfile — find the process by its listening port. This
  # catches services that dev_up.sh found "already listening" before it could
  # record a pidfile.
  if [[ -n "$port" ]]; then
    local ppid; ppid=$(pid_on_port "$port")
    if [[ -n "$ppid" ]] && kill -0 "$ppid" 2>/dev/null; then
      stop_pid "${name} (via port ${port})" "$ppid"
      return
    fi
  fi
  warn "${name} not running"
}

# memd, worker, minio via pidfiles (reverse order), with port fallback.
kill_pidfile "memd"   "${RUN_DIR}/memd.pid"   8787
kill_pidfile "worker" "${RUN_DIR}/worker.pid" 50051
kill_pidfile "minio"  "${RUN_DIR}/minio.pid"  9100

# Postgres via pg_ctl (graceful).
if [[ -f "${RUN_DIR}/pg_bin.path" ]]; then
  PG_BIN=$(cat "${RUN_DIR}/pg_bin.path")
  if [[ -x "${PG_BIN}/pg_ctl" ]] && "${PG_BIN}/pg_ctl" -D "$PGDATA" status >/dev/null 2>&1; then
    log "stopping postgres (pg_ctl -m fast)"
    "${PG_BIN}/pg_ctl" -D "$PGDATA" -m fast stop >/dev/null 2>&1 && ok "postgres stopped" || warn "pg_ctl stop returned non-zero"
  else
    warn "postgres not running"
  fi
else
  warn "postgres: no pg_bin.path (was it started by dev_up.sh?)"
fi

if (( PURGE )); then
  log "--purge: deleting runtime data dirs"
  rm -rf "$PGDATA" "$MINIO_DATA"
  ok "purged ${PGDATA} and ${MINIO_DATA}"
fi

ok "stack down."
