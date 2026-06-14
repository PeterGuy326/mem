import * as React from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import {
  ArrowLeft,
  Download,
  Trash2,
  Copy,
  Image as ImageIcon,
  FileText,
  Music,
  FileQuestion,
  Hash,
  MapPin,
  Clock,
  Tag as TagIcon,
  Sparkles,
  Users,
} from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Badge, StatusBadge } from '@/components/ui/Badge';
import { Card, CardBody, CardHeader, CardTitle } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { useDeleteFile, useFile, useRelated } from '@/hooks/useFiles';
import { useAuthedBlobUrl } from '@/hooks/useAuthedBlob';
import { AuthedImage } from '@/components/ui/AuthedImage';
import { downloadFile } from '@/lib/api';
import { formatBytes, formatDateTime } from '@/lib/format';
import { toast } from 'sonner';
import type { FileKind, MemFile } from '@/lib/types';

export function FileDetailPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { data: file, isLoading, error } = useFile(id);
  const { data: related } = useRelated(id);
  const del = useDeleteFile();
  const [confirmOpen, setConfirmOpen] = React.useState(false);

  if (isLoading) return <DetailSkeleton />;

  if (error || !file) {
    return (
      <div className="mx-auto max-w-5xl px-8 py-12">
        <Button variant="ghost" size="sm" onClick={() => navigate(-1)} className="mb-6">
          <ArrowLeft className="h-3.5 w-3.5" />
          返回
        </Button>
        <EmptyState
          icon={<FileQuestion />}
          title="文件不存在"
          description="可能已删除，或 file_id 错了。"
          action={
            <Link to="/">
              <Button variant="secondary" size="sm">回到首页</Button>
            </Link>
          }
        />
      </div>
    );
  }

  function copyId() {
    if (!file) return;
    navigator.clipboard.writeText(file.id).catch(() => {});
    toast.success('已复制 file_id');
  }

  async function onDelete() {
    if (!file) return;
    try {
      await del.mutateAsync(file.id);
      toast.success('已删除');
      navigate('/', { replace: true });
    } catch (err) {
      toast.error('删除失败', { description: err instanceof Error ? err.message : undefined });
    }
  }

  return (
    <div className="mx-auto max-w-7xl px-8 py-8">
      {/* Top bar */}
      <div className="flex items-center gap-3 mb-6">
        <Button variant="ghost" size="sm" onClick={() => navigate(-1)}>
          <ArrowLeft className="h-3.5 w-3.5" />
          返回
        </Button>
        <div className="flex-1 min-w-0">
          <div className="text-sm text-fg truncate">{file.name}</div>
          <div className="text-2xs text-fg-subtle font-mono truncate">{file.path}</div>
        </div>
        <Button variant="ghost" size="sm" onClick={copyId}>
          <Copy className="h-3.5 w-3.5" />
          复制 ID
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => {
            downloadFile(file.id, file.name).catch(() => toast.error('下载失败'));
          }}
        >
          <Download className="h-3.5 w-3.5" />
          下载
        </Button>
        <Button variant="danger" size="sm" onClick={() => setConfirmOpen(true)}>
          <Trash2 className="h-3.5 w-3.5" />
          删除
        </Button>
      </div>

      {/* Two-column layout */}
      <div className="grid grid-cols-1 lg:grid-cols-[minmax(0,1fr)_380px] gap-6 items-start">
        {/* Preview */}
        <section className="surface overflow-hidden">
          <PreviewArea file={file} />
        </section>

        {/* Right panel */}
        <aside className="flex flex-col gap-4">
          <Card>
            <CardHeader className="flex items-center justify-between">
              <CardTitle>状态</CardTitle>
              <StatusBadge status={file.index_status} />
            </CardHeader>
            <CardBody className="grid grid-cols-2 gap-x-4 gap-y-3 text-xs">
              <MetaRow icon={<Hash className="h-3 w-3" />} label="大小" value={formatBytes(file.size)} />
              <MetaRow icon={<TagIcon className="h-3 w-3" />} label="MIME" value={file.mime} mono />
              <MetaRow
                icon={<Clock className="h-3 w-3" />}
                label="时间锚点"
                value={formatDateTime(file.timeline_at)}
              />
              <MetaRow
                icon={<Clock className="h-3 w-3" />}
                label="入库时间"
                value={formatDateTime(file.created_at)}
              />
              {file.geo && (
                <MetaRow
                  icon={<MapPin className="h-3 w-3" />}
                  label="经纬"
                  value={`${file.geo.lat.toFixed(2)}, ${file.geo.lon.toFixed(2)}`}
                  mono
                  full
                />
              )}
              <MetaRow
                icon={<Hash className="h-3 w-3" />}
                label="SHA-256"
                value={file.sha256}
                mono
                truncate
                full
              />
            </CardBody>
          </Card>

          <Card>
            <CardHeader className="flex items-center gap-2">
              <Sparkles className="h-3.5 w-3.5 text-accent" />
              <CardTitle>AI 洞察</CardTitle>
            </CardHeader>
            <CardBody className="space-y-4 text-sm">
              {file.caption && (
                <div>
                  <div className="text-2xs uppercase tracking-wider text-fg-subtle mb-1">Caption (VLM)</div>
                  <div className="text-fg leading-relaxed">{file.caption}</div>
                </div>
              )}
              {file.summary && (
                <div>
                  <div className="text-2xs uppercase tracking-wider text-fg-subtle mb-1">摘要</div>
                  <div className="text-fg-muted leading-relaxed">{file.summary}</div>
                </div>
              )}
              {file.tags.length > 0 && (
                <div>
                  <div className="text-2xs uppercase tracking-wider text-fg-subtle mb-1.5">自动标签</div>
                  <div className="flex flex-wrap gap-1.5">
                    {file.tags.map((t) => (
                      <Badge key={t} tone="neutral">{t}</Badge>
                    ))}
                  </div>
                </div>
              )}
              {file.entities && file.entities.length > 0 && (
                <div>
                  <div className="text-2xs uppercase tracking-wider text-fg-subtle mb-1.5 flex items-center gap-1.5">
                    <Users className="h-3 w-3" />
                    识别到的实体
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {file.entities.map((e) => (
                      <Badge key={e.id} tone={e.type === 'person' ? 'accent' : 'neutral'}>
                        {e.name}
                      </Badge>
                    ))}
                  </div>
                </div>
              )}
              {!file.caption && !file.summary && file.tags.length === 0 && (
                <div className="text-xs text-fg-subtle">
                  AI 还在处理中，稍等一下就能看到 caption 和标签。
                </div>
              )}
            </CardBody>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>相关文件</CardTitle>
            </CardHeader>
            <CardBody className="p-0">
              {related?.results.length ? (
                <ol className="divide-y divide-border">
                  {related.results.map((r) => (
                    <RelatedRow
                      key={r.file.id}
                      file={r.file}
                      relation={r.relation}
                    />
                  ))}
                </ol>
              ) : (
                <div className="p-4 text-xs text-fg-subtle">暂无关联（mock：W3 接入后启用）</div>
              )}
            </CardBody>
          </Card>
        </aside>
      </div>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="删除该文件？"
        description={`将永久删除 “${file.name}” 及其所有 AI 索引（caption / embeddings / 人脸聚类）。此操作不可恢复。`}
        confirmText="删除"
        destructive
        onConfirm={onDelete}
      />
    </div>
  );
}

