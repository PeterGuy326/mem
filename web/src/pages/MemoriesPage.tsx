import * as React from 'react';
import {
  AlertTriangle,
  ArrowLeft,
  BookOpenText,
  Fingerprint,
  LockKeyhole,
  ShieldCheck,
} from 'lucide-react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { MemoryDetail } from '@/components/memory/MemoryDetail';
import { MemoryFilters, type MemoryFilterValue } from '@/components/memory/MemoryFilters';
import { MemoryList } from '@/components/memory/MemoryList';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useMemories, useMemory } from '@/hooks/useMemories';
import { useCapabilities } from '@/hooks/useWorkspace';
import { useT } from '@/i18n';
import { ApiException } from '@/lib/api';
import {
  MEMORY_KINDS,
  type AgentMemorySummary,
  type MemoryKind,
  type MemoryLifecycleFilter,
} from '@/lib/types';

function errorText(error: unknown): string {
  if (error instanceof ApiException) return error.hint || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

function readFilters(params: URLSearchParams): MemoryFilterValue {
  const rawKind = params.get('kind');
  const kind = MEMORY_KINDS.includes(rawKind as MemoryKind) ? (rawKind as MemoryKind) : undefined;
  const rawLifecycle = params.get('lifecycle');
  const lifecycle: MemoryLifecycleFilter =
    rawLifecycle === 'archived' || rawLifecycle === 'all' ? rawLifecycle : 'active';
  const rawPinned = params.get('pinned');
  const pinned = rawPinned === 'true' ? true : rawPinned === 'false' ? false : undefined;
  return {
    scope: params.get('scope') ?? '',
    kind,
    lifecycle,
    pinned,
  };
}

function uniqueMemories(items: AgentMemorySummary[]): AgentMemorySummary[] {
  const seen = new Set<string>();
  return items.filter((memory) => {
    if (seen.has(memory.id)) return false;
    seen.add(memory.id);
    return true;
  });
}

function LedgerSkeleton() {
  return (
    <div className="surface divide-y divide-border overflow-hidden">
      {Array.from({ length: 6 }).map((_, index) => (
        <div key={index} className="space-y-3 p-3">
          <div className="flex gap-2">
            <Skeleton className="h-5 w-20" />
            <Skeleton className="h-5 w-16" />
          </div>
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-4/5" />
          <Skeleton className="h-3 w-2/3" />
        </div>
      ))}
    </div>
  );
}

function DetailSkeleton() {
  return (
    <div className="space-y-4">
      <div className="surface space-y-4 p-5">
        <div className="flex gap-2">
          <Skeleton className="h-5 w-20" />
          <Skeleton className="h-5 w-16" />
        </div>
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-11/12" />
        <Skeleton className="h-4 w-3/4" />
        <Skeleton className="h-9 w-full" />
      </div>
      <div className="grid gap-4 xl:grid-cols-2">
        <Skeleton className="h-72 w-full" />
        <Skeleton className="h-72 w-full" />
      </div>
    </div>
  );
}

export function MemoriesPage() {
  const { t } = useT();
  const { memoryId } = useParams<{ memoryId?: string }>();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const capabilities = useCapabilities();
  const filters = React.useMemo(() => readFilters(searchParams), [searchParams]);
  const memories = useMemories({
    scope: filters.scope || undefined,
    kind: filters.kind,
    lifecycle: filters.lifecycle,
    pinned: filters.pinned,
    limit: 40,
  });
  const detail = useMemory(memoryId);

  const items = React.useMemo(
    () => uniqueMemories((memories.data?.pages ?? []).flatMap((page) => page.memories ?? [])),
    [memories.data],
  );
  const detailForgotten = detail.error instanceof ApiException && detail.error.status === 410;

  function changeFilters(next: MemoryFilterValue) {
    const params = new URLSearchParams();
    if (next.scope) params.set('scope', next.scope);
    if (next.kind) params.set('kind', next.kind);
    if (next.lifecycle !== 'active') params.set('lifecycle', next.lifecycle);
    if (next.pinned !== undefined) params.set('pinned', String(next.pinned));
    setSearchParams(params, { replace: true });
  }

  function returnToLedger(refresh = false) {
    if (refresh) void memories.refetch();
    navigate(
      {
        pathname: '/memories',
        search: searchParams.toString(),
      },
      { replace: true },
    );
  }

  if (capabilities.isLoading) {
    return (
      <div className="mx-auto max-w-[1500px] space-y-4 px-8 py-10">
        <Skeleton className="h-8 w-52" />
        <Skeleton className="h-4 w-[34rem] max-w-full" />
        <Skeleton className="h-20 w-full" />
        <div className="grid gap-5 xl:grid-cols-[390px_minmax(0,1fr)]">
          <LedgerSkeleton />
          <DetailSkeleton />
        </div>
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
            <Button size="sm" variant="secondary" onClick={() => capabilities.refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      </div>
    );
  }

  if (capabilities.data?.features.memory === false) {
    return (
      <div className="mx-auto max-w-4xl px-8 py-12">
        <EmptyState
          icon={<BookOpenText />}
          title={t('memory.unavailable')}
          description={t('memory.unavailableHint')}
        />
      </div>
    );
  }

  if (!capabilities.data?.permissions.read) {
    return (
      <div className="mx-auto max-w-4xl px-8 py-12">
        <EmptyState
          icon={<LockKeyhole />}
          title={t('memory.readRequired')}
          description={t('memory.readRequiredHint')}
        />
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-[1500px] px-5 py-7 lg:px-8 lg:py-9">
      <header className="mb-6 border-l-2 border-accent pl-4">
        <div className="mb-2 flex flex-wrap items-center gap-2 font-mono text-2xs uppercase tracking-[0.18em] text-fg-subtle">
          <ShieldCheck className="h-3.5 w-3.5 text-accent" />
          {t('memory.trustSurface')}
          {capabilities.data.workspace.name && (
            <>
              <span aria-hidden>·</span>
              <span>{capabilities.data.workspace.name}</span>
            </>
          )}
        </div>
        <h1 className="text-2xl font-semibold tracking-tight">{t('memory.title')}</h1>
        <p className="mt-1.5 max-w-3xl text-sm leading-6 text-fg-muted">{t('memory.subtitle')}</p>
      </header>

      <div className={memoryId ? 'hidden xl:block' : undefined}>
        <MemoryFilters value={filters} onChange={changeFilters} />
      </div>

      <div className="mt-5 grid min-w-0 gap-5 xl:grid-cols-[390px_minmax(0,1fr)] xl:items-start">
        <aside
          className={'min-w-0 xl:sticky xl:top-16 ' + (memoryId ? 'hidden xl:block' : 'block')}
        >
          {memories.isLoading ? (
            <LedgerSkeleton />
          ) : memories.isError ? (
            <EmptyState
              icon={<AlertTriangle />}
              title={t('memory.listFailed')}
              description={errorText(memories.error)}
              action={
                <Button size="sm" variant="secondary" onClick={() => memories.refetch()}>
                  {t('common.retry')}
                </Button>
              }
            />
          ) : items.length === 0 ? (
            <EmptyState
              icon={<BookOpenText />}
              title={t('memory.empty')}
              description={t('memory.emptyHint')}
              className="py-14"
            />
          ) : (
            <MemoryList
              memories={items}
              selectedID={memoryId}
              hasNextPage={Boolean(memories.hasNextPage)}
              isFetchingNextPage={memories.isFetchingNextPage}
              onLoadMore={() => void memories.fetchNextPage()}
            />
          )}
        </aside>

        <main className={'min-w-0 ' + (memoryId ? 'block' : 'hidden xl:block')}>
          {memoryId && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="mb-3 xl:hidden"
              onClick={() => returnToLedger()}
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              {t('memory.backToLedger')}
            </Button>
          )}
          {!memoryId ? (
            <EmptyState
              icon={<Fingerprint />}
              title={t('memory.selectTitle')}
              description={t('memory.selectHint')}
              className="min-h-[360px]"
            />
          ) : detail.isLoading ? (
            <DetailSkeleton />
          ) : detail.isError || !detail.data ? (
            <EmptyState
              icon={<AlertTriangle />}
              title={detailForgotten ? t('memory.forgotten') : t('memory.detailUnavailable')}
              description={detailForgotten ? t('memory.forgottenHint') : errorText(detail.error)}
              action={
                <div className="flex gap-2">
                  {!detailForgotten && (
                    <Button size="sm" variant="secondary" onClick={() => detail.refetch()}>
                      {t('common.retry')}
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" onClick={() => returnToLedger(true)}>
                    {t('memory.backToLedger')}
                  </Button>
                </div>
              }
            />
          ) : (
            <MemoryDetail
              memory={detail.data}
              onReload={() => void detail.refetch()}
              onForgotten={() => returnToLedger(true)}
            />
          )}
        </main>
      </div>
    </div>
  );
}
