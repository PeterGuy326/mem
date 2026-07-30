package managedusage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/auth"
	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

// TestReleasedFileStageRetryPostgres proves the production entitlement and
// managed-usage services can retry a provider stage that was proven not
// invoked without weakening generic idempotency, duplicating a succeeded
// provider call, or reserving later stages after replay.
func TestReleasedFileStageRetryPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping managed usage PostgreSQL test")
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
	user, err := authService.CreateUser(
		ctx,
		fmt.Sprintf("managed-usage-retry-%s@example.test", uuid.NewString()),
		"secret-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspaceService.Resolve(ctx, user.ID, nil)
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
			t.Errorf("clean up managed usage tenant: %v", err)
		}
	})

	now := time.Now().UTC().Truncate(time.Second)
	if _, err := database.Pool.Exec(ctx, `
		UPDATE workspace_entitlements
		   SET plan_key = 'member',
		       status = 'active',
		       period_start = $2,
		       period_end = $3,
		       managed_embedding_unit_limit = 10,
		       managed_embedding_units_reserved = 0,
		       managed_embedding_units_consumed = 0,
		       updated_at = $2
		 WHERE workspace_id = $1
	`, ws.ID, now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	ledger := entitlement.New(database.Pool, 10*time.Minute)
	coordinator := New(ledger)
	command := qualityCommand()
	command.WorkspaceID = ws.ID
	command.FileID = uuid.New()
	command.ContentSHA256 = strings.Repeat("d", 64)

	baseEmbedding, err := reserveCommand(command, normalizedStage{
		Stage:        StageEmbedding,
		ProviderSpec: command.Stages[0].ProviderSpec,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := coordinator.Prepare(ctx, command)
	if err != nil {
		t.Fatalf("first Prepare() error = %v", err)
	}
	firstReservations := first.Reservations()
	if len(firstReservations) != 2 || first.HasReplay() {
		t.Fatalf("first reservations = %#v", firstReservations)
	}
	for _, reservation := range firstReservations {
		if err := coordinator.SettleUsage(ctx, reservation.UsageID, OutcomeNotInvoked); err != nil {
			t.Fatalf("release first usage: %v", err)
		}
		if err := coordinator.SettleUsage(ctx, reservation.UsageID, OutcomeNotInvoked); err != nil {
			t.Fatalf("repeat release first usage: %v", err)
		}
	}

	if _, err := ledger.Reserve(ctx, baseEmbedding); !errors.Is(err, entitlement.ErrReleasedKey) {
		t.Fatalf("generic Reserve() error = %v, want ErrReleasedKey", err)
	}
	if _, err := ledger.LookupReservation(
		ctx,
		baseEmbedding,
		entitlement.ReservationLookupOptions{},
	); !errors.Is(err, entitlement.ErrReleasedKey) {
		t.Fatalf("lookup without explicit option error = %v, want ErrReleasedKey", err)
	}
	released, err := ledger.LookupReservation(
		ctx,
		baseEmbedding,
		entitlement.ReservationLookupOptions{IncludeReleased: true},
	)
	if err != nil {
		t.Fatalf("released lookup error = %v", err)
	}
	if released.ID != firstReservations[0].UsageID ||
		released.Status != entitlement.StatusReleased {
		t.Fatalf("released lookup = %#v, want first embedding", released)
	}

	second, err := coordinator.Prepare(ctx, command)
	if err != nil {
		t.Fatalf("second Prepare() error = %v", err)
	}
	secondReservations := second.Reservations()
	if len(secondReservations) != 2 || second.HasReplay() {
		t.Fatalf("second reservations = %#v", secondReservations)
	}
	for i := range secondReservations {
		if secondReservations[i].UsageID == firstReservations[i].UsageID {
			t.Fatalf("stage %d reused released usage ID", i)
		}
	}
	if err := coordinator.SettleUsage(
		ctx,
		secondReservations[0].UsageID,
		OutcomeSucceeded,
	); err != nil {
		t.Fatalf("finalize retried embedding: %v", err)
	}
	if err := coordinator.SettleUsage(
		ctx,
		secondReservations[0].UsageID,
		OutcomeSucceeded,
	); err != nil {
		t.Fatalf("repeat finalize retried embedding: %v", err)
	}
	if err := coordinator.SettleUsage(
		ctx,
		secondReservations[1].UsageID,
		OutcomeNotInvoked,
	); err != nil {
		t.Fatalf("release retried LLM: %v", err)
	}
	if err := coordinator.SettleUsage(
		ctx,
		secondReservations[1].UsageID,
		OutcomeNotInvoked,
	); err != nil {
		t.Fatalf("repeat release retried LLM: %v", err)
	}

	third, err := coordinator.Prepare(ctx, command)
	if err != nil {
		t.Fatalf("third Prepare() error = %v", err)
	}
	thirdReservations := third.Reservations()
	if len(thirdReservations) != 1 || !third.HasReplay() ||
		!thirdReservations[0].Replayed ||
		thirdReservations[0].UsageID != secondReservations[0].UsageID {
		t.Fatalf("third reservations = %#v, want only succeeded embedding replay", thirdReservations)
	}

	var usageCount, succeeded, releasedCount, reserved int
	if err := database.Pool.QueryRow(ctx, `
		SELECT
		    count(*),
		    count(*) FILTER (WHERE status = 'succeeded'),
		    count(*) FILTER (WHERE status = 'released'),
		    count(*) FILTER (WHERE status = 'reserved')
		  FROM managed_embedding_usage
		 WHERE workspace_id = $1
	`, ws.ID).Scan(&usageCount, &succeeded, &releasedCount, &reserved); err != nil {
		t.Fatal(err)
	}
	if usageCount != 4 || succeeded != 1 || releasedCount != 3 || reserved != 0 {
		t.Fatalf(
			"usage states total=%d succeeded=%d released=%d reserved=%d, want 4/1/3/0",
			usageCount,
			succeeded,
			releasedCount,
			reserved,
		)
	}
	summary, err := ledger.Summary(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Reserved != 0 || summary.Consumed != 1 || summary.Remaining != 9 {
		t.Fatalf("summary after retry/replay = %+v", summary)
	}
}
