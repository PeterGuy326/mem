package workspacetransfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/PeterGuy326/mem/server/internal/modeltext"
	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type importTarget struct {
	WorkspaceID uuid.UUID
	OwnerID     uuid.UUID
}

type importReplay struct {
	BundleID          uuid.UUID
	ArchiveSHA256     string
	SourceWorkspaceID uuid.UUID
	ImportedAt        time.Time
	Mode              string
}

type importCommitOutcome uint8

const (
	importCommitUnknown importCommitOutcome = iota
	importCommitCommitted
	importCommitAbsent
	importCommitConflict
)

type importCommitVerification struct {
	Outcome importCommitOutcome
	Replay  importReplay
	Merge   *MergeSummary
	Err     error
}

func (s *Service) Import(
	ctx context.Context,
	request ImportRequest,
) (*ImportResult, error) {
	if s == nil || s.pool == nil || s.store == nil {
		return nil, ErrNotConfigured
	}
	if request.WorkspaceID == uuid.Nil {
		return nil, fmt.Errorf("workspace_id is required")
	}
	mode := strings.TrimSpace(request.Mode)
	if mode == "" {
		mode = RestoreModeFresh
	}
	switch mode {
	case RestoreModeFresh, RestoreModeMergeConservative:
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedMode, mode)
	}
	if request.Reader == nil || request.Size <= 0 {
		return nil, fmt.Errorf("reader and positive size are required")
	}

	archiveDigest, err := archiveSHA256(request.Reader, request.Size)
	if err != nil {
		return nil, fmt.Errorf("hash workspace bundle: %w", err)
	}
	archive, err := workspacebundle.Open(
		request.Reader,
		request.Size,
		s.options.Reader,
	)
	if err != nil {
		return nil, fmt.Errorf("open workspace bundle: %w", err)
	}
	counts := countsFor(archive.BundleData)

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		// The target workspace row below is the import mutex. READ COMMITTED is
		// intentional: a waiter must observe the winner's ledger row after the
		// lock is released and return a replay instead of failing on a stale
		// repeatable-read snapshot.
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return nil, fmt.Errorf("begin workspace import: %w", err)
	}
	defer rollback(tx)

	target, err := lockImportTarget(ctx, tx, request.WorkspaceID)
	if err != nil {
		return nil, err
	}
	replay, found, err := findImportReplay(
		ctx,
		tx,
		target.WorkspaceID,
		archive.Manifest.BundleID,
		archiveDigest,
	)
	if err != nil {
		return nil, err
	}
	if found {
		if replay.BundleID != archive.Manifest.BundleID ||
			replay.ArchiveSHA256 != archiveDigest {
			return nil, &ConflictError{Conflicts: []Conflict{{
				Kind:     "bundle_identity",
				Resource: "workspace_imports",
				Value:    archive.Manifest.BundleID.String(),
				Detail:   "bundle_id or archive digest was already committed with different content",
			}}, Total: 1}
		}
		if replay.Mode != mode {
			return nil, &ConflictError{Conflicts: []Conflict{mergeConflict(
				"restore_mode",
				"workspace_imports",
				archive.Manifest.BundleID.String(),
			)}, Total: 1}
		}
		var merge *MergeSummary
		if replay.Mode == RestoreModeMergeConservative {
			merge, err = loadMergeSummary(
				ctx,
				tx,
				target.WorkspaceID,
				replay.BundleID,
			)
			if err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit workspace import replay: %w", err)
		}
		return &ImportResult{
			BundleID:          replay.BundleID,
			ArchiveSHA256:     replay.ArchiveSHA256,
			SourceWorkspaceID: replay.SourceWorkspaceID,
			ImportedAt:        replay.ImportedAt.UTC(),
			Counts:            counts,
			Replayed:          true,
			Mode:              replay.Mode,
			Merge:             merge,
		}, nil
	}

	var plan *mergePlan
	if mode == RestoreModeMergeConservative {
		planned, err := planMerge(ctx, tx, target, archive.BundleData)
		if err != nil {
			return nil, err
		}
		if planned.aborted != nil {
			// Fail closed: more distinct conflicts than the bounded detail
			// budget can honestly enumerate means nothing is written.
			return nil, planned.aborted
		}
		plan = planned
	} else {
		conflicts, err := preflightFresh(ctx, tx, target, archive.BundleData)
		if err != nil {
			return nil, err
		}
		if conflicts.Total > 0 {
			return nil, &ConflictError{
				Conflicts: conflicts.Conflicts,
				Total:     conflicts.Total,
				Truncated: conflicts.Truncated,
			}
		}
	}

	var selectedFiles map[uuid.UUID]struct{}
	if plan != nil {
		selectedFiles = plan.insertedFileIDs()
	}
	storageKeys, uploaded, err := s.uploadArchiveBlobs(ctx, target, archive, selectedFiles)
	if err != nil {
		return nil, err
	}
	cleanup := func(cause error) error {
		return cleanupUploaded(s.store, uploaded, cause)
	}

	if plan != nil {
		if err := insertMergeState(
			ctx,
			tx,
			target,
			archive.BundleData,
			storageKeys,
			plan,
		); err != nil {
			return nil, cleanup(err)
		}
	} else {
		if err := insertPortableState(
			ctx,
			tx,
			target,
			archive.BundleData,
			storageKeys,
		); err != nil {
			return nil, cleanup(err)
		}
	}
	var importedAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO workspace_imports (
			target_workspace_id,
			bundle_id,
			archive_sha256,
			source_workspace_id,
			schema_version,
			restore_mode
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING imported_at
	`,
		target.WorkspaceID,
		archive.Manifest.BundleID,
		archiveDigest,
		archive.Manifest.Source.WorkspaceID,
		archive.Manifest.SchemaVersion,
		mode,
	).Scan(&importedAt)
	if err != nil {
		return nil, cleanup(fmt.Errorf("record workspace import: %w", err))
	}
	var mergeSummary *MergeSummary
	if plan != nil {
		if err := recordMergeLedger(ctx, tx, target.WorkspaceID, archive.Manifest.BundleID, plan); err != nil {
			return nil, cleanup(err)
		}
		mergeSummary = plan.summary()
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		// Commit errors are ambiguous: PostgreSQL may have committed even when
		// the client did not receive the acknowledgement. pgxpool releases the
		// transaction connection after Commit returns, so use an independent pool
		// transaction to wait on the workspace import mutex and inspect the durable
		// ledger. Uploaded objects are cleaned only while that mutex is held and
		// ledger absence is therefore confirmed.
		return s.resolveAmbiguousImportCommit(
			target,
			importReplay{
				BundleID:          archive.Manifest.BundleID,
				ArchiveSHA256:     archiveDigest,
				SourceWorkspaceID: archive.Manifest.Source.WorkspaceID,
				Mode:              mode,
			},
			counts,
			commitErr,
			cleanup,
		)
	}
	return &ImportResult{
		BundleID:          archive.Manifest.BundleID,
		ArchiveSHA256:     archiveDigest,
		SourceWorkspaceID: archive.Manifest.Source.WorkspaceID,
		ImportedAt:        importedAt.UTC(),
		Counts:            counts,
		Mode:              mode,
		Merge:             mergeSummary,
	}, nil
}

func lockImportTarget(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
) (importTarget, error) {
	var target importTarget
	err := tx.QueryRow(ctx, `
		SELECT id, resource_owner_user_id
		  FROM workspaces
		 WHERE id = $1
		 FOR UPDATE
	`, workspaceID).Scan(&target.WorkspaceID, &target.OwnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return importTarget{}, ErrWorkspaceNotFound
	}
	if err != nil {
		return importTarget{}, fmt.Errorf("lock target workspace: %w", err)
	}
	return target, nil
}

func findImportReplay(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID, bundleID uuid.UUID,
	archiveDigest string,
) (importReplay, bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT bundle_id, archive_sha256, source_workspace_id, imported_at, restore_mode
		  FROM workspace_imports
		 WHERE target_workspace_id = $1
		   AND (bundle_id = $2 OR archive_sha256 = $3)
		 ORDER BY bundle_id
		 FOR SHARE
	`, workspaceID, bundleID, archiveDigest)
	if err != nil {
		return importReplay{}, false, fmt.Errorf("load workspace import replay: %w", err)
	}
	defer rows.Close()
	var replays []importReplay
	for rows.Next() {
		var replay importReplay
		if err := rows.Scan(
			&replay.BundleID,
			&replay.ArchiveSHA256,
			&replay.SourceWorkspaceID,
			&replay.ImportedAt,
			&replay.Mode,
		); err != nil {
			return importReplay{}, false, fmt.Errorf("scan workspace import replay: %w", err)
		}
		replays = append(replays, replay)
	}
	if err := rows.Err(); err != nil {
		return importReplay{}, false, fmt.Errorf("iterate workspace import replay: %w", err)
	}
	if len(replays) == 0 {
		return importReplay{}, false, nil
	}
	if len(replays) > 1 {
		return importReplay{}, false, &ConflictError{Conflicts: []Conflict{{
			Kind:     "bundle_identity",
			Resource: "workspace_imports",
			Value:    bundleID.String(),
			Detail:   "bundle_id and archive digest resolve to different imports",
		}}, Total: 1}
	}
	return replays[0], true, nil
}

