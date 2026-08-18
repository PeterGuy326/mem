/**
 * Component tests for MemoryRelationsPanel: outbound/inbound relation rows,
 * the empty state, and the error state, driven by a deterministic msw server.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { MemoryRouter } from 'react-router';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { I18nProvider } from '@/i18n';
import type { Capabilities, MemoryRelation } from '@/lib/types';
import { MemoryRelationsPanel } from './MemoryRelationsPanel';

const MEMORY_ID = '11111111-1111-4111-8111-111111111111';
const PEER_ID = '22222222-2222-4222-8222-222222222222';

const CAPABILITIES: Capabilities = {
  deployment_mode: 'standard',
  registration_mode: 'open',
  workspace: {
    id: '33333333-3333-4333-8333-333333333333',
    name: 'relations-test',
    resource_owner_user_id: '44444444-4444-4444-8444-444444444444',
    role: 'owner',
    created_at: '2026-01-01T00:00:00Z',
  },
  features: {
    context: true,
    handoff: true,
    memory: true,
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

const OUTBOUND: MemoryRelation[] = [
  {
    id: '55555555-5555-4555-8555-555555555555',
    workspace_id: CAPABILITIES.workspace.id,
    source_id: MEMORY_ID,
    target_id: PEER_ID,
    relation_type: 'supersedes',
    reason: 'v2 replaces the stale v1 snapshot',
    created_at: '2026-07-01T03:04:05Z',
  },
];

const INBOUND: MemoryRelation[] = [
  {
    id: '66666666-6666-4666-8666-666666666666',
    workspace_id: CAPABILITIES.workspace.id,
    source_id: PEER_ID,
    target_id: MEMORY_ID,
    relation_type: 'corrects',
    created_at: '2026-07-02T05:06:07Z',
  },
];

const server = setupServer(
  http.get('/v1/capabilities', () => HttpResponse.json(CAPABILITIES)),
  http.get(`/v1/memories/${MEMORY_ID}/relations`, ({ request }) => {
    const direction = new URL(request.url).searchParams.get('direction');
    return HttpResponse.json({
      relations: direction === 'source' ? OUTBOUND : INBOUND,
    });
  }),
  http.get(`/v1/memories/${PEER_ID}`, () =>
    HttpResponse.json({
      id: PEER_ID,
      workspace_id: CAPABILITIES.workspace.id,
      kind: 'note',
      content: 'peer memory body',
      content_sha256: 'a'.repeat(64),
      path: '/',
      lifecycle_status: 'active',
      state_version: 1,
      created_at: '2026-06-30T00:00:00Z',
      updated_at: '2026-06-30T00:00:00Z',
    }),
  ),
);

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  server.resetHandlers();
  cleanup();
});
afterAll(() => server.close());

function renderPanel() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <I18nProvider>
        <MemoryRouter>
          <MemoryRelationsPanel memoryID={MEMORY_ID} />
        </MemoryRouter>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('MemoryRelationsPanel', () => {
  it('renders outbound and inbound relation rows with badges and reason', async () => {
    renderPanel();

    expect(await screen.findByText('Points to')).toBeInTheDocument();
    expect(screen.getByText('Pointed to by')).toBeInTheDocument();
    expect(screen.getByText('Supersedes')).toBeInTheDocument();
    expect(screen.getByText('Corrects')).toBeInTheDocument();
    expect(screen.getByText(/v2 replaces the stale v1 snapshot/)).toBeInTheDocument();
    // The same peer appears once per direction section.
    expect((await screen.findAllByText('peer memory body')).length).toBe(2);
  });

  it('shows the empty state when no relations exist', async () => {
    server.use(
      http.get(`/v1/memories/${MEMORY_ID}/relations`, () =>
        HttpResponse.json({ relations: [] }),
      ),
    );

    renderPanel();

    expect(await screen.findByText('No relations recorded')).toBeInTheDocument();
  });

  it('shows the error state when the relations endpoint fails', async () => {
    server.use(
      http.get(`/v1/memories/${MEMORY_ID}/relations`, () =>
        HttpResponse.json({ error: 'boom' }, { status: 500 }),
      ),
    );

    renderPanel();

    await waitFor(() => {
      expect(screen.getByText('Could not load memory relations')).toBeInTheDocument();
    });
  });
});
