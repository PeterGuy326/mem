# 本地运行 mem 全栈（裸机 · 无 Docker）

本文用于启动完整开发栈和手工 smoke。可重复的单元、Race、PostgreSQL 集成、
Web 浏览器验收及其通过标准统一见 [TESTING.md](TESTING.md)。

在没有 Docker 的 macOS 开发环境中，整套栈可用**本地进程**拉起，不走
`docker compose`。
一条命令起、一条命令停，运行时数据全部落在 `.dev/`（已 gitignore）。

```
┌──────────┐   gRPC    ┌──────────┐   HTTP    ┌──────────┐
│  worker  │◀──────────│   memd   │◀──────────│  web/CLI │
│ :50051   │           │  :8787   │           │          │
└────┬─────┘           └────┬─────┘           └──────────┘
     │ Ollama :11434        │ pgvector :5432
     │ (nomic + VLM 可选)   │ MinIO    :9100
     ▼                      ▼
  本地 Ollama          PostgreSQL + MinIO（本地进程）
```

## 一次性准备（首次或换机器时）

1. **依赖二进制**（脚本假设它们已就位）：
   - PostgreSQL + pgvector（brew，keg-only，无需 sudo）：
     ```bash
     brew install postgresql@17 pgvector
     ```
     > 用 `@17` 而不是 `@16`：brew 的 pgvector bottle 只为 postgresql@17/@18
     > 编译了 `vector.so`，装在 @16 上 `CREATE EXTENSION vector` 会失败。
     > `dev_up.sh` 会自动探测 @17/@18/@16 中带匹配 pgvector 的版本。
   - MinIO server + mc client。推荐用 brew（dl.min.io 在本网络偶发限流/TLS 断连，
     brew 走 ghcr.io 更稳）：
     ```bash
     brew install minio minio-mc
     ```
     `dev_up.sh` 会自动在 `.dev/bin/`、brew、PATH 中找 `minio`/`mc`。
     也可手动下到 `.dev/bin/`（若 dl.min.io 可达）：
     ```bash
     mkdir -p .dev/bin
     curl -fL -o .dev/bin/minio https://dl.min.io/server/minio/release/linux-amd64/minio
     curl -fL -o .dev/bin/mc     https://dl.min.io/client/mc/release/linux-amd64/mc
     chmod +x .dev/bin/minio .dev/bin/mc
     ```
     > `mc` 是可选的：memd 启动时会自己 `MakeBucket` 建桶，没有 mc 也能跑通。
   - worker Python 依赖（`--extra clip` 为**可选**的图搜图能力安装 CLIP）：
     ```bash
     cd worker && uv sync --extra clip && cd ..
     ```
     > `dev_up.sh` 启动 worker 前也会自动跑一次 `uv sync --extra clip`，所以
     > 平时直接 `bash scripts/dev_up.sh` 即可，无需手动 sync。
     > CLIP 走 CPU，ViT-B-32 权重约 600MB。它必须在要启用图搜图的部署中单独
     > 预置/缓存；`profile select local-fast-v1` 不会安装、下载或 probe CLIP，
     > 它也不是本地文本 profile 激活的前置条件。
   - memd 二进制（`dev_up.sh` 会在缺失时自动 `go build`，也可手动）：
     ```bash
     make build-memd build-mem   # -> bin/memd, bin/mem
     ```

