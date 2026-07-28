package workspacetransfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type sourceWorkspace struct {
	ID      uuid.UUID
	Name    string
	OwnerID uuid.UUID
}

type storedBlob struct {
	workspacebundle.BlobInfo
	storageKey string
}

func (s *Service) Export(
	ctx context.Context,
	request ExportRequest,
) (*ExportResult, error) {
	if s == nil || s.pool == nil || s.store == nil {
		return nil, ErrNotConfigured
	}
	if request.WorkspaceID == uuid.Nil {
		return nil, fmt.Errorf("workspace_id is required")
	}
	if request.Writer == nil {
		return nil, fmt.Errorf("writer is required")
	}
	bundleID := request.BundleID
	if bundleID == uuid.Nil {
		bundleID = s.newUUID()
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead,
	})
	if err != nil {
		return nil, fmt.Errorf("begin workspace export snapshot: %w", err)
	}
	defer rollback(tx)

	source, err := loadSourceWorkspace(ctx, tx, request.WorkspaceID)
	if err != nil {
		return nil, err
	}
	data, blobs, err := loadBundleData(ctx, tx, source)
	if err != nil {
		return nil, err
	}
	counts := countsFor(data)
	data.Manifest = workspacebundle.NewManifest(
		bundleID,
		s.now(),
		workspacebundle.SourceDescriptor{
			WorkspaceID:     source.ID,
			WorkspaceName:   source.Name,
			Exporter:        s.exporter,
			ExporterVersion: s.exporterVersion,
		},
		counts,
	)
	sources := make([]workspacebundle.BlobSource, 0, len(blobs))
	for _, blob := range blobs {
		blob := blob
		sources = append(sources, workspacebundle.BlobSource{
			BlobInfo: blob.BlobInfo,
			Open: func() (io.ReadCloser, error) {
				reader, err := s.store.Get(ctx, blob.storageKey)
				if err != nil {
					return nil, fmt.Errorf(
						"open source object %s for file digest %s: %w",
						blob.storageKey,
						blob.SHA256,
						err,
					)
				}
				return reader, nil
			},
		})
	}
	if err := workspacebundle.Write(
		request.Writer,
		workspacebundle.WriteInput{
			BundleData:  data,
			BlobSources: sources,
		},
		s.options.Writer,
	); err != nil {
		return nil, fmt.Errorf("write workspace bundle: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit workspace export snapshot: %w", err)
	}
	return &ExportResult{BundleID: bundleID, Counts: counts}, nil
}

func loadSourceWorkspace(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
) (sourceWorkspace, error) {
	var out sourceWorkspace
	err := tx.QueryRow(ctx, `
		SELECT id, name, resource_owner_user_id
		  FROM workspaces
		 WHERE id = $1
		 FOR SHARE
	`, workspaceID).Scan(&out.ID, &out.Name, &out.OwnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sourceWorkspace{}, ErrWorkspaceNotFound
	}
	if err != nil {
		return sourceWorkspace{}, fmt.Errorf("load source workspace: %w", err)
	}
	return out, nil
}

func loadBundleData(
	ctx context.Context,
	tx pgx.Tx,
	source sourceWorkspace,
) (workspacebundle.BundleData, []storedBlob, error) {
	folders, err := loadFolders(ctx, tx, source.OwnerID)
	if err != nil {
		return workspacebundle.BundleData{}, nil, err
	}
	files, blobs, err := loadFiles(ctx, tx, source.OwnerID)
	if err != nil {
		return workspacebundle.BundleData{}, nil, err
	}
	memories, err := loadMemories(ctx, tx, source.ID)
	if err != nil {
		return workspacebundle.BundleData{}, nil, err
	}
	memoryEvents, err := loadMemoryEvents(ctx, tx, source.ID)
	if err != nil {
		return workspacebundle.BundleData{}, nil, err
	}
	tasks, err := loadTasks(ctx, tx, source.ID)
	if err != nil {
		return workspacebundle.BundleData{}, nil, err
	}
	checkpoints, payloads, err := loadCheckpoints(ctx, tx, source.ID, tasks)
	if err != nil {
		return workspacebundle.BundleData{}, nil, err
	}
	refs, err := loadCheckpointRefs(ctx, tx, source.ID)
	if err != nil {
		return workspacebundle.BundleData{}, nil, err
	}
	blobInfos := make([]workspacebundle.BlobInfo, 0, len(blobs))
	for _, blob := range blobs {
		blobInfos = append(blobInfos, blob.BlobInfo)
	}
	return workspacebundle.BundleData{
		Folders:            folders,
		Files:              files,
		Memories:           memories,
		MemoryEvents:       memoryEvents,
		Tasks:              tasks,
		Checkpoints:        checkpoints,
		CheckpointRefs:     refs,
		CheckpointPayloads: payloads,
		Blobs:              blobInfos,
	}, blobs, nil
}

