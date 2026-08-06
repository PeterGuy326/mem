package durablecontext

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
	"github.com/PeterGuy326/mem/server/internal/memory"
)

// TestDurableContextPostgres certifies the AC-001 service scenario against a
// real PostgreSQL: one approved principal with two sessions, a negative
// principal, active/superseded/forgotten/unapproved memories, and a second
// workspace. It is opt-in so ordinary unit tests do not require a database.
//
//	MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_integration_test?sslmode=disable \
//	  go test ./internal/durablecontext -run TestDurableContextPostgres
func TestDurableContextPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping durable context PostgreSQL test")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse MEM_TEST_DB: %v", err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatalf("refusing to modify non-test database %q; MEM_TEST_DB must end in _test",
			config.ConnConfig.Database)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	database, err := memdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	userA, workspaceA := createDurableTenant(t, ctx, database, "durable-a")
	userB, workspaceB := createDurableTenant(t, ctx, database, "durable-b")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = database.Pool.Exec(cleanupCtx,
			`DELETE FROM users WHERE id = $1 OR id = $2`, userA, userB)
	})

	memories := memory.New(database.Pool)
	service := New(database.Pool, memories)

	remember := func(workspace uuid.UUID, actor uuid.UUID, key, content string) memory.Memory {
		t.Helper()
		tokenID := uuid.New()
		result, err := memories.Remember(ctx, memory.Command{
			WorkspaceID:      workspace,
			CreatedByUserID:  &actor,
			CreatedByTokenID: &tokenID,
			Kind:             memory.KindTaskState,
			Content:          content,
			Path:             "/Tasks/" + key + "/",
			SourceType:       "agent",
			SourceRef:        "agent://durable-context/integration",
			ProducerAgent:    "employee",
			ProducerSession:  "session-1",
			IdempotencyKey:   key,
		})
		if err != nil {
			t.Fatalf("remember %s: %v", key, err)
		}
		return result.Memory
	}

	// Scenario memories in workspace A.
	activeTask := remember(workspaceA, userA, "active-task-"+uuid.NewString(),
		"resume the approved onboarding task")
	activeNote := remember(workspaceA, userA, "active-note-"+uuid.NewString(),
		"resume the approved onboarding note")
	superseded := remember(workspaceA, userA, "superseded-"+uuid.NewString(),
		"old roadmap that was archived")
	unapproved := remember(workspaceA, userA, "unapproved-"+uuid.NewString(),
		"private draft that was never granted")
	toForget := remember(workspaceA, userA, "forget-me-"+uuid.NewString(),
		"sensitive material scheduled for redaction")
	// Negative control in a second workspace.
	otherWorkspaceMemory := remember(workspaceB, userB, "other-ws-"+uuid.NewString(),
		"memory owned by the second installation")

	grant := func(workspace uuid.UUID, principal string, memoryID uuid.UUID) *Grant {
		t.Helper()
		created, err := service.Grant(ctx, GrantCommand{
			WorkspaceID: workspace,
			Principal:   principal,
			MemoryID:    memoryID,
			ActorUserID: &userA,
		})
		if err != nil {
			t.Fatalf("grant %s -> %s: %v", principal, memoryID, err)
		}
		return created
	}

	aliceTaskGrant := grant(workspaceA, "alice", activeTask.ID)
	grant(workspaceA, "alice", activeNote.ID)
	grant(workspaceA, "alice", superseded.ID)

	// Archive the superseded memory so its grant must surface as stale.
	if _, err := memories.Archive(ctx, memory.LifecycleCommand{
		WorkspaceID:     workspaceA,
		MemoryID:        superseded.ID,
		ActorUserID:     &userA,
		IdempotencyKey:  "archive-" + superseded.ID.String(),
		ExpectedVersion: superseded.StateVersion,
	}); err != nil {
		t.Fatalf("archive superseded memory: %v", err)
	}

	t.Run("approved principal resumes the same context across sessions", func(t *testing.T) {
		for _, session := range []string{"session-1", "session-2"} {
			result, err := service.Recall(ctx, RecallQuery{
				WorkspaceID: workspaceA,
				Principal:   "alice",
				SessionRef:  session,
			})
			if err != nil {
				t.Fatalf("recall session %s: %v", session, err)
			}
			if result.Contract != ContractVersion || result.Principal != "alice" {
				t.Fatalf("contract envelope = %+v", result)
			}
			if len(result.Hits) != 2 {
				t.Fatalf("expected 2 approved active hits, got %d", len(result.Hits))
			}
			seen := map[uuid.UUID]string{}
			for _, hit := range result.Hits {
				seen[hit.Memory.ID] = hit.Locator
				if hit.Locator != Locator(hit.Memory.ID, hit.StateVersion) {
					t.Fatalf("locator %q not version-pinned", hit.Locator)
				}
				if hit.Provenance.SourceRef != "agent://durable-context/integration" {
					t.Fatalf("provenance missing: %+v", hit.Provenance)
				}
			}
			wantLocator := Locator(activeTask.ID, activeTask.StateVersion)
			if seen[activeTask.ID] != wantLocator {
				t.Fatalf("task locator = %q want %q", seen[activeTask.ID], wantLocator)
			}
			if _, ok := seen[activeNote.ID]; !ok {
				t.Fatalf("approved note absent from recall: %v", seen)
			}
			// Superseded and unapproved items must not leak any locator.
			for _, denied := range []uuid.UUID{superseded.ID, unapproved.ID, toForget.ID} {
				if _, ok := seen[denied]; ok {
					t.Fatalf("denied memory %s leaked into recall", denied)
				}
			}
		}
	})

	t.Run("unapproved principal is denied explicitly", func(t *testing.T) {
		_, err := service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Principal:   "mallory",
		})
		if !errors.Is(err, ErrScopeDenied) {
			t.Fatalf("err = %v, want ErrScopeDenied", err)
		}
	})

	t.Run("second workspace cannot recall or grant across the boundary", func(t *testing.T) {
		_, err := service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceB,
			Principal:   "alice",
		})
		if !errors.Is(err, ErrScopeDenied) {
			t.Fatalf("cross-workspace recall err = %v, want ErrScopeDenied", err)
		}
		_, err = service.Grant(ctx, GrantCommand{
			WorkspaceID: workspaceB,
			Principal:   "alice",
			MemoryID:    activeTask.ID,
			ActorUserID: &userB,
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-workspace grant err = %v, want ErrNotFound", err)
		}
	})

	t.Run("get enforces approval, lifecycle, and forget", func(t *testing.T) {
		hit, err := service.Get(ctx, GetQuery{
			WorkspaceID: workspaceA,
			Principal:   "alice",
			MemoryID:    activeTask.ID,
		})
		if err != nil {
			t.Fatalf("get approved: %v", err)
		}
		if hit.Locator != Locator(activeTask.ID, activeTask.StateVersion) {
			t.Fatalf("get locator = %q", hit.Locator)
		}

		_, err = service.Get(ctx, GetQuery{
			WorkspaceID: workspaceA,
			Principal:   "alice",
			MemoryID:    unapproved.ID,
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("unapproved get err = %v, want ErrNotFound", err)
		}

		_, err = service.Get(ctx, GetQuery{
			WorkspaceID: workspaceA,
			Principal:   "mallory",
			MemoryID:    activeTask.ID,
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-principal get err = %v, want ErrNotFound (F5A.5)", err)
		}

		_, err = service.Get(ctx, GetQuery{
			WorkspaceID: workspaceA,
			Principal:   "alice",
			MemoryID:    superseded.ID,
		})
		if !errors.Is(err, ErrStale) {
			t.Fatalf("archived get err = %v, want ErrStale", err)
		}
	})

	t.Run("forgotten memories cannot be granted or resumed", func(t *testing.T) {
		_, err := service.Grant(ctx, GrantCommand{
			WorkspaceID: workspaceA,
			Principal:   "alice",
			MemoryID:    otherWorkspaceMemory.ID,
			ActorUserID: &userA,
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-workspace grant err = %v, want ErrNotFound", err)
		}
		grant(workspaceA, "alice", toForget.ID)
		_, err = memories.Forget(ctx, memory.ForgetCommand{
			LifecycleCommand: memory.LifecycleCommand{
				WorkspaceID:     workspaceA,
				MemoryID:        toForget.ID,
				ActorUserID:     &userA,
				IdempotencyKey:  "forget-" + toForget.ID.String(),
				ExpectedVersion: toForget.StateVersion,
			},
			Reason: memory.ForgetReasonSensitive,
		})
		if err != nil {
			t.Fatalf("forget: %v", err)
		}

		_, err = service.Get(ctx, GetQuery{
			WorkspaceID: workspaceA,
			Principal:   "alice",
			MemoryID:    toForget.ID,
		})
		if !errors.Is(err, ErrForgotten) {
			t.Fatalf("forgotten get err = %v, want ErrForgotten", err)
		}
		result, err := service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Principal:   "alice",
		})
		if err != nil {
			t.Fatalf("recall after forget: %v", err)
		}
		for _, hit := range result.Hits {
			if hit.Memory.ID == toForget.ID {
				t.Fatalf("forgotten memory %s still resumable", toForget.ID)
			}
		}

		// A fresh grant on a redacted payload must be refused.
		_, err = service.Grant(ctx, GrantCommand{
			WorkspaceID: workspaceA,
			Principal:   "bob",
			MemoryID:    toForget.ID,
			ActorUserID: &userA,
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("grant forgotten err = %v, want ErrNotFound", err)
		}
	})

	t.Run("revoke removes context and re-grant restores it idempotently", func(t *testing.T) {
		revoked, err := service.Revoke(ctx, RevokeCommand{
			WorkspaceID: workspaceA,
			GrantID:     aliceTaskGrant.ID,
			ActorUserID: &userA,
		})
		if err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if revoked.RevokedAt == nil {
			t.Fatalf("revoke did not stamp revoked_at")
		}
		// Idempotent replay of the same revocation.
		again, err := service.Revoke(ctx, RevokeCommand{
			WorkspaceID: workspaceA,
			GrantID:     aliceTaskGrant.ID,
			ActorUserID: &userA,
		})
		if err != nil {
			t.Fatalf("revoke replay: %v", err)
		}
		if again.ID != aliceTaskGrant.ID || again.RevokedAt == nil {
			t.Fatalf("revoke replay = %+v", again)
		}

		result, err := service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Principal:   "alice",
		})
		if err != nil {
			t.Fatalf("recall after revoke: %v", err)
		}
		for _, hit := range result.Hits {
			if hit.Memory.ID == activeTask.ID {
				t.Fatalf("revoked memory %s still resumable", activeTask.ID)
			}
		}

		regrant := grant(workspaceA, "alice", activeTask.ID)
		if regrant.ID != aliceTaskGrant.ID {
			t.Fatalf("re-grant created a duplicate row: %v != %v",
				regrant.ID, aliceTaskGrant.ID)
		}
		result, err = service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Principal:   "alice",
		})
		if err != nil {
			t.Fatalf("recall after re-grant: %v", err)
		}
		found := false
		for _, hit := range result.Hits {
			if hit.Memory.ID == activeTask.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("re-granted memory not resumable")
		}
	})

	t.Run("grants list supports audit", func(t *testing.T) {
		grants, err := service.ListGrants(ctx, ListGrantsQuery{
			WorkspaceID: workspaceA,
			Principal:   "alice",
		})
		if err != nil {
			t.Fatalf("list grants: %v", err)
		}
		if len(grants) < 3 {
			t.Fatalf("expected at least 3 alice grants, got %d", len(grants))
		}
	})

	t.Run("recall limit counts only active memories", func(t *testing.T) {
		olderActive := []memory.Memory{
			remember(workspaceA, userA, "carol-active-1-"+uuid.NewString(),
				"older active context that must stay resumable"),
			remember(workspaceA, userA, "carol-active-2-"+uuid.NewString(),
				"second older active context that must stay resumable"),
		}
		newerArchived := []memory.Memory{
			remember(workspaceA, userA, "carol-archived-1-"+uuid.NewString(),
				"newer roadmap that gets archived"),
			remember(workspaceA, userA, "carol-archived-2-"+uuid.NewString(),
				"second newer roadmap that gets archived"),
		}
		for _, item := range olderActive {
			grant(workspaceA, "carol", item.ID)
		}
		for _, item := range newerArchived {
			grant(workspaceA, "carol", item.ID)
			if _, err := memories.Archive(ctx, memory.LifecycleCommand{
				WorkspaceID:     workspaceA,
				MemoryID:        item.ID,
				ActorUserID:     &userA,
				IdempotencyKey:  "archive-" + item.ID.String(),
				ExpectedVersion: item.StateVersion,
			}); err != nil {
				t.Fatalf("archive %s: %v", item.ID, err)
			}
		}

		result, err := service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Principal:   "carol",
			Limit:       2,
		})
		if err != nil {
			t.Fatalf("recall with limit: %v", err)
		}
		if len(result.Hits) != 2 {
			t.Fatalf("expected the 2 active memories inside the limit window, got %d hits",
				len(result.Hits))
		}
		for _, hit := range result.Hits {
			if hit.Memory.LifecycleStatus != memory.StatusActive {
				t.Fatalf("recalled non-active memory %s (%s)",
					hit.Memory.ID, hit.Memory.LifecycleStatus)
			}
		}
	})

	t.Run("principal with only inactive grants gets empty hits, not denial", func(t *testing.T) {
		archivedOnly := remember(workspaceA, userA, "dave-archived-"+uuid.NewString(),
			"context that is archived before dave ever resumes")
		grant(workspaceA, "dave", archivedOnly.ID)
		if _, err := memories.Archive(ctx, memory.LifecycleCommand{
			WorkspaceID:     workspaceA,
			MemoryID:        archivedOnly.ID,
			ActorUserID:     &userA,
			IdempotencyKey:  "archive-" + archivedOnly.ID.String(),
			ExpectedVersion: archivedOnly.StateVersion,
		}); err != nil {
			t.Fatalf("archive: %v", err)
		}

		result, err := service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Principal:   "dave",
		})
		if err != nil {
			t.Fatalf("recall with inactive-only grants: %v", err)
		}
		if len(result.Hits) != 0 {
			t.Fatalf("expected empty hits, got %d", len(result.Hits))
		}
	})

	t.Run("validation rejects malformed commands", func(t *testing.T) {
		_, err := service.Recall(ctx, RecallQuery{WorkspaceID: workspaceA})
		if !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("missing principal err = %v", err)
		}
		_, err = service.Grant(ctx, GrantCommand{
			WorkspaceID: workspaceA,
			Principal:   "UPPER CASE",
			MemoryID:    activeTask.ID,
		})
		if !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("bad principal err = %v", err)
		}
		_, err = service.Grant(ctx, GrantCommand{
			WorkspaceID: workspaceA,
			Principal:   "alice",
			MemoryID:    uuid.Nil,
		})
		if !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("nil memory err = %v", err)
		}
	})
}

func createDurableTenant(
	t *testing.T,
	ctx context.Context,
	database *memdb.DB,
	prefix string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var userID uuid.UUID
	email := prefix + "-" + uuid.NewString() + "@example.test"
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'integration-test')
		RETURNING id
	`, email).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	var workspaceID uuid.UUID
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO workspaces (name, resource_owner_user_id)
		VALUES ($1, $2)
		RETURNING id
	`, prefix+"-"+uuid.NewString(), userID).Scan(&workspaceID); err != nil {
		t.Fatalf("create test workspace: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO workspace_memberships (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("create test membership: %v", err)
	}
	return userID, workspaceID
}
