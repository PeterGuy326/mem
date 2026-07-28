import {
  AlertTriangle,
  Check,
  CircleHelp,
  ClipboardList,
  GitBranch,
  Lightbulb,
  ListChecks,
  Package,
  Target,
} from 'lucide-react';
import { Badge } from '@/components/ui/Badge';
import { Markdown } from '@/components/ui/Markdown';
import { useT } from '@/i18n';
import type { HandoffArtifact, HandoffState as HandoffStateType } from '@/lib/types';
import { ReferenceLink } from './ReferenceLink';

function ReferenceGroup({ references }: { references: string[] }) {
  if (references.length === 0) return null;
  return (
    <div className="mt-2 flex flex-col gap-1.5">
      {references.map((uri, index) => (
        <ReferenceLink key={`${uri}-${index}`} uri={uri} />
      ))}
    </div>
  );
}

function LedgerSection({
  icon,
  title,
  count,
  children,
}: {
  icon: React.ReactNode;
  title: string;
  count?: number;
  children: React.ReactNode;
}) {
  return (
    <section className="grid gap-4 px-5 py-5 md:grid-cols-[150px_minmax(0,1fr)]">
      <div className="flex items-start gap-2 text-2xs font-medium uppercase tracking-[0.14em] text-fg-subtle">
        <span className="mt-[-1px] text-fg-muted [&>svg]:h-3.5 [&>svg]:w-3.5">{icon}</span>
        <span>{title}</span>
        {count !== undefined && <span className="font-mono text-fg-subtle">{count}</span>}
      </div>
      <div className="min-w-0">{children}</div>
    </section>
  );
}

function EmptyLine() {
  const { t } = useT();
  return <div className="text-sm text-fg-subtle">— {t('task.noneRecorded')}</div>;
}

function ArtifactRow({ artifact }: { artifact: HandoffArtifact }) {
  const { t } = useT();
  return (
    <li className="border-l-2 border-border pl-3">
      <div className="flex flex-wrap items-center gap-2">
        <ReferenceLink uri={artifact.uri} />
        <Badge tone={artifact.required ? 'warn' : 'muted'}>
          {artifact.required ? t('task.required') : t('task.optional')}
        </Badge>
        {artifact.role && <Badge tone="neutral">{artifact.role}</Badge>}
      </div>
      {artifact.sha256 && (
        <div className="mt-1 break-all font-mono text-2xs text-fg-subtle">
          sha256:{artifact.sha256}
        </div>
      )}
    </li>
  );
}

