# mem — 面向 Agent 的可迁移网盘

> Spec-Driven Development · v0.2 · 2026-07-28
>
> 项目名 `mem` 为工作名，最终发布前可改。
> License: Apache-2.0（应用层全开源）。

---

## 0. TL;DR

**mem** 是一个开源、自托管、面向 Agent 的可迁移网盘。
形态像网盘，机器入口像共享 Memory Plane。

- **跨 Agent 接续**：Claude Code、Codex 等 Agent 读写同一份任务状态与证据
- **跨设备恢复**：换电脑后恢复 workspace 的资产、记忆和任务状态；Token、密钥
  与宿主登录态不进入迁移包，必须在目标环境重新授权
- **用户可见可控**：人能像使用网盘一样查看、整理、导出和遗忘全部内容
- **Agent-Native**：CLI / MCP / API 三位一体，Agent 是一等公民
- **AI 检索与关联**：自然语言找回文件、记忆及其关系
- **索引模型可插拔**：Embedding / VLM / ASR / OCR 可本地或云端运行
- **不内置回答模型**：mem 返回证据，外部 Agent 负责推理和最终答案

北极星场景、迁移边界和当前差距以 [GOAL.md](GOAL.md) 为准。

---

## 1. 产品定位

### 1.1 一句话

> 把 Agent 的工作资产、长期记忆和任务状态还给用户，使它们能在不同 Agent、
> 不同会话和不同电脑之间无痛接续，并始终像网盘内容一样可见、可控、可迁移。

### 1.2 与现有玩家的差异化

| 玩家 | 缺什么 |
|------|--------|
| Google Photos / iCloud | 闭源、无 CLI、不能被 Agent 使用 |
| Nextcloud / Seafile | 无 AI 原生数据模型 |
| LangChain / LlamaIndex 向量库 | 是库不是产品，无 UI、无文件管理 |
| Mem.ai / Reflect | 闭源、文件类型窄、不开放 API |
| MCP filesystem server | 只是本地 FS，无 AI 理解 |

**mem 的位置**：开源 + Agent-Native + AI 原生 + 自托管 的 personal data layer。

### 1.3 三个产品支点（任何功能都必须服务于其中之一）

1. **Agent 接得上** — 跨 Agent、跨会话获得正确的任务状态和证据
2. **设备迁得走** — workspace 可校验、可导出、可导入、可恢复
3. **用户看得见** — 文件、记忆、交接、关系和生命周期都能在网盘 UI 中管理

自然语言搜索、多模态索引和关系召回是实现上述支点的关键能力，但不是独立的产品
终点。自然语言搜图仍是核心功能和首发杀手场景，与迁移能力并行推进。

---

## 2. 目标用户与核心场景

### 2.1 Persona

- **P-Human-A**：自托管发烧友 / 隐私敏感用户 / 重度信息整理者
- **P-Human-B**：垂直专业用户（设计师 / 律师 / 科研，Phase 3 关注）
- **P-Agent**：Claude Desktop / Cursor / Cline / 用户自写 Agent
  - 通过 MCP 或 CLI 调用 mem
  - 是高频用户，远超人类频次

### 2.2 北极星用户故事

**US-0 跨 Agent 接续**
> Claude Code 完成一半任务并写入 handoff
> → Codex 连接同一 workspace，恢复目标、进度、决定、阻塞和相关产物
> → 用户无需重新解释背景即可继续。

**US-1 自然语言搜图**
> "我想找到 2012 年高中和小明在云南拍的照片"
→ mem 一秒命中。

**US-2 Agent 通过 MCP 读取知识**
> 用户对 Claude Desktop 说："总结我上个月的合同"
→ Claude 调用 `mem_context(query="合同", since="last month")`
→ mem 返回带出处的证据包，Claude 完成总结，**无需用户手动喂文件**。

**US-3 关联召回**
> 打开一份租房合同 → 自动展示：转账凭证、房东聊天、上一份合同、看房照片、续费提醒。

### 2.3 非目标（Phase 1 明确不做）

- ❌ 团队协作 / 权限分级
- ❌ 移动端 App
- ❌ 桌面同步盘（Phase 2）
- ❌ 分享链接（Phase 2）
- ❌ 在线预览编辑（OnlyOffice 类）
- ❌ 视频在线播放转码
- ❌ 内置聊天产品或通用 Agent runtime

---

## 3. 功能需求

### F1 · 存储与上传

| ID | 需求 |
|----|------|
| F1.1 | 支持单文件、目录、stdin 流、远程 URL 抓取四种入库方式 |
| F1.2 | SHA-256 标识内容身份；不同名称/路径可以引用相同内容。bundle 对相同内容只归档一份 blob，目标对象存储当前仍为每个文件保留独立 key |
| F1.3 | 分块上传，支持断点续传（>10MB 强制分块） |
| F1.4 | 文件入库返回 `file_id`；结构化记忆写入返回独立的 `memory_id` |
| F1.5 | 入库后异步触发 AI Pipeline，不阻塞用户 |

### F2 · AI 索引流水线

| ID | 需求 |
|----|------|
| F2.1 | 文件类型识别（基于 magic number，非扩展名） |
| F2.2 | Processor 接口可插拔，每类文件一个 Processor |
| F2.3 | Phase 1 必备 Processor：Image / Text / PDF / Audio |
| F2.4 | 每个文件至少产出：text embedding、metadata、entities |
| F2.5 | 图片额外产出：visual embedding (CLIP)、VLM caption、face embeddings |
| F2.6 | 索引进度可查询（`mem status`），失败可重试 |

### F3 · 搜索

