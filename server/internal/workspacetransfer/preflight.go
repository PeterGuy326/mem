package workspacetransfer

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	// MaxConflictDetails bounds both the in-memory preflight sample and the
	// service-level conflict payload.
	MaxConflictDetails  = 200
	preflightValueBatch = 256
)

type preflightConflictSummary struct {
	Conflicts []Conflict
	Total     int
	Truncated bool
}

type conflictCollector struct {
	limit     int
	conflicts []Conflict
	seen      map[string]struct{}
	total     int
	truncated bool
}

func newConflictCollector(limit int) *conflictCollector {
	return &conflictCollector{
		limit:     limit,
		conflicts: make([]Conflict, 0, limit),
		seen:      make(map[string]struct{}, limit),
	}
}

// add returns false after the first distinct conflict beyond the detail cap.
// Callers must short-circuit at that point so Total remains an honest lower
// bound without scanning or materializing the rest of a hostile conflict set.
func (collector *conflictCollector) add(conflict Conflict) bool {
	if collector.truncated {
		return false
	}
	key := conflict.Kind + "\x00" + conflict.Resource + "\x00" + conflict.Value
	if _, exists := collector.seen[key]; exists {
		return true
	}
	collector.total++
	if len(collector.conflicts) == collector.limit {
		collector.truncated = true
		return false
	}
	collector.seen[key] = struct{}{}
	collector.conflicts = append(collector.conflicts, conflict)
	return true
}

func (collector *conflictCollector) queryLimit() int {
	// One extra row is enough to prove truncation. Values are unique within
	// each validated record class (file digests are sourced from Blobs).
	return collector.limit - len(collector.conflicts) + 1
}

func (collector *conflictCollector) summary() preflightConflictSummary {
	sort.Slice(collector.conflicts, func(i, j int) bool {
		if collector.conflicts[i].Kind != collector.conflicts[j].Kind {
			return collector.conflicts[i].Kind < collector.conflicts[j].Kind
		}
		if collector.conflicts[i].Resource != collector.conflicts[j].Resource {
			return collector.conflicts[i].Resource < collector.conflicts[j].Resource
		}
		return collector.conflicts[i].Value < collector.conflicts[j].Value
	})
	return preflightConflictSummary{
		Conflicts: collector.conflicts,
		Total:     collector.total,
		Truncated: collector.truncated,
	}
}

