/**
 * Deterministic hierarchical fixtures.
 *
 * Files use real folder paths under `/Photos/...`, `/Docs/...`, `/Audio/...`,
 * plus a handful of root-level files. The PRNG is seeded so reloads are stable.
 */

import type { Entity, MemFile, IndexStatus, FileKind } from '@/lib/types';

// ---------- seeded PRNG ----------
function mulberry32(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const rand = mulberry32(20260511);
const pick = <T,>(arr: T[]): T => arr[Math.floor(rand() * arr.length)] as T;
const between = (a: number, b: number) => a + Math.floor(rand() * (b - a + 1));

function uuid(): string {
  const hex = '0123456789abcdef';
  let out = '';
  for (let i = 0; i < 32; i++) {
    out += hex[Math.floor(rand() * 16)];
    if (i === 7 || i === 11 || i === 15 || i === 19) out += '-';
  }
  return out;
}

// ---------- Entities ----------
export const ENTITIES: Entity[] = [
  { id: 'ent-xm', type: 'person', name: '小明', metadata: { face_count: 18 } },
  { id: 'ent-xh', type: 'person', name: '小红', metadata: { face_count: 12 } },
  { id: 'ent-mama', type: 'person', name: '妈妈', metadata: { face_count: 9 } },
  { id: 'ent-xz', type: 'person', name: '小张', metadata: { face_count: 6 } },
  { id: 'ent-laowang', type: 'person', name: '老王', metadata: { face_count: 4 } },
  { id: 'ent-yunnan', type: 'place', name: '云南' },
  { id: 'ent-beijing', type: 'place', name: '北京' },
  { id: 'ent-graduation', type: 'event', name: '高中毕业' },
];

// ---------- Themes ----------
const IMAGE_THEMES = [
  { caption: '草地上的金毛犬在追飞盘', tags: ['宠物', '金毛', '户外'] },
  { caption: '云南大理苍山下的客栈庭院', tags: ['旅行', '云南', '风景'] },
  { caption: '高中毕业那天的合影', tags: ['毕业', '同学', '校园'] },
  { caption: '雪后的故宫角楼', tags: ['北京', '故宫', '冬天'] },
  { caption: '生日蛋糕特写', tags: ['生日', '美食'] },
  { caption: '咖啡馆里的笔记本电脑和拿铁拉花', tags: ['工作', '咖啡'] },
  { caption: '夜晚的城市天际线', tags: ['夜景', '城市'] },
  { caption: '海边日落', tags: ['旅行', '海边', '日落'] },
  { caption: '猫蜷在窗台上晒太阳', tags: ['宠物', '猫', '室内'] },
  { caption: '一桌火锅', tags: ['美食', '火锅'] },
];

const STATUS_DIST: IndexStatus[] = [
  ...Array(80).fill('done'),
  ...Array(10).fill('processing'),
  ...Array(7).fill('pending'),
  ...Array(3).fill('failed'),
] as IndexStatus[];

const MIMES: Record<FileKind, string[]> = {
  image: ['image/jpeg', 'image/png', 'image/webp'],
  doc: [
    'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    'application/msword',
  ],
  pdf: ['application/pdf'],
  text: ['text/markdown', 'text/plain'],
  audio: ['audio/mpeg', 'audio/x-m4a', 'audio/wav'],
  video: ['video/mp4'],
  other: ['application/octet-stream'],
};

function randomDate(yearMin = 2012, yearMax = 2024): string {
  const y = between(yearMin, yearMax);
  const m = between(1, 12);
  const d = between(1, 28);
  const hh = between(0, 23);
  const mm = between(0, 59);
  return new Date(Date.UTC(y, m - 1, d, hh, mm)).toISOString();
}

function picsum(seed: string, w = 800, h = 600): string {
  return `https://picsum.photos/seed/${encodeURIComponent(seed)}/${w}/${h}`;
}

function pickEntitiesFor(theme: { tags: string[]; caption: string }): Entity[] {
  const out: Entity[] = [];
  if (rand() < 0.45) out.push(pick([ENTITIES[0]!, ENTITIES[1]!, ENTITIES[2]!, ENTITIES[3]!, ENTITIES[4]!]));
  if (theme.caption.includes('云南')) out.push(ENTITIES[5]!);
  if (theme.caption.includes('北京') || theme.caption.includes('故宫')) out.push(ENTITIES[6]!);
  if (theme.caption.includes('毕业')) out.push(ENTITIES[7]!);
  return Array.from(new Map(out.map((e) => [e.id, e])).values());
}

function makeImage(folder: string, idx: number, opts: { yearMin?: number; yearMax?: number } = {}): MemFile {
  const theme = pick(IMAGE_THEMES);
  const id = uuid();
  const status = pick(STATUS_DIST);
  const ts = randomDate(opts.yearMin ?? 2012, opts.yearMax ?? 2024);
  const name = `IMG_${String(1000 + idx).padStart(4, '0')}_${id.slice(0, 4)}.jpg`;
  const ents = pickEntitiesFor(theme);
  const personNames = ents.filter((e) => e.type === 'person').map((e) => e.name);
  return {
    id,
    user_id: 'user-1',
    name,
    path: `${folder}/${name}`,
    size: between(800_000, 6_500_000),
    sha256: id.replace(/-/g, '') + '0'.repeat(32),
    mime: pick(MIMES.image),
    storage_key: `s3://mem/photos/${id}`,
    summary: null,
    caption: status === 'done' ? theme.caption : null,
    tags: status === 'done' ? [...theme.tags, ...personNames] : [],
    timeline_at: ts,
    geo:
      theme.caption.includes('云南')
        ? { lat: 25.04, lon: 102.71 }
        : theme.caption.includes('北京') || theme.caption.includes('故宫')
          ? { lat: 39.92, lon: 116.4 }
          : null,
    index_status: status,
    created_at: ts,
    updated_at: ts,
    kind: 'image',
    preview_url: picsum(id, 1200, 900),
    thumbnail_url: picsum(id, 480, 360),
    download_url: picsum(id, 1600, 1200),
    entities: ents,
  };
}

function makeNamedFile(opts: {
  folder: string;
  name: string;
  kind: FileKind;
  summary?: string | null;
  yearMin?: number;
  yearMax?: number;
}): MemFile {
  const { folder, name, kind, summary = null } = opts;
  const id = uuid();
  const status = pick(STATUS_DIST);
  const ts = randomDate(opts.yearMin ?? 2020, opts.yearMax ?? 2024);
  const mime = (() => {
    if (kind === 'pdf') return 'application/pdf';
    if (kind === 'text') return name.endsWith('.md') ? 'text/markdown' : 'text/plain';
    if (kind === 'audio') return pick(MIMES.audio);
    if (kind === 'doc') return pick(MIMES.doc);
    return 'application/octet-stream';
  })();
  return {
    id,
    user_id: 'user-1',
    name,
    path: `${folder}/${name}`,
    size: between(20_000, 8_000_000),
    sha256: id.replace(/-/g, '') + '0'.repeat(32),
    mime,
    storage_key: `s3://mem/${kind}/${id}`,
    summary: status === 'done' ? summary : null,
    caption: null,
    tags: status === 'done' ? (kind === 'audio' ? ['音频'] : ['文档']) : [],
    timeline_at: ts,
    geo: null,
    index_status: status,
    created_at: ts,
    updated_at: ts,
    kind,
    preview_url: null,
    thumbnail_url: null,
    download_url: `#/mock/${id}`,
    entities: [],
  };
}

// ---------- Build the dataset ----------
function buildDataset(): MemFile[] {
  const files: MemFile[] = [];

  // /Photos/2012 — 25 张
  for (let i = 0; i < 25; i++) {
    files.push(makeImage('/Photos/2012', i, { yearMin: 2012, yearMax: 2012 }));
  }
  // /Photos/2024 — 25 张
  for (let i = 0; i < 25; i++) {
    files.push(makeImage('/Photos/2024', i, { yearMin: 2024, yearMax: 2024 }));
  }
  // /Photos/Pets — 10 张
  for (let i = 0; i < 10; i++) {
    files.push(makeImage('/Photos/Pets', i, { yearMin: 2020, yearMax: 2024 }));
  }
  // /Docs/合同 — 5 份
  const CONTRACTS = [
    { name: '租房合同-2023.pdf', kind: 'pdf' as FileKind, summary: '甲方老李，期限一年，月租 7800。' },
    { name: '工作合同副本.pdf', kind: 'pdf' as FileKind, summary: '入职 2022-03-01，试用期 6 个月。' },
    { name: '保险合同.pdf', kind: 'pdf' as FileKind, summary: '人寿保险，保额 100w。' },
    { name: '车辆买卖合同.docx', kind: 'doc' as FileKind, summary: '二手车交易，附验车报告。' },
    { name: '装修合同.pdf', kind: 'pdf' as FileKind, summary: '半包，工期 60 天。' },
  ];
  for (const c of CONTRACTS) {
    files.push(makeNamedFile({ folder: '/Docs/合同', name: c.name, kind: c.kind, summary: c.summary }));
  }
  // /Docs/笔记 — 10 份
  const NOTES = [
    { name: 'RAG 调研.md', summary: '对比 LangChain / LlamaIndex / Haystack。' },
    { name: '《编码》读书笔记.md', summary: '从电报到逻辑门。' },
    { name: '云南行程单.md', summary: '昆明 → 大理 → 丽江 → 香格里拉。' },
    { name: '会议-Q3规划.md', summary: 'Q3 重点：搜索召回率、人脸聚类。' },
    { name: '产品 spec 思考.md', summary: 'Agent-Native AI 网盘的核心数据模型。' },
    { name: 'Go vs Rust.md', summary: '主服务最终选 Go。' },
    { name: 'pgvector 索引.md', summary: 'HNSW vs IVFFLAT。' },
    { name: '设计 token.md', summary: '配色 / 字号 / 间距。' },
    { name: 'MCP 接入要点.md', summary: 'tool 入参约定 + 错误协议。' },
    { name: '本周复盘.md', summary: '完成了什么，没完成什么。' },
  ];
  for (const n of NOTES) {
    files.push(makeNamedFile({ folder: '/Docs/笔记', name: n.name, kind: 'text', summary: n.summary }));
  }
  // /Audio — 5 个
  const AUDIO = [
    { name: '播客-黄执中聊辩论.mp3', summary: '辩论本质是寻找共识空间。' },
    { name: '语音备忘-地下室漏水.m4a', summary: '物业周三上门检查。' },
    { name: '钢琴-肖邦Op9No2.mp3', summary: '第二乐章节奏略快。' },
    { name: '小米发布会录音.mp3', summary: '新机发布与现场氛围。' },
    { name: '英语口语练习.m4a', summary: '日常话题 20 分钟自言自语。' },
  ];
  for (const a of AUDIO) {
    files.push(makeNamedFile({ folder: '/Audio', name: a.name, kind: 'audio', summary: a.summary }));
  }
  // 根目录散落 5 个
  const ROOT_FILES = [
    { name: 'README.md', kind: 'text' as FileKind, summary: '我的网盘根目录速览。' },
    { name: '银行流水-2023.pdf', kind: 'pdf' as FileKind, summary: '主要支出：房租 / 食物 / 旅行。' },
    { name: '体检报告.pdf', kind: 'pdf' as FileKind, summary: '总体良好，关注尿酸偏高。' },
    { name: '简历-final.pdf', kind: 'pdf' as FileKind, summary: '最新简历，三页。' },
    { name: '账号密码-存档.txt', kind: 'text' as FileKind, summary: '已迁移到密码管理器。' },
  ];
  for (const r of ROOT_FILES) {
    files.push(makeNamedFile({ folder: '', name: r.name, kind: r.kind, summary: r.summary }));
  }

  // Pin a hero file under /Photos/2012
  const heroTs = '2012-07-14T15:22:00Z';
  const hero: MemFile = {
    id: 'hero-2012-yunnan-xiaoming',
    user_id: 'user-1',
    name: 'IMG_2012_DALI_0427.jpg',
    path: '/Photos/2012/IMG_2012_DALI_0427.jpg',
    size: 3_842_112,
    sha256: 'a'.repeat(64),
    mime: 'image/jpeg',
    storage_key: 's3://mem/photos/hero',
    summary: null,
    caption: '2012 年夏天，云南大理洱海边，和小明的合影。',
    tags: ['旅行', '云南', '大理', '小明', '高中', '2012'],
    timeline_at: heroTs,
    geo: { lat: 25.706, lon: 100.18 },
    index_status: 'done',
    created_at: heroTs,
    updated_at: heroTs,
    kind: 'image',
    preview_url: picsum('hero-yunnan-2012', 1600, 1066),
    thumbnail_url: picsum('hero-yunnan-2012', 480, 320),
    download_url: picsum('hero-yunnan-2012', 2400, 1600),
    entities: [ENTITIES[0]!, ENTITIES[5]!],
  };
  files.unshift(hero);

  // Make root-level files have stable root paths (we wrote "" + "/x" above).
  for (const f of files) {
    if (f.path.startsWith('//')) f.path = f.path.slice(1);
    if (!f.path.startsWith('/')) f.path = '/' + f.path;
  }

  return files.sort((a, b) => (a.created_at < b.created_at ? 1 : -1));
}

export const FILES: MemFile[] = buildDataset();

/** Folders explicitly created via the UI that may have no files. Mock-only. */
export const GHOST_FOLDERS: Set<string> = new Set<string>();

export function findFile(id: string): MemFile | undefined {
  return FILES.find((f) => f.id === id);
}

// ---------- Search / scoring (left intact for /v1/search if still used) ----------
function tokenize(s: string): string[] {
  const parts = s
    .toLowerCase()
    .replace(/[，。！？、,.!?]/g, ' ')
    .split(/\s+/)
    .filter(Boolean);
  const tokens = new Set(parts);
  // The mock is only a deterministic UI fixture, not a semantic evaluator.
  // Add CJK bigrams so the product's Chinese demo queries still exercise the
  // intended result UI instead of becoming one unmatched, sentence-long token.
  for (const part of parts) {
    if (!/^[\p{Script=Han}]+$/u.test(part) || part.length < 2) continue;
    for (let i = 0; i < part.length - 1; i++) tokens.add(part.slice(i, i + 2));
  }
  return Array.from(tokens);
}

export function searchFiles(opts: {
  q: string;
  type?: string;
  since?: string;
  until?: string;
  face?: string;
  limit?: number;
}): {
  results: {
    file: MemFile;
    score: number;
    snippet: string | null;
    channel: 'visual' | 'text' | 'metadata' | 'fused';
  }[];
  total: number;
} {
  const tokens = tokenize(opts.q);
  let pool = FILES.filter((f) => f.index_status === 'done');
  if (opts.type && opts.type !== 'any') {
    pool = pool.filter((f) => {
      if (opts.type === 'image') return f.kind === 'image';
      if (opts.type === 'doc') return f.kind === 'doc' || f.kind === 'pdf' || f.kind === 'text';
      if (opts.type === 'audio') return f.kind === 'audio';
      return true;
    });
  }
  if (opts.since) pool = pool.filter((f) => (f.timeline_at ?? f.created_at) >= opts.since!);
  if (opts.until) pool = pool.filter((f) => (f.timeline_at ?? f.created_at) <= opts.until!);
  if (opts.face) {
    pool = pool.filter((f) =>
      f.entities?.some((e) => e.type === 'person' && e.name === opts.face),
    );
  }
  const scored = pool.map((f) => {
    const hay = [
      f.name,
      f.caption ?? '',
      f.summary ?? '',
      ...f.tags,
      ...(f.entities ?? []).map((e) => e.name),
    ]
      .join(' ')
      .toLowerCase();
    let score = 0;
    for (const tok of tokens) if (hay.includes(tok)) score += 1;
    score += 1 / (1 + (Date.now() - new Date(f.created_at).getTime()) / (365 * 24 * 3600 * 1000));
    const channel: 'visual' | 'text' | 'metadata' | 'fused' =
      f.kind === 'image' ? 'visual' : f.kind === 'audio' ? 'metadata' : 'text';
    const snippet = f.caption ?? f.summary ?? (f.tags.length ? `标签：${f.tags.join('、')}` : null);
    return { file: f, score, snippet, channel };
  });
  scored.sort((a, b) => b.score - a.score);
  const limit = opts.limit ?? 30;
  return { results: scored.slice(0, limit), total: scored.length };
}

export function relatedFor(id: string) {
  const f = findFile(id);
  if (!f) return [];
  const personNames = (f.entities ?? []).filter((e) => e.type === 'person').map((e) => e.name);
  const tagSet = new Set(f.tags);
  const year = (f.timeline_at ?? f.created_at).slice(0, 4);

  const candidates = FILES.filter((x) => x.id !== id && x.index_status === 'done').map((x) => {
    const personOverlap = (x.entities ?? [])
      .filter((e) => e.type === 'person')
      .some((e) => personNames.includes(e.name));
    const tagOverlap = x.tags.filter((t) => tagSet.has(t)).length;
    const sameYear = (x.timeline_at ?? x.created_at).startsWith(year);

    let relation: '同事件' | '同人' | '同主题' | '续作' = '同主题';
    let score = tagOverlap * 0.2;
    if (personOverlap) {
      relation = '同人';
      score += 0.6;
    }
    if (sameYear && tagOverlap >= 2) {
      relation = '同事件';
      score += 0.4;
    }
    return { file: x, relation, score };
  });
  candidates.sort((a, b) => b.score - a.score);
  return candidates.slice(0, 8).filter((c) => c.score > 0.15);
}
