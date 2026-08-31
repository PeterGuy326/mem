# mem-mcp

> npm wrapper for [fullstack-ai-infra/mem](https://github.com/fullstack-ai-infra/mem) — a portable, self-hosted memory plane for AI agents.

This package distributes the `mem-mcp` stdio MCP server binary so it can be installed with `npm install` and launched by any MCP-compatible host (Claude Desktop, Cursor, Cline, Codex, etc.).

## Install

```bash
npm install @fullstack-ai-infra/mem-mcp
```

## Usage

After install, the `mem-mcp` binary is available via `npx` or in `node_modules/.bin/`:

```bash
MEM_SERVER=http://localhost:8787 MEM_TOKEN=mem_... npx mem-mcp
```

## Download integrity

Installing this package does not run a dependency lifecycle script or download
an executable. On the first explicit `mem-mcp` invocation, the wrapper downloads
`mem-mcp-checksums.txt` and the platform binary from the same versioned GitHub
Release. It accepts exactly one checksum row for that asset, verifies the binary
with SHA-256, and only then makes it executable and starts it.

Later invocations download the small versioned checksum manifest and reverify
the cached binary; they do not download the binary again when it still matches.
A missing, corrupt, or substituted cache is replaced only after the new download
passes verification. Manifest download or parse failures fail closed by removing
any final executable whose checksum could not be established in that invocation;
download or checksum failures likewise remove unverified files. In every failure
case the MCP child process is prevented from starting. A final executable already
verified and atomically published by the invocation is not removed merely because
a later diagnostic sink fails.

The executable cache is outside the installed npm package and is isolated by
package version and platform. Defaults are `$XDG_CACHE_HOME` (or `~/.cache`) on
Linux, `~/Library/Caches` on macOS, and `%LOCALAPPDATA%` on Windows, below
`fullstack-ai-infra/mem-mcp`. Set `MEM_MCP_CACHE_DIR` to an absolute path to use
a different writable cache root. A per-asset cross-process lock serializes
verification and atomic replacement, so concurrent hosts cannot expose or
delete each other's downloads. Stale-lock recovery removes only artifacts named
by that lock owner's nonce and leaves foreign temporary files untouched.

Bootstrap and verification diagnostics use stderr. Stdout is inherited by the
verified binary and remains clean for the MCP stdio protocol. This first-run
bootstrap works with npm 12 without approving dependency install scripts or
making the installed package writable. The wrapper forwards termination signals
to the native MCP process and force-stops a child that does not exit within the
bounded shutdown grace period.

### Claude Desktop config

```json
{
  "mcpServers": {
    "mem": {
      "command": "npx",
      "args": ["-y", "@fullstack-ai-infra/mem-mcp"],
      "env": {
        "MEM_SERVER": "http://localhost:8787",
        "MEM_TOKEN": "mem_..."
      }
    }
  }
}
```

### Claude Code

```bash
claude mcp add --scope project --transport stdio \
  --env MEM_SERVER=http://localhost:8787 \
  --env MEM_TOKEN=mem_... \
  mem -- npx -y @fullstack-ai-infra/mem-mcp
```

## Configuration

| Environment variable | Flag equivalent | Default | Description |
|---------------------|----------------|---------|-------------|
| `MEM_SERVER` | `--server` | `http://localhost:8787` | memd base URL |
| `MEM_TOKEN` | `--token` | _(empty)_ | Bearer token; required for non-public operations |
| `MEM_WORKSPACE` | `--workspace` | _(personal workspace)_ | Workspace UUID |

## Compatibility

- **Platforms**: linux-amd64, linux-arm64, darwin-amd64, darwin-arm64,
  windows-amd64, windows-arm64
- **Protocol**: MCP 2024-11-05
- **Tools**: 26 built-in tools (put, get, search, context, remember, checkpoint, resume, etc.)

See the [full tool list](https://github.com/fullstack-ai-infra/mem/blob/main/docs/mcp.md) for details.

## About mem

mem is an open-source, self-hosted memory plane — one core across API, MCP, CLI, and UI. It keeps files, metadata, and embeddings under your control. Learn more at [github.com/fullstack-ai-infra/mem](https://github.com/fullstack-ai-infra/mem).
