#!/usr/bin/env bash
# Isolated process-level acceptance for the canonical memory API, CLI and MCP.
set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/docker-compose.test.yml"
COMPOSE_PROJECT=""
PG_PORT="${MEM_ACCEPTANCE_PG_PORT:-0}"
S3_PORT="${MEM_ACCEPTANCE_S3_PORT:-0}"
HTTP_PORT_REQUESTED="${MEM_ACCEPTANCE_HTTP_PORT:-}"
HTTP_PORT=""
DB_NAME="mem_acceptance_test"
DB_URL=""
BASE_URL=""
E2E_ROOT="${REPO_ROOT}/.dev"
E2E_DIR=""
HTTP_LOCK_ROOT="${E2E_ROOT}/acceptance-port-locks"
HTTP_LOCK_DIR=""
MEMD_PID=""
COMPOSE_STARTED=false

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

compose() {
  MEM_TEST_PROJECT="$COMPOSE_PROJECT" \
  MEM_TEST_PG_PORT="$PG_PORT" \
  MEM_TEST_S3_PORT="$S3_PORT" \
  MEM_TEST_DB_NAME="$DB_NAME" \
    docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" --profile e2e "$@"
}

cleanup() {
  local original_status=$?
  local cleanup_status=0
  local attempt
  trap - EXIT INT TERM
  if [[ -n "$MEMD_PID" ]] && kill -0 "$MEMD_PID" 2>/dev/null; then
    kill "$MEMD_PID" 2>/dev/null || true
    for ((attempt = 0; attempt < 25; attempt++)); do
      if ! kill -0 "$MEMD_PID" 2>/dev/null; then
        break
      fi
      sleep 0.2
    done
    if kill -0 "$MEMD_PID" 2>/dev/null; then
      kill -KILL "$MEMD_PID" 2>/dev/null || true
    fi
    wait "$MEMD_PID" 2>/dev/null || true
  fi
  if [[ "$COMPOSE_STARTED" == true ]] &&
    ! compose down --volumes --remove-orphans >/dev/null 2>&1; then
    printf 'WARNING: failed to remove Compose project %s\n' "$COMPOSE_PROJECT" >&2
    cleanup_status=1
  fi
  if [[ -n "$E2E_DIR" && "$E2E_DIR" == "${E2E_ROOT}/acceptance."* ]]; then
    if ! rm -rf -- "$E2E_DIR"; then
      printf 'WARNING: failed to remove acceptance directory\n' >&2
      cleanup_status=1
    fi
  fi
  if [[ -n "$HTTP_LOCK_DIR" ]]; then
    if [[ "$HTTP_LOCK_DIR" != "${HTTP_LOCK_ROOT}/http-"*".lock" ]] ||
      ! rmdir -- "$HTTP_LOCK_DIR"; then
      printf 'WARNING: failed to release HTTP port lock\n' >&2
      cleanup_status=1
    fi
  fi
  rmdir -- "$HTTP_LOCK_ROOT" 2>/dev/null || true
  if [[ "$original_status" -eq 0 && "$cleanup_status" -ne 0 ]]; then
    exit "$cleanup_status"
  fi
  exit "$original_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

require_command docker
require_command go
require_command curl
require_command jq
require_command od
require_command tr

curl_safe() {
  command curl --connect-timeout 2 --max-time 20 "$@"
}

http_port_in_use() {
  local port="$1"
  (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null
}

random_http_port() {
  local random_value
  random_value="$(
    LC_ALL=C od -An -N4 -tu4 /dev/urandom |
      tr -d '[:space:]'
  )" || die "could not read a random HTTP port seed"
  [[ "$random_value" =~ ^[0-9]+$ ]] \
    || die "random HTTP port seed was not numeric"
  printf '%s\n' "$((20000 + (random_value % 10000)))"
}

release_http_lock() {
  [[ -n "$HTTP_LOCK_DIR" ]] || return 0
  [[ "$HTTP_LOCK_DIR" == "${HTTP_LOCK_ROOT}/http-"*".lock" ]] || return 1
  rmdir -- "$HTTP_LOCK_DIR" || return 1
  HTTP_LOCK_DIR=""
}

acquire_http_port() {
  local requested="${1:-}"
  local candidate
  local candidate_lock
  local attempt

  if [[ -n "$requested" ]] &&
    { [[ ! "$requested" =~ ^[0-9]+$ ]] ||
      ((requested < 1024 || requested > 65535)); }; then
    die "MEM_ACCEPTANCE_HTTP_PORT must be an integer between 1024 and 65535"
  fi

  for ((attempt = 0; attempt < 100; attempt++)); do
    if [[ -n "$requested" ]]; then
      candidate="$requested"
    else
      candidate="$(random_http_port)"
    fi
    candidate_lock="${HTTP_LOCK_ROOT}/http-${candidate}.lock"
    if ! mkdir -- "$candidate_lock" 2>/dev/null; then
      if [[ -n "$requested" ]]; then
        die "HTTP port ${candidate} is locked by another acceptance run"
      fi
      continue
    fi
    HTTP_LOCK_DIR="$candidate_lock"
    if http_port_in_use "$candidate"; then
      release_http_lock \
        || die "could not release occupied HTTP port lock ${candidate}"
      if [[ -n "$requested" ]]; then
        die "HTTP port ${candidate} is already in use"
      fi
      continue
    fi
    HTTP_PORT="$candidate"
    BASE_URL="http://127.0.0.1:${HTTP_PORT}"
    return 0
  done
  die "could not acquire a free HTTP port after 100 attempts"
}

refresh_http_port_before_start() {
  if ! http_port_in_use "$HTTP_PORT"; then
    return 0
  fi
  release_http_lock \
    || die "could not release HTTP port lock ${HTTP_PORT}"
  if [[ -n "$HTTP_PORT_REQUESTED" ]]; then
    die "HTTP port ${HTTP_PORT} became occupied before memd startup"
  fi
  acquire_http_port ""
  http_port_in_use "$HTTP_PORT" &&
    die "replacement HTTP port ${HTTP_PORT} became occupied before memd startup"
}

published_port() {
  local service="$1"
  local container_port="$2"
  local address
  address="$(compose port "$service" "$container_port")" \
    || die "could not resolve published port for $service"
  printf '%s\n' "${address##*:}"
}

mkdir -p "$E2E_ROOT"
E2E_DIR="$(mktemp -d "${E2E_ROOT}/acceptance.XXXXXX")"
run_suffix="${E2E_DIR##*.}"
run_suffix="$(printf '%s' "$run_suffix" | tr '[:upper:]' '[:lower:]')"
[[ "$run_suffix" =~ ^[a-z0-9]+$ ]] \
  || die "temporary acceptance suffix is not Compose-safe"
COMPOSE_PROJECT="mem-acceptance-${run_suffix}"
mkdir -p "$HTTP_LOCK_ROOT"
chmod 700 "$HTTP_LOCK_ROOT"
acquire_http_port "$HTTP_PORT_REQUESTED"

log "Starting isolated PostgreSQL and MinIO"
COMPOSE_STARTED=true
compose up -d --wait postgres minio
compose run --rm minio-init >/dev/null
PG_PORT="$(published_port postgres 5432)"
S3_PORT="$(published_port minio 9000)"
DB_URL="postgres://mem:mem@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"

log "Building current memd, CLI and MCP adapter"
(
  cd "${REPO_ROOT}/server"
  go build -trimpath -o "${E2E_DIR}/memd" ./cmd/memd
  go build -trimpath -o "${E2E_DIR}/mem" ./cmd/mem
  go build -trimpath -o "${E2E_DIR}/mem-mcp" ./cmd/mem-mcp
  go build -trimpath -o "${E2E_DIR}/mcp-acceptance" \
    ../scripts/mcp_acceptance.go
)

refresh_http_port_before_start
log "Starting current memd on ${BASE_URL}"
MEM_HTTP_ADDR="127.0.0.1:${HTTP_PORT}" \
MEM_DB_URL="$DB_URL" \
MEM_REDIS_URL="" \
MEM_S3_ENDPOINT="http://127.0.0.1:${S3_PORT}" \
MEM_S3_BUCKET="mem" \
MEM_S3_ACCESS_KEY="mem" \
MEM_S3_SECRET_KEY="mem-minio-password" \
MEM_S3_USE_SSL="false" \
MEM_WORKER_GRPC="127.0.0.1:1" \
MEM_REGISTRATION_MODE="open" \
MEM_SESSION_TTL="30m" \
  "${E2E_DIR}/memd" >"${E2E_DIR}/memd.log" 2>&1 &
MEMD_PID=$!

for ((attempt = 0; attempt < 60; attempt++)); do
  if ! kill -0 "$MEMD_PID" 2>/dev/null; then
    tail -n 100 "${E2E_DIR}/memd.log" >&2
    die "memd exited before becoming healthy"
  fi
  if curl_safe -fsS "${BASE_URL}/healthz" 2>/dev/null |
    jq -e '.ok == true' >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
kill -0 "$MEMD_PID" 2>/dev/null ||
  die "memd exited after its health endpoint became reachable"
curl_safe -fsS "${BASE_URL}/healthz" | jq -e '.ok == true' >/dev/null \
  || die "memd did not become healthy within 60 seconds"

expected_schema_version=0
for migration in \
  "${REPO_ROOT}"/server/internal/db/migrations/[0-9][0-9][0-9][0-9]_*.sql; do
  [[ -e "$migration" ]] || continue
  migration_name="${migration##*/}"
  migration_version="${migration_name%%_*}"
  migration_version=$((10#$migration_version))
  if ((migration_version > expected_schema_version)); then
    expected_schema_version="$migration_version"
  fi
done
((expected_schema_version > 0)) || die "could not resolve migration head"
schema_version="$(
  compose exec -T postgres psql -U mem -d "$DB_NAME" -Atc \
    'SELECT max(version_id) FROM goose_db_version WHERE is_applied'
)"
[[ "$schema_version" == "$expected_schema_version" ]] \
  || die "expected migration version ${expected_schema_version}, got ${schema_version}"

log "Registering an isolated user and path-scoped Agent token"
session_json="$(
  curl_safe -fsS -X POST "${BASE_URL}/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"mem-e2e-$$@example.invalid\",\"password\":\"e2e-password\"}"
)"
session_token="$(printf '%s' "$session_json" | jq -er '.token')"
workspace_id="$(
  curl_safe -fsS "${BASE_URL}/v1/workspaces/current" \
    -H "Authorization: Bearer ${session_token}" |
    jq -er '.id'
)"
agent_json="$(
  curl_safe -fsS -X POST "${BASE_URL}/v1/auth/tokens" \
    -H "Authorization: Bearer ${session_token}" \
    -H "X-Workspace-ID: ${workspace_id}" \
    -H 'Content-Type: application/json' \
    -d '{"name":"local-e2e-agent","scopes":["search","read","write","delete"],"paths":["/E2E"]}'
)"
agent_token="$(printf '%s' "$agent_json" | jq -er '.token')"
printf '%s' "$agent_json" |
  jq -e --arg workspace_id "$workspace_id" '
    .workspace_id == $workspace_id and
    .paths == ["/E2E"] and
    ((.scopes | sort) == (["search", "read", "write", "delete"] | sort))
  ' >/dev/null

