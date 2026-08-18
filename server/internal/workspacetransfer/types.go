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

const (
	RestoreModeFresh             = workspacebundle.RestoreModeFresh
	RestoreModeMergeConservative = workspacebundle.RestoreModeMergeConservative
)

// Merge object types are the durable ledger categories recorded by
// merge_conservative imports in workspace_import_objects.
const (
	MergeObjectFolder         = "folder"
	MergeObjectFile           = "file"
	MergeObjectFileAnnotation = "file_annotation"
	MergeObjectMemory         = "memory"
	MergeObjectMemoryEvent    = "memory_event"
	MergeObjectTask           = "task"
	MergeObjectCheckpoint     = "checkpoint"
	MergeObjectCheckpointRef  = "checkpoint_ref"
)

// Merge outcomes and reasons recorded in the durable object ledger. Skipped
// objects carry one of the MergeSkip* reasons; conflicted objects carry the
// conflict kind so a replayed merge can reconstruct its structured details.
const (
	MergeOutcomeInserted = "inserted"
	MergeOutcomeSkipped  = "skipped"
	MergeOutcomeConflict = "conflict"

	MergeSkipIdentical      = "identical"
	MergeSkipContentPresent = "content_present"
	MergeSkipParentSkipped  = "parent_skipped"
)

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
	// Mode is the restore mode recorded in the durable import ledger. It is
	// always equal to the accepted request mode.
	Mode string
	// Merge is non-nil exactly when Mode is RestoreModeMergeConservative,
	// including replayed merge results reconstructed from the object ledger.
	Merge *MergeSummary
}

// MergeSummary is the structured outcome of one merge_conservative import.
// Inserted and Skipped are keyed by merge object type; SkippedByReason is
// keyed by merge skip reason. Conflicts is a bounded sample capped at
// MaxConflictDetails entries; ConflictTotal is exact when
// ConflictsTruncated is false and a lower bound otherwise. The full
// per-object detail is always persisted in workspace_import_objects.
type MergeSummary struct {
	Inserted           map[string]int64
	Skipped            map[string]int64
	SkippedByReason    map[string]int64
	Conflicts          []Conflict
	ConflictTotal      int
	ConflictsTruncated bool
}
