import {
  ArrowLeft,
  Clock3,
  Copy,
  Fingerprint,
  GitCommitHorizontal,
  UserRoundCog,
} from 'lucide-react';
import { Link, useParams } from 'react-router';
import { toast } from 'sonner';
import { HandoffState } from '@/components/tasks/HandoffState';
import { ReferenceList } from '@/components/tasks/ReferenceList';
import { ResumePanel } from '@/components/tasks/ResumePanel';
import { CheckpointKindBadge, TaskStatusBadge } from '@/components/tasks/TaskStatusBadge';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, CardTitle } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useCheckpoint } from '@/hooks/useTasks';
import { useT } from '@/i18n';
import { ApiException } from '@/lib/api';
import { checkpointPagePath, taskPagePath } from '@/lib/handoff';
import { formatDateTime } from '@/lib/format';

function errorText(error: unknown): string {
  if (error instanceof ApiException) return error.hint || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

export function CheckpointDetailPage() {
  const { t } = useT();
  const { taskKey = '', checkpointId = '' } = useParams<{
    taskKey: string;
    checkpointId: string;
  }>();
  const checkpoint = useCheckpoint(taskKey, checkpointId);

  if (checkpoint.isLoading) {
    return (
      <div className="mx-auto max-w-7xl space-y-5 px-8 py-9">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-36 w-full" />
        <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
          <Skeleton className="h-[640px] w-full" />
          <Skeleton className="h-80 w-full" />
        </div>
      </div>
    );
  }

  if (checkpoint.isError || !checkpoint.data) {
    return (
      <div className="mx-auto max-w-4xl px-8 py-12">
        <EmptyState
          icon={<GitCommitHorizontal />}
          title={t('checkpoint.notFound')}
          description={checkpoint.isError ? errorText(checkpoint.error) : t('checkpoint.notFoundHint')}
          action={
            <div className="flex gap-2">
              <Link to={taskPagePath(taskKey)}>
                <Button variant="secondary" size="sm">{t('task.backToTask')}</Button>
              </Link>
              {checkpoint.isError && (
                <Button variant="ghost" size="sm" onClick={() => checkpoint.refetch()}>
                  {t('common.retry')}
                </Button>
              )}
            </div>
          }
        />
      </div>
    );
  }

  const record = checkpoint.data;
  const state = record.handoff.state;
  const copyID = async () => {
    try {
      await navigator.clipboard.writeText(record.id);
      toast.success(t('checkpoint.copied'));
    } catch {
      toast.error(t('checkpoint.copyFailed'));
    }
  };

  return (
    <div className="mx-auto max-w-7xl px-8 py-9">
      <Link
        to={taskPagePath(taskKey)}
        className="mb-5 inline-flex items-center gap-1.5 text-sm text-fg-muted transition-colors hover:text-fg"
      >
        <ArrowLeft className="h-3.5 w-3.5" />
        {t('task.backToTask')}
      </Link>

      <header className="mb-7 border-l-2 border-accent pl-4">
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <span className="font-mono text-2xs uppercase tracking-[0.16em] text-fg-subtle">
            {taskKey}
          </span>
          <span className="text-fg-subtle">/</span>
          <span className="font-mono text-2xs text-fg-muted">
            #{String(record.sequence).padStart(4, '0')}
          </span>
          <TaskStatusBadge status={state.status} />
          <CheckpointKindBadge kind={record.checkpoint_kind} />
        </div>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h1 className="text-2xl font-semibold tracking-tight">{t('checkpoint.title')}</h1>
            <p className="mt-1.5 max-w-3xl text-sm leading-6 text-fg-muted">
              {state.progress.summary}
            </p>
          </div>
          <Button variant="ghost" size="sm" onClick={copyID}>
            <Copy className="h-3.5 w-3.5" />
            {t('checkpoint.copyId')}
          </Button>
        </div>
      </header>

      <div className="mb-6">
        <ResumePanel taskKey={taskKey} checkpointID={record.id} />
      </div>

      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
        <HandoffState state={state} />

        <aside className="space-y-4 lg:sticky lg:top-20">
          <Card>
            <CardHeader>
              <CardTitle>{t('checkpoint.provenance')}</CardTitle>
            </CardHeader>
            <CardBody className="space-y-4 text-xs">
              <Meta
                icon={<UserRoundCog />}
                label={t('checkpoint.producer')}
                value={record.producer_session
                  ? `${record.producer_agent} / ${record.producer_session}`
                  : record.producer_agent}
              />
              <Meta icon={<Clock3 />} label={t('checkpoint.createdAt')} value={formatDateTime(record.created_at)} />
              <Meta icon={<Fingerprint />} label={t('checkpoint.scope')} value={record.scope_path} mono />
              <Meta icon={<GitCommitHorizontal />} label="SHA-256" value={record.payload_sha256} mono />
              <Meta icon={<Fingerprint />} label={t('checkpoint.id')} value={record.id} mono />
              {record.base_checkpoint_id && (
                <div>
                  <div className="mb-1 flex items-center gap-1.5 text-2xs uppercase tracking-wider text-fg-subtle">
                    <GitCommitHorizontal className="h-3 w-3" />
                    {t('checkpoint.base')}
                  </div>
                  <Link
                    to={checkpointPagePath(taskKey, record.base_checkpoint_id)}
                    className="break-all font-mono text-xs text-fg-muted transition-colors hover:text-accent"
                  >
                    {record.base_checkpoint_id}
                  </Link>
                </div>
              )}
            </CardBody>
          </Card>

          <Card>
            <CardHeader className="flex items-center justify-between">
              <CardTitle>{t('checkpoint.references')}</CardTitle>
              <span className="font-mono text-2xs text-fg-subtle">{record.references.length}</span>
            </CardHeader>
            <CardBody className="p-0">
              <ReferenceList references={record.references} />
            </CardBody>
          </Card>
        </aside>
      </div>
    </div>
  );
}

function Meta({
  icon,
  label,
  value,
  mono,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div>
      <div className="mb-1 flex items-center gap-1.5 text-2xs uppercase tracking-wider text-fg-subtle">
        <span className="[&>svg]:h-3 [&>svg]:w-3">{icon}</span>
        {label}
      </div>
      <div className={`break-all text-fg ${mono ? 'font-mono' : ''}`}>{value}</div>
    </div>
  );
}
