# ADR 0005: File enrichment trust and human review

- Status: Accepted
- Date: 2026-07-29
- Issue: `#52`
- Analysis version: `file-enrichment-v1`

## Context

Uploaded files can arrive with capture facts from a phone or AI device, media
facts extracted deterministically from the bytes, and semantic descriptions or
tags proposed by a model. Treating those three sources as one metadata bag
causes several trust failures:

- reindexing can overwrite a tag the user supplied;
- model confidence can be mistaken for human confirmation;
- EXIF GPS or a timezone-naive timestamp can silently become an asserted fact;
- provider errors or generated reasoning can leak through a file-detail API;
- retries can duplicate a suggestion or revive one that a person rejected.

Upload must also remain useful when no Worker, VLM, LLM, or embedding provider
is configured.

## Decision

### Four trust layers

File enrichment is separated into four layers:

1. `source_metadata` contains bounded facts supplied by the caller or capture
   device. It accepts an offset-bearing RFC3339 `captured_at`, optional
   latitude/longitude with accuracy and label, a small `source_kind`
   vocabulary, and a non-sensitive `source_name`.
2. `processor_metadata` is a server allow-list of deterministic observations.
   Valid EXIF GPS and offset-aware time fill an empty effective projection.
   They never replace explicit source facts. A timezone-naive EXIF value is
   retained with `timeline_timezone_unknown=true` and is not converted to UTC.
3. `file_annotations` contains bounded model suggestions. Each suggestion has
   a stable key, kind, value, confidence, source, provider, processor, analysis
   version, and review state.
4. `files.summary`, `files.tags`, `files.timeline_at`, and `files.geo` remain
   compatibility projections. Effective tags are user tags plus accepted tag
   suggestions. An accepted description becomes the effective summary.
   `files.caption` is only a reviewable preview: the newest accepted
   description takes precedence, otherwise the newest pending description is
   shown. Rejected and superseded descriptions are never projected into file
   detail or visual-search snippets.

`files.user_tags` stores upload/user tags separately. Existing tags are
migrated into both `user_tags` and the effective `tags` projection so the
change cannot silently remove legacy data.

Downgrading below the enrichment migration is deliberately fail-closed for
tag provenance. Before `user_tags` and `file_annotations` are removed, the
down migration rewrites `files.tags` to the user-authored `user_tags` subset.
Accepted model tags are reproducible derived data and are discarded because
the legacy schema cannot represent their source or review state. A later
re-up therefore cannot copy those model tags into `user_tags`; enrichment may
regenerate them as pending suggestions for review.

### Asynchronous and optional models

`POST /v1/files` validates and stores source metadata with the file, then uses
the existing asynchronous indexing queue. No model call is made on the upload
request path. A disabled Worker leaves the file pending; a degraded processor
uses the explicit `partial` state; model-independent file and memory operations
remain available.

The Worker asks for one strict JSON description/tag result. It may emit at
most one description and 20 tags. Description length is at most 2,000 Unicode
scalars, tag length is at most 64, and confidence is finite within `[0,1]`.
Generated content is delimited as untrusted input. Malformed JSON-like output,
unknown fields, nested JSON-like values, hidden-reasoning wrappers, and raw
upstream errors are not persisted. Both descriptions and tags must normalize
to the same bounded plain-display value. Legacy plain replies may become one
bounded description suggestion at confidence `0.5`.

### Review and retry semantics

Every new model suggestion starts `pending`, regardless of confidence.
Confidence orders review; it never grants trust.

The stable key is:

```text
"sha256:" + lowercase_hex(
  SHA256(kind + NUL + source + NUL + normalized_value)
)
```

`normalized_value` applies Unicode lowercase, splits on Unicode whitespace,
and joins the resulting fields with one ASCII space (thereby trimming and
collapsing whitespace). `analysis_version` remains explicit provenance but is
not part of human-decision identity, so a model or prompt upgrade cannot
resurrect the same accepted/rejected value. Reprocessing updates an existing
pending suggestion, increments its optimistic version when pending
content/provenance changes, preserves terminal accepted/rejected state, and
supersedes obsolete pending model values from the previous analysis. A
rejected stable key is never resurrected.

Worker metadata carries `annotations_complete=true` only after an annotation
model returns a usable result. A valid completed empty result supersedes all
obsolete pending model suggestions and clears its stale derived caption.
Absent or false completion preserves pending suggestions and captions, so a
disabled, skipped, or failed model cannot erase review state during reindex.
Accepting or rejecting a description recomputes the caption projection in the
same transaction; a rejected value therefore cannot remain visible through
file detail or visual search.

The Phase 1 single-process indexer serializes runs per file so a slower earlier
trigger cannot overwrite a newer run's pending suggestions or embeddings.
Distributed deployments must replace this coordinator with persisted,
monotonic index generations before running multiple indexer processes.
A partial retry that produces no replacement text embedding preserves the last
usable text index; a successful completed empty extraction still clears stale
rows.

The canonical mutation is:

```http
PUT /v1/files/{fileID}/annotations/{annotationID}
Content-Type: application/json

{"decision":"accepted","expected_version":1}
```

The file owner, token path, and write scope are checked before mutation. The
same terminal decision is an idempotent replay. The opposite terminal decision
returns `409 annotation_decision_conflict`; a stale pending version returns
`409 annotation_version_conflict`. Row locks and the effective-projection
recompute keep concurrent decisions from losing user tags or creating
duplicates.

### Portability and privacy

Workspace bundles preserve source metadata, user tags, effective projections,
and annotation review state. Decision actor user IDs are target-local and are
not exported. Processor metadata is reproducible derived state and is not
transported. Provider credentials, raw provider errors, generated reasoning,
and source file content outside the existing content-addressed blob are never
added to metadata records.

GPS and source names are personal data. The upload object is strictly decoded,
limited to 4 KiB, rejects unknown fields and control characters, and does not
accept arbitrary device identifiers or metadata bags. Initial enrichment does
not send caller-provided location or device metadata to a model.

## Consequences

Positive:

- source/device facts, deterministic observations, and model suggestions have
  explicit and inspectable provenance;
- user tags survive reindex, provider changes, rejection, and bundle restore;
- upload and model-independent memory remain available offline;
- Web review can represent uncertainty without inventing a second trust model.

Trade-offs:

- model suggestions require a human decision before they affect tag
  filtering or the effective description;
- timezone-naive EXIF timestamps stay unresolved until a caller supplies a
  timezone-aware fact;
- same-content upload deduplication still returns the existing file and does
  not model multiple capture occurrences; that requires a separate
  asset-versus-occurrence decision;
- fake providers verify protocol behavior, not real-model tagging quality.

## Rejected alternatives

### Auto-accept above a confidence threshold

Model confidence is not calibrated evidence and cannot turn an inference into
a user fact.

### Store every model/processor field in one JSON object

An open metadata bag makes bounds, privacy review, projection precedence, and
portable export impossible to enforce reliably.

### Run enrichment synchronously during upload

It couples file durability and latency to optional providers and makes an
offline or degraded deployment unable to ingest files.

### Replace `files.tags` with model output

It loses user intent and makes retries/provider changes destructive. The
effective projection is retained for compatibility and recomputed from trusted
inputs instead.
