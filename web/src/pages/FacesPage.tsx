/**
 * Face cluster management (SPEC §F6.1, F6.2).
 * List clusters → click to rename. Multi-select 2 to merge.
 */
import * as React from 'react';
import { Users, Pencil, GitMerge, RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { listFaces, nameFace, mergeFaces, type FaceCluster } from '@/lib/ai';
import { ApiException } from '@/lib/api';

export function FacesPage() {
  const [clusters, setClusters] = React.useState<FaceCluster[] | null>(null);
  const [busy, setBusy] = React.useState(true);
  const [err, setErr] = React.useState<string | null>(null);
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [renaming, setRenaming] = React.useState<string | null>(null);
  const [draftName, setDraftName] = React.useState('');

  async function refresh() {
    setBusy(true);
    setErr(null);
    try {
      const r = await listFaces();
      setClusters(r.clusters ?? []);
    } catch (e) {
      setErr(e instanceof ApiException ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }
  React.useEffect(() => {
    refresh();
  }, []);

  function toggleSelect(id: string) {
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else if (n.size < 2) n.add(id);
      return n;
    });
  }

  async function doMerge() {
    if (selected.size !== 2) return;
    const ids = Array.from(selected);
    const a = ids[0]!;
    const b = ids[1]!;
    if (!confirm(`Merge "${labelFor(b)}" into "${labelFor(a)}"?\n(${b.slice(0, 8)} → ${a.slice(0, 8)})`)) return;
    try {
      await mergeFaces(a, b);
      setSelected(new Set());
      await refresh();
    } catch (e) {
      setErr(e instanceof ApiException ? e.message : String(e));
    }
  }

  function labelFor(id: string) {
    const c = clusters?.find((x) => x.id === id);
    return c?.name || '(unnamed)';
  }

  async function doRename(id: string) {
    try {
      await nameFace(id, draftName);
      setRenaming(null);
      setDraftName('');
      await refresh();
    } catch (e) {
      setErr(e instanceof ApiException ? e.message : String(e));
    }
  }

  return (
    <div className="mx-auto max-w-4xl px-8 py-10">
      <div className="flex items-center gap-3 mb-1">
        <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-2">
          <Users className="h-5 w-5 text-accent" /> Faces
        </h1>
        <Button variant="ghost" size="sm" onClick={refresh} disabled={busy}>
          <RefreshCw className={busy ? 'h-3.5 w-3.5 animate-spin' : 'h-3.5 w-3.5'} />
          Refresh
        </Button>
        {selected.size === 2 && (
          <Button variant="primary" size="sm" onClick={doMerge} className="ml-auto">
            <GitMerge className="h-3.5 w-3.5" />
            Merge selected
          </Button>
        )}
      </div>
      <p className="text-sm text-fg-muted mb-6">
        Person clusters discovered by insightface. Pick two clusters to merge them (first selected wins).
      </p>

      {err && (
        <div className="mb-4 rounded-md border border-danger/40 bg-danger/5 px-4 py-3 text-sm text-danger">
          {err}
        </div>
      )}

      {busy && !clusters ? (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : !clusters || clusters.length === 0 ? (
        <EmptyState
          icon={<Users />}
          title="No faces yet"
          description="Upload photos with people in them; the worker will detect + cluster them automatically."
        />
      ) : (
        <ol className="surface divide-y divide-border">
          {clusters.map((c) => (
            <li
              key={c.id}
              className={
                'flex items-center gap-3 px-4 py-3 transition-colors ' +
                (selected.has(c.id) ? 'bg-accent/5' : 'hover:bg-bg-inset/60')
              }
            >
              <input
                type="checkbox"
                checked={selected.has(c.id)}
                onChange={() => toggleSelect(c.id)}
                className="h-4 w-4"
              />
              <div className="flex-1 min-w-0">
                {renaming === c.id ? (
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      doRename(c.id);
                    }}
                    className="flex gap-2"
                  >
                    <input
                      autoFocus
                      value={draftName}
                      onChange={(e) => setDraftName(e.target.value)}
                      className="h-8 rounded-md border border-border bg-bg-inset px-2 text-sm flex-1"
                      placeholder="e.g. 小明"
                    />
                    <Button size="sm" type="submit">
                      Save
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      type="button"
                      onClick={() => {
                        setRenaming(null);
                        setDraftName('');
                      }}
                    >
                      Cancel
                    </Button>
                  </form>
                ) : (
                  <div className="flex items-center gap-2">
                    <button
                      onClick={() => {
                        setRenaming(c.id);
                        setDraftName(c.name || '');
                      }}
                      className="text-fg hover:text-accent text-sm inline-flex items-center gap-1.5"
                    >
                      <Pencil className="h-3 w-3 opacity-60" />
                      {c.name || <span className="text-fg-subtle">(unnamed)</span>}
                    </button>
                    <span className="text-2xs font-mono text-fg-subtle">{c.id.slice(0, 8)}</span>
                  </div>
                )}
              </div>
              <div className="text-xs text-fg-muted tabular-nums">
                {c.face_count} face · {c.file_count} file
              </div>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}