func (s *Service) resolveAmbiguousImportCommit(
	target importTarget,
	expected importReplay,
	counts workspacebundle.ObjectCounts,
	commitErr error,
	cleanup func(error) error,
) (*ImportResult, error) {
	verificationCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	verificationTx, err := s.pool.BeginTx(verificationCtx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return resolveImportCommitVerification(
			importCommitVerification{
				Outcome: importCommitUnknown,
				Err:     fmt.Errorf("begin ledger verification: %w", err),
			},
			counts,
			commitErr,
			cleanup,
		)
	}
	defer rollback(verificationTx)

	// Waiting for the same row lock used by Import establishes that the
	// ambiguous transaction has finished before an empty ledger is trusted.
	// It also keeps a concurrent retry from reusing the deterministic object
	// keys until confirmed-absent cleanup has completed.
	if _, err := lockImportTarget(
		verificationCtx,
		verificationTx,
		target.WorkspaceID,
	); err != nil {
		return resolveImportCommitVerification(
			importCommitVerification{
				Outcome: importCommitUnknown,
				Err:     fmt.Errorf("lock target for ledger verification: %w", err),
			},
			counts,
			commitErr,
			cleanup,
		)
	}
	replay, found, lookupErr := findImportReplay(
		verificationCtx,
		verificationTx,
		target.WorkspaceID,
		expected.BundleID,
		expected.ArchiveSHA256,
	)
	verification := classifyImportCommitVerification(expected, replay, found, lookupErr)
	if verification.Outcome == importCommitCommitted &&
		verification.Replay.Mode == RestoreModeMergeConservative {
		merge, err := loadMergeSummary(
			verificationCtx,
			verificationTx,
			target.WorkspaceID,
			verification.Replay.BundleID,
		)
		if err != nil {
			verification = importCommitVerification{
				Outcome: importCommitUnknown,
				Err:     fmt.Errorf("reconstruct merge summary from ledger: %w", err),
			}
		} else {
			verification.Merge = merge
		}
	}
	return resolveImportCommitVerification(
		verification,
		counts,
		commitErr,
		cleanup,
	)
}