2. **Ollama（`local-fast-v1` 的明确前置条件）**：要验证文件语义检索时，需在
   `http://localhost:11434` 运行。工作区的本地快速档固定使用
   `ollama:qwen3-embedding:0.6b`，并要求 Worker 取得**恰好 768 维**文本向量；
   `mem profile select` 不会下载模型，也不会把缺失模型换成其他默认模型。先由操作者
   明确安装并做本机兼容性、manifest digest 与 768 维校验：
   ```bash
   bin/mem model install qwen3-embedding-0.6b-ollama
   ```
   随后的工作区 profile 选择会经由 memd 对 Worker 做一次权威的 768 维 probe；probe
   失败时选择不会写入。不要静默 pull 默认模型。若要使用旧的本地模型目录来评估或
   安装其他**兼容/高级** embedding artifact，可显式执行：
   ```bash
   bin/mem model list
   bin/mem model recommend --language zh
   bin/mem model install <profile-id>
   ```
   `install` 只在用户显式选择后通过 Ollama HTTP API 下载，随后校验固定 manifest
   digest 和 768 维 `/api/embed` probe；默认不会激活。完整目录和 integrity 更新
   规则见 [LOCAL_EMBEDDING_MODELS.md](LOCAL_EMBEDDING_MODELS.md)。
   目录中的文本 embedding profile（包括适用时的 `nomic-embed-text`）必须通过
   768 维 probe，对上 schema `embeddings_text vector(768)`，不要绕过目录流程
   直接把未验证模型设为默认值。
   - `local-fast-v1` 有意关闭 LLM、VLM、ASR 和 rerank；它不会继承
     `MEM_DEFAULT_LLM`、`MEM_DEFAULT_VLM` 或任何全局云模型。这样本地档只走固定的
     本地文本 embedding 与 CLIP 视觉 embedding；没有启用的富化阶段不会被某个默认
     模型悄悄补上。旧的、未选择 workspace profile 的兼容路径仍可另行显式安装
     `ollama pull qwen2.5:7b` 等模型。
   - 视觉模型（minicpm-v）**可选缺失**：影响的是 caption 文本，不影响图搜图本身。
     图片的视觉向量由 **CLIP**（`clip:ViT-B-32`，见下）产出，与 Ollama 无关。
   - **图搜图 = CLIP（可选，独立于 profile 激活）**：装了 `--extra clip` 且已预置
     CLIP 权重后，图片入库时由 CLIP image-tower
     编码成 512 维视觉向量写进 `embeddings_visual`，搜索时 query 文本由 CLIP
     text-tower 编码到同一空间做 ANN——这才是"以文搜图"。若 CLIP 没装，图片
     仍会保留 caption / EXIF 等元数据，但不会伪造不兼容的视觉向量；
     `embeddings_visual` 为空时，图搜图不会返回该图片。
   - **中文质量边界**：当前默认 `clip:ViT-B-32:openai` 是已经跑通的英文基线，
     不是已达标的中文模型。“草地上的金毛”在固定三图集上尚未稳定命中金毛；
     不要把中文命令能执行误写成中文召回已验收。候选多语言模型必须先通过
     `worker/tests/test_multilingual_visual_acceptance.py`，并配套全量重建带版本的
     visual index，才能切换生产默认值。当前证据见
     [acceptance/VISUAL_SEARCH_BASELINE.md](acceptance/VISUAL_SEARCH_BASELINE.md)。

   结构化记忆的 `remember → context --source memory` 只依赖 PostgreSQL 和
   memd，不调用 Worker、embedding、VLM 或回答模型，可在这些模型全部离线时验证。

## 用户回来怎么用（日常）

