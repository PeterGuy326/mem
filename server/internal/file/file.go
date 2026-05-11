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
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/storage"
)

// File mirrors the `files` table.
type File struct {
	ID          uuid.UUID  `json:"id"`
	UserID      uuid.UUID  `json:"user_id"`
	Name        string     `json:"name"`
	Path        string     `json:"path"`
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
type ListFilter struct {
	Tag   string
	Type  string // mime prefix, e.g. "image" -> "image/%"
	Since *time.Time
	Until *time.Time
	Limit int
	Page  int
}

// Service is the file service.
type Service struct {
	pool  *pgxpool.Pool
	store *storage.Store
}

// New constructs a file Service.
func New(pool *pgxpool.Pool, store *storage.Store) *Service {
	return &Service{pool: pool, store: store}
}

// ErrNotFound is returned when a file id is unknown to the user.
var ErrNotFound = errors.New("file not found")

// Put streams the request body, computes SHA-256 on the fly, dedupes by
// (user_id, sha256), and otherwise uploads to S3 + writes a DB row.
//
// `name` and `declaredMIME` come from the client; `size` may be -1 if unknown.
func (s *Service) Put(ctx context.Context, userID uuid.UUID, name, declaredMIME string, size int64, tags []string, body io.Reader) (*PutResult, error) {
	if name == "" {
		return nil, errors.New("name is required")
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
		mime = "application/octet-stream"
	}
	if err := s.store.Put(ctx, key, tmp, written, mime); err != nil {
		return nil, fmt.Errorf("s3 put: %w", err)
	}

	now := time.Now().UTC()
	if tags == nil {
		tags = []string{}
	}
	f := &File{
		ID:          id,
		UserID:      userID,
		Name:        name,
		Path:        "/",
		Size:        written,
		SHA256:      sum,
		MIME:        mime,
		StorageKey:  key,
		Tags:        tags,
		IndexStatus: "pending",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO files
		 (id, user_id, name, path, size, sha256, mime, storage_key, tags, index_status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		f.ID, f.UserID, f.Name, f.Path, f.Size, f.SHA256, f.MIME, f.StorageKey,
		f.Tags, f.IndexStatus, f.CreatedAt, f.UpdatedAt,
	)
	if err != nil {
		// best-effort cleanup of orphaned object
		_ = s.store.Delete(context.Background(), key)
		return nil, fmt.Errorf("insert file: %w", err)
	}
	return &PutResult{File: f, Deduped: false}, nil
}

// Get returns a file row scoped to userID.
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*File, error) {
	f, err := s.scanOne(ctx,
		`SELECT id, user_id, name, path, size, sha256, mime, storage_key,
		        summary, caption, tags, timeline_at, index_status, created_at, updated_at
		 FROM files WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return nil, err
	}
	return f, nil
}

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
	args := []any{userID}
	q := strings.Builder{}
	q.WriteString(`SELECT id, user_id, name, path, size, sha256, mime, storage_key,
	        summary, caption, tags, timeline_at, index_status, created_at, updated_at
	 FROM files WHERE user_id = $1`)
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
		var fr File
		if err := rows.Scan(&fr.ID, &fr.UserID, &fr.Name, &fr.Path, &fr.Size, &fr.SHA256, &fr.MIME, &fr.StorageKey,
			&fr.Summary, &fr.Caption, &fr.Tags, &fr.TimelineAt, &fr.IndexStatus, &fr.CreatedAt, &fr.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, fr)
	}
	return out, rows.Err()
}

func (s *Service) findBySHA(ctx context.Context, userID uuid.UUID, sum string) (*File, error) {
	f, err := s.scanOne(ctx,
		`SELECT id, user_id, name, path, size, sha256, mime, storage_key,
		        summary, caption, tags, timeline_at, index_status, created_at, updated_at
		 FROM files WHERE user_id = $1 AND sha256 = $2`, userID, sum)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

func (s *Service) scanOne(ctx context.Context, q string, args ...any) (*File, error) {
	var f File
	err := s.pool.QueryRow(ctx, q, args...).Scan(
		&f.ID, &f.UserID, &f.Name, &f.Path, &f.Size, &f.SHA256, &f.MIME, &f.StorageKey,
		&f.Summary, &f.Caption, &f.Tags, &f.TimelineAt, &f.IndexStatus, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &f, nil
}

func storageKey(userID, fileID uuid.UUID, name string) string {
	clean := path.Base(name)
	if clean == "" || clean == "." || clean == "/" {
		clean = fileID.String()
	}
	return fmt.Sprintf("users/%s/%s/%s", userID.String(), fileID.String(), clean)
}
