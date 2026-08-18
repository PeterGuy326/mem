import * as React from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { GitMerge, PencilLine, X } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useCreateMemoryRelation } from '@/hooks/useMemories';
import { useT } from '@/i18n';
import { ApiException } from '@/lib/api';
import { truncateMiddle } from '@/lib/format';
import type { AgentMemory, AgentMemorySummary, MemoryRelationType } from '@/lib/types';

const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

type WritableRelationType = Extract<MemoryRelationType, 'supersedes' | 'corrects'>;

export function CreateRelationDialog({
  memory,
  relationType,
  open,
  candidates,
  onOpenChange,
}: {
  memory: AgentMemory;
  relationType: WritableRelationType;
  open: boolean;
  candidates: AgentMemorySummary[];
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT();
  const createRelation = useCreateMemoryRelation();
  // 'in' = the selected peer supersedes/corrects this memory (the common
  // archival flow); 'out' = this memory supersedes/corrects the peer.
  const [direction, setDirection] = React.useState<'in' | 'out'>('in');
  const [peerMode, setPeerMode] = React.useState<'list' | 'manual'>('list');
  const [selectedPeerID, setSelectedPeerID] = React.useState('');
  const [manualPeerID, setManualPeerID] = React.useState('');
  const [reason, setReason] = React.useState('');
  const [error, setError] = React.useState<string | null>(null);

  const busy = createRelation.isPending;

  React.useEffect(() => {
    if (open) {
      setDirection('in');
      setPeerMode('list');
      setSelectedPeerID('');
      setManualPeerID('');
      setReason('');
      setError(null);
    }
  }, [open, relationType]);

  const peers = React.useMemo(
    () => candidates.filter((candidate) => candidate.id !== memory.id),
    [candidates, memory.id],
  );
  const peerID = (peerMode === 'list' ? selectedPeerID : manualPeerID.trim()).toLowerCase();

  function relationErrorText(err: unknown): string {
    if (err instanceof ApiException) {
      if (err.status === 409) return t('memories.relationDialog.conflict');
      if (err.status === 404) return t('memories.relationDialog.notFound');
      if (err.status === 410) return t('memories.relationDialog.forgotten');
      if (err.hint) return err.hint;
    }
    return t('memories.relationDialog.failed');
  }

  async function submit() {
    setError(null);
    if (!peerID) {
      setError(t('memories.relationDialog.peerRequired'));
      return;
    }
    if (!UUID_PATTERN.test(peerID)) {
      setError(t('memories.relationDialog.peerInvalid'));
      return;
    }
    const sourceID = direction === 'out' ? memory.id : peerID;
    const targetID = direction === 'out' ? peerID : memory.id;
    try {
      await createRelation.mutateAsync({
        source_id: sourceID,
        target_id: targetID,
        relation_type: relationType,
        reason: reason.trim() || undefined,
      });
      toast.success(t(`memories.relationSuccess.${relationType}`));
      onOpenChange(false);
    } catch (err) {
      setError(relationErrorText(err));
    }
  }

  const Icon = relationType === 'supersedes' ? GitMerge : PencilLine;

  return (
    <Dialog.Root open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-bg/80 backdrop-blur-sm animate-fade-in" />
        <Dialog.Content
          className="fixed left-1/2 top-1/2 z-50 w-[560px] max-w-[94vw] -translate-x-1/2 -translate-y-1/2
                     rounded-lg border border-border bg-bg-panel p-5 shadow-soft animate-fade-in"
        >
          <div className="flex items-start gap-3">
            <div className="grid h-9 w-9 flex-none place-items-center rounded-md border border-accent/30 bg-accent/10 text-accent">
              <Icon className="h-4 w-4" />
            </div>
            <div className="min-w-0 flex-1">
              <Dialog.Title className="text-base font-semibold text-fg">
                {t(`memories.relationDialog.title.${relationType}`)}
              </Dialog.Title>
              <Dialog.Description className="mt-1.5 text-sm leading-6 text-fg-muted">
                {t('memories.relationDialog.description')}
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
            <div className="line-clamp-2 whitespace-pre-wrap break-words text-sm text-fg">
              {memory.content}
            </div>
            <div className="mt-2 truncate font-mono text-2xs text-fg-subtle">
              {truncateMiddle(memory.id, 24)} · {memory.path}
            </div>
          </div>

          <fieldset className="mt-4 grid gap-1.5">
            <legend className="text-xs font-medium text-fg-muted">
              {t('memories.relationDialog.direction')}
            </legend>
            {(['in', 'out'] as const).map((candidate) => (
              <label
                key={candidate}
                className="flex cursor-pointer items-center gap-2 rounded-md border border-border bg-bg-inset/60 px-3 py-2 text-xs text-fg-muted transition-colors has-[:checked]:border-accent/50 has-[:checked]:bg-accent/5"
              >
                <input
                  type="radio"
                  name={`relation-direction-${relationType}`}
                  value={candidate}
                  checked={direction === candidate}
                  onChange={() => setDirection(candidate)}
                  className="accent-accent"
                />
                {t(`memories.relationDialog.direction${candidate === 'in' ? 'In' : 'Out'}.${relationType}`)}
              </label>
            ))}
          </fieldset>

          <label className="mt-3 grid gap-1.5">
            <span className="text-xs font-medium text-fg-muted">
              {t('memories.relationDialog.peer')}
            </span>
            <select
              value={peerMode}
              onChange={(event) => setPeerMode(event.target.value as 'list' | 'manual')}
              className="h-9 rounded-md border border-border bg-bg-inset px-3 text-sm text-fg outline-none focus:border-accent/60"
            >
              <option value="list">{t('memories.relationDialog.peerFromList')}</option>
              <option value="manual">{t('memories.relationDialog.peerManual')}</option>
            </select>
          </label>

          {peerMode === 'list' ? (
            <select
              value={selectedPeerID}
              onChange={(event) => setSelectedPeerID(event.target.value)}
              aria-label={t('memories.relationDialog.peer')}
              className="mt-2 h-9 w-full rounded-md border border-border bg-bg-inset px-3 text-sm text-fg outline-none focus:border-accent/60"
            >
              <option value="">{t('memories.relationDialog.peerPlaceholder')}</option>
              {peers.map((candidate) => (
                <option key={candidate.id} value={candidate.id}>
                  {candidate.excerpt.replace(/\s+/g, ' ').slice(0, 60) || candidate.id}
                  {' · '}
                  {candidate.path}
                </option>
              ))}
            </select>
          ) : (
            <Input
              value={manualPeerID}
              onChange={(event) => setManualPeerID(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              className="mt-2 font-mono"
              placeholder={t('memories.relationDialog.peerIdPlaceholder')}
              aria-label={t('memories.relationDialog.peer')}
            />
          )}

          <label className="mt-3 grid gap-1.5">
            <span className="text-xs font-medium text-fg-muted">
              {t('memories.relationDialog.reason')}
            </span>
            <Input
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              placeholder={t('memories.relationDialog.reasonPlaceholder')}
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
                {t('memories.relationDialog.cancel')}
              </Button>
            </Dialog.Close>
            <Button
              type="button"
              variant="primary"
              size="sm"
              loading={busy}
              onClick={() => void submit()}
            >
              {t('memories.relationDialog.submit')}
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