curl_safe -fsS "${BASE_URL}/v1/capabilities" \
  -H "Authorization: Bearer ${agent_token}" \
  -H "X-Workspace-ID: ${workspace_id}" |
  jq -e '
    .features.memory == true and
    .permissions.search == true and
    .permissions.read == true and
    .permissions.write == true and
    .permissions.delete == true
  ' >/dev/null

log "Validating HTTP remember, replay, conflict and model-free context"
outside_marker="outside-${run_suffix}-must-never-reach-the-path-scoped-agent"
outside_body="$(
  jq -nc --arg marker "$outside_marker" '{
    kind: "decision",
    content: $marker,
    path: "/Outside",
    source: {type: "user"}
  }'
)"
outside_json="$(
  curl_safe -fsS -X POST "${BASE_URL}/v1/memories" \
    -H "Authorization: Bearer ${session_token}" \
    -H "X-Workspace-ID: ${workspace_id}" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: outside-root-${run_suffix}" \
    -d "$outside_body"
)"
outside_memory_id="$(printf '%s' "$outside_json" | jq -er '.memory.id')"
printf '%s' "$outside_json" |
  jq -e '.replayed == false and .memory.path == "/Outside"' >/dev/null

outside_code="$(
  curl_safe -sS -o "${E2E_DIR}/outside-path.json" -w '%{http_code}' \
    -X POST "${BASE_URL}/v1/memories" \
    -H "Authorization: Bearer ${agent_token}" \
    -H "X-Workspace-ID: ${workspace_id}" \
    -H 'Content-Type: application/json' \
    -H 'Idempotency-Key: outside-path-must-fail-v1' \
    -d '{"kind":"decision","content":"must not persist","path":"/Outside","source":{"type":"agent"}}'
)"
[[ "$outside_code" == "403" ]] \
  || die "out-of-scope remember returned HTTP ${outside_code}"
