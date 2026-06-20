/**
 * Lightweight, dependency-free i18n.
 *
 * Usage:
 *   const { t, lang, setLang } = useT();
 *   <span>{t('nav.drive')}</span>
 *   t('detail.relatedCount', { n: 5 })   // "5 related" / "5 个相关"
 *
 * Add a language by extending `dict` with a new column. Missing keys fall back
 * to English, then to the key itself, so a half-translated string is visible
 * rather than blank.
 */
import * as React from 'react';

export type Lang = 'zh' | 'en';
export const LANGS: { code: Lang; label: string }[] = [
  { code: 'zh', label: '中文' },
  { code: 'en', label: 'English' },
];

// Flat key → per-language string. `{n}` placeholders are interpolated.
const dict: Record<string, Partial<Record<Lang, string>>> = {
  // ---- nav / chrome ----
  'nav.drive': { zh: '网盘', en: 'Drive' },
  'nav.ask': { zh: '问答', en: 'Ask' },
  'nav.faces': { zh: '人脸', en: 'Faces' },
  'nav.providers': { zh: '模型', en: 'Providers' },
  'nav.searchPlaceholder': { zh: '搜索你的网盘…', en: 'Search your drive…' },
  'nav.logout': { zh: '退出登录', en: 'Sign out' },
  'nav.notSignedIn': { zh: '未登录', en: 'Not signed in' },
  'nav.language': { zh: '语言', en: 'Language' },

  // ---- common actions ----
  'common.back': { zh: '返回', en: 'Back' },
  'common.backToDrive': { zh: '返回云盘', en: 'Back to drive' },
  'common.download': { zh: '下载', en: 'Download' },
  'common.delete': { zh: '删除', en: 'Delete' },
  'common.copyId': { zh: '复制 ID', en: 'Copy ID' },
  'common.clear': { zh: '清空', en: 'Clear' },
  'common.recent': { zh: '最近', en: 'Recent' },

  // ---- file detail ----
  'detail.status': { zh: '状态', en: 'Status' },
  'detail.ready': { zh: '已就绪', en: 'Ready' },
  'detail.size': { zh: '大小', en: 'Size' },
  'detail.timeAnchor': { zh: '时间锚点', en: 'Time anchor' },
  'detail.ingestedAt': { zh: '入库时间', en: 'Indexed at' },
  'detail.aiInsights': { zh: 'AI 洞察', en: 'AI insights' },
  'detail.summary': { zh: '摘要', en: 'Summary' },
  'detail.captionVlm': { zh: '图像描述 (VLM)', en: 'Caption (VLM)' },
  'detail.aiProcessing': {
    zh: 'AI 还在处理中，稍等一下就能看到 caption 和标签。',
    en: 'AI is still processing — caption and tags will appear shortly.',
  },
  'detail.relatedFiles': { zh: '相关文件', en: 'Related files' },
  'detail.sameTopic': { zh: '同主题', en: 'Same topic' },
  'detail.sameEvent': { zh: '同事件', en: 'Same event' },
  'detail.unsupported': { zh: '无法直接预览此文件类型', en: "Can't preview this file type" },
  'detail.downloadToView': { zh: '下载查看', en: 'Download to view' },
  'detail.loadingAudio': { zh: '加载音频…', en: 'Loading audio…' },
  'detail.loadingVideo': { zh: '加载视频…', en: 'Loading video…' },

  // ---- ask ----
  'ask.title': { zh: '问答', en: 'Ask' },
  'ask.subtitle': {
    zh: '提个问题；mem 会检索最相关的片段并综合出带出处的答案。',
    en: 'Ask a question; mem retrieves the most relevant snippets and synthesizes an answer with sources.',
  },
  'ask.placeholder': {
    zh: '问任何关于你已索引文件的问题…',
    en: 'Ask anything about your indexed files…',
  },
  'ask.run': { zh: '提问', en: 'Ask' },
  'ask.try': { zh: '试试:', en: 'Try:' },
  'ask.execution': { zh: '执行过程', en: 'Execution' },
  'ask.running': { zh: '执行中…', en: 'Running…' },
  'ask.inProgress': { zh: '进行中', en: 'in progress' },
  'ask.stageRetrieve': { zh: '向量检索', en: 'Vector retrieval' },
  'ask.stageRetrieveHint': {
    zh: '把问题编码成向量，从 pgvector 召回最相关片段',
    en: 'Embed the question and pull the most relevant snippets from pgvector',
  },
  'ask.stageGenerate': { zh: '生成答案', en: 'Generate answer' },
  'ask.stageGenerateHint': {
    zh: 'LLM 基于片段推理并作答（会先思考）',
    en: 'The LLM reasons over the snippets and answers (it thinks first)',
  },
  'ask.thinkingNote': {
    zh: '推理模型会先思考再作答，单次约 15–30 秒。',
    en: 'A reasoning model thinks before answering — about 15–30s per question.',
  },
  'ask.thinking': { zh: '思考过程', en: 'Thinking' },
  'ask.thinkingChars': { zh: '{n} 字', en: '{n} chars' },
  'ask.sources': { zh: '出处', en: 'Sources' },
  'ask.emptyAnswer': { zh: '（空答案）', en: '(empty answer)' },
  'ask.pressAsk': { zh: '点击提问', en: 'Press Ask' },

  // ---- search ----
  'search.title': { zh: '搜索', en: 'Search' },
  'search.subtitle': { zh: '用自然语言找回任何东西。', en: 'Find anything in natural language.' },
  'search.placeholder': {
    zh: '搜索图片、文档… 比如 “2012 年在云南拍的照片”',
    en: 'Search images, docs… e.g. "photos from Yunnan in 2012"',
  },
  'search.recent': { zh: '最近搜索:', en: 'Recent:' },
  'search.try': { zh: '试试搜:', en: 'Try:' },
  'search.filter': { zh: '过滤', en: 'Filter' },
  'search.typeAny': { zh: '全部', en: 'All' },
  'search.typeImage': { zh: '图片', en: 'Images' },
  'search.typeDoc': { zh: '文档', en: 'Docs' },
  'search.typeAudio': { zh: '音频', en: 'Audio' },
  'search.from': { zh: '自', en: 'From' },
  'search.to': { zh: '至', en: 'To' },
  'search.start': { zh: '开始搜索', en: 'Start searching' },
  'search.startHint': {
    zh: '语义 + 视觉多路召回。支持中文自然语言、按类型和时间过滤。',
    en: 'Semantic + visual recall. Natural language, with type and date filters.',
  },
  'search.noHits': { zh: '没有命中', en: 'No results' },
  'search.noHitsHint': {
    zh: '试试换个说法，或者把日期范围去掉。',
    en: 'Try rephrasing, or remove the date range.',
  },
  'search.visualResults': { zh: '视觉结果', en: 'Visual results' },
  'search.docsAudio': { zh: '文档与音频', en: 'Docs & audio' },
  'search.noImages': { zh: '这次搜索没有图片命中', en: 'No image hits this time' },
  'search.none': { zh: '无', en: 'None' },
};

