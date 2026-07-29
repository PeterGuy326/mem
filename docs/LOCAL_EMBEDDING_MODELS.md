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

## Recommended workspace profile: `local-fast-v1`

For a new local workspace, select the server-owned `local-fast-v1` pipeline
rather than setting an arbitrary embedding provider. It fixes text embedding to
`ollama:qwen3-embedding:0.6b` with an output dimension of 768 and fixes visual
embedding to `clip:ViT-B-32` (512 dimensions). Its LLM, VLM, ASR, and rerank
stages are intentionally disabled. A disabled stage does not inherit a
`MEM_DEFAULT_*` value.

CLIP visual search is optional and provisioned separately. It is not part of
the local text-profile activation probe: preinstall/cache the CLIP runtime and
weights before intentionally enabling image indexing, or accept that visual
embeddings will be unavailable/degraded. `mem profile select local-fast-v1`
does not download or verify CLIP.

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
mem profile select local-fast-v1
```

Selection asks memd to make a Worker-side probe of
`ollama:qwen3-embedding:0.6b` with the requested 768 output dimensions. It
accepts only an exactly 768-dimensional result. A missing model, unavailable
Ollama endpoint, or wrong dimension fails the selection without changing the
active profile; it never silently substitutes `MEM_DEFAULT_EMBEDDING` or a
cloud provider.

A profile locks the model choices for every declared stage. It is not a
benchmark claim: `local-fast-v1` is a local/privacy-and-latency deployment
choice, not a promise about universal retrieval quality. On media that would
need a disabled stage, mem may retain the outputs of the enabled stages and
report partial processing; it does not send that media to an undeclared model.

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
is not a verified catalog profile and cannot override `local-fast-v1` (or any
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