| ID | 需求 |
|----|------|
| F3.1 | 入参：自然语言 query + 可选过滤（type / since / until / face / tag） |
| F3.2 | Query Planner：规则/索引元数据拆解 query → 实体 + 语义 + 时间 + scope |
| F3.3 | 多路召回：visual / text caption / metadata 并行 → rerank |
| F3.4 | P99 < 500ms（10 万文件级别） |
| F3.5 | 支持流式返回 `--stream` |
| F3.6 | 输出格式：人类可读（默认）/ JSON（`--format json`） |

### F4 · 关联

| ID | 需求 |
|----|------|
| F4.1 | 四种关系：同事件 / 同人 / 同主题 / 续作 |
| F4.2 | `mem related <file_id>` 默认返回 Top 10 |
| F4.3 | 可按关系类型过滤 |
| F4.4 | 关系计算异步：入库时计算 + 后台周期性更新 |

### F5 · Context（Agent 证据包）

| ID | 需求 |
|----|------|
| F5.1 | `mem context "..."` → 返回有大小预算的文件/结构化记忆证据包 |
| F5.2 | 每条 evidence 必须含 source kind/id、稳定 citation、内容哈希、片段和 locator |
| F5.3 | mem 只走 recall → context pack；回答与行动由调用方 Agent 完成 |
| F5.4 | `source=all|file|memory`；结构化记忆在无 Worker、无模型时也必须可立即召回 |
| F5.5 | 联合召回单路失败但仍有证据时返回 `200 + partial=true + warnings[]`；无幸存证据时返回 `502 context_unavailable` |

### F5A · 结构化 Agent 记忆

| ID | 需求 |
|----|------|
| F5A.1 | `remember` 保存 observation / decision / preference / task_state / fact / note / artifact |
| F5A.2 | 每次写入保留 workspace、路径、来源、producer、事件时间、内容哈希和创建者 |
| F5A.3 | 调用方必须提供 workspace 内唯一幂等键；同请求重放返回原 ID，不同请求复用键返回冲突 |
| F5A.4 | 一条记录表示一次不可变事件；相同内容使用不同幂等键时保留为两次独立发生 |
| F5A.5 | 读取与召回统一执行 workspace、Token scope 和路径边界，越权对象按不存在处理 |
| F5A.6 | 当前基线使用 PostgreSQL 词法/FTS/trigram 召回；语义巩固、反馈和遗忘后续版本化演进 |

REST 写入契约：`Idempotency-Key` 是必填 HTTP Header，不放在 body 中。首次提交
返回 `201 + replayed=false`；同 key、同归一化请求返回原记录及
`200 + replayed=true`；同 key、不同请求返回 `409 idempotency_conflict`。

### F6 · 实体管理

| ID | 需求 |
|----|------|
| F6.1 | 人脸聚类：相同人脸自动聚到同一 cluster_id |
| F6.2 | `mem face list` 列出聚类，`mem face name <id> "小明"` 命名 |
| F6.3 | 时间轴：`mem timeline 2012` 输出该年所有文件按月分组 |

### F7 · 鉴权与配额

| ID | 需求 |
|----|------|
| F7.1 | 多用户支持，自部署默认单用户 |
| F7.2 | Token 模型：name / scope / quota / paths / expires / redact-pii |
| F7.3 | Scope：search / read / write / delete / admin |
| F7.4 | 配额：calls/day、storage、索引模型 calls/tokens per day |
| F7.5 | 429 错误必须返回 retry-after |

当前路由 scope：`POST /v1/memories` 需要 `write`，若关联
`source.file_id` 额外需要 `read`；memory list/detail 需要 `read`；
feedback/archive/restore 需要 `read + write`；forget 需要 `delete` 和允许删除的
workspace 角色；
`POST /v1/search` 与 `POST /v1/context` 需要 `search`。`admin` 包含全部 scope，
所有路由仍需执行 workspace 与 Token 路径边界。`POST checkpoint` 需要 `write`，
若引用 `mem://` 证据还需要 `read`；`resume` 的确定性 checkpoint 恢复只需要
`read`，只有可选的语义 Context Pack 增强需要 `search`。

### F8 · Provider 可插拔

| ID | 需求 |
|----|------|
| F8.1 | Provider 类型：Embedding / Visual Embedding / VLM / ASR / OCR |
| F8.2 | 每类 Provider 一个接口，社区可贡献 adapter |
| F8.3 | Phase 1 内置：Ollama、OpenAI、Anthropic |
| F8.4 | 用户 API Key 仅本地存储，不上报任何服务 |
| F8.5 | 配置：`mem provider set <type> <vendor>:<model>` |
| F8.6 | 本地 embedding 目录必须版本化、厂商中立，并只把当前运行时和 768 维语料契约已验证的 profile 标为可安装 |
| F8.7 | 模型下载必须由用户显式选择；pull 后校验固定 manifest digest，再用批量 `/api/embed` + `dimensions=768` probe |
| F8.8 | 安装和激活是两个状态；只有本地校验通过且服务端 canonical provider probe 接受后才可激活 |

这里的 Provider 只服务于文件理解与索引。上层 Agent 使用哪个回答模型不由 mem
配置，也不会通过 mem Worker 代理调用。

本地模型目录的 schema、设备兼容性、integrity pin、推荐输入和故障边界见
[`docs/LOCAL_EMBEDDING_MODELS.md`](docs/LOCAL_EMBEDDING_MODELS.md)。跳过或安装
失败不得影响结构化记忆 lexical lane、checkpoint/resume 或其他 model-independent
operations。任意 `ollama:<model>` 自定义 provider 仍是高级路径，不能冒充目录中
已验证的 profile。

### F9 · 任务交接与可移植性

