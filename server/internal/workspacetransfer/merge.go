package workspacetransfer

import (
	"context"
	"fmt"
	"strconv"

	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// merge_conservative is deliberately fail-closed. Every bundle object is
// compared against the target workspace under the workspace import lock:
//
//   - identical content under the same stable identity  -> skipped, never
//     rewritten;
//   - equal file content already present under another file record ->
//     skipped as content_present;
//   - divergent content under the same stable identity  -> recorded as a
//     structured conflict and never overwritten;
//   - absent objects                                    -> inserted.
//
// Objects whose parent was skipped or conflicted are themselves skipped as
// parent_skipped, so the merge never writes dangling references and never
// touches existing target rows. The workspace_imports row stays the bundle
// idempotency boundary; workspace_import_objects is the durable per-object
// ledger that lets a replayed merge report the exact same summary.

// mergeLedgerBatch bounds each multi-row ledger write so very large bundles
// do not queue an unbounded pgx batch.
const mergeLedgerBatch = 1000

type mergeDecision struct {
	outcome string
	reason  string
	// conflict is populated exactly when outcome is MergeOutcomeConflict.
	conflict Conflict
}

func mergeInserted() mergeDecision {
	return mergeDecision{outcome: MergeOutcomeInserted}
}

func mergeSkipped(reason string) mergeDecision {
	return mergeDecision{outcome: MergeOutcomeSkipped, reason: reason}
}

func mergeConflicted(kind, resource, value string) mergeDecision {
	conflict := mergeConflict(kind, resource, value)
	return mergeDecision{
		outcome:  MergeOutcomeConflict,
		reason:   kind,
		conflict: conflict,
	}
}

// mergeParentResolves reports whether a parent decision leaves a row with the
// same portable identity available to child objects: either this merge wrote
// the parent or the target already holds the identical row.
func mergeParentResolves(decision mergeDecision) bool {
	return decision.outcome == MergeOutcomeInserted ||
		(decision.outcome == MergeOutcomeSkipped && decision.reason == MergeSkipIdentical)
}

// mergeConflict builds the deterministic conflict item shared by the live
// planner and the ledger-based replay reconstruction, so both report
// identical structured details.
func mergeConflict(kind, resource, value string) Conflict {
	return Conflict{
		Kind:     kind,
		Resource: resource,
		Value:    value,
		Detail:   mergeConflictDetail(kind, resource),
	}
}

func mergeConflictDetail(kind, resource string) string {
	switch kind {
	case "global_id":
		return resource + " id already exists with divergent portable state"
	case "path":
		return resource + " path already exists with a different portable id"
	case "idempotency":
		return resource + " idempotency key already exists with divergent portable state"
	case "task_key":
		return resource + " task_key already exists with a different portable id"
	case "stable_key":
		return resource + " stable_key already exists with a different portable id"
	case "sequence":
		return resource + " sequence position already exists with a divergent payload"
	case "restore_mode":
		return "bundle was already imported with a different restore mode"
	default:
		return resource + " already exists"
	}
}

type mergeLedgerEntry struct {
	objectType string
	objectID   string
	decision   mergeDecision
}

type mergePlan struct {
	folders     map[uuid.UUID]mergeDecision
	files       map[uuid.UUID]mergeDecision
	annotations map[uuid.UUID]mergeDecision
	memories    map[uuid.UUID]mergeDecision
	events      map[uuid.UUID]mergeDecision
	tasks       map[uuid.UUID]mergeDecision
	checkpoints map[uuid.UUID]mergeDecision
	refs        map[string]mergeDecision

	entries   []mergeLedgerEntry
	collector *conflictCollector
	// aborted is non-nil when planning found more distinct conflicts than the
	// bounded detail budget can honestly enumerate. Import refuses to write
	// anything in that case.
	aborted *ConflictError
}

func newMergePlan(data workspacebundle.BundleData) *mergePlan {
	objectCount := len(data.Folders) + len(data.Files) + len(data.Memories) +
		len(data.MemoryEvents) + len(data.Tasks) + len(data.Checkpoints) +
		len(data.CheckpointRefs)
	for _, file := range data.Files {
		objectCount += len(file.Annotations)
	}
	return &mergePlan{
		folders:     make(map[uuid.UUID]mergeDecision, len(data.Folders)),
		files:       make(map[uuid.UUID]mergeDecision, len(data.Files)),
		annotations: make(map[uuid.UUID]mergeDecision),
		memories:    make(map[uuid.UUID]mergeDecision, len(data.Memories)),
		events:      make(map[uuid.UUID]mergeDecision, len(data.MemoryEvents)),
		tasks:       make(map[uuid.UUID]mergeDecision, len(data.Tasks)),
		checkpoints: make(map[uuid.UUID]mergeDecision, len(data.Checkpoints)),
		refs:        make(map[string]mergeDecision, len(data.CheckpointRefs)),
		entries:     make([]mergeLedgerEntry, 0, objectCount),
		collector:   newConflictCollector(MaxConflictDetails),
	}
}

func (plan *mergePlan) record(
	objectType string,
	objectID string,
	decision mergeDecision,
) error {
	if decision.outcome == MergeOutcomeConflict &&
		!plan.collector.add(decision.conflict) {
		summary := plan.collector.summary()
		plan.aborted = &ConflictError{
			Conflicts: summary.Conflicts,
			Total:     summary.Total,
			Truncated: true,
		}
		return plan.aborted
	}
	plan.entries = append(plan.entries, mergeLedgerEntry{
		objectType: objectType,
		objectID:   objectID,
		decision:   decision,
	})
	return nil
}

func (plan *mergePlan) insertedFileIDs() map[uuid.UUID]struct{} {
	selected := make(map[uuid.UUID]struct{})
	for id, decision := range plan.files {
		if decision.outcome == MergeOutcomeInserted {
			selected[id] = struct{}{}
		}
	}
	return selected
}

func (plan *mergePlan) summary() *MergeSummary {
	summary := &MergeSummary{
		Inserted:        map[string]int64{},
		Skipped:         map[string]int64{},
		SkippedByReason: map[string]int64{},
	}
	for _, entry := range plan.entries {
		switch entry.decision.outcome {
		case MergeOutcomeInserted:
			summary.Inserted[entry.objectType]++
		case MergeOutcomeSkipped:
			summary.Skipped[entry.objectType]++
			summary.SkippedByReason[entry.decision.reason]++
		}
	}
	conflicts := plan.collector.summary()
	summary.Conflicts = conflicts.Conflicts
	summary.ConflictTotal = conflicts.Total
	summary.ConflictsTruncated = conflicts.Truncated
	return summary
}

func mergeRefObjectID(checkpointID uuid.UUID, ordinal int) string {
	return checkpointID.String() + "@" + strconv.Itoa(ordinal)
}

// Target snapshot projections. All fields are bounded by the validated bundle
// record counts plus, for checkpoints, the target rows attached to bundle
// task identities.

type mergeTargetFolder struct {
	ownerID uuid.UUID
	path    string
}

type mergeTargetFile struct {
	ownerID uuid.UUID
	sha256  string
}

type mergeAnnotationSlot struct {
	fileID    uuid.UUID
	stableKey string
}

type mergeTargetAnnotation struct {
	fileID    uuid.UUID
	stableKey string
	kind      string
	valueText string
}

type mergeTargetMemory struct {
	workspaceID   uuid.UUID
	contentSHA256 string
}

type mergeTargetEvent struct {
	workspaceID      uuid.UUID
	memoryID         uuid.UUID
	action           string
	expectedVersion  int64
	resultingVersion int64
}

type mergeTargetTask struct {
	workspaceID uuid.UUID
	taskKey     string
}

type mergeCheckpointSlot struct {
	taskID   uuid.UUID
	sequence int64
}

type mergeTargetCheckpoint struct {
	workspaceID   uuid.UUID
	payloadSHA256 string
}

type mergeTargetSnapshot struct {
	foldersByID       map[uuid.UUID]mergeTargetFolder
	foldersByPath     map[string]struct{}
	filesByID         map[uuid.UUID]mergeTargetFile
	fileSHAs          map[string]struct{}
	annotationsByID   map[uuid.UUID]mergeTargetAnnotation
	annotationsBySlot map[mergeAnnotationSlot]struct{}
	memoriesByID      map[uuid.UUID]mergeTargetMemory
	memoriesByIdem    map[string]mergeTargetMemory
	eventsByID        map[uuid.UUID]mergeTargetEvent
	eventsByIdem      map[string]mergeTargetEvent
	tasksByID         map[uuid.UUID]mergeTargetTask
	tasksByKey        map[string]struct{}
	checkpointsByID   map[uuid.UUID]mergeTargetCheckpoint
	checkpointsByIdem map[string]mergeTargetCheckpoint
	checkpointsBySlot map[mergeCheckpointSlot]mergeTargetCheckpoint
}

// forEachMergeBatch walks records in preflightValueBatch windows so snapshot
// queries stay parameter-bounded for large bundles.
func forEachMergeBatch(total int, fn func(start, end int) error) error {
	for start := 0; start < total; start += preflightValueBatch {
		end := min(start+preflightValueBatch, total)
		if err := fn(start, end); err != nil {
			return err
		}
	}
	return nil
}

func loadMergeTargetSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	data workspacebundle.BundleData,
) (*mergeTargetSnapshot, error) {
	snapshot := &mergeTargetSnapshot{
		foldersByID:       make(map[uuid.UUID]mergeTargetFolder, len(data.Folders)),
		foldersByPath:     make(map[string]struct{}, len(data.Folders)),
		filesByID:         make(map[uuid.UUID]mergeTargetFile, len(data.Files)),
		fileSHAs:          make(map[string]struct{}, len(data.Files)),
		annotationsByID:   make(map[uuid.UUID]mergeTargetAnnotation),
		annotationsBySlot: make(map[mergeAnnotationSlot]struct{}),
		memoriesByID:      make(map[uuid.UUID]mergeTargetMemory, len(data.Memories)),
		memoriesByIdem:    make(map[string]mergeTargetMemory, len(data.Memories)),
		eventsByID:        make(map[uuid.UUID]mergeTargetEvent, len(data.MemoryEvents)),
		eventsByIdem:      make(map[string]mergeTargetEvent, len(data.MemoryEvents)),
		tasksByID:         make(map[uuid.UUID]mergeTargetTask, len(data.Tasks)),
		tasksByKey:        make(map[string]struct{}, len(data.Tasks)),
		checkpointsByID:   make(map[uuid.UUID]mergeTargetCheckpoint, len(data.Checkpoints)),
		checkpointsByIdem: make(map[string]mergeTargetCheckpoint, len(data.Checkpoints)),
		checkpointsBySlot: make(map[mergeCheckpointSlot]mergeTargetCheckpoint, len(data.Checkpoints)),
	}

	if err := forEachMergeBatch(len(data.Folders), func(start, end int) error {
		ids := make([]uuid.UUID, end-start)
		paths := make([]string, end-start)
		for index, record := range data.Folders[start:end] {
			ids[index] = record.ID
			paths[index] = record.Path
		}
		rows, err := tx.Query(ctx, `
			SELECT id, user_id, path FROM folders WHERE id = ANY($1::uuid[])
		`, ids)
		if err != nil {
			return fmt.Errorf("load merge target folders: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var folder mergeTargetFolder
			if err := rows.Scan(&id, &folder.ownerID, &folder.path); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target folders: %w", err)
			}
			snapshot.foldersByID[id] = folder
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target folders: %w", err)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `
			SELECT path FROM folders WHERE user_id = $1 AND path = ANY($2::text[])
		`, target.OwnerID, paths)
		if err != nil {
			return fmt.Errorf("load merge target folder paths: %w", err)
		}
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target folder paths: %w", err)
			}
			snapshot.foldersByPath[path] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target folder paths: %w", err)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}

	if err := forEachMergeBatch(len(data.Files), func(start, end int) error {
		ids := make([]uuid.UUID, end-start)
		shas := make([]string, end-start)
		for index, record := range data.Files[start:end] {
			ids[index] = record.ID
			shas[index] = record.SHA256
		}
		rows, err := tx.Query(ctx, `
			SELECT id, user_id, sha256 FROM files WHERE id = ANY($1::uuid[])
		`, ids)
		if err != nil {
			return fmt.Errorf("load merge target files: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var file mergeTargetFile
			if err := rows.Scan(&id, &file.ownerID, &file.sha256); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target files: %w", err)
			}
			snapshot.filesByID[id] = file
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target files: %w", err)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `
			SELECT DISTINCT sha256 FROM files WHERE user_id = $1 AND sha256 = ANY($2::text[])
		`, target.OwnerID, shas)
		if err != nil {
			return fmt.Errorf("load merge target file digests: %w", err)
		}
		for rows.Next() {
			var sha string
			if err := rows.Scan(&sha); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target file digests: %w", err)
			}
			snapshot.fileSHAs[sha] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target file digests: %w", err)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}

	annotations := flattenFileAnnotations(data.Files)
	if err := forEachMergeBatch(len(annotations), func(start, end int) error {
		ids := make([]uuid.UUID, end-start)
		for index, record := range annotations[start:end] {
			ids[index] = record.ID
		}
		rows, err := tx.Query(ctx, `
			SELECT id, file_id, stable_key, kind, value_text
			  FROM file_annotations
			 WHERE id = ANY($1::uuid[])
		`, ids)
		if err != nil {
			return fmt.Errorf("load merge target annotations: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var annotation mergeTargetAnnotation
			if err := rows.Scan(
				&id,
				&annotation.fileID,
				&annotation.stableKey,
				&annotation.kind,
				&annotation.valueText,
			); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target annotations: %w", err)
			}
			snapshot.annotationsByID[id] = annotation
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target annotations: %w", err)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}
	// Slot matches cover annotations of identical files, whose target rows
	// keep the bundle file ID. UNIQUE (file_id, stable_key) bounds each slot
	// to one row.
	if err := forEachMergeBatch(len(data.Files), func(start, end int) error {
		ids := make([]uuid.UUID, end-start)
		for index, record := range data.Files[start:end] {
			ids[index] = record.ID
		}
		rows, err := tx.Query(ctx, `
			SELECT file_id, stable_key FROM file_annotations WHERE file_id = ANY($1::uuid[])
		`, ids)
		if err != nil {
			return fmt.Errorf("load merge target annotation slots: %w", err)
		}
		for rows.Next() {
			var slot mergeAnnotationSlot
			if err := rows.Scan(&slot.fileID, &slot.stableKey); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target annotation slots: %w", err)
			}
			snapshot.annotationsBySlot[slot] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target annotation slots: %w", err)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}

	if err := forEachMergeBatch(len(data.Memories), func(start, end int) error {
		ids := make([]uuid.UUID, end-start)
		keys := make([]string, end-start)
		for index, record := range data.Memories[start:end] {
			ids[index] = record.ID
			keys[index] = record.IdempotencyKeySHA256
		}
		rows, err := tx.Query(ctx, `
			SELECT id, workspace_id, content_sha256 FROM memories WHERE id = ANY($1::uuid[])
		`, ids)
		if err != nil {
			return fmt.Errorf("load merge target memories: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var memory mergeTargetMemory
			if err := rows.Scan(&id, &memory.workspaceID, &memory.contentSHA256); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target memories: %w", err)
			}
			snapshot.memoriesByID[id] = memory
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target memories: %w", err)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `
			SELECT idempotency_key_sha256, id, content_sha256
			  FROM memories
			 WHERE workspace_id = $1 AND idempotency_key_sha256 = ANY($2::text[])
		`, target.WorkspaceID, keys)
		if err != nil {
			return fmt.Errorf("load merge target memory idempotency keys: %w", err)
		}
		for rows.Next() {
			var key string
			var id uuid.UUID
			var memory mergeTargetMemory
			if err := rows.Scan(&key, &id, &memory.contentSHA256); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target memory idempotency keys: %w", err)
			}
			memory.workspaceID = target.WorkspaceID
			snapshot.memoriesByIdem[key] = memory
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target memory idempotency keys: %w", err)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}

	if err := forEachMergeBatch(len(data.MemoryEvents), func(start, end int) error {
		ids := make([]uuid.UUID, end-start)
		keys := make([]string, end-start)
		for index, record := range data.MemoryEvents[start:end] {
			ids[index] = record.ID
			keys[index] = record.IdempotencyKeySHA256
		}
		rows, err := tx.Query(ctx, `
			SELECT id, workspace_id, memory_id, action, expected_version, resulting_version
			  FROM memory_events
			 WHERE id = ANY($1::uuid[])
		`, ids)
		if err != nil {
			return fmt.Errorf("load merge target memory events: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var event mergeTargetEvent
			if err := rows.Scan(
				&id,
				&event.workspaceID,
				&event.memoryID,
				&event.action,
				&event.expectedVersion,
				&event.resultingVersion,
			); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target memory events: %w", err)
			}
			snapshot.eventsByID[id] = event
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target memory events: %w", err)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `
			SELECT idempotency_key_sha256, id, memory_id, action, expected_version, resulting_version
			  FROM memory_events
			 WHERE workspace_id = $1 AND idempotency_key_sha256 = ANY($2::text[])
		`, target.WorkspaceID, keys)
		if err != nil {
			return fmt.Errorf("load merge target memory event idempotency keys: %w", err)
		}
		for rows.Next() {
			var key string
			var id uuid.UUID
			var event mergeTargetEvent
			if err := rows.Scan(
				&key,
				&id,
				&event.memoryID,
				&event.action,
				&event.expectedVersion,
				&event.resultingVersion,
			); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target memory event idempotency keys: %w", err)
			}
			event.workspaceID = target.WorkspaceID
			snapshot.eventsByIdem[key] = event
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target memory event idempotency keys: %w", err)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}

	if err := forEachMergeBatch(len(data.Tasks), func(start, end int) error {
		ids := make([]uuid.UUID, end-start)
		taskKeys := make([]string, end-start)
		for index, record := range data.Tasks[start:end] {
			ids[index] = record.ID
			taskKeys[index] = record.TaskKey
		}
		rows, err := tx.Query(ctx, `
			SELECT id, workspace_id, task_key
			  FROM agent_tasks
			 WHERE id = ANY($1::uuid[])
			    OR (workspace_id = $2 AND task_key = ANY($3::text[]))
		`, ids, target.WorkspaceID, taskKeys)
		if err != nil {
			return fmt.Errorf("load merge target tasks: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var task mergeTargetTask
			if err := rows.Scan(&id, &task.workspaceID, &task.taskKey); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target tasks: %w", err)
			}
			snapshot.tasksByID[id] = task
			if task.workspaceID == target.WorkspaceID {
				snapshot.tasksByKey[task.taskKey] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target tasks: %w", err)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}

	if err := forEachMergeBatch(len(data.Checkpoints), func(start, end int) error {
		ids := make([]uuid.UUID, end-start)
		keys := make([]string, end-start)
		taskIDs := make([]uuid.UUID, end-start)
		for index, record := range data.Checkpoints[start:end] {
			ids[index] = record.ID
			keys[index] = record.IdempotencyKey
			taskIDs[index] = record.TaskID
		}
		rows, err := tx.Query(ctx, `
			SELECT id, workspace_id, task_id, sequence, payload_sha256
			  FROM task_checkpoints
			 WHERE id = ANY($1::uuid[])
			    OR (workspace_id = $2 AND idempotency_key = ANY($3::text[]))
			    OR task_id = ANY($4::uuid[])
		`, ids, target.WorkspaceID, keys, taskIDs)
		if err != nil {
			return fmt.Errorf("load merge target checkpoints: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var taskID uuid.UUID
			var sequence int64
			var checkpoint mergeTargetCheckpoint
			if err := rows.Scan(
				&id,
				&checkpoint.workspaceID,
				&taskID,
				&sequence,
				&checkpoint.payloadSHA256,
			); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target checkpoints: %w", err)
			}
			snapshot.checkpointsByID[id] = checkpoint
			if checkpoint.workspaceID == target.WorkspaceID {
				snapshot.checkpointsBySlot[mergeCheckpointSlot{
					taskID:   taskID,
					sequence: sequence,
				}] = checkpoint
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target checkpoints: %w", err)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}
	// Idempotency-key matches must be attributed back to their keys, so load
	// them with the key projected.
	if err := forEachMergeBatch(len(data.Checkpoints), func(start, end int) error {
		keys := make([]string, end-start)
		for index, record := range data.Checkpoints[start:end] {
			keys[index] = record.IdempotencyKey
		}
		rows, err := tx.Query(ctx, `
			SELECT id, idempotency_key, payload_sha256
			  FROM task_checkpoints
			 WHERE workspace_id = $1 AND idempotency_key = ANY($2::text[])
		`, target.WorkspaceID, keys)
		if err != nil {
			return fmt.Errorf("load merge target checkpoint idempotency keys: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var key string
			var checkpoint mergeTargetCheckpoint
			if err := rows.Scan(&id, &key, &checkpoint.payloadSHA256); err != nil {
				rows.Close()
				return fmt.Errorf("scan merge target checkpoint idempotency keys: %w", err)
			}
			checkpoint.workspaceID = target.WorkspaceID
			snapshot.checkpointsByIdem[key] = checkpoint
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate merge target checkpoint idempotency keys: %w", err)
		}
		rows.Close()
		return nil
	}); err != nil {
		return nil, err
	}

	return snapshot, nil
}

// planMerge computes the fail-closed per-object merge decisions while the
// import transaction holds the workspace lock. It never writes. A non-nil
// plan.aborted means Import must refuse the merge entirely.
func planMerge(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	data workspacebundle.BundleData,
) (*mergePlan, error) {
	snapshot, err := loadMergeTargetSnapshot(ctx, tx, target, data)
	if err != nil {
		return nil, err
	}
	plan := newMergePlan(data)

	for _, record := range foldersByDepth(data.Folders) {
		var decision mergeDecision
		if existing, ok := snapshot.foldersByID[record.ID]; ok {
			if existing.ownerID == target.OwnerID && existing.path == record.Path {
				decision = mergeSkipped(MergeSkipIdentical)
			} else {
				decision = mergeConflicted("global_id", MergeObjectFolder, record.ID.String())
			}
		} else if _, ok := snapshot.foldersByPath[record.Path]; ok {
			decision = mergeConflicted("path", MergeObjectFolder, record.Path)
		} else {
			decision = mergeInserted()
			if record.ParentID != nil {
				parent, known := plan.folders[*record.ParentID]
				if !known {
					return nil, fmt.Errorf(
						"%w: folder %s references parent %s missing from the bundle",
						ErrIntegrity,
						record.ID,
						*record.ParentID,
					)
				}
				if !mergeParentResolves(parent) {
					decision = mergeSkipped(MergeSkipParentSkipped)
				}
			}
		}
		plan.folders[record.ID] = decision
		if err := plan.record(MergeObjectFolder, record.ID.String(), decision); err != nil {
			return plan, nil
		}
	}

	for _, record := range data.Files {
		var decision mergeDecision
		switch {
		case fileIDExists(snapshot, record):
			decision = mergeFileIDDecision(snapshot, target, record)
		case fileContentExists(snapshot, record):
			decision = mergeSkipped(MergeSkipContentPresent)
		default:
			decision = mergeInserted()
			if record.FolderID != nil {
				folder, known := plan.folders[*record.FolderID]
				if !known {
					return nil, fmt.Errorf(
						"%w: file %s references folder %s missing from the bundle",
						ErrIntegrity,
						record.ID,
						*record.FolderID,
					)
				}
				if !mergeParentResolves(folder) {
					decision = mergeSkipped(MergeSkipParentSkipped)
				}
			}
		}
		plan.files[record.ID] = decision
		if err := plan.record(MergeObjectFile, record.ID.String(), decision); err != nil {
			return plan, nil
		}
	}

	for _, file := range data.Files {
		fileDecision := plan.files[file.ID]
		for _, annotation := range file.Annotations {
			var decision mergeDecision
			switch {
			case fileDecision.outcome == MergeOutcomeInserted:
				decision = mergeInserted()
			case fileDecision.outcome == MergeOutcomeSkipped &&
				fileDecision.reason == MergeSkipIdentical:
				decision = mergeAnnotationDecision(snapshot, file.ID, annotation)
			default:
				decision = mergeSkipped(MergeSkipParentSkipped)
			}
			plan.annotations[annotation.ID] = decision
			if err := plan.record(
				MergeObjectFileAnnotation,
				annotation.ID.String(),
				decision,
			); err != nil {
				return plan, nil
			}
		}
	}

	for _, record := range data.Memories {
		var decision mergeDecision
		if existing, ok := snapshot.memoriesByID[record.ID]; ok {
			if existing.workspaceID != target.WorkspaceID {
				decision = mergeConflicted("global_id", MergeObjectMemory, record.ID.String())
			} else if existing.contentSHA256 == record.ContentSHA256 {
				decision = mergeSkipped(MergeSkipIdentical)
			} else {
				decision = mergeConflicted("global_id", MergeObjectMemory, record.ID.String())
			}
		} else if existing, ok := snapshot.memoriesByIdem[record.IdempotencyKeySHA256]; ok {
			if existing.contentSHA256 == record.ContentSHA256 {
				decision = mergeSkipped(MergeSkipIdentical)
			} else {
				decision = mergeConflicted("idempotency", MergeObjectMemory, record.ID.String())
			}
		} else {
			decision = mergeInserted()
		}
		plan.memories[record.ID] = decision
		if err := plan.record(MergeObjectMemory, record.ID.String(), decision); err != nil {
			return plan, nil
		}
	}

	for _, record := range data.MemoryEvents {
		memoryDecision, known := plan.memories[record.MemoryID]
		if !known {
			return nil, fmt.Errorf(
				"%w: memory event %s references memory %s missing from the bundle",
				ErrIntegrity,
				record.ID,
				record.MemoryID,
			)
		}
		var decision mergeDecision
		switch {
		case !mergeParentResolves(memoryDecision):
			decision = mergeSkipped(MergeSkipParentSkipped)
		default:
			if existing, ok := snapshot.eventsByID[record.ID]; ok {
				if existing.workspaceID != target.WorkspaceID {
					decision = mergeConflicted(
						"global_id",
						MergeObjectMemoryEvent,
						record.ID.String(),
					)
				} else if mergeEventsEqual(existing, record) {
					decision = mergeSkipped(MergeSkipIdentical)
				} else {
					decision = mergeConflicted(
						"global_id",
						MergeObjectMemoryEvent,
						record.ID.String(),
					)
				}
			} else if existing, ok := snapshot.eventsByIdem[record.IdempotencyKeySHA256]; ok {
				if mergeEventsEqual(existing, record) {
					decision = mergeSkipped(MergeSkipIdentical)
				} else {
					decision = mergeConflicted(
						"idempotency",
						MergeObjectMemoryEvent,
						record.ID.String(),
					)
				}
			} else {
				decision = mergeInserted()
			}
		}
		plan.events[record.ID] = decision
		if err := plan.record(MergeObjectMemoryEvent, record.ID.String(), decision); err != nil {
			return plan, nil
		}
	}

	for _, record := range data.Tasks {
		var decision mergeDecision
		if existing, ok := snapshot.tasksByID[record.ID]; ok {
			if existing.workspaceID != target.WorkspaceID {
				decision = mergeConflicted("global_id", MergeObjectTask, record.ID.String())
			} else if existing.taskKey == record.TaskKey {
				decision = mergeSkipped(MergeSkipIdentical)
			} else {
				decision = mergeConflicted("global_id", MergeObjectTask, record.ID.String())
			}
		} else if _, ok := snapshot.tasksByKey[record.TaskKey]; ok {
			decision = mergeConflicted("task_key", MergeObjectTask, record.TaskKey)
		} else {
			decision = mergeInserted()
		}
		plan.tasks[record.ID] = decision
		if err := plan.record(MergeObjectTask, record.ID.String(), decision); err != nil {
			return plan, nil
		}
	}

	for _, record := range data.Checkpoints {
		taskDecision, known := plan.tasks[record.TaskID]
		if !known {
			return nil, fmt.Errorf(
				"%w: checkpoint %s references task %s missing from the bundle",
				ErrIntegrity,
				record.ID,
				record.TaskID,
			)
		}
		decision := mergeCheckpointDecision(snapshot, target, taskDecision, record)
		plan.checkpoints[record.ID] = decision
		if err := plan.record(MergeObjectCheckpoint, record.ID.String(), decision); err != nil {
			return plan, nil
		}
	}

	for _, record := range data.CheckpointRefs {
		checkpointDecision, known := plan.checkpoints[record.CheckpointID]
		if !known {
			return nil, fmt.Errorf(
				"%w: checkpoint ref references checkpoint %s missing from the bundle",
				ErrIntegrity,
				record.CheckpointID,
			)
		}
		var decision mergeDecision
		if checkpointDecision.outcome == MergeOutcomeInserted {
			decision = mergeInserted()
		} else {
			decision = mergeSkipped(MergeSkipParentSkipped)
		}
		objectID := mergeRefObjectID(record.CheckpointID, record.Ordinal)
		plan.refs[objectID] = decision
		if err := plan.record(MergeObjectCheckpointRef, objectID, decision); err != nil {
			return plan, nil
		}
	}

	return plan, nil
}

func fileIDExists(snapshot *mergeTargetSnapshot, record workspacebundle.FileRecord) bool {
	_, ok := snapshot.filesByID[record.ID]
	return ok
}

func fileContentExists(snapshot *mergeTargetSnapshot, record workspacebundle.FileRecord) bool {
	_, ok := snapshot.fileSHAs[record.SHA256]
	return ok
}

func mergeFileIDDecision(
	snapshot *mergeTargetSnapshot,
	target importTarget,
	record workspacebundle.FileRecord,
) mergeDecision {
	existing := snapshot.filesByID[record.ID]
	if existing.ownerID != target.OwnerID {
		return mergeConflicted("global_id", MergeObjectFile, record.ID.String())
	}
	if existing.sha256 == record.SHA256 {
		return mergeSkipped(MergeSkipIdentical)
	}
	return mergeConflicted("global_id", MergeObjectFile, record.ID.String())
}

func mergeAnnotationDecision(
	snapshot *mergeTargetSnapshot,
	fileID uuid.UUID,
	annotation workspacebundle.FileAnnotationRecord,
) mergeDecision {
	if existing, ok := snapshot.annotationsByID[annotation.ID]; ok {
		if existing.fileID == fileID &&
			existing.stableKey == annotation.StableKey &&
			existing.kind == annotation.Kind &&
			existing.valueText == annotation.ValueText {
			return mergeSkipped(MergeSkipIdentical)
		}
		return mergeConflicted("global_id", MergeObjectFileAnnotation, annotation.ID.String())
	}
	slot := mergeAnnotationSlot{fileID: fileID, stableKey: annotation.StableKey}
	if _, ok := snapshot.annotationsBySlot[slot]; ok {
		return mergeConflicted("stable_key", MergeObjectFileAnnotation, annotation.ID.String())
	}
	return mergeInserted()
}

func mergeEventsEqual(
	existing mergeTargetEvent,
	record workspacebundle.MemoryEventRecord,
) bool {
	return existing.memoryID == record.MemoryID &&
		existing.action == record.Action &&
		existing.expectedVersion == record.ExpectedVersion &&
		existing.resultingVersion == record.ResultingVersion
}

func mergeCheckpointDecision(
	snapshot *mergeTargetSnapshot,
	target importTarget,
	taskDecision mergeDecision,
	record workspacebundle.CheckpointRecord,
) mergeDecision {
	var decision mergeDecision
	switch {
	case checkpointIDExists(snapshot, record):
		existing := snapshot.checkpointsByID[record.ID]
		if existing.workspaceID != target.WorkspaceID {
			decision = mergeConflicted("global_id", MergeObjectCheckpoint, record.ID.String())
		} else if existing.payloadSHA256 == record.PayloadSHA256 {
			decision = mergeSkipped(MergeSkipIdentical)
		} else {
			decision = mergeConflicted("global_id", MergeObjectCheckpoint, record.ID.String())
		}
	case checkpointIdemExists(snapshot, record):
		existing := snapshot.checkpointsByIdem[record.IdempotencyKey]
		if existing.payloadSHA256 == record.PayloadSHA256 {
			decision = mergeSkipped(MergeSkipIdentical)
		} else {
			decision = mergeConflicted("idempotency", MergeObjectCheckpoint, record.ID.String())
		}
	default:
		// Sequence positions only collide under tasks that already exist in
		// the target with the same portable ID.
		if taskDecision.outcome == MergeOutcomeSkipped &&
			taskDecision.reason == MergeSkipIdentical {
			if existing, ok := snapshot.checkpointsBySlot[mergeCheckpointSlot{
				taskID:   record.TaskID,
				sequence: record.Sequence,
			}]; ok {
				if existing.payloadSHA256 == record.PayloadSHA256 {
					decision = mergeSkipped(MergeSkipIdentical)
				} else {
					decision = mergeConflicted(
						"sequence",
						MergeObjectCheckpoint,
						record.ID.String(),
					)
				}
				break
			}
		}
		decision = mergeInserted()
	}
	if decision.outcome == MergeOutcomeInserted && !mergeParentResolves(taskDecision) {
		decision = mergeSkipped(MergeSkipParentSkipped)
	}
	return decision
}

func checkpointIDExists(
	snapshot *mergeTargetSnapshot,
	record workspacebundle.CheckpointRecord,
) bool {
	_, ok := snapshot.checkpointsByID[record.ID]
	return ok
}

func checkpointIdemExists(
	snapshot *mergeTargetSnapshot,
	record workspacebundle.CheckpointRecord,
) bool {
	_, ok := snapshot.checkpointsByIdem[record.IdempotencyKey]
	return ok
}

// insertMergeState writes exactly the objects the merge plan decided to
// insert, using the same INSERT statements as a fresh restore.
func insertMergeState(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	data workspacebundle.BundleData,
	storageKeys map[uuid.UUID]string,
	plan *mergePlan,
) error {
	for _, record := range foldersByDepth(data.Folders) {
		if plan.folders[record.ID].outcome != MergeOutcomeInserted {
			continue
		}
		if err := insertFolderRecord(ctx, tx, target, record); err != nil {
			return err
		}
	}
	for _, record := range data.Files {
		fileDecision := plan.files[record.ID]
		if fileDecision.outcome == MergeOutcomeInserted {
			storageKey, ok := storageKeys[record.ID]
			if !ok {
				return fmt.Errorf("%w: file %s has no uploaded object", ErrIntegrity, record.ID)
			}
			if err := insertFileRecord(ctx, tx, target, record, storageKey); err != nil {
				return err
			}
		}
		// Annotations can be inserted both under newly inserted files and
		// under identical files, whose target rows keep the bundle file ID
		// and may be missing annotations the source still carries.
		if !mergeParentResolves(fileDecision) {
			continue
		}
		for _, annotation := range record.Annotations {
			if plan.annotations[annotation.ID].outcome != MergeOutcomeInserted {
				continue
			}
			if err := insertFileAnnotationRecord(ctx, tx, record.ID, annotation); err != nil {
				return err
			}
		}
	}
	for _, record := range data.Memories {
		if plan.memories[record.ID].outcome != MergeOutcomeInserted {
			continue
		}
		if err := insertMemoryRecord(ctx, tx, target, record); err != nil {
			return err
		}
	}
	for _, record := range data.MemoryEvents {
		if plan.events[record.ID].outcome != MergeOutcomeInserted {
			continue
		}
		if err := insertMemoryEventRecord(ctx, tx, target, record); err != nil {
			return err
		}
	}
	for _, record := range data.Tasks {
		if plan.tasks[record.ID].outcome != MergeOutcomeInserted {
			continue
		}
		if err := insertTaskRecord(ctx, tx, target, record); err != nil {
			return err
		}
	}
	taskKeys := make(map[uuid.UUID]string, len(data.Tasks))
	for _, task := range data.Tasks {
		taskKeys[task.ID] = task.TaskKey
	}
	for _, record := range checkpointsByTaskSequence(data.Checkpoints) {
		if plan.checkpoints[record.ID].outcome != MergeOutcomeInserted {
			continue
		}
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
		objectID := mergeRefObjectID(record.CheckpointID, record.Ordinal)
		if plan.refs[objectID].outcome != MergeOutcomeInserted {
			continue
		}
		if err := insertCheckpointRefRecord(ctx, tx, record); err != nil {
			return err
		}
	}
	return nil
}

// recordMergeLedger persists the durable per-object outcome of the merge in
// the same transaction as the imported state, so a replay can reconstruct
// the identical structured summary.
func recordMergeLedger(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID, bundleID uuid.UUID,
	plan *mergePlan,
) error {
	for start := 0; start < len(plan.entries); start += mergeLedgerBatch {
		end := min(start+mergeLedgerBatch, len(plan.entries))
		batch := &pgx.Batch{}
		for _, entry := range plan.entries[start:end] {
			batch.Queue(`
				INSERT INTO workspace_import_objects (
					target_workspace_id,
					bundle_id,
					object_type,
					object_id,
					outcome,
					reason
				)
				VALUES ($1, $2, $3, $4, $5, $6)
			`,
				workspaceID,
				bundleID,
				entry.objectType,
				entry.objectID,
				entry.decision.outcome,
				entry.decision.reason,
			)
		}
		results := tx.SendBatch(ctx, batch)
		for range end - start {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return fmt.Errorf("record merge ledger: %w", err)
			}
		}
		if err := results.Close(); err != nil {
			return fmt.Errorf("record merge ledger: %w", err)
		}
	}
	return nil
}

// loadMergeSummary reconstructs the structured merge result from the durable
// object ledger for replayed imports and ambiguous-commit verification.
func loadMergeSummary(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID, bundleID uuid.UUID,
) (*MergeSummary, error) {
	summary := &MergeSummary{
		Inserted:        map[string]int64{},
		Skipped:         map[string]int64{},
		SkippedByReason: map[string]int64{},
	}
	rows, err := tx.Query(ctx, `
		SELECT object_type, outcome, reason, count(*)
		  FROM workspace_import_objects
		 WHERE target_workspace_id = $1 AND bundle_id = $2
		 GROUP BY object_type, outcome, reason
		 ORDER BY object_type, outcome, reason
	`, workspaceID, bundleID)
	if err != nil {
		return nil, fmt.Errorf("load merge ledger summary: %w", err)
	}
	for rows.Next() {
		var objectType, outcome, reason string
		var count int64
		if err := rows.Scan(&objectType, &outcome, &reason, &count); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan merge ledger summary: %w", err)
		}
		switch outcome {
		case MergeOutcomeInserted:
			summary.Inserted[objectType] += count
		case MergeOutcomeSkipped:
			summary.Skipped[objectType] += count
			summary.SkippedByReason[reason] += count
		case MergeOutcomeConflict:
			summary.ConflictTotal += int(count)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate merge ledger summary: %w", err)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT object_type, object_id, reason
		  FROM workspace_import_objects
		 WHERE target_workspace_id = $1 AND bundle_id = $2 AND outcome = $3
		 ORDER BY reason, object_type, object_id
		 LIMIT $4
	`, workspaceID, bundleID, MergeOutcomeConflict, MaxConflictDetails+1)
	if err != nil {
		return nil, fmt.Errorf("load merge ledger conflicts: %w", err)
	}
	conflicts := make([]Conflict, 0, MaxConflictDetails)
	for rows.Next() {
		var objectType, objectID, reason string
		if err := rows.Scan(&objectType, &objectID, &reason); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan merge ledger conflicts: %w", err)
		}
		if len(conflicts) == MaxConflictDetails {
			summary.ConflictsTruncated = true
			continue
		}
		conflicts = append(conflicts, Conflict{
			Kind:     reason,
			Resource: objectType,
			Value:    objectID,
			Detail:   mergeConflictDetail(reason, objectType),
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate merge ledger conflicts: %w", err)
	}
	rows.Close()
	summary.Conflicts = conflicts
	return summary, nil
}