```bash
# 1. 一键拉起全部（幂等：已起的服务会跳过）
bash scripts/dev_up.sh

# 2. 灌种子数据 + 跑 3/3 语义搜索断言（证明 vector 搜索真通）
bash scripts/seed_demo_data.sh

# 3. 用 CLI 或 curl 真实使用
export MEM_TOKEN=$(curl -s -X POST http://localhost:8787/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@mem.local","password":"demo-password-change-me"}' | jq -r .token)

# 工作区 profile 只允许选择服务端编译并启用的 ID；不会接收 URL、模型名或 key。
# 本地部署默认只暴露 local-fast-v1。
bin/mem --server http://localhost:8787 profile list
bin/mem --server http://localhost:8787 profile status

# 这会对 ollama:qwen3-embedding:0.6b 做 Worker 端 768 维 probe；
# 不会下载 artifact，也不会回退到 MEM_DEFAULT_EMBEDDING。
bin/mem --server http://localhost:8787 profile select local-fast-v1

# 文本搜索（文档语义检索）
bin/mem --server http://localhost:8787 search "a dog on a meadow" --format json

# Agent 写入结构化记忆（无需回答模型，提交后立即可召回）
bin/mem --server http://localhost:8787 remember \
  "合同复核优先检查自动续费条款" \
  --kind decision --path /Contracts \
  --idempotency-key local-contract-review-v1 --agent-id codex

# 只召回结构化记忆；也可用 --source all 联合文件与记忆
bin/mem --server http://localhost:8787 context \
  "合同复核优先检查什么" \
  --source memory --memory-kind decision --scope /Contracts --format json

# Claude Code / Codex 共用的版本化任务交接（示例不依赖 Worker）
cat <<'JSON' | bin/mem --server http://localhost:8787 checkpoint \
  --input - --idempotency-key contract-review-handoff-v1
{
  "contract": "mem.handoff",
  "schema_version": 1,
  "checkpoint_kind": "handoff",
  "task_key": "contract-review/v1",
  "base_checkpoint_id": null,
  "scope_path": "/Contracts",
  "state": {
    "status": "ready",
    "goal": "完成合同复核并给出可追溯的风险结论",
    "progress": {
      "summary": "已确认优先检查自动续费条款",
      "completed": ["建立结构化复核决定"]
    },
    "decisions": [],
    "next_steps": [
      {"summary": "检查解约窗口和违约责任", "references": []}
    ],
    "blockers": [],
    "open_questions": [],
    "artifacts": []
  },
  "producer": {"agent_id": "claude-code", "session_id": "local-demo"}
}
JSON

# 另一个 Agent/设备只需同一 workspace 的 read token 即可恢复确定性状态
bin/mem --server http://localhost:8787 resume \
  "contract-review/v1" --format json

# 整个 Agent workspace 跨工具/跨设备迁移。导出不会覆盖已有文件；
# 确认要替换时显式加 --force。
bin/mem --server http://localhost:8787 workspace export \
  --output ./agent-workspace.membundle

# 在目标 mem 实例或目标 workspace 中恢复。当前只实现 fresh 模式：
# 目标中任何对象冲突都会以 409 列出冲突项，不会静默合并。
# 脚本/CI 没有交互式 TTY，必须显式传 --yes。
bin/mem --server http://target-host:8787 workspace import \
  --input ./agent-workspace.membundle --mode fresh --yes

# 两个命令都要求：
#   1) admin token（admin 是当前 scope 模型的层级超集）；
#   2) Token 没有局部 paths[] 限制（/ 视为完整路径）；
#   3) 当前 workspace 角色为 owner 或 admin。
# capabilities 会分别返回 permissions.workspace_export /
# permissions.workspace_import，以及当前真实支持的 restore modes 和 bundle schema。

# REST 等价写入：幂等键必须放 Header，不放 body
curl -i -X POST http://localhost:8787/v1/memories \
  -H "Authorization: Bearer $MEM_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: local-rest-decision-v1' \
  -d '{"kind":"decision","content":"合同复核优先检查自动续费条款","path":"/Contracts","source":{"type":"agent"},"producer":{"agent_id":"codex"}}'
# 首次为 201/replayed:false；原请求重试为 200/replayed:true；
# 同 key 修改归一化后的 payload 为 409/idempotency_conflict。

# 图搜图（以文搜图，走 CLIP 视觉空间）：
#   --route visual 只搜图片；--route auto（默认）text+visual 并行融合
bin/mem put scripts/demo_data/images/golden_retriever_grass.jpg   # 先灌示例照片
bin/mem search "golden retriever on grass" --route visual          # 金毛排首位
bin/mem search "a cat" --route visual

# 为外部 Agent 召回带出处的上下文（mem 不生成答案）
curl -s -X POST http://localhost:8787/v1/context \
  -H "Authorization: Bearer $MEM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"What breed is the dog and what is it like?","source":"file","limit":8,"max_chars":12000}' | jq

# source=all 时调用方必须检查 partial/warnings；source=memory 可单独验证
# 模型离线时的记忆闭环。Agent Token 推荐 search,read,write，并且 path 必须落在
# Token paths[] 范围内。workspace 通过 --workspace / X-Workspace-ID 选择；
# 已绑定 workspace 的 Token 不能借此切换到其他 workspace。

# 4. 收工（保留数据，下次秒起）
bash scripts/dev_down.sh
# 或彻底清空数据： bash scripts/dev_down.sh --purge
```