| ID | 需求 |
|----|------|
| F9.1 | 定义版本化 handoff schema，至少包含目标、进度、决定、下一步、阻塞、产物引用和 producer |
| F9.2 | checkpoint 写入必须幂等；resume 必须返回 handoff、相关证据和缺失项 |
| F9.3 | workspace 导出包必须包含版本化 manifest、内容哈希、对象引用和 scope，不默认包含密钥 |
| F9.4 | import/restore 必须可重试，并明确报告 schema 不兼容、内容缺失、冲突和 commit 结果不确定 |
| F9.5 | Claude Code、Codex 等宿主适配器只做格式转换，不成为内部数据真相 |
| F9.6 | Web UI 必须能查看 handoff、checkpoint、导入导出状态及其来源 |

`mem.handoff` v1 的权威结构见
[`docs/schemas/handoff.v1.schema.json`](docs/schemas/handoff.v1.schema.json)，
并采用独立的 task/checkpoint/reference 模型。checkpoint 是不可变版本；
第二个及后续版本必须用 `base_checkpoint_id` 对当前 head 做 compare-and-swap。
实现和取舍见
[`ADR 0002`](docs/adr/0002-versioned-task-handoff.md)。

workspace import 的数据库 commit 若无法确认结果，服务端不得删除已上传对象，也
不得返回普通内部错误。API 固定返回
`503 workspace_import_commit_indeterminate`，并要求调用方用完全相同的 bundle
重试；幂等 ledger 会把已成功提交的原请求识别为 replay。bundle 契约见
[`ADR 0004`](docs/adr/0004-workspace-bundle.md)。

---

## 4. 非功能需求

| 维度 | 目标 |
|------|------|
| **性能** | 搜索 P99 < 500ms（10 万文件） |
| **入库吞吐** | 单机 ≥ 100 文件/分钟（含 AI 处理，本地模型） |
| **隐私** | 默认零外发；调用云 Provider 前明确提示 |
| **部署** | `docker compose up` 一键起，含所有依赖 |
| **资源** | 最低 4 核 8GB 可用；本地 VLM 需 16GB+ |
| **可观测** | Prometheus metrics + 结构化日志 |
| **可移植** | 数据全部存可导出格式，零厂商锁定 |

---

## 5. 系统架构

```
┌────────────────────────────────────────────────────────────────┐
│  入口层（三位一体，共享同一鉴权 + 同一 Service 层）             │
│                                                                 │
│   CLI (Go)         MCP Server (Go)        REST/gRPC API        │
│   人 + Agent       Claude/Cursor          第三方集成            │
│        │                  │                     │              │
│        └──────────────────┴─────────────────────┘              │
│                           │                                     │
│              Gateway: Token / Scope / Quota / Rate              │
├────────────────────────────────────────────────────────────────┤
│  Service 层（Go）                                               │
│   File · Memory · Search · Context · Related · Face · Provider │
├────────────────────────────────────────────────────────────────┤
│  AI Worker（Python，gRPC，可水平扩展）                          │
│   Processor: Image / Text / PDF / Audio / ...                  │
│   Provider Adapter: Ollama / OpenAI / Anthropic / ...          │
├────────────────────────────────────────────────────────────────┤
│  Data 层                                                        │
│   PostgreSQL + pgvector  ·  Redis (queue/cache)                │
│   S3 协议存储（MinIO / OSS / R2 / 本地 FS）                    │
├────────────────────────────────────────────────────────────────┤
│  Web UI（AI 网盘 + 来源/纠错/权限/遗忘的信任控制面）            │
└────────────────────────────────────────────────────────────────┘
```

### 5.1 关键技术决策

| 决策 | 选择 | 备选 | 理由 |
|------|------|------|------|
| 主服务语言 | **Go** | Rust / Node | 单二进制、网盘场景成熟 |
| AI Worker 语言 | **Python** | — | AI 生态唯一选择 |
| Go ↔ Python 通信 | **gRPC** | HTTP / NATS | 跨语言稳定、流式友好 |
| 元数据 + 向量 | **PostgreSQL + pgvector** | Qdrant / Milvus | Phase 1 一个库搞定；亿级再迁 |
| 对象存储 | **S3 协议** | 私有协议 | 一份代码适配所有云 |
| 消息队列 | **Redis + Asynq** | RabbitMQ / NATS | 轻量、Go 原生 |
| 前端框架 | **React + Vite + Tailwind** | Vue / Svelte | 主流、AI 生态多 |
| 容器化 | **Docker Compose**（开发）/ **Helm**（生产） | — | 标准 |

---

## 6. 数据模型

### 6.1 核心表