export function HandoffState({ state }: { state: HandoffStateType }) {
  const { t } = useT();
  const workspace = state.workspace_state;
  const vcs = workspace?.vcs;

  return (
    <div className="surface divide-y divide-border overflow-hidden">
      <LedgerSection icon={<Target />} title={t('task.goal')}>
        <Markdown className="text-[15px] leading-7 text-fg">{state.goal}</Markdown>
      </LedgerSection>

      <LedgerSection
        icon={<ListChecks />}
        title={t('task.progress')}
        count={state.progress.completed.length}
      >
        <Markdown className="text-sm leading-6 text-fg">{state.progress.summary}</Markdown>
        {state.progress.completed.length > 0 ? (
          <ul className="mt-3 space-y-2">
            {state.progress.completed.map((item, index) => (
              <li key={`${item}-${index}`} className="flex gap-2 text-sm text-fg-muted">
                <Check className="mt-0.5 h-4 w-4 flex-none text-success" />
                <span>{item}</span>
              </li>
            ))}
          </ul>
        ) : (
          <div className="mt-3"><EmptyLine /></div>
        )}
      </LedgerSection>

      <LedgerSection
        icon={<Lightbulb />}
        title={t('task.decisions')}
        count={state.decisions.length}
      >
        {state.decisions.length > 0 ? (
          <ol className="space-y-4">
            {state.decisions.map((decision, index) => (
              <li key={`${decision.summary}-${index}`} className="grid grid-cols-[24px_1fr] gap-2">
                <span className="font-mono text-2xs text-fg-subtle">
                  {String(index + 1).padStart(2, '0')}
                </span>
                <div>
                  <div className="text-sm text-fg">{decision.summary}</div>
                  {decision.rationale && (
                    <div className="mt-1 text-xs leading-5 text-fg-muted">{decision.rationale}</div>
                  )}
                  <ReferenceGroup references={decision.references} />
                </div>
              </li>
            ))}
          </ol>
        ) : <EmptyLine />}
      </LedgerSection>

      <LedgerSection
        icon={<ClipboardList />}
        title={t('task.nextSteps')}
        count={state.next_steps.length}
      >
        {state.next_steps.length > 0 ? (
          <ol className="space-y-3">
            {state.next_steps.map((step, index) => (
              <li key={`${step.summary}-${index}`} className="flex gap-3">
                <span className="mt-0.5 grid h-5 w-5 flex-none place-items-center rounded border border-border font-mono text-2xs text-fg-subtle">
                  {index + 1}
                </span>
                <div className="min-w-0 text-sm text-fg">
                  {step.summary}
                  <ReferenceGroup references={step.references} />
                </div>
              </li>
            ))}
          </ol>
        ) : <EmptyLine />}
      </LedgerSection>

      <LedgerSection
        icon={<AlertTriangle />}
        title={t('task.blockers')}
        count={state.blockers.length}
      >
        {state.blockers.length > 0 ? (
          <ul className="space-y-3">
            {state.blockers.map((blocker, index) => (
              <li key={`${blocker.summary}-${index}`} className="border-l-2 border-warn/60 pl-3">
                <div className="text-sm text-fg">{blocker.summary}</div>
                {blocker.needs && (
                  <div className="mt-1 text-xs text-warn">
                    {t('task.needs')}: {blocker.needs}
                  </div>
                )}
                <ReferenceGroup references={blocker.references} />
              </li>
            ))}
          </ul>
        ) : <EmptyLine />}
      </LedgerSection>

      <LedgerSection
        icon={<CircleHelp />}
        title={t('task.openQuestions')}
        count={state.open_questions.length}
      >
        {state.open_questions.length > 0 ? (
          <ul className="space-y-2 text-sm text-fg-muted">
            {state.open_questions.map((question, index) => (
              <li key={`${question}-${index}`} className="flex gap-2">
                <span className="font-mono text-2xs text-fg-subtle">?</span>
                {question}
              </li>
            ))}
          </ul>
        ) : <EmptyLine />}
      </LedgerSection>

      <LedgerSection
        icon={<Package />}
        title={t('task.artifacts')}
        count={state.artifacts.length}
      >
        {state.artifacts.length > 0 ? (
          <ul className="space-y-3">
            {state.artifacts.map((artifact, index) => (
              <ArtifactRow key={`${artifact.uri}-${index}`} artifact={artifact} />
            ))}
          </ul>
        ) : <EmptyLine />}
      </LedgerSection>

      <LedgerSection icon={<GitBranch />} title={t('task.workspaceState')}>
        {workspace ? (
          <dl className="grid gap-x-6 gap-y-3 text-xs sm:grid-cols-2">
            {workspace.working_directory && (
              <div className="sm:col-span-2">
                <dt className="text-2xs uppercase tracking-wider text-fg-subtle">
                  {t('task.workingDirectory')}
                </dt>
                <dd className="mt-1 break-all font-mono text-fg">{workspace.working_directory}</dd>
              </div>
            )}
            {vcs?.branch && <Meta label={t('task.branch')} value={vcs.branch} />}
            {vcs?.revision && <Meta label={t('task.revision')} value={vcs.revision} mono />}
            {vcs?.dirty !== undefined && (
              <Meta
                label={t('task.workingTree')}
                value={vcs.dirty ? t('task.dirty') : t('task.clean')}
              />
            )}
            {vcs?.status_summary && (
              <Meta label={t('task.statusSummary')} value={vcs.status_summary} full />
            )}
          </dl>
        ) : <EmptyLine />}
      </LedgerSection>
    </div>
  );
}

function Meta({
  label,
  value,
  mono,
  full,
}: {
  label: string;
  value: string;
  mono?: boolean;
  full?: boolean;
}) {
  return (
    <div className={full ? 'sm:col-span-2' : undefined}>
      <dt className="text-2xs uppercase tracking-wider text-fg-subtle">{label}</dt>
      <dd className={`mt-1 break-words text-fg ${mono ? 'font-mono' : ''}`}>{value}</dd>
    </div>
  );
}
