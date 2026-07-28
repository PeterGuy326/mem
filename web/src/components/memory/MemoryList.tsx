import { Bot, ChevronRight, FolderClosed, Pin, ThumbsDown, ThumbsUp } from 'lucide-react';
import { Link, useLocation } from 'react-router-dom';
import { Badge, type BadgeProps } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { useT } from '@/i18n';
import { formatDateTime } from '@/lib/format';
import type { AgentMemorySummary, MemoryKind } from '@/lib/types';

const KIND_TONES: Record<MemoryKind, NonNullable<BadgeProps['tone']>> = {
  observation: 'neutral',
  decision: 'accent',
  preference: 'warn',
  task_state: 'success',
  fact: 'success',
  note: 'muted',
  artifact: 'neutral',
};

export function MemoryKindBadge({ kind }: { kind: MemoryKind }) {
  const { t } = useT();
  return <Badge tone={KIND_TONES[kind]}>{t(`memory.kind.${kind}`)}</Badge>;
}

function FeedbackSignal({ memory }: { memory: AgentMemorySummary }) {
  const { t } = useT();
  if (memory.feedback_count <= 0) return null;
  const positive = memory.feedback_score >= 0;
  const Icon = positive ? ThumbsUp : ThumbsDown;
  return (
    <span
      className={
        positive
          ? 'inline-flex items-center gap-1 text-success'
          : 'inline-flex items-center gap-1 text-danger'
      }
      title={t('memory.feedbackSummary', {
        score: memory.feedback_score,
        count: memory.feedback_count,
      })}
    >
      <Icon className="h-3 w-3" />
      <span className="font-mono">{memory.feedback_score}</span>
    </span>
  );
}

export function MemoryList({
  memories,
  selectedID,
  hasNextPage,
  isFetchingNextPage,
  onLoadMore,
}: {
  memories: AgentMemorySummary[];
  selectedID?: string;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  onLoadMore: () => void;
}) {
  const { t } = useT();
  const location = useLocation();

  return (
    <section className="surface min-w-0 overflow-hidden" aria-label={t('memory.list')}>
      <div className="flex items-center justify-between border-b border-border bg-bg-subtle/55 px-3 py-2">
        <span className="text-2xs uppercase tracking-[0.16em] text-fg-subtle">
          {t('memory.auditLog')}
        </span>
        <span className="font-mono text-2xs text-fg-subtle">
          {t('memory.loadedCount', { n: memories.length })}
        </span>
      </div>
      <ol className="divide-y divide-border">
        {memories.map((memory) => {
          const selected = memory.id === selectedID;
          return (
            <li key={memory.id} data-testid={`memory-${memory.id}`}>
              <Link
                to={`/memories/${encodeURIComponent(memory.id)}${location.search}`}
                className={
                  'group block border-l-2 px-3 py-3 transition-colors ' +
                  (selected
                    ? 'border-l-accent bg-accent/5'
                    : 'border-l-transparent hover:bg-bg-inset/45')
                }
                aria-current={selected ? 'page' : undefined}
              >
                <div className="flex min-w-0 items-center gap-1.5">
                  <MemoryKindBadge kind={memory.kind} />
                  <Badge tone={memory.lifecycle_status === 'active' ? 'success' : 'muted'} dot>
                    {t(`memory.lifecycle.${memory.lifecycle_status}`)}
                  </Badge>
                  {memory.pinned && (
                    <span className="inline-flex text-warn" title={t('memory.pinned')}>
                      <Pin className="h-3.5 w-3.5 fill-current" />
                    </span>
                  )}
                  <span className="ml-auto">
                    <FeedbackSignal memory={memory} />
                  </span>
                </div>
                <p className="mt-2 line-clamp-3 whitespace-pre-wrap break-words text-sm leading-5 text-fg">
                  {memory.excerpt}
                </p>
                <div className="mt-2 grid min-w-0 gap-1 text-2xs text-fg-subtle">
                  <span className="inline-flex min-w-0 items-center gap-1.5">
                    <FolderClosed className="h-3 w-3 flex-none" />
                    <span className="truncate font-mono">{memory.path}</span>
                  </span>
                  <span className="flex min-w-0 items-center gap-1.5">
                    <Bot className="h-3 w-3 flex-none" />
                    <span className="truncate font-mono">
                      {memory.producer_agent || t('memory.humanOrUnknown')}
                    </span>
                    <span aria-hidden>·</span>
                    <time dateTime={memory.event_at ?? memory.created_at}>
                      {formatDateTime(memory.event_at ?? memory.created_at)}
                    </time>
                    <ChevronRight className="ml-auto h-3.5 w-3.5 flex-none opacity-0 transition-opacity group-hover:opacity-100" />
                  </span>
                </div>
              </Link>
            </li>
          );
        })}
      </ol>
      {hasNextPage && (
        <div className="border-t border-border p-3 text-center">
          <Button size="sm" variant="secondary" loading={isFetchingNextPage} onClick={onLoadMore}>
            {t('memory.loadMore')}
          </Button>
        </div>
      )}
    </section>
  );
}
