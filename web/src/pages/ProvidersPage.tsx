/**
 * Provider settings page (SPEC §F8.5).
 * List current settings, set a new spec, optionally test it before saving.
 * Switching embedding to a different-dim provider kicks off a server-side
 * reindex (visible in the response).
 */
import * as React from 'react';
import { Settings, FlaskConical, Save, RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Skeleton } from '@/components/ui/Skeleton';
import {
  listProviders,
  setProvider,
  testProvider,
  type ProviderSetting,
} from '@/lib/ai';
import { ApiException } from '@/lib/api';

const SAMPLE_SPECS: Record<string, string[]> = {
  embedding: [
    'ollama:nomic-embed-text',
    'ollama:mxbai-embed-large',
    'openai:text-embedding-3-small',
    'openai:text-embedding-3-large',
  ],
  llm: [
    'ollama:llama3.1:latest',
    'openai:gpt-4o-mini',
    'openai:gpt-4o',
    'anthropic:claude-opus-4-7',
    'anthropic:claude-haiku-4-5-20251001',
  ],
  vlm: [
    'ollama:minicpm-v',
    'openai:gpt-4o-mini',
    'anthropic:claude-haiku-4-5-20251001',
  ],
};

export function ProvidersPage() {
  const [data, setData] = React.useState<{ settings: ProviderSetting[]; kinds: string[] } | null>(null);
  const [busy, setBusy] = React.useState(true);
  const [err, setErr] = React.useState<string | null>(null);
  const [editing, setEditing] = React.useState<Record<string, string>>({});
  const [savingKind, setSavingKind] = React.useState<string | null>(null);
  const [lastResult, setLastResult] = React.useState<string | null>(null);

  async function refresh() {
    setBusy(true);
    setErr(null);
    try {
      const r = await listProviders();
      setData(r);
    } catch (e) {
      setErr(e instanceof ApiException ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }
  React.useEffect(() => {
    refresh();
  }, []);

  function currentFor(kind: string): ProviderSetting | undefined {
    return data?.settings.find((s) => s.kind === kind);
  }

  async function doSave(kind: string) {
    const spec = (editing[kind] ?? '').trim();
    if (!spec) return;
    setSavingKind(kind);
    setErr(null);
    setLastResult(null);
    try {
      const r = await setProvider(kind, spec);
      let msg = `${kind} = ${r.setting.spec}`;
      if (r.setting.dim) msg += ` (dim=${r.setting.dim})`;
      if (r.dim_migration_ok) {
        const prev = r.previous_dim ?? '(none)';
        msg += ` · schema migrated ${prev} → ${r.setting.dim}`;
      }
      if (r.reindex_queued) msg += ` · reindex queued: ${r.reindex_files ?? 0} file(s)`;
      setLastResult(msg);
      setEditing((m) => {
        const { [kind]: _, ...rest } = m;
        return rest;
      });
      await refresh();
    } catch (e) {
      setErr(e instanceof ApiException ? e.message : String(e));
    } finally {
      setSavingKind(null);
    }
  }

  async function doTest(kind: string, spec?: string) {
    setErr(null);
    setLastResult(null);
    try {
      const r = await testProvider(kind, spec);
      setLastResult(`test ${kind}: ${JSON.stringify(r)}`);
    } catch (e) {
      setErr(e instanceof ApiException ? e.message : String(e));
    }
  }

  return (
    <div className="mx-auto max-w-3xl px-8 py-10">
      <div className="flex items-center gap-3 mb-1">
        <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-2">
          <Settings className="h-5 w-5 text-accent" /> Providers
        </h1>
        <Button variant="ghost" size="sm" onClick={refresh} disabled={busy}>
          <RefreshCw className={busy ? 'h-3.5 w-3.5 animate-spin' : 'h-3.5 w-3.5'} />
          Refresh
        </Button>
      </div>
      <p className="text-sm text-fg-muted mb-6">
        Pick which AI provider serves each role. Switching embedding to a different dim triggers an
        automatic schema migration + reindex (you'll see the file count in the response).
      </p>

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
          {(data?.kinds ?? ['embedding', 'llm', 'vlm']).map((kind) => {
            const cur = currentFor(kind);
            const samples = SAMPLE_SPECS[kind] ?? [];
            const draft = editing[kind] ?? cur?.spec ?? '';
            return (
              <section key={kind} className="surface p-4">
                <div className="flex items-center justify-between mb-2">
                  <div className="text-sm font-medium uppercase tracking-wider">{kind}</div>
                  <div className="text-2xs text-fg-subtle">
                    {cur ? (
                      <>
                        current: <span className="font-mono text-fg-muted">{cur.spec}</span>
                        {cur.dim ? ` · dim ${cur.dim}` : ''}
                      </>
                    ) : (
                      <span>(default)</span>
                    )}
                  </div>
                </div>

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
                    Test
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => doSave(kind)}
                    disabled={savingKind === kind || !draft || draft === cur?.spec}
                  >
                    <Save className="h-3.5 w-3.5" />
                    {savingKind === kind ? 'Saving…' : 'Save'}
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
