# AI profile evaluation

This is the opt-in acceptance protocol for comparing the two server-owned
workspace profiles. It is deliberately separate from CI: a real local model or
Idealab credential is an external dependency, and a skipped provider call is
not a passing quality result.

The two decisions are different:

- `idealab-quality-v1` is justified only when it wins on the agreed retrieval
  and enrichment use cases for the paid audience, with no safety regression.
- `local-fast-v1` is justified when its fixed local model is fast enough and
  useful enough on the small-corpus use case. It is not expected to have the
  same large-corpus or generative-enrichment behavior as the quality profile.

Do not claim a result until the input corpus, profile snapshot, runtime
versions, hardware/network class, quota snapshots, and generated artifacts are
retained together. Neither profile name nor a successful embedding probe is a
quality result.

## What is measured

The checked-in [`profile-text-v1`](../benchmarks/recall/data/profile-text-v1)
fixture is a small CC0, hand-authored, text-file-only smoke corpus: five files
and four Chinese/English queries covering exact match, paraphrase, a path
filter, and a hard negative. It is intentionally suitable for a repeatable
comparison of the two 768-dimensional **text** embedding spaces.

It does not measure the following:

- structured memories, which use the independent model-free lexical path;
- image retrieval, because a caption string is not a source image and visual
  models need a separately reviewed image fixture;
- Qwen3.7 text/VLM enrichment quality, which needs its own human-scored
  annotation/caption acceptance set; or
- a production workload, a currency cost, or a universal quality threshold.

The larger [`data/v1`](../benchmarks/recall/data/v1) fixture intentionally
contains structured-memory and image-caption scenarios. Do not upload its
caption text as a `.txt` file and call that an image or VLM evaluation.

First verify the offline evaluator and the profile-text fixture without any
model, server, or credential. Use Python 3.11 or newer (substitute `python3`
only when it resolves to that version):

```bash
PYTHON=python3.11 make test-recall

python3.11 -m benchmarks.recall run \
  --dataset benchmarks/recall/data/profile-text-v1 \
  --output /tmp/mem-profile-text-lexical.json
```

The second command runs only the lexical reference. Its `0 ms` latency is a
sentinel and is not a profile performance result.

## Freeze one corpus, then run two isolated candidates

Use a disposable empty workspace (and, preferably, a disposable service
instance) for each candidate. Select the profile **before** any upload. Never
switch a populated workspace from `local-fast-v1` to `idealab-quality-v1`, or
the other direction: their text vectors are different spaces even though both
are 768-dimensional. A complete new index generation is required for a later
switch.

For each candidate, retain an evaluation manifest containing at least:

- the fixture/dataset checksum from the benchmark artifact and the exact
  input-file SHA-256 values;
- `mem profile status --format json`, including profile and pipeline revision;
- memd and Worker revision, PostgreSQL/pgvector version, local hardware class,
  and for the managed run a coarse network region/class;
- route (`text`), top-k, path-filter behavior, warm-up policy, and the exact
  measurement definition; and
- for the managed run, entitlement snapshots before and after the selected
  measurement window.

The following creates the five synthetic source files from the checked-in
manifest. Choose a new empty directory for `MEM_PROFILE_FIXTURE`; do not use a
production file directory.

```bash
export MEM_PROFILE_FIXTURE=/tmp/mem-profile-text-v1

python3.11 - "$MEM_PROFILE_FIXTURE" <<'PY'
import json
from pathlib import Path
import sys

destination = Path(sys.argv[1]).resolve()
for line in Path("benchmarks/recall/data/profile-text-v1/corpus.jsonl").read_text(
    encoding="utf-8"
).splitlines():
    record = json.loads(line)
    target = destination / record["path"].lstrip("/")
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(record["text"] + "\n", encoding="utf-8")
PY
```

With a token and workspace that belong only to this candidate, select one
profile, record its immutable snapshot, upload the fixture, and wait until all
five files are `done`. `MEM_TOKEN` is intentionally only read from the process
environment; do not put it, a provider endpoint, or an API key in an artifact.

```bash
export MEM_SERVER=http://localhost:8787
export MEM_WORKSPACE=<empty-candidate-workspace-uuid>
# Inject MEM_TOKEN through the shell/session or a secret manager.

export MEM_EVAL_DIR=/tmp/mem-profile-local-fast
mkdir -p "$MEM_EVAL_DIR"

bin/mem --server "$MEM_SERVER" --workspace "$MEM_WORKSPACE" \
  profile select local-fast-v1 --format json \
  >"$MEM_EVAL_DIR/profile.json"

bin/mem --server "$MEM_SERVER" --workspace "$MEM_WORKSPACE" \
  put "$MEM_PROFILE_FIXTURE/profile-eval" --recursive --to /profile-eval

for attempt in $(seq 1 90); do
  bin/mem --server "$MEM_SERVER" --workspace "$MEM_WORKSPACE" \
    ls --prefix /profile-eval --format json \
    >"$MEM_EVAL_DIR/files.json"
  if jq -e 'length == 5 and all(.[]; .index_status == "done")' \
    "$MEM_EVAL_DIR/files.json"; then
    break
  fi
  jq -e 'all(.[]; .index_status != "failed")' "$MEM_EVAL_DIR/files.json"
  sleep 2
done

jq -e 'length == 5 and all(.[]; .index_status == "done")' \
  "$MEM_EVAL_DIR/files.json"
```

