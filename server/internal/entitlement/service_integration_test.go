package entitlement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/auth"
	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

// TestManagedEmbeddingEntitlementPostgres exercises the real row locks,
// constraints, idempotency uniqueness, audit events, TTL reconciliation and
// period rollover used by hosted managed embeddings.
func TestManagedEmbeddingEntitlementPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping managed entitlement PostgreSQL test")
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

	authService := auth.New(database.Pool)
	workspaceService := workspace.New(database.Pool)
	user, ws := createEntitlementTenant(t, ctx, authService, workspaceService, "primary")
	otherUser, otherWS := createEntitlementTenant(
		t,
		ctx,
		authService,
		workspaceService,
		"other",
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(
			cleanupCtx,
			`DELETE FROM users WHERE id = $1 OR id = $2`,
			user.ID,
			otherUser.ID,
		); err != nil {
			t.Errorf("clean up entitlement tenants: %v", err)
		}
	})

	baseNow := time.Now().UTC().Truncate(time.Second)
	service := New(database.Pool, time.Minute)
	service.now = func() time.Time { return baseNow }

	t.Run("inactive plan fails before creating usage", func(t *testing.T) {
		resetEntitlement(t, ctx, database.Pool, ws.ID, baseNow, false, 0)
		_, err := service.Reserve(ctx, reserveCommand(ws.ID, "no-plan", "a"))
		if !errors.Is(err, ErrPlanRequired) {
			t.Fatalf("reserve error = %v, want plan required", err)
		}
		assertUsageCount(t, ctx, database.Pool, ws.ID, 0)
	})

	t.Run("concurrent last unit idempotency and audit", func(t *testing.T) {
		resetEntitlement(t, ctx, database.Pool, ws.ID, baseNow, true, 1)
		left := reserveCommand(ws.ID, "last-left", "a")
		right := reserveCommand(ws.ID, "last-right", "b")

		var start sync.WaitGroup
		start.Add(1)
		type result struct {
			res *Reservation
			err error
		}
		results := make(chan result, 2)
		for _, command := range []ReserveCommand{left, right} {
			command := command
			go func() {
				start.Wait()
				reservation, err := service.Reserve(ctx, command)
				results <- result{res: reservation, err: err}
			}()
		}
		start.Done()

		var winner *Reservation
		successes, exhausted := 0, 0
		for range 2 {
			got := <-results
			switch {
			case got.err == nil:
				successes++
				winner = got.res
			case errors.Is(got.err, ErrQuotaExhausted):
				exhausted++
			default:
				t.Fatalf("concurrent reserve error = %v", got.err)
			}
		}
		if successes != 1 || exhausted != 1 {
			t.Fatalf("successes=%d exhausted=%d, want 1/1", successes, exhausted)
		}
		summary, err := service.Summary(ctx, ws.ID)
		if err != nil {
			t.Fatal(err)
		}
		if summary.Reserved != 1 || summary.Consumed != 0 || summary.Remaining != 0 {
			t.Fatalf("reserved summary = %+v", summary)
		}

		reference := ReplayReference{
			Source:     "text",
			EvidenceID: uuid.New(),
			FileID:     uuid.New(),
			Score:      0.75,
		}
		summary, err = service.Finalize(ctx, winner.ID, []ReplayReference{reference})
		if err != nil {
			t.Fatal(err)
		}
		if summary.Reserved != 0 || summary.Consumed != 1 || summary.Remaining != 0 {
			t.Fatalf("finalized summary = %+v", summary)
		}

		var winningCommand ReserveCommand
		if winner.ID != uuid.Nil {
			var keyHash string
			if err := database.Pool.QueryRow(ctx, `
				SELECT idempotency_key_sha256
				  FROM managed_embedding_usage
				 WHERE id = $1
			`, winner.ID).Scan(&keyHash); err != nil {
				t.Fatal(err)
			}
			for _, command := range []ReserveCommand{left, right} {
				expected := hashDomain(
					"mem/managed-embedding/idempotency/v1/"+ws.ID.String()+"/"+command.Operation,
					command.IdempotencyKey,
				)
				if keyHash == expected {
					winningCommand = command
				}
			}
		}
		replayed, err := service.Reserve(ctx, winningCommand)
		if err != nil {
			t.Fatal(err)
		}
		if !replayed.Replayed || replayed.ID != winner.ID ||
			len(replayed.References) != 1 ||
			replayed.References[0] != reference {
			t.Fatalf("replay = %+v", replayed)
		}
		conflict := winningCommand
		conflict.RequestFingerprint = strings.Repeat("c", 64)
		if _, err := service.Reserve(ctx, conflict); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("conflict error = %v", err)
		}

		var reservedEvents, succeededEvents int
		if err := database.Pool.QueryRow(ctx, `
			SELECT
			    count(*) FILTER (WHERE status = 'reserved'),
			    count(*) FILTER (WHERE status = 'succeeded')
			  FROM managed_embedding_usage_events
			 WHERE usage_id = $1
		`, winner.ID).Scan(&reservedEvents, &succeededEvents); err != nil {
			t.Fatal(err)
		}
		if reservedEvents != 1 || succeededEvents != 1 {
			t.Fatalf(
				"audit events reserved=%d succeeded=%d, want 1/1",
				reservedEvents,
				succeededEvents,
			)
		}
	})

	t.Run("stale reservation becomes indeterminate and never replays provider", func(t *testing.T) {
		resetEntitlement(t, ctx, database.Pool, ws.ID, baseNow, true, 2)
		oldCommand := reserveCommand(ws.ID, "stale", "d")
		old, err := service.Reserve(ctx, oldCommand)
		if err != nil {
			t.Fatal(err)
		}
		service.now = func() time.Time { return baseNow.Add(2 * time.Minute) }
		fresh, err := service.Reserve(
			ctx,
			reserveCommand(ws.ID, "fresh-after-stale", "e"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Reserve(ctx, oldCommand); !errors.Is(err, ErrRequestIndeterminate) {
			t.Fatalf("stale replay error = %v", err)
		}
		var status string
		if err := database.Pool.QueryRow(ctx,
			`SELECT status FROM managed_embedding_usage WHERE id = $1`,
			old.ID,
		).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != StatusIndeterminate {
			t.Fatalf("stale status = %q", status)
		}
		if _, err := service.Release(ctx, fresh.ID); err != nil {
			t.Fatal(err)
		}
		service.now = func() time.Time { return baseNow }
	})

	t.Run("summary atomically rolls period and freezes crossing request", func(t *testing.T) {
		resetEntitlement(t, ctx, database.Pool, ws.ID, baseNow, true, 5)
		crossing, err := service.Reserve(
			ctx,
			reserveCommand(ws.ID, "cross-period", "f"),
		)
		if err != nil {
			t.Fatal(err)
		}
		periodStart := baseNow.Add(-2 * time.Hour)
		periodEnd := baseNow.Add(-time.Hour)
		if _, err := database.Pool.Exec(ctx, `
			UPDATE workspace_entitlements
			   SET period_start = $2,
			       period_end = $3,
			       managed_embedding_units_reserved = 1,
			       managed_embedding_units_consumed = 4
			 WHERE workspace_id = $1
		`, ws.ID, periodStart, periodEnd); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Pool.Exec(ctx, `
			UPDATE managed_embedding_usage
			   SET period_start = $1, period_end = $2
			 WHERE id = $3
		`, periodStart, periodEnd, crossing.ID); err != nil {
			t.Fatal(err)
		}
		summary, err := service.Summary(ctx, ws.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !summary.Qualifying || summary.Reserved != 0 ||
			summary.Consumed != 0 || summary.Remaining != 5 ||
			!summary.ResetAt.After(baseNow) {
			t.Fatalf("rolled summary = %+v", summary)
		}
		var status string
		if err := database.Pool.QueryRow(ctx,
			`SELECT status FROM managed_embedding_usage WHERE id = $1`,
			crossing.ID,
		).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != StatusIndeterminate {
			t.Fatalf("cross-period status = %q", status)
		}
	})

	t.Run("idempotency digest is workspace bound", func(t *testing.T) {
		resetEntitlement(t, ctx, database.Pool, ws.ID, baseNow, true, 1)
		resetEntitlement(t, ctx, database.Pool, otherWS.ID, baseNow, true, 1)
		first, err := service.Reserve(ctx, reserveCommand(ws.ID, "same-key", "1"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := service.Reserve(ctx, reserveCommand(otherWS.ID, "same-key", "1"))
		if err != nil {
			t.Fatal(err)
		}
		var firstHash, secondHash string
		if err := database.Pool.QueryRow(ctx, `
			SELECT
			    (SELECT idempotency_key_sha256 FROM managed_embedding_usage WHERE id = $1),
			    (SELECT idempotency_key_sha256 FROM managed_embedding_usage WHERE id = $2)
		`, first.ID, second.ID).Scan(&firstHash, &secondHash); err != nil {
			t.Fatal(err)
		}
		if firstHash == secondHash {
			t.Fatal("same plaintext key must not be linkable across workspaces")
		}
	})

	t.Run("replay schema excludes arbitrary payload and nonfinite scores", func(t *testing.T) {
		var dataType string
		err := database.Pool.QueryRow(ctx, `
			SELECT data_type
			  FROM information_schema.columns
			 WHERE table_schema = 'public'
			   AND table_name = 'managed_embedding_replay_results'
			   AND column_name = 'evidence_id'
		`).Scan(&dataType)
		if err != nil {
			t.Fatal(err)
		}
		if dataType != "uuid" {
			t.Fatalf("evidence_id type = %q, want uuid", dataType)
		}
		var unsafeColumns int
		if err := database.Pool.QueryRow(ctx, `
			SELECT count(*)
			  FROM information_schema.columns
			 WHERE table_schema = 'public'
			   AND table_name IN (
			       'managed_embedding_usage',
			       'managed_embedding_usage_events',
			       'managed_embedding_replay_results'
			   )
			   AND (
			       column_name LIKE '%query%'
			       OR column_name LIKE '%content%'
			       OR column_name LIKE '%vector%'
			       OR column_name LIKE '%credential%'
			       OR data_type = 'jsonb'
			   )
		`).Scan(&unsafeColumns); err != nil {
			t.Fatal(err)
		}
		if unsafeColumns != 0 {
			t.Fatalf("managed usage schema has %d unsafe payload columns", unsafeColumns)
		}

		resetEntitlement(t, ctx, database.Pool, ws.ID, baseNow, true, 1)
		reservation, err := service.Reserve(
			ctx,
			reserveCommand(ws.ID, "nan-check", "2"),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = database.Pool.Exec(ctx, `
			INSERT INTO managed_embedding_replay_results (
			    usage_id, rank, source, evidence_id, file_id, score
			) VALUES ($1, 0, 'text', $2, $3, 'NaN'::real)
		`, reservation.ID, uuid.New(), uuid.New())
		if err == nil {
			t.Fatal("expected replay score constraint to reject NaN")
		}
	})

	t.Run("account deletion erases workspace entitlement and usage", func(t *testing.T) {
		deleteUser, deleteWorkspace := createEntitlementTenant(
			t,
			ctx,
			authService,
			workspaceService,
			"delete",
		)
		resetEntitlement(t, ctx, database.Pool, deleteWorkspace.ID, baseNow, true, 1)
		service.now = func() time.Time { return baseNow }
		reservation, err := service.Reserve(
			ctx,
			reserveCommand(deleteWorkspace.ID, "delete-account", "3"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Finalize(ctx, reservation.ID, []ReplayReference{{
			Source:     "text",
			EvidenceID: uuid.New(),
			FileID:     uuid.New(),
			Score:      0.25,
		}}); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Pool.Exec(
			ctx,
			`DELETE FROM users WHERE id = $1`,
			deleteUser.ID,
		); err != nil {
			t.Fatal(err)
		}

		var entitlements, usages, events, replayResults int
		if err := database.Pool.QueryRow(ctx, `
			SELECT
			    (SELECT count(*) FROM workspace_entitlements WHERE workspace_id = $1),
			    (SELECT count(*) FROM managed_embedding_usage WHERE workspace_id = $1),
			    (SELECT count(*) FROM managed_embedding_usage_events WHERE workspace_id = $1),
			    (SELECT count(*) FROM managed_embedding_replay_results WHERE usage_id = $2)
		`, deleteWorkspace.ID, reservation.ID).Scan(
			&entitlements,
			&usages,
			&events,
			&replayResults,
		); err != nil {
			t.Fatal(err)
		}
		if entitlements != 0 || usages != 0 || events != 0 || replayResults != 0 {
			t.Fatalf(
				"post-delete rows entitlement=%d usage=%d events=%d replay=%d",
				entitlements,
				usages,
				events,
				replayResults,
			)
		}
	})
}

func createEntitlementTenant(
	t *testing.T,
	ctx context.Context,
	authService *auth.Service,
	workspaceService *workspace.Service,
	label string,
) (*auth.User, *workspace.Workspace) {
	t.Helper()
	user, err := authService.CreateUser(
		ctx,
		fmt.Sprintf("entitlement-%s-%s@example.test", label, uuid.NewString()),
		"secret-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspaceService.Resolve(ctx, user.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return user, ws
}

func resetEntitlement(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID uuid.UUID,
	now time.Time,
	active bool,
	limit int64,
) {
	t.Helper()
	status, plan := "inactive", "free"
	if active {
		status, plan = "active", "member"
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM managed_embedding_usage WHERE workspace_id = $1`,
		workspaceID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE workspace_entitlements
		   SET plan_key = $2,
		       status = $3,
		       period_start = $4,
		       period_end = $5,
		       managed_embedding_unit_limit = $6,
		       managed_embedding_units_reserved = 0,
		       managed_embedding_units_consumed = 0,
		       updated_at = $4
		 WHERE workspace_id = $1
	`, workspaceID, plan, status, now.Add(-time.Hour), now.Add(time.Hour), limit); err != nil {
		t.Fatal(err)
	}
}

func reserveCommand(
	workspaceID uuid.UUID,
	key, fingerprintByte string,
) ReserveCommand {
	return ReserveCommand{
		WorkspaceID:        workspaceID,
		Operation:          "search.query",
		ProviderSpec:       "openai:text-embedding-3-small",
		Units:              1,
		IdempotencyKey:     key,
		RequestFingerprint: strings.Repeat(fingerprintByte, 64),
	}
}

func assertUsageCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID uuid.UUID,
	want int,
) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM managed_embedding_usage WHERE workspace_id = $1`,
		workspaceID,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("usage count = %d, want %d", got, want)
	}
}
