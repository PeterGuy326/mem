# Curated local embedding models

mem does not require a model for `remember`, structured-memory lexical recall,
checkpoint/resume, lifecycle control, or other deterministic memory
operations. An embedding model enriches file indexing and semantic file
recall. Self-hosted users can run that optional capability locally instead of
buying a cloud embedding API.

The CLI never downloads a model from `list` or `recommend`, and `install`
requires an explicit catalog profile. In an interactive terminal, omitting the
profile shows a numbered compatible list and a `Skip` option. In a script or
Agent host, the profile ID is mandatory.

## Recommended workspace profile: `local-fast-v2`

For a new local workspace, select the server-owned `local-fast-v2` pipeline
rather than setting an arbitrary embedding provider. It fixes text embedding to
`ollama:qwen3-embedding:0.6b` with an output dimension of 768. Its visual
embedding, LLM, VLM, ASR, and rerank stages are intentionally disabled. A
disabled stage does not inherit a `MEM_DEFAULT_*` value. In particular, this
profile does not instantiate `open_clip`, because an uncached checkpoint could
trigger an implicit download. Visual support needs a later profile revision
backed by the same explicit installer, integrity, disk, and offline-cache
checks as the text model.

The current immutable snapshot is `local-fast-v2@2026-07-30.1` with pipeline
revision `file-enrichment-v2`.

The published `local-fast-v1@2026-07-29` / `file-enrichment-v1` definition
remains unchanged for an existing persisted selection. It is hidden from
`profile list` and cannot be newly selected. The server's default runtime
allowlist retains it only so an upgraded self-hosted deployment can keep
routing its existing workspace. V1 includes its historical image/audio MIME
coverage and CLIP stage; V2 intentionally does not. If a V1 workspace already
has any indexed corpus, selecting V2 is blocked even though both use the same
text embedding model: a versioned generation must rebuild the whole corpus.
An empty V1 workspace may select V2 after reviewing the new contract.

The profile selector never downloads an Ollama artifact. Use the curated
installer before starting memd and its Worker; it checks the local hardware
and disk budget, pulls only the catalog-pinned artifact, verifies its manifest
digest, and proves a 768-dimensional output:

```bash
# This is the explicit download and integrity-verification step.
# `mem profile select` never downloads a model.
mem model install qwen3-embedding-0.6b-ollama

bash scripts/dev_up.sh
```

After logging in (or exporting a valid `MEM_TOKEN`), inspect and select the
allowlisted workspace profile:

```bash
mem profile list
mem profile status
mem profile select local-fast-v2
```

Selection asks memd to make a Worker-side probe of
`ollama:qwen3-embedding:0.6b` with the requested 768 output dimensions. It
accepts only an exactly 768-dimensional result. A missing model, unavailable
Ollama endpoint, or wrong dimension fails the selection without changing the
active profile; it never silently substitutes `MEM_DEFAULT_EMBEDDING` or a
cloud provider.

The evidence boundary is deliberate: the installer/operator workflow verifies
host memory and disk plus the registry manifest digest, while the canonical
profile API independently enforces the loopback Ollama boundary, exact provider
identity, and 768-dimensional runtime output. The API does not attest host
capacity or artifact digest on its own, so direct API operators must retain the
installer evidence; server-side artifact attestation is not claimed.

A profile locks the model choices for every declared stage. It is not a
benchmark claim: `local-fast-v2` is a local/privacy-and-latency deployment
choice, not a promise about universal retrieval quality. On media that would
need a disabled stage, the current profile rejects that MIME before dispatch;
it does not download a model or send the media to an undeclared provider.

Switching to or from this profile requires a fresh, versioned index generation
and a complete rebuild before activation. Vectors from different embedding
providers must never be mixed merely because both are 768-dimensional.

## Legacy catalog path (private deployments without an active workspace profile)

The curated model catalog remains useful for reviewing a pinned local artifact
or operating the older per-provider path. It does not select or modify a
workspace AI profile. Once a workspace profile is active, legacy provider and
model-activation commands cannot override it.

Start Ollama, then inspect the current machine:

```bash
mem model list
mem model list --json
mem model recommend --language zh
```

The recommendation is advisory. It first removes profiles that do not match
the runtime, OS, architecture, available memory/disk, or current 768-dimensional
corpus. It then ranks by declared language coverage, local resource headroom,
whether the pinned artifact is already installed, and download size. Vendor
name and public download counts are not ranking inputs.