function PreviewArea({ file }: { file: MemFile }) {
  if (file.kind === 'image') {
    return (
      <div className="bg-bg-inset/60 flex items-center justify-center min-h-[280px]">
        <AuthedImage
          fileId={file.id}
          alt={file.caption ?? file.name}
          className="max-h-[72vh] w-auto object-contain"
          fallback={
            <div className="p-12 text-fg-subtle">
              <ImageIcon className="h-10 w-10" />
            </div>
          }
        />
      </div>
    );
  }
  if (file.kind === 'text' || file.kind === 'pdf' || file.kind === 'doc') {
    return (
      <div className="p-8 max-h-[72vh] overflow-y-auto">
        <div className="font-mono text-xs text-fg-muted whitespace-pre-wrap leading-relaxed">
          {file.summary
            ? file.summary
            : '（文本预览占位 — 后端接入后会展示分块原文或 PDF 转写。）'}
        </div>
      </div>
    );
  }
  if (file.kind === 'audio') {
    return <AudioPreview file={file} />;
  }
  return (
    <div className="p-12 flex flex-col items-center gap-3 text-fg-muted">
      <FileQuestion className="h-10 w-10 text-fg-subtle" />
      <div className="text-sm">无法预览此文件类型</div>
      <div className="text-2xs text-fg-subtle">{file.mime}</div>
    </div>
  );
}

