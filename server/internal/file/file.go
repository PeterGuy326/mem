// Package file is the File service: ingestion, metadata, retrieval.
//
// W1 scope: SHA-256 content addressing + dedup ("秒传"), S3 PutObject, DB row
// insertion in `index_status='pending'`. AI fields are populated by the worker
// later (W2+).
package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	stdmime "mime"
	gopath "path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/folder"
	"github.com/PeterGuy326/mem/server/internal/pathx"
	"github.com/PeterGuy326/mem/server/internal/storage"
	"github.com/PeterGuy326/mem/server/internal/workspacelock"
)

// File mirrors the `files` table.
type File struct {
	ID          uuid.UUID  `json:"id"`
	UserID      uuid.UUID  `json:"user_id"`
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	FolderID    *uuid.UUID `json:"folder_id,omitempty"`
	Size        int64      `json:"size"`
	SHA256      string     `json:"sha256"`
	MIME        string     `json:"mime"`
	StorageKey  string     `json:"storage_key"`
	Summary     *string    `json:"summary,omitempty"`
	Caption     *string    `json:"caption,omitempty"`
	Tags        []string   `json:"tags"`
	TimelineAt  *time.Time `json:"timeline_at,omitempty"`
	IndexStatus string     `json:"index_status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// PutResult is returned from Put — `Deduped` is true when an existing file with
// the same (user_id, sha256) was returned (秒传).
type PutResult struct {
	File    *File
	Deduped bool
}

// ListFilter narrows GET /v1/files.
//
// Path-related filters are mutually exclusive — pass at most one of:
//
//	Path:   exact folder match (folder_id resolved from this)
//	Prefix: subtree match (plus exact equal)
type ListFilter struct {
	Tag          string
	Type         string // mime prefix, e.g. "image" -> "image/%"
	Path         string // exact folder absolute path; "" = no filter
	Prefix       string // subtree absolute path; "" = no filter
	AllowedPaths []string
	Since        *time.Time
	Until        *time.Time
	Limit        int
	Page         int
}

// Service is the file service.
type Service struct {
	pool    *pgxpool.Pool
	store   objectStore
	folders *folder.Service
}

type objectStore interface {
	Put(context.Context, string, io.Reader, int64, string) error
	Get(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

type txCommitter interface {
	Commit(context.Context) error
}

type uploadedObject struct {
	store         objectStore
	key           string
	deleteOnError bool
}

func (object *uploadedObject) cleanup() {
	if object == nil || !object.deleteOnError {
		return
	}
	_ = object.store.Delete(context.Background(), object.key)
}

// commitFilePut disarms object cleanup before asking PostgreSQL to commit. A
// commit acknowledgement can be lost after the transaction became durable;
// deleting the object on that error would leave a committed file row pointing
// at missing content. A genuinely rolled-back commit may leave an orphan, which
// is safe for retry and belongs to object garbage collection.
func commitFilePut(ctx context.Context, tx txCommitter, object *uploadedObject) error {
	object.deleteOnError = false
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit file put: %w", err)
	}
	return nil
}

// New constructs a file Service.
func New(pool *pgxpool.Pool, store objectStore, folders *folder.Service) *Service {
	return &Service{pool: pool, store: store, folders: folders}
}

var _ objectStore = (*storage.Store)(nil)

// Pool exposes the underlying connection pool for sibling packages that need
// to run their own queries against the files table (timeline, related, …).
// Kept narrow to one accessor so callers don't grow ad-hoc dependencies.
func (s *Service) Pool() *pgxpool.Pool { return s.pool }

// ErrNotFound is returned when a file id is unknown to the user.
var ErrNotFound = errors.New("file not found")

// Put streams the request body, computes SHA-256 on the fly, dedupes by
// (user_id, sha256), and otherwise uploads to S3 + writes a DB row.
//
// `name` and `declaredMIME` come from the client; `size` may be -1 if unknown.
// `targetPath` is the destination folder absolute path (e.g. "/Photos/2012");
// pass "/" or "" for root. Missing parent folders are auto-created
// (`mkdir -p`).
func (s *Service) Put(ctx context.Context, userID uuid.UUID, name, declaredMIME, targetPath string, size int64, tags []string, body io.Reader) (*PutResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if err := pathx.ValidateName(name); err != nil {
		return nil, err
	}
	destPath, err := pathx.Normalize(targetPath)
	if err != nil {
		return nil, fmt.Errorf("target path: %w", err)
	}

	// Buffer to temp file? For W1 we keep it simple: hash-then-upload by
	// teeing into an in-memory buffer for small payloads, or a spill file
	// for larger ones. To keep dependencies minimal we use a spill file
	// under os.TempDir; production should swap for a streaming hash + S3
	// multipart writer.
	tmp, err := newSpillBuffer()
	if err != nil {
		return nil, err
	}
	defer tmp.Close()

	hasher := sha256.New()
	written, err := io.Copy(tmp, io.TeeReader(body, hasher))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if size >= 0 && size != written {
		return nil, fmt.Errorf("size mismatch: declared=%d got=%d", size, written)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))

	// 秒传：if (user_id, sha256) exists, return the existing row.
	existing, err := s.findBySHA(ctx, userID, sum)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &PutResult{File: existing, Deduped: true}, nil
	}

	id := uuid.New()
	key := storageKey(userID, id, name)

	if err := tmp.Rewind(); err != nil {
		return nil, fmt.Errorf("rewind spill: %w", err)
	}
	mime := declaredMIME
	if mime == "" {
		// Try filename extension first (covers .md/.txt/.json/.pdf/...).
		// stdlib mime.TypeByExtension doesn't ship a .md mapping by default
		// — without this, markdown files arrive as application/octet-stream
		// and the worker's TextProcessor skips them entirely.
		mime = inferMIMEByExtension(name)
		if mime == "" {
			mime = "application/octet-stream"
		}
	}
	if err := s.store.Put(ctx, key, tmp, written, mime); err != nil {
		return nil, fmt.Errorf("s3 put: %w", err)
	}
	object := &uploadedObject{store: s.store, key: key, deleteOnError: true}
	defer object.cleanup()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin file put transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The workspace lock must be the first database action in this
	// transaction. Keep it through mkdir -p and file insertion so a concurrent
	// folder rewrite cannot split files.path from files.folder_id.
	if _, err := workspacelock.ForContentWriteByOwner(ctx, tx, userID); err != nil {
		return nil, err
	}
	var folderID *uuid.UUID
	if destPath != pathx.Root {
		folderRecord, err := s.folders.ResolveOrCreateLockedTx(ctx, tx, userID, destPath)
		if err != nil {
			return nil, fmt.Errorf("mkdir -p: %w", err)
		}
		if folderRecord != nil {
			folderID = &folderRecord.ID
		}
	}

	now := time.Now().UTC()
	if tags == nil {
		tags = []string{}
	}
	f := &File{
		ID:          id,
		UserID:      userID,
		Name:        name,
		Path:        destPath,
		FolderID:    folderID,
		Size:        written,
		SHA256:      sum,
		MIME:        mime,
		StorageKey:  key,
		Tags:        tags,
		IndexStatus: "pending",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO files
		 (id, user_id, name, path, folder_id, size, sha256, mime, storage_key, tags, index_status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		f.ID, f.UserID, f.Name, f.Path, f.FolderID, f.Size, f.SHA256, f.MIME, f.StorageKey,
		f.Tags, f.IndexStatus, f.CreatedAt, f.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert file: %w", err)
	}
	if err := commitFilePut(ctx, tx, object); err != nil {
		return nil, err
	}
	return &PutResult{File: f, Deduped: false}, nil
}

// Get returns a file row scoped to userID.
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*File, error) {
	f, err := s.scanOne(ctx, selectFileSQL+` WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// selectFileSQL is the canonical column projection used by every read path.
const selectFileSQL = `SELECT id, user_id, name, path, folder_id, size, sha256, mime, storage_key,
		        summary, caption, tags, timeline_at, index_status, created_at, updated_at
		 FROM files`

// Content returns a reader for the file's bytes.
func (s *Service) Content(ctx context.Context, userID, id uuid.UUID) (*File, io.ReadCloser, error) {
	f, err := s.Get(ctx, userID, id)
	if err != nil {
		return nil, nil, err
	}
	rc, err := s.store.Get(ctx, f.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return f, rc, nil
}

// List returns paginated files matching the filter.
func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) ([]File, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Path != "" && f.Prefix != "" {
		return nil, errors.New("path and prefix filters are mutually exclusive")
	}
	args := []any{userID}
	q := strings.Builder{}
	q.WriteString(selectFileSQL)
	q.WriteString(` WHERE user_id = $1`)
	idx := 2
	if f.Tag != "" {
		q.WriteString(fmt.Sprintf(" AND $%d = ANY(tags)", idx))
		args = append(args, f.Tag)
		idx++
	}
	if f.Type != "" {
		q.WriteString(fmt.Sprintf(" AND mime LIKE $%d", idx))
		args = append(args, f.Type+"/%")
		idx++
	}
	if f.Path != "" {
		norm, err := pathx.Normalize(f.Path)
		if err != nil {
			return nil, fmt.Errorf("path filter: %w", err)
		}
		q.WriteString(fmt.Sprintf(" AND path = $%d", idx))
		args = append(args, norm)
		idx++
	}
	if f.Prefix != "" {
		norm, err := pathx.Normalize(f.Prefix)
		if err != nil {
			return nil, fmt.Errorf("prefix filter: %w", err)
		}
		if norm == pathx.Root {
			// no constraint — entire user subtree is the prefix
		} else {
			q.WriteString(fmt.Sprintf(
				" AND (path = $%d OR left(path, length($%d) + 1) = $%d || '/')",
				idx, idx, idx,
			))
			args = append(args, norm)
			idx++
		}
	}
	if len(f.AllowedPaths) > 0 {
		normalized := make([]string, 0, len(f.AllowedPaths))
		unrestricted := false
		for _, raw := range f.AllowedPaths {
			if raw == "" {
				return nil, errors.New("allowed path is empty")
			}
			norm, err := pathx.Normalize(raw)
			if err != nil {
				return nil, fmt.Errorf("allowed path: %w", err)
			}
			if norm == pathx.Root {
				unrestricted = true
				break
			}
			normalized = append(normalized, norm)
		}
		clauses := make([]string, 0, len(normalized))
		if !unrestricted {
			for _, norm := range normalized {
				clauses = append(clauses, fmt.Sprintf(
					"(path = $%d OR left(path, length($%d) + 1) = $%d || '/')",
					idx, idx, idx,
				))
				args = append(args, norm)
				idx++
			}
		}
		if len(clauses) > 0 {
			q.WriteString(" AND (" + strings.Join(clauses, " OR ") + ")")
		}
	}
	if f.Since != nil {
		q.WriteString(fmt.Sprintf(" AND COALESCE(timeline_at, created_at) >= $%d", idx))
		args = append(args, *f.Since)
		idx++
	}
	if f.Until != nil {
		q.WriteString(fmt.Sprintf(" AND COALESCE(timeline_at, created_at) <= $%d", idx))
		args = append(args, *f.Until)
		idx++
	}
	q.WriteString(" ORDER BY created_at DESC")
	q.WriteString(fmt.Sprintf(" LIMIT $%d OFFSET $%d", idx, idx+1))
	args = append(args, f.Limit, (f.Page-1)*f.Limit)

	rows, err := s.pool.Query(ctx, q.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query files: %w", err)
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		fr, err := scanFileRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *fr)
	}
	return out, rows.Err()
}

