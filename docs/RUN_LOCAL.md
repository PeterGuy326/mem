# 本地运行 mem 全栈（裸机 · 无 Docker）

这台机器没有 Docker，所以整套栈用**本地进程**拉起，不走 `docker compose`。
一条命令起、一条命令停，运行时数据全部落在 `.dev/`（已 gitignore）。

```
┌──────────┐   gRPC    ┌──────────┐   HTTP    ┌──────────┐
│  worker  │◀──────────│   memd   │◀──────────│  web/CLI │
│ :50051   │           │  :8787   │           │          │
└────┬─────┘           └────┬─────┘           └──────────┘
     │ Ollama :11434        │ pgvector :5432
     │ (nomic + llama3.1)   │ MinIO    :9100
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
   - worker Python 依赖（`--extra clip` 装 CLIP，启用真正的图搜图）：
     ```bash
     cd worker && uv sync --extra clip && cd ..
     ```
     > `dev_up.sh` 启动 worker 前也会自动跑一次 `uv sync --extra clip`，所以
     > 平时直接 `bash scripts/dev_up.sh` 即可，无需手动 sync。
     > CLIP 走 CPU、首次会下载 ViT-B-32 权重（~600MB，缓存在 `~/.cache`），
     > torch CPU wheel 也较大，第一次 sync 耐心等。
   - memd 二进制（`dev_up.sh` 会在缺失时自动 `go build`，也可手动）：
     ```bash
     make build-memd build-mem   # -> bin/memd, bin/mem
     ```

2. **Ollama**：必须已在 `http://localhost:11434` 运行，且已 pull：
   - `nomic-embed-text`（768 维文本 embedding，对上 schema `embeddings_text vector(768)`）
   - `llama3.1`（RAG 问答的 chat LLM）
   - 视觉模型（minicpm-v）**可选缺失**：影响的是 caption 文本，不影响图搜图本身。
     图片的视觉向量由 **CLIP**（`clip:ViT-B-32`，见下）产出，与 Ollama 无关。
   - **图搜图 = CLIP**：装了 `--extra clip` 后，图片入库时由 CLIP image-tower
     编码成 512 维视觉向量写进 `embeddings_visual`，搜索时 query 文本由 CLIP
     text-tower 编码到同一空间做 ANN——这才是"以文搜图"。若 CLIP 没装，图片
     走优雅降级（caption 文本向量是 768 维，被维度守卫 `VISUAL_EMBED_DIM=512`
     拒绝跳过），`embeddings_visual` 为空、图搜图搜不到东西。

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

# 文本搜索（文档语义检索）
bin/mem --server http://localhost:8787 search "a dog on a meadow" --format json

# 图搜图（以文搜图，走 CLIP 视觉空间）：
#   --route visual 只搜图片；--route auto（默认）text+visual 并行融合
bin/mem put scripts/demo_data/images/golden_retriever_grass.jpg   # 先灌示例照片
bin/mem search "golden retriever on grass" --route visual          # 金毛排首位
bin/mem search "a cat" --route visual

# RAG 问答（llama3.1 生成 + 出处）
curl -s -X POST http://localhost:8787/v1/ask \
  -H "Authorization: Bearer $MEM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"question":"What breed is the dog and what is it like?"}' | jq

# 4. 收工（保留数据，下次秒起）
bash scripts/dev_down.sh
# 或彻底清空数据： bash scripts/dev_down.sh --purge
```

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
  不再依赖 worker 默认值碰巧是 CLIP。用户也可在 `provider_settings` 里存
  `kind='visual_embedding'` 覆盖。

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
bin/mem search "草地上的金毛" --route visual
bin/mem search "snowy mountain at sunset" --route visual
```

`scripts/demo_data/images/` 下自带 3 张开放许可示例照片（Wikimedia Commons：
金毛犬 / 猫 / 河流风景），可直接 `bin/mem put` 进去验证图搜图。换成你自己的照片
只需放进任意目录再 `put` 即可——CLIP 是通用视觉模型，对真实照片才有意义
（纯噪声/占位图搜不出有意义结果）。

## 常见问题排查

- `dev_up.sh` 卡在某个 `wait_for`：去 `.dev/logs/<svc>.log` 看尾部。
- `seed_demo_data.sh` 断言超时：CPU 跑 llama3.1/nomic 慢，调大
  `MEM_INGEST_TIMEOUT_SEC=600 bash scripts/seed_demo_data.sh`。
- `CREATE EXTENSION vector failed`：当前 postgres 版本没有匹配的 pgvector
  `.so`；装 `brew install postgresql@17` 并重跑 `dev_up.sh`。
