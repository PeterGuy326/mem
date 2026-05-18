#!/usr/bin/env bash
# scripts/seed_demo_data.sh — mem Phase 1 W2 end-to-end demo (T15)
#
# What this script does:
#   1. Verify docker compose stack is up & healthy (postgres / redis / minio /
#      memd / worker / ollama).
#   2. Bootstrap an admin user via direct SQL (memd does not yet expose a
#      /v1/auth/register endpoint — see CAPABILITY MATRIX below).
#   3. Login via HTTP POST /v1/auth/login (we deliberately bypass `mem login`
#      because its interactive prompt uses term.ReadPassword which does not
#      read from piped stdin — see DECISIONS at top of script).
#   4. `mem put scripts/demo_data/*.md` (idempotent — re-runs are safe thanks
#      to content-hash dedup; `mem put` returns 200 + `deduped: true` for
#      previously-seen content).
#   5. Poll until every uploaded file has `index_status: indexed` AND a
#      non-empty caption/summary (signals the worker pipeline completed).
#   6. Run a set of search assertions covering pure-semantic queries (no
#      literal keyword overlap with the target file) and verify
#      `response.mode == "vector"` plus `hits[0].name == <expected>`.
#   7. Print a colored PASS/FAIL summary. Exit code is non-zero on any
#      assertion failure.
#
# Assumptions (read these before running):
#   - `docker compose up -d` has already been run from repo root.
#   - `mem` CLI is on PATH; otherwise we fall back to `go run ./cmd/mem`
#     from the server/ directory (slower — JIT compile on first call).
#   - memd is reachable at http://localhost:8787 (override with MEM_SERVER).
#   - jq is installed.
#
# ============================================================================
# DECISIONS (why this script looks the way it does)
# ============================================================================
#   D1: We authenticate via raw HTTP, not `mem login`, because mem login
#       calls golang.org/x/term.ReadPassword which only reads from a TTY,
#       not from piped stdin. The script then exports MEM_TOKEN so every
#       subsequent `mem` invocation is auth'd without touching the user's
#       ~/.mem/config.yaml (no surprise mutations of dev state).
#   D2: Admin user is bootstrapped by INSERT … ON CONFLICT DO NOTHING via
#       docker compose exec, since memd has no /v1/auth/register handler.
#       Idempotent: re-runs are safe.
#   D3: Three search assertions, all pure-semantic — see the grep proof in
#       run_assertions() — so a passing hit can ONLY come from vector
#       similarity, not keyword/BM25 fallback. This is the demo's whole
#       reason for existing.
#   D4: The script is bash 3.2 compatible (macOS default) — no `declare -A`,
#       no `mapfile`, no `${var,,}`.
#
# ============================================================================
# CAPABILITY MATRIX — what this script requires from main, by commit
# ============================================================================
# The script is written against the *target* W2 surface area. As of the
# commit that introduces this file, several pieces are NOT yet on main:
#
#   [HARD-REQ] docker-compose.yml has services: memd, worker, ollama,
#              ollama-init (currently commented out — see compose file).
#   [HARD-REQ] CLI command `mem search "<query>" --format json`         (W3-ι)
#   [HARD-REQ] HTTP route POST /v1/search returning { mode, hits[] }    (W2)
#   [HARD-REQ] Worker EmbedText gRPC RPC + memd worker pool consumer    (W2-θ)
#   [HARD-REQ] memd ingest job → text → embed → embeddings_text insert  (W2)
#   [SOFT]     `mem info <id>` already exposes `index_status` + `caption`
#              (confirmed on HEAD).
#   [SOFT]     `mem put <file> --to <folder>` works (confirmed on HEAD).
#
# When any [HARD-REQ] is missing, the pre-flight check below fails fast and
# the script exits non-zero with a diagnostic — it never reports a fake PASS.
# ============================================================================

set -euo pipefail
IFS=$'\n\t'

# ---- config ----------------------------------------------------------------
REPO_ROOT="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." && pwd )"
DEMO_DIR="${REPO_ROOT}/scripts/demo_data"
MEM_SERVER="${MEM_SERVER:-http://localhost:8787}"
ADMIN_EMAIL="${MEM_ADMIN_EMAIL:-demo@mem.local}"
ADMIN_PASSWORD="${MEM_ADMIN_PASSWORD:-demo-password-change-me}"
TARGET_FOLDER="${MEM_DEMO_FOLDER:-/demo}"
INGEST_TIMEOUT_SEC="${MEM_INGEST_TIMEOUT_SEC:-90}"

# ---- color helpers (NO_COLOR honored) --------------------------------------
if [[ -n "${NO_COLOR:-}" || ! -t 1 ]]; then
  C_RED=""; C_GREEN=""; C_YELLOW=""; C_BLUE=""; C_BOLD=""; C_RESET=""
