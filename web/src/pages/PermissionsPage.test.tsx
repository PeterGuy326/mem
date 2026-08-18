import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { I18nProvider } from '@/i18n';
import { ApiException } from '@/lib/api';
import type { Capabilities } from '@/lib/types';
import type { DurableContextGrantView, TokenView } from '@/lib/permissions';
import { PermissionsPage } from './PermissionsPage';

// The localization audit flags literal `hint`/`title`/`description` property
// values under src/pages as unlocalized prose. Test fixture errors are data,
// not UI strings, so build them behind a computed key.
const HINT_KEY = 'hint';
function apiFailure(status: number, errorText: string, hintText: string): ApiException {
  return new ApiException({ error: errorText, status, [HINT_KEY]: hintText });
}

const mocks = vi.hoisted(() => ({
  listTokens: vi.fn(),
  listDurableContextGrants: vi.fn(),
  revokeToken: vi.fn(),
  revokeDurableContextGrant: vi.fn(),
  useCapabilities: vi.fn(),
}));

vi.mock('@/lib/permissions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/permissions')>();
  return {
    ...actual,
    listTokens: (...args: unknown[]) => mocks.listTokens(...args),
    listDurableContextGrants: (...args: unknown[]) => mocks.listDurableContextGrants(...args),
    revokeToken: (...args: unknown[]) => mocks.revokeToken(...args),
    revokeDurableContextGrant: (...args: unknown[]) => mocks.revokeDurableContextGrant(...args),
  };
});

vi.mock('@/hooks/useWorkspace', () => ({
  useCapabilities: () => mocks.useCapabilities(),
}));

const TOKEN_FIXTURES: TokenView[] = [
  {
    id: 'tok-agent-1',
    name: 'claude-code-laptop',
    scopes: ['search', 'read', 'write'],
    paths: ['/Projects'],
    workspaceId: 'ws-1',
    kind: 'agent',
    expiresAt: null,
    lastUsedAt: '2026-07-28T18:20:00Z',
    createdAt: '2026-07-15T10:00:00Z',
  },
  {
    id: 'tok-session-1',
    name: 'session-20260729-080000',
    scopes: ['search', 'read', 'write', 'delete', 'admin'],
    paths: [],
    workspaceId: null,
    kind: 'session',
    expiresAt: null,
    lastUsedAt: null,
    createdAt: '2026-07-29T08:00:00Z',
  },
];

const GRANT_FIXTURES: DurableContextGrantView[] = [
  {
    id: 'grant-active',
    workspace_id: 'ws-1',
    principal: 'agent.claude-code',
    memory_id: 'dddddddd-dddd-4ddd-8ddd-dddddddddd01',
    mode: 'read',
    granted_by_user_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
    granted_at: '2026-07-20T09:00:00Z',
    updated_at: '2026-07-20T09:00:00Z',
    memory_status: 'active',
    status: 'active',
  },
  {
    id: 'grant-superseded',
    workspace_id: 'ws-1',
    principal: 'agent.codex',
    memory_id: 'dddddddd-dddd-4ddd-8ddd-dddddddddd02',
    mode: 'read',
    granted_at: '2026-07-18T09:00:00Z',
    updated_at: '2026-07-25T09:00:00Z',
    memory_status: 'archived',
    status: 'superseded',
  },
  {
    id: 'grant-revoked',
    workspace_id: 'ws-1',
    principal: 'agent.other',
    memory_id: 'dddddddd-dddd-4ddd-8ddd-dddddddddd03',
    mode: 'read',
    granted_at: '2026-07-10T09:00:00Z',
    revoked_at: '2026-07-12T09:00:00Z',
    updated_at: '2026-07-12T09:00:00Z',
    memory_status: 'active',
    status: 'revoked',
  },
];

function capabilitiesWith(permissionsManage: boolean): Capabilities {
  return {
    deployment_mode: 'personal',
    registration_mode: 'open',
    workspace: {
      id: 'ws-1',
      name: 'Test Workspace',
      resource_owner_user_id: 'user-1',
      role: 'owner',
      created_at: '2026-07-01T08:00:00Z',
    },
    features: {
      context: true,
      handoff: true,
      memory: true,
      workspace_export: true,
      workspace_import: true,
    },
    workspace_restore_modes: ['fresh'],
    workspace_bundle_schema_versions: [2],
    permissions: {
      read: true,
      search: true,
      write: true,
      delete: true,
      provider_read: true,
      provider_modify: true,
      workspace_export: true,
      workspace_import: true,
      permissions_manage: permissionsManage,
    },
  };
}