Install one profile explicitly:

```bash
mem model install qwen3-embedding-0.6b-ollama
```

The command performs these steps in order:

1. evaluate the catalog profile against the local device and Ollama runtime;
2. call Ollama `POST /api/pull` with the catalog-pinned model tag and show
   bounded progress (Ctrl-C cancels the HTTP request);
3. read `GET /api/tags` and require the exact catalog manifest digest;
4. call modern batched `POST /api/embed` with `dimensions: 768` and
   `truncate: false`;
5. require one 768-dimensional vector, then stop in `verified` state.

Installation does not change memd's provider setting. In the legacy path,
activation is a separate explicit operation:

```bash
mem model activate qwen3-embedding-0.6b-ollama
```

or, when the combined intent is explicit:

```bash
mem model install qwen3-embedding-0.6b-ollama --activate
```

Legacy activation requires a logged-in mem CLI and a workspace with no active
AI profile. The CLI rechecks the local pinned artifact and output dimension,
then delegates activation to the canonical `PUT /v1/providers/embedding`
server route. memd performs its own Worker probe before persisting the
provider. This second probe is authoritative and also prevents a local CLI
from activating a model that the server-side Worker cannot reach.

Set a non-default Ollama endpoint with `OLLAMA_BASE_URL` or
`--ollama-url`. The CLI and the mem Worker must ultimately address the same
runtime artifact for activation to succeed.

Cancellation, malformed progress, unavailable hardware, unknown profiles,
digest mismatches, and wrong dimensions all fail before activation. Those
failures do not disable structured-memory lexical recall or model-independent
operations.

## Catalog contract

The embedded source is
`server/internal/modelcatalog/catalog.v1.json`. Its public schema identifier is
`mem.model-catalog/v1`. Each profile records:

- stable profile ID, display name, catalog version, and runtime;
- upstream model identity and model-card/source URL;
- exact Ollama tag, Ollama library URL, and registry manifest URL;
- full manifest SHA-256 digest and estimated download bytes;
- expected vector dimension;
- SPDX-style license identifier and license URL;
- explicitly claimed language codes and notes; and
- coarse minimum available memory/disk, OS, architecture, installability, and
  any unavailable reason.

Language recommendation is fail-closed: a profile is selected for a requested
language only when that language code is explicitly present. A broad
“multilingual” description is not treated as evidence that every language is
supported.

The seed catalog currently contains:

| Profile | Runtime model | Dimension | Status |
| --- | --- | ---: | --- |
| `nomic-embed-text-v1.5-ollama` | `nomic-embed-text:v1.5` | 768 | installable |
| `qwen3-embedding-0.6b-ollama` | `qwen3-embedding:0.6b` | requested 768 (model supports MRL dimensions) | installable |
| `granite-embedding-278m-multilingual-ollama` | `granite-embedding:278m` | 768 | installable |
| `bge-m3-567m-ollama` | `bge-m3:567m` | fixed 1024 | visible but unavailable for the current corpus |

An arbitrary `mem provider set embedding ollama:<model>` remains an advanced
provider path for a private deployment without an active workspace profile. It
is not a verified catalog profile and cannot override `local-fast-v2` (or any
other selected workspace profile). In SaaS mode, cloud provider specs must be
made available through an allowlisted workspace profile rather than this path.

## Reproducing or updating artifact metadata

Ollama tags are mutable registry references, so catalog review pins the exact
manifest bytes observed for a release. For each `runtime_manifest_url`:

```bash
manifest_file="$(mktemp)"
curl -fsSL '<runtime_manifest_url>' -o "${manifest_file}"
shasum -a 256 "${manifest_file}"
jq '[.layers[].size] | add' "${manifest_file}"
rm -f "${manifest_file}"
```

`manifest_digest` is `sha256:` plus the SHA-256 of those exact response bytes.
`artifact_size_bytes` is the sum of every manifest layer size (model, license,
parameters, and other downloadable layers); it excludes the small manifest
config metadata object. Reviewers must also compare the model card, license,
dimension, Ollama library tag, and registry manifest URL. A changed digest or
size requires a reviewed catalog update and tests—it must never be accepted
silently at install or activation time.

The required regression uses stub HTTP servers and does not download real
model artifacts. A real disposable Ollama smoke test is optional and must be
reported as `NOT VERIFIED` when the runtime or download budget is unavailable.