### 在本机验证 SaaS 的 Idealab 质量档

`idealab-quality-v1` 只能在 `saas` 模式启用。它不是给 `local-fast-v1` 加一个
全局 key 的开关：配置必须在启动 Worker 与 memd 前同时存在。`dev_up.sh` 会保留已在
运行的进程，因此先停掉旧栈；把 Idealab 的 OpenAI-compatible endpoint 和 API key
通过终端安全注入/密钥管理提供给 Worker，**不要**把实际 key 写进仓库或示例文件。

```bash
bash scripts/dev_down.sh

export MEM_DEPLOYMENT_MODE=saas
export MEM_AI_PROFILES=local-fast-v1,idealab-quality-v1
export MEM_MANAGED_EMBEDDING_PROVIDER=openai:text-embedding-3-large
export OPENAI_BASE_URL='https://<idealab-openai-compatible-endpoint>'
# 在当前进程环境中安全注入 OPENAI_API_KEY；这里不展示真实值。

bash scripts/dev_up.sh

# 使用有权限的 workspace token 登录后：
bin/mem --server http://localhost:8787 profile list
bin/mem --server http://localhost:8787 profile select idealab-quality-v1
```

选择会先验证固定的 `openai:text-embedding-3-large` 768 维 contract。缺 key、endpoint
不可用或维度不匹配都不会触发本地/其他云模型回退；受托管 entitlement 保护的语义请求
仍需有可用 entitlement。索引或搜索只有已声明且成功的阶段才会产生输出；文件可带
`partial` 状态，`context --source all` 可保留独立的结构化记忆 evidence 并附带警告。
质量档的模型固定及其“不回退”是产品路由保证，不是对任何数据集效果的 benchmark
承诺。更完整的托管配置和计费边界见
[MANAGED_EMBEDDINGS.md](MANAGED_EMBEDDINGS.md)。

### Workspace 迁移资源保护

导入和导出默认共用 8 GiB 的归档硬上限、30 分钟总超时，并且每个 memd
最多并行处理 2 个迁移请求。超过归档上限返回 `413`；并发额度已满时返回
`429` 和 `Retry-After`；临时卷空间或配额耗尽时返回
`507 workspace_transfer_storage_exhausted`。这些限制不会改变普通请求的
60 秒超时。

```bash
export MEM_WORKSPACE_TRANSFER_TIMEOUT=30m
export MEM_WORKSPACE_BUNDLE_MAX_BYTES=8589934592
export MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT=2

# 可选：将临时归档放到专用目录。目录必须恰好是 0700。
install -d -m 700 .dev/workspace-transfer
export MEM_WORKSPACE_TRANSFER_TMP_DIR="$PWD/.dev/workspace-transfer"
```

`MEM_WORKSPACE_BUNDLE_MAX_BYTES` 同时约束上传体和导出归档；memd 会先完整生成并
检查导出归档大小，再发送任何响应内容。临时文件权限为 `0600`，请求结束后清理；
memd 不会递归删除所配置的临时目录。

### 旧库与 profile 切换

迁移会把旧文本向量标成 `legacy:unknown`，不会根据今天的配置猜测历史模型。
这是有意的 fail-closed 行为：工作区只可在没有待处理/索引中文件、且现有语料已被
证明与目标 profile 使用同一个 embedding provider 时选择 profile。`legacy:unknown`
或混合 provider 的语料不会被 768 维这个共同尺寸“猜成兼容”。

从 `local-fast-v1` 切到付费档（或反向切换）不是原地改一个 provider：必须创建新的
index generation，使用新 profile 全量重建，并在新 generation 完整后原子激活。当前
服务会拒绝直接覆盖不兼容的既有语料；不要用旧的 `mem provider reindex` 把一个已选择
的 profile 改成任意模型。这样查询不会在同一向量空间中混入不同模型的结果。

## Web 前端接真后端

