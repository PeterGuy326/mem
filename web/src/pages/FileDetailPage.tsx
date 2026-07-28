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
import type { BadgeProps } from '@/components/ui/Badge';
import { Card, CardBody, CardHeader, CardTitle } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { useDeleteFile, useFile, useRelated } from '@/hooks/useFiles';
import { useAuthedBlobUrl } from '@/hooks/useAuthedBlob';
import { AuthedImage } from '@/components/ui/AuthedImage';
import { Markdown } from '@/components/ui/Markdown';
import { useT, tt } from '@/i18n';
import { downloadFile } from '@/lib/api';
import { formatBytes, formatDateTime } from '@/lib/format';
import { toast } from 'sonner';
import type { FileKind, MemFile } from '@/lib/types';

export function FileDetailPage() {
  const { t } = useT();
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
          {t('common.back')}
        </Button>
        <EmptyState
          icon={<FileQuestion />}
          title={t('detail.notFoundTitle')}
          description={t('detail.notFoundDesc')}
          action={
            <Link to="/">
              <Button variant="secondary" size="sm">{t('action.home')}</Button>
            </Link>
          }
        />
      </div>
    );
  }

  function copyId() {
    if (!file) return;
    navigator.clipboard.writeText(file.id).catch(() => {});
    toast.success(tt('toast.copiedId'));
  }

  async function onDelete() {
    if (!file) return;
    try {
      await del.mutateAsync(file.id);
      toast.success(tt('toast.deleted'));
      navigate('/', { replace: true });
    } catch (err) {
      toast.error(tt('toast.deleteFailed'), { description: err instanceof Error ? err.message : undefined });
    }
  }

  return (
    <div className="mx-auto max-w-7xl px-8 py-8">
      {/* Top bar */}
      <div className="flex items-center gap-3 mb-6">
        <Button variant="ghost" size="sm" onClick={() => navigate(-1)}>
          <ArrowLeft className="h-3.5 w-3.5" />
          {t('common.back')}
        </Button>
        <div className="flex-1 min-w-0">
          <div className="text-sm text-fg truncate">{file.name}</div>
          <div className="text-2xs text-fg-subtle font-mono truncate">{file.path}</div>
        </div>
        <Button variant="ghost" size="sm" onClick={copyId}>
          <Copy className="h-3.5 w-3.5" />
          {t('common.copyId')}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => {
            downloadFile(file.id, file.name).catch(() => toast.error(tt('toast.downloadFailed')));
          }}
        >
          <Download className="h-3.5 w-3.5" />
          {t('common.download')}
        </Button>
        <Button variant="danger" size="sm" onClick={() => setConfirmOpen(true)}>
          <Trash2 className="h-3.5 w-3.5" />
          {t('common.delete')}
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
              <CardTitle>{t('detail.status')}</CardTitle>
              <StatusBadge status={file.index_status} />
            </CardHeader>
            <CardBody className="grid grid-cols-2 gap-x-4 gap-y-3 text-xs">
              <MetaRow icon={<Hash className="h-3 w-3" />} label={t('detail.size')} value={formatBytes(file.size)} />
              <MetaRow icon={<TagIcon className="h-3 w-3" />} label="MIME" value={file.mime} mono />
              <MetaRow
                icon={<Clock className="h-3 w-3" />}
                label={t('detail.timeAnchor')}
                value={formatDateTime(file.timeline_at)}
              />
              <MetaRow
                icon={<Clock className="h-3 w-3" />}
                label={t('detail.ingestedAt')}
                value={formatDateTime(file.created_at)}
              />
              {file.geo && (
                <MetaRow
                  icon={<MapPin className="h-3 w-3" />}
                  label={t('detail.geo')}
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
              <CardTitle>{t('detail.aiInsights')}</CardTitle>
            </CardHeader>
            <CardBody className="space-y-4 text-sm">
              {file.caption && (
                <div>
                  <div className="text-2xs uppercase tracking-wider text-fg-subtle mb-1">{t('detail.captionVlm')}</div>
                  <div className="text-fg leading-relaxed">{file.caption}</div>
                </div>
              )}
              {file.summary && (
                <div>
                  <div className="text-2xs uppercase tracking-wider text-fg-subtle mb-1">{t('detail.summary')}</div>
                  <div className="text-fg-muted leading-relaxed">{file.summary}</div>
                </div>
              )}
              {file.tags.length > 0 && (
                <div>
                  <div className="text-2xs uppercase tracking-wider text-fg-subtle mb-1.5">{t('detail.autoTags')}</div>
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
                    {t('detail.entities')}
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
                  {t('detail.aiProcessing')}
                </div>
              )}
            </CardBody>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t('detail.relatedFiles')}</CardTitle>
            </CardHeader>
            <CardBody className="p-0">
              {related?.results.length ? (
                <RelatedGrouped results={related.results} />
              ) : (
                <div className="p-4 text-xs text-fg-subtle">{t('search.none')}</div>
              )}
            </CardBody>
          </Card>
        </aside>
      </div>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('detail.deleteTitle')}
        description={t('detail.deleteDesc', { name: file.name })}
        confirmText={t('common.delete')}
        destructive
        onConfirm={onDelete}
      />
    </div>
  );
}

