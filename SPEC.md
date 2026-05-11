# mem — Agent-Native AI 网盘

> Spec-Driven Development · v0.1 · 2026-05-11
>
> 项目名 `mem` 为工作名，最终发布前可改。
> License: Apache-2.0（应用层全开源）。

---

## 0. TL;DR

**mem** 是一个开源、自托管、Agent-Native 的 AI 网盘。
形态像百度网盘，灵魂像第二大脑。

- **存**：用户和 Agent 一股脑往里扔，零摩擦
- **搜图**：自然语言搜出所有相关图片（杀手场景）
- **关联**：打开一个文件，自动带出全家桶
- **Agent-Native**：CLI / MCP / API 三位一体，Agent 是一等公民
- **模型可插拔**：Embedding / VLM / LLM 用户自带（Ollama / OpenAI / Anthropic / …）

---

## 1. 产品定位

### 1.1 一句话

> 让用户一股脑往里扔，AI 替他找回来；让 Agent 用 CLI / MCP 直接读写他的全部记忆。

### 1.2 与现有玩家的差异化

| 玩家 | 缺什么 |
|------|--------|
| Google Photos / iCloud | 闭源、无 CLI、不能被 Agent 使用 |
| Nextcloud / Seafile | 无 AI 原生数据模型 |
| LangChain / LlamaIndex 向量库 | 是库不是产品，无 UI、无文件管理 |
| Mem.ai / Reflect | 闭源、文件类型窄、不开放 API |
| MCP filesystem server | 只是本地 FS，无 AI 理解 |

**mem 的位置**：开源 + Agent-Native + AI 原生 + 自托管 的 personal data layer。

### 1.3 三板斧（任何功能都必须服务于其中之一）

1. **存得爽** — 上传零摩擦
2. **搜得准** — 自然语言搜图为杀手场景
3. **关联得出** — 找到一个 → 带出一窝

---

## 2. 目标用户与核心场景

### 2.1 Persona

- **P-Human-A**：自托管发烧友 / 隐私敏感用户 / 重度信息整理者
- **P-Human-B**：垂直专业用户（设计师 / 律师 / 科研，Phase 3 关注）
- **P-Agent**：Claude Desktop / Cursor / Cline / 用户自写 Agent
  - 通过 MCP 或 CLI 调用 mem
  - 是高频用户，远超人类频次

### 2.2 杀手用户故事（Phase 1 必须跑通）

**US-1 自然语言搜图**
> "我想找到 2012 年高中和小明在云南拍的照片"
→ mem 一秒命中。

**US-2 Agent 通过 MCP 读取知识**
> 用户对 Claude Desktop 说："总结我上个月的合同"
→ Claude 调用 `mem_search(query="合同", since="last month")` + `mem_ask(...)`
→ 直接给出总结，**无需用户手动喂文件**。

**US-3 关联召回**
> 打开一份租房合同 → 自动展示：转账凭证、房东聊天、上一份合同、看房照片、续费提醒。

### 2.3 非目标（Phase 1 明确不做）

- ❌ 团队协作 / 权限分级
- ❌ 移动端 App
- ❌ 桌面同步盘（Phase 2）
- ❌ 分享链接（Phase 2）
- ❌ 在线预览编辑（OnlyOffice 类）
- ❌ 视频在线播放转码

---

## 3. 功能需求

### F1 · 存储与上传

| ID | 需求 |
|----|------|
| F1.1 | 支持单文件、目录、stdin 流、远程 URL 抓取四种入库方式 |
| F1.2 | 内容寻址：相同 SHA256 自动秒传去重 |
| F1.3 | 分块上传，支持断点续传（>10MB 强制分块） |
| F1.4 | 入库返回 `file_id`，是后续所有操作的主键 |
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
| F3.2 | Query Planner：LLM 拆解 query → 实体 + 语义 |
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

### F5 · Ask（跨文件问答）

| ID | 需求 |
|----|------|
| F5.1 | `mem ask "..."` → 综合多文件给出回答 |
| F5.2 | 必须返回 sources：每个引用文件的 id + 片段 |
| F5.3 | 走 RAG 链路：search → 取原文片段 → LLM 综合 |

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
| F7.4 | 配额：calls/day、storage、AI tokens/day |
| F7.5 | 429 错误必须返回 retry-after |

### F8 · Provider 可插拔

