#!/usr/bin/env bash
# Project-specific validation runner. See docs/TESTING.md.
set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-unit}"
EXPECTED_MIGRATION_HEAD=13
MIGRATION_ROLLBACK_TARGET=11
CONTROL_TEST_DB=""
OWNED_TEST_DATABASE=""

log() {
  printf '\n==> %s\n' "$*"
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

cleanup_owned_test_databases() {
  local original_status=$?
  local cleanup_status=0
  trap - EXIT

  if [[ -n "$CONTROL_TEST_DB" && -n "$OWNED_TEST_DATABASE" ]] &&
    ! MEM_TEST_DB="$CONTROL_TEST_DB" \
      MEM_TEST_TARGET_DB="$OWNED_TEST_DATABASE" \
      testdb drop; then
    printf 'WARNING: failed to drop an owned disposable test database\n' >&2
    cleanup_status=1
  fi

  if [[ "$original_status" -eq 0 && "$cleanup_status" -ne 0 ]]; then
    exit "$cleanup_status"
  fi
  exit "$original_status"
}
trap cleanup_owned_test_databases EXIT

run_server() {
  require_command go
  log "Go build, vet and DB-free tests"
  (
    cd "${REPO_ROOT}/server"
    env -u MEM_TEST_DB go build ./...
    env -u MEM_TEST_DB go vet ./...
    env -u MEM_TEST_DB go test -count=1 ./...
  )
}

run_race() {
  require_command go
  log "Go race regression"
  (
    cd "${REPO_ROOT}/server"
    env -u MEM_TEST_DB go test -race -count=1 \
      ./internal/file \
      ./internal/folder \
      ./internal/memory \
      ./internal/handoff \
      ./internal/workspacelock \
      ./internal/workspacebundle \
      ./internal/workspacetransfer \
      ./internal/relator \
      ./internal/api \
      ./internal/apiclient \
      ./internal/tools/builtin \
      ./cmd/mem \
      ./cmd/mem-mcp
  )
}

run_worker() {
  require_command uv
  [[ -x "${REPO_ROOT}/worker/.venv/bin/python" ]] \
    || die "worker environment missing; run: make bootstrap"
  [[ -f "${REPO_ROOT}/worker/mem_worker/proto/processor_pb2.py" ]] \
    || die "worker protobuf stubs missing; run: (cd worker && make proto)"
  log "Worker pytest"
  (
    cd "${REPO_ROOT}/worker"
    uv run pytest
  )
}

run_web() {
  require_command npm
  [[ -d "${REPO_ROOT}/web/node_modules" ]] \
    || die "web dependencies missing; run: make bootstrap"
  log "Web typecheck, lint and build"
  (
    cd "${REPO_ROOT}/web"
    npm run typecheck
    npm run lint
    npm run build
  )
  log "Web memory acceptance"
  (cd "${REPO_ROOT}/web" && npm run test:memory)
  log "Web workspace-transfer acceptance"
  (cd "${REPO_ROOT}/web" && npm run test:transfer)
}

validate_test_database() {
  [[ -n "${MEM_TEST_DB:-}" ]] \
    || die "MEM_TEST_DB is required; run: make test-env-up"
  require_command go
  (
    cd "${REPO_ROOT}/server"
    go run ../scripts/testdb.go check
  )
  CONTROL_TEST_DB="$MEM_TEST_DB"
}

testdb() {
  (
    cd "${REPO_ROOT}/server"
    go run ../scripts/testdb.go "$@"
  )
}

with_fresh_test_database() {
  local label="$1"
  local callback="$2"
  local control_db="$MEM_TEST_DB"
  local target_db
  local run_status=0
  local cleanup_status=0

  target_db="$(MEM_TEST_DB="$control_db" testdb create "$label")" \
    || die "could not create an owned disposable database for $label"
  OWNED_TEST_DATABASE="$target_db"
  log "Created an owned disposable PostgreSQL database for $label"

  set +e
  (
    set -e
    MEM_TEST_DB="$target_db" \
    MEM_TEST_TARGET_DB="$target_db" \
      "$callback"
  )
  run_status=$?
  set -e

  MEM_TEST_DB="$control_db" MEM_TEST_TARGET_DB="$target_db" testdb drop \
    || cleanup_status=$?
  if [[ "$cleanup_status" -ne 0 ]]; then
    die "failed to drop the owned disposable database for $label"
  fi
  OWNED_TEST_DATABASE=""
  if [[ "$run_status" -ne 0 ]]; then
    return "$run_status"
  fi
}

assert_migration_version() {
  local expected="$1"
  local actual
  actual="$(MEM_TEST_TARGET_DB="$MEM_TEST_DB" testdb version)"
  [[ "$actual" == "$expected" ]] \
    || die "expected migration version $expected, got $actual"
}

run_migration_round_trip() {
  require_command go
  log "Migration validation and 0012/0013 rollback round trip"
  (
    cd "${REPO_ROOT}/server"
    go run github.com/pressly/goose/v3/cmd/goose@v3.22.1 \
      -dir internal/db/migrations validate
    go run github.com/pressly/goose/v3/cmd/goose@v3.22.1 \
      -dir internal/db/migrations postgres "$MEM_TEST_DB" up
  )
  assert_migration_version "$EXPECTED_MIGRATION_HEAD"
  MEM_TEST_TARGET_DB="$MEM_TEST_DB" testdb assert-state up
  (
    cd "${REPO_ROOT}/server"
    go run github.com/pressly/goose/v3/cmd/goose@v3.22.1 \
      -dir internal/db/migrations postgres "$MEM_TEST_DB" \
      down-to "$MIGRATION_ROLLBACK_TARGET"
  )
  assert_migration_version "$MIGRATION_ROLLBACK_TARGET"
  MEM_TEST_TARGET_DB="$MEM_TEST_DB" testdb assert-state down
  (
    cd "${REPO_ROOT}/server"
    go run github.com/pressly/goose/v3/cmd/goose@v3.22.1 \
      -dir internal/db/migrations postgres "$MEM_TEST_DB" up
  )
  assert_migration_version "$EXPECTED_MIGRATION_HEAD"
  MEM_TEST_TARGET_DB="$MEM_TEST_DB" testdb assert-state up
}

run_migrations_up() {
  (
    cd "${REPO_ROOT}/server"
    go run github.com/pressly/goose/v3/cmd/goose@v3.22.1 \
      -dir internal/db/migrations validate
    go run github.com/pressly/goose/v3/cmd/goose@v3.22.1 \
      -dir internal/db/migrations postgres "$MEM_TEST_DB" up
  )
  assert_migration_version "$EXPECTED_MIGRATION_HEAD"
}

run_postgres_tests() {
  local race_flag="${1:-}"
  local integration_log
  local -a required_tests=(
    TestMemoryPostgres
    TestHandoffPostgres
    TestWorkspaceTransferPostgres
    TestHandoffCrossAgentHTTPIntegration
    TestMemoryPathLifecycleIntegration
    TestWorkspacePathLockingIntegration
    TestFilePathLockingIntegration
    TestRecomputePerson
  )

  integration_log="$(mktemp "${TMPDIR:-/tmp}/mem-integration.XXXXXX")"
  log "PostgreSQL integration tests ${race_flag:-without race detector}"
  set +e
  (
    cd "${REPO_ROOT}/server"
    MEM_TEST_DB="$MEM_TEST_DB" go test \
      ${race_flag:+"$race_flag"} \
      -v -count=1 -p 1 -timeout 20m \
      -run '^(TestMemoryPostgres|TestHandoffPostgres|TestWorkspaceTransferPostgres|TestHandoffCrossAgentHTTPIntegration|TestMemoryPathLifecycleIntegration|TestWorkspacePathLockingIntegration|TestFilePathLockingIntegration|TestRecomputePerson)$' \
      ./internal/memory \
      ./internal/handoff \
      ./internal/workspacetransfer \
      ./internal/api \
      ./internal/folder \
      ./internal/file \
      ./internal/relator
  ) 2>&1 | tee "$integration_log"
  local test_status="${PIPESTATUS[0]}"
  set -e

  if [[ "$test_status" -ne 0 ]]; then
    rm -f "$integration_log"
    die "PostgreSQL integration command failed with exit code $test_status"
  fi

  local test_name
  for test_name in "${required_tests[@]}"; do
    if ! grep -q -- "--- PASS: ${test_name}" "$integration_log"; then
      rm -f "$integration_log"
      die "${test_name} did not execute and pass"
    fi
  done
  rm -f "$integration_log"
}

run_integration() {
  validate_test_database
  with_fresh_test_database migration run_migration_round_trip
  with_fresh_test_database integration run_postgres_integration
}

run_integration_race() {
  validate_test_database
  with_fresh_test_database integration_race run_postgres_integration_race
}

run_postgres_integration() {
  run_migrations_up
  run_postgres_tests
}

run_postgres_integration_race() {
  run_migrations_up
  run_postgres_tests "-race"
}

usage() {
  cat <<'EOF'
Usage: scripts/verify.sh <mode>

Modes:
  server             Go build, vet and DB-free tests
  worker             Worker pytest
  web                Web typecheck, lint, build and browser acceptance
  unit               server + worker + web (default)
  race               high-risk DB-free Go race tests
  integration        migration round trip + required PostgreSQL tests
  integration-race   the PostgreSQL suite under the Go race detector
  all                unit + race + integration + integration-race
EOF
}

case "$MODE" in
  server) run_server ;;
  worker) run_worker ;;
  web) run_web ;;
  unit)
    run_server
    run_worker
    run_web
    ;;
  race) run_race ;;
  integration) run_integration ;;
  integration-race) run_integration_race ;;
  all)
    run_server
    run_worker
    run_web
    run_race
    run_integration
    run_integration_race
    ;;
  -h|--help|help) usage ;;
  *)
    usage >&2
    die "unknown mode: $MODE"
    ;;
esac