function PreviewArea({ file }: { file: MemFile }) {
  const mime = file.mime || '';
  if (file.kind === 'image') return <ImagePreview file={file} />;
  if (file.kind === 'pdf' || mime === 'application/pdf') return <PdfPreview file={file} />;
  if (file.kind === 'video' || mime.startsWith('video/')) return <VideoPreview file={file} />;
  if (file.kind === 'audio' || mime.startsWith('audio/')) return <AudioPreview file={file} />;
  if (
    file.kind === 'text' ||
    file.kind === 'doc' ||
    mime.startsWith('text/') ||
    mime === 'application/json'
  )
    return <TextPreview file={file} />;
  return <UnsupportedPreview file={file} />;
}

/** Full-bleed image, contained (no crop) — distinct from the cover-cropped grid thumb. */
function ImagePreview({ file }: { file: MemFile }) {
  const { url, isLoading } = useAuthedBlobUrl(file.id);
  return (
    <div className="bg-bg-inset/60 flex items-center justify-center min-h-[280px]">
      {url ? (
        <img
          src={url}
          alt={file.caption ?? file.name}
          className="max-h-[72vh] w-auto object-contain"
          draggable={false}
        />
      ) : (
        <div className="p-12 text-fg-subtle">
          {isLoading ? (
            <div className="h-10 w-10 animate-pulse rounded bg-bg-inset" />
          ) : (
            <ImageIcon className="h-10 w-10" />
          )}
        </div>
      )}
    </div>
  );
}

/** Native browser PDF viewer via a blob URL in an iframe (renders real pages). */
function PdfPreview({ file }: { file: MemFile }) {
  const { url, isLoading, isError } = useAuthedBlobUrl(file.id);
  if (isLoading)
    return <div className="h-[72vh] w-full animate-pulse bg-bg-inset/60" />;
  if (isError || !url) return <UnsupportedPreview file={file} />;
  return (
    <iframe
      src={`${url}#view=FitH`}
      title={file.name}
      className="w-full h-[72vh] border-0 bg-white"
    />
  );
}

function VideoPreview({ file }: { file: MemFile }) {
  const { t } = useT();
  const { url, isLoading } = useAuthedBlobUrl(file.id);
  return (
    <div className="bg-black flex items-center justify-center min-h-[280px]">
      {url ? (
        <video controls src={url} className="max-h-[72vh] w-auto" />
      ) : (
        <div className="p-12 text-2xs text-fg-subtle">{isLoading ? t('detail.loadingVideo') : '—'}</div>
      )}
    </div>
  );
}

/** Fetch the file's real text via its blob URL and render it (Markdown for .md). */
function TextPreview({ file }: { file: MemFile }) {
  const { url, isLoading } = useAuthedBlobUrl(file.id);
  const [text, setText] = React.useState<string | null>(null);
  const [err, setErr] = React.useState(false);
  React.useEffect(() => {
    if (!url) return;
    let alive = true;
    fetch(url)
      .then((r) => r.text())
      .then((t) => alive && setText(t))
      .catch(() => alive && setErr(true));
    return () => {
      alive = false;
    };
  }, [url]);

  const isMarkdown = /\.(md|markdown)$/i.test(file.name) || file.mime === 'text/markdown';

  if (isLoading || (text === null && !err))
    return (
      <div className="p-8 space-y-3 max-h-[72vh]">
        <Skeleton className="h-4 w-5/6" />
        <Skeleton className="h-4 w-4/6" />
        <Skeleton className="h-4 w-3/4" />
      </div>
    );
  if (err || text === null) return <UnsupportedPreview file={file} />;

  return (
    <div className="px-8 py-10 max-h-[72vh] overflow-y-auto">
      {isMarkdown ? (
        <div className="mx-auto max-w-prose">
          <Markdown className="text-[15px] text-fg">{text}</Markdown>
        </div>
      ) : (
        // Plain text rendered as a readable document, not gray monospace code.
        <article className="mx-auto max-w-prose whitespace-pre-wrap text-[15px] leading-7 text-fg/90">
          {text}
        </article>
      )}
    </div>
  );
}