func classifyImportCommitVerification(
	expected, replay importReplay,
	found bool,
	err error,
) importCommitVerification {
	if err != nil {
		outcome := importCommitUnknown
		if errors.Is(err, ErrConflict) {
			outcome = importCommitConflict
		}
		return importCommitVerification{Outcome: outcome, Err: err}
	}
	if !found {
		return importCommitVerification{Outcome: importCommitAbsent}
	}
	if replay.BundleID != expected.BundleID ||
		replay.ArchiveSHA256 != expected.ArchiveSHA256 {
		return importCommitVerification{
			Outcome: importCommitConflict,
			Err: &ConflictError{Conflicts: []Conflict{{
				Kind:     "bundle_identity",
				Resource: "workspace_imports",
				Value:    expected.BundleID.String(),
				Detail: "commit verification found a different import for " +
					"the bundle_id or archive digest",
			}}, Total: 1},
		}
	}
	return importCommitVerification{
		Outcome: importCommitCommitted,
		Replay:  replay,
	}
}

func resolveImportCommitVerification(
	verification importCommitVerification,
	counts workspacebundle.ObjectCounts,
	commitErr error,
	cleanup func(error) error,
) (*ImportResult, error) {
	switch verification.Outcome {
	case importCommitCommitted:
		// The durable result was recovered rather than directly observed, so
		// replayed=true tells callers that retry semantics produced this answer.
		return &ImportResult{
			BundleID:          verification.Replay.BundleID,
			ArchiveSHA256:     verification.Replay.ArchiveSHA256,
			SourceWorkspaceID: verification.Replay.SourceWorkspaceID,
			ImportedAt:        verification.Replay.ImportedAt.UTC(),
			Counts:            counts,
			Replayed:          true,
			Mode:              verification.Replay.Mode,
			Merge:             verification.Merge,
		}, nil
	case importCommitAbsent:
		return nil, cleanup(fmt.Errorf(
			"commit workspace import was confirmed absent from the ledger: %w",
			commitErr,
		))
	case importCommitConflict:
		return nil, errors.Join(
			verification.Err,
			fmt.Errorf("commit workspace import: %w", commitErr),
		)
	default:
		verificationErr := verification.Err
		if verificationErr == nil {
			verificationErr = errors.New("ledger verification returned no outcome")
		}
		return nil, errors.Join(
			fmt.Errorf(
				"%w; uploaded objects were preserved; retry the same bundle",
				ErrCommitIndeterminate,
			),
			fmt.Errorf("commit workspace import: %w", commitErr),
			fmt.Errorf("verify workspace import ledger: %w", verificationErr),
		)
	}
}

