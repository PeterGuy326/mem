package indexgeneration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/auth"
	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/testutil/pglockwait"
	"github.com/PeterGuy326/mem/server/internal/workspace"
	"github.com/PeterGuy326/mem/server/internal/workspacelock"
)

func TestIndexGenerationPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping index generation PostgreSQL test")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse MEM_TEST_DB: %v", err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatalf("refusing to modify non-test database %q", config.ConnConfig.Database)
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
	owner, err := authService.CreateUser(
		ctx,
		fmt.Sprintf("index-generation-%s@example.test", uuid.NewString()),
		"secret-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.New(database.Pool).Resolve(ctx, owner.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, owner.ID); err != nil {
			t.Errorf("clean up generation tenant: %v", err)
		}
	})

	for index := range 2 {
		insertGenerationTestFile(t, ctx, database.Pool, owner.ID, index)
	}
	createBackend := pglockwait.NewBackend(t, ctx, dsn, "generation-create")
	writerBackend := pglockwait.NewBackend(t, ctx, dsn, "generation-writer")
	service := New(createBackend.Pool, aiprofile.LocalFastV2, aiprofile.IdealabQualityV2)

	// Account deletion must not depend on PostgreSQL's ordering of the file,
	// workspace and optional creator FK actions.
	deletingOwner, err := authService.CreateUser(
		ctx,
		fmt.Sprintf("index-generation-delete-%s@example.test", uuid.NewString()),
		"secret-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	deletingWorkspace, err := workspace.New(database.Pool).Resolve(ctx, deletingOwner.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	insertGenerationTestFile(t, ctx, database.Pool, deletingOwner.ID, 99)
	deletingBuild, err := service.Create(
		ctx, deletingWorkspace.ID, deletingOwner.ID, aiprofile.LocalFastV2,
	)
	if err != nil {
		t.Fatalf("create account-deletion generation: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, deletingOwner.ID); err != nil {
		t.Fatalf("delete generation owner: %v", err)
	}
	var deletingBuildPresent bool
	if err := database.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM index_generation_builds WHERE id = $1)
	`, deletingBuild.ID).Scan(&deletingBuildPresent); err != nil || deletingBuildPresent {
		t.Fatalf("account-deletion build present = %t, err = %v", deletingBuildPresent, err)
	}

	// A content writer that acquired the canonical KEY SHARE lock before the
	// build must commit before Create can take its exclusive corpus snapshot.
	writer, err := writerBackend.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := workspacelock.ForContentWriteByOwner(ctx, writer, owner.ID); err != nil {
		t.Fatal(err)
	}
	insertGenerationTestFile(t, ctx, writer, owner.ID, 2)
	type createResult struct {
		build *Build
		err   error
	}
	created := make(chan createResult, 1)
	go func() {
		build, createErr := service.Create(ctx, ws.ID, owner.ID, aiprofile.LocalFastV2)
		created <- createResult{build: build, err: createErr}
	}()
	pglockwait.WaitBlocked(t, ctx, database.Pool, createBackend, writerBackend.PID,
		"SELECT resource_owner_user_id FROM workspaces", "FOR UPDATE")
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-created
	local, err := result.build, result.err
	if err != nil {
		t.Fatalf("create local generation: %v", err)
	}
	if local.State != StateBuilding || local.RequiredTargets != 3 ||
		local.CorpusFileCount != 3 || len(local.Generations) != 1 {
		t.Fatalf("local build = %#v", local)
	}
	definition, _ := aiprofile.Find(aiprofile.LocalFastV2)
	if !reflect.DeepEqual(local.ProfileSnapshot, snapshotFromDefinition(definition)) {
		t.Fatalf("persisted profile snapshot = %#v", local.ProfileSnapshot)
	}
	encodedBuild, err := json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedBuild), owner.ID.String()) || !local.CreatorPresent {
		t.Fatalf("build creator privacy/presence = %s", encodedBuild)
	}
	if local.Generations[0].OutputDimension != 768 || local.Generations[0].RouteKind != RouteText {
		t.Fatalf("local route = %#v", local.Generations[0])
	}

	// Files committed after the snapshot lock are explicitly excluded from the
	// build and are picked up by a later generation.
	postSnapshotID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	insertGenerationTestFileWithID(t, ctx, database.Pool, owner.ID, postSnapshotID, 3)
	var included bool
	if err := database.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM index_generation_targets t
			JOIN index_generations g ON g.id = t.generation_id
			WHERE g.build_id = $1 AND t.file_id = $2
		)
	`, local.ID, postSnapshotID).Scan(&included); err != nil || included {
		t.Fatalf("post-snapshot file included = %t, err = %v", included, err)
	}
	again, err := service.Create(ctx, ws.ID, owner.ID, aiprofile.LocalFastV2)
	if err != nil || again.ID != local.ID {
		t.Fatalf("idempotent Create() = %#v, %v", again, err)
	}
	assertPostgresCode(t, func() error {
		_, updateErr := database.Pool.Exec(ctx, `
			UPDATE index_generation_builds
			SET profile_snapshot = jsonb_set(profile_snapshot, '{revision}', '"tampered"')
			WHERE id = $1
		`, local.ID)
		return updateErr
	}(), "23514")

	other, err := authService.CreateUser(
		ctx,
		fmt.Sprintf("index-generation-other-%s@example.test", uuid.NewString()),
		"secret-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspace, err := workspace.New(database.Pool).Resolve(ctx, other.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = database.Pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, other.ID)
	})
	if _, err := service.Get(ctx, otherWorkspace.ID, local.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Get() error = %v, want ErrNotFound", err)
	}
	assertPostgresCode(t, func() error {
		_, insertErr := database.Pool.Exec(ctx, `
			INSERT INTO index_generations (
				id, build_id, workspace_id, route_kind, provider,
				model_revision, output_dimension, pipeline_revision,
				profile_id, profile_revision
			) VALUES ($1,$2,$3,'visual','clip','ViT-B-32',512,$4,$5,$6)
		`, uuid.New(), local.ID, otherWorkspace.ID, local.PipelineRevision,
			local.ProfileID, local.ProfileRevision)
		return insertErr
	}(), "23503")

	firstAttempt, err := service.ClaimTarget(ctx, ws.ID, local.ID)
	if err != nil {
		t.Fatalf("claim first attempt: %v", err)
	}
	if firstAttempt.AttemptToken == uuid.Nil ||
		!firstAttempt.LeaseExpiresAt.After(time.Now()) {
		t.Fatalf("claim did not return a live attempt lease: %#v", firstAttempt)
	}
	realNow := service.now
	service.now = func() time.Time { return firstAttempt.LeaseExpiresAt.Add(time.Second) }
	expiredAttempt, err := service.ClaimTarget(ctx, ws.ID, local.ID)
	service.now = realNow
	if err != nil || expiredAttempt.FileID != firstAttempt.FileID ||
		expiredAttempt.AttemptToken == firstAttempt.AttemptToken || expiredAttempt.Attempts != 2 {
		t.Fatalf("expired lease reclaim = %#v, %v", expiredAttempt, err)
	}
	cancelled, err := service.Cancel(ctx, ws.ID, owner.ID, local.ID)
	if err != nil || cancelled.State != StateCancelled {
		t.Fatalf("Cancel() = %#v, %v", cancelled, err)
	}
	if _, err := service.ClaimTarget(ctx, ws.ID, local.ID); !errors.Is(err, ErrTargetUnavailable) {
		t.Fatalf("claim cancelled target error = %v", err)
	}
	resumed, err := service.Resume(ctx, ws.ID, owner.ID, local.ID)
	if err != nil || resumed.State != StateBuilding {
		t.Fatalf("Resume() = %#v, %v", resumed, err)
	}

	first, err := service.ClaimTarget(ctx, ws.ID, local.ID)
	if err != nil {
		t.Fatalf("claim first local target: %v", err)
	}
	if first.FileID != firstAttempt.FileID || first.AttemptToken == firstAttempt.AttemptToken ||
		first.AttemptToken == expiredAttempt.AttemptToken || first.Attempts != 3 {
		t.Fatalf("reclaimed target = %#v; first attempt = %#v", first, firstAttempt)
	}
	if _, err := service.SucceedTarget(ctx, *firstAttempt, []Vector{{
		Ordinal: 0, EvidenceText: "stale", Values: make([]float32, 768),
	}}); !errors.Is(err, ErrTargetUnavailable) {
		t.Fatalf("stale attempt completion error = %v", err)
	}
	if _, err := service.SucceedTarget(ctx, *expiredAttempt, []Vector{{
		Ordinal: 0, EvidenceText: "expired", Values: make([]float32, 768),
	}}); !errors.Is(err, ErrTargetUnavailable) {
		t.Fatalf("expired attempt completion error = %v", err)
	}
	if _, err := service.SucceedTarget(ctx, *first, []Vector{{
		Ordinal: 0, EvidenceText: "first", Values: make([]float32, 512),
	}}); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("mixed-dimension success error = %v", err)
	}
	local, err = service.SucceedTarget(ctx, *first, []Vector{{
		Ordinal: 0, EvidenceText: "first", Values: make([]float32, 768),
	}})
	if err != nil || local.State != StateBuilding || local.SucceededTargets != 1 {
		t.Fatalf("first success = %#v, %v", local, err)
	}
	for local.State == StateBuilding {
		next, claimErr := service.ClaimTarget(ctx, ws.ID, local.ID)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		local, err = service.SucceedTarget(ctx, *next, []Vector{{
			Ordinal: 0, EvidenceText: "remaining", Values: make([]float32, 768),
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if local.State != StateReady || local.SucceededTargets != 3 {
		t.Fatalf("ready local build = %#v", local)
	}
	local, err = service.Activate(ctx, ws.ID, owner.ID, local.ID)
	if err != nil || local.State != StateActive {
		t.Fatalf("activate local build = %#v, %v", local, err)
	}

	// Reusing an identical profile for a later corpus creates a new build; the
	// permanent comparable-space identity is no longer globally unique.
	laterLocal, err := service.Create(ctx, ws.ID, owner.ID, aiprofile.LocalFastV2)
	if err != nil || laterLocal.ID == local.ID || laterLocal.RequiredTargets != 4 ||
		laterLocal.CorpusFileCount != 4 {
		t.Fatalf("same profile on later corpus = %#v, %v", laterLocal, err)
	}
	claimBuildLocked := make(chan struct{})
	releaseClaim := make(chan struct{})
	releasedClaim := false
	defer func() {
		if !releasedClaim {
			close(releaseClaim)
		}
	}()
	service.afterClaimBuildLocked = func() {
		close(claimBuildLocked)
		<-releaseClaim
	}
	type claimResult struct {
		target *Target
		err    error
	}
	claimed := make(chan claimResult, 1)
	go func() {
		target, claimErr := service.ClaimTarget(ctx, ws.ID, laterLocal.ID)
		claimed <- claimResult{target: target, err: claimErr}
	}()
	<-claimBuildLocked

	deleteBackend := pglockwait.NewBackend(t, ctx, dsn, "generation-delete")
	deleteTx, err := deleteBackend.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := workspacelock.ForContentWriteByOwner(ctx, deleteTx, owner.ID); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() {
		_, deleteErr := deleteTx.Exec(ctx,
			`DELETE FROM files WHERE id = $1 AND user_id = $2`,
			postSnapshotID, owner.ID)
		deleted <- deleteErr
	}()
	// DELETE already owns the file row. The trigger must now wait on the build
	// held by ClaimTarget, not acquire the target and create build<->target
	// deadlock when the claim continues.
	pglockwait.WaitBlocked(t, ctx, database.Pool, deleteBackend, createBackend.PID,
		"DELETE FROM files")
	close(releaseClaim)
	releasedClaim = true
	claimOutcome := <-claimed
	deletingAttempt, err := claimOutcome.target, claimOutcome.err
	if err != nil || deletingAttempt.FileID != postSnapshotID {
		t.Fatalf("claim post-snapshot target = %#v, %v", deletingAttempt, err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("delete after concurrent claim: %v", err)
	}
	service.afterClaimBuildLocked = nil

	// Simulate a caller failure after the BEFORE trigger ran. Transaction
	// rollback must restore the source, target lease and progress atomically;
	// the next canonical delete must then succeed without a leaked exception.
	if err := deleteTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var (
		sourcePresent bool
		targetState   string
		attemptToken  uuid.UUID
		skippedCount  int
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT t.source_present, t.state, t.attempt_token, b.skipped_targets
		  FROM index_generation_targets t
		  JOIN index_generations g ON g.id = t.generation_id
		  JOIN index_generation_builds b ON b.id = g.build_id
		 WHERE b.id = $1 AND t.file_id = $2
	`, laterLocal.ID, postSnapshotID).Scan(
		&sourcePresent, &targetState, &attemptToken, &skippedCount,
	); err != nil || !sourcePresent || targetState != TargetProcessing ||
		attemptToken != deletingAttempt.AttemptToken || skippedCount != 0 {
		t.Fatalf("rolled-back delete state = present:%t state:%s token:%s skipped:%d err:%v",
			sourcePresent, targetState, attemptToken, skippedCount, err)
	}
	retryDeleteTx, err := deleteBackend.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer retryDeleteTx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := workspacelock.ForContentWriteByOwner(ctx, retryDeleteTx, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := retryDeleteTx.Exec(ctx,
		`DELETE FROM files WHERE id = $1 AND user_id = $2`,
		postSnapshotID, owner.ID); err != nil {
		t.Fatalf("retry file delete: %v", err)
	}
	if err := retryDeleteTx.Commit(ctx); err != nil {
		t.Fatalf("commit retried file delete: %v", err)
	}
	laterLocal, err = service.Get(ctx, ws.ID, laterLocal.ID)
	if err != nil || laterLocal.RequiredTargets != 4 || laterLocal.SkippedTargets != 1 ||
		laterLocal.State != StateBuilding {
		t.Fatalf("deleted-source tombstone build = %#v, %v", laterLocal, err)
	}
	if _, err := service.SucceedTarget(ctx, *deletingAttempt, []Vector{{
		Ordinal: 0, EvidenceText: "deleted", Values: make([]float32, 768),
	}}); !errors.Is(err, ErrTargetUnavailable) {
		t.Fatalf("deleted-source stale completion error = %v", err)
	}
	laterLocal, err = service.Cancel(ctx, ws.ID, owner.ID, laterLocal.ID)
	if err != nil {
		t.Fatal(err)
	}
	laterLocal, err = service.Discard(ctx, ws.ID, owner.ID, laterLocal.ID)
	if err != nil || laterLocal.RetentionUntil == nil {
		t.Fatalf("discard later corpus = %#v, %v", laterLocal, err)
	}

	managed, err := service.Create(ctx, ws.ID, owner.ID, aiprofile.IdealabQualityV2)
	if err != nil {
		t.Fatalf("create replacement generation: %v", err)
	}
	failing, err := service.ClaimTarget(ctx, ws.ID, managed.ID)
	if err != nil {
		t.Fatal(err)
	}
	managed, err = service.FailTarget(ctx, *failing, "provider_unavailable")
	if err != nil || managed.State != StateBuilding {
		t.Fatalf("first failed target = %#v, %v", managed, err)
	}
	successful, err := service.ClaimTarget(ctx, ws.ID, managed.ID)
	if err != nil {
		t.Fatal(err)
	}
	managed, err = service.SucceedTarget(ctx, *successful, []Vector{{
		Ordinal: 0, EvidenceText: "managed", Values: make([]float32, 768),
	}})
	if err != nil || managed.State != StateBuilding || managed.FailedTargets != 1 {
		t.Fatalf("replacement second target = %#v, %v", managed, err)
	}
	successful, err = service.ClaimTarget(ctx, ws.ID, managed.ID)
	if err != nil {
		t.Fatal(err)
	}
	managed, err = service.SucceedTarget(ctx, *successful, []Vector{{
		Ordinal: 0, EvidenceText: "managed-2", Values: make([]float32, 768),
	}})
	if err != nil || managed.State != StateFailed || managed.FailedTargets != 1 {
		t.Fatalf("failed replacement = %#v, %v", managed, err)
	}
	stillActive, err := service.Get(ctx, ws.ID, local.ID)
	if err != nil || stillActive.State != StateActive {
		t.Fatalf("failed replacement changed active build: %#v, %v", stillActive, err)
	}
	if _, err := service.Activate(ctx, ws.ID, owner.ID, managed.ID); !errors.Is(err, ErrQualityGate) {
		t.Fatalf("activate failed replacement error = %v", err)
	}

	managed, err = service.Resume(ctx, ws.ID, owner.ID, managed.ID)
	if err != nil {
		t.Fatalf("resume replacement: %v", err)
	}
	retry, err := service.ClaimTarget(ctx, ws.ID, managed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ContentSHA256 != failing.ContentSHA256 || retry.Attempts != 2 {
		t.Fatalf("retry target = %#v", retry)
	}
	managed, err = service.SucceedTarget(ctx, *retry, []Vector{{
		Ordinal: 0, EvidenceText: "retry", Values: make([]float32, 768),
	}})
	if err != nil || managed.State != StateReady {
		t.Fatalf("replacement ready = %#v, %v", managed, err)
	}
	delete(service.enabled, aiprofile.IdealabQualityV2)
	if _, err := service.Activate(ctx, ws.ID, owner.ID, managed.ID); !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("disabled historical profile activation error = %v", err)
	}
	service.enabled[aiprofile.IdealabQualityV2] = struct{}{}
	managed, err = service.Activate(ctx, ws.ID, owner.ID, managed.ID)
	if err != nil || managed.State != StateActive {
		t.Fatalf("replacement active = %#v, %v", managed, err)
	}
	local, err = service.Get(ctx, ws.ID, local.ID)
	if err != nil || local.State != StateInactive {
		t.Fatalf("prior build not inactive = %#v, %v", local, err)
	}

	local, err = service.Rollback(ctx, ws.ID, owner.ID, local.ID)
	if err != nil || local.State != StateActive {
		t.Fatalf("rollback = %#v, %v", local, err)
	}
	managed, err = service.Get(ctx, ws.ID, managed.ID)
	if err != nil || managed.State != StateInactive {
		t.Fatalf("rolled-back replacement = %#v, %v", managed, err)
	}
	managed, err = service.Discard(ctx, ws.ID, owner.ID, managed.ID)
	if err != nil || managed.State != StateDiscarded || managed.RetentionUntil == nil {
		t.Fatalf("discard = %#v, %v", managed, err)
	}
	var vectorCount int
	if err := database.Pool.QueryRow(ctx, `
		SELECT count(*) FROM index_generation_vectors v
		JOIN index_generations g ON g.id = v.generation_id
		WHERE g.build_id = $1
	`, managed.ID).Scan(&vectorCount); err != nil || vectorCount != 3 {
		t.Fatalf("discarded vectors count = %d, err = %v", vectorCount, err)
	}
	managed, err = service.Rollback(ctx, ws.ID, owner.ID, managed.ID)
	if err != nil || managed.State != StateActive {
		t.Fatalf("recover discarded build inside retention = %#v, %v", managed, err)
	}
	local, err = service.Rollback(ctx, ws.ID, owner.ID, local.ID)
	if err != nil || local.State != StateActive {
		t.Fatalf("replace recovered build with original = %#v, %v", local, err)
	}
	managed, err = service.Discard(ctx, ws.ID, owner.ID, managed.ID)
	if err != nil || managed.RetentionUntil == nil {
		t.Fatalf("discard recovered replacement = %#v, %v", managed, err)
	}
	events, err := service.Events(ctx, ws.ID, local.ID)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if !hasEvent(events, "created") || !hasEvent(events, "ready") ||
		!hasEvent(events, "activated") || !hasEvent(events, "rolled_back") {
		t.Fatalf("local audit events = %#v", events)
	}
	encodedEvents, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedEvents), owner.ID.String()) || !hasActorEvent(events) {
		t.Fatalf("event actor privacy/presence = %s", encodedEvents)
	}
	assertPostgresCode(t, func() error {
		_, insertErr := database.Pool.Exec(ctx, `
			INSERT INTO index_generation_events (
				build_id, workspace_id, event_type, details
			) VALUES ($1,$2,'target_claimed',$3::jsonb)
		`, local.ID, ws.ID, []byte(`{"payload":"`+strings.Repeat("x", 5000)+`"}`))
		return insertErr
	}(), "23514")

	cleanupAt := managed.RetentionUntil.Add(time.Second)
	service.now = func() time.Time { return cleanupAt }
	cleaned, err := service.CleanupExpired(ctx, ws.ID)
	if err != nil || cleaned != 2 {
		t.Fatalf("CleanupExpired() = %d, %v", cleaned, err)
	}
	if _, err := service.Get(ctx, ws.ID, managed.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleaned managed build error = %v", err)
	}
	if _, err := service.Get(ctx, ws.ID, laterLocal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleaned later local build error = %v", err)
	}
}

func insertGenerationTestFile(
	t *testing.T,
	ctx context.Context,
	executor generationTestExecutor,
	ownerID uuid.UUID,
	index int,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	insertGenerationTestFileWithID(t, ctx, executor, ownerID, id, index)
	return id
}

type generationTestExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func insertGenerationTestFileWithID(
	t *testing.T,
	ctx context.Context,
	executor generationTestExecutor,
	ownerID, id uuid.UUID,
	index int,
) {
	t.Helper()
	if _, err := executor.Exec(ctx, `
		INSERT INTO files (
			id, user_id, name, path, size, sha256, mime, storage_key,
			tags, user_tags, source_metadata, processor_metadata, index_status
		) VALUES ($1,$2,$3,'/',1,$4,'text/plain',$5,
		          '{}','{}','{}'::jsonb,'{}'::jsonb,'done')
	`, id, ownerID, fmt.Sprintf("generation-%d.txt", index),
		fmt.Sprintf("%064x", index+1), "generation-test/"+id.String()); err != nil {
		t.Fatalf("insert generation test file: %v", err)
	}
}

func assertPostgresCode(t *testing.T, err error, want string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != want {
		t.Fatalf("PostgreSQL error = %v, want SQLSTATE %s", err, want)
	}
}

func hasEvent(events []Event, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}

func hasActorEvent(events []Event) bool {
	for _, event := range events {
		if event.ActorPresent {
			return true
		}
	}
	return false
}