function AudioPreview({ file }: { file: MemFile }) {
  const { url, isLoading } = useAuthedBlobUrl(file.id);
  return (
    <div className="p-12 flex flex-col items-center gap-4">
      <div className="h-20 w-20 rounded-2xl bg-accent/10 text-accent grid place-items-center">
        <Music className="h-8 w-8" />
      </div>
      {url ? (
        <audio controls src={url} className="w-full max-w-md" />
      ) : (
        <div className="text-2xs text-fg-subtle">{isLoading ? '加载音频…' : '音频不可用'}</div>
      )}
      <div className="text-xs text-fg-muted text-center max-w-md">
        {file.summary ?? '音频 ASR 转写完成后会出现在 AI 洞察区。'}
      </div>
    </div>
  );
}

function MetaRow({
  icon,
  label,
  value,
  mono,
  truncate,
  full,
}: {
  icon?: React.ReactNode;
  label: string;
  value: string;
  mono?: boolean;
  truncate?: boolean;
  full?: boolean;
}) {
  return (
    <div className={full ? 'col-span-2' : undefined}>
      <div className="flex items-center gap-1.5 text-2xs uppercase tracking-wider text-fg-subtle mb-0.5">
        {icon}
        {label}
      </div>
      <div
        className={
          (mono ? 'font-mono ' : '') +
          (truncate ? 'truncate ' : 'break-words ') +
          'text-fg'
        }
        title={value}
      >
        {value}
      </div>
    </div>
  );
}

function kindIcon(kind: FileKind) {
  if (kind === 'image') return ImageIcon;
  if (kind === 'audio') return Music;
  if (kind === 'doc' || kind === 'pdf' || kind === 'text') return FileText;
  return FileQuestion;
}

function RelatedRow({
  file,
  relation,
}: {
  file: MemFile;
  relation: string;
}) {
  const Icon = kindIcon(file.kind);
  return (
    <Link
      to={`/files/${file.id}`}
      className="group flex items-center gap-3 px-4 py-2.5 hover:bg-bg-inset/60 transition-colors"
    >
      <div className="h-9 w-9 flex-none rounded-md overflow-hidden bg-bg-inset border border-border grid place-items-center">
        {file.kind === 'image' ? (
          <AuthedImage fileId={file.id} fallback={<Icon className="h-3.5 w-3.5 text-fg-subtle" />} />
        ) : (
          <Icon className="h-3.5 w-3.5 text-fg-subtle" />
        )}
      </div>
      <div className="flex-1 min-w-0">
        <div className="text-xs text-fg truncate group-hover:text-accent transition-colors">
          {file.name}
        </div>
        <div className="text-2xs text-fg-subtle">{file.caption ?? file.summary ?? '—'}</div>
      </div>
      <Badge tone="muted">{relation}</Badge>
    </Link>
  );
}

function DetailSkeleton() {
  return (
    <div className="mx-auto max-w-7xl px-8 py-8">
      <div className="flex items-center gap-3 mb-6">
        <Skeleton className="h-7 w-16" />
        <Skeleton className="h-5 w-64 flex-1" />
        <Skeleton className="h-7 w-20" />
        <Skeleton className="h-7 w-20" />
      </div>
      <div className="grid grid-cols-1 lg:grid-cols-[1fr_380px] gap-6">
        <Skeleton className="aspect-[4/3] w-full" />
        <div className="space-y-4">
          <Skeleton className="h-40 w-full" />
          <Skeleton className="h-60 w-full" />
        </div>
      </div>
    </div>
  );
}