func preflightFresh(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
	data workspacebundle.BundleData,
) (preflightConflictSummary, error) {
	collector := newConflictCollector(MaxConflictDetails)
	fail := func(err error) (preflightConflictSummary, error) {
		return preflightConflictSummary{}, err
	}

	// Checks run in (kind, resource) order. Together with the final sort this
	// makes the bounded response deterministic without building a full result.
	if err := uuidRecordConflicts(
		ctx, tx, collector, "global_id", "checkpoint",
		"task_checkpoints", "id", data.Checkpoints,
		func(record workspacebundle.CheckpointRecord) uuid.UUID { return record.ID },
	); err != nil {
		return fail(err)
	}
	if err := uuidRecordConflicts(
		ctx, tx, collector, "global_id", "file",
		"files", "id", data.Files,
		func(record workspacebundle.FileRecord) uuid.UUID { return record.ID },
	); err != nil {
		return fail(err)
	}
	fileAnnotations := flattenFileAnnotations(data.Files)
	if err := uuidRecordConflicts(
		ctx, tx, collector, "global_id", "file_annotation",
		"file_annotations", "id", fileAnnotations,
		func(record workspacebundle.FileAnnotationRecord) uuid.UUID { return record.ID },
	); err != nil {
		return fail(err)
	}
	if err := uuidRecordConflicts(
		ctx, tx, collector, "global_id", "folder",
		"folders", "id", data.Folders,
		func(record workspacebundle.FolderRecord) uuid.UUID { return record.ID },
	); err != nil {
		return fail(err)
	}
	if err := uuidRecordConflicts(
		ctx, tx, collector, "global_id", "memory",
		"memories", "id", data.Memories,
		func(record workspacebundle.MemoryRecord) uuid.UUID { return record.ID },
	); err != nil {
		return fail(err)
	}
	if err := uuidRecordConflicts(
		ctx, tx, collector, "global_id", "memory_event",
		"memory_events", "id", data.MemoryEvents,
		func(record workspacebundle.MemoryEventRecord) uuid.UUID { return record.ID },
	); err != nil {
		return fail(err)
	}
	if err := uuidRecordConflicts(
		ctx, tx, collector, "global_id", "task",
		"agent_tasks", "id", data.Tasks,
		func(record workspacebundle.TaskRecord) uuid.UUID { return record.ID },
	); err != nil {
		return fail(err)
	}
	if collector.truncated {
		return collector.summary(), nil
	}

	if err := textRecordConflicts(
		ctx, tx, collector, "idempotency", "checkpoint",
		`SELECT DISTINCT idempotency_key FROM task_checkpoints
		  WHERE workspace_id = $1 AND idempotency_key = ANY($2::text[])`,
		target.WorkspaceID, data.Checkpoints,
		func(record workspacebundle.CheckpointRecord) string {
			return record.IdempotencyKey
		},
	); err != nil {
		return fail(err)
	}
	if err := textRecordConflicts(
		ctx, tx, collector, "idempotency", "memory",
		`SELECT DISTINCT idempotency_key_sha256 FROM memories
		  WHERE workspace_id = $1 AND idempotency_key_sha256 = ANY($2::text[])`,
		target.WorkspaceID, data.Memories,
		func(record workspacebundle.MemoryRecord) string {
			return record.IdempotencyKeySHA256
		},
	); err != nil {
		return fail(err)
	}
	if err := textRecordConflicts(
		ctx, tx, collector, "idempotency", "memory_event",
		`SELECT DISTINCT idempotency_key_sha256 FROM memory_events
		  WHERE workspace_id = $1 AND idempotency_key_sha256 = ANY($2::text[])`,
		target.WorkspaceID, data.MemoryEvents,
		func(record workspacebundle.MemoryEventRecord) string {
			return record.IdempotencyKeySHA256
		},
	); err != nil {
		return fail(err)
	}
	if collector.truncated {
		return collector.summary(), nil
	}

	if err := textRecordConflicts(
		ctx, tx, collector, "path", "folder",
		`SELECT DISTINCT path FROM folders
		  WHERE user_id = $1 AND path = ANY($2::text[])`,
		target.OwnerID, data.Folders,
		func(record workspacebundle.FolderRecord) string { return record.Path },
	); err != nil {
		return fail(err)
	}
	if err := textRecordConflicts(
		ctx, tx, collector, "sha256", "file",
		`SELECT DISTINCT sha256 FROM files
		  WHERE user_id = $1 AND sha256 = ANY($2::text[])`,
		target.OwnerID, data.Blobs,
		func(record workspacebundle.BlobInfo) string { return record.SHA256 },
	); err != nil {
		return fail(err)
	}
	if collector.truncated {
		return collector.summary(), nil
	}

	nonEmpty, err := targetPortableCounts(ctx, tx, target)
	if err != nil {
		return fail(err)
	}
	for _, resource := range []string{
		"checkpoints",
		"files",
		"folders",
		"memories",
		"memory_events",
		"tasks",
	} {
		count := nonEmpty[resource]
		if count > 0 && !collector.add(Conflict{
			Kind:     "target_not_empty",
			Resource: resource,
			Value:    strconv.FormatInt(count, 10),
			Detail:   "fresh restore requires an empty portable target",
		}) {
			return collector.summary(), nil
		}
	}

	if err := textRecordConflicts(
		ctx, tx, collector, "task_key", "task",
		`SELECT DISTINCT task_key FROM agent_tasks
		  WHERE workspace_id = $1 AND task_key = ANY($2::text[])`,
		target.WorkspaceID, data.Tasks,
		func(record workspacebundle.TaskRecord) string { return record.TaskKey },
	); err != nil {
		return fail(err)
	}
	return collector.summary(), nil
}

