import { http, HttpResponse, delay } from 'msw';
import { FILES, findFile, relatedFor, searchFiles, ENTITIES } from './fixtures';
import type { MemFile, IndexStatus } from '@/lib/types';

const BASE = '/v1';

function jitter() {
  return delay(120 + Math.random() * 220);
}

function inferKind(mime: string, name: string): MemFile['kind'] {
  if (mime.startsWith('image/')) return 'image';
  if (mime.startsWith('audio/')) return 'audio';
  if (mime.startsWith('video/')) return 'video';
  if (mime === 'application/pdf' || name.endsWith('.pdf')) return 'pdf';
  if (mime.startsWith('text/') || name.match(/\.(md|txt|json|log|yml|yaml)$/i)) return 'text';
  if (name.match(/\.(docx?|xlsx?|pptx?)$/i)) return 'doc';
  return 'other';
}

export const handlers = [
  // ----- Auth -----
  http.post(`${BASE}/auth/login`, async ({ request }) => {
    await jitter();
    const body = (await request.json().catch(() => ({}))) as { email?: string };
    return HttpResponse.json({
      token: 'mock-token-' + Date.now().toString(36),
      user: {
        id: 'user-1',
        email: body?.email ?? 'demo@mem.dev',
        created_at: '2024-01-01T00:00:00Z',
      },
    });
  }),

  // ----- Files: list -----
  http.get(`${BASE}/files`, async ({ request }) => {
    await jitter();
    const url = new URL(request.url);
    const limit = Number(url.searchParams.get('limit') ?? 30);
    return HttpResponse.json({
      items: FILES.slice(0, limit),
      next_cursor: FILES.length > limit ? 'cursor-2' : null,
    });
  }),

  // ----- Files: detail -----
  http.get(`${BASE}/files/:id`, async ({ params }) => {
    await jitter();
    const file = findFile(String(params.id));
    if (!file) {
      return HttpResponse.json(
        { error: 'file not found', hint: '检查 file_id 是否正确，或文件可能已删除' },
        { status: 404 },
      );
    }
    return HttpResponse.json(file);
  }),

  // ----- Files: related -----
  http.get(`${BASE}/files/:id/related`, async ({ params }) => {
    await jitter();
    return HttpResponse.json({ results: relatedFor(String(params.id)) });
  }),

  // ----- Files: delete -----
  http.delete(`${BASE}/files/:id`, async ({ params }) => {
    await jitter();
    const idx = FILES.findIndex((f) => f.id === String(params.id));
    if (idx >= 0) FILES.splice(idx, 1);
    return HttpResponse.json({ ok: true });
  }),

  // ----- Files: upload -----
  http.post(`${BASE}/files`, async ({ request }) => {
    await jitter();
    const form = await request.formData();
    const file = form.get('file');
    const nameOverride = String(form.get('name') ?? '');

    if (!(file instanceof File)) {
      return HttpResponse.json({ error: 'missing file', hint: '请通过 multipart 上传文件' }, { status: 400 });
    }

    const name = nameOverride || file.name;
    const mime = file.type || 'application/octet-stream';
    const kind = inferKind(mime, name);
    const id = 'upl-' + Math.random().toString(36).slice(2, 10);
    const now = new Date().toISOString();
    const previewUrl =
      kind === 'image'
        ? `https://picsum.photos/seed/${encodeURIComponent(id)}/1200/900`
        : null;
    const status: IndexStatus = 'processing';

    const created: MemFile = {
      id,
      user_id: 'user-1',
      name,
      path: `/Inbox/${name}`,
      size: file.size,
      sha256: id.padEnd(64, '0'),
      mime,
      storage_key: `s3://mem/inbox/${id}`,
      summary: null,
      caption: null,
      tags: [],
      timeline_at: now,
      geo: null,
      index_status: status,
      created_at: now,
      updated_at: now,
      kind,
      preview_url: previewUrl,
      thumbnail_url: previewUrl,
      download_url: previewUrl,
      entities: [],
    };
    FILES.unshift(created);

    // Simulate AI pipeline: flip to done a few seconds later.
    setTimeout(() => {
      const f = findFile(id);
      if (!f) return;
      f.index_status = 'done';
      if (f.kind === 'image') {
        f.caption = '刚上传的照片 — AI 已生成 caption 占位';
        f.tags = ['新上传'];
      } else {
        f.summary = '刚上传的文件 — AI 摘要占位';
        f.tags = ['新上传'];
      }
      f.updated_at = new Date().toISOString();
    }, 4000);

    return HttpResponse.json(created, { status: 201 });
  }),

  // ----- Search -----
  http.get(`${BASE}/search`, async ({ request }) => {
    await jitter();
    const url = new URL(request.url);
    const q = url.searchParams.get('q') ?? '';
    const type = url.searchParams.get('type') ?? undefined;
    const since = url.searchParams.get('since') ?? undefined;
    const until = url.searchParams.get('until') ?? undefined;
    const face = url.searchParams.get('face') ?? undefined;
    const limit = Number(url.searchParams.get('limit') ?? 30);
    const { results, total } = searchFiles({ q, type, since, until, face, limit });
    return HttpResponse.json({
      results,
      total,
      query_plan: {
        entities: ENTITIES.filter((e) => q.includes(e.name)).map((e) => e.name),
        semantic_query: q,
      },
      _meta: { quota_remaining: 9999, latency_ms: Math.round(120 + Math.random() * 180) },
    });
  }),

  // ----- Entities / faces (W2 placeholder) -----
  http.get(`${BASE}/entities`, async ({ request }) => {
    await jitter();
    const url = new URL(request.url);
    const type = url.searchParams.get('type');
    const items = type ? ENTITIES.filter((e) => e.type === type) : ENTITIES;
    return HttpResponse.json({ items });
  }),
];
