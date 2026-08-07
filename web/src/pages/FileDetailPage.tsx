import * as React from 'react';
import { Link, useNavigate, useParams } from 'react-router';
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
  Check,
  X,
  AlertTriangle,
  Bot,
  Cpu,
} from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Badge, StatusBadge } from '@/components/ui/Badge';
import type { BadgeProps } from '@/components/ui/Badge';
import { Card, CardBody, CardHeader, CardTitle } from '@/components/ui/Card';
import { Skeleton } from '@/components/ui/Skeleton';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { useDecideFileAnnotation, useDeleteFile, useFile, useRelated } from '@/hooks/useFiles';
import { useAuthedBlobUrl } from '@/hooks/useAuthedBlob';
import { AuthedImage } from '@/components/ui/AuthedImage';
import { Markdown } from '@/components/ui/Markdown';
import { useT, tt } from '@/i18n';
import { ApiException, downloadFile } from '@/lib/api';
import { formatBytes, formatDateTime } from '@/lib/format';
import { toast } from 'sonner';
import type {
  FileAnnotation,
  FileAnnotationDecision,
  FileAnnotationStatus,
  FileKind,
  MemFile,
} from '@/lib/types';

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
    const notFound = !error || (error instanceof ApiException && error.status === 404);
    return (
      <div className="mx-auto max-w-5xl px-8 py-12">
        <Button variant="ghost" size="sm" onClick={() => navigate(-1)} className="mb-6">
          <ArrowLeft className="h-3.5 w-3.5" />
          {t('common.back')}
        </Button>
        <EmptyState
          icon={<FileQuestion />}
          title={t(notFound ? 'detail.notFoundTitle' : 'detail.loadFailedTitle')}
          description={t(notFound ? 'detail.notFoundDesc' : 'detail.loadFailedDesc')}
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
              {(file.index_status === 'partial' || file.index_status === 'failed') && (
                <div
                  role="status"
                  className="col-span-2 rounded-md border border-warn/30 bg-warn/10 p-3"
                >
                  <div className="flex items-center gap-2 text-xs font-medium text-warn">
                    <AlertTriangle className="h-3.5 w-3.5" />
                    {file.index_status === 'failed'
                      ? t('detail.failedTitle')
                      : t('detail.partialTitle')}
                  </div>
                  <p className="mt-1 text-2xs leading-relaxed text-fg-muted">
                    {file.index_status === 'failed'
                      ? t('detail.failedHint')
                      : t('detail.partialHint')}
                  </p>
                  {processorDegradedSteps(file).length > 0 && (
                    <div className="mt-2 font-mono text-2xs text-fg-subtle">
                      {processorDegradedSteps(file).join(' · ')}
                    </div>
                  )}
                </div>
              )}
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
                  label={t('detail.effectiveGeo')}
                  value={formatGeo(file.geo.lat, file.geo.lon)}
                  mono
                  full
                />
              )}
              {file.source_metadata.captured_at && (
                <MetaRow
                  icon={<Clock className="h-3 w-3" />}
                  label={t('detail.captureTime')}
                  value={formatDateTime(file.source_metadata.captured_at)}
                />
              )}
              {(file.source_metadata.source_kind || file.source_metadata.source_name) && (
                <MetaRow
                  icon={<Cpu className="h-3 w-3" />}
                  label={t('detail.source')}
                  value={[file.source_metadata.source_kind, file.source_metadata.source_name]
                    .filter(Boolean)
                    .join(' · ')}
                />
              )}
              {file.source_metadata.location && (
                <MetaRow
                  icon={<MapPin className="h-3 w-3" />}
                  label={t('detail.sourceLocation')}
                  value={formatSourceLocation(file.source_metadata.location)}
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

          <AIInsightsCard file={file} />

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

function AIInsightsCard({ file }: { file: MemFile }) {
  const { t } = useT();
  const pending = file.annotations
    .filter((annotation) => annotation.status === 'pending')
    .sort(comparePendingAnnotations);
  const reviewed = file.annotations.filter((annotation) => annotation.status !== 'pending');
  const userTags = new Set(file.user_tags);
  const hasAcceptedSummary =
    file.summary !== null &&
    file.annotations.some(
      (annotation) =>
        annotation.kind === 'description' &&
        annotation.status === 'accepted' &&
        annotation.value_text === file.summary,
    );

  return (
    <Card>
      <CardHeader className="flex items-center gap-2">
        <Sparkles className="h-3.5 w-3.5 text-accent" />
        <CardTitle>{t('detail.aiInsights')}</CardTitle>
      </CardHeader>
      <CardBody className="space-y-5 text-sm">
        <section
          aria-labelledby="effective-values-heading"
          className="rounded-md border border-success/25 bg-success/5 p-3"
        >
          <div
            id="effective-values-heading"
            className="mb-3 flex items-center gap-2 text-2xs font-semibold uppercase tracking-wider text-success"
          >
            <Check className="h-3.5 w-3.5" />
            {t('detail.effectiveValues')}
          </div>
          <div className="space-y-3">
            {file.summary && hasAcceptedSummary && (
              <div>
                <div className="mb-1 text-2xs uppercase tracking-wider text-fg-subtle">
                  {t('detail.summary')}
                </div>
                <div className="break-words leading-relaxed text-fg">{file.summary}</div>
              </div>
            )}
            {file.tags.length > 0 && (
              <div>
                <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2">
                  <div className="text-2xs uppercase tracking-wider text-fg-subtle">
                    {t('detail.effectiveTags')}
                  </div>
                  <div className="flex items-center gap-2 text-2xs text-fg-subtle">
                    <span className="inline-flex items-center gap-1">
                      <span className="h-1.5 w-1.5 rounded-full bg-fg-muted" />
                      {t('detail.userTag')}
                    </span>
                    <span className="inline-flex items-center gap-1">
                      <span className="h-1.5 w-1.5 rounded-full bg-success" />
                      {t('detail.acceptedTag')}
                    </span>
                  </div>
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {file.tags.map((tag) => {
                    const isUserTag = userTags.has(tag);
                    return (
                      <Badge
                        key={tag}
                        tone={isUserTag ? 'neutral' : 'success'}
                        aria-label={`${tag} — ${
                          isUserTag ? t('detail.userTag') : t('detail.acceptedTag')
                        }`}
                      >
                        {tag}
                      </Badge>
                    );
                  })}
                </div>
              </div>
            )}
            {(!file.summary || !hasAcceptedSummary) && file.tags.length === 0 && (
              <div className="text-xs text-fg-subtle">{t('search.none')}</div>
            )}
          </div>
        </section>

        {file.summary && !hasAcceptedSummary && (
          <section
            aria-label={t('detail.legacySummary')}
            className="rounded-md border border-dashed border-warn/30 bg-warn/5 p-3"
          >
            <div className="mb-1.5 flex items-center gap-2 text-2xs font-semibold uppercase tracking-wider text-warn">
              <Bot className="h-3.5 w-3.5" />
              {t('detail.legacySummary')}
            </div>
            <div className="break-words leading-relaxed text-fg">{file.summary}</div>
            <p className="mt-2 text-2xs leading-relaxed text-fg-subtle">
              {t('detail.legacySummaryHint')}
            </p>
          </section>
        )}

        {file.caption && file.caption !== file.summary && (
          <section
            aria-label={t('detail.captionVlm')}
            className="rounded-md border border-dashed border-warn/30 bg-warn/5 p-3"
          >
            <div className="mb-1.5 flex items-center gap-2 text-2xs font-semibold uppercase tracking-wider text-warn">
              <Bot className="h-3.5 w-3.5" />
              {t('detail.captionVlm')}
            </div>
            <div className="break-words leading-relaxed text-fg">{file.caption}</div>
            <p className="mt-2 text-2xs leading-relaxed text-fg-subtle">
              {t('detail.captionHint')}
            </p>
          </section>
        )}

        {file.entities && file.entities.length > 0 && (
          <section>
            <div className="mb-1.5 flex items-center gap-1.5 text-2xs uppercase tracking-wider text-fg-subtle">
              <Users className="h-3 w-3" />
              {t('detail.entities')}
            </div>
            <div className="flex flex-wrap gap-1.5">
              {file.entities.map((entity) => (
                <Badge key={entity.id} tone={entity.type === 'person' ? 'accent' : 'neutral'}>
                  {entity.name}
                </Badge>
              ))}
            </div>
          </section>
        )}

        <section
          aria-labelledby="pending-suggestions-heading"
          className="border-t border-border pt-4"
        >
          <div className="flex items-center justify-between gap-3">
            <div
              id="pending-suggestions-heading"
              className="flex items-center gap-2 text-2xs font-semibold uppercase tracking-wider text-accent"
            >
              <Sparkles className="h-3.5 w-3.5" />
              {t('detail.reviewSuggestions')}
            </div>
            {pending.length > 0 && <Badge tone="accent">{pending.length}</Badge>}
          </div>
          <p className="mt-1 text-2xs leading-relaxed text-fg-subtle">{t('detail.reviewHint')}</p>

          {pending.length > 0 ? (
            <div className="mt-3 space-y-3">
              {pending.map((annotation) => (
                <AnnotationSuggestion
                  key={annotation.id}
                  fileID={file.id}
                  annotation={annotation}
                />
              ))}
            </div>
          ) : (
            <div className="mt-3 rounded-md border border-border bg-bg-inset/40 px-3 py-2 text-xs text-fg-subtle">
              {file.index_status === 'pending' || file.index_status === 'processing'
                ? t('detail.aiProcessing')
                : t('detail.noPendingSuggestions')}
            </div>
          )}
        </section>

        {file.annotations_truncated && (
          <div
            role="note"
            data-testid="annotations-truncated-notice"
            className="flex items-start gap-2 rounded-md border border-border bg-bg-inset/40 px-3 py-2 text-2xs leading-relaxed text-fg-subtle"
          >
            <AlertTriangle className="mt-0.5 h-3 w-3 flex-none text-warn" />
            <span>{t('detail.reviewHistoryTruncated')}</span>
          </div>
        )}

        {reviewed.length > 0 && (
          <details className="border-t border-border pt-4">
            <summary className="cursor-pointer select-none text-2xs font-medium uppercase tracking-wider text-fg-subtle hover:text-fg-muted">
              {t('detail.reviewedSuggestions')} · {reviewed.length}
            </summary>
            <div className="mt-3 space-y-2">
              {reviewed.map((annotation) => (
                <ReviewedAnnotation key={annotation.id} annotation={annotation} />
              ))}
            </div>
          </details>
        )}
      </CardBody>
    </Card>
  );
}

function comparePendingAnnotations(left: FileAnnotation, right: FileAnnotation): number {
  if (left.confidence !== right.confidence) return right.confidence - left.confidence;

  const createdOrder = left.created_at.localeCompare(right.created_at);
  if (createdOrder !== 0) return createdOrder;

  const updatedOrder = right.updated_at.localeCompare(left.updated_at);
  if (updatedOrder !== 0) return updatedOrder;

  return left.id.localeCompare(right.id);
}

function AnnotationSuggestion({
  fileID,
  annotation,
}: {
  fileID: string;
  annotation: FileAnnotation;
}) {
  const { t } = useT();
  const decision = useDecideFileAnnotation(fileID);
  const [inlineError, setInlineError] = React.useState<string | null>(null);
  const confidence = formatConfidence(annotation.confidence);
  const provenance = annotationProvenance(annotation);

  async function review(next: FileAnnotationDecision) {
    setInlineError(null);
    decision.reset();
    try {
      await decision.mutateAsync({
        annotationID: annotation.id,
        decision: next,
        expectedVersion: annotation.state_version,
      });
      toast.success(
        next === 'accepted' ? tt('toast.annotationAccepted') : tt('toast.annotationRejected'),
      );
    } catch (error) {
      if (error instanceof ApiException && error.status === 409) {
        setInlineError(tt('detail.reviewConflict'));
        toast.error(tt('toast.annotationConflict'), {
          description: tt('detail.reviewConflict'),
        });
        return;
      }
      const message =
        error instanceof ApiException
          ? (error.hint ?? error.message)
          : error instanceof Error
            ? error.message
            : tt('detail.reviewError');
      setInlineError(message);
      toast.error(tt('toast.annotationFailed'), { description: message });
    }
  }

  const accepting = decision.isPending && decision.variables?.decision === 'accepted';
  const rejecting = decision.isPending && decision.variables?.decision === 'rejected';

  return (
    <article
      data-testid={`annotation-${annotation.id}`}
      className="rounded-md border border-accent/25 bg-accent/5 p-3"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Badge tone="accent">
          {annotation.kind === 'description'
            ? t('detail.pendingDescription')
            : t('detail.pendingTag')}
        </Badge>
        <span className="font-mono text-2xs text-accent">
          {t('detail.confidence', { value: confidence })}
        </span>
      </div>
      <div className="mt-2 break-words text-sm leading-relaxed text-fg">
        {annotation.value_text}
      </div>
      <div className="mt-2 flex items-start gap-1.5 text-2xs leading-relaxed text-fg-subtle">
        <Cpu className="mt-0.5 h-3 w-3 flex-none" />
        <span>
          {t('detail.provenance')}: {provenance}
        </span>
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="primary"
          loading={accepting}
          disabled={decision.isPending}
          aria-label={t('detail.acceptSuggestion', { value: annotation.value_text })}
          onClick={() => void review('accepted')}
        >
          <Check className="h-3.5 w-3.5" />
          {t('common.accept')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          loading={rejecting}
          disabled={decision.isPending}
          aria-label={t('detail.rejectSuggestion', { value: annotation.value_text })}
          onClick={() => void review('rejected')}
        >
          <X className="h-3.5 w-3.5" />
          {t('common.reject')}
        </Button>
      </div>
      {inlineError && (
        <div
          role="alert"
          className="mt-3 rounded border border-danger/30 bg-danger/10 px-2.5 py-2 text-2xs leading-relaxed text-danger"
        >
          {inlineError}
        </div>
      )}
    </article>
  );
}

function ReviewedAnnotation({ annotation }: { annotation: FileAnnotation }) {
  const { t } = useT();
  return (
    <div className="rounded-md border border-border bg-bg-inset/35 px-3 py-2">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 break-words text-xs text-fg-muted">{annotation.value_text}</div>
        <Badge tone={annotationStatusTone(annotation.status)}>
          {t(`detail.annotation.${annotation.status}`)}
        </Badge>
      </div>
      <div className="mt-1 truncate font-mono text-2xs text-fg-subtle">
        {formatConfidence(annotation.confidence)} · {annotationProvenance(annotation)}
      </div>
    </div>
  );
}

function annotationStatusTone(status: FileAnnotationStatus): BadgeProps['tone'] {
  if (status === 'accepted') return 'success';
  if (status === 'rejected') return 'danger';
  if (status === 'pending') return 'accent';
  return 'muted';
}

function annotationProvenance(annotation: FileAnnotation): string {
  return [
    annotation.provider && `provider=${annotation.provider}`,
    annotation.processor && `processor=${annotation.processor}`,
    annotation.analysis_version && `version=${annotation.analysis_version}`,
    annotation.source && `source=${annotation.source}`,
  ]
    .filter(Boolean)
    .join(' · ');
}

function formatConfidence(value: number): string {
  const bounded = Number.isFinite(value) ? Math.min(1, Math.max(0, value)) : 0;
  return `${Math.round(bounded * 100)}%`;
}

function processorDegradedSteps(file: MemFile): string[] {
  const steps = file.processor_metadata.degraded_steps;
  if (!Array.isArray(steps)) return [];
  return steps.filter((step): step is string => typeof step === 'string');
}

function formatGeo(lat: number, lon: number): string {
  return `${lat.toFixed(4)}, ${lon.toFixed(4)}`;
}

function formatSourceLocation(
  location: NonNullable<MemFile['source_metadata']['location']>,
): string {
  const parts = [formatGeo(location.lat, location.lon)];
  if (location.label) parts.push(location.label);
  if (location.accuracy_m !== undefined) parts.push(`±${Math.round(location.accuracy_m)} m`);
  return parts.join(' · ');
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
