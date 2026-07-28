import * as React from 'react';
import {
  AlertCircle,
  CheckCircle2,
  Copy,
  DatabaseZap,
  RefreshCw,
  SearchX,
  ShieldAlert,
} from 'lucide-react';
import { toast } from 'sonner';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { useResumeTask } from '@/hooks/useTasks';
import { useCapabilities } from '@/hooks/useWorkspace';
import { useT } from '@/i18n';
import { ApiException } from '@/lib/api';
import type {
  ContextEvidence,
  ResolvedHandoffReference,
  ResumeResponse,
} from '@/lib/types';
import { ReferenceLink } from './ReferenceLink';
import { TaskStatusBadge } from './TaskStatusBadge';

function errorDescription(error: unknown): string {
  if (error instanceof ApiException) return error.hint || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

function resolutionTone(status: string): 'success' | 'warn' | 'danger' | 'muted' {
  if (status === 'available') return 'success';
  if (status === 'external_unverified') return 'warn';
  if (status === 'hash_mismatch') return 'danger';
  return 'muted';
}

function ResolutionRow({ reference }: { reference: ResolvedHandoffReference }) {
  const { t } = useT();
  return (
    <li className="border-l-2 border-border pl-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={resolutionTone(reference.status)} dot>{reference.status}</Badge>
        <Badge tone="muted">{reference.relation}</Badge>
        {reference.required && <Badge tone="warn">{t('task.required')}</Badge>}
      </div>
      <ReferenceLink uri={reference.citation ?? reference.uri} className="mt-1.5" />
      {reference.actual_sha256 && (
        <div className="mt-1 break-all font-mono text-2xs text-fg-subtle">
          sha256:{reference.actual_sha256}
        </div>
      )}
    </li>
  );
}

function EvidenceRow({ evidence }: { evidence: ContextEvidence }) {
  return (
    <li className="border-l-2 border-accent/40 pl-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={evidence.route === 'visual' ? 'accent' : 'neutral'}>
          {evidence.route}
        </Badge>
        {evidence.reason && <Badge tone="muted">{evidence.reason}</Badge>}
        <span className="font-mono text-2xs text-fg-subtle">
          {Math.round(evidence.score * 1000) / 1000}
        </span>
      </div>
      <ReferenceLink uri={evidence.citation} className="mt-1.5" />
      <div className="mt-2 whitespace-pre-wrap text-xs leading-5 text-fg-muted">
        {evidence.excerpt}
      </div>
    </li>
  );
}

function ResumeResult({
  result,
  canSearch,
}: {
  result: ResumeResponse;
  canSearch: boolean;
}) {
  const { t } = useT();
  const state = result.checkpoint.handoff.state;
  const warnings = result.warnings ?? [];

  return (
    <div className="mt-4 space-y-5" data-testid="resume-result">
      <div
        className={
          result.complete
            ? 'border-l-2 border-success bg-success/5 px-3 py-2.5'
            : 'border-l-2 border-warn bg-warn/5 px-3 py-2.5'
        }
      >
        <div className="flex items-center gap-2">
          {result.complete ? (
            <CheckCircle2 className="h-4 w-4 text-success" />
          ) : (
            <ShieldAlert className="h-4 w-4 text-warn" />
          )}
          <span className="text-sm font-medium text-fg">
            {result.complete ? t('resume.complete') : t('resume.incomplete')}
          </span>
        </div>
        <div className="mt-1 text-xs leading-5 text-fg-muted">
          {result.complete ? t('resume.completeHint') : t('resume.incompleteHint')}
        </div>
      </div>

      <section>
        <div className="mb-2 flex items-center justify-between gap-2">
          <div className="text-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">
            {t('resume.deterministicState')}
          </div>
          <TaskStatusBadge status={state.status} />
        </div>
        <div className="border-l-2 border-accent/50 pl-3">
          <div className="font-mono text-2xs text-fg-subtle">
            #{result.checkpoint.sequence} · {result.checkpoint.id}
          </div>
          <div className="mt-1 text-sm text-fg">{state.goal}</div>
          <div className="mt-1 text-xs leading-5 text-fg-muted">{state.progress.summary}</div>
        </div>
      </section>

      {(result.resolved.length > 0 || result.missing.length > 0) && (
        <section className="grid gap-4 md:grid-cols-2">
          <div>
            <div className="mb-2 text-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">
              {t('resume.resolved')} · {result.resolved.length}
            </div>
            {result.resolved.length > 0 ? (
              <ul className="space-y-3">
                {result.resolved.map((reference, index) => (
                  <ResolutionRow key={`${reference.uri}-${index}`} reference={reference} />
                ))}
              </ul>
            ) : <div className="text-xs text-fg-subtle">—</div>}
          </div>
          <div>
            <div className="mb-2 text-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">
              {t('resume.missing')} · {result.missing.length}
            </div>
            {result.missing.length > 0 ? (
              <ul className="space-y-3">
                {result.missing.map((reference, index) => (
                  <ResolutionRow key={`${reference.uri}-${index}`} reference={reference} />
                ))}
              </ul>
            ) : <div className="text-xs text-fg-subtle">—</div>}
          </div>
        </section>
      )}

      {warnings.length > 0 && (
        <section className="space-y-2">
          {warnings.map((warning, index) => (
            <div key={`${warning.code}-${index}`} className="flex gap-2 text-xs text-warn">
              <AlertCircle className="mt-0.5 h-3.5 w-3.5 flex-none" />
              <span>
                <span className="font-mono">{warning.code}</span> · {warning.message}
              </span>
            </div>
          ))}
        </section>
      )}

      <section>
        <div className="mb-2 text-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">
          {t('resume.semanticEvidence')}
        </div>
        {result.context?.evidence.length ? (
          <ul className="space-y-4">
            {result.context.evidence.map((evidence) => (
              <EvidenceRow key={evidence.evidence_id} evidence={evidence} />
            ))}
          </ul>
        ) : (
          <div className="flex gap-2 border-l-2 border-border pl-3 text-xs leading-5 text-fg-muted">
            {canSearch ? (
              <SearchX className="mt-0.5 h-3.5 w-3.5 flex-none text-fg-subtle" />
            ) : (
              <ShieldAlert className="mt-0.5 h-3.5 w-3.5 flex-none text-warn" />
            )}
            <span>
              {canSearch ? t('resume.noEvidence') : t('resume.noSearchPermission')}
              <span className="block text-fg-subtle">{t('resume.stateStillAvailable')}</span>
            </span>
          </div>
        )}
      </section>
    </div>
  );
}

export function ResumePanel({
  taskKey,
  checkpointID,
}: {
  taskKey: string;
  checkpointID?: string;
}) {
  const { t } = useT();
  const capabilities = useCapabilities();
  const resume = useResumeTask(taskKey);
  const canSearch = capabilities.data?.permissions.search ?? false;

  const run = React.useCallback(() => {
    resume.mutate(checkpointID ? { checkpoint_id: checkpointID } : {});
  }, [checkpointID, resume]);

  const copyResult = React.useCallback(async () => {
    if (!resume.data) return;
    try {
      await navigator.clipboard.writeText(JSON.stringify(resume.data, null, 2));
      toast.success(t('resume.copied'));
    } catch {
      toast.error(t('resume.copyFailed'));
    }
  }, [resume.data, t]);

  return (
    <section className="surface overflow-hidden">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-border bg-bg-subtle/45 px-4 py-3">
        <div>
          <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-[0.14em] text-fg-muted">
            <DatabaseZap className="h-3.5 w-3.5 text-accent" />
            {t('resume.title')}
          </div>
          <p className="mt-1 max-w-xl text-xs leading-5 text-fg-subtle">
            {checkpointID ? t('resume.historicalHint') : t('resume.headHint')}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {resume.data && (
            <Button variant="ghost" size="sm" onClick={copyResult}>
              <Copy className="h-3.5 w-3.5" />
              {t('resume.copy')}
            </Button>
          )}
          <Button variant="primary" size="sm" loading={resume.isPending} onClick={run}>
            <RefreshCw className="h-3.5 w-3.5" />
            {resume.data ? t('resume.rebuild') : t('resume.build')}
          </Button>
        </div>
      </div>
      <div className="px-4 py-4">
        {!resume.data && !resume.isError && (
          <div className="text-xs leading-5 text-fg-muted">
            {t('resume.beforeRun')}
          </div>
        )}
        {resume.isError && (
          <div className="flex items-start justify-between gap-3 border-l-2 border-danger bg-danger/5 px-3 py-2.5">
            <div className="flex gap-2">
              <AlertCircle className="mt-0.5 h-4 w-4 flex-none text-danger" />
              <div>
                <div className="text-sm text-danger">{t('resume.failed')}</div>
                <div className="mt-1 text-xs leading-5 text-fg-muted">
                  {errorDescription(resume.error)}
                </div>
              </div>
            </div>
            <Button variant="ghost" size="sm" onClick={run}>{t('common.retry')}</Button>
          </div>
        )}
        {resume.data && <ResumeResult result={resume.data} canSearch={canSearch} />}
      </div>
    </section>
  );
}
