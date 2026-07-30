package indexer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/auth"
	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/managedusage"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
	"github.com/PeterGuy326/mem/server/internal/workerpb"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

var errTestSettlementUnavailable = errors.New("test settlement unavailable")

type settlementUnavailableCoordinator struct {
	*managedusage.Service
}

func (*settlementUnavailableCoordinator) SettleUsage(
	context.Context,
	uuid.UUID,
	managedusage.Outcome,
) error {
	return errTestSettlementUnavailable
}

type gatedProfileProbe struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (p *gatedProfileProbe) ProbeEmbedding(
	ctx context.Context,
	_ string,
	dimensions int,
) (int, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.entered != nil {
		p.once.Do(func() { close(p.entered) })
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return dimensions, nil
}

func (p *gatedProfileProbe) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type scriptedProcessorServer struct {
	workerpb.UnimplementedProcessorServiceServer

	mu          sync.Mutex
	responses   []*workerpb.ProcessResponse
	requests    []*workerpb.ProcessRequest
	calls       int
	started     chan struct{}
	release     <-chan struct{}
	startedOnce sync.Once
}

func (s *scriptedProcessorServer) Process(
	ctx context.Context,
	request *workerpb.ProcessRequest,
) (*workerpb.ProcessResponse, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	if request != nil {
		s.requests = append(
			s.requests,
			proto.Clone(request).(*workerpb.ProcessRequest),
		)
	}
	var response *workerpb.ProcessResponse
	if call <= len(s.responses) {
		response = s.responses[call-1]
	}
	s.mu.Unlock()

	if s.started != nil {
		s.startedOnce.Do(func() { close(s.started) })
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if response == nil {
		return nil, fmt.Errorf("unexpected Worker call %d", call)
	}
	return response, nil
}

func (s *scriptedProcessorServer) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *scriptedProcessorServer) LastRequest() *workerpb.ProcessRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil
	}
	return proto.Clone(s.requests[len(s.requests)-1]).(*workerpb.ProcessRequest)
}

type managedAIOutboxFixture struct {
	userID      uuid.UUID
	workspaceID uuid.UUID
	profiles    *aiprofile.Service
}

func TestManagedAISettlementOutboxPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping managed AI settlement outbox test")
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

	definition, ok := aiprofile.Find(aiprofile.IdealabQualityV2)
	if !ok {
		t.Fatal("quality profile missing")
	}

	t.Run("signed persisted Idealab V1 closes both managed stages exactly once", func(t *testing.T) {
		legacy, ok := aiprofile.Find(aiprofile.IdealabQualityV1)
		if !ok {
			t.Fatal("published Idealab V1 profile missing")
		}
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, legacy)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("5", 64),
			"text/plain; charset=utf-8",
		)
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTwoStageResponse(legacy),
			},
		}
		authKeyID := "memd-v1-acceptance"
		authKey := []byte("0123456789abcdef0123456789abcdef")
		workerAddress, verifier := startAuthenticatedScriptedProcessorServer(
			t,
			worker,
			authKeyID,
			authKey,
		)
		entitlementService := entitlement.New(database.Pool, 10*time.Minute)
		usageService := managedusage.New(entitlementService)
		indexService := newManagedAIIndexer(
			database.Pool,
			newAuthenticatedTestWorkerClient(
				t,
				workerAddress,
				authKeyID,
				authKey,
			),
			fixture.profiles,
			&settlementUnavailableCoordinator{Service: usageService},
		)

		indexService.IndexFile(ctx, fileRow)
		if worker.Calls() != 1 || verifier.Calls() != 1 {
			t.Fatalf(
				"Worker/authenticated calls = %d/%d, want 1/1",
				worker.Calls(),
				verifier.Calls(),
			)
		}
		assertPersistedV1RouteAndRequest(
			t,
			ctx,
			fixture.profiles,
			fixture.workspaceID,
			legacy,
			worker.LastRequest(),
		)
		assertCommittedManagedResultPending(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
		)
		assertPendingV1TwoStageOutbox(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			legacy,
		)

		reconciler := New(database.Pool, nil, nil, nil, nil)
		reconciler.SetManagedUsage(usageService)
		settled, err := reconciler.ReconcileManagedUsageSettlements(ctx, 100)
		if err != nil {
			t.Fatalf("reconcile signed V1 settlements: %v", err)
		}
		if settled != 2 {
			t.Fatalf("reconciled signed V1 settlements = %d, want 2", settled)
		}
		settled, err = reconciler.ReconcileManagedUsageSettlements(ctx, 100)
		if err != nil {
			t.Fatalf("reconcile signed V1 settlements twice: %v", err)
		}
		if settled != 0 {
			t.Fatalf("second signed V1 reconciliation = %d, want 0", settled)
		}
		if worker.Calls() != 1 || verifier.Calls() != 1 {
			t.Fatalf(
				"Worker/authenticated calls after reconciliation = %d/%d, want 1/1",
				worker.Calls(),
				verifier.Calls(),
			)
		}
		assertManagedUsageProjection(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			2,
			0,
			0,
			2,
			0,
		)
		assertV1TwoStageUsageClosedExactlyOnce(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
		)
	})

	t.Run("signed managed embedding contract rejection closes accounting atomically", func(t *testing.T) {
		current, ok := aiprofile.Find(aiprofile.IdealabQualityV2)
		if !ok {
			t.Fatal("current managed profile missing")
		}
		tests := []struct {
			name     string
			hashChar string
			mutate   func(*workerpb.ProcessResponse)
		}{
			{
				name:     "provider",
				hashChar: "8",
				mutate: func(response *workerpb.ProcessResponse) {
					response.Embeddings["text"].Provider = "idealab:wrong-model"
				},
			},
			{
				name:     "set dimensions",
				hashChar: "9",
				mutate: func(response *workerpb.ProcessResponse) {
					response.Embeddings["text"].Dim = 767
				},
			},
			{
				name:     "row dimensions",
				hashChar: "a",
				mutate: func(response *workerpb.ProcessResponse) {
					response.Embeddings["text"].Rows[0].Values = make([]float32, 767)
				},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				fixture := newManagedAIOutboxFixture(
					t,
					ctx,
					database.Pool,
					current,
				)
				fileRow := insertManagedAIOutboxFile(
					t,
					ctx,
					database.Pool,
					fixture.userID,
					strings.Repeat(test.hashChar, 64),
					"text/plain",
				)
				response := successfulManagedTwoStageResponse(current)
				response.Caption = "must-not-persist"
				test.mutate(response)
				worker := &scriptedProcessorServer{
					responses: []*workerpb.ProcessResponse{response},
				}
				authKeyID := "memd-contract-rejection"
				authKey := []byte("abcdef0123456789abcdef0123456789")
				workerAddress, verifier := startAuthenticatedScriptedProcessorServer(
					t,
					worker,
					authKeyID,
					authKey,
				)
				entitlementService := entitlement.New(database.Pool, 10*time.Minute)
				usageService := managedusage.New(entitlementService)
				indexService := newManagedAIIndexer(
					database.Pool,
					newAuthenticatedTestWorkerClient(
						t,
						workerAddress,
						authKeyID,
						authKey,
					),
					fixture.profiles,
					usageService,
				)

				indexService.IndexFile(ctx, fileRow)
				if worker.Calls() != 1 || verifier.Calls() != 1 {
					t.Fatalf(
						"rejected Worker/authenticated calls = %d/%d, want 1/1",
						worker.Calls(),
						verifier.Calls(),
					)
				}
				assertProfileResultState(
					t,
					ctx,
					database.Pool,
					fixture.workspaceID,
					fileRow.ID,
					aiprofile.IdealabQualityV2,
					"partial",
					0,
				)
				var businessOutputAbsent bool
				if err := database.Pool.QueryRow(ctx, `
					SELECT caption IS NULL
					       AND processor_metadata = '{}'::jsonb
					  FROM files
					 WHERE id = $1
				`, fileRow.ID).Scan(&businessOutputAbsent); err != nil {
					t.Fatalf("inspect rejected business output: %v", err)
				}
				if !businessOutputAbsent {
					t.Fatal("deterministically rejected response persisted business output")
				}
				assertRejectedManagedResultAccounting(
					t,
					ctx,
					database.Pool,
					fixture.workspaceID,
					fileRow.ID,
					current,
					2,
				)
				reconciler := New(database.Pool, nil, nil, nil, nil)
				reconciler.SetManagedUsage(usageService)
				if settled, err := reconciler.ReconcileManagedUsageSettlements(
					ctx,
					100,
				); err != nil || settled != 0 {
					t.Fatalf(
						"second-pass contract rejection reconciliation = %d/%v, want 0/nil",
						settled,
						err,
					)
				}
			})
		}
	})

	t.Run("committed result resumes without a duplicate Worker call", func(t *testing.T) {
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, definition)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("a", 64),
			"text/plain; charset=utf-8",
		)
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTextResponse(definition),
			},
		}
		workerAddress := startScriptedProcessorServer(t, worker)
		entitlementService := entitlement.New(database.Pool, 10*time.Minute)
		usageService := managedusage.New(entitlementService)

		// The result transaction commits, but every post-commit accounting
		// transition is unavailable. This is the observable state a fresh
		// process must recover after a crash in the commit-to-settle gap.
		firstClient := newTestWorkerClient(t, workerAddress)
		firstIndexer := newManagedAIIndexer(
			database.Pool,
			firstClient,
			fixture.profiles,
			&settlementUnavailableCoordinator{Service: usageService},
		)
		firstIndexer.IndexFile(ctx, fileRow)
		if got := worker.Calls(); got != 1 {
			t.Fatalf("Worker calls after initial indexing = %d, want 1", got)
		}
		assertCommittedManagedResultPending(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
		)

		// A fresh Service and gRPC client exercise the public queue entrypoint.
		// The durable outbox is found before dispatch, settled, and retained as
		// replay proof; the Worker must not observe a second request.
		restartedClient := newTestWorkerClient(t, workerAddress)
		restarted := newManagedAIIndexer(
			database.Pool,
			restartedClient,
			fixture.profiles,
			managedusage.New(entitlementService),
		)
		if err := restarted.IndexFileByID(ctx, fileRow.ID, fixture.userID); err != nil {
			t.Fatalf("resume committed result through IndexFileByID: %v", err)
		}
		if got := worker.Calls(); got != 1 {
			t.Fatalf("Worker calls after settlement recovery = %d, want 1", got)
		}
		assertManagedUsageProjection(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			1,
			1,
			0,
			1,
			0,
		)

		if err := restarted.IndexFileByID(ctx, fileRow.ID, fixture.userID); err != nil {
			t.Fatalf("duplicate queue delivery: %v", err)
		}
		if got := worker.Calls(); got != 1 {
			t.Fatalf("Worker calls after duplicate delivery = %d, want 1", got)
		}
	})

	t.Run("failed not-invoked result settles then retries safely", func(t *testing.T) {
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, definition)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("b", 64),
			"text/plain",
		)
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				failedNotInvokedManagedTextResponse(),
				successfulManagedTextResponse(definition),
			},
		}
		workerAddress := startScriptedProcessorServer(t, worker)
		entitlementService := entitlement.New(database.Pool, 10*time.Minute)
		usageService := managedusage.New(entitlementService)

		firstClient := newTestWorkerClient(t, workerAddress)
		firstIndexer := newManagedAIIndexer(
			database.Pool,
			firstClient,
			fixture.profiles,
			&settlementUnavailableCoordinator{Service: usageService},
		)
		err := firstIndexer.IndexFileByID(ctx, fileRow.ID, fixture.userID)
		if err == nil || !strings.Contains(err.Error(), "index_status=failed") {
			t.Fatalf("failed attempt error = %v, want failed index status", err)
		}
		if got := worker.Calls(); got != 1 {
			t.Fatalf("Worker calls after failed attempt = %d, want 1", got)
		}
		assertRetryableManagedUsagePending(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
		)

		// The next queue delivery first releases both proven-not-invoked
		// reservations, removes the non-replayable outbox identity, derives
		// fresh reservation keys, and then performs exactly one safe retry.
		retryClient := newTestWorkerClient(t, workerAddress)
		retryIndexer := newManagedAIIndexer(
			database.Pool,
			retryClient,
			fixture.profiles,
			managedusage.New(entitlementService),
		)
		if err := retryIndexer.IndexFileByID(ctx, fileRow.ID, fixture.userID); err != nil {
			t.Fatalf("retry failed/not-invoked delivery: %v", err)
		}
		if got := worker.Calls(); got != 2 {
			t.Fatalf("Worker calls after safe retry = %d, want 2", got)
		}
		assertManagedUsageProjection(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			1,
			3,
			0,
			1,
			0,
		)

		if err := retryIndexer.IndexFileByID(ctx, fileRow.ID, fixture.userID); err != nil {
			t.Fatalf("duplicate delivery after safe retry: %v", err)
		}
		if got := worker.Calls(); got != 2 {
			t.Fatalf("Worker calls after retry replay = %d, want 2", got)
		}
	})

	t.Run("uncommitted result blocks stale reconciliation", func(t *testing.T) {
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, definition)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("d", 64),
			"text/plain",
		)
		workerStarted := make(chan struct{})
		releaseWorker := make(chan struct{})
		commitPaused := make(chan struct{})
		releaseCommit := make(chan struct{})
		var releaseWorkerOnce, releaseCommitOnce, commitPausedOnce sync.Once
		t.Cleanup(func() {
			releaseWorkerOnce.Do(func() { close(releaseWorker) })
			releaseCommitOnce.Do(func() { close(releaseCommit) })
		})
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTextResponse(definition),
			},
			started: workerStarted,
			release: releaseWorker,
		}
		workerAddress := startScriptedProcessorServer(t, worker)
		entitlementService := entitlement.New(database.Pool, 10*time.Minute)
		indexService := newManagedAIIndexer(
			database.Pool,
			newTestWorkerClient(t, workerAddress),
			fixture.profiles,
			&settlementUnavailableCoordinator{
				Service: managedusage.New(entitlementService),
			},
		)
		indexService.managedUsageResultCommitHook = func(hookCtx context.Context) error {
			commitPausedOnce.Do(func() { close(commitPaused) })
			select {
			case <-releaseCommit:
				return nil
			case <-hookCtx.Done():
				return hookCtx.Err()
			}
		}

		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			indexService.IndexFile(ctx, fileRow)
		}()
		waitForTestSignal(t, workerStarted, "Worker did not receive the stale-race request")
		if _, err := database.Pool.Exec(ctx, `
			UPDATE managed_embedding_usage
			   SET updated_at = now() - interval '1 hour'
			 WHERE workspace_id = $1
			   AND status = 'reserved'
		`, fixture.workspaceID); err != nil {
			t.Fatalf("make managed usage stale: %v", err)
		}
		releaseWorkerOnce.Do(func() { close(releaseWorker) })
		waitForTestSignal(t, commitPaused, "result transaction did not pause before commit")

		reconcileDone := make(chan struct {
			count int64
			err   error
		}, 1)
		go func() {
			count, reconcileErr := entitlementService.ReconcileStale(ctx)
			reconcileDone <- struct {
				count int64
				err   error
			}{count: count, err: reconcileErr}
		}()
		waitForBlockedPostgresQuery(
			t,
			ctx,
			database.Pool,
			"mem.reconcile-stale-workspaces",
		)
		select {
		case result := <-reconcileDone:
			t.Fatalf(
				"stale reconciliation crossed an uncommitted outbox: count=%d err=%v",
				result.count,
				result.err,
			)
		default:
		}

		releaseCommitOnce.Do(func() { close(releaseCommit) })
		select {
		case result := <-reconcileDone:
			if result.err != nil {
				t.Fatalf("reconcile stale after result commit: %v", result.err)
			}
			if result.count != 0 {
				t.Fatalf("stale reconciled rows after outbox commit = %d, want 0", result.count)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("stale reconciliation did not finish after result commit")
		}
		waitForTestSignal(t, indexDone, "indexing did not finish after result commit")
		assertCommittedManagedResultPending(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
		)
	})

	t.Run("uncommitted result blocks period rollover", func(t *testing.T) {
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, definition)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("e", 64),
			"text/plain",
		)
		workerStarted := make(chan struct{})
		releaseWorker := make(chan struct{})
		commitPaused := make(chan struct{})
		releaseCommit := make(chan struct{})
		var releaseWorkerOnce, releaseCommitOnce, commitPausedOnce sync.Once
		t.Cleanup(func() {
			releaseWorkerOnce.Do(func() { close(releaseWorker) })
			releaseCommitOnce.Do(func() { close(releaseCommit) })
		})
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTextResponse(definition),
			},
			started: workerStarted,
			release: releaseWorker,
		}
		workerAddress := startScriptedProcessorServer(t, worker)
		entitlementService := entitlement.New(database.Pool, 10*time.Minute)
		indexService := newManagedAIIndexer(
			database.Pool,
			newTestWorkerClient(t, workerAddress),
			fixture.profiles,
			&settlementUnavailableCoordinator{
				Service: managedusage.New(entitlementService),
			},
		)
		indexService.managedUsageResultCommitHook = func(hookCtx context.Context) error {
			commitPausedOnce.Do(func() { close(commitPaused) })
			select {
			case <-releaseCommit:
				return nil
			case <-hookCtx.Done():
				return hookCtx.Err()
			}
		}

		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			indexService.IndexFile(ctx, fileRow)
		}()
		waitForTestSignal(t, workerStarted, "Worker did not receive the rollover-race request")

		expiredStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
		expiredEnd := expiredStart.Add(time.Hour)
		expireTx, err := database.Pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin period-expiration transaction: %v", err)
		}
		if _, err := expireTx.Exec(ctx, `
			UPDATE workspace_entitlements
			   SET period_start = $2,
			       period_end = $3,
			       updated_at = now()
			 WHERE workspace_id = $1
		`, fixture.workspaceID, expiredStart, expiredEnd); err != nil {
			_ = expireTx.Rollback(ctx)
			t.Fatalf("expire workspace entitlement: %v", err)
		}
		if _, err := expireTx.Exec(ctx, `
			UPDATE managed_embedding_usage
			   SET period_start = $2,
			       period_end = $3,
			       updated_at = now()
			 WHERE workspace_id = $1
			   AND status = 'reserved'
		`, fixture.workspaceID, expiredStart, expiredEnd); err != nil {
			_ = expireTx.Rollback(ctx)
			t.Fatalf("expire managed usage period: %v", err)
		}
		if err := expireTx.Commit(ctx); err != nil {
			t.Fatalf("commit period expiration: %v", err)
		}

		releaseWorkerOnce.Do(func() { close(releaseWorker) })
		waitForTestSignal(t, commitPaused, "result transaction did not pause before commit")

		summaryDone := make(chan error, 1)
		go func() {
			_, summaryErr := entitlementService.Summary(ctx, fixture.workspaceID)
			summaryDone <- summaryErr
		}()
		waitForBlockedPostgresQuery(
			t,
			ctx,
			database.Pool,
			"mem.prepare-entitlement",
		)
		select {
		case summaryErr := <-summaryDone:
			t.Fatalf("period rollover crossed an uncommitted outbox: %v", summaryErr)
		default:
		}

		releaseCommitOnce.Do(func() { close(releaseCommit) })
		select {
		case summaryErr := <-summaryDone:
			if !errors.Is(summaryErr, entitlement.ErrEntitlementUnavailable) ||
				!errors.Is(summaryErr, entitlement.ErrSettlementPending) {
				t.Fatalf(
					"Summary() error = %v, want entitlement unavailable and settlement pending",
					summaryErr,
				)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("period rollover did not finish after result commit")
		}
		waitForTestSignal(t, indexDone, "indexing did not finish after result commit")
		assertCommittedManagedResultPending(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
		)

		var gotStart, gotEnd time.Time
		if err := database.Pool.QueryRow(ctx, `
			SELECT period_start, period_end
			  FROM workspace_entitlements
			 WHERE workspace_id = $1
		`, fixture.workspaceID).Scan(&gotStart, &gotEnd); err != nil {
			t.Fatalf("inspect blocked period rollover: %v", err)
		}
		if !gotStart.Equal(expiredStart) || !gotEnd.Equal(expiredEnd) {
			t.Fatalf(
				"period changed across pending result: got %s..%s, want %s..%s",
				gotStart,
				gotEnd,
				expiredStart,
				expiredEnd,
			)
		}
	})

	t.Run("completed period rollover rejects a late result", func(t *testing.T) {
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, definition)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("f", 64),
			"text/plain",
		)
		workerStarted := make(chan struct{})
		releaseWorker := make(chan struct{})
		var releaseWorkerOnce sync.Once
		t.Cleanup(func() {
			releaseWorkerOnce.Do(func() { close(releaseWorker) })
		})
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTextResponse(definition),
			},
			started: workerStarted,
			release: releaseWorker,
		}
		workerAddress := startScriptedProcessorServer(t, worker)
		entitlementService := entitlement.New(database.Pool, 10*time.Minute)
		indexService := newManagedAIIndexer(
			database.Pool,
			newTestWorkerClient(t, workerAddress),
			fixture.profiles,
			&settlementUnavailableCoordinator{
				Service: managedusage.New(entitlementService),
			},
		)

		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			indexService.IndexFile(ctx, fileRow)
		}()
		waitForTestSignal(t, workerStarted, "Worker did not receive the late-result request")

		expiredStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
		expiredEnd := expiredStart.Add(time.Hour)
		expireTx, err := database.Pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin winning-rollover transaction: %v", err)
		}
		if _, err := expireTx.Exec(ctx, `
			UPDATE workspace_entitlements
			   SET period_start = $2,
			       period_end = $3,
			       updated_at = now()
			 WHERE workspace_id = $1
		`, fixture.workspaceID, expiredStart, expiredEnd); err != nil {
			_ = expireTx.Rollback(ctx)
			t.Fatalf("expire entitlement before rollover: %v", err)
		}
		if _, err := expireTx.Exec(ctx, `
			UPDATE managed_embedding_usage
			   SET period_start = $2,
			       period_end = $3,
			       updated_at = now()
			 WHERE workspace_id = $1
			   AND status = 'reserved'
		`, fixture.workspaceID, expiredStart, expiredEnd); err != nil {
			_ = expireTx.Rollback(ctx)
			t.Fatalf("expire reservations before rollover: %v", err)
		}
		if err := expireTx.Commit(ctx); err != nil {
			t.Fatalf("commit winning-rollover setup: %v", err)
		}
		if _, err := entitlementService.Summary(ctx, fixture.workspaceID); err != nil {
			t.Fatalf("complete period rollover before result: %v", err)
		}

		releaseWorkerOnce.Do(func() { close(releaseWorker) })
		waitForTestSignal(t, indexDone, "late result did not finish after period rollover")

		var indexStatus string
		var outboxRows, embeddings, indeterminate int
		if err := database.Pool.QueryRow(ctx, `
			SELECT
			    (
			        SELECT index_status
			          FROM files
			         WHERE id = $1
			    ),
			    (
			        SELECT count(*)
			          FROM managed_ai_stage_settlement_outbox
			         WHERE file_id = $1
			    ),
			    (
			        SELECT count(*)
			          FROM embeddings_text
			         WHERE file_id = $1
			    ),
			    (
			        SELECT count(*)
			          FROM managed_embedding_usage
			         WHERE workspace_id = $2
			           AND status = 'indeterminate'
			    )
		`, fileRow.ID, fixture.workspaceID).Scan(
			&indexStatus,
			&outboxRows,
			&embeddings,
			&indeterminate,
		); err != nil {
			t.Fatalf("inspect rejected late result: %v", err)
		}
		if indexStatus != "partial" ||
			outboxRows != 0 ||
			embeddings != 0 ||
			indeterminate != 2 {
			t.Fatalf(
				"late result status/outbox/embeddings/indeterminate = %s/%d/%d/%d, "+
					"want partial/0/0/2",
				indexStatus,
				outboxRows,
				embeddings,
				indeterminate,
			)
		}
		if got := worker.Calls(); got != 1 {
			t.Fatalf("Worker calls for rejected late result = %d, want 1", got)
		}
	})

	t.Run("V1 result commit makes concurrent V2 selection fail", func(t *testing.T) {
		legacy, ok := aiprofile.Find(aiprofile.LocalFastV1)
		if !ok {
			t.Fatal("legacy local profile missing")
		}
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, legacy)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("1", 64),
			"text/plain",
		)
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTextResponse(legacy),
			},
		}
		indexService := newManagedAIIndexer(
			database.Pool,
			newTestWorkerClient(t, startScriptedProcessorServer(t, worker)),
			fixture.profiles,
			nil,
		)
		commitPaused := make(chan struct{})
		releaseCommit := make(chan struct{})
		var pausedOnce, releaseOnce sync.Once
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(releaseCommit) })
		})
		indexService.aiProfileResultCommitHook = func(hookCtx context.Context) error {
			pausedOnce.Do(func() { close(commitPaused) })
			select {
			case <-releaseCommit:
				return nil
			case <-hookCtx.Done():
				return hookCtx.Err()
			}
		}

		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			indexService.IndexFile(ctx, fileRow)
		}()
		waitForTestSignal(
			t,
			commitPaused,
			"V1 result transaction did not pause before commit",
		)

		probe := &gatedProfileProbe{}
		profiles := aiprofile.New(database.Pool, probe, aiprofile.LocalFastV2)
		selectDone := make(chan error, 1)
		go func() {
			_, selectErr := profiles.Select(
				ctx,
				fixture.workspaceID,
				fixture.userID,
				aiprofile.LocalFastV2,
			)
			selectDone <- selectErr
		}()
		waitForBlockedPostgresQuery(
			t,
			ctx,
			database.Pool,
			"mem.ai-profile-workspace",
		)
		select {
		case selectErr := <-selectDone:
			t.Fatalf("V2 selection crossed uncommitted V1 result: %v", selectErr)
		default:
		}

		releaseOnce.Do(func() { close(releaseCommit) })
		waitForTestSignal(t, indexDone, "V1 result did not commit after release")
		select {
		case selectErr := <-selectDone:
			if !errors.Is(selectErr, aiprofile.ErrProfileCorpusMismatch) {
				t.Fatalf(
					"Select(V2) after V1 commit error = %v, want corpus mismatch",
					selectErr,
				)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("V2 selection did not finish after V1 result commit")
		}
		if probe.Calls() != 0 {
			t.Fatalf("blocked V2 selection made %d probes", probe.Calls())
		}
		assertProfileResultState(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			aiprofile.LocalFastV1,
			"done",
			1,
		)
	})

	t.Run("V2 selection commit rejects a concurrent V1 result", func(t *testing.T) {
		legacy, ok := aiprofile.Find(aiprofile.LocalFastV1)
		if !ok {
			t.Fatal("legacy local profile missing")
		}
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, legacy)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("2", 64),
			"text/plain",
		)
		if _, err := database.Pool.Exec(ctx, `
			UPDATE files
			   SET index_status = 'done'
			 WHERE id = $1
		`, fileRow.ID); err != nil {
			t.Fatalf("prepare empty V1 workspace for selection: %v", err)
		}
		fileRow.IndexStatus = "done"

		probeEntered := make(chan struct{})
		releaseProbe := make(chan struct{})
		var releaseProbeOnce sync.Once
		t.Cleanup(func() {
			releaseProbeOnce.Do(func() { close(releaseProbe) })
		})
		probe := &gatedProfileProbe{
			entered: probeEntered,
			release: releaseProbe,
		}
		profiles := aiprofile.New(database.Pool, probe, aiprofile.LocalFastV2)
		selectDone := make(chan error, 1)
		go func() {
			_, selectErr := profiles.Select(
				ctx,
				fixture.workspaceID,
				fixture.userID,
				aiprofile.LocalFastV2,
			)
			selectDone <- selectErr
		}()
		waitForTestSignal(
			t,
			probeEntered,
			"V2 selection did not reach the probe while holding its DB lock",
		)

		workerStarted := make(chan struct{})
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTextResponse(legacy),
			},
			started: workerStarted,
		}
		indexService := newManagedAIIndexer(
			database.Pool,
			newTestWorkerClient(t, startScriptedProcessorServer(t, worker)),
			fixture.profiles,
			nil,
		)
		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			indexService.IndexFile(ctx, fileRow)
		}()
		waitForTestSignal(
			t,
			workerStarted,
			"concurrent V1 indexing did not reach the Worker",
		)
		waitForBlockedPostgresQuery(
			t,
			ctx,
			database.Pool,
			"mem.ai-profile-workspace",
		)
		select {
		case <-indexDone:
			t.Fatal("V1 result crossed an uncommitted V2 selection")
		default:
		}

		releaseProbeOnce.Do(func() { close(releaseProbe) })
		select {
		case selectErr := <-selectDone:
			if selectErr != nil {
				t.Fatalf("commit V2 selection before V1 result: %v", selectErr)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("V2 selection did not finish after releasing the probe")
		}
		waitForTestSignal(
			t,
			indexDone,
			"stale V1 result did not finish after V2 selection commit",
		)
		if worker.Calls() != 1 {
			t.Fatalf("concurrent V1 Worker calls = %d, want 1", worker.Calls())
		}
		assertProfileResultState(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			aiprofile.LocalFastV2,
			"partial",
			0,
		)
	})

	t.Run("managed V1 result commit makes concurrent managed V2 selection fail", func(t *testing.T) {
		legacy, ok := aiprofile.Find(aiprofile.IdealabQualityV1)
		if !ok {
			t.Fatal("published managed V1 profile missing")
		}
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, legacy)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("6", 64),
			"text/plain",
		)
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTwoStageResponse(legacy),
			},
		}
		entitlementService := entitlement.New(database.Pool, 10*time.Minute)
		usageService := managedusage.New(entitlementService)
		indexService := newManagedAIIndexer(
			database.Pool,
			newTestWorkerClient(t, startScriptedProcessorServer(t, worker)),
			fixture.profiles,
			usageService,
		)
		commitPaused := make(chan struct{})
		releaseCommit := make(chan struct{})
		var pausedOnce, releaseOnce sync.Once
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(releaseCommit) })
		})
		indexService.aiProfileResultCommitHook = func(hookCtx context.Context) error {
			pausedOnce.Do(func() { close(commitPaused) })
			select {
			case <-releaseCommit:
				return nil
			case <-hookCtx.Done():
				return hookCtx.Err()
			}
		}

		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			indexService.IndexFile(ctx, fileRow)
		}()
		waitForTestSignal(
			t,
			commitPaused,
			"managed V1 result transaction did not pause before commit",
		)

		probe := &gatedProfileProbe{}
		profiles := aiprofile.New(
			database.Pool,
			probe,
			aiprofile.IdealabQualityV2,
		)
		profiles.SetManagedProbeUsage(usageService)
		selectDone := make(chan error, 1)
		go func() {
			_, selectErr := profiles.Select(
				ctx,
				fixture.workspaceID,
				fixture.userID,
				aiprofile.IdealabQualityV2,
			)
			selectDone <- selectErr
		}()
		waitForBlockedPostgresQuery(
			t,
			ctx,
			database.Pool,
			"mem.ai-profile-workspace",
		)
		select {
		case selectErr := <-selectDone:
			t.Fatalf(
				"managed V2 selection crossed an uncommitted managed V1 result: %v",
				selectErr,
			)
		default:
		}

		releaseOnce.Do(func() { close(releaseCommit) })
		waitForTestSignal(t, indexDone, "managed V1 result did not commit after release")
		select {
		case selectErr := <-selectDone:
			if !errors.Is(selectErr, aiprofile.ErrProfileCorpusMismatch) {
				t.Fatalf(
					"managed V2 Select after V1 result error = %v, want corpus mismatch",
					selectErr,
				)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("managed V2 selection did not finish after V1 result commit")
		}
		if probe.Calls() != 0 || worker.Calls() != 1 {
			t.Fatalf(
				"managed result-wins probe/Worker calls = %d/%d, want 0/1",
				probe.Calls(),
				worker.Calls(),
			)
		}
		assertProfileResultState(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			aiprofile.IdealabQualityV1,
			"done",
			1,
		)
		assertManagedUsageProjection(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			2,
			0,
			0,
			2,
			0,
		)
		reconciler := New(database.Pool, nil, nil, nil, nil)
		reconciler.SetManagedUsage(usageService)
		if settled, err := reconciler.ReconcileManagedUsageSettlements(
			ctx,
			100,
		); err != nil || settled != 0 {
			t.Fatalf(
				"second-pass managed result-wins reconciliation = %d/%v, want 0/nil",
				settled,
				err,
			)
		}
	})

	t.Run("managed V2 selection preserves closed accounting for rejected managed V1 result", func(t *testing.T) {
		legacy, ok := aiprofile.Find(aiprofile.IdealabQualityV1)
		if !ok {
			t.Fatal("published managed V1 profile missing")
		}
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, legacy)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("7", 64),
			"text/plain",
		)
		if _, err := database.Pool.Exec(ctx, `
			UPDATE files
			   SET index_status = 'done'
			 WHERE id = $1
		`, fileRow.ID); err != nil {
			t.Fatalf("prepare empty managed V1 workspace for selection: %v", err)
		}
		fileRow.IndexStatus = "done"

		entitlementService := entitlement.New(database.Pool, 10*time.Minute)
		usageService := managedusage.New(entitlementService)
		probeEntered := make(chan struct{})
		releaseProbe := make(chan struct{})
		var releaseProbeOnce sync.Once
		t.Cleanup(func() {
			releaseProbeOnce.Do(func() { close(releaseProbe) })
		})
		probe := &gatedProfileProbe{
			entered: probeEntered,
			release: releaseProbe,
		}
		profiles := aiprofile.New(
			database.Pool,
			probe,
			aiprofile.IdealabQualityV2,
		)
		profiles.SetManagedProbeUsage(usageService)
		selectDone := make(chan error, 1)
		go func() {
			_, selectErr := profiles.Select(
				ctx,
				fixture.workspaceID,
				fixture.userID,
				aiprofile.IdealabQualityV2,
			)
			selectDone <- selectErr
		}()
		waitForTestSignal(
			t,
			probeEntered,
			"managed V2 selection did not hold its DB lock in the probe",
		)

		workerStarted := make(chan struct{})
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTwoStageResponse(legacy),
			},
			started: workerStarted,
		}
		indexService := newManagedAIIndexer(
			database.Pool,
			newTestWorkerClient(t, startScriptedProcessorServer(t, worker)),
			fixture.profiles,
			usageService,
		)
		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			indexService.IndexFile(ctx, fileRow)
		}()
		waitForTestSignal(
			t,
			workerStarted,
			"concurrent managed V1 indexing did not reach the Worker",
		)
		waitForBlockedPostgresQuery(
			t,
			ctx,
			database.Pool,
			"mem.ai-profile-workspace",
		)
		select {
		case <-indexDone:
			t.Fatal("managed V1 result crossed an uncommitted managed V2 selection")
		default:
		}

		releaseProbeOnce.Do(func() { close(releaseProbe) })
		select {
		case selectErr := <-selectDone:
			if selectErr != nil {
				t.Fatalf("commit managed V2 selection before V1 result: %v", selectErr)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("managed V2 selection did not finish after releasing the probe")
		}
		waitForTestSignal(
			t,
			indexDone,
			"rejected managed V1 result did not close accounting",
		)
		if worker.Calls() != 1 {
			t.Fatalf("rejected managed V1 Worker calls = %d, want 1", worker.Calls())
		}
		assertProfileResultState(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			aiprofile.IdealabQualityV2,
			"partial",
			0,
		)
		assertRejectedManagedResultAccounting(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			legacy,
			3,
		)
		reconciler := New(database.Pool, nil, nil, nil, nil)
		reconciler.SetManagedUsage(usageService)
		if settled, err := reconciler.ReconcileManagedUsageSettlements(
			ctx,
			100,
		); err != nil || settled != 0 {
			t.Fatalf(
				"second-pass rejected managed reconciliation = %d/%v, want 0/nil",
				settled,
				err,
			)
		}
	})

	t.Run("first profile selection rejects a concurrent legacy result", func(t *testing.T) {
		current, ok := aiprofile.Find(aiprofile.LocalFastV2)
		if !ok {
			t.Fatal("current local profile missing")
		}
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, current)
		if _, err := database.Pool.Exec(ctx, `
			DELETE FROM workspace_ai_profiles
			 WHERE workspace_id = $1
		`, fixture.workspaceID); err != nil {
			t.Fatalf("remove initial profile for legacy route: %v", err)
		}
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("3", 64),
			"text/plain",
		)
		if _, err := database.Pool.Exec(ctx, `
			UPDATE files
			   SET index_status = 'done'
			 WHERE id = $1
		`, fileRow.ID); err != nil {
			t.Fatalf("prepare empty legacy workspace for selection: %v", err)
		}
		fileRow.IndexStatus = "done"

		probeEntered := make(chan struct{})
		releaseProbe := make(chan struct{})
		var releaseProbeOnce sync.Once
		t.Cleanup(func() {
			releaseProbeOnce.Do(func() { close(releaseProbe) })
		})
		probe := &gatedProfileProbe{
			entered: probeEntered,
			release: releaseProbe,
		}
		profiles := aiprofile.New(database.Pool, probe, aiprofile.LocalFastV2)
		selectDone := make(chan error, 1)
		go func() {
			_, selectErr := profiles.Select(
				ctx,
				fixture.workspaceID,
				fixture.userID,
				aiprofile.LocalFastV2,
			)
			selectDone <- selectErr
		}()
		waitForTestSignal(
			t,
			probeEntered,
			"first profile selection did not hold its DB lock in the probe",
		)

		workerStarted := make(chan struct{})
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTextResponse(current),
			},
			started: workerStarted,
		}
		indexService := newLegacyProfileAwareIndexer(
			database.Pool,
			newTestWorkerClient(t, startScriptedProcessorServer(t, worker)),
			aiprofile.New(database.Pool, nil, aiprofile.LocalFastV2),
		)
		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			indexService.IndexFile(ctx, fileRow)
		}()
		waitForTestSignal(
			t,
			workerStarted,
			"legacy indexing did not reach the Worker",
		)
		waitForBlockedPostgresQuery(
			t,
			ctx,
			database.Pool,
			"mem.ai-profile-workspace",
		)
		select {
		case <-indexDone:
			t.Fatal("legacy result crossed an uncommitted first profile selection")
		default:
		}

		releaseProbeOnce.Do(func() { close(releaseProbe) })
		select {
		case selectErr := <-selectDone:
			if selectErr != nil {
				t.Fatalf("commit first profile selection: %v", selectErr)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("first profile selection did not finish after releasing the probe")
		}
		waitForTestSignal(
			t,
			indexDone,
			"stale legacy result did not finish after profile selection commit",
		)
		if worker.Calls() != 1 {
			t.Fatalf("concurrent legacy Worker calls = %d, want 1", worker.Calls())
		}
		assertProfileResultState(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			aiprofile.LocalFastV2,
			"partial",
			0,
		)
	})

	t.Run("legacy result commit makes first profile selection fail closed", func(t *testing.T) {
		current, ok := aiprofile.Find(aiprofile.LocalFastV2)
		if !ok {
			t.Fatal("current local profile missing")
		}
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, current)
		if _, err := database.Pool.Exec(ctx, `
			DELETE FROM workspace_ai_profiles
			 WHERE workspace_id = $1
		`, fixture.workspaceID); err != nil {
			t.Fatalf("remove initial profile for legacy route: %v", err)
		}
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("4", 64),
			"text/plain",
		)
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTextResponse(current),
			},
		}
		indexService := newLegacyProfileAwareIndexer(
			database.Pool,
			newTestWorkerClient(t, startScriptedProcessorServer(t, worker)),
			aiprofile.New(database.Pool, nil, aiprofile.LocalFastV2),
		)
		commitPaused := make(chan struct{})
		releaseCommit := make(chan struct{})
		var pausedOnce, releaseOnce sync.Once
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(releaseCommit) })
		})
		indexService.aiProfileResultCommitHook = func(hookCtx context.Context) error {
			pausedOnce.Do(func() { close(commitPaused) })
			select {
			case <-releaseCommit:
				return nil
			case <-hookCtx.Done():
				return hookCtx.Err()
			}
		}

		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			indexService.IndexFile(ctx, fileRow)
		}()
		waitForTestSignal(
			t,
			commitPaused,
			"legacy result transaction did not pause before commit",
		)

		probe := &gatedProfileProbe{}
		profiles := aiprofile.New(database.Pool, probe, aiprofile.LocalFastV2)
		selectDone := make(chan error, 1)
		go func() {
			_, selectErr := profiles.Select(
				ctx,
				fixture.workspaceID,
				fixture.userID,
				aiprofile.LocalFastV2,
			)
			selectDone <- selectErr
		}()
		waitForBlockedPostgresQuery(
			t,
			ctx,
			database.Pool,
			"mem.ai-profile-workspace",
		)
		select {
		case selectErr := <-selectDone:
			t.Fatalf("first profile selection crossed an uncommitted legacy result: %v", selectErr)
		default:
		}

		releaseOnce.Do(func() { close(releaseCommit) })
		waitForTestSignal(t, indexDone, "legacy result did not commit after release")
		select {
		case selectErr := <-selectDone:
			if !errors.Is(selectErr, aiprofile.ErrProfileCorpusIdentityUnknown) {
				t.Fatalf(
					"Select after legacy result error = %v, want corpus identity unknown",
					selectErr,
				)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("first profile selection did not finish after legacy result commit")
		}
		if probe.Calls() != 0 {
			t.Fatalf("blocked first profile selection made %d probes", probe.Calls())
		}
		assertLegacyResultState(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			fileRow.ID,
			"done",
			1,
		)
	})

	t.Run("deleting a file while the Worker is in flight detaches settlement intent", func(t *testing.T) {
		fixture := newManagedAIOutboxFixture(t, ctx, database.Pool, definition)
		fileRow := insertManagedAIOutboxFile(
			t,
			ctx,
			database.Pool,
			fixture.userID,
			strings.Repeat("c", 64),
			"text/plain",
		)
		workerStarted := make(chan struct{})
		releaseWorker := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(releaseWorker) })
		})
		worker := &scriptedProcessorServer{
			responses: []*workerpb.ProcessResponse{
				successfulManagedTextResponse(definition),
			},
			started: workerStarted,
			release: releaseWorker,
		}
		workerAddress := startScriptedProcessorServer(t, worker)
		entitlementService := entitlement.New(database.Pool, 10*time.Minute)
		firstClient := newTestWorkerClient(t, workerAddress)
		firstIndexer := newManagedAIIndexer(
			database.Pool,
			firstClient,
			fixture.profiles,
			&settlementUnavailableCoordinator{
				Service: managedusage.New(entitlementService),
			},
		)
		indexDone := make(chan struct{})
		go func() {
			defer close(indexDone)
			firstIndexer.IndexFile(ctx, fileRow)
		}()
		select {
		case <-workerStarted:
		case <-time.After(10 * time.Second):
			t.Fatal("Worker did not receive the indexing request")
		}

		if _, err := database.Pool.Exec(
			ctx,
			`DELETE FROM files WHERE id = $1`,
			fileRow.ID,
		); err != nil {
			t.Fatalf("delete file while Worker is in flight: %v", err)
		}
		releaseOnce.Do(func() { close(releaseWorker) })
		select {
		case <-indexDone:
		case <-time.After(10 * time.Second):
			t.Fatal("indexing did not finish after releasing the Worker")
		}
		if got := worker.Calls(); got != 1 {
			t.Fatalf("Worker calls after in-flight deletion = %d, want 1", got)
		}

		usageIDs := managedUsageIDsForWorkspace(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
		)
		if len(usageIDs) != 2 {
			t.Fatalf("reserved usage IDs = %d, want 2", len(usageIDs))
		}
		var retained, pending, detached, scrubbed, succeeded, notInvoked, nonReplayable int
		if err := database.Pool.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE settled_at IS NULL),
			       count(*) FILTER (WHERE file_id IS NULL),
			       count(*) FILTER (WHERE content_sha256 IS NULL),
			       count(*) FILTER (WHERE outcome = 'succeeded'),
			       count(*) FILTER (WHERE outcome = 'not_invoked'),
			       count(*) FILTER (WHERE NOT replayable)
			  FROM managed_ai_stage_settlement_outbox
			 WHERE usage_id = ANY($1::uuid[])
		`, usageIDs).Scan(
			&retained,
			&pending,
			&detached,
			&scrubbed,
			&succeeded,
			&notInvoked,
			&nonReplayable,
		); err != nil {
			t.Fatalf("inspect detached outbox: %v", err)
		}
		if retained != 2 ||
			pending != 2 ||
			detached != 2 ||
			scrubbed != 2 ||
			succeeded != 1 ||
			notInvoked != 1 ||
			nonReplayable != 2 {
			t.Fatalf(
				"retained/pending/detached/scrubbed/succeeded/not-invoked/non-replayable "+
					"outbox = %d/%d/%d/%d/%d/%d/%d, want 2/2/2/2/1/1/2",
				retained,
				pending,
				detached,
				scrubbed,
				succeeded,
				notInvoked,
				nonReplayable,
			)
		}

		reconciler := New(database.Pool, nil, nil, nil, nil)
		reconciler.SetManagedUsage(managedusage.New(entitlementService))
		if _, err := reconciler.ReconcileManagedUsageSettlements(ctx, 100); err != nil {
			t.Fatalf("reconcile detached settlement intent: %v", err)
		}
		assertDetachedSettlements(
			t,
			ctx,
			database.Pool,
			fixture.workspaceID,
			usageIDs,
		)
	})
}

func newManagedAIOutboxFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	definition aiprofile.Definition,
) managedAIOutboxFixture {
	t.Helper()
	user, err := auth.New(pool).CreateUser(
		ctx,
		fmt.Sprintf("managed-ai-outbox-%s@example.test", uuid.NewString()),
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
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			15*time.Second,
		)
		defer cleanupCancel()
		if _, err := pool.Exec(
			cleanupCtx,
			`DELETE FROM users WHERE id = $1`,
			user.ID,
		); err != nil {
			t.Errorf("clean up managed AI outbox tenant: %v", err)
		}
	})

	if _, err := pool.Exec(ctx, `
		UPDATE workspace_entitlements
		   SET plan_key = 'pro',
		       status = 'active',
		       period_start = now() - interval '1 minute',
		       period_end = now() + interval '1 hour',
		       managed_embedding_unit_limit = 10,
		       managed_embedding_units_reserved = 0,
		       managed_embedding_units_consumed = 0,
		       updated_at = now()
		 WHERE workspace_id = $1
	`, ws.ID); err != nil {
		t.Fatalf("activate test entitlement: %v", err)
	}

	selectedAt := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_ai_profiles (
		    workspace_id,
		    profile_id,
		    profile_revision,
		    pipeline_revision,
		    embedding_provider,
		    embedding_dimensions,
		    visual_embedding_provider,
		    visual_embedding_dimensions,
		    llm_provider,
		    vlm_provider,
		    asr_provider,
		    rerank_provider,
		    data_egress,
		    allowed_mime_types,
		    selected_by_user_id,
		    selected_at,
		    updated_at
		) VALUES (
		    $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16
		)
	`,
		ws.ID,
		definition.ID,
		definition.Revision,
		definition.PipelineRevision,
		definition.Embedding.Provider,
		definition.Embedding.Dimensions,
		nullableProfileProvider(definition.VisualEmbedding),
		nullableProfileDimensions(definition.VisualEmbedding),
		nullableProfileProvider(definition.LLM),
		nullableProfileProvider(definition.VLM),
		nullableProfileProvider(definition.ASR),
		nullableProfileProvider(definition.Rerank),
		definition.DataEgress,
		definition.AllowedMIMETypes,
		user.ID,
		selectedAt,
	); err != nil {
		t.Fatalf("insert workspace AI profile: %v", err)
	}

	return managedAIOutboxFixture{
		userID:      user.ID,
		workspaceID: ws.ID,
		profiles:    aiprofile.New(pool, nil, definition.ID),
	}
}

