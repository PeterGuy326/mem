package aiprofile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/auth"
	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

// TestAIProfilePostgres exercises the real workspace profile table and store.
// The migration round-trip harness separately proves 16→17→16→17 lifecycle.
func TestAIProfilePostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping workspace AI profile PostgreSQL test")
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

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	database, err := memdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	authService := auth.New(database.Pool)
	user, err := authService.CreateUser(
		ctx,
		fmt.Sprintf("ai-profile-%s@example.test", uuid.NewString()),
		"secret-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(database.Pool).Resolve(ctx, user.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(
			cleanupCtx,
			`DELETE FROM users WHERE id = $1`,
			user.ID,
		); err != nil {
			t.Errorf("clean up workspace AI profile tenant: %v", err)
		}
	})

	definition, ok := Find(LocalFastV1)
	if !ok {
		t.Fatal("local profile missing")
	}
	selectedAt := time.Now().UTC().Truncate(time.Microsecond)
	selection := selectionFromDefinition(definition, ws.ID, user.ID, selectedAt)
	store := &postgresStore{pool: database.Pool}

	saved, err := store.save(ctx, selection)
	if err != nil {
		t.Fatalf("save profile: %v", err)
	}
	if err := ValidateSelection(saved); err != nil {
		t.Fatalf("saved selection is invalid: %v", err)
	}
	got, err := store.get(ctx, ws.ID)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	resolved, err := store.resolveForOwner(ctx, user.ID)
	if err != nil {
		t.Fatalf("resolve profile: %v", err)
	}
	for name, candidate := range map[string]*Selection{
		"saved":    saved,
		"get":      got,
		"resolved": resolved,
	} {
		if candidate.WorkspaceID != ws.ID ||
			candidate.ProfileID != definition.ID ||
			candidate.ProfileRevision != definition.Revision ||
			candidate.PipelineRevision != definition.PipelineRevision ||
			candidate.Embedding != definition.Embedding ||
			candidate.DataEgress != definition.DataEgress {
			t.Fatalf("%s selection = %#v", name, candidate)
		}
	}

	updated := selection
	updated.UpdatedAt = selectedAt.Add(time.Minute)
	again, err := store.save(ctx, updated)
	if err != nil {
		t.Fatalf("upsert profile: %v", err)
	}
	if !again.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("updated_at = %s, want %s", again.UpdatedAt, updated.UpdatedAt)
	}

	fileID := uuid.New()
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO files (
			id, user_id, name, path, size, sha256, mime, storage_key,
			tags, user_tags, source_metadata, processor_metadata, index_status
		)
		VALUES (
			$1,$2,'legacy-profile.txt','/',1,$3,'text/plain',$4,
			'{}','{}','{}'::jsonb,'{}'::jsonb,'done'
		)
	`,
		fileID,
		user.ID,
		strings.Repeat("f", 64),
		"ai-profile-test/"+fileID.String(),
	); err != nil {
		t.Fatalf("insert V1 corpus file: %v", err)
	}
	vector := "[" + strings.Repeat("0,", 767) + "0]"
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO embeddings_text (
			file_id, chunk_index, chunk_text, embedding, provider
		)
		VALUES ($1, 0, 'legacy corpus', $2::vector, $3)
	`, fileID, vector, definition.Embedding.Provider); err != nil {
		t.Fatalf("insert V1 corpus embedding: %v", err)
	}
	probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
	profileService := New(database.Pool, probe, LocalFastV2)
	if _, err := profileService.Select(
		ctx,
		ws.ID,
		user.ID,
		LocalFastV2,
	); !errors.Is(err, ErrProfileCorpusMismatch) {
		t.Fatalf("V1 corpus to V2 Select() error = %v, want ErrProfileCorpusMismatch", err)
	}
	if probe.calls != 0 {
		t.Fatalf("blocked V1 to V2 switch made %d Worker probes", probe.calls)
	}

	_, err = database.Pool.Exec(ctx, `
		UPDATE workspace_ai_profiles
		   SET embedding_dimensions = 0
		 WHERE workspace_id = $1
	`, ws.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("invalid dimensions error = %v, want check violation", err)
	}

	t.Run("visual-only V1 corpus blocks V2 selection", func(t *testing.T) {
		ownerID, workspaceID := newAIProfileTestWorkspace(
			t,
			ctx,
			database.Pool,
		)
		legacy, ok := Find(LocalFastV1)
		if !ok {
			t.Fatal("legacy local profile missing")
		}
		legacySelection := selectionFromDefinition(
			legacy,
			workspaceID,
			ownerID,
			time.Now().UTC(),
		)
		if _, err := (&postgresStore{pool: database.Pool}).save(
			ctx,
			legacySelection,
		); err != nil {
			t.Fatalf("save visual-only V1 profile: %v", err)
		}
		fileID := insertAIProfileTestFile(
			t,
			ctx,
			database.Pool,
			ownerID,
			"visual-only.png",
			"image/png",
		)
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO embeddings_visual (file_id, embedding)
			VALUES ($1, $2::vector)
		`, fileID, zeroVectorLiteral(visualEmbeddingDimension)); err != nil {
			t.Fatalf("insert visual-only V1 corpus: %v", err)
		}

		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
		profiles := New(database.Pool, probe, LocalFastV2)
		if _, err := profiles.Select(
			ctx,
			workspaceID,
			ownerID,
			LocalFastV2,
		); !errors.Is(err, ErrProfileCorpusMismatch) {
			t.Fatalf(
				"visual-only V1 to V2 Select() error = %v, want ErrProfileCorpusMismatch",
				err,
			)
		}
		if probe.calls != 0 {
			t.Fatalf("blocked visual-only switch made %d probes", probe.calls)
		}
	})

	t.Run("profile annotation-only V1 corpus blocks V2 selection", func(t *testing.T) {
		ownerID, workspaceID := newAIProfileTestWorkspace(
			t,
			ctx,
			database.Pool,
		)
		legacy, ok := Find(LocalFastV1)
		if !ok {
			t.Fatal("legacy local profile missing")
		}
		legacySelection := selectionFromDefinition(
			legacy,
			workspaceID,
			ownerID,
			time.Now().UTC(),
		)
		if _, err := (&postgresStore{pool: database.Pool}).save(
			ctx,
			legacySelection,
		); err != nil {
			t.Fatalf("save annotation-only V1 profile: %v", err)
		}
		fileID := insertAIProfileTestFile(
			t,
			ctx,
			database.Pool,
			ownerID,
			"annotation-only.txt",
			"text/plain",
		)
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO file_annotations (
			    file_id,
			    stable_key,
			    kind,
			    value_text,
			    confidence,
			    source,
			    provider,
			    processor,
			    analysis_version,
			    status
			) VALUES (
			    $1,
			    'description:profile-test',
			    'description',
			    'profile-derived annotation',
			    0.9,
			    'model',
			    '',
			    'text',
			    $2,
			    'pending'
			)
		`, fileID, legacy.PipelineRevision); err != nil {
			t.Fatalf("insert annotation-only V1 corpus: %v", err)
		}

		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
		profiles := New(database.Pool, probe, LocalFastV2)
		if _, err := profiles.Select(
			ctx,
			workspaceID,
			ownerID,
			LocalFastV2,
		); !errors.Is(err, ErrProfileCorpusMismatch) {
			t.Fatalf(
				"annotation-only V1 to V2 Select() error = %v, want ErrProfileCorpusMismatch",
				err,
			)
		}
		if probe.calls != 0 {
			t.Fatalf("blocked annotation-only switch made %d probes", probe.calls)
		}
	})

	t.Run("empty V1 workspace may select V2", func(t *testing.T) {
		ownerID, workspaceID := newAIProfileTestWorkspace(
			t,
			ctx,
			database.Pool,
		)
		legacy, ok := Find(LocalFastV1)
		if !ok {
			t.Fatal("legacy local profile missing")
		}
		legacySelection := selectionFromDefinition(
			legacy,
			workspaceID,
			ownerID,
			time.Now().UTC(),
		)
		if _, err := (&postgresStore{pool: database.Pool}).save(
			ctx,
			legacySelection,
		); err != nil {
			t.Fatalf("save empty V1 profile: %v", err)
		}

		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
		profiles := New(database.Pool, probe, LocalFastV2)
		selected, err := profiles.Select(
			ctx,
			workspaceID,
			ownerID,
			LocalFastV2,
		)
		if err != nil {
			t.Fatalf("empty V1 to V2 Select(): %v", err)
		}
		if selected.ProfileID != LocalFastV2 || probe.calls != 1 {
			t.Fatalf(
				"empty selection profile/probes = %s/%d, want %s/1",
				selected.ProfileID,
				probe.calls,
				LocalFastV2,
			)
		}
	})
}

func newAIProfileTestWorkspace(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	user, err := auth.New(pool).CreateUser(
		ctx,
		fmt.Sprintf("ai-profile-corpus-%s@example.test", uuid.NewString()),
		"secret-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(pool).Resolve(ctx, user.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
			t.Errorf("clean up AI profile corpus tenant: %v", err)
		}
	})
	return user.ID, ws.ID
}

func insertAIProfileTestFile(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	ownerID uuid.UUID,
	name, mimeType string,
) uuid.UUID {
	t.Helper()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO files (
		    id, user_id, name, path, size, sha256, mime, storage_key,
		    index_status
		) VALUES ($1,$2,$3,'/',1,$4,$5,$6,'done')
	`,
		fileID,
		ownerID,
		name,
		strings.Repeat("a", 64),
		mimeType,
		"ai-profile-corpus/"+fileID.String(),
	); err != nil {
		t.Fatalf("insert AI profile corpus file: %v", err)
	}
	return fileID
}

func zeroVectorLiteral(dimensions int) string {
	if dimensions <= 0 {
		return "[]"
	}
	return "[" + strings.Repeat("0,", dimensions-1) + "0]"
}