func loadFolders(
	ctx context.Context,
	tx pgx.Tx,
	ownerID uuid.UUID,
) ([]workspacebundle.FolderRecord, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, parent_id, path, name, created_at, updated_at
		  FROM folders
		 WHERE user_id = $1
		 ORDER BY path, id
		 FOR SHARE
	`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list export folders: %w", err)
	}
	defer rows.Close()
	out := make([]workspacebundle.FolderRecord, 0)
	for rows.Next() {
		var record workspacebundle.FolderRecord
		if err := rows.Scan(
			&record.ID,
			&record.ParentID,
			&record.Path,
			&record.Name,
			&record.CreatedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan export folder: %w", err)
		}
		record.CreatedAt = record.CreatedAt.UTC()
		record.UpdatedAt = record.UpdatedAt.UTC()
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate export folders: %w", err)
	}
	return out, nil
}

func loadFiles(
	ctx context.Context,
	tx pgx.Tx,
	ownerID uuid.UUID,
) ([]workspacebundle.FileRecord, []storedBlob, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, folder_id, name, path, size, sha256, mime, storage_key,
		       summary, caption, tags, timeline_at, created_at, updated_at
		  FROM files
		 WHERE user_id = $1
		 ORDER BY id
		 FOR SHARE
	`, ownerID)
	if err != nil {
		return nil, nil, fmt.Errorf("list export files: %w", err)
	}
	defer rows.Close()
	records := make([]workspacebundle.FileRecord, 0)
	blobs := make([]storedBlob, 0)
	blobIndexes := make(map[string]int)
	for rows.Next() {
		var (
			record     workspacebundle.FileRecord
			storageKey string
		)
		if err := rows.Scan(
			&record.ID,
			&record.FolderID,
			&record.Name,
			&record.Path,
			&record.Size,
			&record.SHA256,
			&record.MIME,
			&storageKey,
			&record.Summary,
			&record.Caption,
			&record.Tags,
			&record.TimelineAt,
			&record.CreatedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan export file: %w", err)
		}
		blobPath, err := workspacebundle.BlobEntryPath(record.SHA256)
		if err != nil {
			return nil, nil, fmt.Errorf("file %s blob path: %w", record.ID, err)
		}
		record.BlobPath = blobPath
		record.CreatedAt = record.CreatedAt.UTC()
		record.UpdatedAt = record.UpdatedAt.UTC()
		if record.TimelineAt != nil {
			value := record.TimelineAt.UTC()
			record.TimelineAt = &value
		}
		records = append(records, record)
		blob := storedBlob{
			BlobInfo: workspacebundle.BlobInfo{
				SHA256: record.SHA256,
				Path:   blobPath,
				Size:   record.Size,
			},
			storageKey: storageKey,
		}
		if index, exists := blobIndexes[record.SHA256]; exists {
			existing := blobs[index]
			if existing.Size != blob.Size || existing.Path != blob.Path {
				return nil, nil, fmt.Errorf(
					"%w: files sharing digest %s disagree on blob size/path",
					ErrIntegrity,
					record.SHA256,
				)
			}
			continue
		}
		blobIndexes[record.SHA256] = len(blobs)
		blobs = append(blobs, blob)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate export files: %w", err)
	}
	return records, blobs, nil
}

