import * as React from 'react';
import {
  AlertTriangle,
  ArrowDownToLine,
  ArrowLeftRight,
  ArrowUpFromLine,
  CheckCircle2,
  FileArchive,
  Fingerprint,
  Info,
  LockKeyhole,
  RotateCcw,
  ShieldCheck,
  X,
} from 'lucide-react';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { Card, CardBody, CardHeader, CardTitle } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useWorkspaceExport, useWorkspaceImport } from '@/hooks/useWorkspaceTransfer';
import { useCapabilities } from '@/hooks/useWorkspace';
import { useT } from '@/i18n';
import { formatBytes, formatDateTime } from '@/lib/format';
import type {
  Capabilities,
  WorkspaceImportConflict,
  WorkspaceImportResult,
  WorkspaceObjectCounts,
} from '@/lib/types';
import {
  validateWorkspaceBundleFile,
  WorkspaceTransferError,
  type WorkspaceBundleFileIssue,
} from '@/lib/workspace-transfer';

const COUNT_KEYS = [
  'folders',
  'files',
  'memories',
  'memory_events',
  'tasks',
  'checkpoints',
  'checkpoint_refs',
  'checkpoint_payloads',
  'blobs',
] as const satisfies readonly (keyof WorkspaceObjectCounts)[];

const CURRENT_WORKSPACE_BUNDLE_SCHEMA_VERSION = 2;
const IMPORTABLE_WORKSPACE_BUNDLE_SCHEMA_VERSIONS = [1, 2] as const;

function advertisedWorkspaceBundleSchema(capabilities: Capabilities): number | null {
  return capabilities.workspace_bundle_schema_versions.includes(
    CURRENT_WORKSPACE_BUNDLE_SCHEMA_VERSION,
  )
    ? CURRENT_WORKSPACE_BUNDLE_SCHEMA_VERSION
    : null;
}

function supportsWorkspaceBundleImport(capabilities: Capabilities): boolean {
  return IMPORTABLE_WORKSPACE_BUNDLE_SCHEMA_VERSIONS.some((version) =>
    capabilities.workspace_bundle_schema_versions.includes(version),
  );
}