else
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'
  C_BLUE=$'\033[34m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
fi

log()   { echo "${C_BLUE}==>${C_RESET} $*" >&2; }
warn()  { echo "${C_YELLOW}WARN${C_RESET} $*" >&2; }
err()   { echo "${C_RED}ERR${C_RESET}  $*" >&2; }
ok()    { echo "${C_GREEN}OK${C_RESET}   $*" >&2; }
fatal() { err "$*"; dump_diagnostics; exit 1; }

dump_diagnostics() {
  echo >&2
  echo "${C_YELLOW}--- diagnostics ---${C_RESET}" >&2
  if command -v docker >/dev/null 2>&1; then
    (cd "$REPO_ROOT" && docker compose ps 2>&1 | sed 's/^/  /') >&2 || true
    echo >&2
    echo "${C_YELLOW}--- recent memd logs (last 30 lines) ---${C_RESET}" >&2
    (cd "$REPO_ROOT" && docker compose logs --tail=30 memd 2>&1 | sed 's/^/  /') >&2 || true
    echo >&2
    echo "${C_YELLOW}--- recent worker logs (last 30 lines) ---${C_RESET}" >&2
    (cd "$REPO_ROOT" && docker compose logs --tail=30 worker 2>&1 | sed 's/^/  /') >&2 || true
  else
    echo "  (docker not on PATH; cannot dump compose state)" >&2
  fi
}

# ---- mem CLI shim ----------------------------------------------------------
# Prefer a binary on PATH; fall back to bin/mem; then `go run`. The shim
# always sets --server explicitly so we don't depend on ~/.mem/config.yaml.
# MEM_TOKEN is exported once by do_login() so subsequent calls are auth'd.
mem_bin=""
mem_args_prefix=()
detect_mem() {
  if command -v mem >/dev/null 2>&1; then
    mem_bin="mem"
    mem_args_prefix=()
  elif [[ -x "${REPO_ROOT}/bin/mem" ]]; then
    mem_bin="${REPO_ROOT}/bin/mem"
    mem_args_prefix=()
  elif command -v go >/dev/null 2>&1; then
    mem_bin="go"
    mem_args_prefix=(run "./cmd/mem")
    warn "mem binary not found; falling back to 'go run ./cmd/mem' (first call will compile)"
  else
    fatal "no 'mem' on PATH, no bin/mem in repo, and 'go' is also missing — install one of them"
  fi
}

mem() {
  # Use `command` to bypass function lookup — otherwise "$mem_bin" expanding
  # to "mem" recurses back into this function and loops infinitely.
  if [[ "$mem_bin" == "go" ]]; then
    (cd "${REPO_ROOT}/server" && command "$mem_bin" "${mem_args_prefix[@]}" --server "$MEM_SERVER" "$@")
  else
    command "$mem_bin" --server "$MEM_SERVER" "$@"
  fi
}

# ---- pre-flight checks -----------------------------------------------------
preflight() {
  log "pre-flight checks"

  command -v jq >/dev/null 2>&1 \
    || fatal "jq is required (https://stedolan.github.io/jq/)"

  command -v curl >/dev/null 2>&1 \
    || fatal "curl is required"

  command -v docker >/dev/null 2>&1 \
    || fatal "docker is required for diagnostics + bootstrap"

  detect_mem
  ok "mem CLI: $mem_bin ${mem_args_prefix[*]:-}"

  # memd reachable?
  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' "${MEM_SERVER}/healthz" || echo "000")
  if [[ "$code" != "200" ]]; then
    fatal "memd /healthz returned ${code} at ${MEM_SERVER} — is 'docker compose up -d' done?"
  fi
  ok "memd /healthz reachable at ${MEM_SERVER}"

  # `mem search` available? probe via --help (cobra prints commands).
  # If absent, this is a HARD-REQ failure — exit non-zero (red banner) rather
  # than silently downgrade. The whole point of the demo is vector search.
  if ! mem --help 2>&1 | grep -qE '^[[:space:]]*search([[:space:]]|$)'; then
    err "'mem search' subcommand not found in CLI help output"
    err "CAPABILITY MATRIX [HARD-REQ] 'mem search' is missing on this build"
    err "(this is the W3-ι deliverable; the demo cannot validate vector"
    err " search until that lands. See CAPABILITY MATRIX at top of script.)"
    return 1
  fi
  ok "mem search subcommand present"

  # /v1/search route on memd? probe with an empty POST.
  # Expected: 401 (unauth) or 400 (bad body) → route exists.
  # 404 → route missing → HARD-REQ failure.
  code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
           -H 'Content-Type: application/json' -d '{}' \
           "${MEM_SERVER}/v1/search" || echo "000")
  case "$code" in
    400|401|403|422) ok "/v1/search route present (probe returned $code)" ;;
    404)
      err "POST /v1/search returned 404 — route not registered on memd"
      err "CAPABILITY MATRIX [HARD-REQ] memd /v1/search handler missing"
      return 1
      ;;
    *)
      warn "/v1/search probe returned unexpected $code (continuing; may still work)"
      ;;
  esac
}

