import * as React from 'react';
import {
  Archive,
  Bot,
  Check,
  Clipboard,
  Clock3,
  Database,
  FileText,
  Fingerprint,
  FolderClosed,
  LockKeyhole,
  Pin,
  PinOff,
  RotateCcw,
  ShieldCheck,
  ThumbsDown,
  ThumbsUp,
  Trash2,
  UserRound,
} from 'lucide-react';
import { Link } from 'react-router';
import { toast } from 'sonner';
import { ForgetMemoryDialog } from './ForgetMemoryDialog';
import { MemoryKindBadge } from './MemoryList';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, CardTitle } from '@/components/ui/Card';
import {
  useArchiveMemory,
  useForgetMemory,
  useMemoryFeedback,
  useRestoreMemory,
} from '@/hooks/useMemories';
import { useCapabilities } from '@/hooks/useWorkspace';
import { useT } from '@/i18n';
import { ApiException } from '@/lib/api';
import { formatDateTime, truncateMiddle } from '@/lib/format';
import type { AgentMemory, MemoryFeedbackAction, MemoryForgetReason } from '@/lib/types';

function errorText(error: unknown, conflict: string): string {
  if (error instanceof ApiException) {
    if (error.status === 409) return conflict;
    return error.hint || error.message;
  }
  if (error instanceof Error) return error.message;
  return String(error);
}

function jsonText(value: Record<string, unknown>): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return '{}';
  }
}

function Meta({
  icon,
  label,
  value,
  mono,
  children,
}: {
  icon?: React.ReactNode;
  label: string;
  value?: string | number | null;
  mono?: boolean;
  children?: React.ReactNode;
}) {
  if ((value === undefined || value === null || value === '') && !children) return null;
  return (
    <div className="min-w-0">
      <div className="mb-1 flex items-center gap-1.5 text-2xs uppercase tracking-wider text-fg-subtle">
        {icon}
        {label}
      </div>
      {children ?? (
        <div
          className={(mono ? 'font-mono ' : '') + 'break-words text-xs leading-5 text-fg-muted'}
          title={String(value)}
        >
          {value}
        </div>
      )}
    </div>
  );
}