```sql
-- 用户
users (id, email, password_hash, created_at)

-- Token（Agent token 绑定 workspace；空 paths[] 表示不限制，显式 "/" 表示根）
tokens (id, user_id, workspace_id, name, hash, scopes[], quota_jsonb,
        paths[], expires_at, redact_pii, created_at)
-- redact_pii 是保留字段；未实现前服务端拒绝创建/使用该类 token，避免静默泄漏

-- 文件夹（一等公民，允许空文件夹存在）
folders (
  id              uuid pk,
  user_id         uuid,
  parent_id       uuid null,               -- null = 根目录的直接子
  path            text not null,           -- 规范化绝对路径，如 "/Photos/2012"
  name            text not null,           -- 末段，如 "2012"
  created_at      timestamptz,
  updated_at      timestamptz,
  unique (user_id, path)                   -- 同一用户路径唯一
);
-- 根目录"/"对每个用户隐式存在，可不入表（path='/'）；也可作为单条 user_id, parent_id=null, path='/' 入表，二选一。
-- 实现选择：不存根，仅靠 path 派生。

-- 文件主表（AI-Native 数据模型）
files (
  id              uuid pk,
  user_id         uuid,
  name            text,
  path            text,                    -- 虚拟路径（冗余存储父目录绝对路径，方便检索）
  folder_id       uuid null,               -- 指向 folders.id；根目录文件为 null
  size            bigint,
  sha256          text,                    -- 秒传 key
  mime            text,
  storage_key     text,                    -- S3 key
  -- AI 字段
  summary         text,                    -- 可选派生摘要（不得替代原文）
  caption         text,                    -- VLM caption（图片）
  tags            text[],                  -- 自动标签
  timeline_at     timestamptz,             -- EXIF / 内容推断时间
  geo             point,                   -- 经纬度
  -- 状态
  index_status    text,                    -- pending / processing / done / failed
  -- 时间
  created_at      timestamptz,
  updated_at      timestamptz
);

-- Agent 结构化记忆：一次写入代表一次可审计的发生，不在写入时调用模型
memories (
  id                    uuid pk,
  workspace_id          uuid,
  created_by_user_id    uuid null,
  created_by_token_id   uuid null,         -- 仅服务端审计，不出现在公共 JSON
  kind                  text,
  content               text,
  attributes            jsonb,
  path                  text,
  event_at              timestamptz null,
  source_type           text,
  source_ref            text,
  source_file_id        uuid null,
  source_file_sha256    text,
  source_locator        jsonb,
  producer_agent        text,
  producer_session      text,
  producer_task         text,
  idempotency_key_sha256 text,             -- 调用方 key 只持久化 SHA-256
  request_sha256        text,              -- forgotten tombstone 固定为全 0
  content_sha256        text,
  lifecycle_status      text,              -- active / archived / forgotten(tombstone only)
  state_version         bigint,            -- control-plane optimistic CAS
  pinned_at             timestamptz null,
  useful_count          int,
  not_useful_count      int,
  feedback_at           timestamptz null,
  forgotten_at          timestamptz null,
  forgotten_by_user_id  uuid null,
  forgotten_by_token_id uuid null,         -- 仅服务端审计
  created_at            timestamptz,
  updated_at            timestamptz,
  unique (workspace_id, idempotency_key_sha256)
);

-- feedback / archive / restore / forget 的 append-only 审计事件。
-- 幂等键只保存 SHA-256，不保存调用方明文 key。
memory_events (
  id                      uuid pk,
  workspace_id            uuid,
  memory_id               uuid,
  action                  text,
  actor_user_id           uuid null,
  actor_token_id          uuid null,       -- 仅服务端审计
  idempotency_key_sha256  text,
  request_sha256          text,
  replay_principal_sha256 text,            -- forget 精确重试的单向用户收据
  expected_version        bigint,
  resulting_version       bigint,
  reason                  text,
  created_at              timestamptz,
  unique (workspace_id, idempotency_key_sha256)
);

-- 实体（人 / 地 / 物 / 事件，最重要的是人脸）
entities (
  id           uuid pk,
  user_id      uuid,
  type         text,                       -- person / place / org / event
  name         text,                       -- 可由用户命名
  metadata     jsonb,                      -- 人脸特征 vec、聚类信息
  created_at   timestamptz
);

-- 文件 ↔ 实体（多对多）
file_entities (file_id, entity_id, confidence)

-- 文件 ↔ 文件 关系
file_relations (
  src_id, dst_id,
  type           text,                    -- 同事件/同人/同主题/续作
  score          float,
  computed_at    timestamptz
);

-- 文本嵌入（长文档分块）
embeddings_text (
  id              uuid pk,
  file_id         uuid,
  chunk_index     int,
  chunk_text      text,
  embedding       vector(D_text),
  provider        text                     -- 固化实际向量空间，禁止查询/语料漂移
);

-- 视觉嵌入（图片）
embeddings_visual (
  file_id         uuid pk,
  embedding       vector(512)             -- CLIP / SigLIP
);

-- 人脸嵌入
embeddings_face (
  id              uuid pk,
  file_id         uuid,
  entity_id       uuid,                   -- 聚类后归属
  bbox            jsonb,
  embedding       vector(512)
);
```

### 6.2 索引策略

- `files (user_id, timeline_at)` — 时间过滤
- `files (sha256)` — 秒传查重
- `files (user_id, folder_id)` — 列文件夹内容（最高频）
- `files (user_id, path text_pattern_ops)` — 子树候选索引；查询必须使用字面量段边界，
  不得把路径中的 `%`、`_` 解释为 wildcard
- `folders (user_id, parent_id)` — 列子文件夹
- `folders (user_id, path)` UNIQUE — 路径唯一性约束
- `memories (workspace_id, idempotency_key_sha256)` UNIQUE — 不落明文幂等键的幂等写入
- `memories` FTS + trigram — 无模型的确定性立即召回
- `embeddings_* (embedding)` — pgvector HNSW
- `file_entities (entity_id)` — 反查"和某人有关的所有文件"

### 6.3 文件夹一致性规则（重要）

| 操作 | 必须保证 |
|------|---------|
| **创建文件夹** | 自动创建所有缺失的父级（mkdir -p 语义） |
| **上传文件到 /a/b/c.jpg** | 确保 /a 和 /a/b 文件夹存在（自动 mkdir -p） |
| **重命名文件夹 /a → /A** | 批量更新所有子文件夹、文件和 memories 的 path 前缀，一个事务内完成 |
| **移动文件夹 /a → /b/a** | 同上，前缀替换 + 父级 id 改 |
| **删除文件夹** | 当前目录或子树存在 active/archived memory 时拒绝删除；不得通过递归删除隐式遗忘 memory |
| **不允许** | 把文件夹移动到自己或自己的子孙下 |

**路径规范**：
- 始终绝对路径，以 `/` 开头
- 不带尾部 `/`（根除外）
- 段不能包含 `/`、`\0`、纯 `.` 或 `..`
- 大小写敏感（避免跨 OS 歧义）

---

## 6bis. 路径模型决策记录

> 这是 v0.3 锁定的核心决策，后续所有改动必须沿着这条线。