function lookup(key: string, lang: Lang): string {
  const entry = dict[key];
  if (!entry) return key;
  return entry[lang] ?? entry.en ?? key;
}

function interpolate(s: string, vars?: Record<string, string | number>): string {
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (_, k) => String(vars[k] ?? `{${k}}`));
}

interface I18nCtx {
  lang: Lang;
  setLang: (l: Lang) => void;
  t: (key: string, vars?: Record<string, string | number>) => string;
}

const Ctx = React.createContext<I18nCtx | null>(null);
const STORAGE_KEY = 'mem.lang';

function detectInitial(): Lang {
  try {
    const saved = localStorage.getItem(STORAGE_KEY) as Lang | null;
    if (saved === 'zh' || saved === 'en') return saved;
  } catch {
    /* ignore */
  }
  const nav = typeof navigator !== 'undefined' ? navigator.language.toLowerCase() : 'en';
  return nav.startsWith('zh') ? 'zh' : 'en';
}

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [lang, setLangState] = React.useState<Lang>(detectInitial);
  const setLang = React.useCallback((l: Lang) => {
    setLangState(l);
    try {
      localStorage.setItem(STORAGE_KEY, l);
    } catch {
      /* ignore */
    }
    if (typeof document !== 'undefined') document.documentElement.lang = l;
  }, []);
  React.useEffect(() => {
    if (typeof document !== 'undefined') document.documentElement.lang = lang;
  }, [lang]);

  const t = React.useCallback(
    (key: string, vars?: Record<string, string | number>) => interpolate(lookup(key, lang), vars),
    [lang],
  );

  const value = React.useMemo(() => ({ lang, setLang, t }), [lang, setLang, t]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useT(): I18nCtx {
  const ctx = React.useContext(Ctx);
  if (!ctx) throw new Error('useT must be used within <I18nProvider>');
  return ctx;
}
