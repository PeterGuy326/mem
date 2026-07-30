/**
 * Advanced indexing-model settings.
 * This surface intentionally excludes answer-generation models: the web
 * product is a file workspace, while these models create searchable file
 * representations in the background.
 */
import * as React from 'react';
import {
  AudioLines,
  FlaskConical,
  Image,
  RefreshCw,
  Save,
  ScanText,
  Settings,
  TextSearch,
  type LucideIcon,
} from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Skeleton } from '@/components/ui/Skeleton';
import {
  isIndexProviderKind,
  listProviders,
  setProvider,
  testProvider,
  type IndexProviderKind,
  type ProviderSetting,
} from '@/lib/ai';
import {
  getEntitlementSummary,
  presentManagedEmbeddingError,
  type EntitlementResponse,
} from '@/lib/managed-embeddings';
import { formatDateTime } from '@/lib/format';
import { useT } from '@/i18n';

const DEFAULT_INDEX_KINDS: IndexProviderKind[] = ['embedding', 'vlm'];

const KIND_DETAILS: Record<
  IndexProviderKind,
  { labelKey: string; descriptionKey: string; icon: LucideIcon }
> = {
  embedding: {
    labelKey: 'providers.kind.embedding.label',
    descriptionKey: 'providers.kind.embedding.description',
    icon: TextSearch,
  },
  vlm: {
    labelKey: 'providers.kind.vlm.label',
    descriptionKey: 'providers.kind.vlm.description',
    icon: Image,
  },
  asr: {
    labelKey: 'providers.kind.asr.label',
    descriptionKey: 'providers.kind.asr.description',
    icon: AudioLines,
  },
  ocr: {
    labelKey: 'providers.kind.ocr.label',
    descriptionKey: 'providers.kind.ocr.description',
    icon: ScanText,
  },
};

const SAMPLE_SPECS: Partial<Record<IndexProviderKind, string[]>> = {
  embedding: [
    'ollama:nomic-embed-text',
    'ollama:mxbai-embed-large',
    'openai:text-embedding-3-small',
    'openai:text-embedding-3-large',
  ],
  vlm: [
    'ollama:minicpm-v',
    'openai:gpt-4o-mini',
    'anthropic:claude-haiku-4-5-20251001',
  ],
};

