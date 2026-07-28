package folder

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/PeterGuy326/mem/server/internal/memory"
	"github.com/PeterGuy326/mem/server/internal/testutil/pglockwait"
	"github.com/PeterGuy326/mem/server/internal/workspacelock"
)

// TestWorkspacePathLockingIntegration exercises schedules that previously
// allowed folder guards and prefix rewrites to race workspace-scoped writers.
//
//	MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_test?sslmode=disable \
//	  go test ./internal/folder -run TestWorkspacePathLockingIntegration
func TestWorkspacePathLockingIntegration(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping DB integration test")
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

	t.Run("content lock blocks folder rewrite", func(t *testing.T) {
		userID, _ := createWorkspaceLockTenant(t, ctx, database.Pool, "content-blocks-folder")
		service := New(database.Pool)
		if _, err := service.Create(ctx, userID, "/Shared/Child"); err != nil {
			t.Fatalf("create source: %v", err)
		}
		renameBackend := pglockwait.NewBackend(t, ctx, dsn, "folder-rewrite")
		renameService := New(renameBackend.Pool)

		writerTx, err := database.Pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("begin writer transaction: %v", err)
		}
		defer func() { _ = writerTx.Rollback(ctx) }()
		if _, err := workspacelock.ForContentWriteByOwner(ctx, writerTx, userID); err != nil {
			t.Fatalf("take content lock: %v", err)
		}
		writerPID := pglockwait.BackendPID(t, ctx, writerTx)

		renameDone := make(chan error, 1)
		go func() {
			renameDone <- renameService.Rename(ctx, userID, "/Shared", "Renamed")
		}()
		pglockwait.WaitBlocked(
			t,
			ctx,
			database.Pool,
			renameBackend,
			writerPID,
			"FROM workspaces",
			"FOR UPDATE",
		)

		if err := writerTx.Commit(ctx); err != nil {
			t.Fatalf("commit writer transaction: %v", err)
		}
		if err := awaitWorkspaceOperation(t, renameDone); err != nil {
			t.Fatalf("rename after writer commit: %v", err)
		}
		if _, err := service.Get(ctx, userID, "/Renamed/Child"); err != nil {
			t.Fatalf("renamed descendant missing: %v", err)
		}
	})

	t.Run("folder lock blocks every path content writer", func(t *testing.T) {
		userID, workspaceID := createWorkspaceLockTenant(
			t,
			ctx,
			database.Pool,
			"folder-blocks-writers",
		)
		createBackend := pglockwait.NewBackend(t, ctx, dsn, "folder-create")
		memoryBackend := pglockwait.NewBackend(t, ctx, dsn, "memory-remember")
		checkpointBackend := pglockwait.NewBackend(t, ctx, dsn, "handoff-checkpoint")
		folderService := New(createBackend.Pool)
		memoryService := memory.New(memoryBackend.Pool)
		handoffService := handoff.New(checkpointBackend.Pool)

		mutationTx, err := database.Pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("begin folder transaction: %v", err)
		}
		defer func() { _ = mutationTx.Rollback(ctx) }()
		if _, err := workspacelock.ForPathMutation(ctx, mutationTx, userID); err != nil {
			t.Fatalf("take folder lock: %v", err)
		}
		mutationPID := pglockwait.BackendPID(t, ctx, mutationTx)

		createDone := make(chan error, 1)
		go func() {
			_, err := folderService.Create(ctx, userID, "/Locked/Create")
			createDone <- err
		}()
		memoryDone := make(chan error, 1)
		go func() {
			_, err := memoryService.Remember(ctx, memory.Command{
				WorkspaceID:    workspaceID,
				Kind:           memory.KindDecision,
				Content:        "serialize this write with folder path mutations",
				Path:           "/Locked/Memory",
				SourceType:     "test",
				IdempotencyKey: "workspace-lock-memory-" + uuid.NewString(),
			})
			memoryDone <- err
		}()
		checkpointDone := make(chan error, 1)
		go func() {
			_, err := handoffService.Checkpoint(
				ctx,
				workspaceLockCheckpointCommand(workspaceID, userID),
			)
			checkpointDone <- err
		}()

		for _, waiter := range []pglockwait.Backend{
			createBackend,
			memoryBackend,
			checkpointBackend,
		} {
			pglockwait.WaitBlocked(
				t,
				ctx,
				database.Pool,
				waiter,
				mutationPID,
				"FROM workspaces",
				"FOR KEY SHARE",
			)
		}

		if err := mutationTx.Commit(ctx); err != nil {
			t.Fatalf("commit folder transaction: %v", err)
		}
		for name, done := range map[string]<-chan error{
			"folder create": createDone,
			"memory":        memoryDone,
			"checkpoint":    checkpointDone,
		} {
			if err := awaitWorkspaceOperation(t, done); err != nil {
				t.Fatalf("%s after folder commit: %v", name, err)
			}
		}
	})

	t.Run("concurrent renames cannot split a subtree", func(t *testing.T) {
		userID, _ := createWorkspaceLockTenant(t, ctx, database.Pool, "rename-race")
		service := New(database.Pool)
		child, err := service.Create(ctx, userID, "/Race/Child")
		if err != nil {
			t.Fatalf("create source subtree: %v", err)
		}
		fileID := uuid.New()
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO files (
				id, user_id, name, path, folder_id, size, sha256, mime,
				storage_key, tags, index_status
			)
			VALUES ($1, $2, 'race.txt', '/Race/Child', $3, 1, $4,
			        'text/plain', $5, '{}', 'pending')
		`,
			fileID,
			userID,
			child.ID,
			strings.Repeat("a", 64),
			"workspace-lock/"+fileID.String(),
		); err != nil {
			t.Fatalf("insert descendant file: %v", err)
		}
		renameBBackend := pglockwait.NewBackend(t, ctx, dsn, "folder-rename-b")
		renameCBackend := pglockwait.NewBackend(t, ctx, dsn, "folder-rename-c")
		type renameAttempt struct {
			name    string
			backend pglockwait.Backend
			service *Service
		}
		attempts := []renameAttempt{
			{name: "RaceB", backend: renameBBackend, service: New(renameBBackend.Pool)},
			{name: "RaceC", backend: renameCBackend, service: New(renameCBackend.Pool)},
		}

		gateTx, err := database.Pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("begin rename gate: %v", err)
		}
		defer func() { _ = gateTx.Rollback(ctx) }()
		if _, err := workspacelock.ForContentWriteByOwner(ctx, gateTx, userID); err != nil {
			t.Fatalf("take rename gate: %v", err)
		}
		gatePID := pglockwait.BackendPID(t, ctx, gateTx)

		results := make(chan error, 2)
		for _, attempt := range attempts {
			attempt := attempt
			go func() {
				results <- attempt.service.Rename(ctx, userID, "/Race", attempt.name)
			}()
		}
		for _, attempt := range attempts {
			pglockwait.WaitBlocked(
				t,
				ctx,
				database.Pool,
				attempt.backend,
				gatePID,
				"FROM workspaces",
				"FOR UPDATE",
			)
		}
		if err := gateTx.Commit(ctx); err != nil {
			t.Fatalf("release rename gate: %v", err)
		}

		var (
			successes int
			notFound  int
		)
		for range 2 {
			switch err := awaitWorkspaceOperation(t, results); {
			case err == nil:
				successes++
			case errors.Is(err, ErrNotFound):
				notFound++
			default:
				t.Fatalf("concurrent rename error: %v", err)
			}
		}
		if successes != 1 || notFound != 1 {
			t.Fatalf("rename outcomes: successes=%d not-found=%d", successes, notFound)
		}

		var winner string
		if _, err := service.Get(ctx, userID, "/RaceB"); err == nil {
			winner = "/RaceB"
		} else if _, err := service.Get(ctx, userID, "/RaceC"); err == nil {
			winner = "/RaceC"
		} else {
			t.Fatal("neither concurrent rename committed")
		}
		if _, err := service.Get(ctx, userID, winner+"/Child"); err != nil {
			t.Fatalf("subtree split from winner %s: %v", winner, err)
		}
		var (
			filePath   string
			folderPath string
		)
		if err := database.Pool.QueryRow(ctx, `
			SELECT f.path, d.path
			  FROM files f
			  JOIN folders d ON d.id = f.folder_id
			 WHERE f.id = $1
		`, fileID).Scan(&filePath, &folderPath); err != nil {
			t.Fatalf("load descendant file: %v", err)
		}
		if filePath != winner+"/Child" || folderPath != filePath {
			t.Fatalf(
				"split subtree: file.path=%q folder.path=%q winner=%q",
				filePath,
				folderPath,
				winner,
			)
		}
	})
}

func createWorkspaceLockTenant(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	prefix string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'workspace-lock-test')
		RETURNING id
	`, prefix+"-"+uuid.NewString()+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create workspace-lock user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	var workspaceID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspaces (name, resource_owner_user_id)
		VALUES ($1, $2)
		RETURNING id
	`, prefix, userID).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace-lock workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_memberships (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("create workspace-lock membership: %v", err)
	}
	return userID, workspaceID
}

func workspaceLockCheckpointCommand(
	workspaceID uuid.UUID,
	userID uuid.UUID,
) handoff.CheckpointCommand {
	taskKey := "workspace-lock-" + uuid.NewString()
	return handoff.CheckpointCommand{
		WorkspaceID:     workspaceID,
		CreatedByUserID: &userID,
		TaskKey:         taskKey,
		IdempotencyKey:  "workspace-lock-checkpoint-" + uuid.NewString(),
		Handoff: handoff.HandoffV1{
			Contract:       handoff.ContractName,
			SchemaVersion:  handoff.SchemaVersionV1,
			CheckpointKind: handoff.CheckpointKindCheckpoint,
			TaskKey:        taskKey,
			ScopePath:      "/Locked/Task",
			State: handoff.StateV1{
				Status:        handoff.TaskStatusInProgress,
				Goal:          "prove checkpoint locking",
				Progress:      handoff.ProgressV1{Summary: "waiting", Completed: []string{}},
				Decisions:     []handoff.DecisionV1{},
				NextSteps:     []handoff.NextStepV1{},
				Blockers:      []handoff.BlockerV1{},
				OpenQuestions: []string{},
				Artifacts:     []handoff.ArtifactV1{},
			},
			Producer: handoff.ProducerV1{AgentID: "workspace-lock-test"},
		},
	}
}

func awaitWorkspaceOperation(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for workspace operation")
		return nil
	}
}
