package handoff

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	memdb "github.com/PeterGuy326/mem/server/internal/db"
)

// TestHandoffPostgres exercises transaction, unique-index, row-lock, and
// migration semantics that a SQL mock cannot represent faithfully.
//
//	MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_test?sslmode=disable \
//	  go test ./internal/handoff -run TestHandoffPostgres
func TestHandoffPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping PostgreSQL integration test")
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
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	userA, workspaceA := createHandoffTenant(t, ctx, database, "handoff-a")
	userB, workspaceB := createHandoffTenant(t, ctx, database, "handoff-b")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(cleanupCtx,
			`DELETE FROM users WHERE id = $1 OR id = $2`,
			userA, userB); err != nil {
			t.Errorf("cleanup handoff tenants: %v", err)
		}
	})

	service := New(database.Pool)

	t.Run("idempotency head CAS resume and path isolation", func(t *testing.T) {
		taskKey := "migration-" + uuid.NewString()
		firstCommand := postgresCheckpointCommand(
			workspaceA,
			userA,
			taskKey,
			"first-"+uuid.NewString(),
			"/Work/project",
		)
		firstCommand.Handoff.State.Progress.Summary = strings.Repeat("界", 600)
		first, err := service.Checkpoint(ctx, firstCommand)
		if err != nil {
			t.Fatal(err)
		}
		if first.Replayed || first.Checkpoint.Sequence != 1 ||
			first.Checkpoint.BaseCheckpointID != nil {
			t.Fatalf("first result = %+v", first)
		}
		if len(first.Checkpoint.References) != 3 {
			t.Fatalf("first references = %#v", first.Checkpoint.References)
		}
		summaries, err := service.ListCheckpoints(ctx, ListCheckpointsQuery{
			WorkspaceID: workspaceA,
			TaskKey:     taskKey,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(summaries) != 1 ||
			summaries[0].ID != first.Checkpoint.ID ||
			summaries[0].Status != TaskStatusReady ||
			len([]rune(summaries[0].ProgressExcerpt)) != 500 ||
			summaries[0].ProgressLength != 600 ||
			summaries[0].CompletedCount != 1 ||
			summaries[0].ReferenceCount != 3 {
			t.Fatalf("bounded checkpoint summaries = %+v", summaries)
		}

		retry := firstCommand
		retry.CreatedByTokenID = uuidPointer(uuid.New())
		replayed, err := service.Checkpoint(ctx, retry)
		if err != nil {
			t.Fatal(err)
		}
		if !replayed.Replayed || replayed.Checkpoint.ID != first.Checkpoint.ID {
			t.Fatalf("replay = %+v; first=%s", replayed, first.Checkpoint.ID)
		}

		conflict := firstCommand
		conflict.Handoff.State.Progress.Summary = "An incompatible retry payload"
		if _, err := service.Checkpoint(ctx, conflict); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("idempotency conflict error = %v", err)
		}

		missingBase := postgresCheckpointCommand(
			workspaceA,
			userA,
			taskKey,
			"missing-base-"+uuid.NewString(),
			"/Work/project",
		)
		if _, err := service.Checkpoint(ctx, missingBase); !errors.Is(err, ErrBaseRequired) {
			t.Fatalf("missing base error = %v", err)
		}

		wrongBase := postgresCheckpointCommand(
			workspaceA,
			userA,
			taskKey,
			"wrong-base-"+uuid.NewString(),
			"/Work/project",
		)
		wrongBase.Handoff.BaseCheckpointID = uuidPointer(uuid.New())
		if _, err := service.Checkpoint(ctx, wrongBase); !errors.Is(err, ErrHeadConflict) {
			t.Fatalf("wrong base error = %v", err)
		}

		secondCommand := postgresCheckpointCommand(
			workspaceA,
			userA,
			taskKey,
			"second-"+uuid.NewString(),
			"/Work/project",
		)
		secondCommand.Handoff.BaseCheckpointID = uuidPointer(first.Checkpoint.ID)
		secondCommand.Handoff.State.Progress.Summary = "Second checkpoint"
		second, err := service.Checkpoint(ctx, secondCommand)
		if err != nil {
			t.Fatal(err)
		}
		if second.Checkpoint.Sequence != 2 ||
			second.Checkpoint.BaseCheckpointID == nil ||
			*second.Checkpoint.BaseCheckpointID != first.Checkpoint.ID {
			t.Fatalf("second result = %+v", second)
		}
		replayedAfterHeadAdvance, err := service.Checkpoint(ctx, retry)
		if err != nil {
			t.Fatal(err)
		}
		if !replayedAfterHeadAdvance.Replayed ||
			replayedAfterHeadAdvance.Checkpoint.ID != first.Checkpoint.ID {
			t.Fatalf("replay after head advance = %+v", replayedAfterHeadAdvance)
		}

		snapshot, err := service.Resume(ctx, ResumeQuery{
			WorkspaceID:  workspaceA,
			TaskKey:      taskKey,
			AllowedPaths: []string{"/Work"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Checkpoint.ID != second.Checkpoint.ID ||
			snapshot.Task.HeadCheckpointID == nil ||
			*snapshot.Task.HeadCheckpointID != second.Checkpoint.ID ||
			snapshot.Contract != ResumeContractName ||
			len(snapshot.References) != len(second.Checkpoint.References) {
			t.Fatalf("resume snapshot = %+v", snapshot)
		}

		historical, err := service.Resume(ctx, ResumeQuery{
			WorkspaceID:  workspaceA,
			TaskKey:      taskKey,
			CheckpointID: uuidPointer(first.Checkpoint.ID),
			Scope:        "/Work/project",
			AllowedPaths: []string{"/Work"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if historical.Checkpoint.ID != first.Checkpoint.ID {
			t.Fatalf("historical checkpoint = %s", historical.Checkpoint.ID)
		}

		for _, query := range []ResumeQuery{
			{WorkspaceID: workspaceB, TaskKey: taskKey},
			{WorkspaceID: workspaceA, TaskKey: taskKey, AllowedPaths: []string{"/Other"}},
			{WorkspaceID: workspaceA, TaskKey: taskKey, Scope: "/Workflows"},
		} {
			if _, err := service.Resume(ctx, query); !errors.Is(err, ErrNotFound) {
				t.Fatalf("isolated resume %+v error = %v", query, err)
			}
		}
	})

	t.Run("concurrent writers sharing one base have one winner", func(t *testing.T) {
		taskKey := "cas-" + uuid.NewString()
		initial, err := service.Checkpoint(ctx, postgresCheckpointCommand(
			workspaceA,
			userA,
			taskKey,
			"cas-initial-"+uuid.NewString(),
			"/Concurrency",
		))
		if err != nil {
			t.Fatal(err)
		}
		left := postgresCheckpointCommand(
			workspaceA, userA, taskKey, "cas-left-"+uuid.NewString(), "/Concurrency",
		)
		left.Handoff.BaseCheckpointID = uuidPointer(initial.Checkpoint.ID)
		left.Handoff.State.Progress.Summary = "left"
		right := postgresCheckpointCommand(
			workspaceA, userA, taskKey, "cas-right-"+uuid.NewString(), "/Concurrency",
		)
		right.Handoff.BaseCheckpointID = uuidPointer(initial.Checkpoint.ID)
		right.Handoff.State.Progress.Summary = "right"

		var start sync.WaitGroup
		start.Add(1)
		results := make(chan error, 2)
		for _, command := range []CheckpointCommand{left, right} {
			command := command
			go func() {
				start.Wait()
				_, err := service.Checkpoint(ctx, command)
				results <- err
			}()
		}
		start.Done()

		successes, conflicts := 0, 0
		for range 2 {
			err := <-results
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrHeadConflict):
				conflicts++
			default:
				t.Fatalf("concurrent CAS error = %v", err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("successes=%d conflicts=%d, want 1/1", successes, conflicts)
		}
		checkpoints, err := service.ListCheckpoints(ctx, ListCheckpointsQuery{
			WorkspaceID: workspaceA,
			TaskKey:     taskKey,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(checkpoints) != 2 || checkpoints[0].Sequence != 2 ||
			checkpoints[1].Sequence != 1 {
			t.Fatalf("checkpoint lineage = %+v", checkpoints)
		}
	})

	t.Run("concurrent equal retries commit one checkpoint", func(t *testing.T) {
		taskKey := "replay-" + uuid.NewString()
		command := postgresCheckpointCommand(
			workspaceA,
			userA,
			taskKey,
			"same-"+uuid.NewString(),
			"/Concurrency/replay",
		)
		const writers = 12
		results := make(chan *CheckpointResult, writers)
		errs := make(chan error, writers)
		var start sync.WaitGroup
		start.Add(1)
		for range writers {
			go func() {
				start.Wait()
				result, err := service.Checkpoint(ctx, command)
				results <- result
				errs <- err
			}()
		}
		start.Done()

		ids := map[uuid.UUID]struct{}{}
		created := 0
		for range writers {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent retry: %v", err)
			}
			result := <-results
			ids[result.Checkpoint.ID] = struct{}{}
			if !result.Replayed {
				created++
			}
		}
		if len(ids) != 1 || created != 1 {
			t.Fatalf("unique ids=%d created=%d, want 1/1", len(ids), created)
		}
	})

	t.Run("task listing is workspace and path scoped", func(t *testing.T) {
		tasks, err := service.ListTasks(ctx, ListTasksQuery{
			WorkspaceID:  workspaceA,
			AllowedPaths: []string{"/Concurrency"},
			Limit:        100,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) < 2 {
			t.Fatalf("concurrency task listing = %+v", tasks)
		}
		for _, task := range tasks {
			if task.ScopePath != "/Concurrency" &&
				task.ScopePath != "/Concurrency/replay" {
				t.Fatalf("out-of-scope task leaked: %+v", task)
			}
		}
		tasks, err = service.ListTasks(ctx, ListTasksQuery{
			WorkspaceID: workspaceB,
			Scope:       "/Concurrency",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 0 {
			t.Fatalf("cross-workspace tasks = %+v", tasks)
		}
	})
}

func postgresCheckpointCommand(
	workspaceID uuid.UUID,
	userID uuid.UUID,
	taskKey string,
	key string,
	path string,
) CheckpointCommand {
	handoff := validHandoffV1(taskKey)
	handoff.ScopePath = path
	return CheckpointCommand{
		WorkspaceID:      workspaceID,
		CreatedByUserID:  uuidPointer(userID),
		CreatedByTokenID: uuidPointer(uuid.New()),
		TaskKey:          taskKey,
		Handoff:          handoff,
		IdempotencyKey:   key,
	}
}

var handoffTestRunSuffix = uuid.NewString()

func createHandoffTenant(
	t *testing.T,
	ctx context.Context,
	database *memdb.DB,
	prefix string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var userID uuid.UUID
	email := prefix + "-" + handoffTestRunSuffix + "@example.com"
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'integration-test')
		RETURNING id
	`, email).Scan(&userID); err != nil {
		t.Fatalf("create handoff test user: %v", err)
	}
	var workspaceID uuid.UUID
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO workspaces (name, resource_owner_user_id)
		VALUES ($1, $2)
		RETURNING id
	`, prefix, userID).Scan(&workspaceID); err != nil {
		t.Fatalf("create handoff test workspace: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO workspace_memberships (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("create handoff test workspace membership: %v", err)
	}
	return userID, workspaceID
}