func nullableProfileProvider(stage aiprofile.Stage) any {
	if !stage.Enabled {
		return nil
	}
	return stage.Provider
}

func nullableProfileDimensions(stage aiprofile.Stage) any {
	if !stage.Enabled {
		return nil
	}
	return stage.Dimensions
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal(failure)
	}
}

func waitForBlockedPostgresQuery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryMarker string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1
			      FROM pg_stat_activity
			     WHERE datname = current_database()
			       AND pid <> pg_backend_pid()
			       AND state = 'active'
			       AND wait_event_type = 'Lock'
			       AND query LIKE '%' || $1 || '%'
			)
		`, queryMarker).Scan(&blocked); err != nil {
			t.Fatalf("inspect PostgreSQL lock wait for %q: %v", queryMarker, err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for PostgreSQL lock %q: %v", queryMarker, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("query %q did not block before result commit", queryMarker)
}

func insertManagedAIOutboxFile(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	userID uuid.UUID,
	contentSHA256, mimeType string,
) *file.File {
	t.Helper()
	fileID := uuid.New()
	storageKey := "managed-ai-outbox/" + fileID.String()
	if _, err := pool.Exec(ctx, `
		INSERT INTO files (
		    id, user_id, name, path, size, sha256, mime, storage_key,
		    index_status
		) VALUES ($1,$2,'outbox.txt','/',12,$3,$4,$5,'pending')
	`,
		fileID,
		userID,
		contentSHA256,
		mimeType,
		storageKey,
	); err != nil {
		t.Fatalf("insert test file: %v", err)
	}
	return &file.File{
		ID:          fileID,
		UserID:      userID,
		Name:        "outbox.txt",
		Size:        12,
		SHA256:      contentSHA256,
		MIME:        mimeType,
		StorageKey:  storageKey,
		IndexStatus: "pending",
	}
}

func startScriptedProcessorServer(
	t *testing.T,
	processor *scriptedProcessorServer,
) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for test Worker: %v", err)
	}
	server := grpc.NewServer()
	workerpb.RegisterProcessorServiceServer(server, processor)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case err := <-serveDone:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				t.Errorf("serve test Worker: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("test Worker did not stop")
		}
	})
	return listener.Addr().String()
}

type testWorkerAuthVerifier struct {
	mu       sync.Mutex
	verified int
}

func (v *testWorkerAuthVerifier) Calls() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.verified
}

func startAuthenticatedScriptedProcessorServer(
	t *testing.T,
	processor *scriptedProcessorServer,
	keyID string,
	key []byte,
) (string, *testWorkerAuthVerifier) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for authenticated test Worker: %v", err)
	}
	verifier := &testWorkerAuthVerifier{}
	server := grpc.NewServer(grpc.UnaryInterceptor(
		func(
			ctx context.Context,
			request any,
			info *grpc.UnaryServerInfo,
			handler grpc.UnaryHandler,
		) (any, error) {
			message, ok := request.(proto.Message)
			if !ok {
				return nil, errors.New("authenticated test Worker request is not protobuf")
			}
			nonce, scope, err := verifyTestWorkerRequest(
				ctx,
				info.FullMethod,
				message,
				keyID,
				key,
			)
			if err != nil {
				return nil, err
			}
			response, err := handler(ctx, request)
			if err != nil {
				return nil, err
			}
			responseMessage, ok := response.(proto.Message)
			if !ok {
				return nil, errors.New("authenticated test Worker response is not protobuf")
			}
			trailer, err := signTestWorkerResponse(
				info.FullMethod,
				scope,
				keyID,
				nonce,
				key,
				responseMessage,
			)
			if err != nil {
				return nil, err
			}
			if err := grpc.SetTrailer(ctx, trailer); err != nil {
				return nil, fmt.Errorf("set authenticated test Worker trailer: %w", err)
			}
			verifier.mu.Lock()
			verifier.verified++
			verifier.mu.Unlock()
			return response, nil
		},
	))
	workerpb.RegisterProcessorServiceServer(server, processor)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case err := <-serveDone:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				t.Errorf("serve authenticated test Worker: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("authenticated test Worker did not stop")
		}
	})
	return listener.Addr().String(), verifier
}

const (
	testWorkerRequestAuthContract  = "mem.worker.hmac/v1"
	testWorkerResponseAuthContract = "mem.worker.response-hmac/v1"
	testWorkerRequestAuthDomain    = "mem.worker.request-auth/v1"
	testWorkerResponseAuthDomain   = "mem.worker.response-auth/v1"
)

func verifyTestWorkerRequest(
	ctx context.Context,
	method string,
	message proto.Message,
	wantKeyID string,
	key []byte,
) (string, string, error) {
	incoming, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", "", errors.New("authenticated test Worker request has no metadata")
	}
	single := func(name string) (string, error) {
		values := incoming.Get(name)
		if len(values) != 1 || values[0] == "" {
			return "", fmt.Errorf("authenticated test Worker metadata %s is invalid", name)
		}
		return values[0], nil
	}
	contract, err := single("x-mem-auth-contract")
	if err != nil {
		return "", "", err
	}
	keyID, err := single("x-mem-auth-key-id")
	if err != nil {
		return "", "", err
	}
	timestamp, err := single("x-mem-auth-timestamp")
	if err != nil {
		return "", "", err
	}
	nonce, err := single("x-mem-auth-nonce")
	if err != nil {
		return "", "", err
	}
	scope, err := single("x-mem-auth-scope")
	if err != nil {
		return "", "", err
	}
	encodedSignature, err := single("x-mem-auth-signature")
	if err != nil {
		return "", "", err
	}
	if contract != testWorkerRequestAuthContract ||
		keyID != wantKeyID ||
		method != workerpb.ProcessorService_Process_FullMethodName ||
		scope != "process" {
		return "", "", errors.New("authenticated test Worker request binding is invalid")
	}
	bodyDigest, err := testWorkerMessageDigest(message)
	if err != nil {
		return "", "", err
	}
	expected := testWorkerHMAC(key, []byte(strings.Join([]string{
		testWorkerRequestAuthDomain,
		method,
		scope,
		keyID,
		timestamp,
		nonce,
		bodyDigest,
	}, "\n")))
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil || !hmac.Equal(signature, expected) {
		return "", "", errors.New("authenticated test Worker request signature is invalid")
	}
	return nonce, scope, nil
}

func signTestWorkerResponse(
	method, scope, keyID, nonce string,
	key []byte,
	message proto.Message,
) (metadata.MD, error) {
	bodyDigest, err := testWorkerMessageDigest(message)
	if err != nil {
		return nil, err
	}
	signature := base64.RawURLEncoding.EncodeToString(testWorkerHMAC(
		key,
		[]byte(strings.Join([]string{
			testWorkerResponseAuthDomain,
			method,
			scope,
			keyID,
			nonce,
			"0",
			bodyDigest,
		}, "\n")),
	))
	return metadata.Pairs(
		"x-mem-auth-response-contract", testWorkerResponseAuthContract,
		"x-mem-auth-response-key-id", keyID,
		"x-mem-auth-response-nonce", nonce,
		"x-mem-auth-response-signature", signature,
	), nil
}

func testWorkerMessageDigest(message proto.Message) (string, error) {
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func testWorkerHMAC(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func newTestWorkerClient(t *testing.T, address string) *workerclient.Client {
	t.Helper()
	client := workerclient.New(address, "test-bucket")
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close test Worker client: %v", err)
		}
	})
	return client
}

func newAuthenticatedTestWorkerClient(
	t *testing.T,
	address, keyID string,
	key []byte,
) *workerclient.Client {
	t.Helper()
	client := workerclient.New(
		address,
		"test-bucket",
		workerclient.WithHMACAuth(keyID, key),
	)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close authenticated test Worker client: %v", err)
		}
	})
	return client
}

func newManagedAIIndexer(
	pool *pgxpool.Pool,
	client *workerclient.Client,
	profiles *aiprofile.Service,
	usage managedUsageCoordinator,
) *Service {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := New(pool, client, nil, nil, logger)
	service.SetAIProfiles(profiles, true)
	service.SetManagedUsage(usage)
	return service
}

func newLegacyProfileAwareIndexer(
	pool *pgxpool.Pool,
	client *workerclient.Client,
	profiles *aiprofile.Service,
) *Service {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := New(pool, client, nil, nil, logger)
	service.SetAIProfiles(profiles, false)
	return service
}

func successfulManagedTextResponse(
	definition aiprofile.Definition,
) *workerpb.ProcessResponse {
	return &workerpb.ProcessResponse{
		Status:    workerpb.ProcessStatus_STATUS_OK,
		Processor: "text",
		MetadataJson: []byte(
			`{"managed_usage":{"contract":"mem.managed-stage-receipt/v1",` +
				`"stages":{"embedding":"succeeded","llm":"not_invoked"}}}`,
		),
		Embeddings: map[string]*workerpb.Embedding{
			"text": {
				Provider: definition.Embedding.Provider,
				Dim:      768,
				Rows: []*workerpb.EmbeddingRow{{
					Values:    make([]float32, 768),
					Index:     0,
					ChunkText: "short source",
				}},
			},
		},
	}
}

func successfulManagedTwoStageResponse(
	definition aiprofile.Definition,
) *workerpb.ProcessResponse {
	response := successfulManagedTextResponse(definition)
	response.MetadataJson = []byte(
		`{"managed_usage":{"contract":"mem.managed-stage-receipt/v1",` +
			`"stages":{"embedding":"succeeded","llm":"succeeded"}}}`,
	)
	return response
}

func failedNotInvokedManagedTextResponse() *workerpb.ProcessResponse {
	return &workerpb.ProcessResponse{
		Status: workerpb.ProcessStatus_STATUS_FAILED,
		Error:  "synthetic processing failure",
		MetadataJson: []byte(
			`{"managed_usage":{"contract":"mem.managed-stage-receipt/v1",` +
				`"stages":{"embedding":"not_invoked","llm":"not_invoked"}}}`,
		),
	}
}

func assertCommittedManagedResultPending(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, fileID uuid.UUID,
) {
	t.Helper()
	var status string
	var embeddings, pending, reserved, replayable int
	if err := pool.QueryRow(ctx, `
		SELECT index_status,
		       (SELECT count(*) FROM embeddings_text WHERE file_id = $1),
		       (
		           SELECT count(*)
		             FROM managed_ai_stage_settlement_outbox
		            WHERE file_id = $1
		              AND settled_at IS NULL
		       ),
		       (
		           SELECT count(*)
		             FROM managed_embedding_usage
		            WHERE workspace_id = $2
		              AND status = 'reserved'
		       ),
		       (
		           SELECT count(*)
		             FROM managed_ai_stage_settlement_outbox
		            WHERE file_id = $1
		              AND replayable
		       )
		  FROM files
		 WHERE id = $1
	`, fileID, workspaceID).Scan(
		&status,
		&embeddings,
		&pending,
		&reserved,
		&replayable,
	); err != nil {
		t.Fatalf("inspect committed crash-gap result: %v", err)
	}
	if status != "done" ||
		embeddings != 1 ||
		pending != 2 ||
		reserved != 2 ||
		replayable != 2 {
		t.Fatalf(
			"status/embeddings/pending/reserved/replayable = %s/%d/%d/%d/%d",
			status,
			embeddings,
			pending,
			reserved,
			replayable,
		)
	}
}

func assertPersistedV1RouteAndRequest(
	t *testing.T,
	ctx context.Context,
	profiles *aiprofile.Service,
	workspaceID uuid.UUID,
	definition aiprofile.Definition,
	request *workerpb.ProcessRequest,
) {
	t.Helper()
	selection, err := profiles.Get(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load persisted V1 profile: %v", err)
	}
	if selection.WorkspaceID != workspaceID ||
		selection.ProfileID != definition.ID ||
		definition.ProfileSnapshotMismatch(selection) {
		t.Fatalf(
			"persisted V1 selection = %#v, want exact %s/%s/%s snapshot",
			selection,
			definition.ID,
			definition.Revision,
			definition.PipelineRevision,
		)
	}
	if request == nil {
		t.Fatal("authenticated V1 Worker request was not captured")
	}
	var envelope struct {
		AIProfile *workerclient.AIProfileOptions `json:"ai_profile"`
	}
	if err := json.Unmarshal(request.OptionsJson, &envelope); err != nil {
		t.Fatalf("decode authenticated V1 Worker options: %v", err)
	}
	expectedRoute, err := routeFromAIProfile(selection)
	if err != nil {
		t.Fatalf("derive persisted V1 route: %v", err)
	}
	if envelope.AIProfile == nil ||
		expectedRoute.AIProfile == nil ||
		*envelope.AIProfile != *expectedRoute.AIProfile {
		t.Fatalf(
			"authenticated V1 Worker profile = %#v, want %#v",
			envelope.AIProfile,
			expectedRoute.AIProfile,
		)
	}
}

func assertPendingV1TwoStageOutbox(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, fileID uuid.UUID,
	definition aiprofile.Definition,
) {
	t.Helper()
	var (
		rows, exactIdentity, embeddingSucceeded, llmSucceeded int
		unsettled, replayable, exactProviders                 int
	)
	if err := pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (
		           WHERE outbox.profile_id = $3
		             AND outbox.profile_revision = $4
		             AND outbox.pipeline_revision = $5
		       ),
		       count(*) FILTER (
		           WHERE outbox.stage = 'embedding'
		             AND outbox.outcome = 'succeeded'
		       ),
		       count(*) FILTER (
		           WHERE outbox.stage = 'llm'
		             AND outbox.outcome = 'succeeded'
		       ),
		       count(*) FILTER (WHERE outbox.settled_at IS NULL),
		       count(*) FILTER (WHERE outbox.replayable),
		       count(*) FILTER (
		           WHERE (
		               outbox.stage = 'embedding'
		               AND usage.provider || ':' || usage.model = $6
		           ) OR (
		               outbox.stage = 'llm'
		               AND usage.provider || ':' || usage.model = $7
		           )
		       )
		  FROM managed_ai_stage_settlement_outbox AS outbox
		  JOIN managed_embedding_usage AS usage ON usage.id = outbox.usage_id
		 WHERE outbox.file_id = $1
		   AND usage.workspace_id = $2
	`, fileID,
		workspaceID,
		definition.ID,
		definition.Revision,
		definition.PipelineRevision,
		definition.Embedding.Provider,
		definition.LLM.Provider,
	).Scan(
		&rows,
		&exactIdentity,
		&embeddingSucceeded,
		&llmSucceeded,
		&unsettled,
		&replayable,
		&exactProviders,
	); err != nil {
		t.Fatalf("inspect pending V1 two-stage outbox: %v", err)
	}
	if rows != 2 ||
		exactIdentity != 2 ||
		embeddingSucceeded != 1 ||
		llmSucceeded != 1 ||
		unsettled != 2 ||
		replayable != 2 ||
		exactProviders != 2 {
		t.Fatalf(
			"V1 outbox rows/identity/embedding/llm/unsettled/replayable/providers = "+
				"%d/%d/%d/%d/%d/%d/%d, want 2/2/1/1/2/2/2",
			rows,
			exactIdentity,
			embeddingSucceeded,
			llmSucceeded,
			unsettled,
			replayable,
			exactProviders,
		)
	}
}