jq -e '.error == "path_forbidden"' \
  "${E2E_DIR}/outside-path.json" >/dev/null

outside_code="$(
  curl_safe -sS -o "${E2E_DIR}/outside-get.json" -w '%{http_code}' \
    "${BASE_URL}/v1/memories/${outside_memory_id}" \
    -H "Authorization: Bearer ${agent_token}" \
    -H "X-Workspace-ID: ${workspace_id}"
)"
[[ "$outside_code" == "404" ]] \
  || die "out-of-scope memory read returned HTTP ${outside_code}"
jq -e '.error == "not_found"' \
  "${E2E_DIR}/outside-get.json" >/dev/null

curl_safe -fsS -G "${BASE_URL}/v1/memories" \
  -H "Authorization: Bearer ${agent_token}" \
  -H "X-Workspace-ID: ${workspace_id}" \
  --data-urlencode 'scope=/' \
  --data-urlencode 'lifecycle=all' \
  --data-urlencode 'limit=100' \
  >"${E2E_DIR}/outside-list.json"
jq -e --arg id "$outside_memory_id" '
  all(.memories[]; .id != $id)
' "${E2E_DIR}/outside-list.json" >/dev/null

outside_context_body="$(
  jq -nc --arg query "$outside_marker" '{
    query: $query,
    scope: "/",
    source: "memory",
    memory_kind: "decision",
    limit: 10,
    max_chars: 4096
  }'
)"
curl_safe -fsS -X POST "${BASE_URL}/v1/context" \
  -H "Authorization: Bearer ${agent_token}" \
  -H "X-Workspace-ID: ${workspace_id}" \
  -H 'Content-Type: application/json' \
  -d "$outside_context_body" \
  >"${E2E_DIR}/outside-context.json"
