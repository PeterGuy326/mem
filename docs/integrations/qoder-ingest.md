# Qoder CLI-session ingestion

`mem ingest qoder` is a local-artifacts connector that turns AI-CLI conversation
transcripts into first-class mem input. It reads Qoder/CLI session stores
(`~/.qoder/projects/**/*.jsonl` by default), normalizes each conversation turn
into a memory `observation`, and writes it through the standard
`POST /v1/memories` API — the same path `mem remember` uses — so ingested records
are immediately recallable from the API, MCP, CLI, and Web UI with no changes to
those surfaces.

## Usage

```
mem auth login                       # once, if not already logged in
mem ingest qoder                     # default ~/.qoder/projects
mem ingest qoder --root ~/sessions   # custom store
mem ingest qoder --dry-run           # parse + plan: no writes, cursors untouched
mem ingest qoder --limit 200         # stop after 200 memories
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--root` | `~/.qoder/projects` | glob base scanned recursively for `*.jsonl` |
| `--path-root` | `/AgentTranscripts` | virtual path prefix for ingested memories |
| `--state-dir` | `~/.mem/ingest/qoder` | checkpoint cursor directory |
| `--dry-run` | `false` | parse and plan only; do not write |
| `--limit` | `0` | stop after N memories (0 = no limit) |

## What is ingested

Each parseable line becomes one memory:

- **kind** — `observation`
- **content** — the message text (capped at 512 KiB)
- **path** — `<path-root>/<project>/<session>`, where `<project>` is the first
  path segment under the ingest root and `<session>` is the transcript file name
  minus `.jsonl`
- **source** — `{"type":"qoder","ref":<abs path>,"locator":{"line":N}}`
- **producer** — `session_id` (session slug) and `agent_id` (the model/agent id
  recorded on the line, when present)
- **event_at** — the message timestamp (RFC 3339 or epoch), when present
- **Idempotency-Key** header — `qoder:<hash(abs:line)>`, stable per file+line

### Tolerant parsing

The transcript schema is intentionally unopinionated. Each JSONL line is decoded
and probed for well-known key shapes for message text (`content`, `text`,
`message.content`, `output`, `body`…), role (`role`, `type`, `speaker`, `sender`),
agent/model, and timestamp (`timestamp`, `created_at`, `createdAt`, `ts`, `time`,
plus epoch-second/millisecond numeric values). Lines that do not decode as JSON,
carry no ingestible text, or are JSON-LD continuations are skipped rather than
failing the run. Undocumented fields do not block ingestion.

> The connector targets the transcript store described in
> [fullstack-ai-infra/mem#103](https://github.com/fullstack-ai-infra/mem/issues/103).
> If your CLI emits a materially different shape, the parser keys live in
> `server/cmd/mem/qoder_transcript.go` and are trivially extended.

## Checkpoints & idempotency

Ingestion is **incremental and idempotent**:

- A per-file cursor (`~/.mem/ingest/qoder/<sha1(abs)>.json`) records the highest
  already-ingested line. A re-run parses only lines appended since the last run.
- Even if the cursor is lost, the stable `Idempotency-Key` per line makes a
  re-post an idempotent replay (`replayed` responses are counted, not
  duplicated).

Deleting `~/.mem/ingest/qoder` resets all cursors (safe: ids remain idempotent).

## Privacy note

AI-CLI transcripts routinely contain material users did not consciously choose
to archive: API keys, tokens, secrets, or other credentials they pasted into a
prompt, plus personal data that scoped into an agent conversation. `mem ingest
qoder` writes the message text **verbatim** into the vault as a normal memory
`observation`, where it becomes recallable through the same search/context/USI
surfaces and governed by the same token/path permissions as any other record.

Before running ingest in an environment that matters, consider:

- Using `--dry-run` first to preview exactly what would be ingested.
- Purging secrets from the transcript store (or excluding those files) so they
  are never written.
- Deleting the state dir and re-ingesting is **not** a redaction path:
  `forget`/delete on the resulting memories is the way to remove credentials
  you do not want retained. Plainly, **do not ingest a transcript you would be
  unwilling to have searched later.**

## Recall

Because ingestion writes through `POST /v1/memories`, records appear exactly like
any other memory. For example:

```
mem search "recruit rubric" --path /AgentTranscripts
```

## MyContext interop (follow-up, not a blocker)

[#103](https://github.com/fullstack-ai-infra/mem/issues/103) accepts MyContext
interop as a deferred follow-up (AC-003). The connector is structured so a
MyContext bridge can reuse the same normalize-and-write path later:

1. **Local artifacts connector → MyContext bridge** — once openTrinity/mycontext
   ships its planned "agent interactions" source, a read-only sync can ingest
   from its SQLite vault (source-flagged) instead of re-parsing JSONL.
2. **ACP runtime capture** — mycontext already embeds an `opencode` ACP runtime;
   capturing session streams at the runtime level would avoid parsing files at
   all.

Neither is required for this connector to be useful today.