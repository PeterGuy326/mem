package workspacetransfer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestWorkspaceTransferMergeConservativePostgres exercises the
// merge_conservative restore mode against the real migration: merges into an
// empty workspace must match a fresh restore, merges into a populated
// workspace must skip identical objects, restore absent objects, and report
// structured conflicts without ever overwriting local edits. Retries must be
// idempotent through the durable per-object ledger, and unbounded conflict
// sets must abort the whole merge without writing anything.
//
//	MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_transfer_test?sslmode=disable \
//	  go test ./internal/workspacetransfer -run TestWorkspaceTransferMergeConservativePostgres
func TestWorkspaceTransferMergeConservativePostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping PostgreSQL integration test")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse MEM_TEST_DB: %v", err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatalf(
			"refusing to modify non-test database %q; MEM_TEST_DB must end in _test",
			config.ConnConfig.Database,
		)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	database, err := memdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	// Every restore preserves portable UUIDs, so each scenario needs its own
	// source tenant: rows left behind by an earlier scenario would otherwise
	// collide with the next export's global identities.
	emptySourceUser, emptySourceWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"merge-empty-source",
	)
	mixedSourceUser, mixedSourceWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"merge-mixed-source",
	)
	modeSourceUser, modeSourceWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"merge-mode-source",
	)
	emptyUser, emptyWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"merge-empty-target",
	)
	mixedUser, mixedWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"merge-mixed-target",
	)
	_, modeWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"merge-mode-target",
	)
	overflowUser, overflowWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"merge-overflow-target",
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			15*time.Second,
		)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(cleanupCtx, `
			DELETE FROM users WHERE email LIKE 'merge-%'
		`); err != nil {
			t.Errorf("cleanup merge tenants: %v", err)
		}
	})

	store := newFakeObjectStore()
	fixedNow := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	service := New(database.Pool, store, Options{
		Exporter:        "memd-test",
		ExporterVersion: "v1",
		Now:             func() time.Time { return fixedNow },
	})

	// exportBundle seeds one dedicated source tenant, exports its workspace
	// into an archive, and removes the tenant again so only the archive
	// carries the portable state.
	exportBundle := func(
		sourcePrefix string,
		sourceUser, sourceWorkspace uuid.UUID,
	) ([]byte, transferFixture) {
		t.Helper()
		fixture := seedTransferSource(t, ctx, database, store, sourceUser, sourceWorkspace)
		var buffer bytes.Buffer
		exported, err := service.Export(ctx, ExportRequest{
			WorkspaceID: sourceWorkspace,
			BundleID:    fixture.bundleID,
			Writer:      &buffer,
		})
		if err != nil {
			t.Fatalf("export %s workspace: %v", sourcePrefix, err)
		}
		if exported.BundleID != fixture.bundleID {
			t.Fatalf("%s export result = %+v", sourcePrefix, exported)
		}
		if _, err := database.Pool.Exec(ctx, `
			DELETE FROM users WHERE id = $1
		`, sourceUser); err != nil {
			t.Fatalf("remove %s tenant: %v", sourcePrefix, err)
		}
		return buffer.Bytes(), fixture
	}
	importBundle := func(
		importCtx context.Context,
		raw []byte,
		workspaceID uuid.UUID,
		mode string,
	) (*ImportResult, error) {
		reader := bytes.NewReader(raw)
		return service.Import(importCtx, ImportRequest{
			WorkspaceID: workspaceID,
			Mode:        mode,
			Reader:      reader,
			Size:        int64(reader.Len()),
		})
	}
	countQuery := func(query string, args ...any) int {
		t.Helper()
		var count int
		if err := database.Pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
			t.Fatalf("count query %q: %v", query, err)
		}
		return count
	}

	// A merge into an empty workspace must behave exactly like a fresh
	// restore, and an interruption mid-upload must roll back cleanly so the
	// same bundle can be retried.
	t.Run("empty target equivalence", func(t *testing.T) {
		emptyBundle, fixture := exportBundle(
			"merge-empty-source",
			emptySourceUser,
			emptySourceWorkspace,
		)
		interruptCtx, cancelInterrupt := context.WithCancel(ctx)
		store.afterPut = cancelInterrupt
		_, err := importBundle(
			interruptCtx,
			emptyBundle,
			emptyWorkspace,
			RestoreModeMergeConservative,
		)
		store.afterPut = nil
		cancelInterrupt()
		if err == nil {
			t.Fatal("interrupted merge unexpectedly succeeded")
		}
		if rows := countQuery(
			`SELECT count(*) FROM files WHERE user_id = $1`,
			emptyUser,
		); rows != 0 {
			t.Fatalf("interrupted merge left %d file rows", rows)
		}
		if rows := countQuery(
			`SELECT count(*) FROM workspace_imports WHERE target_workspace_id = $1`,
			emptyWorkspace,
		); rows != 0 {
			t.Fatalf("interrupted merge left %d import ledger rows", rows)
		}
		if rows := countQuery(
			`SELECT count(*) FROM workspace_import_objects WHERE target_workspace_id = $1`,
			emptyWorkspace,
		); rows != 0 {
			t.Fatalf("interrupted merge left %d object ledger rows", rows)
		}
		if len(store.deletes) != 2 {
			t.Fatalf(
				"interrupted merge compensated deletes=%v objects=%v",
				store.deletes,
				store.objects,
			)
		}

		imported, err := importBundle(
			ctx,
			emptyBundle,
			emptyWorkspace,
			RestoreModeMergeConservative,
		)
		if err != nil {
			t.Fatalf("merge into empty workspace: %v", err)
		}
		if imported.Replayed ||
			imported.Mode != RestoreModeMergeConservative ||
			imported.Merge == nil {
			t.Fatalf("merge result = %+v", imported)
		}
		wantInserted := map[string]int64{
			MergeObjectFolder:         2,
			MergeObjectFile:           2,
			MergeObjectFileAnnotation: 3,
			MergeObjectMemory:         1,
			MergeObjectMemoryEvent:    1,
			MergeObjectTask:           1,
			MergeObjectCheckpoint:     2,
			MergeObjectCheckpointRef:  4,
		}
		if !reflect.DeepEqual(imported.Merge.Inserted, wantInserted) ||
			len(imported.Merge.Skipped) != 0 ||
			len(imported.Merge.SkippedByReason) != 0 ||
			imported.Merge.ConflictTotal != 0 ||
			len(imported.Merge.Conflicts) != 0 ||
			imported.Merge.ConflictsTruncated {
			t.Fatalf("merge summary on empty target = %+v", imported.Merge)
		}
		var restoreMode string
		if err := database.Pool.QueryRow(ctx, `
			SELECT restore_mode
			  FROM workspace_imports
			 WHERE target_workspace_id = $1 AND bundle_id = $2
		`, emptyWorkspace, fixture.bundleID).Scan(&restoreMode); err != nil {
			t.Fatalf("load merge import ledger: %v", err)
		}
		if restoreMode != RestoreModeMergeConservative {
			t.Fatalf("restore_mode = %q", restoreMode)
		}
		// The merged state must be indistinguishable from a fresh restore,
		// including the ordinary service replay contracts.
		assertImportedState(
			t,
			ctx,
			database,
			store,
			emptyUser,
			emptyWorkspace,
			fixture,
		)
	})

	// A merge into a populated workspace skips identical objects, restores
	// absent ones, and reports divergent ones as structured conflicts without
	// overwriting local edits.
	var (
		populatedMerge *ImportResult
		mixedBundle    []byte
	)
	t.Run("populated target keeps local edits", func(t *testing.T) {
		bundle, fixture := exportBundle(
			"merge-mixed-source",
			mixedSourceUser,
			mixedSourceWorkspace,
		)
		mixedBundle = bundle
		if _, err := importBundle(ctx, bundle, mixedWorkspace, RestoreModeFresh); err != nil {
			t.Fatalf("seed target with fresh restore: %v", err)
		}
		// Drop the seed ledger so the next import is planned as a real merge
		// against existing rows instead of a replay.
		if _, err := database.Pool.Exec(ctx, `
			DELETE FROM workspace_imports WHERE target_workspace_id = $1
		`, mixedWorkspace); err != nil {
			t.Fatalf("drop seed import ledger: %v", err)
		}
		localMemoryContent := "Locally edited memory content."
		localMemorySHA := digestBytes([]byte(localMemoryContent))
		if _, err := database.Pool.Exec(ctx, `
			UPDATE memories SET content = $2, content_sha256 = $3 WHERE id = $1
		`, fixture.memoryID, localMemoryContent, localMemorySHA); err != nil {
			t.Fatalf("diverge target memory: %v", err)
		}
		localCheckpointPayload := `{"merge_test":"locally-edited-checkpoint"}`
		localCheckpointSHA := digestBytes([]byte(localCheckpointPayload))
		if _, err := database.Pool.Exec(ctx, `
			UPDATE task_checkpoints
			   SET payload = $2::jsonb, payload_sha256 = $3
			 WHERE id = $1
		`, fixture.checkpointID, localCheckpointPayload, localCheckpointSHA); err != nil {
			t.Fatalf("diverge target checkpoint: %v", err)
		}
		if _, err := database.Pool.Exec(ctx, `
			DELETE FROM files WHERE id = $1
		`, fixture.secondFileID); err != nil {
			t.Fatalf("remove target file: %v", err)
		}
		if _, err := database.Pool.Exec(ctx, `
			DELETE FROM folders WHERE id = $1
		`, fixture.secondFolderID); err != nil {
			t.Fatalf("remove target folder: %v", err)
		}
		if _, err := database.Pool.Exec(ctx, `
			DELETE FROM file_annotations WHERE id = $1
		`, fixture.rejectedAnnotation); err != nil {
			t.Fatalf("remove target annotation: %v", err)
		}

		putsBefore := len(store.puts)
		merged, err := importBundle(ctx, bundle, mixedWorkspace, RestoreModeMergeConservative)
		if err != nil {
			t.Fatalf("merge into populated workspace: %v", err)
		}
		populatedMerge = merged
		if merged.Replayed ||
			merged.Mode != RestoreModeMergeConservative ||
			merged.Merge == nil {
			t.Fatalf("merge result = %+v", merged)
		}
		wantInserted := map[string]int64{
			MergeObjectFolder:         1,
			MergeObjectFileAnnotation: 1,
		}
		wantSkipped := map[string]int64{
			MergeObjectFolder:         1,
			MergeObjectFile:           2,
			MergeObjectFileAnnotation: 2,
			MergeObjectMemoryEvent:    1,
			MergeObjectTask:           1,
			MergeObjectCheckpoint:     1,
			MergeObjectCheckpointRef:  4,
		}
		wantSkipReasons := map[string]int64{
			MergeSkipIdentical:      6,
			MergeSkipContentPresent: 1,
			MergeSkipParentSkipped:  5,
		}
		if !reflect.DeepEqual(merged.Merge.Inserted, wantInserted) ||
			!reflect.DeepEqual(merged.Merge.Skipped, wantSkipped) ||
			!reflect.DeepEqual(merged.Merge.SkippedByReason, wantSkipReasons) ||
			merged.Merge.ConflictTotal != 2 ||
			merged.Merge.ConflictsTruncated {
			t.Fatalf("populated merge summary = %+v", merged.Merge)
		}
		wantConflicts := []Conflict{
			mergeConflict(
				"global_id",
				MergeObjectCheckpoint,
				fixture.checkpointID.String(),
			),
			mergeConflict(
				"global_id",
				MergeObjectMemory,
				fixture.memoryID.String(),
			),
		}
		if !reflect.DeepEqual(merged.Merge.Conflicts, wantConflicts) {
			t.Fatalf("merge conflicts = %+v, want %+v", merged.Merge.Conflicts, wantConflicts)
		}
		if len(store.puts) != putsBefore {
			t.Fatalf("merge uploaded unexpected objects: %v", store.puts[putsBefore:])
		}

		// Conflicts never overwrite: both locally edited rows keep their
		// divergent content.
		var memoryContent, memorySHA string
		if err := database.Pool.QueryRow(ctx, `
			SELECT content, content_sha256 FROM memories WHERE id = $1
		`, fixture.memoryID).Scan(&memoryContent, &memorySHA); err != nil {
			t.Fatalf("load merged memory: %v", err)
		}
		if memoryContent != localMemoryContent || memorySHA != localMemorySHA {
			t.Fatalf("memory content = %q sha = %q", memoryContent, memorySHA)
		}
		var checkpointSHA string
		if err := database.Pool.QueryRow(ctx, `
			SELECT payload_sha256 FROM task_checkpoints WHERE id = $1
		`, fixture.checkpointID).Scan(&checkpointSHA); err != nil {
			t.Fatalf("load merged checkpoint: %v", err)
		}
		if checkpointSHA != localCheckpointSHA {
			t.Fatalf("checkpoint payload_sha256 = %q, want %q", checkpointSHA, localCheckpointSHA)
		}
		// Shared-content files stay absent (content_present), while absent
		// folders and annotations are restored.
		if rows := countQuery(
			`SELECT count(*) FROM files WHERE id = $1`,
			fixture.secondFileID,
		); rows != 0 {
			t.Fatalf("content_present file was written: %d rows", rows)
		}
		var folderOwner uuid.UUID
		if err := database.Pool.QueryRow(ctx, `
			SELECT user_id FROM folders WHERE id = $1
		`, fixture.secondFolderID).Scan(&folderOwner); err != nil {
			t.Fatalf("load restored folder: %v", err)
		}
		if folderOwner != mixedUser {
			t.Fatalf("restored folder owner = %s", folderOwner)
		}
		var annotationStatus string
		if err := database.Pool.QueryRow(ctx, `
			SELECT status FROM file_annotations WHERE id = $1
		`, fixture.rejectedAnnotation).Scan(&annotationStatus); err != nil {
			t.Fatalf("load restored annotation: %v", err)
		}
		if annotationStatus != "rejected" {
			t.Fatalf("restored annotation status = %q", annotationStatus)
		}
		// The durable object ledger records every decision in the same
		// transaction as the merged state.
		var inserted, skipped, conflicted int
		if err := database.Pool.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE outcome = 'inserted'),
			       count(*) FILTER (WHERE outcome = 'skipped'),
			       count(*) FILTER (WHERE outcome = 'conflict')
			  FROM workspace_import_objects
			 WHERE target_workspace_id = $1 AND bundle_id = $2
		`, mixedWorkspace, fixture.bundleID).Scan(
			&inserted,
			&skipped,
			&conflicted,
		); err != nil {
			t.Fatalf("load merge object ledger: %v", err)
		}
		if inserted != 2 || skipped != 12 || conflicted != 2 {
			t.Fatalf(
				"object ledger inserted=%d skipped=%d conflict=%d",
				inserted,
				skipped,
				conflicted,
			)
		}
	})

	// A retry after a successful merge is a replay whose structured summary is
	// reconstructed from the durable ledger and equals the original result.
	t.Run("replay idempotency", func(t *testing.T) {
		if populatedMerge == nil || mixedBundle == nil {
			t.Fatal("populated target merge did not run")
		}
		putsBefore := len(store.puts)
		replayed, err := importBundle(
			ctx,
			mixedBundle,
			mixedWorkspace,
			RestoreModeMergeConservative,
		)
		if err != nil {
			t.Fatalf("replay merge import: %v", err)
		}
		if !replayed.Replayed ||
			replayed.Mode != RestoreModeMergeConservative ||
			replayed.ImportedAt != populatedMerge.ImportedAt ||
			replayed.ArchiveSHA256 != populatedMerge.ArchiveSHA256 {
			t.Fatalf("replay result = %+v, first = %+v", replayed, populatedMerge)
		}
		if !reflect.DeepEqual(replayed.Merge, populatedMerge.Merge) {
			t.Fatalf(
				"replayed merge summary = %+v, want %+v",
				replayed.Merge,
				populatedMerge.Merge,
			)
		}
		if len(store.puts) != putsBefore {
			t.Fatalf("replay uploaded objects: %v", store.puts[putsBefore:])
		}
		if rows := countQuery(
			`SELECT count(*) FROM workspace_import_objects
			  WHERE target_workspace_id = $1`,
			mixedWorkspace,
		); rows != 16 {
			t.Fatalf("replay changed the object ledger: %d rows", rows)
		}
	})

	// The same bundle cannot silently switch restore modes once committed.
	t.Run("restore mode mismatch fails closed", func(t *testing.T) {
		bundle, fixture := exportBundle(
			"merge-mode-source",
			modeSourceUser,
			modeSourceWorkspace,
		)
		if _, err := importBundle(ctx, bundle, modeWorkspace, RestoreModeFresh); err != nil {
			t.Fatalf("fresh seed import: %v", err)
		}
		_, err := importBundle(ctx, bundle, modeWorkspace, RestoreModeMergeConservative)
		var conflictErr *ConflictError
		if !errors.As(err, &conflictErr) {
			t.Fatalf("mode mismatch error = %v", err)
		}
		if !slices.ContainsFunc(conflictErr.Conflicts, func(value Conflict) bool {
			return value.Kind == "restore_mode" &&
				value.Resource == "workspace_imports" &&
				value.Value == fixture.bundleID.String()
		}) {
			t.Fatalf("mode mismatch conflicts = %+v", conflictErr.Conflicts)
		}
		replayedFresh, err := importBundle(ctx, bundle, modeWorkspace, RestoreModeFresh)
		if err != nil || !replayedFresh.Replayed ||
			replayedFresh.Mode != RestoreModeFresh ||
			replayedFresh.Merge != nil {
			t.Fatalf("fresh replay after mismatch = %+v err=%v", replayedFresh, err)
		}
		if rows := countQuery(
			`SELECT count(*) FROM workspace_import_objects WHERE target_workspace_id = $1`,
			modeWorkspace,
		); rows != 0 {
			t.Fatalf("fresh import wrote object ledger rows: %d", rows)
		}
	})

	// More distinct conflicts than the bounded detail budget can enumerate
	// aborts the whole merge before anything is written.
	t.Run("conflict budget aborts merge", func(t *testing.T) {
		if mixedBundle == nil {
			t.Fatal("populated target merge did not run")
		}
		const overflowFolders = MaxConflictDetails + 5
		now := time.Date(2026, time.July, 29, 13, 0, 0, 0, time.UTC)
		records := make([]workspacebundle.FolderRecord, overflowFolders)
		seedRows := make([][]any, overflowFolders)
		for index := range overflowFolders {
			id := uuid.New()
			folderPath := fmt.Sprintf("/overflow-%03d", index)
			records[index] = workspacebundle.FolderRecord{
				ID:        id,
				Path:      folderPath,
				Name:      fmt.Sprintf("overflow-%03d", index),
				CreatedAt: now,
				UpdatedAt: now,
			}
			// Distinct IDs with identical paths force path conflicts instead
			// of identity matches.
			seedRows[index] = []any{
				uuid.New(),
				overflowUser,
				nil,
				folderPath,
				records[index].Name,
				now,
				now,
			}
		}
		if copied, err := database.Pool.CopyFrom(
			ctx,
			pgx.Identifier{"folders"},
			[]string{
				"id",
				"user_id",
				"parent_id",
				"path",
				"name",
				"created_at",
				"updated_at",
			},
			pgx.CopyFromRows(seedRows),
		); err != nil || copied != overflowFolders {
			t.Fatalf("seed overflow folders: copied=%d err=%v", copied, err)
		}

		overflowBundle := appendBundleFolders(t, mixedBundle, records)
		reader := bytes.NewReader(overflowBundle)
		_, err := service.Import(ctx, ImportRequest{
			WorkspaceID: overflowWorkspace,
			Mode:        RestoreModeMergeConservative,
			Reader:      reader,
			Size:        int64(reader.Len()),
		})
		var conflictErr *ConflictError
		if !errors.As(err, &conflictErr) ||
			!conflictErr.Truncated ||
			len(conflictErr.Conflicts) != MaxConflictDetails ||
			conflictErr.Total != MaxConflictDetails+1 {
			t.Fatalf("overflow merge error = %v", err)
		}
		if rows := countQuery(
			`SELECT count(*) FROM workspace_imports WHERE target_workspace_id = $1`,
			overflowWorkspace,
		); rows != 0 {
			t.Fatalf("aborted merge wrote import ledger rows: %d", rows)
		}
		if rows := countQuery(
			`SELECT count(*) FROM workspace_import_objects WHERE target_workspace_id = $1`,
			overflowWorkspace,
		); rows != 0 {
			t.Fatalf("aborted merge wrote object ledger rows: %d", rows)
		}
		if rows := countQuery(
			`SELECT count(*) FROM folders WHERE user_id = $1`,
			overflowUser,
		); rows != overflowFolders {
			t.Fatalf("aborted merge changed folder rows: %d", rows)
		}
	})
}

// appendBundleFolders rewrites an archive's folders index with extra
// top-level folder records appended (patching the manifest index descriptor
// and regenerating checksums), keeping the result a valid bundle. It mirrors
// historicalV1Bundle's in-memory ZIP rewrite pattern.
func appendBundleFolders(
	t *testing.T,
	raw []byte,
	extra []workspacebundle.FolderRecord,
) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open bundle for folder append: %v", err)
	}
	entries := make([]transferZIPEntry, 0, len(reader.File))
	for _, archiveFile := range reader.File {
		stream, err := archiveFile.Open()
		if err != nil {
			t.Fatalf("open bundle entry %s: %v", archiveFile.Name, err)
		}
		data, err := io.ReadAll(stream)
		closeErr := stream.Close()
		if err != nil {
			t.Fatalf("read bundle entry %s: %v", archiveFile.Name, err)
		}
		if closeErr != nil {
			t.Fatalf("close bundle entry %s: %v", archiveFile.Name, closeErr)
		}
		header := archiveFile.FileHeader
		header.CompressedSize64 = 0
		header.UncompressedSize64 = 0
		header.CRC32 = 0
		entries = append(entries, transferZIPEntry{header: header, data: data})
	}

	var extraLines bytes.Buffer
	for _, record := range extra {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode appended folder record: %v", err)
		}
		extraLines.Write(line)
		extraLines.WriteByte('\n')
	}
	found := false
	for index := range entries {
		switch entries[index].header.Name {
		case workspacebundle.ManifestPath:
			var manifest map[string]any
			if err := json.Unmarshal(entries[index].data, &manifest); err != nil {
				t.Fatalf("decode manifest for folder append: %v", err)
			}
			indexes, ok := manifest["indexes"].(map[string]any)
			if !ok {
				t.Fatalf("manifest has no index catalog")
			}
			folders, ok := indexes["folders"].(map[string]any)
			if !ok {
				t.Fatalf("manifest has no folders index descriptor")
			}
			count, ok := folders["count"].(float64)
			if !ok {
				t.Fatalf("folders index descriptor has no count")
			}
			folders["count"] = count + float64(len(extra))
			patched, err := json.Marshal(manifest)
			if err != nil {
				t.Fatalf("encode patched manifest: %v", err)
			}
			entries[index].data = patched
		case workspacebundle.FoldersIndexPath:
			entries[index].data = append(entries[index].data, extraLines.Bytes()...)
			found = true
		}
	}
	if !found {
		t.Fatalf("bundle has no %s entry", workspacebundle.FoldersIndexPath)
	}

	checksumEntries := make([]transferZIPEntry, 0, len(entries)-1)
	for _, entry := range entries {
		if entry.header.Name == workspacebundle.ChecksumsPath {
			continue
		}
		checksumEntries = append(checksumEntries, entry)
	}
	sort.Slice(checksumEntries, func(left, right int) bool {
		return checksumEntries[left].header.Name < checksumEntries[right].header.Name
	})
	var checksumLines strings.Builder
	for _, entry := range checksumEntries {
		fmt.Fprintf(
			&checksumLines,
			"%s\t%d\t%s\n",
			digestBytes(entry.data),
			len(entry.data),
			entry.header.Name,
		)
	}
	for index := range entries {
		if entries[index].header.Name == workspacebundle.ChecksumsPath {
			entries[index].data = []byte(checksumLines.String())
		}
	}

	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		stream, err := writer.CreateHeader(&entry.header)
		if err != nil {
			t.Fatalf("create rewritten entry %s: %v", entry.header.Name, err)
		}
		if _, err := stream.Write(entry.data); err != nil {
			t.Fatalf("write rewritten entry %s: %v", entry.header.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close rewritten bundle: %v", err)
	}
	return output.Bytes()
}
