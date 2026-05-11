// Package folder implements the Folder service.
//
// Folders are first-class citizens (SPEC §6bis, decision B locked at v0.3):
//   - persisted in the `folders` table (supports empty folders)
//   - the root "/" is implicit (not stored)
//   - rename / move is a transactional batch update of every descendant's
//     `path` prefix in both `folders` and `files`
//
// All mutating operations run inside a single PostgreSQL transaction.
package folder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/pathx"
)

// Folder mirrors a row in the `folders` table.
type Folder struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"user_id"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// FileBrief is a lightweight projection of `files` rows for folder listings —
// keeps the folder package decoupled from the (heavier) file.File struct.
type FileBrief struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Size        int64      `json:"size"`
	MIME        string     `json:"mime"`
	IndexStatus string     `json:"index_status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	FolderID    *uuid.UUID `json:"folder_id,omitempty"`
}

// Node is a tree node produced by Tree.
type Node struct {
	ID        *uuid.UUID `json:"id,omitempty"` // nil for synthetic root
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	FileCount int        `json:"file_count"`
	Children  []*Node    `json:"children,omitempty"`
}

// Service is the folder service.
type Service struct {
	pool *pgxpool.Pool
}

// New constructs a folder Service.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Sentinel errors.
var (
	ErrNotFound   = errors.New("folder not found")
	ErrNotEmpty   = errors.New("folder is not empty")
	ErrCycle      = errors.New("cannot move folder into itself or a descendant")
	ErrRootOp     = errors.New("operation not allowed on root")
	ErrConflict   = errors.New("a folder with that path already exists")
)

// Create implements mkdir -p semantics: every missing ancestor is created.
// Returns the deepest folder (the one matching `path`). Idempotent.
//
// Passing "/" returns (nil, nil) because root is not stored.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, path string) (*Folder, error) {
	norm, err := pathx.Normalize(path)
	if err != nil {
		return nil, err
	}
	if norm == pathx.Root {
		return nil, nil
	}
	var deepest *Folder
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		f, e := ensureFolderTx(ctx, tx, userID, norm)
		if e != nil {
			return e
		}
		deepest = f
		return nil
	})
	return deepest, err
}

// ensureFolderTx walks every ancestor of `path` top-down and inserts any
// missing folder rows, returning the deepest folder. Must run inside a tx.
func ensureFolderTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, path string) (*Folder, error) {
	if path == pathx.Root {
		return nil, nil
	}
	var parentID *uuid.UUID
	var deepest *Folder
	for _, anc := range pathx.Ancestors(path) {
		existing, err := selectFolderByPathTx(ctx, tx, userID, anc)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if existing != nil {
			parentID = &existing.ID
			deepest = existing
			continue
		}
		name := pathx.Base(anc)
		id := uuid.New()
		now := time.Now().UTC()
		_, err = tx.Exec(ctx,
			`INSERT INTO folders (id, user_id, parent_id, path, name, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$6)`,
			id, userID, parentID, anc, name, now)
		if err != nil {
			return nil, fmt.Errorf("insert folder %q: %w", anc, err)
		}
		f := &Folder{
			ID: id, UserID: userID, ParentID: cloneUUID(parentID),
			Path: anc, Name: name, CreatedAt: now, UpdatedAt: now,
		}
		parentID = &id
		deepest = f
	}
	return deepest, nil
}

// Get returns the folder identified by path, or nil for root.
func (s *Service) Get(ctx context.Context, userID uuid.UUID, path string) (*Folder, error) {
	norm, err := pathx.Normalize(path)
	if err != nil {
		return nil, err
	}
	if norm == pathx.Root {
		return nil, nil
	}
	return s.selectFolderByPath(ctx, userID, norm)
}

// GetByID looks up a folder by its UUID (and verifies user ownership).
func (s *Service) GetByID(ctx context.Context, userID, id uuid.UUID) (*Folder, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, user_id, parent_id, path, name, created_at, updated_at
		 FROM folders WHERE id = $1 AND user_id = $2`, id, userID)
	return scanFolder(row)
}

// List returns the direct children of parentPath: the immediate subfolders +
// the files whose folder_id matches parentPath (NULL for root).
func (s *Service) List(ctx context.Context, userID uuid.UUID, parentPath string) ([]*Folder, []*FileBrief, error) {
	norm, err := pathx.Normalize(parentPath)
	if err != nil {
		return nil, nil, err
	}

	var parentID *uuid.UUID
	if norm != pathx.Root {
		p, err := s.selectFolderByPath(ctx, userID, norm)
		if err != nil {
			return nil, nil, err
		}
		parentID = &p.ID
	}

	folders, err := s.listSubfolders(ctx, userID, parentID)
	if err != nil {
		return nil, nil, err
	}
	files, err := s.listFiles(ctx, userID, parentID)
	if err != nil {
		return nil, nil, err
	}
	return folders, files, nil
}