**决策**：文件夹是一等公民。
- ✅ 用 `folders` 表持久化（支持空文件夹）
- ✅ `files.path` 冗余存父目录绝对路径（高频查询不 join）
- ✅ `files.folder_id` 是真正的外键，重命名/移动靠改 folder 即可批量传导
- ❌ 不用纯派生方案（A），因为空文件夹存不下来不符合网盘用户预期
- ❌ 不用 `.mem_keep` 占位（C），hack 味重

**物理存储不变**：S3 key 仍是 `users/<user_id>/<file_id>/<name>`，与虚拟路径完全解耦。移动/重命名零 S3 IO。

---

## 7. CLI 规范（v0.1 最小集）

### 7.1 输出约定

- **默认**：人类可读，带颜色、表格
- **`--format json`**：机器可读
- **`-q / --quiet`**：只输出关键字段
- **`--stream`**：流式输出（支持该能力的检索命令）
- **退出码**：`0` ok · `2` not_found · `3` auth · `4` quota · `5` provider_error

### 7.2 命令清单（Phase 1）

```bash
# 认证
mem auth login
mem auth logout
mem auth status
mem auth token create --name <name> --scope <scopes> [--quota ...] [--expires ...]
mem auth token list
mem auth token revoke <token_id>

# 存
mem put <path>                            # 单文件
mem put <dir> --recursive                 # 目录
mem put - --name <name> [--mime <type>]   # stdin
mem put --url <url> [--name <name>]       # 远程
mem put <path> --tag <tag>...
mem put <path> --watch                    # 守护，新文件自动入

# 取
mem get <file_id> -o <path>
mem cat <file_id>                         # 输出文本内容到 stdout

# 列
mem ls [path]                             # 列虚拟路径
mem ls --tag <tag>
mem info <file_id>                        # 详情（含 AI 摘要）

# 搜
mem search <query> [--type ...] [--since ...] [--until ...] [--face <name>] [--limit N]
mem search <query> --format json --stream

# 关联
mem related <file_id> [--type ...] [--limit N]

# Agent 上下文
mem remember <content> --kind <kind> --path <path> \
  --idempotency-key <stable-key> [--event-at <rfc3339>] \
  [--source-type <type>] [--source-ref <ref>] [--source-file-id <uuid>] \
  [--agent-id <id>] [--session-id <id>] [--task-id <id>]
mem memory <memory-id> [--scope <path>] [--format json]
mem context <query> [--scope <path>] [--source all|file|memory] \
  [--memory-kind <kind>] [--limit N] [--max-chars N] [--format json]
mem checkpoint --input <handoff.json|-> --idempotency-key <stable-key>
mem tasks [--scope <path>] [--limit N] [--after <task-uuid>] [--format json]
mem checkpoints <task-key> [--scope <path>] [--limit N] \
  [--before <sequence>] [--format json]
mem checkpoint get <task-key> <checkpoint-id> [--scope <path>] [--format json]
mem resume <task-key> [--checkpoint-id <uuid>] [--scope <path>] \
  [--focus <text>] [--limit N] [--max-chars N] [--format json]

# 实体
mem face list
mem face name <cluster_id> <name>
mem face merge <id1> <id2>
mem timeline <year-or-range>

# Provider
mem provider list
mem provider set <type> <vendor>:<model>
mem provider test <type>
mem provider reindex                    # 显式重建 legacy/unknown 文本向量

# 本地 embedding 模型（list/recommend 不下载）
mem model list [--json]
mem model recommend [--language <code>] [--json]
mem model install [<profile-id>] [--activate] [--json]
mem model activate <profile-id> [--json]

# 系统
mem status                                # 索引状态、配额、配置
mem version
```

旧的顶层 `mem login`、`mem logout` 和 `mem token ...` 在迁移期保持隐藏兼容，
执行时输出 deprecated 提示；新脚本必须使用 `mem auth ...`。

CLI 的 `--idempotency-key` 由适配器转换为 HTTP `Idempotency-Key` Header，
不会作为记忆 body 或公共响应字段传播。

---

## 8. MCP 工具规范

独立 MCP 适配器启动方式：`MEM_TOKEN=... ./bin/mem-mcp`。它与 CLI 共用 API，
不维护第二套业务逻辑。

### 8.1 Tools 清单（Phase 1）