export function MemoryDetail({
  memory,
  onReload,
  onForgotten,
}: {
  memory: AgentMemory;
  onReload: () => void;
  onForgotten: () => void;
}) {
  const { t } = useT();
  const capabilities = useCapabilities();
  const feedback = useMemoryFeedback();
  const archive = useArchiveMemory();
  const restore = useRestoreMemory();
  const forget = useForgetMemory();
  const [forgetOpen, setForgetOpen] = React.useState(false);
  const [actionError, setActionError] = React.useState<string | null>(null);
  const [isConflict, setIsConflict] = React.useState(false);

  const canWrite =
    capabilities.data?.permissions.read === true && capabilities.data.permissions.write === true;
  const canDelete = capabilities.data?.permissions.delete === true;
  const busy = feedback.isPending || archive.isPending || restore.isPending || forget.isPending;

  React.useEffect(() => {
    setActionError(null);
    setIsConflict(false);
    setForgetOpen(false);
  }, [memory.id, memory.state_version]);

  async function runAction(action: string, command: () => Promise<unknown>): Promise<boolean> {
    setActionError(null);
    setIsConflict(false);
    try {
      await command();
      toast.success(t(`memory.actionSuccess.${action}`));
      return true;
    } catch (error) {
      if (error instanceof ApiException && error.status === 410) {
        toast.error(t('memory.forgotten'));
        onForgotten();
        return false;
      }
      const conflict = error instanceof ApiException && error.status === 409;
      setIsConflict(conflict);
      setActionError(errorText(error, t('memory.conflict')));
      return false;
    }
  }

  function sendFeedback(action: MemoryFeedbackAction) {
    void runAction(action, () =>
      feedback.mutateAsync({
        memoryID: memory.id,
        expectedVersion: memory.state_version,
        action,
      }),
    );
  }

  async function confirmForget(reason: MemoryForgetReason) {
    const ok = await runAction('forget', () =>
      forget.mutateAsync({
        memoryID: memory.id,
        expectedVersion: memory.state_version,
        reason,
      }),
    );
    if (ok) {
      setForgetOpen(false);
      onForgotten();
    }
  }

  async function copyCitation() {
    try {
      await navigator.clipboard.writeText(memory.citation);
      toast.success(t('memory.citationCopied'));
    } catch {
      toast.error(t('memory.copyFailed'));
    }
  }

  const writeTitle = canWrite ? undefined : t('memory.writePermissionRequired');
  const deleteTitle = canDelete ? undefined : t('memory.deletePermissionRequired');
  const hasLocator = Object.keys(memory.source_locator).length > 0;
  const hasAttributes = Object.keys(memory.attributes).length > 0;

  return (
    <article className="min-w-0 space-y-4" aria-label={t('memory.detail')}>
      {memory.lifecycle_status === 'archived' && (
        <div className="flex items-center gap-2 rounded-lg border border-warn/30 bg-warn/5 px-4 py-3 text-xs text-fg-muted">
          <Archive className="h-4 w-4 flex-none text-warn" />
          <span>{t('memory.archivedNotice')}</span>
        </div>
      )}

      {actionError && (
        <div
          className="flex items-center justify-between gap-3 rounded-lg border border-danger/35 bg-danger/5 px-4 py-3 text-xs text-danger"
          role="alert"
        >
          <span>{actionError}</span>
          {isConflict && (
            <Button type="button" size="sm" variant="secondary" onClick={onReload}>
              <RotateCcw className="h-3.5 w-3.5" />
              {t('memory.reload')}
            </Button>
          )}
        </div>
      )}

      {(!canWrite || !canDelete) && (
        <div
          className="flex items-start gap-2 rounded-lg border border-border bg-bg-subtle/45 px-4 py-3 text-xs leading-5 text-fg-muted"
          role="note"
        >
          <LockKeyhole className="mt-0.5 h-3.5 w-3.5 flex-none text-fg-subtle" />
          <div>
            {!canWrite && <div>{t('memory.writePermissionRequired')}</div>}
            {!canDelete && <div>{t('memory.deletePermissionRequired')}</div>}
          </div>
        </div>
      )}

      <section className="surface overflow-hidden">
        <div className="border-b border-border bg-bg-subtle/45 px-5 py-4">
          <div className="flex flex-wrap items-center gap-2">
            <MemoryKindBadge kind={memory.kind} />
            <Badge tone={memory.lifecycle_status === 'active' ? 'success' : 'muted'} dot>
              {t(`memory.lifecycle.${memory.lifecycle_status}`)}
            </Badge>
            {memory.pinned && (
              <Badge tone="warn">
                <Pin className="h-3 w-3 fill-current" />
                {t('memory.pinned')}
              </Badge>
            )}
            <span className="ml-auto font-mono text-2xs text-fg-subtle">
              v{memory.state_version}
            </span>
          </div>
          <div className="mt-3 flex items-center gap-1.5 text-2xs text-fg-subtle">
            <FolderClosed className="h-3 w-3" />
            <span className="truncate font-mono">{memory.path}</span>
          </div>
        </div>

        <pre className="whitespace-pre-wrap break-words px-5 py-6 font-sans text-[15px] leading-7 text-fg">
          {memory.content}
        </pre>

        <div className="flex flex-wrap items-center gap-2 border-t border-border bg-bg-subtle/35 px-4 py-3">
          <Button
            type="button"
            size="sm"
            variant={memory.pinned ? 'secondary' : 'ghost'}
            disabled={!canWrite || busy}
            title={writeTitle}
            onClick={() => sendFeedback(memory.pinned ? 'unpin' : 'pin')}
          >
            {memory.pinned ? <PinOff className="h-3.5 w-3.5" /> : <Pin className="h-3.5 w-3.5" />}
            {memory.pinned ? t('memory.unpin') : t('memory.pin')}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={!canWrite || busy}
            title={writeTitle}
            onClick={() => sendFeedback('useful')}
          >
            <ThumbsUp className="h-3.5 w-3.5" />
            {t('memory.useful')} · {memory.useful_count}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={!canWrite || busy}
            title={writeTitle}
            onClick={() => sendFeedback('not_useful')}
          >
            <ThumbsDown className="h-3.5 w-3.5" />
            {t('memory.notUseful')} · {memory.not_useful_count}
          </Button>
          <div className="mx-1 hidden h-5 w-px bg-border sm:block" aria-hidden />
          {memory.lifecycle_status === 'active' ? (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={!canWrite || busy}
              title={writeTitle}
              onClick={() =>
                void runAction('archive', () =>
                  archive.mutateAsync({
                    memoryID: memory.id,
                    expectedVersion: memory.state_version,
                  }),
                )
              }
            >
              <Archive className="h-3.5 w-3.5" />
              {t('memory.archive')}
            </Button>
          ) : (
            <Button
              type="button"
              size="sm"
              variant="secondary"
              disabled={!canWrite || busy}
              title={writeTitle}
              onClick={() =>
                void runAction('restore', () =>
                  restore.mutateAsync({
                    memoryID: memory.id,
                    expectedVersion: memory.state_version,
                  }),
                )
              }
            >
              <RotateCcw className="h-3.5 w-3.5" />
              {t('memory.restore')}
            </Button>
          )}
          <Button
            type="button"
            size="sm"
            variant="danger"
            className="ml-auto"
            disabled={!canDelete || busy}
            title={deleteTitle}
            onClick={() => {
              setActionError(null);
              setForgetOpen(true);
            }}
          >
            <Trash2 className="h-3.5 w-3.5" />
            {t('memory.forget')}
          </Button>
        </div>
      </section>

      <div className="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader className="flex items-center gap-2">
            <ShieldCheck className="h-3.5 w-3.5 text-accent" />
            <CardTitle>{t('memory.provenance')}</CardTitle>
          </CardHeader>
          <CardBody className="grid gap-4 sm:grid-cols-2">
            <Meta
              icon={<Database className="h-3 w-3" />}
              label={t('memory.sourceType')}
              value={memory.source_type}
              mono
            />
            <Meta
              icon={<Clock3 className="h-3 w-3" />}
              label={t('memory.eventAt')}
              value={formatDateTime(memory.event_at)}
            />
            <Meta
              icon={<Bot className="h-3 w-3" />}
              label={t('memory.agent')}
              value={memory.producer_agent || t('memory.humanOrUnknown')}
              mono
            />
            <Meta
              icon={<Fingerprint className="h-3 w-3" />}
              label={t('memory.session')}
              value={memory.producer_session}
              mono
            />
            <Meta
              icon={<Check className="h-3 w-3" />}
              label={t('memory.task')}
              value={memory.producer_task}
              mono
            />
            <Meta
              icon={<UserRound className="h-3 w-3" />}
              label={t('memory.createdBy')}
              value={memory.created_by_user_id}
              mono
            />
            <Meta icon={<FileText className="h-3 w-3" />} label={t('memory.sourceFile')}>
              {memory.source_file_id ? (
                <Link
                  to={`/files/${encodeURIComponent(memory.source_file_id)}`}
                  className="break-all font-mono text-xs text-accent hover:text-accent-hover"
                >
                  {memory.source_file_id}
                </Link>
              ) : (
                <span className="text-xs text-fg-subtle">{t('memory.noSourceFile')}</span>
              )}
            </Meta>
            <Meta
              icon={<Fingerprint className="h-3 w-3" />}
              label={t('memory.sourceHash')}
              value={
                memory.source_file_sha256
                  ? truncateMiddle(memory.source_file_sha256, 32)
                  : undefined
              }
              mono
            />
            <div className="sm:col-span-2">
              <Meta label={t('memory.sourceRef')} value={memory.source_ref} mono />
            </div>
            {hasLocator && (
              <div className="sm:col-span-2">
                <div className="mb-1 text-2xs uppercase tracking-wider text-fg-subtle">
                  {t('memory.locator')}
                </div>
                <pre className="max-h-48 overflow-auto rounded-md border border-border bg-bg-inset p-3 font-mono text-2xs leading-5 text-fg-muted">
                  {jsonText(memory.source_locator)}
                </pre>
              </div>
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader className="flex items-center gap-2">
            <Fingerprint className="h-3.5 w-3.5 text-accent" />
            <CardTitle>{t('memory.identity')}</CardTitle>
          </CardHeader>
          <CardBody className="grid gap-4 sm:grid-cols-2">
            <Meta label={t('memory.createdAt')} value={formatDateTime(memory.created_at)} />
            <Meta label={t('memory.updatedAt')} value={formatDateTime(memory.updated_at)} />
            <Meta label={t('memory.feedbackScore')} value={memory.feedback_score} mono />
            <Meta label={t('memory.feedbackCount')} value={memory.feedback_count} mono />
            <div className="sm:col-span-2">
              <Meta label={t('memory.memoryId')} value={memory.id} mono />
            </div>
            <div className="sm:col-span-2">
              <Meta label={t('memory.workspaceId')} value={memory.workspace_id} mono />
            </div>
            <div className="sm:col-span-2">
              <Meta label="SHA-256" value={memory.content_sha256} mono />
            </div>
            <div className="sm:col-span-2">
              <div className="mb-1 text-2xs uppercase tracking-wider text-fg-subtle">
                {t('memory.citation')}
              </div>
              <div className="flex items-center gap-2 rounded-md border border-border bg-bg-inset px-3 py-2">
                <code className="min-w-0 flex-1 truncate text-2xs text-fg-muted">
                  {memory.citation}
                </code>
                <Button type="button" size="sm" variant="ghost" onClick={() => void copyCitation()}>
                  <Clipboard className="h-3.5 w-3.5" />
                  {t('memory.copyCitation')}
                </Button>
              </div>
            </div>
          </CardBody>
        </Card>
      </div>

      {hasAttributes && (
        <Card>
          <CardHeader>
            <CardTitle>{t('memory.attributes')}</CardTitle>
          </CardHeader>
          <CardBody>
            <pre className="max-h-72 overflow-auto rounded-md border border-border bg-bg-inset p-3 font-mono text-2xs leading-5 text-fg-muted">
              {jsonText(memory.attributes)}
            </pre>
          </CardBody>
        </Card>
      )}

      <ForgetMemoryDialog
        memory={memory}
        open={forgetOpen}
        busy={forget.isPending}
        error={forgetOpen ? (actionError ?? undefined) : undefined}
        onOpenChange={setForgetOpen}
        onConfirm={confirmForget}
      />
    </article>
  );
}
