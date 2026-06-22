/**
 * Floating assistant bubble + compact Ask panel, mounted globally so you can
 * ask about your drive from any page (Drive / Search / detail) without leaving
 * what you're doing. Replaces the dedicated full-page Ask.
 */
import * as React from 'react';
import {
  Sparkles,
  Send,
  X,
  Brain,
  ChevronDown,
  Cpu,
  Search,
  Check,
  Loader2,
} from 'lucide-react';
import { Markdown } from '@/components/ui/Markdown';
import { BotFace } from './BotFace';
import { askQuestion, streamAsk, type AskStep, type AskSource } from '@/lib/ai';
import { ApiException } from '@/lib/api';
import { useHistory, splitThinking } from '@/hooks/useHistory';
import { useAuth } from '@/hooks/useAuth';
import { useT } from '@/i18n';

export function AskWidget() {
  const { token } = useAuth();
  const { t } = useT();
  const [open, setOpen] = React.useState(false);

  // Don't show the assistant on the login screen / when signed out.
  if (!token) return null;

  return (
    <>
      {open && <AskPanel onClose={() => setOpen(false)} />}
      <button
        onClick={() => setOpen((o) => !o)}
        aria-label={t('ask.title')}
        className={`group fixed bottom-5 right-5 z-40 grid place-items-center rounded-2xl text-white
                    bg-gradient-to-br from-accent-hover via-accent to-accent-muted
                    transition-all duration-300 hover:scale-105 active:scale-95
                    ${open ? 'rotate-3' : 'animate-breathe'}`}
        style={{ height: 56, width: 56 }}
      >
        {open ? (
          <X className="h-5 w-5" />
        ) : (
          <BotFace size={34} awake className="drop-shadow transition-transform group-hover:scale-110" />
        )}
      </button>
    </>
  );
}

