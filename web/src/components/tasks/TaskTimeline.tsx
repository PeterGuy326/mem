import { ArrowRight, GitCommitHorizontal } from 'lucide-react';
import { Link } from 'react-router-dom';
import { CheckpointKindBadge, TaskStatusBadge } from './TaskStatusBadge';
import { checkpointPagePath } from '@/lib/handoff';
import { formatDateTime, truncateMiddle } from '@/lib/format';
import { useT } from '@/i18n';
import type { CheckpointSummary } from '@/lib/types';

export function TaskTimeline({
  taskKey,
  checkpoints,
}: {
  taskKey: string;
  checkpoints: CheckpointSummary[];
}) {
  const { t } = useT();

  return (
    <section className="surface overflow-hidden" aria-label={t('task.timeline')}>
      <div className="grid grid-cols-[76px_minmax(0,1fr)] border-b border-border bg-bg-subtle/50">
        <div className="border-r border-border px-3 py-2 text-2xs font-mono text-fg-subtle">
          {t('task.sequence')}
        </div>
        <div className="px-4 py-2 text-2xs uppercase tracking-[0.16em] text-fg-subtle">
          {t('task.immutableLog')}
        </div>
      </div>
      <ol className="divide-y divide-border">
        {checkpoints.map((checkpoint, index) => {
          const isHead = index === 0;
          return (
            <li
              key={checkpoint.id}
              className="group grid grid-cols-[76px_minmax(0,1fr)]"
              data-testid={`checkpoint-${checkpoint.sequence}`}
            >
              <div className="relative flex items-start justify-center border-r border-border px-2 py-4">
                <div className="absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-border" />
                <div className="relative z-10 rounded bg-bg-panel px-1.5 py-0.5 font-mono text-2xs text-fg-muted">
                  #{String(checkpoint.sequence).padStart(4, '0')}
                </div>
              </div>
              <Link
                to={checkpointPagePath(taskKey, checkpoint.id)}
                className="grid gap-3 px-4 py-4 transition-colors hover:bg-bg-inset/45 md:grid-cols-[minmax(0,1fr)_auto]"
              >
                <div className="min-w-0">
                  <div className="mb-2 flex flex-wrap items-center gap-2">
                    {isHead && <span className="ledger-head-mark">{t('task.head')}</span>}
                    <TaskStatusBadge status={checkpoint.status} />
                    <CheckpointKindBadge kind={checkpoint.checkpoint_kind} />
                    <span className="text-2xs text-fg-subtle">
                      {checkpoint.producer_agent}
                    </span>
                  </div>
                  <div className="text-sm text-fg">{checkpoint.progress_excerpt}</div>
                  <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-2xs text-fg-subtle">
                    <span>{formatDateTime(checkpoint.created_at)}</span>
                    <span className="inline-flex items-center gap-1 font-mono">
                      <GitCommitHorizontal className="h-3 w-3" />
                      {truncateMiddle(checkpoint.payload_sha256, 18)}
                    </span>
                    <span>{t('task.completedCount', { n: checkpoint.completed_count })}</span>
                  </div>
                </div>
                <div className="flex items-center self-center text-fg-subtle group-hover:text-accent">
                  <ArrowRight className="h-4 w-4" />
                </div>
              </Link>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