func (s *Service) uploadArchiveBlobs(
	ctx context.Context,
	target importTarget,
	archive *workspacebundle.Archive,
	selected map[uuid.UUID]struct{},
) (map[uuid.UUID]string, []string, error) {
	blobsByDigest := make(map[string]workspacebundle.BlobInfo, len(archive.Blobs))
	for _, blob := range archive.Blobs {
		blobsByDigest[blob.SHA256] = blob
	}
	keys := make(map[uuid.UUID]string, len(archive.Files))
	uploaded := make([]string, 0, len(archive.Files))
	for _, file := range archive.Files {
		if selected != nil {
			if _, ok := selected[file.ID]; !ok {
				continue
			}
		}
		blob, ok := blobsByDigest[file.SHA256]
		if !ok {
			return nil, uploaded, fmt.Errorf(
				"%w: file %s blob %s is missing",
				ErrIntegrity,
				file.ID,
				file.SHA256,
			)
		}
		key := importedStorageKey(
			target.OwnerID,
			archive.Manifest.BundleID,
			file.ID,
			file.Name,
		)
		reader, err := archive.OpenBlob(file.SHA256)
		if err != nil {
			return nil, uploaded, cleanupUploaded(s.store, uploaded, err)
		}
		uploaded = append(uploaded, key)
		verifier := &hashingReader{
			reader: reader,
			hasher: sha256.New(),
		}
		putErr := s.store.Put(ctx, key, verifier, blob.Size, file.MIME)
		closeErr := reader.Close()
		switch {
		case putErr != nil:
			return nil, uploaded, cleanupUploaded(
				s.store,
				uploaded,
				fmt.Errorf("upload file %s: %w", file.ID, putErr),
			)
		case closeErr != nil:
			return nil, uploaded, cleanupUploaded(
				s.store,
				uploaded,
				fmt.Errorf("close bundle blob %s: %w", file.SHA256, closeErr),
			)
		case verifier.read != blob.Size:
			return nil, uploaded, cleanupUploaded(
				s.store,
				uploaded,
				fmt.Errorf(
					"%w: blob %s upload consumed %d bytes, expected %d",
					ErrIntegrity,
					file.SHA256,
					verifier.read,
					blob.Size,
				),
			)
		case hex.EncodeToString(verifier.hasher.Sum(nil)) != file.SHA256:
			return nil, uploaded, cleanupUploaded(
				s.store,
				uploaded,
				fmt.Errorf("%w: blob %s upload hash mismatch", ErrIntegrity, file.SHA256),
			)
		}
		keys[file.ID] = key
	}
	return keys, uploaded, nil
}

type hashingReader struct {
	reader io.Reader
	hasher hash.Hash
	read   int64
}

func (reader *hashingReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if count > 0 {
		_, _ = reader.hasher.Write(buffer[:count])
		reader.read += int64(count)
	}
	return count, err
}

