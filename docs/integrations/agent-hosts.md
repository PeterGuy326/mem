# Agent host certification

`mem-mcp` has one supported host transport: newline-delimited JSON-RPC 2.0
over stdio using the MCP `2024-11-05` baseline. The same adapter and memd API
are used by every host. A host-specific config shape does not create a second
implementation.

The checked-in certification has two deliberately separate evidence layers:

1. The hermetic contract starts a fake, loopback-only memd and drives the
   current `mem-mcp` process through `initialize`, the initialized
   notification, `tools/list`, and safe `tools/call` operations. It proves
   authentication, write/search/recall, context source identity,
   feedback/archive/restore/forget, active cross-workspace denial, and the
   documented failure paths.
2. The opt-in real-host runner uses an installed CLI under that host's
   documented temporary config/state roots. A config parse is never reported
   as a tool invocation. The sanitized checked-in result is the verbatim
   canonical JSON emitted by `real-hosts`; the runner validates the same schema
   and derives every host status/result from its command rows before returning.
   That result is
   [agent-host-certification.json](agent-host-certification.json).

CI runs the first layer with no desktop application, hosted model, API key, or
network download. Real-host execution is an evidence matrix, not a CI
dependency. Contract output and `diagnose-opencode` output are deliberately not
copied into the real-host report.

## Evidence levels

| Status | Direct observation |
| --- | --- |
| `REGISTERED` | The host parsed the isolated config and reported the named server and command. |
| `DISCOVERED` | The host connected and reported the `mem` server healthy after MCP discovery. |
| `INVOKED` | That host caused an attributable, safe `tools/call` to reach fake memd. |
| `NOT RUN` | The executable, safe isolation, approval, or runtime evidence was unavailable. |

`REGISTERED` and `DISCOVERED` are `PARTIAL`, not a compatibility pass. Only
`INVOKED` is `PASS`. The manifest harness calls the adapter once for each
fixture shape, but that is explicitly not external-host evidence.

## Configuration

Export values in the launching shell or the host's documented secret
mechanism. Do not replace token placeholders in a tracked config:

```bash
export MEM_MCP_COMMAND=/absolute/path/to/mem-mcp
export MEM_SERVER=http://127.0.0.1:8787
export MEM_TOKEN='<Agent token from your secret store>'
export MEM_WORKSPACE='<workspace UUID>'
```

The versioned, parser-tested examples are:

| Host | Config scope | Fixture | Runtime isolation used by the runner |
| --- | --- | --- | --- |
| OpenClaw 2026.3.28 | `mcp.servers.mem` | [openclaw.json](../../scripts/agent_certification/fixtures/openclaw.json) | `OPENCLAW_CONFIG_PATH`, `OPENCLAW_STATE_DIR` |
| Hermes Agent | `mcp_servers.mem` | [hermes.yaml](../../scripts/agent_certification/fixtures/hermes.yaml) | `HERMES_HOME` is documented, but runtime was not available |
| Claude Code 2.1.209 | project `mcpServers.mem` | [claude-code.json](../../scripts/agent_certification/fixtures/claude-code.json) | temporary project plus `CLAUDE_CONFIG_DIR` |
| OpenCode 1.17.9 | `mcp.mem` local server | [opencode.json](../../scripts/agent_certification/fixtures/opencode.json) | `OPENCODE_CONFIG_CONTENT`, `OPENCODE_CONFIG_DIR`, `OPENCODE_DB`, temporary XDG roots |
| Codex CLI 0.145.0 | `mcp_servers.mem` | [codex.toml](../../scripts/agent_certification/fixtures/codex.toml) | runtime probe not performed: `-c` still merges normal user config |

OpenCode's key is singular `mcp`, and its local command is an argv array.
Codex's checked-in example uses `env_vars`, so the token is inherited from the
process environment and never appears in the command line. Hermes and the
JSON configs retain `${MEM_TOKEN}` or `{env:MEM_TOKEN}` for runtime
interpolation.

The source and version assumptions for each shape are machine-readable in
[`scripts/agent_certification/manifests/`](../../scripts/agent_certification/manifests/).
They link to the applicable official host documentation.

## Reproduce the contract

On Linux, build the current adapter and pass its explicit path:

```bash
mkdir -p '/tmp/mem agent certification'
(cd server && go build -trimpath \
  -o '/tmp/mem agent certification/mem-mcp' ./cmd/mem-mcp)
MEM_MCP_CERT_BINARY='/tmp/mem agent certification/mem-mcp' \
  make test-agent-certification
```

On a managed macOS machine, do not run a temporary Go executable. Use the
repository's safe Linux/Docker validation workflow described in
[TESTING.md](../TESTING.md).

The opt-in real-host probe is:

```bash
report="$(mktemp)"
python3 scripts/agent_certification/certify.py real-hosts \
  --mcp-binary /absolute/path/to/mem-mcp >"$report" &&
  python3 -m json.tool "$report" >/dev/null &&
  mv "$report" docs/integrations/agent-host-certification.json
```

It retains at most 64 KiB of each command's output, bounds command time, kills
the POSIX process group on completion/timeout, and sanitizes tokens, loopback
ports, temporary roots, worktree paths, and private home paths. It never sets
`HOME` or `CODEX_HOME`. POSIX cleanup covers descendants that remain in the
runner-created process group; descendants that deliberately escape that group
are not verified. Windows process-tree cleanup is not implemented. Secret
scanning guarantees removal of the certification run's known tokens and
ordinary `Bearer` values; it is not a detector for arbitrary transformed
secrets.

## Current real-host result

The 2026-07-29 macOS arm64 run did not produce a host compatibility pass:

Every row was observed on 2026-07-29, Darwin 24.5.0 arm64, with stdio and
`MEM_TOKEN` bearer authentication supplied through the environment. Each
version, exact sanitized argv, bounded output, validation decision, operation,
result, and evidence link comes directly from the machine-readable report.

| Host | Version | Tested operations | Status/result | Evidence / reason |
| --- | --- | --- | --- | --- |
| OpenClaw | pinned 2026.3.28 | version, isolated MCP list | `NOT RUN` | [manifest](../../scripts/agent_certification/manifests/openclaw.json); both commands exited `-9` |
| Hermes | no executable | none | `NOT RUN` | [manifest](../../scripts/agent_certification/manifests/hermes.json); static schema only |
| Claude Code | pinned 2.1.209; version not observed | version, isolated MCP list | `NOT RUN` | [manifest](../../scripts/agent_certification/manifests/claude-code.json); both commands exited `-9` |
| OpenCode | 1.17.9 | version, isolated MCP list | `DISCOVERED` / `PARTIAL` | [manifest](../../scripts/agent_certification/manifests/opencode.json); the isolated host reported `mem` connected |
| Codex | 0.145.0 | version only | `NOT RUN` | [manifest](../../scripts/agent_certification/manifests/codex.json); no safe host-specific config-root isolation |

The separate metadata-only OpenCode diagnostic remains available for
troubleshooting, but its fields cannot enter the canonical certification
schema and never affect `status` or `result`.

No row that is below `INVOKED` is a production compatibility promise. The
fallback is the existing [`mem` CLI or canonical HTTP API](../RUN_LOCAL.md)
with the same workspace-bound token and memd service. The fixture harness is
not a fallback pass and does not raise a host's status.

## Troubleshooting

- `Pending approval` is not registration or discovery evidence. Approve the
  project in an interactive host session, then rerun the isolated probe.
- `failed` or `not connected` is rejected even when the CLI exits `0`.
- A missing/invalid token, insufficient role, unavailable server, malformed
  response, and timeout must remain visible tool errors. They are not skips.
- `mem-mcp` reserves stdout for JSON-RPC. Any log, foreign response ID,
  non-object JSON, malformed frame, or token on stdout fails certification.
- OpenClaw 2026.3.28 has registration-oriented `list/show/set/unset`; it does
  not have the newer connect/probe command surface, so list cannot be promoted
  beyond `REGISTERED`.

## Model boundary

Agent-host selection is independent of mem's indexing providers. The calling
Agent (Claude, Codex, OpenCode, Hermes, OpenClaw, or another MCP client) owns
reasoning and the final answer model. mem owns evidence, provenance, storage,
retrieval, and optional embedding/VLM/ASR indexing. Changing the Agent answer
model neither changes nor recertifies the embedding model, and changing an
embedding provider does not select an Agent answer model.