jq -e --arg id "$outside_memory_id" --arg marker "$outside_marker" '
  all(.evidence[];
    .memory_id != $id and
    (((.excerpt // "") | contains($marker)) | not))
' "${E2E_DIR}/outside-context.json" >/dev/null

http_body='{"kind":"decision","content":"HTTP lifecycle evidence uses PostgreSQL","path":"/E2E/HTTP","source":{"type":"agent"},"producer":{"agent_id":"http-e2e"}}'
http_code="$(
  curl_safe -sS -o "${E2E_DIR}/http-first.json" -w '%{http_code}' \
    -X POST "${BASE_URL}/v1/memories" \
    -H "Authorization: Bearer ${agent_token}" \
    -H "X-Workspace-ID: ${workspace_id}" \
    -H 'Content-Type: application/json' \
    -H 'Idempotency-Key: http-e2e-remember-v1' \
    -d "$http_body"
)"
[[ "$http_code" == "201" ]] || die "first remember returned HTTP ${http_code}"
http_memory_id="$(jq -er '.memory.id' "${E2E_DIR}/http-first.json")"
jq -e '.replayed == false and .memory.state_version == 1' \
  "${E2E_DIR}/http-first.json" >/dev/null

http_code="$(
  curl_safe -sS -o "${E2E_DIR}/http-replay.json" -w '%{http_code}' \
    -X POST "${BASE_URL}/v1/memories" \
    -H "Authorization: Bearer ${agent_token}" \
    -H "X-Workspace-ID: ${workspace_id}" \
    -H 'Content-Type: application/json' \
    -H 'Idempotency-Key: http-e2e-remember-v1' \
    -d "$http_body"
)"
[[ "$http_code" == "200" ]] || die "remember replay returned HTTP ${http_code}"
jq -e --arg id "$http_memory_id" \
  '.replayed == true and .memory.id == $id' \
  "${E2E_DIR}/http-replay.json" >/dev/null

http_code="$(
  curl_safe -sS -o "${E2E_DIR}/http-conflict.json" -w '%{http_code}' \
    -X POST "${BASE_URL}/v1/memories" \
    -H "Authorization: Bearer ${agent_token}" \
    -H "X-Workspace-ID: ${workspace_id}" \
    -H 'Content-Type: application/json' \
    -H 'Idempotency-Key: http-e2e-remember-v1' \
    -d '{"kind":"decision","content":"changed payload","path":"/E2E/HTTP","source":{"type":"agent"}}'
)"
[[ "$http_code" == "409" ]] || die "remember conflict returned HTTP ${http_code}"
jq -e '.error == "idempotency_conflict"' \
  "${E2E_DIR}/http-conflict.json" >/dev/null

curl_safe -fsS -X POST "${BASE_URL}/v1/context" \
  -H "Authorization: Bearer ${agent_token}" \
  -H "X-Workspace-ID: ${workspace_id}" \
  -H 'Content-Type: application/json' \
  -d '{"query":"PostgreSQL lifecycle evidence","scope":"/E2E","source":"memory","memory_kind":"decision","limit":5,"max_chars":4096}' |
  jq -e --arg id "$http_memory_id" '
    .source == "memory" and
    .partial == false and
    any(.evidence[]; .memory_id == $id and .source_kind == "memory")
  ' >/dev/null

log "Creating an out-of-token-path checkpoint for adapter isolation"
outside_task_key="outside-e2e-${run_suffix}"
outside_checkpoint_sentinel="outside-handoff-${run_suffix}-must-never-reach-the-path-scoped-agent"
jq -nc \
  --arg task_key "$outside_task_key" \
  --arg sentinel "$outside_checkpoint_sentinel" '
    {
      contract: "mem.handoff",
      schema_version: 1,
      checkpoint_kind: "handoff",
      task_key: $task_key,
      scope_path: "/Outside",
      state: {
        status: "ready",
        goal: $sentinel,
        progress: {
          summary: $sentinel,
          completed: []
        },
        decisions: [],
        next_steps: [],
        blockers: [],
        open_questions: [],
        artifacts: []
      },
      producer: {
        agent_id: "outside-session"
      }
    }
  ' >"${E2E_DIR}/outside-handoff.json"
outside_checkpoint_json="$(
  curl_safe -fsS -X POST \
    "${BASE_URL}/v1/tasks/${outside_task_key}/checkpoints" \
    -H "Authorization: Bearer ${session_token}" \
    -H "X-Workspace-ID: ${workspace_id}" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: outside-checkpoint-${run_suffix}" \
    --data-binary "@${E2E_DIR}/outside-handoff.json"
)"
outside_checkpoint_id="$(
  printf '%s' "$outside_checkpoint_json" |
    jq -er '.checkpoint.id'
)"

