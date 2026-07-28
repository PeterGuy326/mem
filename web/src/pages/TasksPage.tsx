import {
  ArrowRight,
  Clock3,
  FolderClosed,
  GitCommitHorizontal,
  ScrollText,
} from 'lucide-react';
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useTasks } from '@/hooks/useTasks';
import { useCapabilities } from '@/hooks/useWorkspace';
import { useT } from '@/i18n';
import { ApiException } from '@/lib/api';
import { taskPagePath } from '@/lib/handoff';
import { formatDateTime, truncateMiddle } from '@/lib/format';
import type { AgentTask } from '@/lib/types';

function errorText(error: unknown): string {
  if (error instanceof ApiException) return error.hint || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

function TaskRow({ task }: { task: AgentTask }) {
  const { t } = useT();
  return (
    <li className="group" data-testid={`task-${task.task_key}`}>
      <Link
        to={taskPagePath(task.task_key)}
        className="grid gap-3 px-4 py-4 transition-colors hover:bg-bg-inset/45 md:grid-cols-[minmax(0,1.3fr)_minmax(180px,.7fr)_150px_28px] md:items-center"
      >
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <ScrollText className="h-4 w-4 flex-none text-accent" />
            <span className="truncate font-mono text-sm text-fg">{task.task_key}</span>
          </div>
          <div className="mt-1.5 flex items-center gap-1.5 text-2xs text-fg-subtle">
            <FolderClosed className="h-3 w-3" />
            <span className="truncate font-mono">{task.scope_path}</span>
          </div>
        </div>
        <div>
          <div className="text-2xs uppercase tracking-wider text-fg-subtle">
            {t('task.headCheckpoint')}
          </div>
          <div className="mt-1 flex items-center gap-2 font-mono text-xs text-fg-muted">
            <span>#{String(task.head_sequence).padStart(4, '0')}</span>
            {task.head_checkpoint_id && (
              <span className="text-fg-subtle">
                {truncateMiddle(task.head_checkpoint_id, 14)}
              </span>
            )}
          </div>
        </div>
        <div>
          <div className="text-2xs uppercase tracking-wider text-fg-subtle">
            {t('task.updated')}
          </div>
          <div className="mt-1 flex items-center gap-1.5 text-xs text-fg-muted">
            <Clock3 className="h-3 w-3" />
            {formatDateTime(task.updated_at)}
          </div>
        </div>
        <ArrowRight className="hidden h-4 w-4 text-fg-subtle group-hover:text-accent md:block" />
      </Link>
    </li>
  );
}

function TasksSkeleton() {
  return (
    <div className="surface divide-y divide-border overflow-hidden">
      {Array.from({ length: 5 }).map((_, index) => (
        <div key={index} className="grid gap-4 px-4 py-4 md:grid-cols-[1.3fr_.7fr_150px]">
          <div className="space-y-2">
            <Skeleton className="h-4 w-56 max-w-full" />
            <Skeleton className="h-3 w-36" />
          </div>
          <Skeleton className="h-8 w-28" />
          <Skeleton className="h-8 w-32" />
        </div>
      ))}
    </div>
  );
}

export function TasksPage() {
  const { t } = useT();
  const capabilities = useCapabilities();
  const tasks = useTasks({ limit: 100 });
  const items = tasks.data?.tasks ?? [];

  return (
    <div className="mx-auto max-w-6xl px-8 py-10">
      <header className="mb-7 border-l-2 border-accent pl-4">
        <div className="mb-2 flex flex-wrap items-center gap-2 font-mono text-2xs uppercase tracking-[0.18em] text-fg-subtle">
          <GitCommitHorizontal className="h-3.5 w-3.5 text-accent" />
          {t('task.portableLedger')}
          {capabilities.data?.workspace.name && (
            <>
              <span aria-hidden>·</span>
              <span>{capabilities.data.workspace.name}</span>
            </>
          )}
        </div>
        <h1 className="text-2xl font-semibold tracking-tight">{t('task.title')}</h1>
        <p className="mt-1.5 max-w-2xl text-sm leading-6 text-fg-muted">
          {t('task.subtitle')}
        </p>
      </header>

      {tasks.isLoading ? (
        <TasksSkeleton />
      ) : tasks.isError ? (
        <EmptyState
          icon={<ScrollText />}
          title={t('task.loadFailed')}
          description={errorText(tasks.error)}
          action={
            <Button variant="secondary" size="sm" onClick={() => tasks.refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      ) : items.length === 0 ? (
        <EmptyState
          icon={<ScrollText />}
          title={t('task.empty')}
          description={t('task.emptyHint')}
        />
      ) : (
        <section className="surface overflow-hidden">
          <div className="grid grid-cols-[minmax(0,1fr)_auto] border-b border-border bg-bg-subtle/50 px-4 py-2">
            <span className="text-2xs uppercase tracking-[0.16em] text-fg-subtle">
              {t('task.immutableLog')}
            </span>
            <span className="font-mono text-2xs text-fg-subtle">
              {t('task.taskCount', { n: items.length })}
            </span>
          </div>
          <ol className="divide-y divide-border">
            {items.map((task) => <TaskRow key={task.id} task={task} />)}
          </ol>
        </section>
      )}
    </div>
  );
}