func (s *Service) listSubfolders(ctx context.Context, userID uuid.UUID, parentID *uuid.UUID) ([]*Folder, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if parentID == nil {
		rows, err = s.pool.Query(ctx,
			`SELECT id, user_id, parent_id, path, name, created_at, updated_at
			 FROM folders WHERE user_id = $1 AND parent_id IS NULL
			 ORDER BY name`, userID)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id, user_id, parent_id, path, name, created_at, updated_at
			 FROM folders WHERE user_id = $1 AND parent_id = $2
			 ORDER BY name`, userID, *parentID)
	}
	if err != nil {
		return nil, fmt.Errorf("list subfolders: %w", err)
	}
	defer rows.Close()
	var out []*Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Service) listFiles(ctx context.Context, userID uuid.UUID, folderID *uuid.UUID) ([]*FileBrief, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if folderID == nil {
		rows, err = s.pool.Query(ctx,
			`SELECT id, name, path, size, mime, index_status, created_at, updated_at, folder_id
			 FROM files WHERE user_id = $1 AND folder_id IS NULL
			 ORDER BY name`, userID)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id, name, path, size, mime, index_status, created_at, updated_at, folder_id
			 FROM files WHERE user_id = $1 AND folder_id = $2
			 ORDER BY name`, userID, *folderID)
	}
	if err != nil {
		return nil, fmt.Errorf("list folder files: %w", err)
	}
	defer rows.Close()
	var out []*FileBrief
	for rows.Next() {
		var fb FileBrief
		if err := rows.Scan(&fb.ID, &fb.Name, &fb.Path, &fb.Size, &fb.MIME,
			&fb.IndexStatus, &fb.CreatedAt, &fb.UpdatedAt, &fb.FolderID); err != nil {
			return nil, err
		}
		out = append(out, &fb)
	}
	return out, rows.Err()
}

// Tree returns the full folder tree for a user, rooted at a synthetic "/".
// Each node carries the count of files directly inside it (does NOT recurse).
func (s *Service) Tree(ctx context.Context, userID uuid.UUID) (*Node, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, parent_id, path, name FROM folders WHERE user_id = $1 ORDER BY path`, userID)
	if err != nil {
		return nil, fmt.Errorf("tree folders: %w", err)
	}
	defer rows.Close()

	byID := map[uuid.UUID]*Node{}
	var rootChildren []*Node
	type parented struct {
		node     *Node
		parentID *uuid.UUID
	}
	var all []parented
	for rows.Next() {
		var (
			id       uuid.UUID
			parentID *uuid.UUID
			path     string
			name     string
		)
		if err := rows.Scan(&id, &parentID, &path, &name); err != nil {
			return nil, err
		}
		idCopy := id
		n := &Node{ID: &idCopy, Path: path, Name: name}
		byID[id] = n
		all = append(all, parented{n, parentID})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, p := range all {
		if p.parentID == nil {
			rootChildren = append(rootChildren, p.node)
			continue
		}
		if parent, ok := byID[*p.parentID]; ok {
			parent.Children = append(parent.Children, p.node)
		} else {
			// orphan — should not happen given FK + DELETE CASCADE
			rootChildren = append(rootChildren, p.node)
		}
	}

	// Populate file counts per folder + root.
	rootCount, perFolder, err := s.fileCounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	for id, c := range perFolder {
		if n, ok := byID[id]; ok {
			n.FileCount = c
		}
	}
	root := &Node{Path: pathx.Root, Name: "", FileCount: rootCount, Children: rootChildren}
	return root, nil
}

func (s *Service) fileCounts(ctx context.Context, userID uuid.UUID) (int, map[uuid.UUID]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT folder_id, COUNT(*) FROM files WHERE user_id = $1 GROUP BY folder_id`, userID)
	if err != nil {
		return 0, nil, fmt.Errorf("file counts: %w", err)
	}
	defer rows.Close()
	rootCount := 0
	perFolder := map[uuid.UUID]int{}
	for rows.Next() {
		var fid *uuid.UUID
		var c int
		if err := rows.Scan(&fid, &c); err != nil {
			return 0, nil, err
		}
		if fid == nil {
			rootCount = c
		} else {
			perFolder[*fid] = c
		}
	}
	return rootCount, perFolder, rows.Err()
}