log "Validating CLI against the same HTTP service"
cli_json="$(
  MEM_CONFIG="${E2E_DIR}/nonexistent-cli-config.yaml" \
  MEM_SERVER="$BASE_URL" \
  MEM_TOKEN="$agent_token" \
  MEM_WORKSPACE="$workspace_id" \
    "${E2E_DIR}/mem" remember \
      "CLI adapter reaches the canonical memory API" \
      --kind observation \
      --path /E2E/CLI \
      --idempotency-key cli-e2e-v1 \
      --agent-id cli-e2e \
      --format json
)"
cli_memory_id="$(printf '%s' "$cli_json" | jq -er '.memory.id')"
printf '%s' "$cli_json" |
  jq -e '.replayed == false and .memory.path == "/E2E/CLI"' >/dev/null

cli_memory_detail="$(
  MEM_CONFIG="${E2E_DIR}/nonexistent-cli-config.yaml" \
  MEM_SERVER="$BASE_URL" \
  MEM_TOKEN="$agent_token" \
  MEM_WORKSPACE="$workspace_id" \
    "${E2E_DIR}/mem" memory "$cli_memory_id" \
      --scope /E2E \
      --format json
)"
printf '%s' "$cli_memory_detail" |
  jq -e \
    --arg id "$cli_memory_id" \
    --arg workspace_id "$workspace_id" \
    --arg content "CLI adapter reaches the canonical memory API" '
      ([.. | objects | keys[]] |
        index("created_by_token_id") == null and
        index("forgotten_by_token_id") == null and
        index("idempotency_key") == null and
        index("idempotency_key_sha256") == null and
        index("request_sha256") == null and
        index("replay_principal_sha256") == null) and
      .id == $id and
      .kind == "observation" and
      .content == $content and
      .path == "/E2E/CLI" and
      .source_type == "agent" and
      .producer_agent == "cli-e2e" and
      .lifecycle_status == "active" and
      .state_version == 1 and
      .citation == ("mem://memories/" + $id) and
      .provenance.workspace_id == $workspace_id and
      .provenance.source_type == "agent" and
      .provenance.producer_agent == "cli-e2e"
    ' >/dev/null

MEM_CONFIG="${E2E_DIR}/nonexistent-cli-config.yaml" \
MEM_SERVER="$BASE_URL" \
MEM_TOKEN="$agent_token" \
MEM_WORKSPACE="$workspace_id" \
  "${E2E_DIR}/mem" context \
    "canonical memory API" \
    --source memory \
    --scope /E2E \
    --format json |
  jq -e --arg id "$cli_memory_id" '
    any(.evidence[];
      .source_kind == "memory" and .memory_id == $id)
  ' >/dev/null

