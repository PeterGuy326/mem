/**
 * RAG cross-file question answering (SPEC §F5).
 * Posts to /v1/ask, renders the answer plus the citation list.
 * Thinking models (qwen3.7-max) return their chain-of-thought wrapped in
 * <think>…</think>; we split it out into a collapsible "思考过程" panel.
 */
import * as React from 'react';
import { Send, Sparkles, Brain, ChevronDown, History, X } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { askQuestion, type AskResponse } from '@/lib/ai';
import { ApiException } from '@/lib/api';
import { useHistory, splitThinking } from '@/hooks/useHistory';

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
  const history = useHistory('mem.history.ask');

  async function submit(question: string) {
    if (!question.trim()) return;
    setBusy(true);
    setErr(null);
    setResp(null);
    try {
      const r = await askQuestion({ question, top_k: topK });
      setResp(r);
      history.push(question);
    } catch (e) {
      setErr(e instanceof ApiException ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const split = resp ? splitThinking(resp.answer) : null;

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
        <div className="mt-4 space-y-3">
          {history.items.length > 0 && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="inline-flex items-center gap-1 text-xs text-fg-subtle">
                <History className="h-3 w-3" /> 最近:
              </span>
              {history.items.slice(0, 6).map((s) => (
                <button
                  key={s}
                  onClick={() => {
                    setQ(s);
                    submit(s);
                  }}
                  className="group inline-flex items-center gap-1 rounded-full border border-border bg-bg-subtle
                             hover:bg-bg-inset hover:border-border-strong px-3 py-1 text-xs text-fg-muted
                             hover:text-fg transition-colors max-w-[16rem]"
                >
                  <span className="truncate">{s}</span>
                  <X
                    className="h-3 w-3 opacity-0 group-hover:opacity-60 hover:!opacity-100"
                    onClick={(e) => {
                      e.stopPropagation();
                      history.remove(s);
                    }}
                  />
                </button>
              ))}
              <button
                onClick={history.clear}
                className="text-2xs text-fg-subtle hover:text-fg underline-offset-2 hover:underline"
              >
                清空
              </button>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2">
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
        </div>
      )}

      {busy && <ThinkingIndicator />}

      {err && (
        <div className="mt-8 rounded-md border border-danger/40 bg-danger/5 px-4 py-3 text-sm text-danger">
          {err}
        </div>
      )}

      {resp && split && (
        <div className="mt-8">
          {split.thinking && <ThinkingPanel text={split.thinking} />}

          <div className="surface p-5 leading-relaxed whitespace-pre-wrap text-fg">
            {split.answer || <span className="text-fg-subtle">(empty answer)</span>}
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

/** Live "thinking…" state with an elapsed-seconds counter (thinking models are slow). */
function ThinkingIndicator() {
  const [sec, setSec] = React.useState(0);
  React.useEffect(() => {
    const t = setInterval(() => setSec((s) => s + 1), 1000);
    return () => clearInterval(t);
  }, []);
  return (
    <div className="mt-8 surface p-5">
      <div className="flex items-center gap-2 text-sm text-fg-muted">
        <Brain className="h-4 w-4 text-accent animate-pulse" />
        <span>模型思考中…</span>
        <span className="ml-auto font-mono text-2xs text-fg-subtle">{sec}s</span>
      </div>
      <div className="mt-4 space-y-2">
        <div className="h-3 w-3/4 rounded bg-bg-inset animate-pulse" />
        <div className="h-3 w-5/6 rounded bg-bg-inset animate-pulse" />
        <div className="h-3 w-2/3 rounded bg-bg-inset animate-pulse" />
      </div>
      <p className="mt-3 text-2xs text-fg-subtle">
        qwen3.7-max 是推理模型，会先思考再作答，单次约 15–30 秒。
      </p>
    </div>
  );
}

/** Collapsible panel showing the model's chain-of-thought. Collapsed by default. */
function ThinkingPanel({ text }: { text: string }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div className="mb-3 rounded-md border border-border bg-bg-subtle/60 overflow-hidden">
      <button
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center gap-2 px-4 py-2.5 text-sm text-fg-muted hover:text-fg transition-colors"
      >
        <Brain className="h-4 w-4 text-accent" />
        <span>思考过程</span>
        <span className="text-2xs text-fg-subtle">（{text.length} 字）</span>
        <ChevronDown
          className={`ml-auto h-4 w-4 transition-transform ${open ? 'rotate-180' : ''}`}
        />
      </button>
      {open && (
        <div className="px-4 pb-4 pt-1 text-xs leading-relaxed text-fg-muted whitespace-pre-wrap border-t border-border">
          {text}
        </div>
      )}
    </div>
  );
}