func assertV1TwoStageUsageClosedExactlyOnce(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, fileID uuid.UUID,
) {
	t.Helper()
	var (
		usageRows, succeeded, terminalEvents, terminalViolations int
		outboxRows, settledOutbox, embeddings, doneFiles         int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
		    (
		        SELECT count(*)
		          FROM managed_embedding_usage
		         WHERE workspace_id = $1
		    ),
		    (
		        SELECT count(*)
		          FROM managed_embedding_usage
		         WHERE workspace_id = $1
		           AND status = 'succeeded'
		    ),
		    (
		        SELECT count(*)
		          FROM managed_embedding_usage_events AS event
		          JOIN managed_embedding_usage AS usage
		            ON usage.id = event.usage_id
		         WHERE usage.workspace_id = $1
		           AND event.status = 'succeeded'
		    ),
		    (
		        SELECT count(*)
		          FROM (
		              SELECT event.usage_id
		                FROM managed_embedding_usage_events AS event
		                JOIN managed_embedding_usage AS usage
		                  ON usage.id = event.usage_id
		               WHERE usage.workspace_id = $1
		               GROUP BY event.usage_id
		              HAVING count(*) FILTER (
		                  WHERE event.status IN (
		                      'succeeded',
		                      'released',
		                      'indeterminate'
		                  )
		              ) <> 1
		          ) AS violation
		    ),
		    (
		        SELECT count(*)
		          FROM managed_ai_stage_settlement_outbox
		         WHERE file_id = $2
		    ),
		    (
		        SELECT count(*)
		          FROM managed_ai_stage_settlement_outbox
		         WHERE file_id = $2
		           AND settled_at IS NOT NULL
		    ),
		    (
		        SELECT count(*)
		          FROM embeddings_text
		         WHERE file_id = $2
		    ),
		    (
		        SELECT count(*)
		          FROM files
		         WHERE id = $2
		           AND index_status = 'done'
		    )
	`, workspaceID, fileID).Scan(
		&usageRows,
		&succeeded,
		&terminalEvents,
		&terminalViolations,
		&outboxRows,
		&settledOutbox,
		&embeddings,
		&doneFiles,
	); err != nil {
		t.Fatalf("inspect exactly-once V1 two-stage settlement: %v", err)
	}
	if usageRows != 2 ||
		succeeded != 2 ||
		terminalEvents != 2 ||
		terminalViolations != 0 ||
		outboxRows != 2 ||
		settledOutbox != 2 ||
		embeddings != 1 ||
		doneFiles != 1 {
		t.Fatalf(
			"V1 usage/succeeded/terminal-events/violations/outbox/settled/"+
				"embeddings/done = %d/%d/%d/%d/%d/%d/%d/%d, "+
				"want 2/2/2/0/2/2/1/1",
			usageRows,
			succeeded,
			terminalEvents,
			terminalViolations,
			outboxRows,
			settledOutbox,
			embeddings,
			doneFiles,
		)
	}
}

func assertRejectedManagedResultAccounting(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, fileID uuid.UUID,
	definition aiprofile.Definition,
	wantConsumed int64,
) {
	t.Helper()
	var (
		rows, detached, scrubbed, nonReplayable, settled int
		embeddingSucceeded, llmSucceeded, terminalUsage  int
		terminalEvents, terminalViolations               int
		consumed, reserved                               int64
	)
	if err := pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE outbox.file_id IS NULL),
		       count(*) FILTER (WHERE outbox.content_sha256 IS NULL),
		       count(*) FILTER (WHERE NOT outbox.replayable),
		       count(*) FILTER (WHERE outbox.settled_at IS NOT NULL),
		       count(*) FILTER (
		           WHERE outbox.stage = 'embedding'
		             AND outbox.outcome = 'succeeded'
		       ),
		       count(*) FILTER (
		           WHERE outbox.stage = 'llm'
		             AND outbox.outcome = 'succeeded'
		       ),
		       count(*) FILTER (WHERE usage.status = 'succeeded')
		  FROM managed_ai_stage_settlement_outbox AS outbox
		  JOIN managed_embedding_usage AS usage ON usage.id = outbox.usage_id
		 WHERE usage.workspace_id = $1
		   AND outbox.profile_id = $3
		   AND outbox.profile_revision = $4
		   AND outbox.pipeline_revision = $5
		   AND (outbox.file_id IS NULL OR outbox.file_id <> $2)
	`, workspaceID,
		fileID,
		definition.ID,
		definition.Revision,
		definition.PipelineRevision,
	).Scan(
		&rows,
		&detached,
		&scrubbed,
		&nonReplayable,
		&settled,
		&embeddingSucceeded,
		&llmSucceeded,
		&terminalUsage,
	); err != nil {
		t.Fatalf("inspect rejected managed accounting intent: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (
		           WHERE event.status IN (
		               'succeeded',
		               'released',
		               'indeterminate'
		           )
		       ),
		       (
		           SELECT count(*)
		             FROM (
		                 SELECT nested.usage_id
		                   FROM managed_embedding_usage_events AS nested
		                  WHERE nested.usage_id IN (
		                      SELECT outbox.usage_id
		                        FROM managed_ai_stage_settlement_outbox AS outbox
		                        JOIN managed_embedding_usage AS usage
		                          ON usage.id = outbox.usage_id
		                       WHERE usage.workspace_id = $1
		                         AND outbox.profile_id = $2
		                         AND outbox.profile_revision = $3
		                         AND outbox.pipeline_revision = $4
		                  )
		                  GROUP BY nested.usage_id
		                 HAVING count(*) FILTER (
		                     WHERE nested.status IN (
		                         'succeeded',
		                         'released',
		                         'indeterminate'
		                     )
		                 ) <> 1
		             ) AS violation
		       )
		  FROM managed_embedding_usage_events AS event
		 WHERE event.usage_id IN (
		     SELECT outbox.usage_id
		       FROM managed_ai_stage_settlement_outbox AS outbox
		       JOIN managed_embedding_usage AS usage ON usage.id = outbox.usage_id
		      WHERE usage.workspace_id = $1
		        AND outbox.profile_id = $2
		        AND outbox.profile_revision = $3
		        AND outbox.pipeline_revision = $4
		 )
	`, workspaceID,
		definition.ID,
		definition.Revision,
		definition.PipelineRevision,
	).Scan(&terminalEvents, &terminalViolations); err != nil {
		t.Fatalf("inspect rejected managed terminal events: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT managed_embedding_units_consumed,
		       managed_embedding_units_reserved
		  FROM workspace_entitlements
		 WHERE workspace_id = $1
	`, workspaceID).Scan(&consumed, &reserved); err != nil {
		t.Fatalf("inspect rejected managed entitlement projection: %v", err)
	}
	if rows != 2 ||
		detached != 2 ||
		scrubbed != 2 ||
		nonReplayable != 2 ||
		settled != 2 ||
		embeddingSucceeded != 1 ||
		llmSucceeded != 1 ||
		terminalUsage != 2 ||
		terminalEvents != 2 ||
		terminalViolations != 0 ||
		consumed != wantConsumed ||
		reserved != 0 {
		t.Fatalf(
			"rejected rows/detached/scrubbed/nonreplayable/settled/embedding/llm/"+
				"terminal-usage/events/violations/consumed/reserved = "+
				"%d/%d/%d/%d/%d/%d/%d/%d/%d/%d/%d/%d, "+
				"want 2/2/2/2/2/1/1/2/2/0/%d/0",
			rows,
			detached,
			scrubbed,
			nonReplayable,
			settled,
			embeddingSucceeded,
			llmSucceeded,
			terminalUsage,
			terminalEvents,
			terminalViolations,
			consumed,
			reserved,
			wantConsumed,
		)
	}
}

