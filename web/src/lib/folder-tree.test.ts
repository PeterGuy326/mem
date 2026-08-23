import { describe, it, expect } from 'vitest';
import {
  normalizePath,
  parentPath,
  basename,
  joinPath,
  splitPath,
  isDescendant,
  buildTree,
  findNode,
  buildCrumbs,
  pathToDriveRoute,
  driveRouteToPath,
} from './folder-tree';

describe('normalizePath', () => {
  it('returns / for empty string', () => {
    expect(normalizePath('')).toBe('/');
  });
  it('adds leading slash', () => {
    expect(normalizePath('Photos')).toBe('/Photos');
  });
  it('strips trailing slash', () => {
    expect(normalizePath('/Photos/')).toBe('/Photos');
  });
  it('preserves valid path', () => {
    expect(normalizePath('/Photos/2012')).toBe('/Photos/2012');
  });
});

describe('parentPath', () => {
  it('root parent is root', () => {
    expect(parentPath('/')).toBe('/');
  });
  it('returns parent folder', () => {
    expect(parentPath('/Photos/2012/img.jpg')).toBe('/Photos/2012');
  });
  it('top-level file parent is root', () => {
    expect(parentPath('/file.txt')).toBe('/');
  });
});

describe('basename', () => {
  it('root basename is empty', () => {
    expect(basename('/')).toBe('');
  });
  it('returns last segment', () => {
    expect(basename('/Photos/2012/img.jpg')).toBe('img.jpg');
  });
});

describe('joinPath', () => {
  it('joins root + name', () => {
    expect(joinPath('/', 'Photos')).toBe('/Photos');
  });
  it('joins folder + name', () => {
    expect(joinPath('/Photos', '2012')).toBe('/Photos/2012');
  });
});

describe('splitPath', () => {
  it('root splits to empty array', () => {
    expect(splitPath('/')).toEqual([]);
  });
  it('splits segments', () => {
    expect(splitPath('/Photos/2012/img.jpg')).toEqual(['Photos', '2012', 'img.jpg']);
  });
});

describe('isDescendant', () => {
  it('everything is descendant of root', () => {
    expect(isDescendant('/Photos/2012', '/')).toBe(true);
  });
  it('path is descendant of itself', () => {
    expect(isDescendant('/Photos', '/Photos')).toBe(true);
  });
  it('sibling is not descendant', () => {
    expect(isDescendant('/Documents', '/Photos')).toBe(false);
  });
  it('partial name match is not descendant', () => {
    expect(isDescendant('/Photos2', '/Photos')).toBe(false);
  });
});

describe('buildTree', () => {
  it('builds a tree from files', () => {
    const files = [
      { path: '/Photos/a.jpg' },
      { path: '/Photos/b.jpg' },
      { path: '/Docs/report.pdf' },
    ];
    const tree = buildTree(files);
    expect(tree.path).toBe('/');
    expect(tree.fileCount).toBe(3);
    expect(tree.children).toHaveLength(2);
    const docs = tree.children.find((c) => c.name === 'Docs');
    expect(docs?.fileCount).toBe(1);
  });

  it('includes ghost folders', () => {
    const tree = buildTree([], ['/Empty']);
    expect(tree.children).toHaveLength(1);
    expect(tree.children[0]?.name).toBe('Empty');
    expect(tree.children[0]?.fileCount).toBe(0);
  });
});

describe('findNode', () => {
  it('finds root', () => {
    const tree = buildTree([{ path: '/a.txt' }]);
    expect(findNode(tree, '/')?.path).toBe('/');
  });
  it('finds nested folder', () => {
    const tree = buildTree([{ path: '/Photos/2012/img.jpg' }]);
    const node = findNode(tree, '/Photos/2012');
    expect(node?.name).toBe('2012');
  });
  it('returns null for missing path', () => {
    const tree = buildTree([{ path: '/a.txt' }]);
    expect(findNode(tree, '/missing')).toBeNull();
  });
});

describe('buildCrumbs', () => {
  it('root crumbs', () => {
    const crumbs = buildCrumbs('/');
    expect(crumbs).toHaveLength(1);
    expect(crumbs[0]?.path).toBe('/');
  });
  it('nested crumbs', () => {
    const crumbs = buildCrumbs('/Photos/2012');
    expect(crumbs).toHaveLength(3);
    expect(crumbs[1]).toEqual({ name: 'Photos', path: '/Photos' });
    expect(crumbs[2]).toEqual({ name: '2012', path: '/Photos/2012' });
  });
});

describe('pathToDriveRoute', () => {
  it('root maps to /drive', () => {
    expect(pathToDriveRoute('/')).toBe('/drive');
  });
  it('encodes segments', () => {
    expect(pathToDriveRoute('/My Photos/2012')).toBe('/drive/My%20Photos/2012');
  });
});

describe('driveRouteToPath', () => {
  it('undefined maps to root', () => {
    expect(driveRouteToPath(undefined)).toBe('/');
  });
  it('decodes segments', () => {
    expect(driveRouteToPath('My%20Photos/2012')).toBe('/My Photos/2012');
  });
});
