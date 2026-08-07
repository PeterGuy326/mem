import { AlertTriangle, LockKeyhole, ScrollText } from 'lucide-react';
import { Outlet } from 'react-router';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useCapabilities } from '@/hooks/useWorkspace';
import { useT } from '@/i18n';
import { ApiException } from '@/lib/api';

function errorText(error: unknown): string {
  if (error instanceof ApiException) return error.hint || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

export function HandoffGate() {
  const { t } = useT();
  const capabilities = useCapabilities();

  if (capabilities.isLoading) {
    return (
      <div className="mx-auto max-w-6xl space-y-4 px-8 py-10">
        <Skeleton className="h-8 w-52" />
        <Skeleton className="h-4 w-96 max-w-full" />
        <Skeleton className="mt-8 h-64 w-full" />
      </div>
    );
  }

  if (capabilities.isError) {
    return (
      <div className="mx-auto max-w-4xl px-8 py-12">
        <EmptyState
          icon={<AlertTriangle />}
          title={t('capabilities.failed')}
          description={errorText(capabilities.error)}
          action={
            <Button variant="secondary" size="sm" onClick={() => capabilities.refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      </div>
    );
  }

  if (!capabilities.data?.features?.handoff) {
    return (
      <div className="mx-auto max-w-4xl px-8 py-12">
        <EmptyState
          icon={<ScrollText />}
          title={t('capabilities.handoffUnavailable')}
          description={t('capabilities.handoffUnavailableHint')}
        />
      </div>
    );
  }

  if (!capabilities.data.permissions.read) {
    return (
      <div className="mx-auto max-w-4xl px-8 py-12">
        <EmptyState
          icon={<LockKeyhole />}
          title={t('capabilities.readRequired')}
          description={t('capabilities.readRequiredHint')}
        />
      </div>
    );
  }

  return <Outlet />;
}