func cleanupUploaded(
	store ObjectStore,
	keys []string,
	cause error,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cleanupErrors []error
	for index := len(keys) - 1; index >= 0; index-- {
		if err := store.Delete(cleanupCtx, keys[index]); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf(
				"delete uploaded object %s: %w",
				keys[index],
				err,
			))
		}
	}
	if len(cleanupErrors) == 0 {
		return cause
	}
	return errors.Join(append([]error{cause}, cleanupErrors...)...)
}

func importedStorageKey(
	ownerID, bundleID, fileID uuid.UUID,
	name string,
) string {
	clean := path.Base(name)
	if clean == "" || clean == "." || clean == "/" {
		clean = fileID.String()
	}
	return fmt.Sprintf(
		"users/%s/imports/%s/%s/%s",
		ownerID,
		bundleID,
		fileID,
		clean,
	)
}

// foldersByDepth orders folder records so parents are always written before
// their children, satisfying the folders parent_id self-reference.
func foldersByDepth(folders []workspacebundle.FolderRecord) []workspacebundle.FolderRecord {
	sorted := append([]workspacebundle.FolderRecord(nil), folders...)
	sort.Slice(sorted, func(i, j int) bool {
		if strings.Count(sorted[i].Path, "/") == strings.Count(sorted[j].Path, "/") {
			return sorted[i].Path < sorted[j].Path
		}
		return strings.Count(sorted[i].Path, "/") <
			strings.Count(sorted[j].Path, "/")
	})
	return sorted
}

// checkpointsByTaskSequence orders checkpoint records so every base checkpoint
// precedes the checkpoints derived from it.
func checkpointsByTaskSequence(
	checkpoints []workspacebundle.CheckpointRecord,
) []workspacebundle.CheckpointRecord {
	sorted := append([]workspacebundle.CheckpointRecord(nil), checkpoints...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].TaskID == sorted[j].TaskID {
			return sorted[i].Sequence < sorted[j].Sequence
		}
		return sorted[i].TaskID.String() < sorted[j].TaskID.String()
	})
	return sorted
}

func insertFolderRecord(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	record workspacebundle.FolderRecord,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO folders (
			id, user_id, parent_id, path, name, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		record.ID,
		target.OwnerID,
		record.ParentID,
		record.Path,
		record.Name,
		record.CreatedAt,
		record.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert folder %s: %w", record.ID, err)
	}
	return nil
}

func insertFileAnnotationRecord(
	ctx context.Context,
	tx pgx.Tx,
	fileID uuid.UUID,
	annotation workspacebundle.FileAnnotationRecord,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO file_annotations (
			id, file_id, stable_key, kind, value_text, confidence,
			source, provider, processor, analysis_version, status,
			state_version, decided_by_user_id, decided_at,
			created_at, updated_at
		)
		VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
			NULL,$13,$14,$15
		)
	`,
		annotation.ID,
		fileID,
		annotation.StableKey,
		annotation.Kind,
		annotation.ValueText,
		annotation.Confidence,
		annotation.Source,
		annotation.Provider,
		annotation.Processor,
		annotation.AnalysisVersion,
		annotation.Status,
		annotation.StateVersion,
		annotation.DecidedAt,
		annotation.CreatedAt,
		annotation.UpdatedAt,
	); err != nil {
		return fmt.Errorf(
			"insert file %s annotation %s: %w",
			fileID,
			annotation.ID,
			err,
		)
	}
	return nil
}

func insertFileRecord(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	record workspacebundle.FileRecord,
	storageKey string,
) error {
	projection := workspacebundle.DeriveFileEnrichmentProjection(record)
	sourceMetadata := record.SourceMetadata
	if len(strings.TrimSpace(string(sourceMetadata))) == 0 ||
		string(sourceMetadata) == "null" {
		sourceMetadata = []byte(`{}`)
	}
	var geo pgtype.Point
	if record.Geo != nil {
		geo = pgtype.Point{
			P: pgtype.Vec2{
				X: record.Geo.Lon,
				Y: record.Geo.Lat,
			},
			Valid: true,
		}
	}
	var caption *string
	if projection.Caption != nil {
		if safeCaption, ok := modeltext.NormalizePlain(*projection.Caption, 2000); ok {
			caption = &safeCaption
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO files (
			id, user_id, name, path, folder_id, size, sha256, mime,
			storage_key, summary, caption, tags, user_tags, timeline_at, geo,
			source_metadata, processor_metadata, index_status,
			created_at, updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15,
			$16::jsonb, '{}'::jsonb, 'pending', $17, $18
		)
	`,
		record.ID,
		target.OwnerID,
		record.Name,
		record.Path,
		record.FolderID,
		record.Size,
		record.SHA256,
		record.MIME,
		storageKey,
		projection.Summary,
		caption,
		projection.Tags,
		projection.UserTags,
		record.TimelineAt,
		geo,
		[]byte(sourceMetadata),
		record.CreatedAt,
		record.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert file %s: %w", record.ID, err)
	}
	return nil
}