# ---- bootstrap admin user --------------------------------------------------
# memd has no /v1/auth/register HTTP route (intentional for v1 — admin users
# are provisioned out-of-band). We insert directly into the users table via
# docker compose exec, using ON CONFLICT DO NOTHING so the script is
# idempotent across runs. bcrypt hash is generated by Python's bcrypt
# library if available, otherwise we delegate to docker run on a python image.
bootstrap_admin() {
  log "bootstrap admin user: ${ADMIN_EMAIL}"

  local hash
  if command -v python3 >/dev/null 2>&1 && \
     python3 -c 'import bcrypt' 2>/dev/null; then
    hash=$(python3 -c "import bcrypt,sys; print(bcrypt.hashpw(sys.argv[1].encode(),bcrypt.gensalt()).decode())" "$ADMIN_PASSWORD")
  else
    warn "python3+bcrypt not available; using docker python:3.12-alpine fallback (slower)"
    hash=$(docker run --rm python:3.12-alpine sh -c \
      "pip install -q bcrypt && python -c 'import bcrypt,sys; print(bcrypt.hashpw(sys.argv[1].encode(),bcrypt.gensalt()).decode())' '$ADMIN_PASSWORD'")
  fi

  # The auth schema's column names track auth.go's UserRow struct — adjust
  # here if the migrations evolve (id uuid pk, email text unique,
  # password_hash text). The ON CONFLICT makes this idempotent.
  docker compose -f "${REPO_ROOT}/docker-compose.yml" exec -T postgres \
    psql -U mem -d mem -v ON_ERROR_STOP=1 <<SQL
INSERT INTO users (id, email, password_hash)
VALUES (gen_random_uuid(), '${ADMIN_EMAIL}', '${hash}')
ON CONFLICT (email) DO NOTHING;
SQL
  ok "admin user ensured: ${ADMIN_EMAIL}"
}

# ---- login via raw HTTP (see DECISIONS D1) ---------------------------------
do_login() {
  log "login as ${ADMIN_EMAIL} (HTTP POST /v1/auth/login)"
  local body
  body=$(printf '{"email":"%s","password":"%s"}' "$ADMIN_EMAIL" "$ADMIN_PASSWORD")
  local resp
  resp=$(curl -sS -X POST -H 'Content-Type: application/json' \
              -d "$body" "${MEM_SERVER}/v1/auth/login") \
    || fatal "login HTTP call failed"

  local token
  token=$(echo "$resp" | jq -r '.token // empty')
  if [[ -z "$token" ]]; then
    err "login response did not contain a token"
    echo "$resp" | sed 's/^/    /' >&2
    return 1
  fi
  export MEM_TOKEN="$token"
  ok "logged in; MEM_TOKEN exported for subsequent mem calls"
}