// Rename changes a folder's basename while keeping its parent. Cascades the
// path-prefix update to every descendant folder and file in one tx.
func (s *Service) Rename(ctx context.Context, userID uuid.UUID, oldPath, newName string) error {
	oldNorm, err := pathx.Normalize(oldPath)
	if err != nil {
		return err
	}
	if oldNorm == pathx.Root {
		return ErrRootOp
	}
	if err := pathx.ValidateName(newName); err != nil {
		return err
	}
	parent := pathx.Parent(oldNorm)
	newPath, err := pathx.Join(parent, newName)
	if err != nil {
		return err
	}
	if newPath == oldNorm {
		return nil // no-op
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		src, err := selectFolderByPathTx(ctx, tx, userID, oldNorm)
		if err != nil {
			return err
		}
		// Conflict guard
		if conflict, err := selectFolderByPathTx(ctx, tx, userID, newPath); err == nil && conflict != nil {
			return ErrConflict
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return rewritePrefixTx(ctx, tx, userID, src.ID, oldNorm, newPath, src.ParentID)
	})
}

// Move relocates a folder under a new parent path. Cascades path-prefix
// updates. Refuses to move a folder into itself or one of its descendants.
func (s *Service) Move(ctx context.Context, userID uuid.UUID, srcPath, dstParentPath string) error {
	srcNorm, err := pathx.Normalize(srcPath)
	if err != nil {
		return err
	}
	if srcNorm == pathx.Root {
		return ErrRootOp
	}
	dstNorm, err := pathx.Normalize(dstParentPath)
	if err != nil {
		return err
	}
	// Cycle check (path-based; also re-checked in tx for race safety)
	if pathx.IsDescendantOrSelf(dstNorm, srcNorm) {
		return ErrCycle
	}
	name := pathx.Base(srcNorm)
	newPath, err := pathx.Join(dstNorm, name)
	if err != nil {
		return err
	}
	if newPath == srcNorm {
		return nil
	}

	return s.withTx(ctx, func(tx pgx.Tx) error {
		src, err := selectFolderByPathTx(ctx, tx, userID, srcNorm)
		if err != nil {
			return err
		}
		// Re-check cycle inside tx (paranoid).
		if pathx.IsDescendantOrSelf(dstNorm, src.Path) {
			return ErrCycle
		}
		// Conflict guard on the destination path.
		if conflict, err := selectFolderByPathTx(ctx, tx, userID, newPath); err == nil && conflict != nil {
			return ErrConflict
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}

		// Ensure destination parent exists (mkdir -p), capture its id (or nil for root).
		var newParentID *uuid.UUID
		if dstNorm != pathx.Root {
			parent, err := ensureFolderTx(ctx, tx, userID, dstNorm)
			if err != nil {
				return err
			}
			newParentID = &parent.ID
		}
		return rewritePrefixTx(ctx, tx, userID, src.ID, srcNorm, newPath, newParentID)
	})
}

// rewritePrefixTx renames `srcID` (and all of its descendant folders + files)
// from oldPrefix to newPrefix. `newParentID` becomes srcID's new parent_id.
// Must run inside a tx.
func rewritePrefixTx(ctx context.Context, tx pgx.Tx, userID, srcID uuid.UUID, oldPrefix, newPrefix string, newParentID *uuid.UUID) error {
	now := time.Now().UTC()
	// 1. update srcID itself: parent_id, path, name, updated_at
	_, err := tx.Exec(ctx,
		`UPDATE folders SET parent_id = $1, path = $2, name = $3, updated_at = $4
		 WHERE id = $5 AND user_id = $6`,
		newParentID, newPrefix, pathx.Base(newPrefix), now, srcID, userID)
	if err != nil {
		return fmt.Errorf("update src folder: %w", err)
	}
	// 2. cascade descendant folders (path LIKE oldPrefix/%)
	likePrefix := oldPrefix + "/%"
	cutLen := len(oldPrefix)
	_, err = tx.Exec(ctx,
		`UPDATE folders
		 SET path = $1 || substring(path FROM $2),
		     updated_at = $3
		 WHERE user_id = $4 AND path LIKE $5`,
		newPrefix, cutLen+1, now, userID, likePrefix)
	if err != nil {
		return fmt.Errorf("cascade folders: %w", err)
	}
	// 3. cascade files whose path is exactly oldPrefix or under it.
	//    Note: files.path stores the *parent directory* of the file, so
	//    rewriting it captures both "files directly in srcID" and
	//    "files in descendant folders".
	_, err = tx.Exec(ctx,
		`UPDATE files
		 SET path = CASE
		              WHEN path = $1 THEN $2
		              ELSE $2 || substring(path FROM $3)
		            END,
		     updated_at = $4
		 WHERE user_id = $5 AND (path = $1 OR path LIKE $6)`,
		oldPrefix, newPrefix, cutLen+1, now, userID, likePrefix)
	if err != nil {
		return fmt.Errorf("cascade files: %w", err)
	}
	return nil
}

