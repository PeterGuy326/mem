/**
 * Component tests for the TransferPage import-history block.
 *
 * A dedicated msw server with deterministic fixtures drives every state:
 * loading skeleton, newest-first ledger rows with expandable details, the
 * empty state, and the error state with a working retry.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { I18nProvider } from '@/i18n';
import type { Capabilities, WorkspaceImportHistoryEntry } from '@/lib/types';
import { TransferPage } from '@/pages/TransferPage';

const CAPABILITIES: Capabilities = {
  deployment_mode: 'standard',
  registration_mode: 'open',
  workspace: {
    id: '11111111-1111-4111-8111-111111111111',
    name: 'history-test',
    resource_owner_user_id: '22222222-2222-4222-8222-222222222222',
    role: 'owner',
    created_at: '2026-01-01T00:00:00Z',
  },
  features: {
    context: true,
    handoff: true,
    workspace_export: true,
    workspace_import: true,
  },
  workspace_restore_modes: ['fresh'],
  workspace_bundle_schema_versions: [1, 2],
  permissions: {
    read: true,
    search: true,
    write: true,
    delete: true,
    provider_read: true,
    provider_modify: true,
    workspace_export: true,
    workspace_import: true,
  },
};

// Deterministic ledger projection, newest first — mirrors the server contract:
// only committed imports, always succeeded; fresh rows carry zero
// conflicts/skips.
const HISTORY_ENTRIES: WorkspaceImportHistoryEntry[] = [
  {
    bundle_id: '77777777-7777-4777-8777-777777777777',
    archive_sha256: 'c'.repeat(64),
    source_workspace_id: '99999999-9999-4999-8999-999999999999',
    schema_version: 2,
    restore_mode: 'fresh',
    result_status: 'succeeded',
    conflict_count: 0,
    skipped_count: 0,
    imported_at: '2026-07-28T07:08:09Z',
  },
  {
    bundle_id: '66666666-6666-4666-8666-666666666666',
    archive_sha256: 'b'.repeat(64),
    source_workspace_id: '88888888-8888-4888-8888-888888888888',
    schema_version: 1,
    restore_mode: 'fresh',
    result_status: 'succeeded',
    conflict_count: 0,
    skipped_count: 0,
    imported_at: '2026-07-01T03:04:05Z',
  },
];

const server = setupServer(
  http.get('/v1/capabilities', () => HttpResponse.json(CAPABILITIES)),
  http.get('/v1/workspaces/current/imports', () =>
    HttpResponse.json({ items: HISTORY_ENTRIES, count: HISTORY_ENTRIES.length }),
  ),
);

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  server.resetHandlers();
  cleanup();
});
afterAll(() => server.close());

function renderTransferPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <I18nProvider>
        <TransferPage />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('TransferPage import history', () => {
  it('shows a loading skeleton while the ledger is fetched', async () => {
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    server.use(
      http.get('/v1/workspaces/current/imports', async () => {
        await pending;
        return HttpResponse.json({ items: HISTORY_ENTRIES, count: HISTORY_ENTRIES.length });
      }),
    );

    renderTransferPage();

    expect(await screen.findByTestId('workspace-import-history')).toBeInTheDocument();
    expect(screen.getByTestId('workspace-import-history-loading')).toBeInTheDocument();

    release();
    expect(await screen.findByTestId('workspace-import-history-list')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.queryByTestId('workspace-import-history-loading')).not.toBeInTheDocument();
    });
  });

  it('lists ledger entries newest first and expands conflict/skip details', async () => {
    const user = userEvent.setup();
    renderTransferPage();

    const list = await screen.findByTestId('workspace-import-history-list');
    const rows = within(list).getAllByTestId('workspace-import-history-row');
    expect(rows).toHaveLength(2);
    const [newestRow, olderRow] = rows as [HTMLElement, HTMLElement];
    // Newest entry (bundle 7777…) renders above the older one (bundle 6666…).
    expect(newestRow.textContent).toContain('77777777-7777');
    expect(olderRow.textContent).toContain('66666666-6666');

    const toggle = within(newestRow).getByTestId('workspace-import-history-toggle');
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await user.click(toggle);

    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    const detail = await screen.findByTestId('workspace-import-history-detail');
    // Expanded detail shows the full bundle identity, mode, counts, and digest.
    expect(detail.textContent).toContain('77777777-7777-4777-8777-777777777777');
    expect(detail.textContent).toContain('99999999-9999-4999-8999-999999999999');
    expect(detail.textContent).toContain('v2');
    expect(detail.textContent).toContain('fresh');
    expect(detail.textContent).toContain('c'.repeat(64));

    await user.click(toggle);
    await waitFor(() => {
      expect(screen.queryByTestId('workspace-import-history-detail')).not.toBeInTheDocument();
    });
  });

  it('renders the empty state when the ledger has no entries', async () => {
    server.use(
      http.get('/v1/workspaces/current/imports', () =>
        HttpResponse.json({ items: [], count: 0 }),
      ),
    );

    renderTransferPage();

    expect(await screen.findByTestId('workspace-import-history-empty')).toBeInTheDocument();
    expect(screen.queryByTestId('workspace-import-history-list')).not.toBeInTheDocument();
  });

  it('renders the error state and recovers through retry', async () => {
    let calls = 0;
    server.use(
      http.get('/v1/workspaces/current/imports', () => {
        calls += 1;
        if (calls === 1) {
          return HttpResponse.json(
            {
              error: 'workspace_transfer_failed',
              hint: 'workspace_transfer_failed',
            },
            { status: 500 },
          );
        }
        return HttpResponse.json({ items: HISTORY_ENTRIES, count: HISTORY_ENTRIES.length });
      }),
    );
    const user = userEvent.setup();

    renderTransferPage();

    const notice = await screen.findByTestId('transfer-error-server');
    expect(screen.queryByTestId('workspace-import-history-list')).not.toBeInTheDocument();

    await user.click(within(notice).getByRole('button'));
    expect(await screen.findByTestId('workspace-import-history-list')).toBeInTheDocument();
    expect(calls).toBe(2);
  });
});