```yaml
- name: mem_put
  description: 上传内容到我的 AI 网盘并触发 AI 索引
  input_schema:
    type: object
    properties:
      content:  { type: [string, binary] }
      name:     { type: string }
      mime:     { type: string }
      tags:     { type: array, items: { type: string } }
    required: [content, name]

- name: mem_remember
  description: 幂等写入一条带来源和 producer 的结构化记忆
  input_schema:
    type: object
    properties:
      content:         { type: string }
      kind:            { type: string, enum: [observation, decision, preference, task_state, fact, note, artifact] }
      path:            { type: string }
      idempotency_key: { type: string }
      event_at:        { type: string, format: date-time }
      source_type:     { type: string, default: agent }
      source_ref:      { type: string }
      source_file_id:  { type: string }
      source_locator:  { type: object }
      agent_id:        { type: string }
      session_id:      { type: string }
      task_id:         { type: string }
      attributes:      { type: object }
    required: [content, kind, path, idempotency_key]
  # idempotency_key 由 MCP 适配器转换为 HTTP Idempotency-Key Header

- name: mem_memory_list
  description: 列出有界结构化记忆摘要；mem_list 保持文件列表语义
  input_schema:
    type: object
    properties:
      scope:     { type: string }
      recursive: { type: boolean, default: true }
      kind:
        type: array
        items: { type: string, enum: [observation, decision, preference, task_state, fact, note, artifact] }
      lifecycle: { type: string, enum: [active, archived, all], default: active }
      pinned:    { type: boolean }
      limit:     { type: integer, default: 50, maximum: 100 }
      cursor:    { type: string }

- name: mem_memory_get
  description: 按 UUID 读取一条结构化记忆的完整内容和来源
  input_schema:
    type: object
    properties:
      memory_id: { type: string, format: uuid }
      scope:     { type: string }
    required: [memory_id]

- name: mem_feedback
  description: 幂等记录 useful/not_useful 或 pin/unpin，并做 state_version CAS
  input_schema:
    type: object
    properties:
      memory_id:       { type: string, format: uuid }
      action:          { type: string, enum: [useful, not_useful, pin, unpin] }
      expected_version: { type: integer, minimum: 1 }
      idempotency_key: { type: string }
    required: [memory_id, action, expected_version, idempotency_key]

- name: mem_archive
  description: 可逆地把记忆移出默认召回
  # required: memory_id, expected_version, idempotency_key

- name: mem_restore
  description: 把 archived 记忆恢复到默认召回
  # required: memory_id, expected_version, idempotency_key

- name: mem_forget
  description: 不删除独立原件，确认后不可逆地清除 live memory payload
  input_schema:
    type: object
    properties:
      memory_id:        { type: string, format: uuid }
      expected_version: { type: integer, minimum: 1 }
      reason:           { type: string, enum: [user_request, incorrect, sensitive, expired, other] }
      idempotency_key:  { type: string }
      confirm:          { type: boolean, const: true }
    required: [memory_id, expected_version, reason, idempotency_key, confirm]

- name: mem_search
  description: 自然语言搜索网盘内容（图片/文档/任意类型）
  input_schema:
    type: object
    properties:
      query: { type: string }
      type:  { type: string, enum: [image, doc, audio, any] }
      since: { type: string, format: date }
      until: { type: string, format: date }
      face:  { type: string }
      limit: { type: integer, default: 10 }
    required: [query]
  output:
    results: [{ id, name, snippet, score, preview_url, timeline_at }]

- name: mem_checkpoint
  description: 幂等写入一个不可变、可跨 Agent 恢复的 mem.handoff 版本
  input_schema:
    type: object
    properties:
      task_key:        { type: string }
      idempotency_key: { type: string }
      handoff:
        description: "完整结构见 docs/schemas/handoff.v1.schema.json"
        type: object
    required: [task_key, idempotency_key, handoff]

- name: mem_resume
  description: 恢复 task head 或指定 checkpoint，并报告已解析/缺失证据
  input_schema:
    type: object
    properties:
      task_key:      { type: string }
      checkpoint_id: { type: string, format: uuid }
      scope:         { type: string }
      focus:         { type: string }
      limit:         { type: integer, default: 8 }
      max_chars:     { type: integer, default: 12000 }
    required: [task_key]

- name: mem_task_list
  description: 列出当前 workspace/path 可见的可恢复任务
  input_schema:
    type: object
    properties:
      scope: { type: string }
      limit: { type: integer, default: 50, maximum: 200 }
      after: { type: string, format: uuid }

- name: mem_checkpoint_list
  description: 按 task_key 列出有界的不可变 checkpoint 摘要；完整 handoff 仅由 get 返回
  input_schema:
    type: object
    properties:
      task_key: { type: string }
      scope:    { type: string }
      limit:    { type: integer, default: 50, maximum: 200 }
      before:   { type: integer, minimum: 1 }
    required: [task_key]
  output:
    checkpoints:
      - {
          id,
          workspace_id,
          task_id,
          task_key,
          sequence,
          checkpoint_kind,
          contract,
          schema_version,
          base_checkpoint_id,
          scope_path,
          status,
          progress_excerpt,
          progress_length,
          completed_count,
          reference_count,
          payload_sha256,
          producer_agent,
          producer_session,
          created_at
        }

- name: mem_checkpoint_get
  description: 按 task_key 和 UUID 读取一个不可变 checkpoint
  input_schema:
    type: object
    properties:
      task_key:      { type: string }
      checkpoint_id: { type: string, format: uuid }
      scope:         { type: string }
    required: [task_key, checkpoint_id]

- name: mem_get
  description: 读取文件文本内容（自动转写音频/OCR 图片）
  input_schema:
    type: object
    properties:
      file_id: { type: string }
    required: [file_id]

- name: mem_related
  description: 找到与某文件关联的其他文件
  input_schema:
    type: object
    properties:
      file_id:  { type: string }
      relation: { type: string, enum: ["同事件","同人","同主题","续作"] }
      limit:    { type: integer, default: 10 }
    required: [file_id]

- name: mem_context
  description: 为调用方 Agent 组装带出处且有大小预算的上下文证据
  input_schema:
    type: object
    properties:
      query:     { type: string }
      scope:     { type: string, description: "限定虚拟路径" }
      source:    { type: string, enum: [all, file, memory], default: all }
      type:      { type: string, description: "文件 MIME 前缀过滤" }
      memory_kind: { type: string, enum: [observation, decision, preference, task_state, fact, note, artifact] }
      since:     { type: string, format: date }
      until:     { type: string, format: date }
      limit:     { type: integer, default: 8 }
      max_chars: { type: integer, default: 12000 }
    required: [query]
  output:
    source: string
    evidence: [{ evidence_id, source_kind, source_id, citation, file_id?, memory_id?, memory_kind?, content_sha256, locator, excerpt, score, reason, provenance? }]
    total_chars: integer
    partial: boolean
    warnings: [{ source, code, message }]
    retrieved_at: string
```

### 8.2 Agent 友好约定

- 错误返回 `{ error, hint }`，让 Agent 自纠正
- 列表型端点分页优先使用 `next_cursor`，不用 offset（逐端点落地）
- `_meta: { quota_remaining, latency_ms }` 与大结果 streaming 是后续能力，
  当前 `remember/context` 不伪造这些字段

### 8.3 Agent host 认证约定

