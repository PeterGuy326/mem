import { ArrowLeft, FolderClosed, GitCommitHorizontal, ScrollText } from 'lucide-react';
import { Link, useParams } from 'react-router';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { ResumePanel } from '@/components/tasks/ResumePanel';
import { TaskStatusBadge } from '@/components/tasks/TaskStatusBadge';
import { TaskTimeline } from '@/components/tasks/TaskTimeline';
import { useTaskCheckpoints } from '@/hooks/useTasks';
import { useT } from '@/i18n';
import { ApiException } from '@/lib/api';

function errorText(error: unknown): string {
  if (error instanceof ApiException) return error.hint || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

export function TaskDetailPage() {
  const { t } = useT();
  const { taskKey = '' } = useParams<{ taskKey: string }>();
  const checkpoints = useTaskCheckpoints(taskKey, { limit: 50 });
  const items = checkpoints.data?.checkpoints ?? [];
  const head = items[0];

  return (
    <div className="mx-auto max-w-6xl px-8 py-9">
      <Link
        to="/tasks"
        className="mb-5 inline-flex items-center gap-1.5 text-sm text-fg-muted transition-colors hover:text-fg"
      >
        <ArrowLeft className="h-3.5 w-3.5" />
        {t('task.backToTasks')}
      </Link>

      <header className="mb-7">
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <ScrollText className="h-4 w-4 text-accent" />
          <span className="font-mono text-2xs uppercase tracking-[0.16em] text-fg-subtle">
            {t('task.taskKey')}
          </span>
          {head && <TaskStatusBadge status={head.status} />}
        </div>
        <h1 className="break-all font-mono text-2xl font-semibold text-fg">{taskKey}</h1>
        {head && (
          <div className="mt-2 flex flex-wrap gap-x-5 gap-y-2 text-xs text-fg-muted">
            <span className="inline-flex items-center gap-1.5">
              <FolderClosed className="h-3.5 w-3.5" />
              <span className="font-mono">{head.scope_path}</span>
            </span>
            <span className="inline-flex items-center gap-1.5">
              <GitCommitHorizontal className="h-3.5 w-3.5" />
              {t('task.headSequence', { n: head.sequence })}
            </span>
          </div>
        )}
      </header>

      {checkpoints.isLoading ? (
        <div className="space-y-5">
          <Skeleton className="h-40 w-full" />
          <Skeleton className="h-80 w-full" />
        </div>
      ) : checkpoints.isError ? (
        <EmptyState
          icon={<ScrollText />}
          title={t('task.loadFailed')}
          description={errorText(checkpoints.error)}
          action={
            <Button variant="secondary" size="sm" onClick={() => checkpoints.refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      ) : items.length === 0 ? (
        <EmptyState
          icon={<ScrollText />}
          title={t('task.noCheckpoints')}
          description={t('task.noCheckpointsHint')}
        />
      ) : (
        <div className="space-y-6">
          <ResumePanel taskKey={taskKey} />
          <div>
            <div className="mb-3 flex items-baseline justify-between gap-3">
              <h2 className="text-sm font-medium text-fg">{t('task.timeline')}</h2>
              <span className="font-mono text-2xs text-fg-subtle">
                {t('task.latestCount', { n: items.length })}
              </span>
            </div>
            <TaskTimeline taskKey={taskKey} checkpoints={items} />
          </div>
        </div>
      )}
    </div>
  );
}
