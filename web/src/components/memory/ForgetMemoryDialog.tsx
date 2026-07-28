import * as React from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { AlertTriangle, FileCheck2, X } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useT } from '@/i18n';
import type { AgentMemory, MemoryForgetReason } from '@/lib/types';

const CONFIRM_WORD = 'FORGET';
const REASONS: MemoryForgetReason[] = [
  'user_request',
  'incorrect',
  'sensitive',
  'expired',
  'other',
];

export function ForgetMemoryDialog({
  memory,
  open,
  busy,
  error,
  onOpenChange,
  onConfirm,
}: {
  memory: AgentMemory;
  open: boolean;
  busy: boolean;
  error?: string;
  onOpenChange: (open: boolean) => void;
  onConfirm: (reason: MemoryForgetReason) => Promise<void>;
}) {
  const { t } = useT();
  const [word, setWord] = React.useState('');
  const [reason, setReason] = React.useState<MemoryForgetReason>('user_request');

  React.useEffect(() => {
    if (open) {
      setWord('');
      setReason('user_request');
    }
  }, [open]);

  const confirmed = word === CONFIRM_WORD;

  return (
    <Dialog.Root open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-bg/80 backdrop-blur-sm animate-fade-in" />
        <Dialog.Content
          className="fixed left-1/2 top-1/2 z-50 w-[520px] max-w-[94vw] -translate-x-1/2 -translate-y-1/2
                     rounded-lg border border-danger/35 bg-bg-panel p-5 shadow-soft animate-fade-in"
        >
          <div className="flex items-start gap-3">
            <div className="grid h-9 w-9 flex-none place-items-center rounded-md border border-danger/30 bg-danger/10 text-danger">
              <AlertTriangle className="h-4 w-4" />
            </div>
            <div className="min-w-0 flex-1">
              <Dialog.Title className="text-base font-semibold text-fg">
                {t('memory.forgetTitle')}
              </Dialog.Title>
              <Dialog.Description className="mt-1.5 text-sm leading-6 text-fg-muted">
                {t('memory.forgetDescription')}
              </Dialog.Description>
            </div>
            <Dialog.Close asChild>
              <button
                type="button"
                disabled={busy}
                aria-label={t('action.close')}
                className="text-fg-subtle transition-colors hover:text-fg disabled:opacity-50"
              >
                <X className="h-4 w-4" />
              </button>
            </Dialog.Close>
          </div>

          <div className="mt-4 rounded-md border border-border bg-bg-inset p-3">
            <div className="line-clamp-3 whitespace-pre-wrap break-words text-sm text-fg">
              {memory.content}
            </div>
            <div className="mt-2 truncate font-mono text-2xs text-fg-subtle">{memory.citation}</div>
          </div>

          <div className="mt-3 flex items-start gap-2 rounded-md border border-success/25 bg-success/5 px-3 py-2.5 text-xs leading-5 text-fg-muted">
            <FileCheck2 className="mt-0.5 h-3.5 w-3.5 flex-none text-success" />
            {t('memory.sourcePreserved')}
          </div>

          <label className="mt-4 grid gap-1.5">
            <span className="text-xs font-medium text-fg-muted">{t('memory.forgetReason')}</span>
            <select
              value={reason}
              onChange={(event) => setReason(event.target.value as MemoryForgetReason)}
              className="h-9 rounded-md border border-border bg-bg-inset px-3 text-sm text-fg outline-none focus:border-accent/60"
            >
              {REASONS.map((candidate) => (
                <option key={candidate} value={candidate}>
                  {t(`memory.forgetReason.${candidate}`)}
                </option>
              ))}
            </select>
          </label>

          <label className="mt-4 grid gap-1.5">
            <span className="text-xs font-medium text-fg-muted">
              {t('memory.typeForget', { word: CONFIRM_WORD })}
            </span>
            <Input
              value={word}
              onChange={(event) => setWord(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              placeholder={CONFIRM_WORD}
              aria-label={t('memory.confirmForget')}
            />
          </label>

          {error && (
            <div
              className="mt-3 rounded-md border border-danger/35 bg-danger/5 px-3 py-2 text-xs text-danger"
              role="alert"
            >
              {error}
            </div>
          )}

          <div className="mt-5 flex justify-end gap-2">
            <Dialog.Close asChild>
              <Button type="button" variant="ghost" size="sm" disabled={busy}>
                {t('action.cancel')}
              </Button>
            </Dialog.Close>
            <Button
              type="button"
              variant="danger"
              size="sm"
              loading={busy}
              disabled={!confirmed}
              onClick={() => void onConfirm(reason)}
            >
              {t('memory.forgetPermanently')}
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
