# mem

> **Agent-Native AI 网盘** · 让用户一股脑往里扔，AI 替他找回来。
>
> 开源 · 自托管 · 模型可插拔 · CLI / MCP / API 三位一体。

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-Phase%201%20MVP-orange.svg)](SPEC.md)

---

## 这是什么

**mem** 是一个开源的、自托管的、**Agent-Native** 的 AI 网盘。
形态像百度网盘，灵魂像第二大脑。

```
你说："找到 2012 年我和小明在云南拍的照片"
mem 一秒命中。
```

```
你的 Claude Desktop 说："总结我上个月的合同"
mem 通过 MCP 协议被直接调用，无需你手动喂文件。
```

---

## 三板斧

| | |
|---|---|
| 🪣 **存得爽** | 桌面拖入 / `mem put` / 浏览器扩展 / Agent 通过 API 写入 — 零摩擦 |
| 🔍 **搜得准** | 自然语言搜图：人脸 + 时间 + 语义 + EXIF 多路融合 |
| 🔗 **关联得出** | 打开一份合同 → 自动带出转账凭证、聊天记录、续费提醒 |

---

## 为什么是 Agent-Native

```bash
# 人用
mem search "草地上的金毛"

# Agent 用（同一个后端）
mem search "草地上的金毛" --format json --stream

# Claude / Cursor / Cline 通过 MCP 直接调用
mem mcp serve
```

每一个能力都必须 **CLI + MCP + API 三位一体**，Agent 是和人同等的一等公民。

---

## 与其他方案的差异

| | Google Photos | Nextcloud / Seafile | LangChain | mem |
|---|---|---|---|---|
| 开源 | ❌ | ✅ | ✅ | ✅ |
| 自托管 | ❌ | ✅ | — | ✅ |
| AI 原生数据模型 | 部分 | ❌ | — | ✅ |
| Agent / MCP 一等公民 | ❌ | ❌ | — | ✅ |
| 模型可插拔（本地/云） | ❌ | ❌ | ✅ | ✅ |

---

## 快速开始（Phase 1 MVP 完成后）

```bash
git clone https://github.com/PeterGuy326/mem.git
cd mem
docker compose up -d
mem login
mem put ~/Photos --recursive
mem search "草地上的金毛"
```

---

## 设计文档

完整产品 + 架构 + CLI / MCP / 数据模型规范见 **[SPEC.md](SPEC.md)**。

---

## 项目状态

🚧 **Phase 1 (4 周 MVP) 开发中**

- [x] SPEC v0.2（产品 / 架构 / CLI / MCP / 数据模型）
- [ ] W1：Go 后端骨架 + DB schema + `mem put/get/cat` + Token 鉴权
- [ ] W2：Python AI Worker + Image/Text Processor + pgvector
- [ ] W3：`mem search` 多路融合 + 人脸聚类 + `mem related`
- [ ] W4：MCP Server + `mem ask` + 极简 Web UI + Docker Compose 一键起

杀手 Demo：**"找到 2012 年和小明在云南拍的照片"** — 通过 Claude Desktop / MCP 完成。

---

## 仓库结构

```
mem/
├── SPEC.md                   ← 产品 + 架构 + 接口规范（Source of Truth）
├── server/                   ← Go 主服务（API / CLI / MCP Server）
├── worker/                   ← Python AI Worker（Processor + Provider）
├── web/                      ← React 极简 Web UI
├── docs/                     ← 设计文档
└── docker-compose.yml        ← 一键起本地全栈
```

---

## License

Apache-2.0 — 应用层永远完整可自托管，没有"开源阉割版"。

Copyright © 2026 mem contributors.
