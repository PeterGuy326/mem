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

## Choose, install, and activate

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

Installation does not change memd's provider setting. Activation is a separate
explicit operation:

```bash
mem model activate qwen3-embedding-0.6b-ollama
```

or, when the combined intent is explicit:

```bash
mem model install qwen3-embedding-0.6b-ollama --activate
```

Activation requires a logged-in mem CLI. The CLI rechecks the local pinned
artifact and output dimension, then delegates activation to the canonical
`PUT /v1/providers/embedding` server route. memd performs its own Worker probe
before persisting the provider. This second probe is authoritative and also
prevents a local CLI from activating a model that the server-side Worker
cannot reach.

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
provider path. It is not a verified catalog profile.

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
