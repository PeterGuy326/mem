# ADR 0004: Workspace bundle v1/v2

- Status: Accepted
- Date: 2026-07-28
- Contract: `mem.workspace_bundle`
- Current writer schema: `2`
- Reader schemas: `1`, `2`
- Extension: `.membundle`
- Media type: `application/vnd.mem.workspace-bundle+zip`

## Context

mem is an Agent-oriented network drive. Moving from Claude Code to Codex, or
moving to another computer, must preserve the user's files, durable memories,
and resumable task history without copying authentication state or
vendor-private transcripts.

The transfer format is an untrusted input boundary. It must detect corruption,
bound decompression and parsing work, and reject ambiguous histories before a
future import service writes PostgreSQL or S3.

Task checkpoints are immutable. Their canonical JSON payload contains the task
key, base checkpoint ID, scope path, references, and producer fields.
Rewriting any of those fields invalidates `payload_sha256` and can change the
historical authorization meaning.

## Decision

### Container and fixed layout

A `.membundle` is a ZIP archive whose producer and consumer MUST support ZIP64.
The writer may use classic ZIP fields for small archives and emits ZIP64
extensions automatically when classic size or entry-count limits are crossed.
Consumers MUST NOT impose classic ZIP's 4 GiB or 65,535-entry limits.

Both schemas use this fixed layout:

```text
manifest.json
objects/folders.ndjson
objects/files.ndjson
objects/memories.ndjson
objects/memory_events.ndjson
objects/tasks.ndjson
objects/checkpoints.ndjson
objects/checkpoint_refs.ndjson
payloads/checkpoints/<checkpoint-uuid>.json
blobs/sha256/<first-two-hex>/<sha256>
checksums.sha256
```

The seven index files are required even when empty. Checkpoint payload and blob
entries are present exactly when referenced by their indexes. Unknown entries,
directories, symlinks, hard links, non-regular files, duplicate names, unsafe
paths, and unsupported compression methods are rejected.

The deterministic writer uses:

1. `manifest.json`;
2. the seven indexes in the order above;
3. checkpoint payloads sorted by UUID;
4. blobs sorted by SHA-256;
5. `checksums.sha256` last.

ZIP timestamps are fixed to `1980-01-01T00:00:00Z`, modes are regular `0600`,
metadata uses DEFLATE, and blobs use STORE. Go's standard `archive/zip`
implementation supplies ZIP64 extensions when required.

### Checksums and canonical data

`checksums.sha256` is UTF-8, LF-terminated, sorted by entry path, and contains
one line for every entry except itself:

```text
<lowercase-sha256>\t<uncompressed-decimal-size>\t<entry-path>\n
```

The reader hashes the decompressed bytes and verifies digest, declared size,
ZIP header size, entry coverage, manifest counts, blob path digest, and file
record digest. This protects integrity and corruption detection; it is not an
origin signature. Import authorization remains the responsibility of the API.

Indexes are strict NDJSON: one JSON object per non-empty line, no unknown
fields, and a final LF from the deterministic writer. JSON objects embedded in
memory attributes, source locators, and checkpoint ref metadata are canonical
compact JSON.

Each checkpoint payload is a separate compact canonical JSON document. The
reader uses `handoff.DecodeV1` and `handoff.NormalizeV1`, recomputes
`payload_sha256`, and verifies that these projected columns match the payload:

- contract and schema version;
- checkpoint kind;
- task key;
- base checkpoint ID;
- scope path;
- producer Agent and session.

The bundle additionally verifies contiguous sequence numbers, exact
base-to-previous lineage, task head-to-tail identity, contiguous ref ordinals,
and equality between indexed refs and the payload's ordered ref projection.

`handoff.collectReferences` is currently not exported. The archive package
therefore defines a `CheckpointPayloadValidator` seam. Its default v1 adapter
uses the exported handoff type and mirrors the authoritative ordered
projection. Exporting an authoritative projector from `handoff` is a desirable
follow-up; v1 does not modify that package.