cli_task_key="cli-e2e-${run_suffix}"
cli_checkpoint_private="cli-private-${run_suffix}-must-not-enter-checkpoint-list"
cli_checkpoint_goal="Verify CLI inspection: ${cli_checkpoint_private}"
jq -nc \
  --arg task_key "$cli_task_key" \
  --arg goal "$cli_checkpoint_goal" '
    {
      contract: "mem.handoff",
      schema_version: 1,
      checkpoint_kind: "handoff",
      task_key: $task_key,
      scope_path: "/E2E/CLI",
      state: {
        status: "ready",
        goal: $goal,
        progress: {
          summary: ("界" * 600),
          completed: ["persisted a real CLI checkpoint"]
        },
        decisions: [],
        next_steps: [],
        blockers: [],
        open_questions: [],
        artifacts: []
      },
      producer: {
        agent_id: "cli-e2e",
        session_id: "cli-e2e-read-session"
      }
    }
  ' >"${E2E_DIR}/cli-handoff.json"

cli_checkpoint_json="$(
  MEM_CONFIG="${E2E_DIR}/nonexistent-cli-config.yaml" \
  MEM_SERVER="$BASE_URL" \
  MEM_TOKEN="$agent_token" \
  MEM_WORKSPACE="$workspace_id" \
    "${E2E_DIR}/mem" checkpoint \
      --input "${E2E_DIR}/cli-handoff.json" \
      --idempotency-key "cli-e2e-checkpoint-${run_suffix}" \
      --format json
)"
cli_checkpoint_id="$(
  printf '%s' "$cli_checkpoint_json" |
    jq -er '.checkpoint.id'
)"
printf '%s' "$cli_checkpoint_json" |
  jq -e \
    --arg id "$cli_checkpoint_id" \
    --arg task_key "$cli_task_key" \
    --arg goal "$cli_checkpoint_goal" '
      .replayed == false and
      .checkpoint.id == $id and
      .checkpoint.task_key == $task_key and
      .checkpoint.sequence == 1 and
      .checkpoint.checkpoint_kind == "handoff" and
      .checkpoint.contract == "mem.handoff" and
      .checkpoint.schema_version == 1 and
      .checkpoint.scope_path == "/E2E/CLI" and
      .checkpoint.producer_agent == "cli-e2e" and
      .checkpoint.handoff.state.status == "ready" and
      .checkpoint.handoff.state.goal == $goal
    ' >/dev/null

cli_tasks_json="$(
  MEM_CONFIG="${E2E_DIR}/nonexistent-cli-config.yaml" \
  MEM_SERVER="$BASE_URL" \
  MEM_TOKEN="$agent_token" \
  MEM_WORKSPACE="$workspace_id" \
    "${E2E_DIR}/mem" tasks \
      --scope / \
      --limit 100 \
      --format json
)"
printf '%s' "$cli_tasks_json" |
  jq -e \
    --arg id "$cli_checkpoint_id" \
    --arg task_key "$cli_task_key" \
    --arg outside_task_key "$outside_task_key" '
      all(.tasks[];
        .task_key != $outside_task_key and
        (.scope_path == "/E2E" or (.scope_path | startswith("/E2E/")))) and
      any(.tasks[];
        .task_key == $task_key and
        .scope_path == "/E2E/CLI" and
        .head_checkpoint_id == $id and
        .head_sequence == 1)
    ' >/dev/null

cli_checkpoints_json="$(
  MEM_CONFIG="${E2E_DIR}/nonexistent-cli-config.yaml" \
  MEM_SERVER="$BASE_URL" \
  MEM_TOKEN="$agent_token" \
  MEM_WORKSPACE="$workspace_id" \
    "${E2E_DIR}/mem" checkpoints "$cli_task_key" \
      --scope /E2E \
      --limit 100 \
      --format json
)"
printf '%s' "$cli_checkpoints_json" |
  jq -e \
    --arg id "$cli_checkpoint_id" \
    --arg task_key "$cli_task_key" \
    --arg private "$cli_checkpoint_private" '
      all(.checkpoints[];
        (has("handoff") | not) and
        (has("references") | not) and
        (has("created_by_user_id") | not) and
        (has("created_by_token_id") | not) and
        (has("idempotency_key") | not) and
        (has("request_sha256") | not)) and
      ([.checkpoints[] | .. | strings | select(contains($private))] | length == 0) and
      any(.checkpoints[];
        .id == $id and
        .task_key == $task_key and
        .sequence == 1 and
        .checkpoint_kind == "handoff" and
        .contract == "mem.handoff" and
        .schema_version == 1 and
        .scope_path == "/E2E/CLI" and
        .status == "ready" and
        .progress_excerpt == ("界" * 500) and
        .progress_length == 600 and
        .completed_count == 1 and
        .reference_count == 0 and
        (.payload_sha256 | test("^[0-9a-f]{64}$")) and
        .producer_agent == "cli-e2e" and
        .producer_session == "cli-e2e-read-session")
    ' >/dev/null

