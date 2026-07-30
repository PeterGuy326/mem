# mem · Web UI (Phase 1)

> 极简 Web UI · 上传 / 搜索 / 详情 三页 · 设计 system + Mock API 跑通

W1 范围：工程骨架 + 设计语言定调 + MSW mock，让产品形态先立起来。W4 接真后端（`/v1/*` → Go `memd`）。

---

## 快速开始

```bash
cd web
npm install
npm run dev          # http://localhost:5173 — 默认开启 MSW mock
```

### 走真后端（W4）

```bash
echo "VITE_USE_MOCK=false" > .env.local
npm run dev          # /v1/* 通过 Vite proxy 转到 http://localhost:8787
```

### 构建 / Lint / 类型检查 / 浏览器回归

```bash
npm run build        # 产出 dist/
npm run lint         # ESLint 严格 0 警告
npm run typecheck    # tsc --noEmit
npm run test:i18n    # 词典/硬编码审计 + 中英文运行时切换验收
npm run test:enrichment # 验证文件增强与人工审核流程
npm run test:memory  # 自启 Vite + MSW，验证 Memory 生命周期与权限态
npm run test:transfer # 自启 Vite + MSW，验证 workspace transfer
```

首次运行浏览器回归前执行 `npx playwright install chromium`。这些标准验收脚本
不需要 memd、Worker 或 PostgreSQL；完整测试环境、预期结果和清理方法见
[`docs/TESTING.md`](../docs/TESTING.md)。

---

## 目录结构

```
web/
├── index.html
├── vite.config.ts          # /v1/* → :8787 proxy
├── tailwind.config.js      # 设计 tokens
├── src/
│   ├── main.tsx            # 入口，按 VITE_USE_MOCK 启 MSW
│   ├── styles/globals.css  # CSS variables（颜色 / 间距）
│   ├── components/
│   │   ├── ui/             # Button / Input / Card / Badge / Skeleton / ConfirmDialog / EmptyState / Kbd
│   │   └── layout/         # AppShell / TopBar / Sidebar / Logo
│   ├── pages/              # Login / Upload / Search / FileDetail / Settings / NotFound
│   ├── routes/             # router + LoginGate
│   ├── hooks/              # useAuth / useFiles (react-query)
│   ├── lib/                # api.ts (fetch + auth header) / types.ts (SPEC §6/§7/§8) / format / cn
│   └── mocks/              # MSW: handlers + fixtures（80+ files, 5 face clusters, 2010-2024）
└── public/                 # mockServiceWorker.js 由 MSW 自动生成
```

---

## 设计 system

- **风格**：Linear / Vercel / Raycast。克制、深色优先、几何感、零拟物。
- **配色**：暗色主调（`--bg: 10 11 15`），强调色 `indigo-400` (`--accent: 129 140 248`)。
  CSS 变量 + Tailwind `<alpha-value>` 模式，支持透明度组合 (`bg-accent/10`)。
- **字体**：Inter (CDN 加载) + 系统中文回退 (`PingFang SC` / `Hiragino Sans GB` / `Microsoft YaHei`)。
- **组件**：Tailwind + Radix UI 原语（Dialog / Dropdown / Tooltip）+ `lucide-react` 图标。
  **拒绝** Antd / MUI。
- **状态色**：`success` 绿、`warn` 琥珀、`danger` 红 — 仅用于 chip / badge / 危险按钮。
- **语言**：账户菜单可即时切换简体中文 / English，显式偏好保存在当前浏览器的
  `mem.lang`；产品文案全部来自双语词典，文件名、记忆正文、Provider/协议标识保持原样。
- **暗色变量**已写齐；`light` 模式 W2 解锁（CSS 变量已就位）。

---

## 三个页面

| 路径 | 文件 | 说明 |
|------|------|------|
| `/` | `UploadPage.tsx` | Hero + drag-drop + 最近 10 条入库 |
| `/search?q=...` | `SearchPage.tsx` | 顶部巨型搜索框 + 类型/时间过滤 + 图片瀑布 + 文档列表混排 |
| `/files/:id` | `FileDetailPage.tsx` | 左预览 / 右元数据 + AI 卡片 + 相关文件 |
| `/login` | `LoginPage.tsx` | 极简登录页（W1 mock：任意账号通过） |
| `/settings` | `SettingsPage.tsx` | Provider / Token / 主题（占位） |

---

## 数据契约

类型定义在 `src/lib/types.ts`，严格对齐 `SPEC.md`：
- `MemFile` → §6.1 `files` 表
- `Token` → §6.1 `tokens` 表 + §7
- `SearchResult` / `SearchResponse` → §8.1 `mem_search` 输出
- `RelatedResponse` → §8.1 `mem_related`
- 错误结构：`{ error, hint }`（§8.2）

API endpoints 默认全部走 mock，与后端 W3/W4 输出一一对应：

| Method | Path | 用途 |
|--------|------|------|
| POST | `/v1/auth/login` | 登录 → token |
| POST | `/v1/files` | multipart 上传 |
| GET | `/v1/files?limit=N` | 最近文件列表 |
| GET | `/v1/files/:id` | 文件详情 |
| GET | `/v1/files/:id/related` | 关联文件 |
| DELETE | `/v1/files/:id` | 删除 |
| GET | `/v1/search?q=...&type=...&since=...` | 搜索 |
| GET | `/v1/entities?type=person` | 实体（W2 人脸） |

---

## Mock 数据

`src/mocks/fixtures.ts` 用确定性 PRNG 生成 80+ 文件：
- 60 张图片（picsum.photos seed，跨 2010-2024）
- 14 个文档（PDF / md / docx，含合同 / 笔记 / 体检报告等）
- 6 个音频（播客 / 备忘 / 钢琴）
- 1 个 "hero" 文件：`hero-2012-yunnan-xiaoming` — SPEC §10.2 的杀手 demo 锚点

实体（人脸聚类）：小明 / 小红 / 妈妈 / 小张 / 老王 + 地点（云南、北京）+ 事件（高中毕业）。

上传后 mock 会 4 秒后把 `index_status` 从 `processing` 翻到 `done`，模拟 AI pipeline。

---

## 未做的（按 W1 范围）

- [ ] 移动端响应式（W1 桌面优先）
- [ ] 主题切换（结构就位 W2 上）
- [ ] Phase 2 路由：`/timeline` `/faces` `/tags`（已占位）
- [ ] 真接后端（W4 移除 mock）

## TODO（与其他 Agent 对齐）

- [ ] W2：人脸过滤 UI（Backend `entities` API）
- [ ] W3：`/files/:id/related` 接真，relation chip 加 score
- [ ] W3：search 接入流式（SSE，对应 CLI `--stream`）
- [ ] W4：MSW 关闭，端到端跑通杀手 demo
- [ ] W4：截图录屏，README 加 hero gif
