// Command memd is the mem backend daemon — HTTP API server + (later) gRPC worker
// client. See SPEC.md §5 / §10.1 W1.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/api"
	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/config"
	"github.com/PeterGuy326/mem/server/internal/contextpack"
	"github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/durablecontext"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/face"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/folder"
	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/PeterGuy326/mem/server/internal/indexer"
	"github.com/PeterGuy326/mem/server/internal/indexgeneration"
	"github.com/PeterGuy326/mem/server/internal/managedusage"
	"github.com/PeterGuy326/mem/server/internal/memory"
	"github.com/PeterGuy326/mem/server/internal/provider"
	"github.com/PeterGuy326/mem/server/internal/queue"
	"github.com/PeterGuy326/mem/server/internal/relator"
	"github.com/PeterGuy326/mem/server/internal/search"
	"github.com/PeterGuy326/mem/server/internal/storage"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
	"github.com/PeterGuy326/mem/server/internal/workspace"
	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/PeterGuy326/mem/server/internal/workspacetransfer"
)

func main() {
	if err := run(); err != nil {
		slog.Error("memd fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	workspaceTransferTmpDir, err := prepareWorkspaceTransferTmpDir(
		cfg.WorkspaceTransferTmpDir,
	)
	if err != nil {
		return fmt.Errorf("workspace transfer temp dir: %w", err)
	}
	logger.Info("memd starting",
		"http_addr", cfg.HTTPAddr,
		"db", redactDSN(cfg.DBURL),
		"s3_endpoint", cfg.S3Endpoint,
		"s3_bucket", cfg.S3Bucket,
		"version", api.Version,
		"workspace_bundle_max_bytes", cfg.WorkspaceBundleMaxBytes,
		"workspace_transfer_max_concurrent", cfg.WorkspaceTransferMaxConcurrent,
		"workspace_transfer_timeout", cfg.WorkspaceTransferTimeout,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// DB — fail fast on connection.
	database, err := db.Open(ctx, cfg.DBURL)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer database.Close()
	logger.Info("db connected")

	if cfg.AutoMigrate {
		if err := database.Migrate(ctx); err != nil {
			return fmt.Errorf("db migrate: %w", err)
		}
		logger.Info("db migrations applied")
	} else {
		logger.Info("automatic database migrations disabled")
	}

	// S3 / MinIO.
	store, err := storage.New(ctx, cfg.S3EndpointHost(), cfg.S3AccessKey, cfg.S3SecretKey,
		cfg.S3Bucket, cfg.S3Region, cfg.S3UseSSL)
	if err != nil {
		return fmt.Errorf("storage init: %w", err)
	}
	logger.Info("storage ready", "bucket", store.Bucket())

	authSvc := auth.New(database.Pool)
	workspaceSvc := workspace.New(database.Pool)
	entitlementSvc := entitlement.New(
		database.Pool,
		cfg.ManagedEmbeddingReservationTTL,
	)
	if cfg.DeploymentMode == "saas" {
		if err := entitlementSvc.Ready(ctx); err != nil {
			return fmt.Errorf("entitlement readiness: %w", err)
		}
		logger.Info("managed embedding entitlement ready",
			"providers", cfg.ManagedEmbeddingProviders,
		)
	}
	folderSvc := folder.New(database.Pool)
	fileSvc := file.New(database.Pool, store, folderSvc)
	memorySvc := memory.New(database.Pool)
	durableContextSvc := durablecontext.New(database.Pool, memorySvc)
	handoffSvc := handoff.New(database.Pool)
	workspaceBundleLimits := workspaceTransferBundleLimits()
	workspaceTransferSvc := workspacetransfer.New(
		database.Pool,
		store,
		workspacetransfer.Options{
			Exporter:        "memd",
			ExporterVersion: api.Version,
			Writer: workspacebundle.WriterOptions{
				Limits: workspaceBundleLimits,
			},
			Reader: workspacebundle.ReaderOptions{
				Limits: workspaceBundleLimits,
			},
		},
	)

	// AI worker client. Empty MEM_WORKER_GRPC disables AI indexing entirely
	// (upload still works, files stay in index_status='pending').
	workerOptions := make([]workerclient.Option, 0, 2)
	if cfg.WorkerAuthKeyID != "" {
		workerOptions = append(
			workerOptions,
			workerclient.WithHMACAuth(cfg.WorkerAuthKeyID, cfg.WorkerAuthKey),
		)
	}
	workerOptions = append(
		workerOptions,
		workerclient.WithManagedOpenAIBinding(cfg.LegacyOpenAIManagedBinding),
	)
	workerCli := workerclient.New(cfg.WorkerGRPC, cfg.S3Bucket, workerOptions...)
	defer workerCli.Close()
	if cfg.WorkerGRPC == "" {
		logger.Warn("worker disabled — MEM_WORKER_GRPC unset; AI indexing skipped")
	} else {
		logger.Info("worker client ready", "addr", cfg.WorkerGRPC)
	}
	if cfg.DeploymentMode == "saas" {
		for _, providerSpec := range cfg.ManagedEmbeddingProviders {
			readyCtx, readyCancel := context.WithTimeout(ctx, 10*time.Second)
			err := workerCli.ReadyAuthenticated(readyCtx, providerSpec)
			readyCancel()
			if err != nil {
				return fmt.Errorf(
					"authenticated Worker readiness for %s: %w",
					providerSpec,
					err,
				)
			}
		}
		logger.Info(
			"authenticated Worker readiness verified",
			"providers",
			cfg.ManagedEmbeddingProviders,
		)
	}
	profileSvc := aiprofile.New(database.Pool, workerCli, cfg.AIProfiles...)
	generationSvc := indexgeneration.New(database.Pool, cfg.AIProfiles...)
	var managedUsageSvc *managedusage.Service
	if cfg.DeploymentMode == "saas" {
		managedUsageSvc = managedusage.New(entitlementSvc)
		profileSvc.SetManagedProbeUsage(managedUsageSvc)
	}
	logger.Info("workspace AI profiles ready", "enabled", cfg.AIProfiles)
	relatorSvc := relator.New(database.Pool, logger)
	faceSvc := face.New(database.Pool, logger)
	idxSvc := indexer.New(database.Pool, workerCli, relatorSvc, faceSvc, logger)
	searchSvc := search.New(
		database.Pool,
		workerCli,
		cfg.ManagedEmbeddingProvider,
	)
	idxSvc.SetAIProfiles(profileSvc, cfg.DeploymentMode == "saas")
	if managedUsageSvc != nil {
		idxSvc.SetManagedUsage(managedUsageSvc)
	}
	searchSvc.SetAIProfiles(profileSvc, cfg.DeploymentMode == "saas")
	// The HTTP managed-search executor owns the entitlement reservation and
	// replay record. Require its in-process authorization marker at the final
	// search-to-Worker boundary so a direct internal caller cannot make a paid
	// quality-profile embedding request without first being accounted for.
	searchSvc.RequireManagedProfileReservation(cfg.DeploymentMode == "saas")

	// Async indexing queue (Asynq + Redis). Falls back to inline goroutine if
	// MEM_REDIS_URL is unset — dev affordance only; production must set it.
	queueCli, err := queue.NewClient(cfg.RedisURL, logger)
	if err != nil {
		return fmt.Errorf("queue client: %w", err)
	}
	defer queueCli.Close()
	if !queueCli.Enabled() {
		logger.Warn("queue disabled — MEM_REDIS_URL unset; uploads will use inline goroutine fallback (NOT crash-safe)")
	} else {
		logger.Info("queue client ready", "redis", redactURLCredentials(cfg.RedisURL))
	}

	providerSvc := provider.New(database.Pool, workerCli, queueCli, logger)
	contextSvc := contextpack.New(searchSvc, &memoryRecallAdapter{service: memorySvc})

	srv := &api.Server{
		Auth:                     authSvc,
		File:                     fileSvc,
		Folder:                   folderSvc,
		Indexer:                  idxSvc,
		Queue:                    queueCli,
		Search:                   searchSvc,
		Context:                  contextSvc,
		Memory:                   memorySvc,
		DurableContext:           durableContextSvc,
		Handoff:                  handoffSvc,
		Provider:                 providerSvc,
		AIProfiles:               profileSvc,
		IndexGenerations:         generationSvc,
		Relator:                  relatorSvc,
		Face:                     faceSvc,
		Workspace:                workspaceSvc,
		WorkspaceTransfer:        workspaceTransferSvc,
		WorkspaceTransferTimeout: cfg.WorkspaceTransferTimeout,
		WorkspaceBundleMaxBytes:  cfg.WorkspaceBundleMaxBytes,
		WorkspaceTransferGate: make(
			chan struct{},
			cfg.WorkspaceTransferMaxConcurrent,
		),
		WorkspaceTransferTmpDir:   workspaceTransferTmpDir,
		DeploymentMode:            cfg.DeploymentMode,
		ManagedEmbeddingProvider:  cfg.ManagedEmbeddingProvider,
		ManagedEmbeddingProviders: cfg.ManagedEmbeddingProviders,
		Entitlements:              entitlementSvc,
		RegistrationMode:          cfg.RegistrationMode,
		SessionTTL:                cfg.SessionTTL,
		CORSOrigins:               cfg.CORSOrigins,
		Log:                       logger,
	}

	if cfg.DeploymentMode == "saas" {
		go reconcileManagedEmbeddingReservations(
			ctx,
			entitlementSvc,
			idxSvc,
			cfg.ManagedEmbeddingReservationTTL,
			logger,
		)
	}

	// Boot the queue consumer in-process. Promotion to a separate binary is a
	// one-line refactor (move this block into cmd/memworker).
	var queueSrv *queue.Server
	if queueCli.Enabled() {
		queueSrv, err = queue.NewServer(cfg.RedisURL, 4, idxSvc, logger)
		if err != nil {
			return fmt.Errorf("queue server: %w", err)
		}
		go func() {
			if err := queueSrv.Run(ctx); err != nil {
				logger.Error("queue server stopped", "err", err)
			}
		}()
		logger.Info("queue consumer running", "concurrency", 4)
	}
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http listening", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	}

	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer sCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	logger.Info("memd stopped cleanly")
	return nil
}

func workspaceTransferBundleLimits() workspacebundle.Limits {
	limits := workspacebundle.DefaultLimits()
	limits.MaxEntries = 100_000
	limits.MaxTotalSize = 32 << 30
	limits.MaxTotalMetadataSize = 128 << 20
	limits.MaxRecordsPerIndex = 100_000
	return limits
}

func reconcileManagedEmbeddingReservations(
	ctx context.Context,
	service *entitlement.Service,
	indexerService *indexer.Service,
	reservationTTL time.Duration,
	logger *slog.Logger,
) {
	interval := reservationTTL / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	reconcile := func() {
		reconcileCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()

		// Drain the transactional stage-settlement outbox first. Otherwise the
		// generic stale-reservation reconciler could classify a durable
		// post-commit result as indeterminate before its known outcome is
		// applied.
		const settlementBatchSize = 100
		totalSettled := 0
		for {
			settled, err := indexerService.ReconcileManagedUsageSettlements(
				reconcileCtx,
				settlementBatchSize,
			)
			totalSettled += settled
			if err != nil {
				logger.Error("managed AI settlement outbox reconcile failed", "err", err)
				return
			}
			if settled < settlementBatchSize {
				break
			}
		}
		if totalSettled > 0 {
			logger.Info("managed AI settlement outbox reconciled", "count", totalSettled)
		}

		reconciled, err := service.ReconcileStale(reconcileCtx)
		if err != nil {
			logger.Error("managed embedding reservation reconcile failed", "err", err)
			return
		}
		if reconciled > 0 {
			logger.Warn(
				"managed embedding reservations marked indeterminate",
				"count",
				reconciled,
			)
		}
	}
	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func prepareWorkspaceTransferTmpDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	path, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Lstat(path)
	if err == nil {
		return validateWorkspaceTransferTmpDir(path, info)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", path, err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	return validateWorkspaceTransferTmpDir(path, info)
}

func validateWorkspaceTransferTmpDir(path string, info os.FileInfo) (string, error) {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%s must be a directory, not a symlink or file", path)
	}
	if permissions := info.Mode().Perm(); permissions != 0o700 {
		return "", fmt.Errorf(
			"%s permissions are %#o; require 0700",
			path,
			permissions,
		)
	}
	return path, nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

func redactDSN(s string) string {
	return redactURLCredentials(s)
}

func redactURLCredentials(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	if _, hasPassword := parsed.User.Password(); !hasPassword {
		return raw
	}
	return parsed.Redacted()
}
