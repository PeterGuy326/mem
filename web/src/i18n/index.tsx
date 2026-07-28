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
  'nav.memories': { zh: '记忆', en: 'Memory' },
  'nav.tasks': { zh: '任务', en: 'Tasks' },
  'nav.transfer': { zh: '迁移', en: 'Transfer' },
  'nav.faces': { zh: '人脸', en: 'Faces' },
  'nav.providers': { zh: '索引模型', en: 'Index models' },
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
  'common.retry': { zh: '重试', en: 'Retry' },

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
  'search.failed': { zh: '检索暂时不可用', en: 'Search is temporarily unavailable' },
  'search.failedHint': {
    zh: '这不是“没有结果”。索引 Worker、模型或存储链路可能不可用，请稍后重试。',
    en: 'This is not an empty result. The indexing worker, model, or storage path may be unavailable; try again shortly.',
  },
  'search.channel.visual': { zh: '视觉', en: 'Visual' },
  'search.channel.text': { zh: '文本', en: 'Text' },
  'search.channel.metadata': { zh: '元数据', en: 'Metadata' },
  'search.channel.fused': { zh: '融合', en: 'Fused' },
  'search.visualResults': { zh: '视觉结果', en: 'Visual results' },
  'search.docsAudio': { zh: '文档与音频', en: 'Docs & audio' },
  'search.noImages': { zh: '这次搜索没有图片命中', en: 'No image hits this time' },
  'search.none': { zh: '无', en: 'None' },
  'search.detectedEntities': { zh: '检测到实体:', en: 'Detected entities:' },
  'search.people': { zh: '人物', en: 'People' },
  'people.unnamed': { zh: '未命名', en: 'Unnamed' },
  'people.namePlaceholder': { zh: '给 TA 起个名…', en: 'Name this person…' },
  'people.photosN': { zh: '{n} 张照片', en: '{n} photos' },
  'people.nameSaved': { zh: '已命名', en: 'Named' },
  'people.back': { zh: '返回人物', en: 'Back to people' },
  'people.hint': {
    zh: '点头像看 TA 的照片；给 TA 起名后可在搜索里找“和 XX 的合照”。',
    en: "Click a face to see their photos; name them to search 'photos with …'.",
  },
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
  'drive.emptyHint': {
    zh: '把文件拖进来，或新建一个子文件夹',
    en: 'Drop files here, or create a subfolder',
  },
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
  'drive.deleteFolderDesc': {
    zh: '文件夹及其中所有文件会被永久删除，且不可恢复。',
    en: 'The folder and all files inside are permanently deleted and cannot be recovered.',
  },
  'drive.deleteFilesDesc': {
    zh: '所选文件会被永久删除，且不可恢复。',
    en: 'The selected files are permanently deleted and cannot be recovered.',
  },

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
  'detail.notFoundDesc': {
    zh: '可能已删除，或 file_id 错了。',
    en: 'It may have been deleted, or the id is wrong.',
  },
  'detail.geo': { zh: '经纬', en: 'Geo' },
  'detail.autoTags': { zh: '自动标签', en: 'Auto tags' },
  'detail.entities': { zh: '识别到的实体', en: 'Detected entities' },
  'detail.deleteTitle': { zh: '删除该文件？', en: 'Delete this file?' },
  'detail.deleteDesc': {
    zh: '将永久删除 “{name}” 及其所有 AI 索引（caption / embeddings / 人脸聚类）。此操作不可恢复。',
    en: 'Permanently deletes "{name}" and all its AI index (caption / embeddings / face clusters). This cannot be undone.',
  },

  // ---- toasts ----
  'toast.uploaded': { zh: '已上传 {n} 个文件到 {path}', en: 'Uploaded {n} files to {path}' },
  'toast.uploadedDesc': { zh: 'AI 索引会在后台异步处理', en: 'AI indexing runs in the background' },
  'toast.uploadFailed': { zh: '上传失败', en: 'Upload failed' },
  'toast.retryLater': { zh: '请稍后重试', en: 'Please try again later' },
  'toast.movedN': { zh: '已移动 {n} 项到 {path}', en: 'Moved {n} items to {path}' },
  'toast.moveFailed': { zh: '移动失败', en: 'Move failed' },
  'toast.noFolderIntoItself': {
    zh: '不能把文件夹拖到自己里面',
    en: "Can't move a folder into itself",
  },
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
  'login.footer': {
    zh: '自部署版本 · 数据全在本地 · Apache-2.0',
    en: 'Self-hosted · all data stays local · Apache-2.0',
  },

  // ---- structured memory ledger ----
  'memory.title': { zh: '记忆账本', en: 'Memory ledger' },
  'memory.subtitle': {
    zh: '检查 Agent 写入的持久记忆、来源和反馈状态。这里是用户的信任与控制界面，不负责聊天或替 Agent 推理。',
    en: 'Inspect durable Agent memories, provenance, and feedback state. This is a human trust and control surface, not a chat or reasoning runtime.',
  },
  'memory.trustSurface': { zh: '用户可控的记忆平面', en: 'User-controlled memory plane' },
  'memory.filters': { zh: '记忆过滤条件', en: 'Memory filters' },
  'memory.scope': { zh: '路径范围', en: 'Path scope' },
  'memory.scopePlaceholder': { zh: '例如 /Projects/mem', en: 'e.g. /Projects/mem' },
  'memory.applyScope': { zh: '应用', en: 'Apply' },
  'memory.kind': { zh: '记忆类型', en: 'Memory kind' },
  'memory.allKinds': { zh: '全部类型', en: 'All kinds' },
  'memory.kind.observation': { zh: '观察', en: 'Observation' },
  'memory.kind.decision': { zh: '决定', en: 'Decision' },
  'memory.kind.preference': { zh: '偏好', en: 'Preference' },
  'memory.kind.task_state': { zh: '任务状态', en: 'Task state' },
  'memory.kind.fact': { zh: '事实', en: 'Fact' },
  'memory.kind.note': { zh: '笔记', en: 'Note' },
  'memory.kind.artifact': { zh: '产物', en: 'Artifact' },
  'memory.visibility': { zh: '生命周期', en: 'Lifecycle' },
  'memory.lifecycle.active': { zh: '生效', en: 'Active' },
  'memory.lifecycle.archived': { zh: '已归档', en: 'Archived' },
  'memory.lifecycle.all': { zh: '全部', en: 'All' },
  'memory.pinFilter': { zh: '置顶状态', en: 'Pin state' },
  'memory.pinAny': { zh: '全部', en: 'Any' },
  'memory.pinned': { zh: '已置顶', en: 'Pinned' },
  'memory.unpinned': { zh: '未置顶', en: 'Not pinned' },
  'memory.resetFilters': { zh: '重置过滤条件', en: 'Reset filters' },
  'memory.list': { zh: '记忆列表', en: 'Memory list' },
  'memory.auditLog': { zh: '可审计记录', en: 'Auditable records' },
  'memory.loadedCount': { zh: '已载入 {n} 条', en: '{n} loaded' },
  'memory.feedbackSummary': {
    zh: '反馈分 {score}，共 {count} 次',
    en: 'Feedback score {score} across {count} events',
  },
  'memory.humanOrUnknown': { zh: '用户 / 未声明', en: 'Human / unspecified' },
  'memory.loadMore': { zh: '载入更多', en: 'Load more' },
  'memory.detail': { zh: '记忆详情', en: 'Memory detail' },
  'memory.archivedNotice': {
    zh: '这条记忆已归档，默认不会进入 Agent 的召回上下文，但仍可检查和恢复。',
    en: 'This memory is archived. It is excluded from default Agent recall but remains inspectable and restorable.',
  },
  'memory.conflict': {
    zh: '状态已被另一个客户端更新。重新载入最新版本后再操作。',
    en: 'Another client changed this memory. Reload the latest version before acting.',
  },
  'memory.reload': { zh: '重新载入', en: 'Reload' },
  'memory.writePermissionRequired': {
    zh: '当前令牌缺少 write 权限',
    en: 'The current token lacks write permission',
  },
  'memory.deletePermissionRequired': {
    zh: '清除当前记忆需要 delete 权限及 owner/admin 角色',
    en: 'Clearing live memory requires delete permission and an owner/admin role',
  },
  'memory.pin': { zh: '置顶', en: 'Pin' },
  'memory.unpin': { zh: '取消置顶', en: 'Unpin' },
  'memory.useful': { zh: '有用', en: 'Useful' },
  'memory.notUseful': { zh: '无用', en: 'Not useful' },
  'memory.archive': { zh: '归档', en: 'Archive' },
  'memory.restore': { zh: '恢复', en: 'Restore' },
  'memory.forget': { zh: '遗忘当前记忆', en: 'Forget live memory' },
  'memory.actionSuccess.pin': { zh: '记忆已置顶', en: 'Memory pinned' },
  'memory.actionSuccess.unpin': { zh: '已取消置顶', en: 'Memory unpinned' },
  'memory.actionSuccess.useful': { zh: '已记录“有用”反馈', en: 'Useful feedback recorded' },
  'memory.actionSuccess.not_useful': { zh: '已记录“无用”反馈', en: 'Not-useful feedback recorded' },
  'memory.actionSuccess.archive': { zh: '记忆已归档', en: 'Memory archived' },
  'memory.actionSuccess.restore': { zh: '记忆已恢复', en: 'Memory restored' },
  'memory.actionSuccess.forget': {
    zh: '当前记忆 payload 已不可逆清除',
    en: 'The live memory payload was irreversibly cleared',
  },
  'memory.provenance': { zh: '来源与写入者', en: 'Provenance & producer' },
  'memory.sourceType': { zh: '来源类型', en: 'Source type' },
  'memory.eventAt': { zh: '事件时间', en: 'Event time' },
  'memory.agent': { zh: 'Agent', en: 'Agent' },
  'memory.session': { zh: '会话', en: 'Session' },
  'memory.task': { zh: '任务', en: 'Task' },
  'memory.createdBy': { zh: '创建用户', en: 'Created by' },
  'memory.sourceFile': { zh: '来源文件', en: 'Source file' },
  'memory.noSourceFile': { zh: '没有关联原始文件', en: 'No linked source file' },
  'memory.sourceHash': { zh: '来源文件哈希', en: 'Source file hash' },
  'memory.sourceRef': { zh: '来源引用', en: 'Source reference' },
  'memory.locator': { zh: '原文定位', en: 'Source locator' },
  'memory.identity': { zh: '身份与完整性', en: 'Identity & integrity' },
  'memory.createdAt': { zh: '写入时间', en: 'Created at' },
  'memory.updatedAt': { zh: '状态更新时间', en: 'State updated at' },
  'memory.feedbackScore': { zh: '反馈分', en: 'Feedback score' },
  'memory.feedbackCount': { zh: '反馈次数', en: 'Feedback count' },
  'memory.memoryId': { zh: '记忆 ID', en: 'Memory ID' },
  'memory.workspaceId': { zh: '工作区 ID', en: 'Workspace ID' },
  'memory.citation': { zh: '稳定引用', en: 'Stable citation' },
  'memory.copyCitation': { zh: '复制', en: 'Copy' },
  'memory.citationCopied': { zh: '已复制记忆引用', en: 'Memory citation copied' },
  'memory.copyFailed': { zh: '复制失败', en: 'Copy failed' },
  'memory.attributes': { zh: '结构化属性', en: 'Structured attributes' },
  'memory.forgetTitle': {
    zh: '不可逆地清除当前记忆？',
    en: 'Irreversibly clear this live memory?',
  },
  'memory.forgetDescription': {
    zh: '这只会不可逆地清除当前系统中的 live memory payload（内容与来源字段）；稳定引用随后只返回已遗忘状态。此操作不会直接删除独立来源文件。',
    en: 'This irreversibly clears only the live memory payload in this system (content and provenance fields). Its stable citation will then resolve only to a forgotten state. It does not directly delete an independent source file.',
  },
  'memory.sourcePreserved': {
    zh: '独立来源文件不会被此操作删除；备份与副本仍遵循部署方的保留和清理策略。如需删除原件，请在网盘中单独操作。',
    en: 'This action does not delete an independent source file. Backups and replicas remain subject to the deployment retention and deletion policy. Remove the source separately from Drive if needed.',
  },
  'memory.forgetReason': { zh: '遗忘原因', en: 'Reason for forgetting' },
  'memory.forgetReason.user_request': { zh: '用户主动要求', en: 'User request' },
  'memory.forgetReason.incorrect': { zh: '内容不正确', en: 'Incorrect content' },
  'memory.forgetReason.sensitive': { zh: '敏感信息', en: 'Sensitive information' },
  'memory.forgetReason.expired': { zh: '已过期', en: 'Expired' },
  'memory.forgetReason.other': { zh: '其他', en: 'Other' },
  'memory.typeForget': {
    zh: '输入 {word} 以确认不可逆清除当前记忆',
    en: 'Type {word} to confirm irreversible clearing of the live memory',
  },
  'memory.confirmForget': { zh: '不可逆清除确认词', en: 'Irreversible clear confirmation' },
  'memory.forgetPermanently': { zh: '确认清除当前记忆', en: 'Clear live memory' },
  'memory.unavailable': { zh: '结构化记忆尚未启用', en: 'Structured memory is not enabled' },
  'memory.unavailableHint': {
    zh: '当前部署没有配置 Memory Plane。文件与任务账本仍然可用。',
    en: 'This deployment has not configured the Memory Plane. Drive and the task ledger remain available.',
  },
  'memory.readRequired': { zh: '缺少记忆读取权限', en: 'Memory read permission required' },
  'memory.readRequiredHint': {
    zh: '当前令牌没有 read 权限，无法查看结构化记忆。',
    en: 'The current token lacks read permission for structured memory.',
  },
  'memory.listFailed': { zh: '记忆账本加载失败', en: 'Could not load the memory ledger' },
  'memory.empty': { zh: '没有符合条件的记忆', en: 'No matching memories' },
  'memory.emptyHint': {
    zh: 'Agent 写入的记忆会出现在这里；也可以清除过滤条件查看其他生命周期。',
    en: 'Agent-written memories appear here. Clear filters to inspect other lifecycle states.',
  },
  'memory.selectTitle': { zh: '选择一条记忆进行检查', en: 'Select a memory to inspect' },
  'memory.selectHint': {
    zh: '详情会显示不可变内容、来源、Agent / 会话、引用和反馈状态。',
    en: 'Details show immutable content, provenance, Agent/session identity, citation, and feedback state.',
  },
  'memory.forgotten': { zh: '这条当前记忆已被遗忘', en: 'This live memory was forgotten' },
  'memory.forgottenHint': {
    zh: '载荷已清除且不会再进入召回；关联原始文件不受影响。',
    en: 'Its payload was cleared and it cannot be recalled. Any linked source file remains unchanged.',
  },
  'memory.detailUnavailable': { zh: '记忆不可用', en: 'Memory unavailable' },
  'memory.backToLedger': { zh: '返回记忆账本', en: 'Back to memory ledger' },

  // ---- portable workspace transfer ----
  'transfer.eyebrow': { zh: '可迁移 Agent 工作区', en: 'Portable Agent workspace' },
  'transfer.title': { zh: '工作区迁移', en: 'Workspace transfer' },
  'transfer.subtitle': {
    zh: '把文件、记忆与任务账本封装成可验证的 .membundle，在 Claude Code、Codex 或另一台电脑上恢复同一个 Agent 工作区。',
    en: 'Seal files, memories, and task ledgers into a verifiable .membundle, then restore the same Agent workspace in Claude Code, Codex, or on another computer.',
  },
  'transfer.evidenceLedger': { zh: '可验证迁移账本', en: 'Verifiable transfer ledger' },
  'transfer.unavailable': { zh: '工作区迁移尚未启用', en: 'Workspace transfer is not enabled' },
  'transfer.unavailableHint': {
    zh: '当前部署没有配置工作区导入/导出服务。网盘、搜索、记忆和任务仍可继续使用。',
    en: 'This deployment has not configured workspace import/export. Drive, search, memory, and tasks remain available.',
  },
  'transfer.gate.permission.title': {
    zh: '当前令牌没有迁移权限',
    en: 'Transfer permission required',
  },
  'transfer.gate.permission.description': {
    zh: '工作区迁移需要 owner/admin 角色、不受路径限制的令牌，以及相应的 admin + read/write scope。',
    en: 'Workspace transfer requires an owner/admin role, an unrestricted token, and the matching admin + read/write scopes.',
  },
  'transfer.gate.unsupported.title': {
    zh: '当前部署不支持此迁移操作',
    en: 'This transfer operation is unsupported',
  },
  'transfer.gate.unsupported.description': {
    zh: '服务端必须显式声明 workspace transfer、bundle schema v1，以及导入所需的 fresh 恢复模式。',
    en: 'The server must advertise workspace transfer, bundle schema v1, and—when importing—the fresh restore mode.',
  },
  'transfer.contract.title': { zh: '迁移契约', en: 'Transfer contract' },
  'transfer.contract.schema': { zh: '归档协议', en: 'Archive contract' },
  'transfer.contract.mode': { zh: '恢复模式', en: 'Restore mode' },
  'transfer.contract.target': { zh: '目标要求', en: 'Target requirement' },
  'transfer.contract.merge': { zh: '合并策略', en: 'Merge policy' },
  'transfer.contract.emptyOnly': { zh: '仅空工作区', en: 'empty workspace only' },
  'transfer.contract.noMerge': { zh: '不支持合并', en: 'merge unavailable' },
  'transfer.contract.unavailable': { zh: '未声明', en: 'not advertised' },
  'transfer.contract.atomic': { zh: '校验后原子提交', en: 'validate, then commit atomically' },
  'transfer.export.eyebrow': { zh: '步骤 01 · 封装', en: 'Step 01 · Seal' },
  'transfer.export.title': { zh: '导出当前工作区', en: 'Export current workspace' },
  'transfer.export.description': {
    zh: '服务端先构建并验证完整归档。浏览器仅在状态成功、MIME 为 v1 且归档非空后才会保存文件。',
    en: 'The server builds and validates the complete archive first. The browser saves it only after a successful status, a v1 MIME check, and a non-empty body.',
  },
  'transfer.export.contentsTitle': { zh: '归档包含', en: 'Archive contents' },
  'transfer.export.contents': {
    zh: '文件夹、文件与内容 blob、结构化记忆及事件、任务、检查点、引用和检查点 payload。',
    en: 'Folders, files and content blobs, structured memories and events, tasks, checkpoints, references, and checkpoint payloads.',
  },
  'transfer.export.exclusions': {
    zh: '不会携带登录凭证、令牌、Provider 密钥、运行环境或可重建的派生索引。',
    en: 'Credentials, tokens, provider secrets, runtime environment, and rebuildable derived indexes are excluded.',
  },
  'transfer.export.action': { zh: '生成并下载归档', en: 'Build and download archive' },
  'transfer.export.preparing': { zh: '正在构建归档…', en: 'Building archive…' },
  'transfer.export.safeDownload': {
    zh: '错误响应绝不会作为 .membundle 保存；不安全的服务端文件名会被清理或替换。',
    en: 'Error responses are never saved as .membundle files. Unsafe server filenames are sanitized or replaced.',
  },
  'transfer.export.success': {
    zh: '归档已验证并交给浏览器下载',
    en: 'Archive verified and sent to downloads',
  },
  'transfer.import.eyebrow': { zh: '步骤 02 · 恢复', en: 'Step 02 · Restore' },
  'transfer.import.title': { zh: '恢复到当前工作区', en: 'Restore into current workspace' },
  'transfer.import.description': {
    zh: '上传一个 .membundle v1 原始归档。服务端会检查结构、完整性、依赖和冲突，全部通过后才写入。',
    en: 'Upload one raw .membundle v1 archive. The server checks structure, integrity, dependencies, and conflicts before writing anything.',
  },
  'transfer.import.freshTitle': { zh: '仅支持 fresh 恢复', en: 'Fresh restore only' },
  'transfer.import.freshDescription': {
    zh: '目标工作区必须没有可迁移数据。不会覆盖或合并现有内容；发现任何冲突时整次导入不会提交。',
    en: 'The target workspace must contain no portable data. Existing content is never overwritten or merged; any conflict prevents the entire import from committing.',
  },
  'transfer.import.choose': { zh: '选择或拖入 .membundle', en: 'Choose or drop a .membundle' },
  'transfer.import.fileHint': {
    zh: '仅接受 .membundle；已报告的 MIME 必须匹配 workspace bundle v1。',
    en: 'Only .membundle files are accepted; any reported MIME must match workspace bundle v1.',
  },
  'transfer.import.mimeNotReported': {
    zh: '浏览器未报告 MIME',
    en: 'MIME not reported by browser',
  },
  'transfer.import.removeFile': { zh: '移除所选归档', en: 'Remove selected archive' },
  'transfer.import.fileIssue.extension': {
    zh: '请选择扩展名为 .membundle 的工作区归档。',
    en: 'Choose a workspace archive with the .membundle extension.',
  },
  'transfer.import.fileIssue.mime': {
    zh: '该文件报告了不兼容的 MIME；请选择 workspace bundle v1。',
    en: 'This file reports an incompatible MIME. Choose a workspace bundle v1.',
  },
  'transfer.import.fileIssue.empty': {
    zh: '归档为空，无法导入。',
    en: 'The selected archive is empty and cannot be imported.',
  },
  'transfer.import.confirm': {
    zh: '我已确认当前目标工作区应为空，并理解本次操作只使用 fresh 模式、不执行合并或覆盖。',
    en: 'I confirm the current target workspace should be empty and understand this uses fresh mode only, with no merge or overwrite.',
  },
  'transfer.import.action': { zh: '校验并恢复工作区', en: 'Validate and restore workspace' },
  'transfer.import.uploading': { zh: '正在上传并校验…', en: 'Uploading and validating…' },
  'transfer.import.uploadingHint': {
    zh: '请保持此页面打开。归档必须完整上传并通过服务端校验后才会提交。',
    en: 'Keep this page open. The archive must finish uploading and pass server validation before it is committed.',
  },
  'transfer.import.success': { zh: '工作区已原子恢复', en: 'Workspace restored atomically' },
  'transfer.import.replayed': {
    zh: '该归档此前已成功导入',
    en: 'This archive was already imported successfully',
  },
  'transfer.import.committedBadge': { zh: '已提交', en: 'committed' },
  'transfer.import.replayBadge': { zh: '安全重放', en: 'safe replay' },
  'transfer.import.bundleId': { zh: 'Bundle ID', en: 'Bundle ID' },
  'transfer.import.sourceWorkspace': { zh: '来源工作区', en: 'Source workspace' },
  'transfer.import.archiveHash': { zh: '归档 SHA-256', en: 'Archive SHA-256' },
  'transfer.import.counts': { zh: '导入对象计数', en: 'Imported object counts' },
  'transfer.count.folders': { zh: '文件夹', en: 'Folders' },
  'transfer.count.files': { zh: '文件', en: 'Files' },
  'transfer.count.memories': { zh: '记忆', en: 'Memories' },
  'transfer.count.memory_events': { zh: '记忆事件', en: 'Memory events' },
  'transfer.count.tasks': { zh: '任务', en: 'Tasks' },
  'transfer.count.checkpoints': { zh: '检查点', en: 'Checkpoints' },
  'transfer.count.checkpoint_refs': { zh: '检查点引用', en: 'Checkpoint refs' },
  'transfer.count.checkpoint_payloads': { zh: '检查点载荷', en: 'Checkpoint payloads' },
  'transfer.count.blobs': { zh: '内容 Blob', en: 'Content blobs' },
  'transfer.count.blob_bytes': { zh: 'Blob 字节', en: 'Blob bytes' },
  'transfer.conflict.title': {
    zh: '目标工作区不为空，fresh 导入已拒绝',
    en: 'Fresh import rejected because the target is not empty',
  },
  'transfer.conflict.description': {
    zh: '未写入任何内容。请切换到一个新的空工作区，再重新上传同一归档。',
    en: 'Nothing was written. Switch to a new empty workspace, then upload the same archive again.',
  },
  'transfer.conflict.kind': { zh: '冲突类型', en: 'Conflict kind' },
  'transfer.conflict.resource': { zh: '资源', en: 'Resource' },
  'transfer.conflict.value': { zh: '值', en: 'Value' },
  'transfer.conflict.noDetails': {
    zh: '服务端没有返回可展示的冲突条目。',
    en: 'The server returned no displayable conflict entries.',
  },
  'transfer.conflict.truncatedWithTotal': {
    zh: '服务端限制了明细数量：已确认至少 {total} 项冲突，当前展示 {shown} 项。总数是已确认下界，可能还有更多。',
    en: 'Conflict details were capped: at least {total} conflicts are confirmed and {shown} are shown. The total is a confirmed lower bound; more may exist.',
  },
  'transfer.conflict.truncated': {
    zh: '服务端限制了明细数量，当前展示 {shown} 项；可能还有更多冲突。',
    en: 'Conflict details were capped at {shown} shown items; more conflicts may exist.',
  },
  'transfer.error.network.title': { zh: '网络连接中断', en: 'Network connection failed' },
  'transfer.error.network.description': {
    zh: '请求没有收到有效 HTTP 响应。检查网络或服务状态后重试。',
    en: 'The request did not receive a valid HTTP response. Check the network or service, then retry.',
  },
  'transfer.error.permission.title': {
    zh: '服务端拒绝了迁移请求',
    en: 'The server denied this transfer',
  },
  'transfer.error.permission.description': {
    zh: '当前会话可能缺少角色、scope 或不受路径限制的令牌。重新授权后再试。',
    en: 'The current session may lack the required role, scopes, or unrestricted token. Re-authorize and try again.',
  },
  'transfer.error.unsupported.title': {
    zh: '协议或响应不受支持',
    en: 'Unsupported protocol or response',
  },
  'transfer.error.unsupported.description': {
    zh: '归档版本、恢复模式、响应 MIME 或返回结构与 Web 支持的 v1 契约不匹配；未保存或提交任何数据。',
    en: 'The bundle version, restore mode, response MIME, or response shape does not match the v1 web contract. No data was saved or committed.',
  },
  'transfer.error.too_large.title': {
    zh: '归档超过上传限制',
    en: 'Archive exceeds the upload limit',
  },
  'transfer.error.too_large.description': {
    zh: '服务端以 413 拒绝该归档。请调整部署上传限制或缩小来源工作区后重试。',
    en: 'The server rejected this archive with 413. Adjust the deployment upload limit or reduce the source workspace, then retry.',
  },
  'transfer.error.invalid.title': { zh: '归档未通过校验', en: 'Archive validation failed' },
  'transfer.error.invalid.description': {
    zh: '400/415 表示文件为空、格式错误、完整性失败或 MIME 不匹配。请重新导出后再试。',
    en: 'A 400/415 means the file was empty, malformed, failed integrity checks, or used the wrong MIME. Export it again and retry.',
  },
  'transfer.error.server.title': {
    zh: '服务端无法完成迁移',
    en: 'The server could not complete the transfer',
  },
  'transfer.error.server.description': {
    zh: '服务端返回 5xx。目标不会留下部分导入；检查服务日志或存储容量后重试。',
    en: 'The server returned a 5xx. The target is not left partially imported; check service logs or storage capacity, then retry.',
  },
  'transfer.error.api.title': { zh: '迁移请求失败', en: 'Transfer request failed' },
  'transfer.error.api.description': {
    zh: '服务端返回了未预期的 API 状态。保留下面的错误代码并在条件恢复后重试。',
    en: 'The server returned an unexpected API status. Keep the error code below and retry when the condition clears.',
  },

  // ---- capabilities / task handoff gate ----
  'capabilities.failed': {
    zh: '无法读取当前工作区能力',
    en: 'Could not load workspace capabilities',
  },
  'capabilities.handoffUnavailable': { zh: '任务交接尚未启用', en: 'Task handoff is not enabled' },
  'capabilities.handoffUnavailableHint': {
    zh: '当前部署没有启用 handoff 服务。文件和搜索仍然可用。',
    en: 'This deployment has not enabled the handoff service. Files and search remain available.',
  },
  'capabilities.readRequired': { zh: '缺少任务读取权限', en: 'Task read permission required' },
  'capabilities.readRequiredHint': {
    zh: '当前令牌没有 read 权限，无法查看任务与检查点。',
    en: 'The current token lacks read permission for tasks and checkpoints.',
  },

  // ---- portable task ledger ----
  'task.title': { zh: '任务账本', en: 'Task ledger' },
  'task.subtitle': {
    zh: '查看 Agent 写入的目标、决定、产物与不可变检查点；在 Claude Code、Codex 和不同电脑之间接续同一任务。',
    en: 'Inspect Agent goals, decisions, artifacts, and immutable checkpoints; resume the same task across Claude Code, Codex, and devices.',
  },
  'task.portableLedger': { zh: '可迁移 Agent 状态', en: 'Portable Agent state' },
  'task.immutableLog': { zh: '不可变版本日志', en: 'Immutable version log' },
  'task.sequence': { zh: '序号', en: 'Seq' },
  'task.head': { zh: '当前', en: 'Head' },
  'task.headCheckpoint': { zh: '当前检查点', en: 'Head checkpoint' },
  'task.headSequence': { zh: '当前序号 #{n}', en: 'Head sequence #{n}' },
  'task.updated': { zh: '最后更新', en: 'Updated' },
  'task.taskCount': { zh: '{n} 个任务', en: '{n} tasks' },
  'task.completedCount': { zh: '已完成 {n} 项', en: '{n} completed' },
  'task.latestCount': { zh: '最近 {n} 个版本', en: 'Latest {n} revisions' },
  'task.loadFailed': { zh: '任务账本加载失败', en: 'Could not load the task ledger' },
  'task.empty': { zh: '还没有 Agent 任务', en: 'No Agent tasks yet' },
  'task.emptyHint': {
    zh: 'Agent 通过 mem_checkpoint 写入首个标准交接后，会在这里形成版本日志。',
    en: 'A version log appears here after an Agent writes its first standard handoff with mem_checkpoint.',
  },
  'task.noCheckpoints': { zh: '没有可见的检查点', en: 'No visible checkpoints' },
  'task.noCheckpointsHint': {
    zh: '任务可能还没有写入版本，或其 scope 不在当前令牌允许的路径内。',
    en: 'The task may have no revisions yet, or its scope is outside this token’s allowed paths.',
  },
  'task.backToTasks': { zh: '返回任务账本', en: 'Back to task ledger' },
  'task.backToTask': { zh: '返回任务', en: 'Back to task' },
  'task.taskKey': { zh: '任务键', en: 'Task key' },
  'task.timeline': { zh: '检查点时间线', en: 'Checkpoint timeline' },
  'task.goal': { zh: '目标', en: 'Goal' },
  'task.progress': { zh: '进展', en: 'Progress' },
  'task.decisions': { zh: '关键决定', en: 'Decisions' },
  'task.nextSteps': { zh: '下一步', en: 'Next steps' },
  'task.blockers': { zh: '阻塞项', en: 'Blockers' },
  'task.needs': { zh: '需要', en: 'Needs' },
  'task.openQuestions': { zh: '待确认问题', en: 'Open questions' },
  'task.artifacts': { zh: '产物与依赖', en: 'Artifacts & dependencies' },
  'task.workspaceState': { zh: '工作区状态', en: 'Workspace state' },
  'task.workingDirectory': { zh: '工作目录', en: 'Working directory' },
  'task.branch': { zh: '分支', en: 'Branch' },
  'task.revision': { zh: '版本', en: 'Revision' },
  'task.workingTree': { zh: '工作树', en: 'Working tree' },
  'task.dirty': { zh: '有未提交改动', en: 'Dirty' },
  'task.clean': { zh: '干净', en: 'Clean' },
  'task.statusSummary': { zh: '状态摘要', en: 'Status summary' },
  'task.noneRecorded': { zh: '未记录', en: 'Not recorded' },
  'task.required': { zh: '必需', en: 'Required' },
  'task.optional': { zh: '可选', en: 'Optional' },
  'task.noReferences': { zh: '没有声明引用', en: 'No declared references' },
  'task.status.in_progress': { zh: '进行中', en: 'In progress' },
  'task.status.ready': { zh: '可交接', en: 'Ready' },
  'task.status.blocked': { zh: '受阻', en: 'Blocked' },
  'task.status.complete': { zh: '已完成', en: 'Complete' },
  'task.kind.checkpoint': { zh: '检查点', en: 'Checkpoint' },
  'task.kind.handoff': { zh: '交接', en: 'Handoff' },

  // ---- checkpoint detail ----
  'checkpoint.title': { zh: '交接详情', en: 'Handoff detail' },
  'checkpoint.notFound': { zh: '检查点不可用', en: 'Checkpoint unavailable' },
  'checkpoint.notFoundHint': {
    zh: '检查点可能不存在，或不在当前 workspace / path scope 内。',
    en: 'The checkpoint may not exist or may be outside the current workspace or path scope.',
  },
  'checkpoint.copyId': { zh: '复制检查点 ID', en: 'Copy checkpoint ID' },
  'checkpoint.copied': { zh: '已复制检查点 ID', en: 'Checkpoint ID copied' },
  'checkpoint.copyFailed': { zh: '复制失败', en: 'Copy failed' },
  'checkpoint.provenance': { zh: '来源与完整性', en: 'Provenance & integrity' },
  'checkpoint.producer': { zh: '写入者', en: 'Producer' },
  'checkpoint.createdAt': { zh: '写入时间', en: 'Created at' },
  'checkpoint.scope': { zh: '授权路径', en: 'Scope path' },
  'checkpoint.id': { zh: '检查点 ID', en: 'Checkpoint ID' },
  'checkpoint.base': { zh: '基础检查点', en: 'Base checkpoint' },
  'checkpoint.references': { zh: '引用索引', en: 'Reference index' },

  // ---- deterministic resume ----
  'resume.title': { zh: '恢复上下文', en: 'Resume context' },
  'resume.headHint': {
    zh: '精确读取当前 head；语义证据是可选增强，不影响确定性任务状态。',
    en: 'Read the current head exactly. Semantic evidence is optional and never replaces deterministic task state.',
  },
  'resume.historicalHint': {
    zh: '精确恢复这个历史检查点，并重新验证它声明的引用。',
    en: 'Restore this historical checkpoint exactly and re-verify its declared references.',
  },
  'resume.build': { zh: '构建恢复包', en: 'Build resume pack' },
  'resume.rebuild': { zh: '重新构建', en: 'Rebuild' },
  'resume.beforeRun': {
    zh: '点击构建后会验证引用，并在权限允许时附加有出处的相关证据。',
    en: 'Build to verify references and, when permitted, attach source-verifiable related evidence.',
  },
  'resume.complete': { zh: '恢复包完整', en: 'Resume pack complete' },
  'resume.completeHint': {
    zh: '所有必需引用均可用，可以基于该检查点继续任务。',
    en: 'All required references are available; the task can continue from this checkpoint.',
  },
  'resume.incomplete': { zh: '缺少必需引用', en: 'Required references missing' },
  'resume.incompleteHint': {
    zh: '确定性任务状态已恢复，但继续前应处理缺失或哈希不一致的依赖。',
    en: 'Deterministic task state was restored, but missing or hash-mismatched dependencies need attention.',
  },
  'resume.deterministicState': { zh: '确定性任务状态', en: 'Deterministic task state' },
  'resume.resolved': { zh: '已解析引用', en: 'Resolved references' },
  'resume.missing': { zh: '缺失 / 未验证', en: 'Missing / unverified' },
  'resume.semanticEvidence': { zh: '相关语义证据', en: 'Related semantic evidence' },
  'resume.noEvidence': {
    zh: '本次恢复没有附加语义证据。',
    en: 'No semantic evidence was attached to this resume.',
  },
  'resume.noSearchPermission': {
    zh: '当前令牌没有 search 权限，未执行语义召回。',
    en: 'The current token lacks search permission, so semantic recall was not run.',
  },
  'resume.stateStillAvailable': {
    zh: '上面的确定性检查点仍然完整可读。',
    en: 'The deterministic checkpoint above remains fully readable.',
  },
  'resume.failed': { zh: '恢复请求失败', en: 'Resume request failed' },
  'resume.copy': { zh: '复制 JSON', en: 'Copy JSON' },
  'resume.copied': { zh: '已复制恢复包 JSON', en: 'Resume JSON copied' },
  'resume.copyFailed': { zh: '复制恢复包失败', en: 'Could not copy resume JSON' },

  // ---- 404 ----
  'notFound.title': { zh: '404 · 这里什么也没有', en: '404 · Nothing here' },
  'notFound.desc': {
    zh: '可能链接过期了，或者你需要先登录。',
    en: 'The link may have expired, or you need to sign in first.',
  },
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