func assertProfileResultState(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, fileID uuid.UUID,
	wantProfile, wantStatus string,
	wantTextEmbeddings int,
) {
	t.Helper()
	var profileID, status string
	var textEmbeddings, visualEmbeddings, annotations int
	if err := pool.QueryRow(ctx, `
		SELECT profile.profile_id,
		       file.index_status,
		       (
		           SELECT count(*)
		             FROM embeddings_text
		            WHERE file_id = $2
		       ),
		       (
		           SELECT count(*)
		             FROM embeddings_visual
		            WHERE file_id = $2
		       ),
		       (
		           SELECT count(*)
		             FROM file_annotations
		            WHERE file_id = $2
		       )
		  FROM workspace_ai_profiles AS profile
		  JOIN files AS file ON file.id = $2
		 WHERE profile.workspace_id = $1
	`, workspaceID, fileID).Scan(
		&profileID,
		&status,
		&textEmbeddings,
		&visualEmbeddings,
		&annotations,
	); err != nil {
		t.Fatalf("inspect serialized profile result: %v", err)
	}
	if profileID != wantProfile ||
		status != wantStatus ||
		textEmbeddings != wantTextEmbeddings ||
		visualEmbeddings != 0 ||
		annotations != 0 {
		t.Fatalf(
			"profile/status/text/visual/annotations = %s/%s/%d/%d/%d, "+
				"want %s/%s/%d/0/0",
			profileID,
			status,
			textEmbeddings,
			visualEmbeddings,
			annotations,
			wantProfile,
			wantStatus,
			wantTextEmbeddings,
		)
	}
}

