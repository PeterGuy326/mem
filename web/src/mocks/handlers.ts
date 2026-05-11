import { http, HttpResponse, delay } from 'msw';
import { FILES, GHOST_FOLDERS, findFile, relatedFor, searchFiles, ENTITIES } from './fixtures';
import type { MemFile, IndexStatus } from '@/lib/types';
import {
  ROOT_PATH,
  basename,
  buildTree,
  isDescendant,
  joinPath,
  normalizePath,
  parentPath,
} from '@/lib/folder-tree';

const BASE = '/v1';

function jitter(min = 120, max = 320) {
  return delay(min + Math.random() * (max - min));
}

function inferKind(mime: string, name: string): MemFile['kind'] {
  if (mime.startsWith('image/')) return 'image';
  if (mime.startsWith('audio/')) return 'audio';
  if (mime.startsWith('video/')) return 'video';
  if (mime === 'application/pdf' || name.endsWith('.pdf')) return 'pdf';
  if (mime.startsWith('text/') || /\.(md|txt|json|log|ya?ml)$/i.test(name)) return 'text';
  if (/\.(docx?|xlsx?|pptx?)$/i.test(name)) return 'doc';
  return 'other';
}

function listByPath(path: string): MemFile[] {
  const target = normalizePath(path);
  return FILES.filter((f) => parentPath(f.path) === target);
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

  // ----- Files: list (by path) -----
  http.get(`${BASE}/files`, async ({ request }) => {
    await jitter();
    const url = new URL(request.url);
    const path = url.searchParams.get('path');
    const limit = Number(url.searchParams.get('limit') ?? 500);
    if (path !== null) {
      const items = listByPath(path).slice(0, limit);
      return HttpResponse.json({ items, next_cursor: null });
    }
    // Default: latest N globally (unused in explorer, kept for compatibility).
    return HttpResponse.json({
      items: FILES.slice(0, limit),
      next_cursor: FILES.length > limit ? 'cursor-2' : null,
    });
  }),

  // ----- Files: folder tree -----
  http.get(`${BASE}/files/tree`, async () => {
    await jitter(80, 200);
    const tree = buildTree(FILES, Array.from(GHOST_FOLDERS));
    return HttpResponse.json({ tree });
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

  // ----- Files: patch (move / rename) -----
  http.patch(`${BASE}/files/:id`, async ({ params, request }) => {
    await delay(150 + Math.random() * 150);
    const file = findFile(String(params.id));
    if (!file) {
      return HttpResponse.json({ error: 'file not found' }, { status: 404 });
    }
    const body = (await request.json().catch(() => ({}))) as { path?: string; name?: string };
    if (body.name) {
      file.name = body.name;
      file.path = joinPath(parentPath(file.path), body.name);
    }
    if (body.path) {
      // body.path is the new full path (folder path); preserve filename.
      const targetFolder = normalizePath(body.path);
      file.path = joinPath(targetFolder, file.name);
    }
    file.updated_at = new Date().toISOString();
    return HttpResponse.json(file);
  }),

  // ----- Files: delete -----
  http.delete(`${BASE}/files/:id`, async ({ params }) => {
    await jitter();
    const idx = FILES.findIndex((f) => f.id === String(params.id));
    if (idx >= 0) FILES.splice(idx, 1);
    return HttpResponse.json({ ok: true });
  }),

  // ----- Files: upload (multipart) -----
  http.post(`${BASE}/files`, async ({ request }) => {
    await delay(600 + Math.random() * 600);
    const form = await request.formData();
    const file = form.get('file');
    const nameOverride = String(form.get('name') ?? '');
    const targetPath = normalizePath(String(form.get('path') ?? ROOT_PATH));

    if (!(file instanceof File)) {
      return HttpResponse.json(
        { error: 'missing file', hint: '请通过 multipart 上传文件' },
        { status: 400 },
      );
    }

    const name = nameOverride || file.name;
    const mime = file.type || 'application/octet-stream';
    const kind = inferKind(mime, name);
    const id = 'upl-' + Math.random().toString(36).slice(2, 10);
    const now = new Date().toISOString();
    const previewUrl =
      kind === 'image' ? `https://picsum.photos/seed/${encodeURIComponent(id)}/1200/900` : null;
    const status: IndexStatus = 'processing';

    const created: MemFile = {
      id,
      user_id: 'user-1',
      name,
      path: joinPath(targetPath, name),
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

    // Simulate AI pipeline: flip to done.
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

  // ----- Folders: create -----
  http.post(`${BASE}/folders`, async ({ request }) => {
    await jitter(80, 200);
    const body = (await request.json().catch(() => ({}))) as { path?: string; name?: string };
    if (!body.path || !body.name) {
      return HttpResponse.json(
        { error: 'missing path or name', hint: '需要 path（父目录）和 name（新文件夹名）' },
        { status: 400 },
      );
    }
    const full = joinPath(normalizePath(body.path), body.name);
    GHOST_FOLDERS.add(full);
    return HttpResponse.json({ path: full, name: body.name });
  }),

  // ----- Folders: rename -----
  http.patch(`${BASE}/folders`, async ({ request }) => {
    await delay(150 + Math.random() * 150);
    const body = (await request.json().catch(() => ({}))) as { path?: string; name?: string };
    if (!body.path || !body.name) {
      return HttpResponse.json({ error: 'missing fields' }, { status: 400 });
    }
    const oldPath = normalizePath(body.path);
    const newPath = joinPath(parentPath(oldPath), body.name);
    // Rewrite descendants of old folder
    for (const f of FILES) {
      if (isDescendant(f.path, oldPath) && f.path !== oldPath) {
        f.path = newPath + f.path.slice(oldPath.length);
        f.updated_at = new Date().toISOString();
      }
    }
    // Move any ghost folders too
    const ghosts = Array.from(GHOST_FOLDERS);
    GHOST_FOLDERS.clear();
    for (const g of ghosts) {
      if (g === oldPath || isDescendant(g, oldPath)) {
        GHOST_FOLDERS.add(newPath + g.slice(oldPath.length));
      } else {
        GHOST_FOLDERS.add(g);
      }
    }
    return HttpResponse.json({ ok: true, old_path: oldPath, new_path: newPath });
  }),

  // ----- Folders: move (change parent) -----
  http.put(`${BASE}/folders`, async ({ request }) => {
    await delay(150 + Math.random() * 150);
    const body = (await request.json().catch(() => ({}))) as { path?: string; new_parent?: string };
    if (!body.path || !body.new_parent) {
      return HttpResponse.json({ error: 'missing fields' }, { status: 400 });
    }
    const oldPath = normalizePath(body.path);
    const newPath = joinPath(normalizePath(body.new_parent), basename(oldPath));
    if (isDescendant(newPath, oldPath)) {
      return HttpResponse.json(
        { error: 'cannot move a folder into itself', hint: '目标路径不能是源路径的子目录' },
        { status: 400 },
      );
    }
    for (const f of FILES) {
      if (isDescendant(f.path, oldPath)) {
        f.path = newPath + f.path.slice(oldPath.length);
        f.updated_at = new Date().toISOString();
      }
    }
    const ghosts = Array.from(GHOST_FOLDERS);
    GHOST_FOLDERS.clear();
    for (const g of ghosts) {
      if (g === oldPath || isDescendant(g, oldPath)) {
        GHOST_FOLDERS.add(newPath + g.slice(oldPath.length));
      } else {
        GHOST_FOLDERS.add(g);
      }
    }
    return HttpResponse.json({ ok: true, old_path: oldPath, new_path: newPath });
  }),

  // ----- Folders: delete -----
  http.delete(`${BASE}/folders`, async ({ request }) => {
    await jitter();
    const url = new URL(request.url);
    const path = normalizePath(url.searchParams.get('path') ?? '');
    if (path === ROOT_PATH) {
      return HttpResponse.json({ error: 'cannot delete root' }, { status: 400 });
    }
    // Remove all files inside
    for (let i = FILES.length - 1; i >= 0; i--) {
      if (isDescendant(FILES[i]!.path, path)) FILES.splice(i, 1);
    }
    const ghosts = Array.from(GHOST_FOLDERS);
    GHOST_FOLDERS.clear();
    for (const g of ghosts) {
      if (!(g === path || isDescendant(g, path))) GHOST_FOLDERS.add(g);
    }
    return HttpResponse.json({ ok: true });
  }),

  // ----- Search (kept for compatibility) -----
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

  // ----- Entities / faces -----
  http.get(`${BASE}/entities`, async ({ request }) => {
    await jitter();
    const url = new URL(request.url);
    const type = url.searchParams.get('type');
    const items = type ? ENTITIES.filter((e) => e.type === type) : ENTITIES;
    return HttpResponse.json({ items });
  }),
];
