// Command mem-migrate applies the embedded PostgreSQL migrations and exits.
//
// Production orchestrators run this command exactly once before rolling out
// memd replicas. memd can then start with MEM_AUTO_MIGRATE=false, avoiding a
// schema-migration race across horizontally scaled processes.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/PeterGuy326/mem/server/internal/db"
)

func main() {
	if err := run(); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("database migrations applied")
}

func run() error {
	databaseURL := strings.TrimSpace(os.Getenv("MEM_DB_URL"))
	if databaseURL == "" {
		return errors.New("MEM_DB_URL is required")
	}

	signalCtx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 5*time.Minute)
	defer cancel()

	database, err := db.Open(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return fmt.Errorf("db migrate: %w", err)
	}
	return nil
}