func assertLegacyResultState(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, fileID uuid.UUID,
	wantStatus string,
	wantTextEmbeddings int,
) {
	t.Helper()
	var profiles, textEmbeddings, visualEmbeddings, annotations int
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT file.index_status,
		       (
		           SELECT count(*)
		             FROM workspace_ai_profiles
		            WHERE workspace_id = $1
		       ),
		       (
		           SELECT count(*)
		             FROM embeddings_text
		            WHERE file_id = $2
		       ),
		       (
		           SELECT count(*)
		             FROM embeddings_visual
		            WHERE file_id = $2
		       ),
		       (
		           SELECT count(*)
		             FROM file_annotations
		            WHERE file_id = $2
		       )
		  FROM files AS file
		 WHERE file.id = $2
	`, workspaceID, fileID).Scan(
		&status,
		&profiles,
		&textEmbeddings,
		&visualEmbeddings,
		&annotations,
	); err != nil {
		t.Fatalf("inspect serialized legacy result: %v", err)
	}
	if status != wantStatus ||
		profiles != 0 ||
		textEmbeddings != wantTextEmbeddings ||
		visualEmbeddings != 0 ||
		annotations != 0 {
		t.Fatalf(
			"status/profiles/text/visual/annotations = %s/%d/%d/%d/%d, "+
				"want %s/0/%d/0/0",
			status,
			profiles,
			textEmbeddings,
			visualEmbeddings,
			annotations,
			wantStatus,
			wantTextEmbeddings,
		)
	}
}

func assertRetryableManagedUsagePending(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, fileID uuid.UUID,
) {
	t.Helper()
	var failed, pending, reserved, notInvoked, nonReplayable int
	if err := pool.QueryRow(ctx, `
		SELECT
		    (SELECT count(*) FROM files WHERE id = $1 AND index_status = 'failed'),
		    count(*) FILTER (WHERE settled_at IS NULL),
		    (
		        SELECT count(*)
		          FROM managed_embedding_usage
		         WHERE workspace_id = $2
		           AND status = 'reserved'
		    ),
		    count(*) FILTER (WHERE outcome = 'not_invoked'),
		    count(*) FILTER (WHERE NOT replayable)
		  FROM managed_ai_stage_settlement_outbox
		 WHERE file_id = $1
	`, fileID, workspaceID).Scan(
		&failed,
		&pending,
		&reserved,
		&notInvoked,
		&nonReplayable,
	); err != nil {
		t.Fatalf("inspect failed-attempt outbox: %v", err)
	}
	if failed != 1 ||
		pending != 2 ||
		reserved != 2 ||
		notInvoked != 2 ||
		nonReplayable != 2 {
		t.Fatalf(
			"failed/pending/reserved/not-invoked/non-replayable = %d/%d/%d/%d/%d",
			failed,
			pending,
			reserved,
			notInvoked,
			nonReplayable,
		)
	}
}

func assertManagedUsageProjection(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, fileID uuid.UUID,
	wantSucceeded, wantReleased, wantUnsettled int,
	wantConsumed, wantReserved int64,
) {
	t.Helper()
	var succeeded, released, unsettled, replayable int
	var consumed, reserved int64
	if err := pool.QueryRow(ctx, `
		SELECT
		    count(*) FILTER (WHERE status = 'succeeded'),
		    count(*) FILTER (WHERE status = 'released')
		  FROM managed_embedding_usage
		 WHERE workspace_id = $1
	`, workspaceID).Scan(&succeeded, &released); err != nil {
		t.Fatalf("inspect settled usage: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE settled_at IS NULL),
		       count(*) FILTER (WHERE replayable)
		  FROM managed_ai_stage_settlement_outbox
		 WHERE file_id = $1
	`, fileID).Scan(&unsettled, &replayable); err != nil {
		t.Fatalf("inspect settled outbox: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT managed_embedding_units_consumed,
		       managed_embedding_units_reserved
		  FROM workspace_entitlements
		 WHERE workspace_id = $1
	`, workspaceID).Scan(&consumed, &reserved); err != nil {
		t.Fatalf("inspect settled entitlement: %v", err)
	}
	if succeeded != wantSucceeded ||
		released != wantReleased ||
		unsettled != wantUnsettled ||
		replayable != 2 ||
		consumed != wantConsumed ||
		reserved != wantReserved {
		t.Fatalf(
			"succeeded/released/unsettled/replayable/consumed/reserved = "+
				"%d/%d/%d/%d/%d/%d, want %d/%d/%d/2/%d/%d",
			succeeded,
			released,
			unsettled,
			replayable,
			consumed,
			reserved,
			wantSucceeded,
			wantReleased,
			wantUnsettled,
			wantConsumed,
			wantReserved,
		)
	}
}

func managedUsageIDsForWorkspace(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID uuid.UUID,
) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT id
		  FROM managed_embedding_usage
		 WHERE workspace_id = $1
		 ORDER BY id
	`, workspaceID)
	if err != nil {
		t.Fatalf("query workspace usage IDs: %v", err)
	}
	defer rows.Close()
	ids := make([]uuid.UUID, 0, 2)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan workspace usage ID: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate workspace usage IDs: %v", err)
	}
	return ids
}