func loadMemories(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
) ([]workspacebundle.MemoryRecord, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, kind, content, attributes::text, path, event_at,
		       source_type, source_ref, source_file_id, source_file_sha256,
		       source_locator::text, producer_agent, producer_session,
		       producer_task, idempotency_key_sha256, request_sha256,
		       content_sha256, lifecycle_status, state_version, pinned_at,
		       useful_count, not_useful_count, feedback_at, forgotten_at,
		       created_at, updated_at
		  FROM memories
		 WHERE workspace_id = $1
		 ORDER BY id
		 FOR SHARE
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list export memories: %w", err)
	}
	defer rows.Close()
	out := make([]workspacebundle.MemoryRecord, 0)
	for rows.Next() {
		var (
			record        workspacebundle.MemoryRecord
			attributesRaw string
			locatorRaw    string
		)
		if err := rows.Scan(
			&record.ID,
			&record.Kind,
			&record.Content,
			&attributesRaw,
			&record.Path,
			&record.EventAt,
			&record.SourceType,
			&record.SourceRef,
			&record.SourceFileID,
			&record.SourceFileSHA256,
			&locatorRaw,
			&record.ProducerAgent,
			&record.ProducerSession,
			&record.ProducerTask,
			&record.IdempotencyKeySHA256,
			&record.OriginRequestSHA256,
			&record.ContentSHA256,
			&record.LifecycleStatus,
			&record.StateVersion,
			&record.PinnedAt,
			&record.UsefulCount,
			&record.NotUsefulCount,
			&record.FeedbackAt,
			&record.ForgottenAt,
			&record.CreatedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan export memory: %w", err)
		}
		record.Attributes, err = canonicalObject([]byte(attributesRaw))
		if err != nil {
			return nil, fmt.Errorf("memory %s attributes: %w", record.ID, err)
		}
		record.SourceLocator, err = canonicalObject([]byte(locatorRaw))
		if err != nil {
			return nil, fmt.Errorf("memory %s source locator: %w", record.ID, err)
		}
		normalizeMemoryTimes(&record)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate export memories: %w", err)
	}
	return out, nil
}

func loadMemoryEvents(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
) ([]workspacebundle.MemoryEventRecord, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, memory_id, action, idempotency_key_sha256,
		       request_sha256, expected_version, resulting_version,
		       reason, created_at
		  FROM memory_events
		 WHERE workspace_id = $1
		 ORDER BY memory_id, expected_version, id
		 FOR SHARE
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list export memory events: %w", err)
	}
	defer rows.Close()
	out := make([]workspacebundle.MemoryEventRecord, 0)
	for rows.Next() {
		var record workspacebundle.MemoryEventRecord
		if err := rows.Scan(
			&record.ID,
			&record.MemoryID,
			&record.Action,
			&record.IdempotencyKeySHA256,
			&record.OriginRequestSHA256,
			&record.ExpectedVersion,
			&record.ResultingVersion,
			&record.Reason,
			&record.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan export memory event: %w", err)
		}
		record.CreatedAt = record.CreatedAt.UTC()
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate export memory events: %w", err)
	}
	return out, nil
}

func loadTasks(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
) ([]workspacebundle.TaskRecord, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, task_key, scope_path, head_checkpoint_id, head_sequence,
		       created_at, updated_at
		  FROM agent_tasks
		 WHERE workspace_id = $1
		 ORDER BY task_key, id
		 FOR SHARE
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list export tasks: %w", err)
	}
	defer rows.Close()
	out := make([]workspacebundle.TaskRecord, 0)
	for rows.Next() {
		var record workspacebundle.TaskRecord
		if err := rows.Scan(
			&record.ID,
			&record.TaskKey,
			&record.ScopePath,
			&record.HeadCheckpointID,
			&record.HeadSequence,
			&record.CreatedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan export task: %w", err)
		}
		record.CreatedAt = record.CreatedAt.UTC()
		record.UpdatedAt = record.UpdatedAt.UTC()
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate export tasks: %w", err)
	}
	return out, nil
}

