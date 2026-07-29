package search

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
)

func TestManagedSearchReplayPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping managed search replay PostgreSQL test")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse MEM_TEST_DB: %v", err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatalf(
			"refusing to modify non-test database %q; MEM_TEST_DB must end in _test",
			config.ConnConfig.Database,
		)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	database, err := memdb.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	userID := uuid.New()
	fileID := uuid.New()
	evidenceID := uuid.New()
	createdAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash)
		VALUES ($1, $2, 'test-only')
	`, userID, "managed-replay-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO files (
		    id, user_id, name, path, size, sha256, mime, storage_key,
		    index_status, created_at, updated_at
		) VALUES (
		    $1, $2, 'plan.md', '/Work', 4, $3, 'text/markdown', $4,
		    'ready', $5, $5
		)
	`, fileID,
		userID,
		strings.Repeat("a", 64),
		"test/"+fileID.String(),
		createdAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO embeddings_text (
		    id, file_id, chunk_index, chunk_text, embedding, provider
		) VALUES ($1, $2, 0, 'managed replay evidence', NULL, $3)
	`,
		evidenceID,
		fileID,
		"openai:text-embedding-3-small",
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(
			cleanupCtx,
			`DELETE FROM users WHERE id = $1`,
			userID,
		); err != nil {
			t.Errorf("cleanup replay tenant: %v", err)
		}
	})

	service := New(database.Pool, nil)
	ref := entitlement.ReplayReference{
		Source:     RouteText,
		EvidenceID: evidenceID,
		FileID:     fileID,
		Score:      0.88,
	}
	base := Query{
		UserID:       userID,
		PathPrefix:   "/Work",
		AllowedPaths: []string{"/Work"},
		Type:         "doc",
		Since:        timePointer(createdAt.Add(-time.Minute)),
		Until:        timePointer(createdAt.Add(time.Minute)),
	}
	hits, err := service.Replay(ctx, base, []entitlement.ReplayReference{ref})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].EvidenceID != evidenceID.String() ||
		hits[0].Snippet != "managed replay evidence" ||
		hits[0].Score != ref.Score {
		t.Fatalf("replayed hits = %+v", hits)
	}

	tests := []struct {
		name   string
		mutate func(*Query)
	}{
		{"requested path", func(q *Query) { q.PathPrefix = "/Other" }},
		{"token path", func(q *Query) { q.AllowedPaths = []string{"/Private"} }},
		{"mime", func(q *Query) { q.Type = "image" }},
		{"since", func(q *Query) { q.Since = timePointer(createdAt.Add(time.Minute)) }},
		{"until", func(q *Query) { q.Until = timePointer(createdAt.Add(-time.Minute)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := base
			test.mutate(&query)
			if _, err := service.Replay(
				ctx,
				query,
				[]entitlement.ReplayReference{ref},
			); !errors.Is(err, ErrReplayReferenceUnavailable) {
				t.Fatalf("replay error = %v", err)
			}
		})
	}

	if _, err := database.Pool.Exec(
		ctx,
		`DELETE FROM embeddings_text WHERE id = $1`,
		evidenceID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Replay(
		ctx,
		base,
		[]entitlement.ReplayReference{ref},
	); !errors.Is(err, ErrReplayReferenceUnavailable) {
		t.Fatalf("deleted replay error = %v", err)
	}
}

func timePointer(value time.Time) *time.Time { return &value }
