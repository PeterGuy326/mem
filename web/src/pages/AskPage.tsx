/**
 * RAG cross-file question answering (SPEC §F5).
 * Posts to /v1/ask, renders the answer plus the citation list.
 */
import * as React from 'react';
import { Send, Sparkles } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { askQuestion, type AskResponse } from '@/lib/ai';
import { ApiException } from '@/lib/api';

const SAMPLE_QS = [
  'what is in my rental contract?',
  '我有 python 相关的笔记吗',
  'summarize the dog memory',
];

export function AskPage() {
  const [q, setQ] = React.useState('');
  const [topK, setTopK] = React.useState(5);
  const [busy, setBusy] = React.useState(false);
  const [resp, setResp] = React.useState<AskResponse | null>(null);
  const [err, setErr] = React.useState<string | null>(null);

  async function submit(question: string) {
    if (!question.trim()) return;
    setBusy(true);
    setErr(null);
    setResp(null);
    try {
      const r = await askQuestion({ question, top_k: topK });
      setResp(r);
    } catch (e) {
      setErr(e instanceof ApiException ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mx-auto max-w-3xl px-8 py-10">
      <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-2">
        <Sparkles className="h-5 w-5 text-accent" /> Ask
      </h1>
      <p className="mt-1.5 text-sm text-fg-muted">
        Ask a question; mem retrieves the most relevant snippets and synthesizes an answer with sources.
      </p>

      <form
        className="mt-5 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          submit(q);
        }}
      >
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Ask anything about your indexed files…"
          className="flex-1 h-11 rounded-md border border-border bg-bg-panel px-4 text-sm outline-none focus:border-accent/60"
          autoFocus
        />
        <select
          value={topK}
          onChange={(e) => setTopK(Number(e.target.value))}
          className="h-11 rounded-md border border-border bg-bg-panel px-3 text-sm"
          aria-label="top-K"
        >
          {[3, 5, 8, 12].map((n) => (
            <option key={n} value={n}>
              top {n}
            </option>
          ))}
        </select>
        <Button type="submit" disabled={busy || !q.trim()}>
          <Send className="h-4 w-4" />
          Ask
        </Button>
      </form>

      {!resp && !busy && !err && (
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <span className="text-xs text-fg-subtle">Try:</span>
          {SAMPLE_QS.map((s) => (
            <button
              key={s}
              onClick={() => {
                setQ(s);
                submit(s);
              }}
              className="rounded-full border border-border bg-bg-subtle hover:bg-bg-inset hover:border-border-strong
                         px-3 py-1 text-xs text-fg-muted hover:text-fg transition-colors"
            >
              {s}
            </button>
          ))}
        </div>
      )}

      {busy && (
        <div className="mt-8 space-y-3">
          <Skeleton className="h-4 w-3/4" />
          <Skeleton className="h-4 w-5/6" />
          <Skeleton className="h-4 w-2/3" />
        </div>
      )}

      {err && (
        <div className="mt-8 rounded-md border border-danger/40 bg-danger/5 px-4 py-3 text-sm text-danger">
          {err}
        </div>
      )}

      {resp && (
        <div className="mt-8">
          <div className="surface p-5 leading-relaxed whitespace-pre-wrap text-fg">
            {resp.answer || <span className="text-fg-subtle">(empty answer)</span>}
          </div>

          {resp.sources?.length > 0 && (
            <div className="mt-6">
              <div className="text-xs uppercase tracking-wider text-fg-muted mb-2">
                Sources · {resp.sources.length}
              </div>
              <ol className="surface divide-y divide-border">
                {resp.sources.map((s, i) => (
                  <li key={s.file_id} className="p-3 text-sm">
                    <div className="flex items-center gap-2">
                      <span className="text-2xs font-mono text-fg-subtle">[{i + 1}]</span>
                      <a
                        href={`/files/${s.file_id}`}
                        className="text-fg hover:text-accent transition-colors"
                      >
                        {s.name}
                      </a>
                      <span className="ml-auto text-2xs text-fg-subtle font-mono">
                        {s.score.toFixed(3)}
                      </span>
                    </div>
                    <div className="text-2xs text-fg-muted mt-1 line-clamp-2">{s.excerpt}</div>
                  </li>
                ))}
              </ol>
            </div>
          )}

          <div className="mt-4 text-2xs text-fg-subtle">
            {resp.provider || '(worker default)'} · {resp.latency_ms} ms
          </div>
        </div>
      )}

      {!busy && !resp && !err && q && (
        <EmptyState icon={<Sparkles />} title="Press Ask" description="—" />
      )}
    </div>
  );
}