```bash
cd web
npm install
# 关掉 mock，指向真后端 :8787（默认 vite proxy 已指向 8787）
echo "VITE_USE_MOCK=false" > .env.local
echo "VITE_API_BASE=http://localhost:8787" >> .env.local
npm run dev        # 开发服务器，代理 /v1 -> :8787
# 或生产构建：
npm run build      # 产物在 web/dist/
```
登录用上面 seed 出来的 `demo@mem.local` / `demo-password-change-me`。

## 服务清单与端口

| 服务      | 端口   | 进程                         | 日志                  |
|-----------|--------|------------------------------|-----------------------|
| PostgreSQL| 5432   | brew `postgresql@17`         | `.dev/logs/postgres.log` |
| MinIO     | 9100   | `.dev/bin/minio`             | `.dev/logs/minio.log` |
| worker    | 50051  | `worker/.venv` python gRPC   | `.dev/logs/worker.log`|
| memd      | 8787   | `bin/memd` (Go)              | `.dev/logs/memd.log`  |
| Ollama    | 11434  | 系统已有                     | —                     |

## 关键设计点

- **Redis 跳过**：`MEM_REDIS_URL` 留空 → memd 用进程内 goroutine fallback 做异步索引
  （dev 足够；生产必须接真 Redis）。
- **迁移自动跑**：memd 启动时调用 `database.Migrate(ctx)`（goose 嵌入式 SQL），
  无需独立迁移命令。
- **数据持久**：`dev_down.sh` 默认保留 `.dev/pgdata` 和 `.dev/miniodata`，
  种子数据和向量在重启后仍在。
- **图片维度守卫**：`embeddings_visual` 是 `vector(512)`；worker 只在向量维度
  == 512 时入库 visual 向量，否则跳过——避免降级 embedder（如 768 维 nomic）
  让整个索引事务回滚把文件标记 `failed`。
- **图片 visual provider 显式化**：indexer 对 `image/*` 文件显式下发
  `visual_embedding_provider=clip:ViT-B-32`（`server/internal/indexer/indexer.go`），
  不再依赖 worker 默认值碰巧是 CLIP。当前不允许在线覆盖 visual provider，
  避免索引与查询进入不同向量空间；待 versioned index generation 落地后再开放。

## 放自己的照片（图搜图）

```bash
export MEM_TOKEN=$(curl -s -X POST http://localhost:8787/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@mem.local","password":"demo-password-change-me"}' | jq -r .token)

# 单张
bin/mem put ~/Pictures/your_photo.jpg
# 整个目录
bin/mem put ~/Pictures --recursive

# 等 index_status=done 后，以文搜图：
bin/mem search "a golden retriever standing on green grass" --route visual
bin/mem search "snowy mountain at sunset" --route visual
```

`scripts/demo_data/images/` 下自带 3 张开放许可示例照片（Wikimedia Commons：
金毛犬 / 猫 / 河流风景），可直接 `bin/mem put` 进去验证图搜图。换成你自己的照片
只需放进任意目录再 `put` 即可。默认 checkpoint 的英文描述能力已通过这组三图
基线；中文查询仍受上面的质量边界约束。纯噪声或占位图本身也无法形成有意义的
语义召回。

## 常见问题排查

- `dev_up.sh` 卡在某个 `wait_for`：去 `.dev/logs/<svc>.log` 看尾部。
- `seed_demo_data.sh` 断言超时：CPU 跑 nomic/CLIP 较慢，调大
  `MEM_INGEST_TIMEOUT_SEC=600 bash scripts/seed_demo_data.sh`。
- `CREATE EXTENSION vector failed`：当前 postgres 版本没有匹配的 pgvector
  `.so`；装 `brew install postgresql@17` 并重跑 `dev_up.sh`。
- `idempotency_conflict`：同一幂等键已绑定另一份归一化请求；重试原请求或换一个
  能稳定标识新事件的 key，不要随机重试冲突请求。
- `path_forbidden`：记忆路径或来源文件超出 Token `paths[]`；缩小目标路径或使用
  被明确授权的 Token。
- `token_workspace_forbidden`：Token 已绑定其他 workspace；切回其绑定 workspace，
  或在目标 workspace 中重新创建 Token。
