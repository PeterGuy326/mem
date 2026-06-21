// Package face handles persistence + greedy clustering of face embeddings.
//
// Flow (per indexed image):
//  1. ImageProcessor returns ProcessResponse.embeddings["face"], one row per
//     detected face. Each row has the 512-d insightface embedding + bbox in
//     row.metadata_json.
//  2. indexer.persist() calls face.Service.Persist() with these rows.
//  3. For each face we look up the closest existing entity (person cluster)
//     under the same user. If cosine distance < clusterThreshold, we attach
//     to that entity. Otherwise we create a new entity (unnamed).
//  4. Insert embeddings_face (file_id, entity_id, bbox, embedding).
//
// This is intentionally O(n) per insert — fine for a personal drive up to
// thousands of faces. For larger corpora swap in pgvector HNSW + offline
// re-clustering.
package face

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// clusterThreshold is the maximum cosine distance (1 - similarity) for two
// face embeddings to belong to the same person. insightface buffalo_l is
// well-calibrated around this value.
const clusterThreshold = 0.4

// Service is the face service.
type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// New constructs a face Service.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, log: log}
}

// FaceRow is one detected face from the worker.
type FaceRow struct {
	Embedding []float32
	BBox      []float64 // [x1, y1, x2, y2]
}

// Persist writes the face rows for a file inside the given transaction.
// Caller is responsible for transaction lifecycle.
//
// Idempotency: previous embeddings_face rows for this file are wiped first.
func (s *Service) Persist(ctx context.Context, tx pgx.Tx, fileID, userID uuid.UUID, faces []FaceRow) error {
	if _, err := tx.Exec(ctx, `DELETE FROM embeddings_face WHERE file_id = $1`, fileID); err != nil {
		return fmt.Errorf("clear faces: %w", err)
	}
	for _, f := range faces {
		entityID, err := s.assignCluster(ctx, tx, userID, f.Embedding)
		if err != nil {
			return fmt.Errorf("assign cluster: %w", err)
		}
		bboxJSON, _ := json.Marshal(map[string]any{"bbox": f.BBox})
		if _, err := tx.Exec(ctx, `
			INSERT INTO embeddings_face (file_id, entity_id, bbox, embedding)
			VALUES ($1, $2, $3::jsonb, $4::vector)
		`, fileID, entityID, string(bboxJSON), vectorLiteral(f.Embedding)); err != nil {
			return fmt.Errorf("insert face: %w", err)
		}
	}
	return nil
}