cli_checkpoint_detail="$(
  MEM_CONFIG="${E2E_DIR}/nonexistent-cli-config.yaml" \
  MEM_SERVER="$BASE_URL" \
  MEM_TOKEN="$agent_token" \
  MEM_WORKSPACE="$workspace_id" \
    "${E2E_DIR}/mem" checkpoint get \
      "$cli_task_key" "$cli_checkpoint_id" \
      --scope /E2E \
      --format json
)"
printf '%s' "$cli_checkpoint_detail" |
  jq -e \
    --arg id "$cli_checkpoint_id" \
    --arg task_key "$cli_task_key" \
    --arg goal "$cli_checkpoint_goal" '
      .id == $id and
      .task_key == $task_key and
      .sequence == 1 and
      .checkpoint_kind == "handoff" and
      .contract == "mem.handoff" and
      .schema_version == 1 and
      .scope_path == "/E2E/CLI" and
      .producer_agent == "cli-e2e" and
      .handoff.state.status == "ready" and
      .handoff.state.goal == $goal and
      .handoff.state.progress.summary == ("界" * 600)
    ' >/dev/null

outside_cli_checkpoints="$(
  MEM_CONFIG="${E2E_DIR}/nonexistent-cli-config.yaml" \
  MEM_SERVER="$BASE_URL" \
  MEM_TOKEN="$agent_token" \
  MEM_WORKSPACE="$workspace_id" \
    "${E2E_DIR}/mem" checkpoints "$outside_task_key" \
      --scope / \
      --limit 100 \
      --format json
)"
printf '%s' "$outside_cli_checkpoints" |
  jq -e '.checkpoints == []' >/dev/null

set +e
MEM_CONFIG="${E2E_DIR}/nonexistent-cli-config.yaml" \
MEM_SERVER="$BASE_URL" \
MEM_TOKEN="$agent_token" \
MEM_WORKSPACE="$workspace_id" \
  "${E2E_DIR}/mem" checkpoint get \
    "$outside_task_key" "$outside_checkpoint_id" \
    --scope / \
    --format json \
    >"${E2E_DIR}/outside-cli-get.stdout" \
    2>"${E2E_DIR}/outside-cli-get.stderr"
outside_cli_get_status=$?
set -e
outside_cli_get_error="$(<"${E2E_DIR}/outside-cli-get.stderr")"
if [[ "$outside_cli_get_status" -eq 0 ||
  "$outside_cli_get_error" != *"not_found"* ]]; then
  die "out-of-token-path CLI checkpoint get did not fail with not_found"
fi

log "Validating sequential MCP handshake, recall, inspection and lifecycle"
: >"${E2E_DIR}/mcp-client.log"
: >"${E2E_DIR}/mcp.log"
chmod 600 "${E2E_DIR}/mcp-client.log" "${E2E_DIR}/mcp.log"
set +e
mcp_summary="$(
  MEM_SERVER="$BASE_URL" \
  MEM_TOKEN="$agent_token" \
  MEM_WORKSPACE="$workspace_id" \
  MEM_OUTSIDE_TASK_KEY="$outside_task_key" \
  MEM_OUTSIDE_CHECKPOINT_ID="$outside_checkpoint_id" \
    "${E2E_DIR}/mcp-acceptance" \
      --mcp-binary "${E2E_DIR}/mem-mcp" \
      --log "${E2E_DIR}/mcp.log"
)" 2>"${E2E_DIR}/mcp-client.log"
mcp_status=$?
set -e
if [[ "$mcp_status" -ne 0 ]]; then
  tail -n 100 "${E2E_DIR}/mcp-client.log" >&2 || true
  tail -n 100 "${E2E_DIR}/mcp.log" >&2 || true
  die "sequential MCP acceptance failed with exit ${mcp_status}"