export function ProvidersPage() {
  const { t } = useT();
  const [data, setData] = React.useState<{ settings: ProviderSetting[]; kinds: string[] } | null>(null);
  const [entitlement, setEntitlement] = React.useState<EntitlementResponse | null>(null);
  const [busy, setBusy] = React.useState(true);
  const [err, setErr] = React.useState<string | null>(null);
  const [entitlementErr, setEntitlementErr] = React.useState<string | null>(null);
  const [editing, setEditing] = React.useState<Record<string, string>>({});
  const [savingKind, setSavingKind] = React.useState<string | null>(null);
  const [lastResult, setLastResult] = React.useState<string | null>(null);

  const indexKinds = React.useMemo(() => {
    const advertised = data?.kinds.filter(isIndexProviderKind) ?? [];
    return advertised.length > 0 ? advertised : DEFAULT_INDEX_KINDS;
  }, [data]);

  const refresh = React.useCallback(async () => {
    setBusy(true);
    setErr(null);
    setEntitlementErr(null);
    const [providersResult, entitlementResult] = await Promise.allSettled([
      listProviders(),
      getEntitlementSummary(),
    ]);
    if (providersResult.status === 'fulfilled') {
      setData(providersResult.value);
    } else {
      setErr(localizedManagedError(providersResult.reason, t));
    }
    if (entitlementResult.status === 'fulfilled') {
      setEntitlement(entitlementResult.value);
    } else {
      // Entitlements describe the optional managed service. Their control
      // plane must never hide otherwise healthy local/BYOM provider settings.
      setEntitlement(null);
      setEntitlementErr(localizedManagedError(entitlementResult.reason, t));
    }
    setBusy(false);
  }, [t]);

  React.useEffect(() => {
    void refresh();
  }, [refresh]);

  function currentFor(kind: IndexProviderKind): ProviderSetting | undefined {
    return data?.settings.find((s) => s.kind === kind);
  }

  async function doSave(kind: IndexProviderKind) {
    const spec = (editing[kind] ?? '').trim();
    if (!spec) return;
    setSavingKind(kind);
    setErr(null);
    setLastResult(null);
    try {
      const r = await setProvider(kind, spec);
      let msg = t('providers.saved', { kind, spec: r.setting.spec });
      if (r.setting.dim) msg += ` (dim=${r.setting.dim})`;
      if (r.dim_migration_ok) {
        const previous = r.previous_dim ?? t('providers.none');
        msg += ` · ${t('providers.schemaCompatible', {
          previous,
          next: r.setting.dim ?? t('providers.none'),
        })}`;
      }
      if (r.reindex_queued) {
        msg += ` · ${t('providers.reindexQueued', { n: r.reindex_files ?? 0 })}`;
      }
      if (r.reindex_required) msg += ` · ${t('providers.rebuildRequired')}`;
      setLastResult(msg);
      setEditing((m) => {
        const { [kind]: _, ...rest } = m;
        return rest;
      });
      await refresh();
    } catch (e) {
      setErr(localizedManagedError(e, t));
    } finally {
      setSavingKind(null);
    }
  }

  async function doTest(kind: IndexProviderKind, spec?: string) {
    setErr(null);
    setLastResult(null);
    try {
      const r = await testProvider(kind, spec);
      setLastResult(t('providers.testResult', { kind, result: JSON.stringify(r) }));
    } catch (e) {
      setErr(localizedManagedError(e, t));
    }
  }

  return (
    <div className="mx-auto max-w-3xl px-8 py-10">
      <div className="flex items-center gap-3 mb-1">
        <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-2">
          <Settings className="h-5 w-5 text-accent" /> {t('providers.title')}
        </h1>
        <span className="rounded-full border border-border bg-bg-inset px-2 py-0.5 text-2xs uppercase tracking-wider text-fg-subtle">
          {t('providers.advanced')}
        </span>
        <Button variant="ghost" size="sm" onClick={refresh} disabled={busy}>
          <RefreshCw className={busy ? 'h-3.5 w-3.5 animate-spin' : 'h-3.5 w-3.5'} />
          {t('providers.refresh')}
        </Button>
      </div>
      <p className="text-sm text-fg-muted mb-6">{t('providers.description')}</p>

      {entitlement && (
        <section
          className="mb-4 rounded-md border border-border bg-bg-subtle px-4 py-3 text-sm"
          data-testid="managed-embedding-entitlement"
        >
          {entitlement.deployment_mode === 'private' ? (
            <>
              <div className="font-medium text-fg">{t('providers.private.title')}</div>
              <p className="mt-1 text-xs text-fg-muted">{t('providers.private.description')}</p>
            </>
          ) : entitlement.managed_embedding ? (
            <>
              <div className="flex items-center justify-between gap-4">
                <span className="font-medium text-fg">
                  {t('providers.managed.title', {
                    plan: entitlement.managed_embedding.plan,
                  })}
                </span>
                <span className="font-mono text-xs text-fg-muted">
                  {t('providers.managed.remaining', {
                    n: entitlement.managed_embedding.managed_embedding_units_remaining,
                  })}
                </span>
              </div>
              <p className="mt-1 text-xs text-fg-muted">
                {entitlement.upgrade_required
                  ? t('providers.managed.upgradeRequired')
                  : t('providers.managed.quotaResets', {
                      time: formatDateTime(entitlement.managed_embedding.reset_at),
                    })}
              </p>
            </>
          ) : (
            <div className="text-fg-muted">{t('providers.managed.unavailable')}</div>
          )}
        </section>
      )}

      {entitlementErr && (
        <div
          className="mb-4 rounded-md border border-border bg-bg-subtle px-4 py-3 text-sm text-fg-muted"
          data-testid="managed-embedding-entitlement-error"
        >
          {t('providers.managed.unavailableDetail', { error: entitlementErr })}
        </div>
      )}

      {err && (
        <div className="mb-4 rounded-md border border-danger/40 bg-danger/5 px-4 py-3 text-sm text-danger">
          {err}
        </div>
      )}
      {lastResult && (
        <div className="mb-4 rounded-md border border-accent/40 bg-accent/5 px-4 py-3 text-xs font-mono text-fg">
          {lastResult}
        </div>
      )}

      {busy && !data ? (
        <div className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-24 w-full" />
          ))}
        </div>
      ) : (
        <div className="space-y-4">
          {indexKinds.map((kind) => {
            const cur = currentFor(kind);
            const samples = SAMPLE_SPECS[kind] ?? [];
            const draft = editing[kind] ?? cur?.spec ?? '';
            const detail = KIND_DETAILS[kind];
            const KindIcon = detail.icon;
            return (
              <section key={kind} className="surface p-4">
                <div className="flex items-center justify-between mb-2">
                  <div className="flex min-w-0 items-start gap-2.5">
                    <div className="mt-0.5 grid h-8 w-8 flex-none place-items-center rounded-md border border-border bg-bg-inset text-accent">
                      <KindIcon className="h-4 w-4" />
                    </div>
                    <div>
                      <div className="text-sm font-medium">{t(detail.labelKey)}</div>
                      <div className="mt-0.5 text-2xs font-mono text-fg-subtle">{kind}</div>
                    </div>
                  </div>
                  <div className="text-2xs text-fg-subtle">
                    {cur ? (
                      <>
                        {t('providers.current')}{' '}
                        <span className="font-mono text-fg-muted">{cur.spec}</span>
                        {cur.dim ? ` · dim ${cur.dim}` : ''}
                      </>
                    ) : (
                      <span>({t('providers.default')})</span>
                    )}
                  </div>
                </div>
                <p className="mb-3 pl-[42px] text-xs text-fg-muted">{t(detail.descriptionKey)}</p>

                <div className="flex gap-2 items-stretch">
                  <input
                    value={draft}
                    onChange={(e) =>
                      setEditing((m) => ({ ...m, [kind]: e.target.value }))
                    }
                    placeholder="vendor:model"
                    className="flex-1 h-9 rounded-md border border-border bg-bg-inset px-3 text-sm font-mono outline-none focus:border-accent/60"
                  />
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => doTest(kind, draft)}
                    disabled={!draft}
                  >
                    <FlaskConical className="h-3.5 w-3.5" />
                    {t('providers.test')}
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => doSave(kind)}
                    disabled={savingKind === kind || !draft || draft === cur?.spec}
                  >
                    <Save className="h-3.5 w-3.5" />
                    {savingKind === kind ? t('providers.saving') : t('providers.save')}
                  </Button>
                </div>

                {samples.length > 0 && (
                  <div className="mt-3 flex flex-wrap gap-1.5">
                    {samples.map((s) => (
                      <button
                        key={s}
                        onClick={() =>
                          setEditing((m) => ({ ...m, [kind]: s }))
                        }
                        className="rounded-full border border-border bg-bg-subtle hover:bg-bg-inset
                                   hover:border-border-strong px-2 py-0.5 text-2xs font-mono
                                   text-fg-muted hover:text-fg transition-colors"
                      >
                        {s}
                      </button>
                    ))}
                  </div>
                )}
              </section>
            );
          })}
        </div>
      )}
    </div>
  );
}

function localizedManagedError(
  error: unknown,
  t: (key: string, vars?: Record<string, string | number>) => string,
): string {
  const presentation = presentManagedEmbeddingError(error);
  const title = t(`providers.error.${presentation.kind}.title`);
  const message =
    presentation.kind === 'unknown' && presentation.hint
      ? presentation.hint
      : t(`providers.error.${presentation.kind}.message`);
  return `${title}: ${message}`;
}