// assignCluster returns an entity_id for this embedding — either the closest
// existing person cluster (if within threshold) or a freshly-created entity.
func (s *Service) assignCluster(ctx context.Context, tx pgx.Tx, userID uuid.UUID, emb []float32) (uuid.UUID, error) {
	// Find the closest existing entity by averaging its known face vectors.
	// Use the centroid embedding of each entity (computed inline).
	var bestEntity uuid.UUID
	var bestDist float64 = 2.0
	rows, err := tx.Query(ctx, `
		SELECT e.id, AVG(ef.embedding) AS centroid
		  FROM entities e
		  JOIN embeddings_face ef ON ef.entity_id = e.id
		 WHERE e.user_id = $1 AND e.type = 'person'
		 GROUP BY e.id
	`, userID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("centroids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eid uuid.UUID
		var centroid string // pgvector returns text form by default
		if err := rows.Scan(&eid, &centroid); err != nil {
			return uuid.Nil, err
		}
		c, err := parseVector(centroid)
		if err != nil || len(c) != len(emb) {
			continue
		}
		d := cosineDistance(emb, c)
		if d < bestDist {
			bestDist = d
			bestEntity = eid
		}
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, err
	}

	if bestEntity != uuid.Nil && bestDist < clusterThreshold {
		return bestEntity, nil
	}

	// New cluster.
	newID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO entities (id, user_id, type, name, metadata, created_at)
		VALUES ($1, $2, 'person', '', '{}'::jsonb, now())
	`, newID, userID); err != nil {
		return uuid.Nil, fmt.Errorf("create entity: %w", err)
	}
	return newID, nil
}

// --- CLI / API surface ---

// Cluster is one person cluster.
type Cluster struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	FaceCount   int        `json:"face_count"`
	FileCount   int        `json:"file_count"`
	CoverFileID *uuid.UUID `json:"cover_file_id,omitempty"` // a representative photo, for an avatar thumbnail
}

// List returns all person clusters for the user with sizes + a cover photo.
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Cluster, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, COALESCE(NULLIF(e.name, ''), '') AS name,
		       COUNT(ef.id) AS face_count,
		       COUNT(DISTINCT ef.file_id) AS file_count,
		       (SELECT ef2.file_id FROM embeddings_face ef2
		         WHERE ef2.entity_id = e.id ORDER BY ef2.id LIMIT 1) AS cover_file_id
		  FROM entities e
		  LEFT JOIN embeddings_face ef ON ef.entity_id = e.id
		 WHERE e.user_id = $1 AND e.type = 'person'
		 GROUP BY e.id, e.name
		 ORDER BY face_count DESC, e.created_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Cluster, 0)
	for rows.Next() {
		var c Cluster
		if err := rows.Scan(&c.ID, &c.Name, &c.FaceCount, &c.FileCount, &c.CoverFileID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// FileRef is one photo in which a person cluster appears.
type FileRef struct {
	FileID      uuid.UUID `json:"file_id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	MIME        string    `json:"mime"`
	Caption     *string   `json:"caption,omitempty"`
	IndexStatus string    `json:"index_status"`
	CreatedAt   time.Time `json:"created_at"`
}

// Files returns the photos in which this person cluster appears.
func (s *Service) Files(ctx context.Context, userID, clusterID uuid.UUID) ([]FileRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT f.id, f.name, f.path, f.mime, f.caption, f.index_status, f.created_at
		  FROM embeddings_face ef
		  JOIN files f ON f.id = ef.file_id
		 WHERE ef.entity_id = $1 AND f.user_id = $2
		 ORDER BY f.created_at DESC
	`, clusterID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]FileRef, 0)
	for rows.Next() {
		var f FileRef
		if err := rows.Scan(&f.FileID, &f.Name, &f.Path, &f.MIME, &f.Caption, &f.IndexStatus, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Name renames a cluster.
func (s *Service) Name(ctx context.Context, userID, clusterID uuid.UUID, name string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE entities SET name = $1
		 WHERE id = $2 AND user_id = $3 AND type = 'person'
	`, name, clusterID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("cluster not found")
	}
	return nil
}

// Merge folds clusterB into clusterA. All embeddings_face rows pointing to B
// are repointed to A; B itself is deleted.
func (s *Service) Merge(ctx context.Context, userID, idA, idB uuid.UUID) error {
	if idA == idB {
		return errors.New("cannot merge a cluster with itself")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Verify both clusters belong to user.
	var ownA, ownB uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT user_id FROM entities WHERE id = $1 AND type='person'`, idA).Scan(&ownA); err != nil {
		return fmt.Errorf("cluster A: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT user_id FROM entities WHERE id = $1 AND type='person'`, idB).Scan(&ownB); err != nil {
		return fmt.Errorf("cluster B: %w", err)
	}
	if ownA != userID || ownB != userID {
		return errors.New("cluster does not belong to this user")
	}

	if _, err := tx.Exec(ctx,
		`UPDATE embeddings_face SET entity_id = $1 WHERE entity_id = $2`,
		idA, idB,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entities WHERE id = $1`, idB); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- helpers ---

func vectorLiteral(vs []float32) string {
	if len(vs) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.Grow(len(vs) * 12)
	b.WriteByte('[')
	for i, v := range vs {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", v)
	}
	b.WriteByte(']')
	return b.String()
}

// parseVector turns pgvector's text form "[v1,v2,...]" into []float32.
func parseVector(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("bad vector format")
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []float32{}, nil
	}
	parts := strings.Split(inner, ",")
	out := make([]float32, 0, len(parts))
	for _, p := range parts {
		var f float32
		if _, err := fmt.Sscanf(strings.TrimSpace(p), "%g", &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// cosineDistance assumes the inputs are already L2-normalized (insightface
// returns normed_embedding). 1 - dot product = cosine distance ∈ [0, 2].
func cosineDistance(a, b []float32) float64 {
	if len(a) != len(b) {
		return 2.0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return 1.0 - dot
}
