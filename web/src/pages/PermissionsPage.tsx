/**
 * Workspace access-control surface (GOAL.md §6 P2).
 *
 * Shows the two identity families that can touch this workspace since mem#89:
 * issued tokens (workspace-bound Agent tokens and unbound browser sessions),
 * and the durable-context.v1 recall allowlist. The page only wraps the
 * existing admin API contracts (list/revoke for both families); it never
 * invents new semantics. Revocation is the only mutation and always requires
 * confirmation.
 */
import * as React from 'react';
import { Link } from 'react-router';
import { KeyRound, RefreshCw, ScrollText, ShieldCheck } from 'lucide-react';
import { Badge, type BadgeProps } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useCapabilities } from '@/hooks/useWorkspace';
import { formatDateTime, formatRelative, truncateMiddle } from '@/lib/format';
import {
  isForbiddenError,
  listDurableContextGrants,
  listTokens,
  permissionErrorText,
  revokeDurableContextGrant,
  revokeToken,
  type DurableContextGrantView,
  type TokenView,
} from '@/lib/permissions';
import { useT } from '@/i18n';

const TOKEN_ROW_GRID =
  'grid grid-cols-[minmax(0,1.5fr)_minmax(0,1.3fr)_8.5rem_8.5rem_5.5rem] items-center gap-3';
const GRANT_ROW_GRID =
  'grid grid-cols-[minmax(0,1.3fr)_minmax(0,1.3fr)_7.5rem_8.5rem_5.5rem] items-center gap-3';

const GRANT_STATUS_TONE: Record<string, NonNullable<BadgeProps['tone']>> = {
  active: 'success',
  revoked: 'muted',
  superseded: 'warn',
  forgotten: 'danger',
};

/** Statuses with permissions.status.* translations; newer servers may add. */
const KNOWN_GRANT_STATUSES = new Set(['active', 'revoked', 'superseded', 'forgotten']);

