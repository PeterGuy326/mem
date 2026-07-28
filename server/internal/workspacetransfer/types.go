// Package workspacetransfer exports and restores complete portable workspace
// state. Archive validation belongs to workspacebundle; this package owns the
// database and object-storage transaction boundary.
package workspacetransfer

import (
	"context"
	"io"
	"time"

	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/google/uuid"
)

const RestoreModeFresh = workspacebundle.RestoreModeFresh

// ObjectStore is the deliberately narrow object-storage seam needed by
// workspace transfer. storage.Store satisfies it directly.
type ObjectStore interface {
	Put(
		ctx context.Context,
		key string,
		body io.Reader,
		size int64,
		contentType string,
	) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

type Options struct {
	Exporter        string
	ExporterVersion string
	Writer          workspacebundle.WriterOptions
	Reader          workspacebundle.ReaderOptions
	Now             func() time.Time
	NewUUID         func() uuid.UUID
}

type ExportRequest struct {
	WorkspaceID uuid.UUID
	// BundleID is optional. A new UUID is generated when it is nil.
	BundleID uuid.UUID
	Writer   io.Writer
}

type ExportResult struct {
	BundleID uuid.UUID
	Counts   workspacebundle.ObjectCounts
}

type ImportRequest struct {
	WorkspaceID uuid.UUID
	Mode        string
	Reader      io.ReaderAt
	Size        int64
}

type ImportResult struct {
	BundleID          uuid.UUID
	ArchiveSHA256     string
	SourceWorkspaceID uuid.UUID
	ImportedAt        time.Time
	Counts            workspacebundle.ObjectCounts
	Replayed          bool
}