func insertMemoryRecord(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	record workspacebundle.MemoryRecord,
) error {
	requestSHA, err := memoryRequestSHA256(target.WorkspaceID, record)
	if err != nil {
		return fmt.Errorf("hash memory %s target request: %w", record.ID, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO memories (
			id, workspace_id, created_by_user_id, created_by_token_id,
			kind, content, attributes, path, event_at, source_type,
			source_ref, source_file_id, source_file_sha256, source_locator,
			producer_agent, producer_session, producer_task, idempotency_key_sha256,
			request_sha256, content_sha256, lifecycle_status, state_version,
			pinned_at, useful_count, not_useful_count, feedback_at,
			forgotten_at, forgotten_by_user_id, forgotten_by_token_id,
			created_at, updated_at
		)
		VALUES (
			$1, $2, NULL, NULL, $3, $4, $5::jsonb, $6, $7, $8,
			$9, $10, $11, $12::jsonb, $13, $14, $15, $16,
			$17, $18, $19, $20, $21, $22, $23, $24,
			$25, NULL, NULL, $26, $27
		)
	`,
		record.ID,
		target.WorkspaceID,
		record.Kind,
		record.Content,
		string(record.Attributes),
		record.Path,
		record.EventAt,
		record.SourceType,
		record.SourceRef,
		record.SourceFileID,
		record.SourceFileSHA256,
		string(record.SourceLocator),
		record.ProducerAgent,
		record.ProducerSession,
		record.ProducerTask,
		record.IdempotencyKeySHA256,
		requestSHA,
		record.ContentSHA256,
		record.LifecycleStatus,
		record.StateVersion,
		record.PinnedAt,
		record.UsefulCount,
		record.NotUsefulCount,
		record.FeedbackAt,
		record.ForgottenAt,
		record.CreatedAt,
		record.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert memory %s: %w", record.ID, err)
	}
	return nil
}

func insertMemoryEventRecord(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	record workspacebundle.MemoryEventRecord,
) error {
	requestSHA, err := workspacebundle.MemoryEventRequestSHA256(
		target.WorkspaceID,
		record,
	)
	if err != nil {
		return fmt.Errorf("hash memory event %s target request: %w", record.ID, err)
	}
	replayPrincipalSHA256 := ""
	if record.Action == "forget" {
		// Actor IDs are intentionally not portable. Use a target-local,
		// domain-separated receipt that no ordinary Forget request can
		// reproduce, so importing a tombstone cannot grant replay authority
		// to a different principal.
		replayPrincipalSHA256 = digestBytes([]byte(fmt.Sprintf(
			"mem/imported-forget-receipt/v1|%s|%s|%s|%s",
			target.WorkspaceID,
			record.MemoryID,
			record.ID,
			record.IdempotencyKeySHA256,
		)))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO memory_events (
			id, workspace_id, memory_id, action, actor_user_id,
			actor_token_id, idempotency_key_sha256, request_sha256,
			replay_principal_sha256, expected_version, resulting_version,
			reason, created_at
		)
		VALUES (
			$1, $2, $3, $4, NULL, NULL, $5, $6, $7, $8, $9, $10, $11
		)
	`,
		record.ID,
		target.WorkspaceID,
		record.MemoryID,
		record.Action,
		record.IdempotencyKeySHA256,
		requestSHA,
		replayPrincipalSHA256,
		record.ExpectedVersion,
		record.ResultingVersion,
		record.Reason,
		record.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert memory event %s: %w", record.ID, err)
	}
	return nil
}

func insertTaskRecord(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	record workspacebundle.TaskRecord,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_tasks (
			id, workspace_id, task_key, scope_path, head_checkpoint_id,
			head_sequence, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		record.ID,
		target.WorkspaceID,
		record.TaskKey,
		record.ScopePath,
		record.HeadCheckpointID,
		record.HeadSequence,
		record.CreatedAt,
		record.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert task %s: %w", record.ID, err)
	}
	return nil
}

func insertCheckpointRecord(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	record workspacebundle.CheckpointRecord,
	payload []byte,
	taskKey string,
) error {
	requestSHA, err := checkpointRequestSHA256(
		target.WorkspaceID,
		taskKey,
		payload,
	)
	if err != nil {
		return fmt.Errorf("hash checkpoint %s target request: %w", record.ID, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_checkpoints (
			id, workspace_id, task_id, sequence, checkpoint_kind,
			contract_name, schema_version, base_checkpoint_id, scope_path,
			payload, payload_sha256, request_sha256, idempotency_key,
			created_by_user_id, created_by_token_id, producer_agent,
			producer_session, created_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10::jsonb, $11, $12, $13, NULL, NULL, $14, $15, $16
		)
	`,
		record.ID,
		target.WorkspaceID,
		record.TaskID,
		record.Sequence,
		record.CheckpointKind,
		record.Contract,
		record.SchemaVersion,
		record.BaseCheckpointID,
		record.ScopePath,
		string(payload),
		record.PayloadSHA256,
		requestSHA,
		record.IdempotencyKey,
		record.ProducerAgent,
		record.ProducerSession,
		record.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert checkpoint %s: %w", record.ID, err)
	}
	return nil
}

func insertCheckpointRefRecord(
	ctx context.Context,
	tx pgx.Tx,
	record workspacebundle.CheckpointRefRecord,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_checkpoint_refs (
			checkpoint_id, ordinal, relation, uri, expected_sha256,
			required, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
	`,
		record.CheckpointID,
		record.Ordinal,
		record.Relation,
		record.URI,
		record.ExpectedSHA256,
		record.Required,
		string(record.Metadata),
	); err != nil {
		return fmt.Errorf(
			"insert checkpoint %s ref %d: %w",
			record.CheckpointID,
			record.Ordinal,
			err,
		)
	}
	return nil
}

func insertPortableState(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	data workspacebundle.BundleData,
	storageKeys map[uuid.UUID]string,
) error {
	for _, record := range foldersByDepth(data.Folders) {
		if err := insertFolderRecord(ctx, tx, target, record); err != nil {
			return err
		}
	}
	for _, record := range data.Files {
		storageKey, ok := storageKeys[record.ID]
		if !ok {
			return fmt.Errorf("%w: file %s has no uploaded object", ErrIntegrity, record.ID)
		}
		if err := insertFileRecord(ctx, tx, target, record, storageKey); err != nil {
			return err
		}
		for _, annotation := range record.Annotations {
			if err := insertFileAnnotationRecord(ctx, tx, record.ID, annotation); err != nil {
				return err
			}
		}
	}
	for _, record := range data.Memories {
		if err := insertMemoryRecord(ctx, tx, target, record); err != nil {
			return err
		}
	}
	for _, record := range data.MemoryEvents {
		if err := insertMemoryEventRecord(ctx, tx, target, record); err != nil {
			return err
		}
	}
	for _, record := range data.Tasks {
		if err := insertTaskRecord(ctx, tx, target, record); err != nil {
			return err
		}
	}
	taskKeys := make(map[uuid.UUID]string, len(data.Tasks))
	for _, task := range data.Tasks {
		taskKeys[task.ID] = task.TaskKey
	}
	for _, record := range checkpointsByTaskSequence(data.Checkpoints) {
		payload, ok := data.CheckpointPayloads[record.ID]
		if !ok {
			return fmt.Errorf("%w: checkpoint %s payload is missing", ErrIntegrity, record.ID)
		}
		if err := insertCheckpointRecord(
			ctx,
			tx,
			target,
			record,
			payload,
			taskKeys[record.TaskID],
		); err != nil {
			return err
		}
	}
	for _, record := range data.CheckpointRefs {
		if err := insertCheckpointRefRecord(ctx, tx, record); err != nil {
			return err
		}
	}
	return nil
}