当前 host transport 只认证 newline-delimited JSON-RPC 2.0 stdio 和 MCP
`2024-11-05` baseline。OpenClaw、Hermes Agent、Claude Code、OpenCode 与
Codex 必须复用同一个 `mem-mcp → memd` 适配链路；host 配置不能引入第二套
memory 语义。

认证分两层：无模型的 hermetic contract 必须完成
`initialize → notifications/initialized → tools/list → tools/call`，并覆盖
auth、remember/search/context、source identity、lifecycle/forget、active
cross-workspace denial 和显式失败；真实 host 则按直接证据分别报告
`REGISTERED`、`DISCOVERED`、`INVOKED` 或 `NOT RUN`。配置解析不等于发现，
发现不等于调用；只有 `INVOKED` 计完整 PASS。版本化 manifest、脱敏配置和
当前证据见 [`docs/integrations/agent-hosts.md`](docs/integrations/agent-hosts.md)。

token 只能经 host 文档化的 environment/secret mechanism 注入，不能进入
tracked config、argv、报告或日志。真实 host runner 不修改用户全局配置；
没有可证明的 host-specific isolation 时必须 `NOT RUN`。

---

## 9. AI Pipeline 规范

### 9.1 Processor 接口

```python
class Processor(Protocol):
    name: str
    accepts: list[str]                    # mime patterns

    def process(self, file: FileRef) -> ProcessResult:
        """
        ProcessResult = {
          summary: str | None,
          caption: str | None,
          tags: list[str],
          entities: list[Entity],          # 人/地/时/物
          embeddings: dict,                # text/visual/face
          metadata: dict,                  # EXIF / 编码信息
        }
        """
```

### 9.2 Phase 1 Processors

| Processor | accepts | 输出 |
|-----------|---------|------|
| ImageProcessor | `image/*` | CLIP visual emb + VLM caption + face emb + EXIF |
| TextProcessor | `text/*`, `application/json`, code mimes | 分块 text emb；摘要是可选索引增强，默认关闭 |
| PDFProcessor | `application/pdf` | 抽文本（含 OCR fallback）+ Text 流程 |
| AudioProcessor | `audio/*` | Whisper ASR → 转 Text 流程 |

### 9.3 Provider 接口

```python
class EmbeddingProvider(Protocol):
    def embed_text(self, texts: list[str]) -> list[Vector]: ...
    def embed_image(self, images: list[Image]) -> list[Vector]: ...

class VLMProvider(Protocol):
    def caption(self, image: Image) -> str: ...
    def vqa(self, image: Image, q: str) -> str: ...
```

Ollama text embedding adapter 使用一次批量 `POST /api/embed`，body 包含输入数组、
`dimensions: 768` 和 `truncate: false`。响应向量数必须与输入数一致，且每条向量
必须恰好 768 维；adapter 不在本地截断或填充向量来伪造兼容。

### 9.4 默认推荐栈（本地优先）

下表全部是索引链路模型；不包含也不代表 Agent 的回答模型。

| Provider | 默认 | 备选 |
|----------|------|------|
| Embedding (text) | 当前兼容默认 `ollama:nomic-embed-text`；新安装应从版本化本地目录显式选择 | `openai:text-embedding-3-small` |
| Embedding (visual) | 内置 OpenCLIP（`ViT-B-32:openai`，英文基线） | 通过固定中英文评测的 512 维多语言 OpenCLIP checkpoint（尚未切换） |
| VLM | `ollama:minicpm-v` | `openai:gpt-4o-mini` / `anthropic:claude-haiku-4-5-20251001` |
| ASR | 内置 `faster-whisper` | — |
| Face | 内置 `insightface` | — |

---

## 10. Phase 1 验收标准（4 周 MVP）

### 10.1 范围与周节奏

| 周 | 交付物 | 验收 |
|----|--------|------|
| W1 | Go 后端骨架 · PostgreSQL schema · `mem put` / `mem get` / `mem cat` · Token 鉴权 | `curl` 上传 + CLI 取回；2 个用户隔离 |
| W2 | Python AI Worker · ImageProcessor + TextProcessor · pgvector 入库 | 100 张照片入库后 `mem info` 能看到 caption + 标签 |
| W3 | `mem search`（含 visual + text 多路） · `mem face` · `mem related` 基础版 | 英文固定集以文搜图通过；中文固定集通过后才宣称“草地金毛”已达标；人脸聚类正确 |
| W4 | MCP Server · `mem context` · 极简 Web UI · Docker Compose · README | **杀手 demo：Claude Desktop 通过 MCP 搜出 2012 年的照片** |

### 10.2 必须跑通的端到端 Demo

1. `docker compose up` 一键起服务
2. 用 `mem put ~/Photos --recursive` 灌入 1000 张照片
3. 等待 AI Pipeline 完成（< 30 分钟）
4. 命令行英文固定查询命中金毛；中文 `mem search "草地上的金毛"` 必须通过
   `worker/tests/test_multilingual_visual_acceptance.py` 后才计为命中
5. 命令行 `mem face name <id> "小明"`，然后 `mem search "和小明的合照"` 命中
6. 在 Claude Desktop 配置 mem MCP → 直接说"找我和小明的合照" → Claude 调用 mem_search 返回
7. `mem context "我有多少张 2012 年的照片"` → Agent 根据证据返回数字 + 抽样列表

### 10.3 必须砍掉的（写明，避免范围爆炸）

- ❌ 桌面同步盘
- ❌ 移动端
- ❌ 分享链接
- ❌ 团队 / 多人协作
- ❌ 视频 Processor（只做关键帧太复杂，Phase 2）
- ❌ 在线预览编辑
- ❌ 主动洞察 / 周报
- ❌ 插件市场

### 10.4 Migration MVP 验收