func loadCheckpoints(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
	tasks []workspacebundle.TaskRecord,
) ([]workspacebundle.CheckpointRecord, map[uuid.UUID][]byte, error) {
	taskKeys := make(map[uuid.UUID]string, len(tasks))
	for _, task := range tasks {
		taskKeys[task.ID] = task.TaskKey
	}
	rows, err := tx.Query(ctx, `
		SELECT id, task_id, sequence, checkpoint_kind, contract_name,
		       schema_version, base_checkpoint_id, scope_path, payload::text,
		       payload_sha256, request_sha256, idempotency_key,
		       producer_agent, producer_session, created_at
		  FROM task_checkpoints
		 WHERE workspace_id = $1
		 ORDER BY task_id, sequence, id
		 FOR SHARE
	`, workspaceID)
	if err != nil {
		return nil, nil, fmt.Errorf("list export checkpoints: %w", err)
	}
	defer rows.Close()
	out := make([]workspacebundle.CheckpointRecord, 0)
	payloads := make(map[uuid.UUID][]byte)
	for rows.Next() {
		var (
			record      workspacebundle.CheckpointRecord
			payloadText string
		)
		if err := rows.Scan(
			&record.ID,
			&record.TaskID,
			&record.Sequence,
			&record.CheckpointKind,
			&record.Contract,
			&record.SchemaVersion,
			&record.BaseCheckpointID,
			&record.ScopePath,
			&payloadText,
			&record.PayloadSHA256,
			&record.OriginRequestSHA256,
			&record.IdempotencyKey,
			&record.ProducerAgent,
			&record.ProducerSession,
			&record.CreatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan export checkpoint: %w", err)
		}
		taskKey, ok := taskKeys[record.TaskID]
		if !ok {
			return nil, nil, fmt.Errorf(
				"%w: checkpoint %s references missing task %s",
				ErrIntegrity,
				record.ID,
				record.TaskID,
			)
		}
		payload, err := canonicalHandoffPayload([]byte(payloadText), taskKey)
		if err != nil {
			return nil, nil, fmt.Errorf("checkpoint %s payload: %w", record.ID, err)
		}
		if digestBytes(payload) != record.PayloadSHA256 {
			return nil, nil, fmt.Errorf(
				"%w: checkpoint %s payload hash mismatch",
				ErrIntegrity,
				record.ID,
			)
		}
		record.PayloadPath = workspacebundle.CheckpointPayloadEntryPath(record.ID)
		record.CreatedAt = record.CreatedAt.UTC()
		out = append(out, record)
		payloads[record.ID] = payload
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate export checkpoints: %w", err)
	}
	return out, payloads, nil
}

func loadCheckpointRefs(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
) ([]workspacebundle.CheckpointRefRecord, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.checkpoint_id, r.ordinal, r.relation, r.uri,
		       r.expected_sha256, r.required, r.metadata::text
		  FROM task_checkpoint_refs AS r
		  JOIN task_checkpoints AS c ON c.id = r.checkpoint_id
		 WHERE c.workspace_id = $1
		 ORDER BY r.checkpoint_id, r.ordinal
		 FOR SHARE OF r
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list export checkpoint refs: %w", err)
	}
	defer rows.Close()
	out := make([]workspacebundle.CheckpointRefRecord, 0)
	for rows.Next() {
		var (
			record      workspacebundle.CheckpointRefRecord
			metadataRaw string
		)
		if err := rows.Scan(
			&record.CheckpointID,
			&record.Ordinal,
			&record.Relation,
			&record.URI,
			&record.ExpectedSHA256,
			&record.Required,
			&metadataRaw,
		); err != nil {
			return nil, fmt.Errorf("scan export checkpoint ref: %w", err)
		}
		record.Metadata, err = canonicalObject([]byte(metadataRaw))
		if err != nil {
			return nil, fmt.Errorf(
				"checkpoint %s ref %d metadata: %w",
				record.CheckpointID,
				record.Ordinal,
				err,
			)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate export checkpoint refs: %w", err)
	}
	return out, nil
}

func canonicalObject(raw []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("JSON value is not an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("JSON contains trailing value")
		}
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func canonicalHandoffPayload(raw []byte, taskKey string) ([]byte, error) {
	value, err := handoff.DecodeV1(raw)
	if err != nil {
		return nil, err
	}
	value, err = handoff.NormalizeV1(value, taskKey)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func countsFor(data workspacebundle.BundleData) workspacebundle.ObjectCounts {
	var blobBytes int64
	for _, blob := range data.Blobs {
		blobBytes += blob.Size
	}
	return workspacebundle.ObjectCounts{
		Folders:            int64(len(data.Folders)),
		Files:              int64(len(data.Files)),
		Memories:           int64(len(data.Memories)),
		MemoryEvents:       int64(len(data.MemoryEvents)),
		Tasks:              int64(len(data.Tasks)),
		Checkpoints:        int64(len(data.Checkpoints)),
		CheckpointRefs:     int64(len(data.CheckpointRefs)),
		CheckpointPayloads: int64(len(data.CheckpointPayloads)),
		Blobs:              int64(len(data.Blobs)),
		BlobBytes:          blobBytes,
	}
}

func normalizeMemoryTimes(record *workspacebundle.MemoryRecord) {
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	for _, field := range []**time.Time{
		&record.EventAt,
		&record.PinnedAt,
		&record.FeedbackAt,
		&record.ForgottenAt,
	} {
		if *field != nil {
			value := (*field).UTC()
			*field = &value
		}
	}
}