func (s *Service) findBySHA(ctx context.Context, userID uuid.UUID, sum string) (*File, error) {
	f, err := s.scanOne(ctx, selectFileSQL+` WHERE user_id = $1 AND sha256 = $2`, userID, sum)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

// scanRow is satisfied by both *pgx.Row and pgx.Rows.
type scanRow interface {
	Scan(dst ...any) error
}

func scanFileRow(r scanRow) (*File, error) {
	var f File
	if err := r.Scan(
		&f.ID, &f.UserID, &f.Name, &f.Path, &f.FolderID, &f.Size, &f.SHA256, &f.MIME, &f.StorageKey,
		&f.Summary, &f.Caption, &f.Tags, &f.TimelineAt, &f.IndexStatus, &f.CreatedAt, &f.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *Service) scanOne(ctx context.Context, q string, args ...any) (*File, error) {
	f, err := scanFileRow(s.pool.QueryRow(ctx, q, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

// Move relocates a file to a different folder (its parent directory changes,
// the basename stays the same). mkdir -p is applied to newPath.
func (s *Service) Move(ctx context.Context, userID, fileID uuid.UUID, newPath string) (*File, error) {
	dest, err := pathx.Normalize(newPath)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := workspacelock.ForContentWriteByOwner(ctx, tx, userID); err != nil {
		return nil, err
	}
	var folderID *uuid.UUID
	if dest != pathx.Root {
		folderRecord, err := s.folders.ResolveOrCreateLockedTx(ctx, tx, userID, dest)
		if err != nil {
			return nil, fmt.Errorf("mkdir -p: %w", err)
		}
		if folderRecord != nil {
			folderID = &folderRecord.ID
		}
	}

	now := time.Now().UTC()
	tag, err := tx.Exec(ctx,
		`UPDATE files SET path = $1, folder_id = $2, updated_at = $3
		 WHERE id = $4 AND user_id = $5`,
		dest, folderID, now, fileID, userID)
	if err != nil {
		return nil, fmt.Errorf("move file: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, fileID)
}

// Rename changes the basename of a file (its parent directory is unchanged).
func (s *Service) Rename(ctx context.Context, userID, fileID uuid.UUID, newName string) (*File, error) {
	if err := pathx.ValidateName(newName); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE files SET name = $1, updated_at = $2 WHERE id = $3 AND user_id = $4`,
		newName, now, fileID, userID)
	if err != nil {
		return nil, fmt.Errorf("rename file: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, userID, fileID)
}

// Delete removes a file: its DB row (which cascades to embeddings + faces via
// FK ON DELETE) and its blob in object storage. Blob removal is best-effort —
// an orphaned object is harmless and shouldn't fail the delete.
func (s *Service) Delete(ctx context.Context, userID, fileID uuid.UUID) error {
	f, err := s.Get(ctx, userID, fileID) // verifies ownership + gets storage_key
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM files WHERE id = $1 AND user_id = $2`, fileID, userID)
	if err != nil {
		return fmt.Errorf("delete file: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if f.StorageKey != "" {
		if derr := s.store.Delete(ctx, f.StorageKey); derr != nil {
			// Row is already gone; a leftover blob is acceptable.
			_ = derr
		}
	}
	return nil
}

func storageKey(userID, fileID uuid.UUID, name string) string {
	clean := gopath.Base(name)
	if clean == "" || clean == "." || clean == "/" {
		clean = fileID.String()
	}
	return fmt.Sprintf("users/%s/%s/%s", userID.String(), fileID.String(), clean)
}

// inferMIMEByExtension picks a content-type from a filename. Tries the
// stdlib registry first (covers .json/.html/.png/...), then a small table
// for ones Go doesn't ship a mapping for. .md is the canonical gotcha —
// without this, markdown uploads default to application/octet-stream and
// the worker's TextProcessor skips them entirely.
func inferMIMEByExtension(name string) string {
	ext := strings.ToLower(gopath.Ext(name))
	if ext == "" {
		return ""
	}
	if m := stdmime.TypeByExtension(ext); m != "" {
		return strings.SplitN(m, ";", 2)[0]
	}
	switch ext {
	case ".md", ".markdown":
		return "text/markdown"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".toml":
		return "application/x-toml"
	case ".rst":
		return "text/x-rst"
	case ".log", ".txt":
		return "text/plain"
	}
	return ""
}