### Object graph and dependencies

The format carries strong records for folders, files, memories, append-only
memory events, tasks, checkpoints, and checkpoint refs.

- Folder UUID/path/parent relationships must form the complete stored tree;
  root `/` remains implicit.
- A file's folder ID and path must agree.
- A file has exactly one content-addressed blob whose size and SHA match.
  Multiple file records may share that SHA. The archive stores one blob entry
  per unique digest, so file-entry count and blob count are intentionally
  different concepts.
- File records preserve caller/source metadata, explicit user tags, effective
  tags/time/location, and bounded annotation review state. Accepted/rejected
  decisions retain their stable IDs, versions, provenance and decision time.
  Target-local decision actor user IDs are excluded.
- Schema v2 adds those enrichment fields. Its redundant effective tags and
  summary must exactly equal values derived from explicit user tags and
  accepted annotations; source capture time/location must equal the effective
  time/location projection. Stable annotation keys are recomputed during
  validation. The writer emits v2 only.
- The reader still accepts historical v1 records, which do not contain v2
  fields. On import, their effective tags are conservatively promoted to user
  tags, while an unreviewed legacy summary is not restored as a confirmed,
  searchable summary. A v1 manifest carrying v2 fields is rejected.
- Duplicate UUIDs, task keys, idempotency-key hashes, checkpoint sequences,
  ref ordinals, payload paths, blob digests, or blob paths are rejected.
- A memory source file ID must resolve and its recorded SHA must match.
- Memory control-plane projection (`state_version`, pin state, useful and
  not-useful counts, feedback time, forgotten time) must agree with its
  contiguous append-only events.
- Portable memory records and memory events preserve only the persisted
  `idempotency_key_sha256`, never the raw replay key. They also preserve the
  applicable origin request hash, versions, reason, and time. A raw
  idempotency key cannot be reconstructed from the bundle. Actor user/token
  IDs are excluded and are never remapped into historical provenance.
- A forgotten memory is a strict tombstone: erased content, attributes,
  locator, producer data, source-file link, and original content hash cannot
  reappear in the bundle. Its terminal `forget` event must be present.
- Recognized `mem://folders`, `mem://files`, `mem://memories`,
  `mem://tasks`, and `mem://checkpoints` refs must resolve. A declared expected
  SHA must match the target object.
- External URI schemes remain inert strings. The reader never follows them,
  reads local paths, or performs network requests.

### Restore semantics

Both schemas are complete root snapshots only:

```json
{"path": "/", "complete": true}
```

It declares two future service modes:

- `fresh`: the portable target domain is empty and all stable object IDs are
  preserved.
- `merge_conservative`: disjoint objects and exact replays may be accepted;
  any divergent immutable identity or task history is a blocking conflict.

Both schemas forbid path rewriting. They also forbid checkpoint renumbering, automatic
branching, URI rewriting, content-identity aliasing, and silent overwrites.
Folder IDs may be mapped by a future restore repository, but stable file,
memory, task, and checkpoint IDs must be preserved so `mem://` URIs and
checkpoint bases stay valid.

Source `workspace_id` is audit metadata, never an instruction to select the
target. A future authorized import service determines the target workspace and
resource owner. It recomputes target-workspace request hashes while preserving
memory content hashes and checkpoint payload hashes.

Each imported file record receives its own target-local storage key. When
several records share one archive blob, the importer reopens that verified
blob and uploads it once per file record. Deleting one restored file therefore
cannot remove another file's bytes.

The import transaction records the bundle ID and archive digest in a durable
ledger. If the database cannot confirm the commit outcome, the importer keeps
every uploaded object and the HTTP boundary returns:

```json
{
  "error": "workspace_import_commit_indeterminate",
  "hint": "uploaded objects were preserved; retry the exact same bundle"
}
```

