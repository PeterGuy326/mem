// Command memd is the mem backend daemon — HTTP API server + (later) gRPC worker
// client. See SPEC.md §5 / §10.1 W1.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PeterGuy326/mem/server/internal/api"
	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/config"
	"github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/storage"
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
	logger.Info("memd starting",
		"http_addr", cfg.HTTPAddr,
		"db", redactDSN(cfg.DBURL),
		"s3_endpoint", cfg.S3Endpoint,
		"s3_bucket", cfg.S3Bucket,
		"version", api.Version,
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

	if err := database.Migrate(ctx); err != nil {
		return fmt.Errorf("db migrate: %w", err)
	}
	logger.Info("db migrations applied")

	// S3 / MinIO.
	store, err := storage.New(ctx, cfg.S3EndpointHost(), cfg.S3AccessKey, cfg.S3SecretKey,
		cfg.S3Bucket, cfg.S3Region, cfg.S3UseSSL)
	if err != nil {
		return fmt.Errorf("storage init: %w", err)
	}
	logger.Info("storage ready", "bucket", store.Bucket())

	authSvc := auth.New(database.Pool)
	fileSvc := file.New(database.Pool, store)

	srv := &api.Server{Auth: authSvc, File: fileSvc, Log: logger}
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
	// crude: postgres://user:pass@host/db -> postgres://user:***@host/db
	at := -1
	colon := -1
	for i, c := range s {
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			// skip protocol
		}
		if c == ':' && i > 10 {
			colon = i
		}
		if c == '@' {
			at = i
			break
		}
	}
	if at < 0 || colon < 0 || colon >= at {
		return s
	}
	return s[:colon+1] + "***" + s[at:]
}
