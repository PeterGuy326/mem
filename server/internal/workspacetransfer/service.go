package workspacetransfer

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool            *pgxpool.Pool
	store           ObjectStore
	exporter        string
	exporterVersion string
	options         Options
	now             func() time.Time
	newUUID         func() uuid.UUID
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func New(pool *pgxpool.Pool, store ObjectStore, options Options) *Service {
	exporter := options.Exporter
	if exporter == "" {
		exporter = "memd"
	}
	version := options.ExporterVersion
	if version == "" {
		version = "dev"
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newUUID := options.NewUUID
	if newUUID == nil {
		newUUID = uuid.New
	}
	return &Service{
		pool:            pool,
		store:           store,
		exporter:        exporter,
		exporterVersion: version,
		options:         options,
		now:             now,
		newUUID:         newUUID,
	}
}