fi
mcp_memory_id="$(printf '%s' "$mcp_summary" | jq -er '.memory_id')"
printf '%s' "$mcp_summary" |
  jq -e '
    .state_version == 5 and
    .task_key == "mcp-e2e-read-surfaces" and
    (.checkpoint_id | test(
      "^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
    ))
  ' >/dev/null

forgotten_code="$(
  curl_safe -sS -o "${E2E_DIR}/forgotten.json" -w '%{http_code}' \
    "${BASE_URL}/v1/memories/${mcp_memory_id}" \
    -H "Authorization: Bearer ${agent_token}" \
    -H "X-Workspace-ID: ${workspace_id}"
)"
[[ "$forgotten_code" == "404" ]] \
  || die "forgotten memory read returned HTTP ${forgotten_code}"
jq -e '.error == "not_found"' \
  "${E2E_DIR}/forgotten.json" >/dev/null

log "Validating logical forget redaction in PostgreSQL"
redaction_json="$(
  compose exec -T postgres \
    psql -X -qAt -v ON_ERROR_STOP=1 \
      -v memory_id="$mcp_memory_id" \
      -v workspace_id="$workspace_id" \
      -v marker='MCP reaches the canonical HTTP memory service' \
      -U mem -d "$DB_NAME" <<'SQL'
WITH target AS (
  SELECT *
    FROM memories
   WHERE id = :'memory_id'::uuid
     AND workspace_id = :'workspace_id'::uuid
),
events AS (
  SELECT *
    FROM memory_events
   WHERE memory_id = :'memory_id'::uuid
     AND workspace_id = :'workspace_id'::uuid
)
SELECT json_build_object(
  'row_count',
    (SELECT count(*) FROM target),
  'projection_redacted',
    COALESCE((
      SELECT created_by_user_id IS NULL
         AND created_by_token_id IS NULL
         AND kind = 'forgotten'
         AND content = ''
         AND attributes = '{}'::jsonb
         AND path = '/'
         AND event_at IS NULL
         AND source_type = 'forgotten'
         AND source_ref = ''
         AND source_file_id IS NULL
         AND source_file_sha256 = ''
         AND source_locator = '{}'::jsonb
         AND producer_agent = ''
         AND producer_session = ''
         AND producer_task = ''
         AND request_sha256 = repeat('0', 64)
         AND content_sha256 = repeat('0', 64)
         AND lifecycle_status = 'forgotten'
         AND pinned_at IS NULL
         AND useful_count = 0
         AND not_useful_count = 0
         AND feedback_at IS NULL
         AND forgotten_at IS NOT NULL
         AND forgotten_by_user_id IS NULL
         AND forgotten_by_token_id IS NULL
         AND created_at = forgotten_at
         AND updated_at = forgotten_at
        FROM target
    ), false),
  'marker_absent_from_projection',
    NOT EXISTS (
      SELECT 1
        FROM target AS memory_row
       WHERE strpos(row_to_json(memory_row)::text, :'marker') > 0
    ),
  'event_actors_redacted',
    NOT EXISTS (
      SELECT 1
        FROM events
       WHERE actor_user_id IS NOT NULL
          OR actor_token_id IS NOT NULL
    ),
  'prior_event_receipts_redacted',
    NOT EXISTS (
      SELECT 1
        FROM events
       WHERE action <> 'forget'
         AND (
           request_sha256 <> repeat('0', 64)
           OR replay_principal_sha256 <> ''
           OR char_length(idempotency_key_sha256) <> 64
         )
    ),
  'forget_receipt_minimal',
    (
      SELECT count(*) = 1
        FROM events
       WHERE action = 'forget'
         AND actor_user_id IS NULL
         AND actor_token_id IS NULL
         AND char_length(idempotency_key_sha256) = 64
         AND char_length(request_sha256) = 64
         AND char_length(replay_principal_sha256) = 64
         AND expected_version = 4
         AND resulting_version = 5
         AND reason = 'user_request'
    ),
  'marker_absent_from_events',
    NOT EXISTS (
      SELECT 1
        FROM events AS event_row
       WHERE strpos(row_to_json(event_row)::text, :'marker') > 0
    )
)::text;
SQL
)"
printf '%s' "$redaction_json" |
  jq -e '
    .row_count == 1 and
    .projection_redacted == true and
    .marker_absent_from_projection == true and
    .event_actors_redacted == true and
    .prior_event_receipts_redacted == true and
    .forget_receipt_minimal == true and
    .marker_absent_from_events == true
  ' >/dev/null

log "PASS: isolated HTTP, CLI and MCP Agent-memory lifecycle acceptance"