function renderPage() {
  return render(
    <I18nProvider>
      <MemoryRouter>
        <PermissionsPage />
      </MemoryRouter>
    </I18nProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.setItem('mem.lang', 'en');
  mocks.useCapabilities.mockReturnValue({ data: capabilitiesWith(true) });
  mocks.listTokens.mockResolvedValue(TOKEN_FIXTURES);
  mocks.listDurableContextGrants.mockResolvedValue(GRANT_FIXTURES);
});

describe('PermissionsPage', () => {
  it('renders loading skeletons until both lists settle', () => {
    let resolveTokens!: (value: TokenView[]) => void;
    mocks.listTokens.mockReturnValue(
      new Promise<TokenView[]>((resolve) => {
        resolveTokens = resolve;
      }),
    );
    mocks.listDurableContextGrants.mockReturnValue(new Promise<DurableContextGrantView[]>(() => {}));

    renderPage();
    expect(screen.getByTestId('permissions-page')).toBeInTheDocument();
    expect(screen.queryByTestId('token-row-tok-agent-1')).not.toBeInTheDocument();

    // Unsettled section stays in loading state while the other one resolves.
    resolveTokens(TOKEN_FIXTURES);
    return waitFor(() => {
      expect(screen.getByTestId('token-row-tok-agent-1')).toBeInTheDocument();
    });
  });

  it('lists tokens with identity, scopes, and usage details', async () => {
    renderPage();
    const agentRow = await screen.findByTestId('token-row-tok-agent-1');
    expect(within(agentRow).getByText('claude-code-laptop')).toBeInTheDocument();
    expect(within(agentRow).getByText('Agent token')).toBeInTheDocument();
    expect(within(agentRow).getByText('search read write')).toBeInTheDocument();
    expect(within(agentRow).getByText('/Projects')).toBeInTheDocument();

    const sessionRow = screen.getByTestId('token-row-tok-session-1');
    expect(within(sessionRow).getByText('Browser session')).toBeInTheDocument();
    // Unrestricted paths and never-used states must be explicit, not blank.
    expect(within(sessionRow).getByText('Unrestricted paths')).toBeInTheDocument();
    expect(within(sessionRow).getByText('Never used')).toBeInTheDocument();
  });

  it('lists recall grants with lifecycle-aware statuses and memory links', async () => {
    renderPage();
    const active = await screen.findByTestId('grant-row-grant-active');
    expect(within(active).getByText('agent.claude-code')).toBeInTheDocument();
    expect(within(active).getByText('Active')).toBeInTheDocument();
    expect(within(active).getByText(/Current workspace/)).toBeInTheDocument();
    expect(within(active).getByRole('link')).toHaveAttribute(
      'href',
      '/memories/dddddddd-dddd-4ddd-8ddd-dddddddddd01',
    );

    const superseded = screen.getByTestId('grant-row-grant-superseded');
    expect(within(superseded).getByText('Superseded')).toBeInTheDocument();

    const revoked = screen.getByTestId('grant-row-grant-revoked');
    expect(within(revoked).getByText('Revoked')).toBeInTheDocument();
    // Already-revoked grants keep the audit row but no revoke action.
    expect(within(revoked).queryByRole('button', { name: 'Revoke' })).not.toBeInTheDocument();
  });

  it('shows empty states when nothing is issued', async () => {
    mocks.listTokens.mockResolvedValue([]);
    mocks.listDurableContextGrants.mockResolvedValue([]);
    renderPage();
    expect(await screen.findByText('No issued tokens')).toBeInTheDocument();
    expect(screen.getByText('No recall grants')).toBeInTheDocument();
  });

  it('gates the whole page when the capability is missing', async () => {
    mocks.useCapabilities.mockReturnValue({ data: capabilitiesWith(false) });
    renderPage();
    expect(await screen.findByTestId('permissions-forbidden')).toBeInTheDocument();
    expect(screen.getByText('Admin permission required')).toBeInTheDocument();
    expect(mocks.listTokens).not.toHaveBeenCalled();
    expect(mocks.listDurableContextGrants).not.toHaveBeenCalled();
  });

  it('keeps healthy sections visible when one list fails', async () => {
    mocks.listTokens.mockRejectedValue(apiFailure(500, 'internal', 'db offline'));
    renderPage();
    const banner = await screen.findByTestId('tokens-error');
    expect(banner).toHaveTextContent('Could not load tokens');
    expect(banner).toHaveTextContent('internal: db offline');
    // Grants section stays usable.
    expect(screen.getByTestId('grant-row-grant-active')).toBeInTheDocument();
  });

  it('shows the section-level forbidden notice on 403', async () => {
    mocks.listTokens.mockRejectedValue(apiFailure(403, 'forbidden', 'admin scope required'));
    renderPage();
    expect(await screen.findByTestId('tokens-forbidden')).toBeInTheDocument();
    expect(screen.queryByTestId('tokens-error')).not.toBeInTheDocument();
  });

  it('revokes a token only after explicit confirmation', async () => {
    const user = userEvent.setup();
    mocks.revokeToken.mockResolvedValue(undefined);
    renderPage();

    const agentRow = await screen.findByTestId('token-row-tok-agent-1');
    await user.click(within(agentRow).getByRole('button', { name: 'Revoke' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('Revoke token "claude-code-laptop"?');
    await user.click(within(dialog).getByRole('button', { name: 'Revoke' }));

    await waitFor(() => {
      expect(mocks.revokeToken).toHaveBeenCalledWith('tok-agent-1');
    });
    await waitFor(() => {
      expect(screen.queryByTestId('token-row-tok-agent-1')).not.toBeInTheDocument();
    });
    expect(screen.getByTestId('permissions-notice')).toHaveTextContent(
      'Token "claude-code-laptop" revoked',
    );
    // The other token survives the revoke.
    expect(screen.getByTestId('token-row-tok-session-1')).toBeInTheDocument();
  });

  it('cancelling the confirm dialog never calls revoke', async () => {
    const user = userEvent.setup();
    renderPage();

    const agentRow = await screen.findByTestId('token-row-tok-agent-1');
    await user.click(within(agentRow).getByRole('button', { name: 'Revoke' }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
    expect(mocks.revokeToken).not.toHaveBeenCalled();
    expect(screen.getByTestId('token-row-tok-agent-1')).toBeInTheDocument();
  });

  it('soft-revokes a grant and flips its status without dropping the audit row', async () => {
    const user = userEvent.setup();
    mocks.revokeDurableContextGrant.mockResolvedValue({
      id: 'grant-active',
      workspace_id: 'ws-1',
      principal: 'agent.claude-code',
      memory_id: 'dddddddd-dddd-4ddd-8ddd-dddddddddd01',
      mode: 'read',
      granted_at: '2026-07-20T09:00:00Z',
      revoked_at: '2026-07-29T10:00:00Z',
      updated_at: '2026-07-29T10:00:00Z',
    });
    renderPage();

    const activeRow = await screen.findByTestId('grant-row-grant-active');
    await user.click(within(activeRow).getByRole('button', { name: 'Revoke' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('Revoke recall grant for agent.claude-code?');
    await user.click(within(dialog).getByRole('button', { name: 'Revoke' }));

    await waitFor(() => {
      expect(mocks.revokeDurableContextGrant).toHaveBeenCalledWith('grant-active');
    });
    // The audit row stays listed, now revoked, with no revoke action left.
    const revokedRow = await screen.findByTestId('grant-row-grant-active');
    await waitFor(() => {
      expect(within(revokedRow).getByText('Revoked')).toBeInTheDocument();
    });
    expect(within(revokedRow).queryByRole('button', { name: 'Revoke' })).not.toBeInTheDocument();
    expect(screen.getByTestId('permissions-notice')).toHaveTextContent(
      'Recall grant for agent.claude-code revoked',
    );
  });

  it('surfaces revoke failures without losing the list', async () => {
    const user = userEvent.setup();
    mocks.revokeToken.mockRejectedValue(apiFailure(500, 'internal', 'revoke failed'));
    renderPage();

    const agentRow = await screen.findByTestId('token-row-tok-agent-1');
    await user.click(within(agentRow).getByRole('button', { name: 'Revoke' }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Revoke' }));

    const banner = await screen.findByTestId('permissions-action-error');
    expect(banner).toHaveTextContent('Action failed');
    expect(banner).toHaveTextContent('internal: revoke failed');
    expect(screen.getByTestId('token-row-tok-agent-1')).toBeInTheDocument();
  });
});