export function PermissionsPage() {
  const { t } = useT();
  const capabilities = useCapabilities();
  const permitted = capabilities.data?.permissions.permissions_manage === true;
  const workspaceId = capabilities.data?.workspace.id ?? null;

  const [tokens, setTokens] = React.useState<TokenView[] | null>(null);
  const [grants, setGrants] = React.useState<DurableContextGrantView[] | null>(null);
  const [tokensErr, setTokensErr] = React.useState<string | null>(null);
  const [grantsErr, setGrantsErr] = React.useState<string | null>(null);
  const [tokensForbidden, setTokensForbidden] = React.useState(false);
  const [grantsForbidden, setGrantsForbidden] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [notice, setNotice] = React.useState<string | null>(null);
  const [actionErr, setActionErr] = React.useState<string | null>(null);
  const [tokenToRevoke, setTokenToRevoke] = React.useState<TokenView | null>(null);
  const [grantToRevoke, setGrantToRevoke] = React.useState<DurableContextGrantView | null>(null);

  // Each list settles independently so a slow or failing request never
  // blocks the healthy section from rendering.
  const refresh = React.useCallback(() => {
    setBusy(true);
    setTokensErr(null);
    setTokensForbidden(false);
    setGrantsErr(null);
    setGrantsForbidden(false);
    const tokensDone = listTokens()
      .then((rows) => setTokens(rows))
      .catch((error: unknown) => {
        setTokens(null);
        if (isForbiddenError(error)) setTokensForbidden(true);
        else setTokensErr(permissionErrorText(error));
      });
    const grantsDone = listDurableContextGrants()
      .then((rows) => setGrants(rows))
      .catch((error: unknown) => {
        setGrants(null);
        if (isForbiddenError(error)) setGrantsForbidden(true);
        else setGrantsErr(permissionErrorText(error));
      });
    void Promise.allSettled([tokensDone, grantsDone]).then(() => setBusy(false));
  }, []);

  React.useEffect(() => {
    if (permitted) void refresh();
  }, [permitted, refresh]);

  const confirmRevokeToken = React.useCallback(async () => {
    if (!tokenToRevoke) return;
    const target = tokenToRevoke;
    setActionErr(null);
    try {
      await revokeToken(target.id);
      setTokens((current) =>
        current ? current.filter((token) => token.id !== target.id) : current,
      );
      setNotice(t('permissions.tokens.revokedNotice', { name: target.name }));
    } catch (error) {
      setActionErr(permissionErrorText(error));
    }
  }, [tokenToRevoke, t]);

  const confirmRevokeGrant = React.useCallback(async () => {
    if (!grantToRevoke) return;
    const target = grantToRevoke;
    setActionErr(null);
    try {
      // The backend revoke is an idempotent soft revoke; it returns the
      // surviving audit row. Merge it over the annotated list row and derive
      // the revoked view state (revocation wins over memory lifecycle).
      const updated = await revokeDurableContextGrant(target.id);
      setGrants((current) =>
        current
          ? current.map((grant) =>
              grant.id === updated.id ? { ...grant, ...updated, status: 'revoked' } : grant,
            )
          : current,
      );
      setNotice(t('permissions.grants.revokedNotice', { principal: target.principal }));
    } catch (error) {
      setActionErr(permissionErrorText(error));
    }
  }, [grantToRevoke, t]);

  if (!capabilities.data) {
    return (
      <div className="mx-auto max-w-3xl px-8 py-10" data-testid="permissions-page">
        <div className="space-y-3">
          <Skeleton className="h-8 w-56" />
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-40 w-full" />
          <Skeleton className="h-40 w-full" />
        </div>
      </div>
    );
  }

  const header = (
    <>
      <div className="flex items-center gap-3 mb-1">
        <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-2">
          <ShieldCheck className="h-5 w-5 text-accent" /> {t('permissions.title')}
        </h1>
        {permitted && (
          <Button variant="ghost" size="sm" onClick={refresh} disabled={busy}>
            <RefreshCw className={busy ? 'h-3.5 w-3.5 animate-spin' : 'h-3.5 w-3.5'} />
            {t('permissions.refresh')}
          </Button>
        )}
      </div>
      <p className="text-sm text-fg-muted mb-6">{t('permissions.description')}</p>
    </>
  );

  if (!permitted) {
    return (
      <div className="mx-auto max-w-3xl px-8 py-10" data-testid="permissions-page">
        {header}
        <div data-testid="permissions-forbidden">
          <EmptyState
            icon={<ShieldCheck />}
            title={t('permissions.forbidden.title')}
            description={t('permissions.forbidden.description')}
          />
        </div>
      </div>
    );
  }

  const forbiddenPanel = (testId: string) => (
    <div
      className="rounded-md border border-border bg-bg-subtle px-4 py-3 text-sm"
      data-testid={testId}
    >
      <div className="font-medium text-fg">{t('permissions.forbidden.title')}</div>
      <p className="mt-1 text-xs text-fg-muted">{t('permissions.forbidden.description')}</p>
    </div>
  );

  return (
    <div className="mx-auto max-w-3xl px-8 py-10" data-testid="permissions-page">
      {header}

      {notice && (
        <div
          className="mb-4 rounded-md border border-success/40 bg-success/5 px-4 py-3 text-sm text-success"
          data-testid="permissions-notice"
        >
          {notice}
        </div>
      )}
      {actionErr && (
        <div
          className="mb-4 rounded-md border border-danger/40 bg-danger/5 px-4 py-3 text-sm text-danger"
          data-testid="permissions-action-error"
        >
          {t('permissions.actionFailed')}: {actionErr}
        </div>
      )}

      {/* ---- Issued tokens / sessions ---- */}
      <section className="mb-10" data-testid="tokens-section">
        <div className="flex items-center gap-2 mb-1">
          <KeyRound className="h-4 w-4 text-accent" aria-hidden="true" />
          <h2 className="text-base font-semibold tracking-tight">
            {t('permissions.tokens.title')}
          </h2>
        </div>
        <p className="text-xs text-fg-muted mb-3">{t('permissions.tokens.description')}</p>

        {tokensForbidden ? (
          forbiddenPanel('tokens-forbidden')
        ) : tokensErr ? (
          <div
            className="mb-2 rounded-md border border-danger/40 bg-danger/5 px-4 py-3 text-sm text-danger"
            data-testid="tokens-error"
          >
            {t('permissions.tokens.error')}: {tokensErr}
          </div>
        ) : busy && tokens === null ? (
          <div className="space-y-2">
            {Array.from({ length: 2 }).map((_, i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : tokens && tokens.length === 0 ? (
          <EmptyState
            icon={<KeyRound />}
            title={t('permissions.tokens.empty')}
            description={t('permissions.tokens.emptyHint')}
          />
        ) : tokens ? (
          <div className="surface overflow-hidden">
            <div
              className={`${TOKEN_ROW_GRID} border-b border-border bg-bg-subtle/60 px-4 py-2 text-2xs uppercase tracking-wider text-fg-subtle`}
            >
              <span>{t('permissions.tokens.col.name')}</span>
              <span>{t('permissions.tokens.col.scopes')}</span>
              <span>{t('permissions.tokens.col.created')}</span>
              <span>{t('permissions.tokens.col.lastUsed')}</span>
              <span />
            </div>
            <ol className="divide-y divide-border">
              {tokens.map((token) => (
                <li
                  key={token.id}
                  className={`${TOKEN_ROW_GRID} px-4 py-3`}
                  data-testid={`token-row-${token.id}`}
                >
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium text-fg">{token.name}</span>
                      <Badge tone={token.kind === 'agent' ? 'accent' : 'neutral'}>
                        {t(`permissions.tokens.kind.${token.kind}`)}
                      </Badge>
                    </div>
                    <div className="mt-0.5 truncate text-2xs text-fg-subtle">
                      {token.paths.length > 0
                        ? token.paths.join(', ')
                        : t('permissions.tokens.allPaths')}
                    </div>
                    {token.expiresAt && (
                      <div className="mt-0.5 text-2xs text-warn">
                        {t('permissions.tokens.expiresAt', {
                          time: formatDateTime(token.expiresAt),
                        })}
                      </div>
                    )}
                  </div>
                  <div className="min-w-0 break-words font-mono text-2xs text-fg-muted">
                    {token.scopes.join(' ')}
                  </div>
                  <div className="text-2xs text-fg-muted">{formatDateTime(token.createdAt)}</div>
                  <div className="text-2xs text-fg-muted">
                    {token.lastUsedAt ? formatRelative(token.lastUsedAt) : t('permissions.tokens.neverUsed')}
                  </div>
                  <div className="justify-self-end">
                    <Button
                      variant="danger"
                      size="sm"
                      onClick={() => {
                        setNotice(null);
                        setTokenToRevoke(token);
                      }}
                    >
                      {t('permissions.tokens.revoke')}
                    </Button>
                  </div>
                </li>
              ))}
            </ol>
          </div>
        ) : null}
      </section>

      {/* ---- durable-context recall grants ---- */}
      <section data-testid="grants-section">
        <div className="flex items-center gap-2 mb-1">
          <ScrollText className="h-4 w-4 text-accent" aria-hidden="true" />
          <h2 className="text-base font-semibold tracking-tight">
            {t('permissions.grants.title')}
          </h2>
        </div>
        <p className="text-xs text-fg-muted mb-3">{t('permissions.grants.description')}</p>

        {grantsForbidden ? (
          forbiddenPanel('grants-forbidden')
        ) : grantsErr ? (
          <div
            className="mb-2 rounded-md border border-danger/40 bg-danger/5 px-4 py-3 text-sm text-danger"
            data-testid="grants-error"
          >
            {t('permissions.grants.error')}: {grantsErr}
          </div>
        ) : busy && grants === null ? (
          <div className="space-y-2">
            {Array.from({ length: 2 }).map((_, i) => (
              <Skeleton key={i} className="h-14 w-full" />
            ))}
          </div>
        ) : grants && grants.length === 0 ? (
          <EmptyState
            icon={<ScrollText />}
            title={t('permissions.grants.empty')}
            description={t('permissions.grants.emptyHint')}
          />
        ) : grants ? (
          <div className="surface overflow-hidden">
            <div
              className={`${GRANT_ROW_GRID} border-b border-border bg-bg-subtle/60 px-4 py-2 text-2xs uppercase tracking-wider text-fg-subtle`}
            >
              <span>{t('permissions.grants.col.principal')}</span>
              <span>{t('permissions.grants.col.memory')}</span>
              <span>{t('permissions.grants.col.status')}</span>
              <span>{t('permissions.grants.col.grantedAt')}</span>
              <span />
            </div>
            <ol className="divide-y divide-border">
              {grants.map((grant) => {
                const statusLabel = KNOWN_GRANT_STATUSES.has(grant.status)
                  ? t(`permissions.status.${grant.status}`)
                  : grant.status;
                return (
                  <li
                    key={grant.id}
                    className={`${GRANT_ROW_GRID} px-4 py-3`}
                    data-testid={`grant-row-${grant.id}`}
                  >
                    <div className="min-w-0">
                      <div className="truncate font-mono text-sm text-fg">{grant.principal}</div>
                      {grant.granted_by_user_id && (
                        <div className="mt-0.5 truncate text-2xs text-fg-subtle">
                          {t('permissions.grants.grantedBy', { user: grant.granted_by_user_id })}
                        </div>
                      )}
                    </div>
                    <div className="min-w-0">
                      <Link
                        to={`/memories/${grant.memory_id}`}
                        className="font-mono text-xs text-accent hover:underline"
                      >
                        {truncateMiddle(grant.memory_id, 20)}
                      </Link>
                      <div className="mt-0.5 truncate text-2xs text-fg-subtle">
                        {grant.mode}
                        {' · '}
                        {grant.workspace_id === workspaceId
                          ? t('permissions.grants.currentWorkspace')
                          : truncateMiddle(grant.workspace_id, 20)}
                      </div>
                    </div>
                    <div className="min-w-0">
                      <Badge tone={GRANT_STATUS_TONE[grant.status] ?? 'neutral'} dot>
                        {statusLabel}
                      </Badge>
                      {grant.revoked_at && (
                        <div className="mt-0.5 text-2xs text-fg-subtle">
                          {t('permissions.grants.revokedAt', {
                            time: formatDateTime(grant.revoked_at),
                          })}
                        </div>
                      )}
                    </div>
                    <div className="text-2xs text-fg-muted">{formatDateTime(grant.granted_at)}</div>
                    <div className="justify-self-end">
                      {grant.status !== 'revoked' && (
                        <Button
                          variant="danger"
                          size="sm"
                          onClick={() => {
                            setNotice(null);
                            setGrantToRevoke(grant);
                          }}
                        >
                          {t('permissions.grants.revoke')}
                        </Button>
                      )}
                    </div>
                  </li>
                );
              })}
            </ol>
          </div>
        ) : null}
      </section>

      {/* ---- destructive revoke confirmations ---- */}
      <ConfirmDialog
        open={tokenToRevoke !== null}
        onOpenChange={(open) => {
          if (!open) setTokenToRevoke(null);
        }}
        title={
          tokenToRevoke
            ? t('permissions.tokens.revokeTitle', { name: tokenToRevoke.name })
            : t('permissions.tokens.revoke')
        }
        description={t('permissions.tokens.revokeDescription')}
        confirmText={t('permissions.tokens.revoke')}
        cancelText={t('action.cancel')}
        destructive
        onConfirm={confirmRevokeToken}
      />
      <ConfirmDialog
        open={grantToRevoke !== null}
        onOpenChange={(open) => {
          if (!open) setGrantToRevoke(null);
        }}
        title={
          grantToRevoke
            ? t('permissions.grants.revokeTitle', { principal: grantToRevoke.principal })
            : t('permissions.grants.revoke')
        }
        description={t('permissions.grants.revokeDescription')}
        confirmText={t('permissions.grants.revoke')}
        cancelText={t('action.cancel')}
        destructive
        onConfirm={confirmRevokeGrant}
      />
    </div>
  );
}
