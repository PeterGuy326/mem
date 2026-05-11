// Package db wires the PostgreSQL connection pool and runs embedded migrations
// at startup. It exposes a thin facade around pgxpool for the rest of the
// codebase to consume.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB is the PostgreSQL pool wrapper used throughout the server.
type DB struct {
	Pool *pgxpool.Pool
	url  string
}

// Open creates a pgx pool and pings the database. It fails fast if connection
// fails — callers (cmd/memd) should treat this as a fatal startup error.
func Open(ctx context.Context, url string) (*DB, error) {
	pcfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	pcfg.MaxConns = 10
	pcfg.MinConns = 1
	pcfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{Pool: pool, url: url}, nil
}

// Close releases the pool. Safe to call on a nil receiver.
func (d *DB) Close() {
	if d == nil || d.Pool == nil {
		return
	}
	d.Pool.Close()
}

// Migrate applies all embedded migrations to bring the schema up to head.
// Uses goose with the database/sql driver bridge from pgx/stdlib.
func (d *DB) Migrate(ctx context.Context) error {
	sqldb, err := sql.Open("pgx", d.url)
	if err != nil {
		return fmt.Errorf("open sql.DB for goose: %w", err)
	}
	defer sqldb.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	goose.SetLogger(slogGooseLogger{})

	if err := goose.UpContext(ctx, sqldb, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// silence pgx unused-import lint; stdlib is required to register the "pgx"
// driver with database/sql for goose.
var _ = stdlib.GetDefaultDriver

type slogGooseLogger struct{}

func (slogGooseLogger) Fatalf(format string, v ...any) {
	slog.Error("goose", "msg", fmt.Sprintf(format, v...))
}
func (slogGooseLogger) Printf(format string, v ...any) {
	slog.Info("goose", "msg", fmt.Sprintf(format, v...))
}