| ID | 需求 |
|----|------|
| F8.1 | Provider 类型：Embedding / VLM / LLM / ASR / OCR |
| F8.2 | 每类 Provider 一个接口，社区可贡献 adapter |
| F8.3 | Phase 1 内置：Ollama、OpenAI、Anthropic |
| F8.4 | 用户 API Key 仅本地存储，不上报任何服务 |
| F8.5 | 配置：`mem provider set <type> <vendor>:<model>` |

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
│   File · Search · Related · Ask · Face · Provider              │
├────────────────────────────────────────────────────────────────┤
│  AI Worker（Python，gRPC，可水平扩展）                          │
│   Processor: Image / Text / PDF / Audio / ...                  │
│   Provider Adapter: Ollama / OpenAI / Anthropic / ...          │
├────────────────────────────────────────────────────────────────┤
│  Data 层                                                        │
│   PostgreSQL + pgvector  ·  Redis (queue/cache)                │
│   S3 协议存储（MinIO / OSS / R2 / 本地 FS）                    │
├────────────────────────────────────────────────────────────────┤
│  Web UI（Phase 1 末，CLI 的可视化壳）                          │
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

-- Token
tokens (id, user_id, name, hash, scopes[], quota_jsonb,
        paths[], expires_at, redact_pii, created_at)

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
  summary         text,                    -- LLM 摘要
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
  embedding       vector(768)
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
- `files (user_id, path text_pattern_ops)` — 子树查询 `path LIKE '/Photos/2012%'`
- `folders (user_id, parent_id)` — 列子文件夹
- `folders (user_id, path)` UNIQUE — 路径唯一性约束
- `embeddings_* (embedding)` — pgvector HNSW
- `file_entities (entity_id)` — 反查"和某人有关的所有文件"

### 6.3 文件夹一致性规则（重要）

| 操作 | 必须保证 |
|------|---------|
| **创建文件夹** | 自动创建所有缺失的父级（mkdir -p 语义） |
| **上传文件到 /a/b/c.jpg** | 确保 /a 和 /a/b 文件夹存在（自动 mkdir -p） |
| **重命名文件夹 /a → /A** | 批量更新所有子文件夹和文件的 path 前缀，一个事务内完成 |
| **移动文件夹 /a → /b/a** | 同上，前缀替换 + 父级 id 改 |
| **删除文件夹** | 软策略：必须先空才能删；硬策略：递归删除子文件夹和文件（含 S3 异步清理）。默认软策略 |
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
- **`--stream`**：流式输出（搜索 / ask）
- **退出码**：`0` ok · `2` not_found · `3` auth · `4` quota · `5` provider_error

### 7.2 命令清单（Phase 1）

```bash
# 认证
mem login
mem logout
mem token create --name <name> --scope <scopes> [--quota ...] [--expires ...]
mem token list
mem token revoke <token_id>

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

# Ask
mem ask <question> [--scope <path>] [--format json]

# 实体
mem face list
mem face name <cluster_id> <name>
mem face merge <id1> <id2>
mem timeline <year-or-range>

# Provider
mem provider list
mem provider set <type> <vendor>:<model>
mem provider test <type>

# 系统
mem status                                # 索引状态、配额、配置
mem version
```

---

## 8. MCP 工具规范

mem 内置 MCP Server，启动方式：`mem mcp serve [--token <t>]`。

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

- name: mem_ask
  description: 跨文件问答，AI 综合多文件回答问题
  input_schema:
    type: object
    properties:
      question: { type: string }
      scope:    { type: string, description: "限定路径或 tag" }
    required: [question]
  output:
    answer: string
    sources: [{ file_id, name, excerpt }]