Repeat in a different empty workspace with
`profile select idealab-quality-v1`. The managed profile must already have its
SaaS configuration, entitlement, and injected Idealab credential; see
[MANAGED_EMBEDDINGS.md](MANAGED_EMBEDDINGS.md). Its selection preflight is a
separate managed action, so take the normal search/index cost baseline only
after selection if the evaluation report separates preflight from the corpus
run.

## Collect rankings and score them

Use `--route text` and `--limit 10` for every query. Give every managed query a
new, stable idempotency key within one evaluation run; repeating a key tests
replay, not provider latency or provider usage.

```bash
bin/mem --server "$MEM_SERVER" --workspace "$MEM_WORKSPACE" \
  search "Cassini Saturn hexagonal north-pole storm rings" \
  --route text --limit 10 --format json \
  --idempotency-key 'profile-eval-local-q-en-text-exact' \
  >"$MEM_EVAL_DIR/q-en-text-exact.json"
```

The CLI has no `--scope` flag. For the fixture's path-filter query, a small
evaluation controller must call `POST /v1/search` with `scope` set to
`/profile-eval/finance/`, the same `route` and `limit`, and an idempotency key.
It should measure client end-to-end elapsed time with a monotonic clock, and
record one `ok`, `partial`, or `error` row for every query.

Convert the raw responses to a
[`mem.recall-rankings.v1`](../benchmarks/recall/fixtures/external-rankings.example.v1.json)
document before invoking the offline evaluator. The controller must:

1. map a returned virtual `path` exactly to the stable fixture `doc_id`;
2. retain rank order and score, map a known path to its stable logical fixture
   citation, and emit an unknown/out-of-fixture path as an unknown document
   rather than silently dropping it (the evaluator then reports leakage);
3. emit an empty result list and a bounded error code for every failed query;
4. record explicit provider/model/dimension/index/search/hardware fields; and
5. omit credentials, endpoint URLs, raw query/file text, vectors, raw upstream
   errors, and live file UUIDs from the ranking artifact.

The fixture's `mem://` citations are stable logical identities used to score
the ranking mapping. They do **not** prove that a live API response contained
the right `mem://files/<uuid>` citation; retain a separate, access-controlled
raw-response check for that API contract and do not present the normalized
`citation_accuracy` as such a proof.

Score both external rankings against the same fixture, then compare them:

```bash
python3.11 -m benchmarks.recall run \
  --dataset benchmarks/recall/data/profile-text-v1 \
  --rankings /secure/eval/local-fast.rankings.json \
  --output /secure/eval/local-fast.artifact.json

python3.11 -m benchmarks.recall run \
  --dataset benchmarks/recall/data/profile-text-v1 \
  --rankings /secure/eval/idealab-quality.rankings.json \
  --output /secure/eval/idealab-quality.artifact.json

python3.11 -m benchmarks.recall compare \
  --baseline /secure/eval/local-fast.artifact.json \
  --candidate /secure/eval/idealab-quality.artifact.json \
  --output /secure/eval/idealab-vs-local.comparison.json
```

The comparison exits non-zero on a forbidden-source result. Metric deltas are
otherwise informational: agree the paid-quality and local-latency thresholds
against a representative, approved evaluation set before calling either
candidate accepted. With only four queries, p95 is a diagnostic maximum, not a
service-level latency claim; retain several independent runs if latency is a
release decision.

## Cost, quality, and acceptance decision

For a managed run, snapshot the authenticated entitlement summary before and
after the selected window:

```bash
curl --fail --silent --show-error \
  -H "Authorization: Bearer $MEM_TOKEN" \
  -H "X-Workspace-ID: $MEM_WORKSPACE" \
  "$MEM_SERVER/v1/entitlements/current" \
  >"$MEM_EVAL_DIR/entitlement-after.json"
```

Report the delta of `managed_embedding_units_consumed` and
`managed_embedding_units_reserved`, plus any indeterminate/error outcome. A
managed unit is an internal accounting unit for declared profile stages; it is
not an Idealab currency price. Obtain currency cost only from the approved
provider billing source, and never copy an invoice, token, endpoint, or raw
provider response into this repository.

The paid profile should be rejected or held when it has any leakage, cannot
complete the agreed corpus/query run, lacks an explicit quality win on the
target scenarios, or cannot justify its measured managed usage. The local
profile should be rejected or held when its small-corpus latency or target
retrieval result misses the predeclared local-use budget. A larger, approved
corpus and human-scored enrichment set are required before generalizing either
decision beyond this smoke fixture.
