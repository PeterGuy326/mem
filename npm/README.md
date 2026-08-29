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