function genericErrorText(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

function TransferPageSkeleton() {
  return (
    <main className="mx-auto w-full max-w-6xl space-y-5 px-4 py-7 sm:px-6 sm:py-10">
      <div className="space-y-3">
        <Skeleton className="h-5 w-44" />
        <Skeleton className="h-9 w-64 max-w-full" />
        <Skeleton className="h-4 w-[42rem] max-w-full" />
      </div>
      <Skeleton className="h-32 w-full" />
      <div className="grid min-w-0 gap-5 lg:grid-cols-2">
        <Skeleton className="h-[28rem] w-full" />
        <Skeleton className="h-[34rem] w-full" />
      </div>
    </main>
  );
}

function GateNotice({ kind }: { kind: 'permission' | 'unsupported' }) {
  const { t } = useT();
  const Icon = kind === 'permission' ? LockKeyhole : Info;
  return (
    <div
      className="flex min-w-0 gap-2.5 rounded-md border border-border bg-bg-inset/70 p-3"
      data-testid={`transfer-${kind}-gate`}
    >
      <Icon className="mt-0.5 h-4 w-4 flex-none text-fg-subtle" aria-hidden="true" />
      <div className="min-w-0">
        <p className="text-xs font-medium text-fg">{t(`transfer.gate.${kind}.title`)}</p>
        <p className="mt-1 text-xs leading-relaxed text-fg-muted">
          {t(`transfer.gate.${kind}.description`)}
        </p>
      </div>
    </div>
  );
}

function ConflictList({
  conflicts,
  total,
  truncated,
}: {
  conflicts: WorkspaceImportConflict[];
  total?: number;
  truncated?: boolean;
}) {
  const { t } = useT();
  return (
    <div
      className="rounded-md border border-warn/30 bg-warn/5 p-3"
      role="alert"
      data-testid="transfer-conflicts"
    >
      <div className="flex items-start gap-2">
        <AlertTriangle className="mt-0.5 h-4 w-4 flex-none text-warn" aria-hidden="true" />
        <div className="min-w-0">
          <p className="text-sm font-medium text-fg">{t('transfer.conflict.title')}</p>
          <p className="mt-1 text-xs leading-relaxed text-fg-muted">
            {t('transfer.conflict.description')}
          </p>
        </div>
      </div>
      {conflicts.length > 0 ? (
        <ol className="mt-3 space-y-2">
          {conflicts.map((conflict, index) => (
            <li
              key={`${conflict.kind}-${index}`}
              className="min-w-0 rounded border border-border bg-bg-panel px-3 py-2.5"
            >
              <dl className="grid min-w-0 gap-x-3 gap-y-1 text-xs sm:grid-cols-[6rem_minmax(0,1fr)]">
                <dt className="text-fg-subtle">{t('transfer.conflict.kind')}</dt>
                <dd className="min-w-0 break-words font-mono text-fg">{conflict.kind}</dd>
                {conflict.resource && (
                  <>
                    <dt className="text-fg-subtle">{t('transfer.conflict.resource')}</dt>
                    <dd className="min-w-0 break-words font-mono text-fg-muted">
                      {conflict.resource}
                    </dd>
                  </>
                )}
                {conflict.value && (
                  <>
                    <dt className="text-fg-subtle">{t('transfer.conflict.value')}</dt>
                    <dd className="min-w-0 break-words font-mono text-fg-muted">
                      {conflict.value}
                    </dd>
                  </>
                )}
              </dl>
            </li>
          ))}
        </ol>
      ) : (
        <p className="mt-3 text-xs text-fg-muted">{t('transfer.conflict.noDetails')}</p>
      )}
      {truncated && (
        <p
          className="mt-3 rounded border border-warn/20 bg-bg-panel px-2.5 py-2 text-xs leading-relaxed text-fg-muted"
          data-testid="transfer-conflicts-truncated"
        >
          {typeof total === 'number'
            ? t('transfer.conflict.truncatedWithTotal', {
                total,
                shown: conflicts.length,
              })
            : t('transfer.conflict.truncated', { shown: conflicts.length })}
        </p>
      )}
    </div>
  );
}

function TransferErrorNotice({
  error,
  onRetry,
}: {
  error: WorkspaceTransferError;
  onRetry?: () => void;
}) {
  const { t } = useT();
  if (error.kind === 'conflict') {
    return (
      <ConflictList
        conflicts={error.conflicts}
        total={error.conflictTotal}
        truncated={error.conflictsTruncated}
      />
    );
  }
  return (
    <div
      className="rounded-md border border-danger/30 bg-danger/5 p-3"
      role="alert"
      data-testid={`transfer-error-${error.kind}`}
    >
      <div className="flex items-start gap-2">
        <AlertTriangle className="mt-0.5 h-4 w-4 flex-none text-danger" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-fg">{t(`transfer.error.${error.kind}.title`)}</p>
          <p className="mt-1 text-xs leading-relaxed text-fg-muted">
            {t(`transfer.error.${error.kind}.description`)}
          </p>
          {(error.code || error.hint) && (
            <div className="mt-2 min-w-0 rounded border border-border bg-bg-inset px-2.5 py-2 font-mono text-2xs leading-relaxed text-fg-muted">
              {error.code && (
                <div className="break-words">
                  {error.status ? `${error.status} · ` : ''}
                  {error.code}
                </div>
              )}
              {error.hint && <div className="mt-1 break-words text-fg-subtle">{error.hint}</div>}
            </div>
          )}
        </div>
      </div>
      {onRetry && (
        <Button className="mt-3" size="sm" variant="outline" onClick={onRetry}>
          <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
          {t('common.retry')}
        </Button>
      )}
    </div>
  );
}

function ContractStrip({ capabilities }: { capabilities: Capabilities }) {
  const { t } = useT();
  const schemaVersion = advertisedWorkspaceBundleSchema(capabilities);
  const items = [
    {
      label: t('transfer.contract.schema'),
      value: schemaVersion
        ? `mem.workspace_bundle · v${schemaVersion}`
        : t('transfer.contract.unavailable'),
    },
    {
      label: t('transfer.contract.mode'),
      value: capabilities.workspace_restore_modes.includes('fresh')
        ? 'fresh'
        : t('transfer.contract.unavailable'),
    },
    {
      label: t('transfer.contract.target'),
      value: t('transfer.contract.emptyOnly'),
    },
    {
      label: t('transfer.contract.merge'),
      value: t('transfer.contract.noMerge'),
    },
  ];
  return (
    <section
      aria-label={t('transfer.contract.title')}
      className="surface overflow-hidden"
      data-testid="transfer-contract"
    >
      <div className="flex flex-col gap-3 border-b border-border bg-bg-subtle/40 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-center gap-2">
          <ShieldCheck className="h-4 w-4 flex-none text-accent" aria-hidden="true" />
          <div className="min-w-0">
            <p className="text-xs font-medium uppercase tracking-wider text-fg-muted">
              {t('transfer.contract.title')}
            </p>
            <p className="mt-0.5 truncate text-xs text-fg-subtle">
              {capabilities.workspace.name} · {capabilities.workspace.id}
            </p>
          </div>
        </div>
        <Badge tone="accent" dot>
          {t('transfer.contract.atomic')}
        </Badge>
      </div>
      <dl className="grid min-w-0 divide-y divide-border sm:grid-cols-2 sm:divide-x sm:divide-y-0 lg:grid-cols-4">
        {items.map((item) => (
          <div key={item.label} className="min-w-0 px-4 py-3">
            <dt className="text-2xs uppercase tracking-wider text-fg-subtle">{item.label}</dt>
            <dd className="mt-1 min-w-0 break-words font-mono text-xs text-fg">{item.value}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

function ExportCard({
  workspaceID,
  supported,
  permitted,
}: {
  workspaceID: string;
  supported: boolean;
  permitted: boolean;
}) {
  const { t } = useT();
  const transfer = useWorkspaceExport(workspaceID);
  const enabled = supported && permitted;
  return (
    <Card className="min-w-0" data-testid="workspace-export-card">
      <CardHeader className="flex flex-row items-start justify-between gap-3">
        <div className="min-w-0">
          <CardTitle>{t('transfer.export.eyebrow')}</CardTitle>
          <h2 className="mt-1.5 text-lg font-semibold">{t('transfer.export.title')}</h2>
        </div>
        <div className="grid h-9 w-9 flex-none place-items-center rounded-md border border-accent/20 bg-accent/10 text-accent">
          <ArrowDownToLine className="h-4 w-4" aria-hidden="true" />
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        <p className="text-sm leading-relaxed text-fg-muted">{t('transfer.export.description')}</p>
        <div className="rounded-md border border-border bg-bg-inset/60 p-3">
          <div className="flex items-center gap-2 text-xs font-medium text-fg">
            <FileArchive className="h-3.5 w-3.5 text-fg-subtle" aria-hidden />
            {t('transfer.export.contentsTitle')}
          </div>
          <p className="mt-1.5 text-xs leading-relaxed text-fg-muted">
            {t('transfer.export.contents')}
          </p>
          <p className="mt-2 text-2xs leading-relaxed text-fg-subtle">
            {t('transfer.export.exclusions')}
          </p>
        </div>
        {!supported && <GateNotice kind="unsupported" />}
        {supported && !permitted && <GateNotice kind="permission" />}
        <Button
          type="button"
          variant="primary"
          className="w-full sm:w-auto"
          loading={transfer.isPending}
          disabled={!enabled}
          onClick={() => transfer.mutate()}
          data-testid="workspace-export-button"
        >
          <ArrowDownToLine className="h-4 w-4" aria-hidden="true" />
          {transfer.isPending ? t('transfer.export.preparing') : t('transfer.export.action')}
        </Button>
        <p className="text-2xs leading-relaxed text-fg-subtle">
          {t('transfer.export.safeDownload')}
        </p>
        {transfer.isSuccess && (
          <div
            className="rounded-md border border-success/30 bg-success/5 p-3"
            role="status"
            aria-live="polite"
            data-testid="workspace-export-success"
          >
            <div className="flex items-start gap-2">
              <CheckCircle2 className="mt-0.5 h-4 w-4 flex-none text-success" aria-hidden="true" />
              <div className="min-w-0">
                <p className="text-sm font-medium text-fg">{t('transfer.export.success')}</p>
                <p className="mt-1 break-words font-mono text-xs text-fg-muted">
                  {transfer.data.filename}
                </p>
                <p className="mt-1 text-2xs text-fg-subtle">
                  {formatBytes(transfer.data.byteLength)}
                </p>
              </div>
            </div>
          </div>
        )}
        {transfer.error && (
          <TransferErrorNotice
            error={transfer.error}
            onRetry={enabled ? () => transfer.mutate() : undefined}
          />
        )}
      </CardBody>
    </Card>
  );
}

function issueKey(issue: WorkspaceBundleFileIssue): string {
  return `transfer.import.fileIssue.${issue}`;
}

function ImportResult({ result }: { result: WorkspaceImportResult }) {
  const { t } = useT();
  return (
    <div
      className="rounded-md border border-success/30 bg-success/5 p-3"
      role="status"
      aria-live="polite"
      data-testid="workspace-import-success"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="flex min-w-0 items-start gap-2">
          <CheckCircle2 className="mt-0.5 h-4 w-4 flex-none text-success" aria-hidden="true" />
          <div className="min-w-0">
            <p className="text-sm font-medium text-fg">
              {result.replayed ? t('transfer.import.replayed') : t('transfer.import.success')}
            </p>
            <p className="mt-1 text-xs text-fg-muted">{formatDateTime(result.imported_at)}</p>
          </div>
        </div>
        <Badge tone={result.replayed ? 'accent' : 'success'} dot>
          {result.replayed ? t('transfer.import.replayBadge') : t('transfer.import.committedBadge')}
        </Badge>
      </div>
      <dl className="mt-3 grid min-w-0 gap-2 rounded border border-border bg-bg-panel p-3 text-xs">
        <div className="min-w-0">
          <dt className="text-fg-subtle">{t('transfer.import.bundleId')}</dt>
          <dd className="mt-0.5 break-all font-mono text-fg">{result.bundle_id}</dd>
        </div>
        <div className="min-w-0">
          <dt className="text-fg-subtle">{t('transfer.import.sourceWorkspace')}</dt>
          <dd className="mt-0.5 break-all font-mono text-fg-muted">{result.source_workspace_id}</dd>
        </div>
        <div className="min-w-0">
          <dt className="flex items-center gap-1.5 text-fg-subtle">
            <Fingerprint className="h-3 w-3" aria-hidden="true" />
            {t('transfer.import.archiveHash')}
          </dt>
          <dd className="mt-0.5 break-all font-mono text-fg-muted">{result.archive_sha256}</dd>
        </div>
      </dl>
      <div className="mt-3">
        <p className="text-2xs font-medium uppercase tracking-wider text-fg-subtle">
          {t('transfer.import.counts')}
        </p>
        <dl className="mt-2 grid grid-cols-2 gap-px overflow-hidden rounded border border-border bg-border sm:grid-cols-3">
          {COUNT_KEYS.map((key) => (
            <div key={key} className="min-w-0 bg-bg-panel px-2.5 py-2">
              <dt className="truncate text-2xs text-fg-subtle">{t(`transfer.count.${key}`)}</dt>
              <dd className="mt-0.5 font-mono text-sm text-fg">{result.counts[key]}</dd>
            </div>
          ))}
          <div className="min-w-0 bg-bg-panel px-2.5 py-2">
            <dt className="truncate text-2xs text-fg-subtle">{t('transfer.count.blob_bytes')}</dt>
            <dd className="mt-0.5 font-mono text-sm text-fg">
              {formatBytes(result.counts.blob_bytes)}
            </dd>
          </div>
        </dl>
      </div>
    </div>
  );
}

function ImportCard({
  workspaceID,
  supported,
  permitted,
}: {
  workspaceID: string;
  supported: boolean;
  permitted: boolean;
}) {
  const { t } = useT();
  const inputID = React.useId();
  const confirmID = React.useId();
  const [file, setFile] = React.useState<File | null>(null);
  const [fileIssue, setFileIssue] = React.useState<WorkspaceBundleFileIssue | null>(null);
  const [confirmed, setConfirmed] = React.useState(false);
  const transfer = useWorkspaceImport(workspaceID);
  const enabled = supported && permitted;

  function selectFile(next: File | null) {
    transfer.reset();
    setConfirmed(false);
    setFile(next);
    setFileIssue(next ? validateWorkspaceBundleFile(next) : null);
  }

  function submit() {
    if (!file || fileIssue || !confirmed || !enabled) return;
    transfer.mutate(file);
  }

  return (
    <Card className="min-w-0" data-testid="workspace-import-card">
      <CardHeader className="flex flex-row items-start justify-between gap-3">
        <div className="min-w-0">
          <CardTitle>{t('transfer.import.eyebrow')}</CardTitle>
          <h2 className="mt-1.5 text-lg font-semibold">{t('transfer.import.title')}</h2>
        </div>
        <div className="grid h-9 w-9 flex-none place-items-center rounded-md border border-accent/20 bg-accent/10 text-accent">
          <ArrowUpFromLine className="h-4 w-4" aria-hidden="true" />
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        <p className="text-sm leading-relaxed text-fg-muted">{t('transfer.import.description')}</p>
        <div className="rounded-md border border-warn/30 bg-warn/5 p-3">
          <div className="flex items-start gap-2">
            <AlertTriangle className="mt-0.5 h-4 w-4 flex-none text-warn" aria-hidden="true" />
            <div className="min-w-0">
              <p className="text-xs font-medium text-fg">{t('transfer.import.freshTitle')}</p>
              <p className="mt-1 text-xs leading-relaxed text-fg-muted">
                {t('transfer.import.freshDescription')}
              </p>
            </div>
          </div>
        </div>
        {!supported && <GateNotice kind="unsupported" />}
        {supported && !permitted && <GateNotice kind="permission" />}

        <div className="min-w-0">
          <input
            id={inputID}
            className="sr-only"
            type="file"
            accept=".membundle,application/vnd.mem.workspace-bundle+zip"
            disabled={!enabled || transfer.isPending}
            onClick={(event) => {
              event.currentTarget.value = '';
            }}
            onChange={(event) => selectFile(event.currentTarget.files?.[0] ?? null)}
            data-testid="workspace-import-input"
          />
          <label
            htmlFor={inputID}
            className={[
              'flex min-w-0 cursor-pointer flex-col items-center rounded-md border border-dashed px-4 py-7 text-center transition-colors',
              enabled && !transfer.isPending
                ? 'border-border-strong bg-bg-inset/50 hover:border-accent/50 hover:bg-bg-inset'
                : 'cursor-not-allowed border-border bg-bg-subtle/40 opacity-60',
            ].join(' ')}
            onDragOver={(event) => {
              event.preventDefault();
              if (!enabled || transfer.isPending) return;
            }}
            onDrop={(event) => {
              event.preventDefault();
              if (!enabled || transfer.isPending) return;
              selectFile(event.dataTransfer.files?.[0] ?? null);
            }}
            data-testid="workspace-import-dropzone"
          >
            <FileArchive className="h-6 w-6 text-fg-subtle" aria-hidden="true" />
            <span className="mt-2 text-sm font-medium text-fg">{t('transfer.import.choose')}</span>
            <span className="mt-1 text-xs leading-relaxed text-fg-subtle">
              {t('transfer.import.fileHint')}
            </span>
          </label>
        </div>

        {file && (
          <div
            className="min-w-0 rounded-md border border-border bg-bg-inset/60 p-3"
            data-testid="workspace-import-file"
          >
            <div className="flex min-w-0 items-start gap-2.5">
              <FileArchive className="mt-0.5 h-4 w-4 flex-none text-accent" aria-hidden="true" />
              <div className="min-w-0 flex-1">
                <p className="break-words text-sm font-medium text-fg">{file.name}</p>
                <p className="mt-1 break-words font-mono text-2xs text-fg-subtle">
                  {formatBytes(file.size)} · {file.type || t('transfer.import.mimeNotReported')}
                </p>
              </div>
              <button
                type="button"
                className="grid h-7 w-7 flex-none place-items-center rounded text-fg-subtle hover:bg-bg-panel hover:text-fg disabled:cursor-not-allowed disabled:opacity-50"
                onClick={() => selectFile(null)}
                disabled={transfer.isPending}
                aria-label={t('transfer.import.removeFile')}
              >
                <X className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </div>
          </div>
        )}

        {fileIssue && (
          <div
            className="rounded-md border border-danger/30 bg-danger/5 px-3 py-2.5 text-xs leading-relaxed text-danger"
            role="alert"
            data-testid="workspace-import-file-error"
          >
            {t(issueKey(fileIssue))}
          </div>
        )}

        <div className="rounded-md border border-border bg-bg-panel p-3">
          <label htmlFor={confirmID} className="flex cursor-pointer items-start gap-2.5">
            <input
              id={confirmID}
              type="checkbox"
              className="mt-0.5 h-4 w-4 flex-none rounded border-border-strong bg-bg-inset text-accent accent-[rgb(var(--accent))]"
              checked={confirmed}
              disabled={!file || Boolean(fileIssue) || !enabled || transfer.isPending}
              onChange={(event) => setConfirmed(event.currentTarget.checked)}
            />
            <span className="min-w-0 text-xs leading-relaxed text-fg-muted">
              {t('transfer.import.confirm')}
            </span>
          </label>
        </div>

        <Button
          type="button"
          variant="primary"
          className="w-full sm:w-auto"
          loading={transfer.isPending}
          disabled={!file || Boolean(fileIssue) || !confirmed || !enabled}
          onClick={submit}
          data-testid="workspace-import-button"
        >
          <ArrowUpFromLine className="h-4 w-4" aria-hidden="true" />
          {transfer.isPending ? t('transfer.import.uploading') : t('transfer.import.action')}
        </Button>
        {transfer.isPending && (
          <p className="text-2xs leading-relaxed text-fg-subtle" role="status">
            {t('transfer.import.uploadingHint')}
          </p>
        )}
        {transfer.data && <ImportResult result={transfer.data} />}
        {transfer.error && (
          <TransferErrorNotice
            error={transfer.error}
            onRetry={
              file && !fileIssue && confirmed && enabled ? () => transfer.mutate(file) : undefined
            }
          />
        )}
      </CardBody>
    </Card>
  );
}

function WorkspaceTransferSurface({ capabilities }: { capabilities: Capabilities }) {
  const { t } = useT();
  const supportsCurrentExport = advertisedWorkspaceBundleSchema(capabilities) !== null;
  const supportsImport = supportsWorkspaceBundleImport(capabilities);
  const supportsFresh = capabilities.workspace_restore_modes.includes('fresh');
  const exportSupported = capabilities.features.workspace_export && supportsCurrentExport;
  const importSupported = capabilities.features.workspace_import && supportsImport && supportsFresh;

  return (
    <main className="mx-auto w-full max-w-6xl space-y-5 px-4 py-7 sm:px-6 sm:py-10">
      <header className="min-w-0">
        <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-[0.16em] text-accent">
          <ArrowLeftRight className="h-3.5 w-3.5" aria-hidden="true" />
          {t('transfer.eyebrow')}
        </div>
        <div className="mt-2 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
          <div className="min-w-0">
            <h1 className="text-2xl font-semibold sm:text-3xl">{t('transfer.title')}</h1>
            <p className="mt-2 max-w-3xl text-sm leading-relaxed text-fg-muted">
              {t('transfer.subtitle')}
            </p>
          </div>
          <Badge tone="neutral" className="self-start sm:self-auto">
            <Fingerprint className="h-3 w-3" aria-hidden="true" />
            {t('transfer.evidenceLedger')}
          </Badge>
        </div>
      </header>

      <ContractStrip capabilities={capabilities} />

      <div className="grid min-w-0 items-start gap-5 lg:grid-cols-2">
        <ExportCard
          workspaceID={capabilities.workspace.id}
          supported={exportSupported}
          permitted={capabilities.permissions.workspace_export}
        />
        <ImportCard
          workspaceID={capabilities.workspace.id}
          supported={importSupported}
          permitted={capabilities.permissions.workspace_import}
        />
      </div>
    </main>
  );
}

export function TransferPage() {
  const { t } = useT();
  const capabilities = useCapabilities();

  if (capabilities.isLoading) return <TransferPageSkeleton />;

  if (capabilities.isError) {
    return (
      <div className="mx-auto max-w-4xl px-4 py-12 sm:px-6">
        <EmptyState
          icon={<AlertTriangle />}
          title={t('capabilities.failed')}
          description={genericErrorText(capabilities.error)}
          action={
            <Button size="sm" variant="secondary" onClick={() => capabilities.refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      </div>
    );
  }

  const value = capabilities.data;
  if (!value) return null;
  if (!value.features.workspace_export && !value.features.workspace_import) {
    return (
      <div className="mx-auto max-w-4xl px-4 py-12 sm:px-6">
        <EmptyState
          icon={<ArrowLeftRight />}
          title={t('transfer.unavailable')}
          description={t('transfer.unavailableHint')}
        />
      </div>
    );
  }

  // The key intentionally remounts all file, confirmation, result, and
  // mutation state when the selected workspace changes.
  return <WorkspaceTransferSurface key={value.workspace.id} capabilities={value} />;
}
