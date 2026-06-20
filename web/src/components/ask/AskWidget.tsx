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
import { askQuestion, type AskResponse, type AskStep } from '@/lib/ai';
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
        className={`fixed bottom-5 right-5 z-40 grid place-items-center rounded-full text-white
                    bg-gradient-to-br from-accent-hover to-accent-muted
                    transition-all duration-200 hover:scale-105 active:scale-95
                    ${open ? 'rotate-90' : 'animate-breathe'}`}
        style={{ height: 54, width: 54 }}
      >
        {open ? <X className="h-5 w-5" /> : <Sparkles className="h-5 w-5 drop-shadow" />}
      </button>
    </>
  );
}

function AskPanel({ onClose }: { onClose: () => void }) {
  const { t } = useT();
  const [q, setQ] = React.useState('');
  const [busy, setBusy] = React.useState(false);
  const [resp, setResp] = React.useState<AskResponse | null>(null);
  const [err, setErr] = React.useState<string | null>(null);
  const history = useHistory('mem.history.ask');
  const bodyRef = React.useRef<HTMLDivElement>(null);

  async function submit(question: string) {
    const Q = question.trim();
    if (!Q || busy) return;
    setBusy(true);
    setErr(null);
    setResp(null);
    try {
      const r = await askQuestion({ question: Q, top_k: 5 });
      setResp(r);
      history.push(Q);
    } catch (e) {
      setErr(e instanceof ApiException ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  React.useEffect(() => {
    bodyRef.current?.scrollTo({ top: bodyRef.current.scrollHeight });
  }, [resp, busy]);

  const split = resp ? splitThinking(resp.answer) : null;

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
        <div className="grid h-7 w-7 place-items-center rounded-lg bg-gradient-to-br from-accent-hover to-accent-muted text-white shadow-sm">
          <Sparkles className="h-4 w-4" />
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
        {!resp && !busy && !err && (
          <div className="space-y-3 py-2">
            <div className="flex flex-col items-center gap-2 py-3 text-center">
              <div className="grid h-11 w-11 place-items-center rounded-2xl bg-gradient-to-br from-accent-hover to-accent-muted text-white shadow-glow">
                <Sparkles className="h-5 w-5" />
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

        {busy && <ThinkingIndicator />}

        {err && (
          <div className="rounded-md border border-danger/40 bg-danger/5 px-3 py-2 text-xs text-danger">
            {err}
          </div>
        )}

        {resp && split && (
          <div className="space-y-3">
            {resp.steps && resp.steps.length > 0 && <ExecutionTrace steps={resp.steps} />}
            {split.thinking && <ThinkingPanel text={split.thinking} />}
            <div className="rounded-xl border border-border/60 bg-gradient-to-b from-bg-subtle/60 to-bg-subtle/20 p-3.5 text-fg shadow-sm">
              {split.answer ? (
                <Markdown className="text-[13px] leading-relaxed">{split.answer}</Markdown>
              ) : (
                <span className="text-fg-subtle">{t('ask.emptyAnswer')}</span>
              )}
            </div>
            {resp.sources?.length > 0 && (
              <div>
                <div className="mb-1 text-2xs uppercase tracking-wider text-fg-muted">
                  {t('ask.sources')} · {resp.sources.length}
                </div>
                <ol className="space-y-1">
                  {resp.sources.map((s, i) => (
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

function ThinkingPanel({ text }: { text: string }) {
  const { t } = useT();
  const [open, setOpen] = React.useState(false);
  return (
    <div className="overflow-hidden rounded-md border border-border bg-bg-subtle/60">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-1.5 px-3 py-2 text-2xs text-fg-muted hover:text-fg"
      >
        <Brain className="h-3.5 w-3.5 text-accent" />
        {t('ask.thinking')}
        <span className="text-fg-subtle">（{t('ask.thinkingChars', { n: text.length })}）</span>
        <ChevronDown className={`ml-auto h-3.5 w-3.5 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <div className="border-t border-border px-3 py-2 text-2xs leading-relaxed text-fg-muted whitespace-pre-wrap">
          {text}
        </div>
      )}
    </div>
  );
}

function ThinkingIndicator() {
  const { t } = useT();
  const [sec, setSec] = React.useState(0);
  React.useEffect(() => {
    const timer = setInterval(() => setSec((s) => s + 1), 1000);
    return () => clearInterval(timer);
  }, []);
  const active = sec < 2 ? 0 : 1;
  const stages = [
    { icon: Search, key: 'ask.stageRetrieve' },
    { icon: Cpu, key: 'ask.stageGenerate' },
  ];
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5 text-xs text-fg-muted">
        <Cpu className="h-3.5 w-3.5 text-accent animate-pulse" />
        {t('ask.running')}
        <span className="ml-auto font-mono text-2xs text-fg-subtle">{sec}s</span>
      </div>
      {stages.map((st, i) => {
        const done = i < active;
        const running = i === active;
        const Icon = st.icon;
        return (
          <div key={i} className="flex items-center gap-1.5 text-2xs">
            <span
              className={`grid h-4 w-4 flex-none place-items-center rounded-full ${
                done ? 'bg-success/15 text-success' : running ? 'bg-accent/15 text-accent' : 'bg-bg-inset text-fg-subtle'
              }`}
            >
              {done ? <Check className="h-2.5 w-2.5" /> : <Icon className="h-2.5 w-2.5" />}
            </span>
            <span className={running || done ? 'text-fg' : 'text-fg-subtle'}>{t(st.key)}</span>
            {running && <span className="text-accent">· {t('ask.inProgress')}</span>}
          </div>
        );
      })}
      <p className="text-2xs text-fg-subtle">{t('ask.thinkingNote')}</p>
    </div>
  );
}
