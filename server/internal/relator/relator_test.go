package relator

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRecomputePerson is a DB-integration test. It requires a real Postgres
// (with the mem schema applied). Skips otherwise so `go test ./...` in CI
// without a DB stays green.
//
// Enable:
//
//	MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_test?sslmode=disable go test ./internal/relator/...
func TestRecomputePerson(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	svc := New(pool, nil)

	// Fresh user + three files sharing a "person" entity.
	user := mustExec1[uuid.UUID](t, ctx, pool,
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`,
		"relator-test-"+uuid.NewString()+"@example.com")

	defer func() {
		// user cascades to files → file_entities → file_relations → entities
		// (entities cascade off user, file_entities cascades off both)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user)
	}()

	fA := insertFile(t, ctx, pool, user, "A.jpg", "image/jpeg")
	fB := insertFile(t, ctx, pool, user, "B.jpg", "image/jpeg")
	fC := insertFile(t, ctx, pool, user, "C.jpg", "image/jpeg")
	fD := insertFile(t, ctx, pool, user, "D.jpg", "image/jpeg") // no entities → should be ignored

	xiaoming := insertPerson(t, ctx, pool, user, "小明")
	xiaohong := insertPerson(t, ctx, pool, user, "小红")

	linkEntity(t, ctx, pool, fA, xiaoming)
	linkEntity(t, ctx, pool, fA, xiaohong)
	linkEntity(t, ctx, pool, fB, xiaoming)
	linkEntity(t, ctx, pool, fB, xiaohong)
	linkEntity(t, ctx, pool, fC, xiaoming)
	// fD: nothing

	if err := svc.recomputePerson(ctx, fA, user, 10); err != nil {
		t.Fatalf("recomputePerson: %v", err)
	}

	hits, err := svc.Get(ctx, user, fA, TypeSamePerson, 10)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 same_person hits, got %d: %+v", len(hits), hits)
	}
	// B shares both (小明+小红), C shares only 小明. B must rank first.
	if hits[0].FileID != fB {
		t.Fatalf("expected top hit fB=%s, got %s", fB, hits[0].FileID)
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("expected fB score > fC score, got %.3f vs %.3f", hits[0].Score, hits[1].Score)
	}
	// D has no entities → must not appear.
	for _, h := range hits {
		if h.FileID == fD {
			t.Fatalf("fD (no entities) should not appear in hits")
		}
	}

	// Idempotency: running again must produce the same set (no dupes, no drift).
	if err := svc.recomputePerson(ctx, fA, user, 10); err != nil {
		t.Fatalf("recomputePerson (rerun): %v", err)
	}
	hits2, err := svc.Get(ctx, user, fA, TypeSamePerson, 10)
	if err != nil {
		t.Fatalf("Get (rerun): %v", err)
	}
	if len(hits2) != 2 {
		t.Fatalf("expected 2 hits after rerun, got %d", len(hits2))
	}
}

func mustExec1[T any](t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var out T
	if err := pool.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
	return out
}

func insertFile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, user uuid.UUID, name, mime string) uuid.UUID {
	t.Helper()
	return mustExec1[uuid.UUID](t, ctx, pool, `
		INSERT INTO files (user_id, name, size, sha256, mime, storage_key, index_status)
		VALUES ($1, $2, 0, $3, $4, $5, 'done')
		RETURNING id
	`, user, name, uuid.NewString(), mime, "test/"+uuid.NewString())
}

func insertPerson(t *testing.T, ctx context.Context, pool *pgxpool.Pool, user uuid.UUID, name string) uuid.UUID {
	t.Helper()
	return mustExec1[uuid.UUID](t, ctx, pool, `
		INSERT INTO entities (user_id, type, name) VALUES ($1, 'person', $2) RETURNING id
	`, user, name)
}

func linkEntity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, file, entity uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO file_entities (file_id, entity_id) VALUES ($1, $2)`,
		file, entity); err != nil {
		t.Fatalf("link entity: %v", err)
	}
}