func flattenFileAnnotations(
	files []workspacebundle.FileRecord,
) []workspacebundle.FileAnnotationRecord {
	count := 0
	for _, file := range files {
		count += len(file.Annotations)
	}
	annotations := make([]workspacebundle.FileAnnotationRecord, 0, count)
	for _, file := range files {
		annotations = append(annotations, file.Annotations...)
	}
	return annotations
}

func uuidRecordConflicts[T any](
	ctx context.Context,
	tx pgx.Tx,
	collector *conflictCollector,
	kind, resource, table, column string,
	records []T,
	valueOf func(T) uuid.UUID,
) error {
	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s = ANY($1::uuid[]) ORDER BY %s LIMIT $2",
		column,
		table,
		column,
		column,
	)
	for start := 0; start < len(records) && !collector.truncated; start += preflightValueBatch {
		end := min(start+preflightValueBatch, len(records))
		values := make([]uuid.UUID, end-start)
		for index, record := range records[start:end] {
			values[index] = valueOf(record)
		}
		rows, err := tx.Query(ctx, query, values, collector.queryLimit())
		if err != nil {
			return fmt.Errorf("preflight %s %s: %w", resource, kind, err)
		}
		for rows.Next() {
			var value uuid.UUID
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return fmt.Errorf("scan preflight %s %s: %w", resource, kind, err)
			}
			if !collector.add(Conflict{
				Kind:     kind,
				Resource: resource,
				Value:    value.String(),
				Detail:   "portable UUID already exists",
			}) {
				rows.Close()
				return nil
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate preflight %s %s: %w", resource, kind, err)
		}
		rows.Close()
	}
	return nil
}

func textRecordConflicts[T any](
	ctx context.Context,
	tx pgx.Tx,
	collector *conflictCollector,
	kind, resource, baseQuery string,
	scope any,
	records []T,
	valueOf func(T) string,
) error {
	query := baseQuery + " ORDER BY 1 LIMIT $3"
	for start := 0; start < len(records) && !collector.truncated; start += preflightValueBatch {
		end := min(start+preflightValueBatch, len(records))
		values := make([]string, end-start)
		for index, record := range records[start:end] {
			values[index] = valueOf(record)
		}
		rows, err := tx.Query(
			ctx,
			query,
			scope,
			values,
			collector.queryLimit(),
		)
		if err != nil {
			return fmt.Errorf("preflight %s %s: %w", resource, kind, err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return fmt.Errorf("scan preflight %s %s: %w", resource, kind, err)
			}
			if !collector.add(Conflict{
				Kind:     kind,
				Resource: resource,
				Value:    value,
				Detail:   "portable unique value already exists in target",
			}) {
				rows.Close()
				return nil
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate preflight %s %s: %w", resource, kind, err)
		}
		rows.Close()
	}
	return nil
}

func targetPortableCounts(
	ctx context.Context,
	tx pgx.Tx,
	target importTarget,
) (map[string]int64, error) {
	var folders, files, memories, events, tasks, checkpoints int64
	err := tx.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM folders WHERE user_id = $1),
			(SELECT count(*) FROM files WHERE user_id = $1),
			(SELECT count(*) FROM memories WHERE workspace_id = $2),
			(SELECT count(*) FROM memory_events WHERE workspace_id = $2),
			(SELECT count(*) FROM agent_tasks WHERE workspace_id = $2),
			(SELECT count(*) FROM task_checkpoints WHERE workspace_id = $2)
	`, target.OwnerID, target.WorkspaceID).Scan(
		&folders,
		&files,
		&memories,
		&events,
		&tasks,
		&checkpoints,
	)
	if err != nil {
		return nil, fmt.Errorf("count target portable state: %w", err)
	}
	return map[string]int64{
		"folders":       folders,
		"files":         files,
		"memories":      memories,
		"memory_events": events,
		"tasks":         tasks,
		"checkpoints":   checkpoints,
	}, nil
}