// Delete removes a folder.
//
//   - recursive=false (default): folder must be empty (no subfolders, no files)
//     or ErrNotEmpty is returned.
//   - recursive=true: subfolders + files are deleted from the DB. S3 cleanup
//     is TODO — for now we only purge the DB rows; orphan blobs will be
//     reaped by a future garbage-collection pass.
func (s *Service) Delete(ctx context.Context, userID uuid.UUID, path string, recursive bool) error {
	norm, err := pathx.Normalize(path)
	if err != nil {
		return err
	}
	if norm == pathx.Root {
		return ErrRootOp
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		src, err := selectFolderByPathTx(ctx, tx, userID, norm)
		if err != nil {
			return err
		}
		if !recursive {
			empty, err := isEmptyTx(ctx, tx, userID, src.ID, src.Path)
			if err != nil {
				return err
			}
			if !empty {
				return ErrNotEmpty
			}
		} else {
			// Hard delete: remove all descendant files first (FKs cascade
			// from folders → files would only NULL out folder_id, so we have
			// to delete files explicitly).
			likePrefix := src.Path + "/%"
			if _, err := tx.Exec(ctx,
				`DELETE FROM files WHERE user_id = $1 AND (folder_id = $2 OR path = $3 OR path LIKE $4)`,
				userID, src.ID, src.Path, likePrefix); err != nil {
				return fmt.Errorf("recursive delete files: %w", err)
			}
			// Subfolder rows cascade via the FK ON DELETE CASCADE when we
			// drop the parent below.
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM folders WHERE id = $1 AND user_id = $2`, src.ID, userID); err != nil {
			return fmt.Errorf("delete folder: %w", err)
		}
		return nil
	})
}

func isEmptyTx(ctx context.Context, tx pgx.Tx, userID, folderID uuid.UUID, path string) (bool, error) {
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM folders WHERE user_id = $1 AND parent_id = $2`,
		userID, folderID).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM files WHERE user_id = $1 AND folder_id = $2`,
		userID, folderID).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// EnsureFolderTx is exposed so the file service can mkdir -p inside its own
// transaction (or share one) when handling uploads.
func EnsureFolderTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, path string) (*Folder, error) {
	return ensureFolderTx(ctx, tx, userID, path)
}

// ResolveOrCreate is a non-tx convenience for callers that just want a
// folder.id for an upload destination (own short tx).
func (s *Service) ResolveOrCreate(ctx context.Context, userID uuid.UUID, path string) (*Folder, error) {
	return s.Create(ctx, userID, path)
}

// --- internal helpers ---

func (s *Service) selectFolderByPath(ctx context.Context, userID uuid.UUID, path string) (*Folder, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, user_id, parent_id, path, name, created_at, updated_at
		 FROM folders WHERE user_id = $1 AND path = $2`, userID, path)
	return scanFolder(row)
}

func selectFolderByPathTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, path string) (*Folder, error) {
	row := tx.QueryRow(ctx,
		`SELECT id, user_id, parent_id, path, name, created_at, updated_at
		 FROM folders WHERE user_id = $1 AND path = $2`, userID, path)
	return scanFolder(row)
}

// scanFolder works for both pgx.Row (from QueryRow) and pgx.Rows (in Next loop)
// because both satisfy the same Scan signature.
type rowScanner interface {
	Scan(dst ...any) error
}

func scanFolder(r rowScanner) (*Folder, error) {
	var f Folder
	err := r.Scan(&f.ID, &f.UserID, &f.ParentID, &f.Path, &f.Name, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &f, nil
}

func (s *Service) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cloneUUID(u *uuid.UUID) *uuid.UUID {
	if u == nil {
		return nil
	}
	c := *u
	return &c
}

// LikePrefix is a tiny helper exposed for tests / SQL builders that want the
// canonical "<path>/%" pattern used everywhere in this package.
func LikePrefix(p string) string {
	if strings.HasSuffix(p, "/") {
		return p + "%"
	}
	return p + "/%"
}