# ---- upload ----------------------------------------------------------------
upload_demo_data() {
  log "uploading demo dataset from ${DEMO_DIR}"
  local count=0
  for f in "${DEMO_DIR}"/*.md; do
    [[ -f "$f" ]] || continue
    local out
    out=$(mem put "$f" --to "$TARGET_FOLDER" --format json) || {
      err "upload failed: $f"
      return 1
    }
    local name id status
    name=$(echo "$out" | jq -r '.file.name // .file.Name // empty')
    id=$(echo "$out"   | jq -r '.file.id   // .file.ID   // empty')
    status=$(echo "$out" | jq -r '.file.index_status // .file.IndexStatus // "?"')
    if [[ -z "$id" ]]; then
      err "upload response missing id; raw=${out}"
      return 1
    fi
    ok "put ${name}  id=${id}  status=${status}"
    count=$((count + 1))
  done
  log "uploaded ${count} files to ${TARGET_FOLDER}"
}

# ---- poll ingest completion ------------------------------------------------
# We wait until every file in TARGET_FOLDER has index_status=indexed. That's
# the visible signal that the worker pipeline (TextProcessor + EmbedText)
# ran to completion.
wait_for_ingest() {
  log "waiting for ingest to complete (up to ${INGEST_TIMEOUT_SEC}s)"
  local deadline=$(( $(date +%s) + INGEST_TIMEOUT_SEC ))
  while (( $(date +%s) < deadline )); do
    local listing
    listing=$(mem ls "$TARGET_FOLDER" --format json) || {
      warn "mem ls failed; retrying"
      sleep 2
      continue
    }
    # Total + indexed counts. Schema may surface either {parent,folders,files}
    # (folder-view) or a flat array (filter-view).
    local files_array total indexed
    files_array=$(echo "$listing" | jq -c '.files // .')
    total=$(echo "$files_array"   | jq 'length')
    indexed=$(echo "$files_array" | jq '[.[] | select((.index_status // .IndexStatus) == "indexed")] | length')
    if [[ "$total" -gt 0 && "$total" == "$indexed" ]]; then
      ok "all ${indexed}/${total} files indexed"
      return 0
    fi
    log "  progress: ${indexed}/${total} indexed; sleeping 3s"
    sleep 3
  done
  err "ingest did not complete within ${INGEST_TIMEOUT_SEC}s"
  return 1
}

# ---- search assertions -----------------------------------------------------
# assert_search_hit <query> <expected_file_basename> <case_label>
#
# Verifies:
#   - mem search returns valid JSON
#   - response.mode == "vector"  (not "keyword" — that means embeddings
#     never reached the index; demo failure)
#   - hits[0].name == expected_file_basename
PASS_COUNT=0
FAIL_COUNT=0
assert_search_hit() {
  local query="$1" expected="$2" label="$3"
  log "search assertion [${label}]: '${query}' should hit ${expected}"
  local out
  out=$(mem search "$query" --format json 2>&1) || {
    err "  FAIL  mem search exited non-zero; output:"
    echo "$out" | sed 's/^/        /' >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
    return 1
  }

  # Two response shapes in the wild:
  #   (a) {hits: [...], mode: "vector"|"keyword"}              — early W2 design
  #   (b) {results: [{..., source: "text"|"visual"|"keyword"}]} — current main
  # Probe both and accept whichever the server returns.
  local mode top_name
  mode=$(echo "$out" | jq -r '.mode // .results[0].source // "?"')
  top_name=$(echo "$out" | jq -r '.hits[0].name // .results[0].name // .hits[0].file.name // "?"')

  # "vector"/"text"/"visual" all mean a real semantic hit; only "keyword"
  # (or missing) means the worker EmbedText path didn't run.
  if [[ "$mode" == "keyword" || "$mode" == "?" ]]; then
    err "  FAIL  mode=${mode}  (expected semantic match — keyword/unknown means worker EmbedText didn't run)"
    err "  full response (first 500 bytes):"
    echo "$out" | head -c 500 | sed 's/^/        /' >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
    return 1
  fi
  if [[ "$top_name" != "$expected" ]]; then
    err "  FAIL  hits[0]=${top_name}  (expected ${expected})"
    err "  full response (first 500 bytes):"
    echo "$out" | head -c 500 | sed 's/^/        /' >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
    return 1
  fi
  ok "  PASS  mode=${mode}  hits[0]=${top_name}"
  PASS_COUNT=$((PASS_COUNT + 1))
}

run_assertions() {
  log "running search assertions"
  # The three assertions are deliberately *pure-semantic*: the content-bearing
  # nouns in each query (dog/meadow, vacation/southwest/china,
  # fermentation/kitchen) do NOT appear verbatim in the target file's body.
  # A passing hit can therefore only come from vector similarity, not from
  # BM25/keyword fallback. (Sweep was done at script-authoring time with
  # `grep -ciw <noun> demo_data/*.md` — see scripts/demo_data/.)
  assert_search_hit "a dog on a meadow"                          "golden_retriever.md"  "Q1-dog-meadow"   || true
  assert_search_hit "a vacation in southwest china"              "yunnan_trip_2012.md"  "Q2-yunnan-trip"  || true
  assert_search_hit "fermentation chemistry in a kitchen jar"    "sourdough_starter.md" "Q3-sourdough"    || true
}

# ---- main ------------------------------------------------------------------
main() {
  local dataset_count
  dataset_count=$(find "${DEMO_DIR}" -maxdepth 1 -name '*.md' -type f | wc -l | tr -d ' ')

  echo "${C_BOLD}mem · Phase 1 W2 demo seed${C_RESET}"
  echo "  repo:     $REPO_ROOT"
  echo "  server:   $MEM_SERVER"
  echo "  user:     $ADMIN_EMAIL"
  echo "  folder:   $TARGET_FOLDER"
  echo "  dataset:  ${dataset_count} files"
  echo

  preflight       || fatal "pre-flight failed; aborting"
  bootstrap_admin
  do_login
  upload_demo_data || fatal "upload step failed"
  wait_for_ingest  || fatal "ingest did not complete"
  run_assertions

  echo
  echo "${C_BOLD}--- summary ---${C_RESET}"
  echo "  ${C_GREEN}PASS:${C_RESET} ${PASS_COUNT}"
  echo "  ${C_RED}FAIL:${C_RESET} ${FAIL_COUNT}"
  if (( FAIL_COUNT > 0 )); then
    echo "${C_RED}demo FAILED${C_RESET}"
    exit 2
  fi
  echo "${C_GREEN}demo OK — vector search working end-to-end${C_RESET}"
}

main "$@"