The status is `503 Service Unavailable`. Clients must retry the byte-identical
bundle against the same target workspace. A committed transaction returns the
ledger replay; a transaction proven absent may safely retry the import. Using a
different bundle cannot resolve the indeterminate operation and can leave the
preserved objects unclaimed.

### Exclusions

The manifest contains a schema-specific exact ordered exclusion declaration.
Historical v1 keeps its original 22-item list. V2 appends explicit exclusions
for file processor metadata, raw processor/provider errors, and generated
reasoning. Neither schema transports:

- users, password hashes, tokens, token hashes, sessions, memberships, or
  embedded user/token provenance IDs;
- raw memory and memory-event idempotency keys;
- S3/storage keys or presigned URLs;
- provider settings, provider credentials, or environment secrets;
- reproducible processor metadata, raw processor/provider errors, and generated
  reasoning; file source metadata and human annotation decisions are portable;
- text, visual, or face embeddings;
- entities, file-entity edges, or file relations;
- worker jobs or runtime index state.

Imported files are expected to be re-indexed by a future service. Checkpoint
payloads may themselves contain historical working directories or Agent
session labels. Because the payload is immutable, callers may include the
checkpoint unchanged or omit the entire transfer; field-level redaction is not
permitted.

### Resource and archive safety

The standard reader accepts `io.ReaderAt` plus archive size, suitable for an
HTTP upload spooled into a restricted temporary file. It validates before
returning:

- archive entry count;
- per-entry and aggregate uncompressed sizes;
- per-entry and aggregate metadata sizes;
- compression ratio;
- archive path depth;
- JSON depth;
- NDJSON line and record counts.

It rejects ZIP slip, absolute and Windows-style paths, NUL and backslash paths,
symlinks, non-regular entries, duplicate entries, checksum gaps, and unknown
v1 entries. It retains blobs in the original ZIP and exposes verified
per-digest readers; no extraction directory is required.

The public archive calls are:

```go
err := workspacebundle.Write(
    destination,
    workspacebundle.WriteInput{
        BundleData:  data,
        BlobSources: sources,
    },
    workspacebundle.WriterOptions{},
)

archive, err := workspacebundle.Open(
    temporaryFile,       // io.ReaderAt
    temporaryFileSize,
    workspacebundle.ReaderOptions{},
)
reader, err := archive.OpenBlob(fileSHA256)
```

The Writer validates metadata before writing and verifies every blob while
streaming. A late blob mismatch can leave a partial output, so callers must
write to a temporary destination and publish it only after success.

## Consequences

Positive:

- Claude Code, Codex, and a new device can consume the same vendor-neutral
  source records.
- Checkpoint payload hashes and resumable history cannot be silently changed.
- The format can be validated with only Go's standard archive, crypto, and JSON
  libraries plus mem's existing handoff contract.
- Large blobs are streamed and never require archive extraction.
- Derived search state is reproducibly rebuilt instead of importing stale model
  output.

Trade-offs:

- v1 cannot clone stable UUIDs into a second workspace in the same database
  when global primary keys already exist.
- Conservative merge rejects cases that a future alias or branch model might
  reconcile.
- Reading validates every blob once, and a future object-store import reads it
  again. The extra I/O is the cost of returning only verified archives.
- Checksums detect corruption but do not authenticate who created a bundle.
  Detached signatures or client-side encryption can be a later version.

## Rejected alternatives

### One large JSON document

It requires buffering file content, makes bounded parsing harder, and prevents
streaming content-addressed blobs.

### Reuse source storage keys

Storage keys contain target-local ownership and layout. Trusting them would
permit namespace confusion and path injection.

### Rewrite checkpoint IDs, paths, or URIs on import

Base checkpoint IDs and paths occur inside the immutable payload. Rewriting
them breaks its hash and historical meaning.

### Import embeddings and entity graphs

They are provider- and model-version-specific derived state. Rebuilding them
is safer and keeps the portable contract independent from one embedding model.
