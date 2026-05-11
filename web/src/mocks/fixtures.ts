/**
 * Deterministic mock fixtures. We seed a tiny PRNG so the same dataset shows
 * up every reload — that makes the demo feel polished. ~80 files spanning
 * 2010-2024, mixed images / docs / audio, plus 5 face clusters.
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
  // Stable-ish pseudo-uuid (deterministic via PRNG)
  const hex = '0123456789abcdef';
  let out = '';
  for (let i = 0; i < 32; i++) {
    out += hex[Math.floor(rand() * 16)];
    if (i === 7 || i === 11 || i === 15 || i === 19) out += '-';
  }
  return out;
}

// ---------- Entities (face clusters) ----------
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

// ---------- Captions / topics ----------
const IMAGE_THEMES = [
  { caption: '草地上的金毛犬在追飞盘，午后阳光斜射', tags: ['宠物', '金毛', '户外'] },
  { caption: '云南大理苍山下的客栈庭院，三角梅开得正盛', tags: ['旅行', '云南', '风景'] },
  { caption: '高中毕业那天的合影，操场背景，三个男生勾肩搭背', tags: ['毕业', '同学', '校园'] },
  { caption: '雪后的故宫角楼，红墙与白雪对比强烈', tags: ['北京', '故宫', '冬天'] },
  { caption: '生日蛋糕特写，奶油和草莓，蜡烛还没点燃', tags: ['生日', '美食'] },
  { caption: '咖啡馆里的笔记本电脑和拿铁拉花', tags: ['工作', '咖啡'] },
  { caption: '夜晚的城市天际线，长曝光下车流如河', tags: ['夜景', '城市'] },
  { caption: '海边日落，沙滩上有两道脚印', tags: ['旅行', '海边', '日落'] },
  { caption: '猫蜷在窗台上晒太阳，外面下着小雨', tags: ['宠物', '猫', '室内'] },
  { caption: '一桌火锅，毛肚和虾滑在翻滚', tags: ['美食', '火锅'] },
  { caption: '滑雪场山顶的自拍，护目镜上反射出蓝天', tags: ['滑雪', '冬天', '运动'] },
  { caption: '婚礼现场，新郎新娘交换戒指的瞬间', tags: ['婚礼', '人物'] },
  { caption: '机场登机口窗外的飞机，傍晚光线', tags: ['旅行', '机场'] },
  { caption: '老家院子里的桂花树，秋天开花了', tags: ['家', '植物', '秋天'] },
  { caption: '演唱会现场，舞台灯光打到台下', tags: ['演唱会', '音乐'] },
];

const DOC_TOPICS = [
  { name: '租房合同 2023.pdf', summary: '甲方房东老李，乙方本人，期限一年，月租 7800，二押一付。' },
  { name: 'RAG 调研笔记.md', summary: '对比 LangChain、LlamaIndex、Haystack；倾向自建检索 + reranker。' },
  { name: '体检报告.pdf', summary: '总体良好，建议关注尿酸偏高与维生素 D 偏低。' },
  { name: '会议纪要-Q3规划.docx', summary: '确认 Q3 重点：搜索召回率、人脸聚类、Web UI 上线。' },
  { name: '读书笔记《编码》.md', summary: '从电报到逻辑门，第 14 章和第 17 章值得回看。' },
  { name: '工作合同副本.pdf', summary: '入职日期 2022-03-01，试用期 6 个月，期权 vesting 4 年 1 cliff。' },
  { name: '云南行程单.md', summary: '昆明 → 大理 → 丽江 → 香格里拉，7 天，含住宿与车票。' },
  { name: '银行流水 2023.pdf', summary: '主要支出：房租 / 食物 / 旅行，年储蓄率约 38%。' },
];

const AUDIO_TOPICS = [
  { name: '播客-黄执中聊辩论.mp3', summary: '辩论本质是寻找共识空间，而非击败对方。' },
  { name: '语音备忘-地下室漏水.m4a', summary: '物业说会在周三上门检查，记得在家。' },
  { name: '钢琴练习-肖邦Op9No2.mp3', summary: '第二乐章节奏略快，左手力度还需控制。' },
];

const MIMES: Record<FileKind, string[]> = {
  image: ['image/jpeg', 'image/png', 'image/webp', 'image/heic'],
  doc: ['application/vnd.openxmlformats-officedocument.wordprocessingml.document', 'application/msword'],
  pdf: ['application/pdf'],
  text: ['text/markdown', 'text/plain'],
  audio: ['audio/mpeg', 'audio/x-m4a', 'audio/wav'],
  video: ['video/mp4'],
  other: ['application/octet-stream'],
};

const STATUS_DIST: IndexStatus[] = [
  // 70% done, 15% processing, 10% pending, 5% failed
  ...Array(70).fill('done'),
  ...Array(15).fill('processing'),
  ...Array(10).fill('pending'),
  ...Array(5).fill('failed'),
] as IndexStatus[];

function randomDate(yearMin = 2010, yearMax = 2024): string {
  const y = between(yearMin, yearMax);
  const m = between(1, 12);
  const d = between(1, 28);
  const hh = between(0, 23);
  const mm = between(0, 59);
  return new Date(Date.UTC(y, m - 1, d, hh, mm)).toISOString();
}

function picsum(seed: string, w = 800, h = 600): string {
  // Picsum is deterministic per seed — great for stable mocks.
  return `https://picsum.photos/seed/${encodeURIComponent(seed)}/${w}/${h}`;
}

function pickEntitiesFor(theme: { tags: string[]; caption: string }): Entity[] {
  const out: Entity[] = [];
  // ~40% of images contain a known person; spread between clusters.
  if (rand() < 0.45) out.push(pick([ENTITIES[0]!, ENTITIES[1]!, ENTITIES[2]!, ENTITIES[3]!, ENTITIES[4]!]));
  if (rand() < 0.18) out.push(ENTITIES[2]!); // 妈妈
  if (theme.caption.includes('云南')) out.push(ENTITIES[5]!);
  if (theme.caption.includes('北京') || theme.caption.includes('故宫')) out.push(ENTITIES[6]!);
  if (theme.caption.includes('毕业')) out.push(ENTITIES[7]!);
  // dedupe
  return Array.from(new Map(out.map((e) => [e.id, e])).values());
}

function makeImage(idx: number): MemFile {
  const theme = pick(IMAGE_THEMES);
  const id = uuid();
  const status = pick(STATUS_DIST);
  const ts = randomDate();
  const name = `IMG_${String(1000 + idx).padStart(4, '0')}.jpg`;
  const ents = pickEntitiesFor(theme);
  const personNames = ents.filter((e) => e.type === 'person').map((e) => e.name);
  return {
    id,
    user_id: 'user-1',
    name,
    path: `/Photos/${ts.slice(0, 7)}/${name}`,
    size: between(800_000, 6_500_000),
    sha256: id.replace(/-/g, '') + '0'.repeat(32),
    mime: pick(MIMES.image),
    storage_key: `s3://mem/photos/${id}`,
    summary: null,
    caption: status === 'done' ? theme.caption : null,
    tags: status === 'done' ? [...theme.tags, ...personNames] : [],
    timeline_at: ts,
    geo: theme.caption.includes('云南')
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

function makeDoc(idx: number): MemFile {
  const topic = pick(DOC_TOPICS);
  const id = uuid();
  const isPdf = topic.name.endsWith('.pdf');
  const isMd = topic.name.endsWith('.md');
  const kind: FileKind = isPdf ? 'pdf' : isMd ? 'text' : 'doc';
  const mime = isPdf
    ? 'application/pdf'
    : isMd
    ? 'text/markdown'
    : pick(MIMES.doc);
  const status = pick(STATUS_DIST);
  const ts = randomDate(2020, 2024);
  return {
    id,
    user_id: 'user-1',
    name: `${topic.name.replace(/(\.[a-z0-9]+)$/i, '')}-${idx}${topic.name.match(/\.[a-z0-9]+$/i)?.[0] ?? ''}`,
    path: `/Docs/${ts.slice(0, 4)}/${topic.name}`,
    size: between(20_000, 2_500_000),
    sha256: id.replace(/-/g, '') + '0'.repeat(32),
    mime,
    storage_key: `s3://mem/docs/${id}`,
    summary: status === 'done' ? topic.summary : null,
    caption: null,
    tags: status === 'done' ? ['文档'] : [],
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

function makeAudio(idx: number): MemFile {
  const topic = pick(AUDIO_TOPICS);
  const id = uuid();
  const status = pick(STATUS_DIST);
  const ts = randomDate(2021, 2024);
  return {
    id,
    user_id: 'user-1',
    name: topic.name.replace(/(\.[a-z0-9]+)$/i, `-${idx}$1`),
    path: `/Audio/${topic.name}`,
    size: between(1_000_000, 30_000_000),
    sha256: id.replace(/-/g, '') + '0'.repeat(32),
    mime: pick(MIMES.audio),
    storage_key: `s3://mem/audio/${id}`,
    summary: status === 'done' ? topic.summary : null,
    caption: null,
    tags: status === 'done' ? ['音频'] : [],
    timeline_at: ts,
    geo: null,
    index_status: status,
    created_at: ts,
    updated_at: ts,
    kind: 'audio',
    preview_url: null,
    thumbnail_url: null,
    download_url: `#/mock/${id}`,
    entities: [],
  };
}

// ---------- Build the dataset ----------
function buildDataset(): MemFile[] {
  const files: MemFile[] = [];
  for (let i = 0; i < 60; i++) files.push(makeImage(i));
  for (let i = 0; i < 14; i++) files.push(makeDoc(i));
  for (let i = 0; i < 6; i++) files.push(makeAudio(i));
  // Pin a "hero" demo file: 2012 云南 with 小明 — the spec's killer demo.
  const hero: MemFile = {
    id: 'hero-2012-yunnan-xiaoming',
    user_id: 'user-1',
    name: 'IMG_2012_DALI_0427.jpg',
    path: '/Photos/2012-07/IMG_2012_DALI_0427.jpg',
    size: 3_842_112,
    sha256: 'a'.repeat(64),
    mime: 'image/jpeg',
    storage_key: 's3://mem/photos/hero',
    summary: null,
    caption: '2012 年夏天，云南大理洱海边，和小明的合影。蓝天白云，远处有海鸥。',
    tags: ['旅行', '云南', '大理', '小明', '高中', '2012'],
    timeline_at: '2012-07-14T15:22:00Z',
    geo: { lat: 25.706, lon: 100.18 },
    index_status: 'done',
    created_at: '2012-07-14T15:22:00Z',
    updated_at: '2026-04-01T08:00:00Z',
    kind: 'image',
    preview_url: picsum('hero-yunnan-2012', 1600, 1066),
    thumbnail_url: picsum('hero-yunnan-2012', 480, 320),
    download_url: picsum('hero-yunnan-2012', 2400, 1600),
    entities: [ENTITIES[0]!, ENTITIES[5]!], // 小明 + 云南
  };
  files.unshift(hero);
  // Sort: latest first, but recently-uploaded files (now) bubble to top.
  // We'll fake "uploaded recently" by tweaking the first 5 created_at.
  const nowMinusMins = (m: number) => new Date(Date.now() - m * 60 * 1000).toISOString();
  for (let i = 0; i < 5; i++) {
    const f = files[i + 1];
    if (f) f.created_at = nowMinusMins(i * 7 + 2);
  }
  return files.sort((a, b) => (a.created_at < b.created_at ? 1 : -1));
}

export const FILES: MemFile[] = buildDataset();

export function findFile(id: string): MemFile | undefined {
  return FILES.find((f) => f.id === id);
}

// ---------- Search / scoring (cheap relevance) ----------
function tokenize(s: string): string[] {
  return s
    .toLowerCase()
    .replace(/[，。！？、,.!?]/g, ' ')
    .split(/\s+/)
    .filter(Boolean);
}

export function searchFiles(opts: {
  q: string;
  type?: string;
  since?: string;
  until?: string;
  face?: string;
  limit?: number;
}): { results: { file: MemFile; score: number; snippet: string | null; channel: 'visual' | 'text' | 'metadata' | 'fused' }[]; total: number } {
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
    pool = pool.filter((f) => f.entities?.some((e) => e.type === 'person' && e.name === opts.face));
  }
  const scored = pool.map((f) => {
    const hay = [
      f.name,
      f.caption ?? '',
      f.summary ?? '',
      ...f.tags,
      ...(f.entities ?? []).map((e) => e.name),
      f.timeline_at?.slice(0, 4) ?? '',
      f.geo ? '地理' : '',
    ]
      .join(' ')
      .toLowerCase();
    let score = 0;
    for (const tok of tokens) {
      if (hay.includes(tok)) score += 1;
    }
    // freshness bonus
    score += 1 / (1 + (Date.now() - new Date(f.created_at).getTime()) / (365 * 24 * 3600 * 1000));
    // small jitter for visual variety
    score += rand() * 0.05;
    const channel: 'visual' | 'text' | 'metadata' | 'fused' =
      f.kind === 'image' ? 'visual' : f.kind === 'audio' ? 'metadata' : 'text';
    const snippet =
      f.caption ??
      f.summary ??
      (f.tags.length ? `标签：${f.tags.join('、')}` : null);
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
    if (x.name.replace(/\d+/g, '') === f.name.replace(/\d+/g, '')) {
      relation = '续作';
      score += 0.3;
    }
    return { file: x, relation, score };
  });
  candidates.sort((a, b) => b.score - a.score);
  return candidates.slice(0, 8).filter((c) => c.score > 0.15);
}