function AskPanel({ onClose }: { onClose: () => void }) {
  const { t } = useT();
  const [q, setQ] = React.useState('');
  const [busy, setBusy] = React.useState(false);
  const [steps, setSteps] = React.useState<AskStep[]>([]);
  const [thinking, setThinking] = React.useState('');
  const [answer, setAnswer] = React.useState('');
  const [sources, setSources] = React.useState<AskSource[]>([]);
  const [err, setErr] = React.useState<string | null>(null);
  const history = useHistory('mem.history.ask');
  const bodyRef = React.useRef<HTMLDivElement>(null);
  const abortRef = React.useRef<AbortController | null>(null);

  async function submit(question: string) {
    const Q = question.trim();
    if (!Q || busy) return;
    setBusy(true);
    setErr(null);
    setSteps([]);
    setThinking('');
    setAnswer('');
    setSources([]);
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    try {
      await streamAsk(
        { question: Q, top_k: 5 },
        (ev) => {
          if (ev.type === 'step' && ev.step) setSteps((p) => [...p, ev.step!]);
          else if (ev.type === 'thinking') setThinking((p) => p + (ev.delta ?? ''));
          else if (ev.type === 'answer') setAnswer((p) => p + (ev.delta ?? ''));
          else if (ev.type === 'sources') setSources(ev.sources ?? []);
          else if (ev.type === 'error') setErr(ev.error ?? 'error');
        },
        ac.signal,
      );
      history.push(Q);
    } catch (e) {
      // Fall back to the non-streaming endpoint if streaming fails.
      try {
        const r = await askQuestion({ question: Q, top_k: 5 });
        const split = splitThinking(r.answer);
        if (split.thinking) setThinking(split.thinking);
        setAnswer(split.answer);
        setSources(r.sources);
        setSteps(r.steps ?? []);
        history.push(Q);
      } catch (e2) {
        setErr(e2 instanceof ApiException ? e2.message : String(e2));
        void e;
      }
    } finally {
      setBusy(false);
    }
  }

  React.useEffect(() => {
    bodyRef.current?.scrollTo({ top: bodyRef.current.scrollHeight });
  }, [steps, thinking, answer, busy]);

  React.useEffect(() => () => abortRef.current?.abort(), []);

  const hasContent = busy || steps.length > 0 || !!thinking || !!answer;

  return (
    <div
      className="fixed bottom-[5.5rem] right-5 z-40 flex w-[min(390px,calc(100vw-2.5rem))] flex-col
                 overflow-hidden rounded-2xl border border-border/80 bg-bg-panel/95 backdrop-blur-xl
                 shadow-2xl ring-1 ring-black/20 animate-slide-up"
      style={{ maxHeight: 'min(580px, calc(100vh - 8rem))' }}
    >
      {/* Header */}
      <div className="relative flex items-center gap-2.5 px-4 py-3
                      bg-gradient-to-r from-accent/15 via-accent/5 to-transparent border-b border-border/70">
        <div className="grid h-8 w-8 place-items-center rounded-xl bg-gradient-to-br from-accent-hover to-accent-muted text-white shadow-sm">
          <BotFace size={22} awake />
        </div>
        <div className="leading-tight">
          <div className="text-sm font-semibold">{t('ask.title')}</div>
          <div className="text-2xs text-fg-subtle">mem · AI</div>
        </div>
        <button
          onClick={onClose}
          className="ml-auto rounded-md p-1.5 text-fg-muted hover:bg-bg-inset hover:text-fg transition-colors"
          aria-label="close"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      {/* Body */}
      <div ref={bodyRef} className="flex-1 overflow-y-auto px-4 py-3.5 text-sm">
        {!hasContent && !err && (
          <div className="space-y-3 py-2">
            <div className="flex flex-col items-center gap-2.5 py-3 text-center">
              <div className="grid h-14 w-14 place-items-center rounded-2xl bg-gradient-to-br from-accent-hover via-accent to-accent-muted text-white shadow-glow">
                <BotFace size={40} awake />
              </div>
              <p className="max-w-[16rem] text-xs leading-relaxed text-fg-muted">{t('ask.subtitle')}</p>
            </div>
            {history.items.length > 0 && (
              <div className="space-y-1.5">
                <div className="text-2xs uppercase tracking-wider text-fg-subtle">{t('common.recent')}</div>
                <div className="flex flex-col gap-1.5">
                  {history.items.slice(0, 4).map((s) => (
                    <button
                      key={s}
                      onClick={() => submit(s)}
                      className="group flex items-center gap-2 truncate rounded-lg border border-border/70 bg-bg-subtle/60 px-3 py-2
                                 text-xs text-fg-muted hover:text-fg hover:border-accent/40 hover:bg-bg-inset transition-colors"
                    >
                      <Sparkles className="h-3 w-3 flex-none text-fg-subtle group-hover:text-accent" />
                      <span className="truncate">{s}</span>
                    </button>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}

        {err && (
          <div className="rounded-md border border-danger/40 bg-danger/5 px-3 py-2 text-xs text-danger">
            {err}
          </div>
        )}

        {hasContent && (
          <div className="space-y-3">
            {steps.length > 0 && <ExecutionTrace steps={steps} />}

            {/* Waiting for the first token after retrieval. */}
            {busy && steps.length > 0 && !thinking && !answer && (
              <div className="flex items-center gap-2 text-xs text-fg-muted">
                <Cpu className="h-3.5 w-3.5 animate-pulse text-accent" />
                {t('ask.running')}
              </div>
            )}

            {thinking && <ThinkingPanel text={thinking} streaming={busy && !answer} />}

            {answer && (
              <div className="rounded-xl border border-border/60 bg-gradient-to-b from-bg-subtle/60 to-bg-subtle/20 p-3.5 text-fg shadow-sm">
                <Markdown className="text-[13px] leading-relaxed">{answer}</Markdown>
                {busy && <span className="ml-0.5 inline-block h-3.5 w-1.5 animate-pulse bg-accent align-middle" />}
              </div>
            )}

            {answer && sources.length > 0 && (
              <div>
                <div className="mb-1 text-2xs uppercase tracking-wider text-fg-muted">
                  {t('ask.sources')} · {sources.length}
                </div>
                <ol className="space-y-1">
                  {sources.map((s, i) => (
                    <li key={s.file_id} className="flex items-center gap-1.5 text-2xs">
                      <span className="font-mono text-fg-subtle">[{i + 1}]</span>
                      <a href={`/files/${s.file_id}`} className="truncate text-fg hover:text-accent">
                        {s.name}
                      </a>
                      <span className="ml-auto font-mono text-fg-subtle">{s.score.toFixed(2)}</span>
                    </li>
                  ))}
                </ol>
              </div>
            )}
          </div>
        )}
      </div>

      {/* Input */}
      <form
        className="flex items-center gap-2 border-t border-border/70 bg-bg-panel/80 p-2.5"
        onSubmit={(e) => {
          e.preventDefault();
          submit(q);
          setQ('');
        }}
      >
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder={t('ask.placeholder')}
          autoFocus
          className="h-10 flex-1 rounded-full border border-border bg-bg-inset px-4 text-[13px]
                     outline-none transition-colors focus:border-accent/60 focus:ring-2 focus:ring-accent/15"
        />
        <button
          type="submit"
          disabled={busy || !q.trim()}
          className="grid h-10 w-10 flex-none place-items-center rounded-full text-white shadow-sm
                     bg-gradient-to-br from-accent-hover to-accent-muted transition-all
                     hover:scale-105 active:scale-95 disabled:opacity-40 disabled:hover:scale-100"
          aria-label={t('ask.run')}
        >
          {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />}
        </button>
      </form>
    </div>
  );
}

function ExecutionTrace({ steps }: { steps: AskStep[] }) {
  const { t } = useT();
  const label = (name: string, fb: string) =>
    name === 'retrieve' ? t('ask.stageRetrieve') : name === 'generate' ? t('ask.stageGenerate') : fb;
  return (
    <div className="rounded-md border border-border bg-bg-subtle/40 p-2.5">
      <div className="mb-1.5 flex items-center gap-1.5 text-2xs uppercase tracking-wider text-fg-muted">
        <Cpu className="h-3 w-3 text-accent" /> {t('ask.execution')}
      </div>
      <ol className="space-y-1">
        {steps.map((s, i) => {
          const Icon = s.name === 'retrieve' ? Search : Cpu;
          return (
            <li key={i} className="flex items-center gap-1.5 text-2xs">
              <Check className="h-3 w-3 flex-none text-success" />
              <Icon className="h-3 w-3 flex-none text-fg-muted" />
              <span className="text-fg">{label(s.name, s.label)}</span>
              <span className="ml-auto font-mono text-fg-subtle">{s.duration_ms} ms</span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

/** Thinking panel — a quiet accessory, not the star. While the model is
 *  reasoning it shows a compact, dimmed live transcript; the moment the answer
 *  starts it folds into a one-line "thought for N" pill (expand to revisit). */
function ThinkingPanel({ text, streaming }: { text: string; streaming?: boolean }) {
  const { t } = useT();
  const [open, setOpen] = React.useState(false);
  // Follow the stream live; once it stops, fold it away unless the user opened it.
  const [userToggled, setUserToggled] = React.useState(false);
  const expanded = userToggled ? open : !!streaming;
  const innerRef = React.useRef<HTMLDivElement>(null);
  React.useEffect(() => {
    if (streaming && innerRef.current) innerRef.current.scrollTop = innerRef.current.scrollHeight;
  }, [text, streaming]);
  return (
    <div className="overflow-hidden rounded-lg border border-border/60 bg-bg-subtle/30">
      <button
        onClick={() => {
          setUserToggled(true);
          setOpen(!expanded);
        }}
        className="flex w-full items-center gap-1.5 px-3 py-1.5 text-2xs text-fg-subtle hover:text-fg transition-colors"
      >
        <Brain className={`h-3 w-3 ${streaming ? 'text-accent animate-pulse' : 'text-fg-subtle'}`} />
        {streaming ? (
          <span className="text-fg-muted">{t('ask.thinking')} · {t('ask.inProgress')}</span>
        ) : (
          <span>
            {t('ask.thinking')} <span className="opacity-60">（{t('ask.thinkingChars', { n: text.length })}）</span>
          </span>
        )}
        <ChevronDown className={`ml-auto h-3 w-3 transition-transform ${expanded ? 'rotate-180' : ''}`} />
      </button>
      {expanded && (
        <div className="relative border-t border-border/50">
          {/* top fade so the scrolling reasoning bleeds out gently */}
          <div className="pointer-events-none absolute inset-x-0 top-0 h-4 bg-gradient-to-b from-bg-panel/80 to-transparent" />
          <div
            ref={innerRef}
            className="max-h-28 overflow-y-auto px-3 py-2 text-2xs leading-relaxed text-fg-subtle/80 whitespace-pre-wrap"
          >
            {text}
            {streaming && <span className="ml-0.5 inline-block h-2.5 w-1 animate-pulse bg-accent/70 align-middle" />}
          </div>
        </div>
      )}
    </div>
  );
}