Phase 1 的文件、检索、MCP 和网盘 UI 是迁移能力的底座；其后的第一个产品闭环必须
通过以下验收：

1. Claude Code 写入一个带稳定 task id 的 handoff。
2. Codex 在没有原会话记录的前提下，从同一 workspace 恢复任务并正确给出下一步。
3. 导出 workspace，在空白环境导入后，文件哈希、记忆条数、handoff 与引用关系一致。
4. Web UI 能展示本次交接和恢复记录，用户可以追溯、导出或删除。
5. 自动化测试验证幂等重试、缺失对象、版本不兼容和路径权限限制。

---

## 11. 仓库结构

```
mem/
├── README.md
├── GOAL.md                              ← 项目北极星与迁移验收
├── SPEC.md                              ← 本文档
├── LICENSE                              ← Apache-2.0
├── docker-compose.yml                   ← 一键起
├── Makefile
├── docs/
│   ├── architecture.md
│   ├── cli.md
│   ├── mcp.md
│   └── provider.md
├── server/                              ← Go 主服务
│   ├── cmd/
│   │   ├── memd/                        ← 服务端 daemon
│   │   ├── mem/                         ← CLI
│   │   └── mem-mcp/                     ← MCP server
│   ├── internal/
│   │   ├── api/
│   │   ├── auth/
│   │   ├── file/
│   │   ├── memory/
│   │   ├── search/
│   │   ├── related/
│   │   ├── contextpack/
│   │   ├── face/
│   │   ├── storage/                     ← S3 adapter
│   │   ├── db/                          ← pgvector
│   │   ├── queue/                       ← Asynq
│   │   └── workerpb/                    ← gRPC to Python
│   └── go.mod
├── worker/                              ← Python AI Worker
│   ├── pyproject.toml
│   ├── mem_worker/
│   │   ├── server.py                    ← gRPC server
│   │   ├── processors/
│   │   │   ├── image.py
│   │   │   ├── text.py
│   │   │   ├── pdf.py
│   │   │   └── audio.py
│   │   ├── providers/
│   │   │   ├── ollama.py
│   │   │   ├── openai.py
│   │   │   └── anthropic.py
│   │   └── pipeline.py
│   └── proto/
├── web/                                 ← React AI 网盘；memory 信任控制面待补
└── scripts/
    └── seed_demo_data.sh
```

---

## 12. 开源策略

- **License**：Apache-2.0
- **核心承诺**：应用层永远完整可自托管，不做"开源阉割版"
- **商业化路径**（不强求，留钩子）：
  1. 托管版 SaaS
  2. 企业插件：SSO / 审计 / 合规导出
  3. Provider 市场：付费高质量 adapter / 微调模型

---

## 13. 风险与对策

| 风险 | 概率 | 影响 | 对策 |
|------|------|------|------|
| 范围爆炸 | 高 | 高 | 严守 Phase 1 砍掉清单 |
| AI 索引成本 | 中 | 高 | 默认本地模型，云模型 opt-in |
| 同步协议复杂 | — | — | Phase 1 不做同步，Phase 2 复用 rclone lib |
| 人脸隐私合规 | 中 | 中 | 人脸功能可一键关闭；不上报特征 |
| 冷启动慢 | 中 | 中 | 进度可视化 + 增量入库 + 优先级队列 |

---

## 14. 关键决策（已闭环 2026-05-11）

| # | 决策项 | 最终选择 | 含义 |
|---|--------|---------|------|
| D1 | 项目名 | **`mem`** | CLI 命令、Go module、域名候选 `mem.dev` / `getmem.io` |
| D2 | 语言栈 | **Go 主服务 + Python AI Worker（gRPC）** | server/ 用 Go，worker/ 用 Python |
| D3 | 数据库 | **PostgreSQL + pgvector** | 元数据 + 向量一个库；亿级再迁 |
| D4 | License | **Apache-2.0** | 最宽松，社区友好优先 |
| D5 | Phase 1 Web UI | **AI 网盘信任控制面（上传 / 搜索 / 详情优先）** | 不提供内置聊天；逐步补召回解释、纠错、权限和遗忘 |
| D6 | 首发 Demo 场景 | **"找到 2012 年和小明在云南的照片"** | 多路融合炫技 + 情感共鸣 |

---

## 15. 下一步

1. ✅ 已实现版本化 `handoff / checkpoint / resume` 契约、CAS/幂等持久化以及
   API / CLI / MCP；以双 Token 路由验收锁定 Claude Code → Codex 接续。
2. ✅ 已实现 workspace bundle v1、API / CLI / Web export、空目标 `fresh`
   import、完整性校验、幂等 ledger、结构化冲突和失败补偿；继续实现
   `merge_conservative`、增量包与断点上传。
3. ✅ 已实现 `remember / context / feedback / archive / restore / forget`
   的模型无关控制闭环；继续补不可变 correction/supersede、巩固与评测驱动排序。
4. ✅ Web UI 已能展示 Drive/Search、结构化记忆账本、任务/checkpoint/Resume 和
   Workspace Transfer；继续补 correction/supersede、导入历史与完整权限管理。
5. 持续增强图片 caption、视觉向量、实体/关系、精确 source locator 与召回评测。

---

## 附录 A：术语表

| 术语 | 含义 |
|------|------|
| File | 用户上传的任意类型文件 |
| Processor | 把某类文件转成结构化 + 向量的处理器 |
| Provider | 索引模型供应商（Embedding/VLM/ASR/OCR） |
| Entity | 抽取出的实体（人/地/时/物） |
| Relation | 文件之间的关系（同事件/同人/同主题/续作） |
| MCP | Model Context Protocol，Agent 调用工具的开放协议 |
| Token Scope | 权限粒度（search/read/write/delete/admin） |
| Index Status | 文件 AI 处理状态（pending/processing/done/failed） |
