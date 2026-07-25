/**
 * Folder-tree utilities.
 *
 * Files carry an absolute virtual `path` like `/Photos/2012/IMG_2012_0427.jpg`.
 * From a flat list of files we derive a hierarchical FolderNode tree, and we
 * can list a single folder's immediate children (sub-folders + direct files).
 *
 * Conventions:
 * - Root path = "/" with name "我的网盘".
 * - Paths are POSIX-style, always start with "/", folders have no trailing slash.
 * - A folder "exists" iff at least one file lives under it.
 *   (We don't (yet) have a server-side folders table — mock-only "ghost folders"
 *   are tracked separately by the mock layer.)
 */

import type { MemFile } from './types';

export interface FolderNode {
  id?: string;
  name: string;
  path: string;
  children: FolderNode[];
  fileCount: number; // direct + descendants
}

export const ROOT_PATH = '/';
export const ROOT_NAME = '我的网盘';

/** Normalize `/a/b/` → `/a/b`, `` → `/`. */
export function normalizePath(p: string): string {
  if (!p) return ROOT_PATH;
  let out = p.trim();
  if (!out.startsWith('/')) out = '/' + out;
  if (out.length > 1 && out.endsWith('/')) out = out.slice(0, -1);
  return out;
}

/** Folder containing the given full path (file or folder). */
export function parentPath(p: string): string {
  const n = normalizePath(p);
  if (n === ROOT_PATH) return ROOT_PATH;
  const i = n.lastIndexOf('/');
  if (i <= 0) return ROOT_PATH;
  return n.slice(0, i);
}

/** Last segment of a path. Root → "". */
export function basename(p: string): string {
  const n = normalizePath(p);
  if (n === ROOT_PATH) return '';
  const i = n.lastIndexOf('/');
  return n.slice(i + 1);
}

/** Join one folder path with a child name into a new normalized path. */
export function joinPath(folder: string, name: string): string {
  const f = normalizePath(folder);
  if (f === ROOT_PATH) return `/${name}`;
  return `${f}/${name}`;
}

/** Split `/Photos/2012/IMG.jpg` → ['Photos','2012','IMG.jpg']. */
export function splitPath(p: string): string[] {
  const n = normalizePath(p);
  if (n === ROOT_PATH) return [];
  return n.slice(1).split('/');
}

/** Is `child` strictly inside `parent` (or equal)? Both are normalized. */
export function isDescendant(child: string, parent: string): boolean {
  const c = normalizePath(child);
  const p = normalizePath(parent);
  if (p === ROOT_PATH) return true;
  return c === p || c.startsWith(p + '/');
}

/**
 * Build a full tree from files + an optional list of "ghost folders" — empty
 * directories created via the mock POST /v1/folders endpoint.
 */
export function buildTree(files: { path: string }[], ghostFolders: string[] = []): FolderNode {
  const root: FolderNode = { name: ROOT_NAME, path: ROOT_PATH, children: [], fileCount: 0 };

  function ensureFolder(folderPath: string): FolderNode {
    const segs = splitPath(folderPath);
    let cur = root;
    let acc = '';
    for (const seg of segs) {
      acc = acc ? `${acc}/${seg}` : `/${seg}`;
      let next = cur.children.find((c) => c.name === seg);
      if (!next) {
        next = { name: seg, path: acc, children: [], fileCount: 0 };
        cur.children.push(next);
      }
      cur = next;
    }
    return cur;
  }

  for (const f of files) {
    const folder = parentPath(f.path);
    const node = ensureFolder(folder);
    // Bump count up the chain.
    node.fileCount += 1;
    let walk: FolderNode | undefined = root;
    const segs = splitPath(folder);
    for (const seg of segs) {
      walk = walk!.children.find((c) => c.name === seg);
      if (!walk) break;
    }
    // Walk for parents too — easier: re-walk from root and bump every ancestor (excl. node already bumped).
  }

  // Recompute fileCount = sum of all descendants direct file counts. (Simpler & correct.)
  // First reset, then count direct files per folder, then propagate.
  function reset(n: FolderNode) {
    n.fileCount = 0;
    n.children.forEach(reset);
  }
  reset(root);

  // Direct file count per folder
  for (const f of files) {
    const folder = parentPath(f.path);
    const node = ensureFolder(folder);
    node.fileCount += 1;
  }

  // Ghost folders may not have files yet — make sure they're materialized.
  for (const g of ghostFolders) ensureFolder(g);

  // Propagate descendant counts upward
  function propagate(n: FolderNode): number {
    let total = n.fileCount;
    for (const c of n.children) total += propagate(c);
    n.fileCount = total;
    return total;
  }
  propagate(root);

  // Stable alpha sort at each level
  function sortRec(n: FolderNode) {
    n.children.sort((a, b) => a.name.localeCompare(b.name, 'zh-Hans-CN'));
    n.children.forEach(sortRec);
  }
  sortRec(root);

  return root;
}

/** Find a node by absolute path. Returns root for "/". */
export function findNode(tree: FolderNode, path: string): FolderNode | null {
  const target = normalizePath(path);
  if (target === ROOT_PATH) return tree;
  const segs = splitPath(target);
  let cur: FolderNode | undefined = tree;
  for (const seg of segs) {
    cur = cur?.children.find((c) => c.name === seg);
    if (!cur) return null;
  }
  return cur ?? null;
}

export interface ListChildrenResult {
  folders: FolderNode[];
  files: MemFile[];
}

/** Return the immediate sub-folders + direct files under a given path. */
export function listChildren(
  tree: FolderNode,
  files: MemFile[],
  currentPath: string,
): ListChildrenResult {
  const node = findNode(tree, currentPath);
  const folders = node ? [...node.children] : [];
  const target = normalizePath(currentPath);
  const direct = files.filter((f) => parentPath(f.path) === target);
  // Sort: folders alpha, files by created_at desc (newer first).
  direct.sort((a, b) => (a.created_at < b.created_at ? 1 : -1));
  return { folders, files: direct };
}

/** Crumbs for "/Photos/2012" → [root, Photos, 2012]. */
export function buildCrumbs(path: string): Array<{ name: string; path: string }> {
  const n = normalizePath(path);
  const crumbs: Array<{ name: string; path: string }> = [{ name: ROOT_NAME, path: ROOT_PATH }];
  if (n === ROOT_PATH) return crumbs;
  let acc = '';
  for (const seg of splitPath(n)) {
    acc = acc ? `${acc}/${seg}` : `/${seg}`;
    crumbs.push({ name: seg, path: acc });
  }
  return crumbs;
}

/** Strip an absolute path to a routable segment under /drive. */
export function pathToDriveRoute(p: string): string {
  const n = normalizePath(p);
  if (n === ROOT_PATH) return '/drive';
  // encode each segment so unicode/space stays clean
  const encoded = splitPath(n)
    .map((s) => encodeURIComponent(s))
    .join('/');
  return `/drive/${encoded}`;
}

/** Inverse of pathToDriveRoute. */
export function driveRouteToPath(splat: string | undefined): string {
  if (!splat) return ROOT_PATH;
  // Strip leading/trailing slashes; decode each segment.
  const trimmed = splat.replace(/^\/+|\/+$/g, '');
  if (!trimmed) return ROOT_PATH;
  const segs = trimmed.split('/').map((s) => {
    try {
      return decodeURIComponent(s);
    } catch {
      return s;
    }
  });
  return '/' + segs.join('/');
}
