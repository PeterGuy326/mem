# 北极星验收：Agent 网盘

这份清单把 [GOAL.md](../../GOAL.md) 的产品愿景转换成可重复执行的验收。
功能只有在真实边界上通过，才算“可迁移”或“可恢复”；界面截图、单个服务的
单元测试和口头演示都不能单独替代端到端证据。

## 1. 跨 Agent 接续

给 Claude Code 与 Codex 两个独立 token，但绑定同一 workspace：

1. Claude Code 用标准 `mem.handoff` v1 写入任务 checkpoint。
2. Codex 仅凭稳定 `task_key` 读取并恢复 checkpoint。
3. 恢复结果包含目标、进度、决定、阻塞、下一步、工作区状态和引用解析结果。
4. Codex 的只读 token 不能新增 checkpoint。
5. 同名任务在另一个 workspace 中不可见；越出 token path 的任务不可见。
6. payload SHA-256 不一致时恢复失败，不能返回未经校验的状态。

通过标准：用户无需重新描述背景，接手 Agent 能基于同一份可验证状态继续。

## 2. 跨设备恢复

在两个相互隔离的 PostgreSQL 与对象存储环境中执行：

1. 源 workspace 同时包含文件/图片、文件夹、结构化记忆、任务、多个 checkpoint
   与 checkpoint 引用；至少两个不同文件名/路径共享同一内容 SHA-256。
2. 导出 `.membundle` 后，在目标环境先完整校验 manifest、每个条目 checksum、
   blob SHA-256、依赖关系和 checkpoint 版本链。
3. 导入到空 workspace，保留所有可移植对象 ID、虚拟路径、时间、内容哈希、
   task key、checkpoint sequence/base/head 和引用次序。
4. token、密码、登录态、membership、provider 密钥、对象存储 key、向量、
   人脸/实体/关系索引、原始幂等键和运行中任务不进入迁移包。
5. 任一校验、ID 冲突或版本链冲突都必须在写入前报告；不能产生“部分成功但没有
   明细”的目标 workspace。
6. 导入后重新生成目标本地的对象存储 key 与派生索引，并能下载原始文件、读取
   记忆、恢复最新任务；两个同 SHA 文件仍是独立条目和独立对象 key，删除一个
   不得破坏另一个。

通过标准：源端关闭后，目标端仍能校验原件并让 Agent 从最后有效 checkpoint 接续。

## 3. 人类可见、可信、可控

在 Web UI 中：

1. Drive 展示原始文件与文件夹，并可预览/下载原件。
2. Tasks 展示任务、不可变 checkpoint 时间线、生产 Agent、状态、哈希和引用。
3. Resume 明确区分确定性任务状态、已解析引用、缺失/哈希不符引用和可选语义证据。
4. Memories 展示内容、类型、作用域、来源、生产 Agent/会话/任务、时间和内容哈希。
5. Transfer 展示 bundle schema、fresh-only/空目标约束、导入 SHA-256、对象计数、
   replay 与有界结构化冲突；导入前必须显式确认。
6. 所有请求都按当前 workspace 缓存与失效；切换 workspace 不得闪现上一空间数据。
7. API 或权限失败有错误原因与重试入口，不能伪装成空列表。

通过标准：用户能回答“系统保存了什么、谁写的、来自哪里、哪个版本、能否验证”。

## 4. 自然语言搜图与搜文件

使用包含相近图片、无关图片、文本/PDF 和时间元数据的固定评测集：

1. “草地上的金毛”等描述能通过 CLIP 文本塔召回由原始图片字节生成的视觉向量。
2. “去年关于 RAG 的笔记”等查询能通过文本通道召回文档。
3. `auto` 合并文本与视觉通道并按 file ID 去重；结果标明实际命中通道。
4. 图片处理必须把原始字节传给 `embed_image`；错误维度的向量不得写入
   `embeddings_visual`。
5. VLM、视觉编码、文本编码、数据库或权限失败必须可观察；没有证据时不得生成
   看似成功的答案。
6. 每个结果包含稳定 file ID、内容 SHA-256、可读 snippet/caption、分数和原件入口。

通过标准：检索结果可回到原件并解释命中依据；迁移后的同一批原件可重新索引并得到
等价的可验证召回能力。

## 5. 发布门槛

每次影响迁移、记忆、搜索或授权边界的发布至少按
[docs/TESTING.md](../TESTING.md) 运行：

- `make test`：Go 全量测试与静态检查、Worker 回归、Web
  typecheck/lint/build 及 Memory/Transfer 浏览器验收；
- `make test-race`：高风险 Go 路径 race 回归；
- `make test-env-up && make test-integration &&
  make test-integration-race`：隔离 `_test` 数据库上的 migration 与
  PostgreSQL 语义/race 场景，具体版本和必跑清单以 `docs/TESTING.md` 为准；
- `make test-acceptance`：隔离进程上的真实 HTTP、CLI 和 MCP Agent-memory
  接续；
- 两个独立环境之间的 bundle round-trip；
- transfer 的部署级 archive/metadata/record/并发/超时边界与磁盘耗尽错误映射；
- `git diff --check` 和 JSON Schema 解析。

任何测试如因外部模型、对象存储或数据库不可用而跳过，发布记录必须明确标出，不能
把“未执行”记为“通过”。双部署 bundle round-trip、真实 Claude Code/Codex
宿主 smoke 和外部模型质量评测当前仍是发布级手工验收，不得用 mock 或单进程测试
替代其结论。
