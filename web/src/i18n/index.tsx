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
  'nav.account': { zh: '账户菜单', en: 'Account menu' },

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
  'search.detectedEntities': { zh: '检测到实体:', en: 'Detected entities:' },
  'search.footer': {
    zh: '共 {total} 条结果 · 用时 {ms} ms · 多路融合（visual / text / metadata）',
    en: '{total} results · {ms} ms · fused (visual / text / metadata)',
  },

  // ---- drive / explorer ----
  'drive.root': { zh: '我的网盘', en: 'My Drive' },
  'drive.newFolder': { zh: '新建文件夹', en: 'New folder' },
  'drive.upload': { zh: '上传', en: 'Upload' },
  'drive.uploadTo': { zh: '上传到 {path}', en: 'Upload to {path}' },
  'drive.grid': { zh: '网格', en: 'Grid' },
  'drive.list': { zh: '列表', en: 'List' },
  'drive.colName': { zh: '名称', en: 'Name' },
  'drive.colSize': { zh: '大小', en: 'Size' },
  'drive.colModified': { zh: '修改时间', en: 'Modified' },
  'drive.colType': { zh: '类型', en: 'Type' },
  'drive.folder': { zh: '文件夹', en: 'Folder' },
  'drive.untitledFolder': { zh: '未命名文件夹', en: 'Untitled folder' },
  'drive.folderName': { zh: '文件夹名', en: 'Folder name' },
  'drive.now': { zh: '现在', en: 'now' },
  'drive.uploading': { zh: '上传中', en: 'Uploading' },
  'drive.itemsN': { zh: '{n} 项', en: '{n} items' },
  'drive.emptyTitle': { zh: '{name} 是空的', en: '{name} is empty' },
  'drive.emptyHint': { zh: '把文件拖进来，或新建一个子文件夹', en: 'Drop files here, or create a subfolder' },
  'drive.selectedN': { zh: '已选 {n} 项', en: '{n} selected' },
  'drive.breadcrumb': { zh: '面包屑', en: 'Breadcrumb' },
  'drive.expand': { zh: '展开', en: 'Expand' },
  'drive.collapse': { zh: '收起', en: 'Collapse' },
  // context-menu / actions
  'action.open': { zh: '打开', en: 'Open' },
  'action.rename': { zh: '重命名', en: 'Rename' },
  'action.confirm': { zh: '确认', en: 'Confirm' },
  'action.cancel': { zh: '取消', en: 'Cancel' },
  'action.close': { zh: '关闭', en: 'Close' },
  'action.home': { zh: '回到首页', en: 'Back home' },
  // delete dialogs
  'drive.deleteNTitle': { zh: '删除 {n} 项？', en: 'Delete {n} items?' },
  'drive.deleteFolderDesc': { zh: '文件夹及其中所有文件会被永久删除，且不可恢复。', en: 'The folder and all files inside are permanently deleted and cannot be recovered.' },
  'drive.deleteFilesDesc': { zh: '所选文件会被永久删除，且不可恢复。', en: 'The selected files are permanently deleted and cannot be recovered.' },

  // ---- file kinds ----
  'kind.image': { zh: '图片', en: 'Image' },
  'kind.audio': { zh: '音频', en: 'Audio' },
  'kind.video': { zh: '视频', en: 'Video' },
  'kind.doc': { zh: '文档', en: 'Document' },
  'kind.text': { zh: '文本', en: 'Text' },
  'kind.other': { zh: '其它', en: 'Other' },

  // ---- index status ----
  'status.pending': { zh: '等待', en: 'Pending' },
  'status.processing': { zh: '处理中', en: 'Processing' },
  'status.done': { zh: '已就绪', en: 'Ready' },
  'status.failed': { zh: '失败', en: 'Failed' },

  // ---- relative time ----
  'time.justNow': { zh: '刚刚', en: 'just now' },
  'time.minutesAgo': { zh: '{n} 分钟前', en: '{n} min ago' },
  'time.hoursAgo': { zh: '{n} 小时前', en: '{n} h ago' },
  'time.daysAgo': { zh: '{n} 天前', en: '{n} d ago' },

  // ---- related types ----
  'related.same_topic': { zh: '同主题', en: 'Same topic' },
  'related.same_person': { zh: '同人', en: 'Same person' },
  'related.same_event': { zh: '同事件', en: 'Same event' },
  'related.sequel': { zh: '续作', en: 'Sequel' },

  // ---- file detail (remaining) ----
  'detail.notFoundTitle': { zh: '文件不存在', en: 'File not found' },
  'detail.notFoundDesc': { zh: '可能已删除，或 file_id 错了。', en: 'It may have been deleted, or the id is wrong.' },
  'detail.geo': { zh: '经纬', en: 'Geo' },
  'detail.autoTags': { zh: '自动标签', en: 'Auto tags' },
  'detail.entities': { zh: '识别到的实体', en: 'Detected entities' },
  'detail.deleteTitle': { zh: '删除该文件？', en: 'Delete this file?' },
  'detail.deleteDesc': { zh: '将永久删除 “{name}” 及其所有 AI 索引（caption / embeddings / 人脸聚类）。此操作不可恢复。', en: 'Permanently deletes "{name}" and all its AI index (caption / embeddings / face clusters). This cannot be undone.' },

  // ---- toasts ----
  'toast.uploaded': { zh: '已上传 {n} 个文件到 {path}', en: 'Uploaded {n} files to {path}' },
  'toast.uploadedDesc': { zh: 'AI 索引会在后台异步处理', en: 'AI indexing runs in the background' },
  'toast.uploadFailed': { zh: '上传失败', en: 'Upload failed' },
  'toast.retryLater': { zh: '请稍后重试', en: 'Please try again later' },
  'toast.movedN': { zh: '已移动 {n} 项到 {path}', en: 'Moved {n} items to {path}' },
  'toast.moveFailed': { zh: '移动失败', en: 'Move failed' },
  'toast.noFolderIntoItself': { zh: '不能把文件夹拖到自己里面', en: "Can't move a folder into itself" },
  'toast.renamedFolder': { zh: '已重命名文件夹', en: 'Folder renamed' },
  'toast.renamed': { zh: '已重命名', en: 'Renamed' },
  'toast.renameFailed': { zh: '重命名失败', en: 'Rename failed' },
  'toast.createdFolder': { zh: '新建文件夹 “{name}”', en: 'Created folder "{name}"' },
  'toast.createFailed': { zh: '新建失败', en: 'Create failed' },
  'toast.deleted': { zh: '已删除', en: 'Deleted' },
  'toast.deleteFailed': { zh: '删除失败', en: 'Delete failed' },
  'toast.downloadFailed': { zh: '下载失败', en: 'Download failed' },
  'toast.copiedId': { zh: '已复制 file_id', en: 'Copied file id' },
  'toast.accountCreated': { zh: '账号已创建', en: 'Account created' },

  // ---- login ----
  'login.tagline': { zh: 'Agent-Native AI 网盘', en: 'Agent-Native AI drive' },
  'login.signIn': { zh: '登录', en: 'Sign in' },
  'login.signUp': { zh: '注册', en: 'Sign up' },
  'login.email': { zh: '邮箱', en: 'Email' },
  'login.password': { zh: '密码', en: 'Password' },
  'login.passwordHint': { zh: '至少 6 位', en: 'At least 6 characters' },
  'login.createAccount': { zh: '创建账号', en: 'Create account' },
  'login.haveAccount': { zh: '已有账号？', en: 'Already have an account?' },
  'login.goSignIn': { zh: '去登录', en: 'Sign in' },
  'login.noAccount': { zh: '还没有账号？', en: "Don't have an account?" },
  'login.goSignUp': { zh: '立即注册', en: 'Sign up' },
  'login.signInFailed': { zh: '登录失败', en: 'Sign in failed' },
  'login.signUpFailed': { zh: '注册失败', en: 'Sign up failed' },
  'login.footer': { zh: '自部署版本 · 数据全在本地 · Apache-2.0', en: 'Self-hosted · all data stays local · Apache-2.0' },

  // ---- 404 ----
  'notFound.title': { zh: '404 · 这里什么也没有', en: '404 · Nothing here' },
  'notFound.desc': { zh: '可能链接过期了，或者你需要先登录。', en: 'The link may have expired, or you need to sign in first.' },
};

function lookup(key: string, lang: Lang): string {
  const entry = dict[key];
  if (!entry) return key;
  return entry[lang] ?? entry.en ?? key;
}

// Module-level mirror of the active language, kept in sync by I18nProvider.
// Lets non-React code (formatters, pure helpers) translate via tt() without a
// hook. React components should still use useT() so they re-render on change.
let currentLang: Lang = 'en';
export function getLang(): Lang {
  return currentLang;
}
export function tt(key: string, vars?: Record<string, string | number>): string {
  return interpolate(lookup(key, currentLang), vars);
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
  const [lang, setLangState] = React.useState<Lang>(() => {
    const l = detectInitial();
    currentLang = l;
    return l;
  });
  const setLang = React.useCallback((l: Lang) => {
    currentLang = l;
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