function UnsupportedPreview({ file }: { file: MemFile }) {
  const { t } = useT();
  return (
    <div className="p-12 flex flex-col items-center gap-3 text-fg-muted">
      <FileQuestion className="h-10 w-10 text-fg-subtle" />
      <div className="text-sm">{t('detail.unsupported')}</div>
      <div className="text-2xs text-fg-subtle">{file.mime}</div>
      <Button variant="secondary" size="sm" onClick={() => downloadFile(file.id, file.name)}>
        <Download className="h-4 w-4" /> {t('detail.downloadToView')}
      </Button>
    </div>
  );
}

function AudioPreview({ file }: { file: MemFile }) {
  const { t } = useT();
  const { url, isLoading } = useAuthedBlobUrl(file.id);
  return (
    <div className="p-12 flex flex-col items-center gap-4">
      <div className="h-20 w-20 rounded-2xl bg-accent/10 text-accent grid place-items-center">
        <Music className="h-8 w-8" />
      </div>
      {url ? (
        <audio controls src={url} className="w-full max-w-md" />
      ) : (
        <div className="text-2xs text-fg-subtle">{isLoading ? t('detail.loadingAudio') : '—'}</div>
      )}
      <div className="text-xs text-fg-muted text-center max-w-md">
        {file.summary ?? ''}
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

/** Badge configuration per relation type. */
function getBadgeProps(relation: string): { tone: BadgeProps['tone']; className?: string } {
  switch (relation) {
    case 'same_topic':
      return { tone: 'accent' };
    case 'same_event':
      return { tone: 'neutral', className: 'bg-purple-500/10 text-purple-500 border-purple-500/30' };
    case 'same_person':
      return { tone: 'warn' };
    default:
      return { tone: 'muted' };
  }
}

/** Groups related file cards by relation type into colour-coded sections. */
function RelatedGrouped({
  results,
}: {
  results: Array<{ file: MemFile; relation: string }>;
}) {
  const { t } = useT();

  const groups = React.useMemo(() => {
    const map = new Map<string, Array<{ file: MemFile; relation: string }>>();
    for (const r of results) {
      const group = map.get(r.relation) ?? [];
      group.push(r);
      map.set(r.relation, group);
    }
    return map;
  }, [results]);

  const preferredOrder = ['same_topic', 'same_event', 'same_person', 'sequel'];
  const sections = [
    ...preferredOrder.filter((type) => (groups.get(type)?.length ?? 0) > 0),
    ...Array.from(groups.keys()).filter((type) => !preferredOrder.includes(type)),
  ];

  if (sections.length === 0) {
    return <div className="p-4 text-xs text-fg-subtle">{t('search.none')}</div>;
  }

  return (
    <div className="divide-y divide-border">
      {sections.map((type) => {
        const items = groups.get(type)!;
        const { tone: badgeTone, className: badgeClassName } = getBadgeProps(type);
        return (
          <div key={type}>
            <div className="px-4 py-2 text-2xs uppercase tracking-wider text-fg-subtle font-medium bg-bg-inset/30">
              {t(`related.${type}`)}
            </div>
            <ol className="divide-y divide-border">
              {items.map((r) => (
                <RelatedRow
                  key={r.file.id}
                  file={r.file}
                  relation={r.relation}
                  badgeTone={badgeTone}
                  badgeClassName={badgeClassName}
                />
              ))}
            </ol>
          </div>
        );
      })}
    </div>
  );
}

function RelatedRow({
  file,
  relation,
  badgeTone,
  badgeClassName,
}: {
  file: MemFile;
  relation: string;
  badgeTone: BadgeProps['tone'];
  badgeClassName?: string;
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
      <Badge tone={badgeTone} className={badgeClassName}>{tt(`related.${relation}`)}</Badge>
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