func assertDetachedSettlements(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID uuid.UUID,
	usageIDs []uuid.UUID,
) {
	t.Helper()
	var settled, detached, succeeded, released int
	var consumed, reserved int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE settled_at IS NOT NULL),
		       count(*) FILTER (WHERE file_id IS NULL)
		  FROM managed_ai_stage_settlement_outbox
		 WHERE usage_id = ANY($1::uuid[])
	`, usageIDs).Scan(&settled, &detached); err != nil {
		t.Fatalf("inspect reconciled detached outbox: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'succeeded'),
		       count(*) FILTER (WHERE status = 'released')
		  FROM managed_embedding_usage
		 WHERE id = ANY($1::uuid[])
	`, usageIDs).Scan(&succeeded, &released); err != nil {
		t.Fatalf("inspect detached usage outcomes: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT managed_embedding_units_consumed,
		       managed_embedding_units_reserved
		  FROM workspace_entitlements
		 WHERE workspace_id = $1
	`, workspaceID).Scan(&consumed, &reserved); err != nil {
		t.Fatalf("inspect detached entitlement projection: %v", err)
	}
	if settled != 2 ||
		detached != 2 ||
		succeeded != 1 ||
		released != 1 ||
		consumed != 1 ||
		reserved != 0 {
		t.Fatalf(
			"settled/detached/succeeded/released/consumed/reserved = "+
				"%d/%d/%d/%d/%d/%d, want 2/2/1/1/1/0",
			settled,
			detached,
			succeeded,
			released,
			consumed,
			reserved,
		)
	}
}