```

### 8.2 Agent 友好约定

- 错误返回 `{ error, hint }`，让 Agent 自纠正
- 分页用 `next_cursor`，不用 offset
- 响应带 `_meta: { quota_remaining, latency_ms }`
- 大结果支持 streaming

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
| TextProcessor | `text/*`, `application/json`, code mimes | 分块 text emb + 摘要 |
| PDFProcessor | `application/pdf` | 抽文本（含 OCR fallback）+ Text 流程 |
| AudioProcessor | `audio/*` | Whisper ASR → 转 Text 流程 |

### 9.3 Provider 接口

```python
class EmbeddingProvider(Protocol):
    def embed_text(self, texts: list[str]) -> list[Vector]: ...
    def embed_image(self, images: list[Image]) -> list[Vector]: ...

class LLMProvider(Protocol):
    def complete(self, messages, **kwargs) -> str: ...
    def stream(self, messages, **kwargs) -> Iterator[str]: ...

class VLMProvider(Protocol):
    def caption(self, image: Image) -> str: ...
    def vqa(self, image: Image, q: str) -> str: ...
```

### 9.4 默认推荐栈（本地优先）

| Provider | 默认 | 备选 |
|----------|------|------|
| Embedding (text) | `ollama:nomic-embed-text` | `openai:text-embedding-3-small` |
| Embedding (visual) | 内置 CLIP（ViT-B/32） | `openai` CLIP API |
| VLM | `ollama:minicpm-v` | `openai:gpt-4o-mini` / `anthropic:claude-haiku-4-5-20251001` |
| LLM | `ollama:qwen2.5:7b` | `anthropic:claude-opus-4-7` |
| ASR | 内置 `faster-whisper` | — |
| Face | 内置 `insightface` | — |

---

## 10. Phase 1 验收标准（4 周 MVP）

### 10.1 范围与周节奏

| 周 | 交付物 | 验收 |
|----|--------|------|
| W1 | Go 后端骨架 · PostgreSQL schema · `mem put` / `mem get` / `mem cat` · Token 鉴权 | `curl` 上传 + CLI 取回；2 个用户隔离 |
| W2 | Python AI Worker · ImageProcessor + TextProcessor · pgvector 入库 | 100 张照片入库后 `mem info` 能看到 caption + 标签 |
| W3 | `mem search`（含 visual + text 多路） · `mem face` · `mem related` 基础版 | 命令行搜出"草地金毛"图；人脸聚类正确 |
| W4 | MCP Server · `mem ask` · 极简 Web UI · Docker Compose · README | **杀手 demo：Claude Desktop 通过 MCP 搜出 2012 年的照片** |

### 10.2 必须跑通的端到端 Demo

1. `docker compose up` 一键起服务
2. 用 `mem put ~/Photos --recursive` 灌入 1000 张照片
3. 等待 AI Pipeline 完成（< 30 分钟）
4. 命令行 `mem search "草地上的金毛"` 命中
5. 命令行 `mem face name <id> "小明"`，然后 `mem search "和小明的合照"` 命中
6. 在 Claude Desktop 配置 mem MCP → 直接说"找我和小明的合照" → Claude 调用 mem_search 返回
7. `mem ask "我有多少张 2012 年的照片"` → 返回数字 + 抽样列表

### 10.3 必须砍掉的（写明，避免范围爆炸）

- ❌ 桌面同步盘
- ❌ 移动端
- ❌ 分享链接
- ❌ 团队 / 多人协作
- ❌ 视频 Processor（只做关键帧太复杂，Phase 2）
- ❌ 在线预览编辑
- ❌ 主动洞察 / 周报
- ❌ 插件市场

---

## 11. 仓库结构（待落地）

```
mem/
├── README.md
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
│   │   ├── search/
│   │   ├── related/
│   │   ├── ask/
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
├── web/                                 ← React UI（Phase 1 末再做）
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
| D5 | Phase 1 Web UI | **极简版（上传 / 搜索 / 详情 三页）** | W4 交付，含截图录屏 |
| D6 | 首发 Demo 场景 | **"找到 2012 年和小明在云南的照片"** | 多路融合炫技 + 情感共鸣 |

---

## 15. 下一步

1. 用户拍板 §14 的开放问题
2. 我下一轮交付：
   - 仓库初始化（`go mod` / `pyproject.toml` / `docker-compose.yml`）
   - W1 第一周的 task list（颗粒度细到可 commit）
   - README draft（开源传播文案）
   - PostgreSQL 初始 migration

---

## 附录 A：术语表

| 术语 | 含义 |
|------|------|
| File | 用户上传的任意类型文件 |
| Processor | 把某类文件转成结构化 + 向量的处理器 |
| Provider | 模型供应商（Embedding/LLM/VLM/ASR） |
| Entity | 抽取出的实体（人/地/时/物） |
| Relation | 文件之间的关系（同事件/同人/同主题/续作） |
| MCP | Model Context Protocol，Agent 调用工具的开放协议 |
| Token Scope | 权限粒度（search/read/write/delete/admin） |
| Index Status | 文件 AI 处理状态（pending/processing/done/failed） |
