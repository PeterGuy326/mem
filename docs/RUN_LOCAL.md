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
   - worker Python 依赖：
     ```bash
     cd worker && uv sync && cd ..
     ```
   - memd 二进制（`dev_up.sh` 会在缺失时自动 `go build`，也可手动）：
     ```bash
     make build-memd build-mem   # -> bin/memd, bin/mem
     ```

2. **Ollama**：必须已在 `http://localhost:11434` 运行，且已 pull：
   - `nomic-embed-text`（768 维文本 embedding，对上 schema `embeddings_text vector(768)`）
   - `llama3.1`（RAG 问答的 chat LLM）
   - 视觉模型（minicpm-v）**可选缺失**：图片走优雅降级，只存 caption/EXIF，
     不产出 512 维 visual 向量，索引不崩（见 `worker/mem_worker/processors/image.py`
     的维度守卫 `VISUAL_EMBED_DIM`）。

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

# 搜索
bin/mem --server http://localhost:8787 search "a dog on a meadow" --format json

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

## 常见问题排查

- `dev_up.sh` 卡在某个 `wait_for`：去 `.dev/logs/<svc>.log` 看尾部。
- `seed_demo_data.sh` 断言超时：CPU 跑 llama3.1/nomic 慢，调大
  `MEM_INGEST_TIMEOUT_SEC=600 bash scripts/seed_demo_data.sh`。
- `CREATE EXTENSION vector failed`：当前 postgres 版本没有匹配的 pgvector
  `.so`；装 `brew install postgresql@17` 并重跑 `dev_up.sh`。
