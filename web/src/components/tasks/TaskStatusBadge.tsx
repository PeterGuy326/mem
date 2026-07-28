import { Badge } from '@/components/ui/Badge';
import { useT } from '@/i18n';
import type { CheckpointKind, TaskStatus } from '@/lib/types';

const STATUS_TONE: Record<TaskStatus, 'accent' | 'success' | 'warn' | 'muted'> = {
  in_progress: 'accent',
  ready: 'success',
  blocked: 'warn',
  complete: 'muted',
};

export function TaskStatusBadge({ status }: { status: TaskStatus }) {
  const { t } = useT();
  return (
    <Badge tone={STATUS_TONE[status]} dot>
      {t(`task.status.${status}`)}
    </Badge>
  );
}

export function CheckpointKindBadge({ kind }: { kind: CheckpointKind }) {
  const { t } = useT();
  return (
    <Badge tone={kind === 'handoff' ? 'accent' : 'neutral'}>
      {t(`task.kind.${kind}`)}
    </Badge>
  );
}
