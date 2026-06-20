/**
 * RAG cross-file question answering (SPEC §F5).
 * Posts to /v1/ask, renders the answer plus the citation list.
 * Thinking models (qwen3.7-max) return their chain-of-thought wrapped in
 * <think>…</think>; we split it out into a collapsible "思考过程" panel.
 */
import * as React from 'react';
import { Send, Sparkles, Brain, ChevronDown, History, X, Search, Cpu, Check } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Markdown } from '@/components/ui/Markdown';
import { askQuestion, type AskResponse, type AskStep } from '@/lib/ai';
import { ApiException } from '@/lib/api';
import { useHistory, splitThinking } from '@/hooks/useHistory';
import { useT } from '@/i18n';

const SAMPLE_QS = [
  'what is in my rental contract?',
  '我有 python 相关的笔记吗',
  'summarize the dog memory',
];

export function AskPage() {
  const { t } = useT();
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
        <Sparkles className="h-5 w-5 text-accent" /> {t('ask.title')}
      </h1>
      <p className="mt-1.5 text-sm text-fg-muted">{t('ask.subtitle')}</p>

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
          placeholder={t('ask.placeholder')}
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
          {t('ask.run')}
        </Button>
      </form>

      {!resp && !busy && !err && (
        <div className="mt-4 space-y-3">
          {history.items.length > 0 && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="inline-flex items-center gap-1 text-xs text-fg-subtle">
                <History className="h-3 w-3" /> {t('common.recent')}:
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
                {t('common.clear')}
              </button>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-fg-subtle">{t('ask.try')}</span>
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
          {resp.steps && resp.steps.length > 0 && <ExecutionTrace steps={resp.steps} />}

          {split.thinking && <ThinkingPanel text={split.thinking} />}

          <div className="surface p-5 text-fg">
            {split.answer ? (
              <Markdown>{split.answer}</Markdown>
            ) : (
              <span className="text-fg-subtle">{t('ask.emptyAnswer')}</span>
            )}
          </div>

          {resp.sources?.length > 0 && (
            <div className="mt-6">
              <div className="text-xs uppercase tracking-wider text-fg-muted mb-2">
                {t('ask.sources')} · {resp.sources.length}
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
        <EmptyState icon={<Sparkles />} title={t('ask.pressAsk')} description="—" />
      )}
    </div>
  );
}

/** Completed execution trace: the real RAG pipeline stages with wall-clock cost. */
function ExecutionTrace({ steps }: { steps: AskStep[] }) {
  const { t } = useT();
  const total = steps.reduce((a, s) => a + s.duration_ms, 0);
  const stepLabel = (name: string, fallback: string) =>
    name === 'retrieve' ? t('ask.stageRetrieve') : name === 'generate' ? t('ask.stageGenerate') : fallback;
  return (
    <div className="mb-3 rounded-md border border-border bg-bg-subtle/40 p-3">
      <div className="mb-2 flex items-center gap-2 text-xs uppercase tracking-wider text-fg-muted">
        <Cpu className="h-3.5 w-3.5 text-accent" /> {t('ask.execution')}
        <span className="ml-auto font-mono text-2xs text-fg-subtle normal-case">{total} ms</span>
      </div>
      <ol className="space-y-1.5">
        {steps.map((s, i) => {
          const Icon = s.name === 'retrieve' ? Search : s.name === 'generate' ? Cpu : Check;
          return (
            <li key={i} className="flex items-center gap-2.5 text-sm">
              <span className="grid h-5 w-5 flex-none place-items-center rounded-full bg-success/15 text-success">
                <Check className="h-3 w-3" />
              </span>
              <Icon className="h-3.5 w-3.5 flex-none text-fg-muted" />
              <span className="text-fg">{stepLabel(s.name, s.label)}</span>
              <span className="text-2xs text-fg-subtle truncate">· {s.detail}</span>
              <span className="ml-auto font-mono text-2xs text-fg-subtle">{s.duration_ms} ms</span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

const PIPELINE_STAGES = [
  { icon: Search, labelKey: 'ask.stageRetrieve', hintKey: 'ask.stageRetrieveHint' },
  { icon: Cpu, labelKey: 'ask.stageGenerate', hintKey: 'ask.stageGenerateHint' },
];

/** Live pipeline progress: advances through retrieve → generate by elapsed time. */
function ThinkingIndicator() {
  const { t } = useT();
  const [sec, setSec] = React.useState(0);
  React.useEffect(() => {
    const timer = setInterval(() => setSec((s) => s + 1), 1000);
    return () => clearInterval(timer);
  }, []);
  // First ~2s feels like retrieval; after that the LLM is generating.
  const active = sec < 2 ? 0 : 1;
  return (
    <div className="mt-8 surface p-5">
      <div className="mb-3 flex items-center gap-2 text-sm text-fg-muted">
        <Cpu className="h-4 w-4 text-accent animate-pulse" />
        <span>{t('ask.running')}</span>
        <span className="ml-auto font-mono text-2xs text-fg-subtle">{sec}s</span>
      </div>
      <ol className="space-y-2">
        {PIPELINE_STAGES.map((st, i) => {
          const done = i < active;
          const running = i === active;
          const Icon = st.icon;
          return (
            <li key={i} className="flex items-start gap-2.5 text-sm">
              <span
                className={`mt-0.5 grid h-5 w-5 flex-none place-items-center rounded-full ${
                  done
                    ? 'bg-success/15 text-success'
                    : running
                      ? 'bg-accent/15 text-accent'
                      : 'bg-bg-inset text-fg-subtle'
                }`}
              >
                {done ? (
                  <Check className="h-3 w-3" />
                ) : running ? (
                  <Icon className="h-3 w-3 animate-pulse" />
                ) : (
                  <Icon className="h-3 w-3" />
                )}
              </span>
              <div className="min-w-0">
                <div className={running || done ? 'text-fg' : 'text-fg-subtle'}>
                  {t(st.labelKey)}
                  {running && <span className="ml-2 text-2xs text-accent">{t('ask.inProgress')}</span>}
                </div>
                <div className="text-2xs text-fg-subtle">{t(st.hintKey)}</div>
              </div>
            </li>
          );
        })}
      </ol>
      <p className="mt-3 text-2xs text-fg-subtle">{t('ask.thinkingNote')}</p>
    </div>
  );
}

/** Collapsible panel showing the model's chain-of-thought. Collapsed by default. */
function ThinkingPanel({ text }: { text: string }) {
  const { t } = useT();
  const [open, setOpen] = React.useState(false);
  return (
    <div className="mb-3 rounded-md border border-border bg-bg-subtle/60 overflow-hidden">
      <button
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center gap-2 px-4 py-2.5 text-sm text-fg-muted hover:text-fg transition-colors"
      >
        <Brain className="h-4 w-4 text-accent" />
        <span>{t('ask.thinking')}</span>
        <span className="text-2xs text-fg-subtle">（{t('ask.thinkingChars', { n: text.length })}）</span>
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
