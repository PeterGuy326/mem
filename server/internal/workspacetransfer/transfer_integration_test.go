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
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/enrichmentkey"
	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/PeterGuy326/mem/server/internal/memory"
	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestWorkspaceTransferPostgres exercises the real migration, repeatable-read
// export, conflict preflight, target remapping, deferred checkpoint lineage,
// and import ledger. Blob I/O is intentionally in-memory so this test does not
// require MinIO.
//
//	MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_transfer_test?sslmode=disable \
//	  go test ./internal/workspacetransfer -run TestWorkspaceTransferPostgres
func TestWorkspaceTransferPostgres(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	database, err := memdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	sourceUser, sourceWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"transfer-source",
	)
	targetUser, targetWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"transfer-target",
	)
	failureUser, failureWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"transfer-failure-target",
	)
	legacyUser, legacyWorkspace := createTransferTenant(
		t,
		ctx,
		database,
		"transfer-v1-target",
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			15*time.Second,
		)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(cleanupCtx, `
			DELETE FROM users WHERE id = $1 OR id = $2 OR id = $3 OR id = $4
		`, sourceUser, targetUser, failureUser, legacyUser); err != nil {
			t.Errorf("cleanup transfer tenants: %v", err)
		}
	})

	store := newFakeObjectStore()
	fixture := seedTransferSource(
		t,
		ctx,
		database,
		store,
		sourceUser,
		sourceWorkspace,
	)
	fixedNow := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	service := New(database.Pool, store, Options{
		Exporter:        "memd-test",
		ExporterVersion: "v1",
		Now:             func() time.Time { return fixedNow },
	})
	var bundle bytes.Buffer
	exported, err := service.Export(ctx, ExportRequest{
		WorkspaceID: sourceWorkspace,
		BundleID:    fixture.bundleID,
		Writer:      &bundle,
	})
	if err != nil {
		t.Fatalf("export source workspace: %v", err)
	}
	if exported.BundleID != fixture.bundleID ||
		exported.Counts.Files != 2 ||
		exported.Counts.Blobs != 1 ||
		exported.Counts.Memories != 1 ||
		exported.Counts.MemoryEvents != 1 ||
		exported.Counts.Checkpoints != 2 {
		t.Fatalf("export result = %+v", exported)
	}

	// The source still owns the portable UUIDs. A preserve-ID restore must
	// reject every collision before touching object storage.
	conflictingReader := bytes.NewReader(bundle.Bytes())
	_, err = service.Import(ctx, ImportRequest{
		WorkspaceID: targetWorkspace,
		Mode:        RestoreModeFresh,
		Reader:      conflictingReader,
		Size:        int64(conflictingReader.Len()),
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || len(conflict.Conflicts) == 0 {
		t.Fatalf("global collision error = %v", err)
	}
	if !slices.ContainsFunc(conflict.Conflicts, func(value Conflict) bool {
		return value.Kind == "global_id" &&
			value.Resource == "file_annotation" &&
			value.Value == fixture.acceptedAnnotation.String()
	}) {
		t.Fatalf("annotation UUID collision missing from preflight: %+v", conflict.Conflicts)
	}
	if len(store.puts) != 0 {
		t.Fatalf("preflight conflict uploaded objects: %v", store.puts)
	}

	// Simulate transfer to an isolated installation: source rows disappear,
	// while the exported archive and source object remain available.
	if _, err := database.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, sourceUser); err != nil {
		t.Fatalf("remove source tenant: %v", err)
	}

	// Historical v1 archives remain importable into a live current-schema
	// database. They promote legacy tags to user provenance, but never invent
	// v2 source/review provenance or project an unreviewed legacy summary.
	legacyBundle := historicalV1Bundle(t, bundle.Bytes(), fixture.fileID)
	legacyStore := newFakeObjectStore()
	legacyService := New(database.Pool, legacyStore, Options{
		Exporter:        "memd-test",
		ExporterVersion: "v1",
		Now:             func() time.Time { return fixedNow },
	})
	legacyReader := bytes.NewReader(legacyBundle)
	legacyImported, err := legacyService.Import(ctx, ImportRequest{
		WorkspaceID: legacyWorkspace,
		Mode:        RestoreModeFresh,
		Reader:      legacyReader,
		Size:        int64(legacyReader.Len()),
	})
	if err != nil {
		t.Fatalf("import historical v1 workspace: %v", err)
	}
	if legacyImported.Replayed || legacyImported.BundleID != fixture.bundleID {
		t.Fatalf("historical v1 import result = %+v", legacyImported)
	}
	var (
		legacySchemaVersion int
		legacyUserTags      []string
		legacySource        []byte
		legacySummary       *string
		legacyCaption       *string
		legacyIndexStatus   string
		legacyAnnotations   int
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT schema_version
		  FROM workspace_imports
		 WHERE target_workspace_id = $1 AND bundle_id = $2
	`, legacyWorkspace, fixture.bundleID).Scan(&legacySchemaVersion); err != nil {
		t.Fatalf("load historical v1 import ledger: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		SELECT user_tags, source_metadata, summary, caption, index_status
		  FROM files
		 WHERE id = $1 AND user_id = $2
	`, fixture.fileID, legacyUser).Scan(
		&legacyUserTags,
		&legacySource,
		&legacySummary,
		&legacyCaption,
		&legacyIndexStatus,
	); err != nil {
		t.Fatalf("load historical v1 imported file: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM file_annotations
		 WHERE file_id = $1
	`, fixture.fileID).Scan(&legacyAnnotations); err != nil {
		t.Fatalf("count historical v1 annotations: %v", err)
	}
	if legacySchemaVersion != workspacebundle.SchemaVersionV1 ||
		!slices.Equal(legacyUserTags, []string{"agent", "reviewed"}) ||
		string(legacySource) != "{}" ||
		legacySummary != nil ||
		legacyCaption == nil ||
		*legacyCaption != "historical visual caption" ||
		legacyIndexStatus != "pending" ||
		legacyAnnotations != 0 ||
		len(legacyStore.puts) != 2 {
		t.Fatalf(
			"historical v1 state schema=%d tags=%v source=%s summary=%v caption=%v status=%s annotations=%d puts=%v",
			legacySchemaVersion,
			legacyUserTags,
			legacySource,
			legacySummary,
			legacyCaption,
			legacyIndexStatus,
			legacyAnnotations,
			legacyStore.puts,
		)
	}
	if _, err := database.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, legacyUser); err != nil {
		t.Fatalf("remove historical v1 target tenant: %v", err)
	}

	// If PostgreSQL fails after the blob has been accepted, the transaction
	// rolls back and every object uploaded by this attempt is compensated.
	failureCtx, cancelFailure := context.WithCancel(context.Background())
	store.afterPut = cancelFailure
	failureReader := bytes.NewReader(bundle.Bytes())
	_, err = service.Import(failureCtx, ImportRequest{
		WorkspaceID: failureWorkspace,
		Mode:        RestoreModeFresh,
		Reader:      failureReader,
		Size:        int64(failureReader.Len()),
	})
	store.afterPut = nil
	cancelFailure()
	if err == nil {
		t.Fatal("database failure after upload unexpectedly succeeded")
	}
	var failedRows, failedLedger int
	if err := database.Pool.QueryRow(ctx, `
		SELECT count(*) FROM files WHERE id = ANY($1::uuid[])
	`, []uuid.UUID{fixture.fileID, fixture.secondFileID}).Scan(&failedRows); err != nil {
		t.Fatalf("count rows after failed import: %v", err)
	}
	if err := database.Pool.QueryRow(ctx, `
		SELECT count(*) FROM workspace_imports WHERE target_workspace_id = $1
	`, failureWorkspace).Scan(&failedLedger); err != nil {
		t.Fatalf("count ledger after failed import: %v", err)
	}
	if failedRows != 0 || failedLedger != 0 || len(store.deletes) != 2 {
		t.Fatalf(
			"failed import rows=%d ledger=%d deletes=%v objects=%v",
			failedRows,
			failedLedger,
			store.deletes,
			store.objects,
		)
	}

	type importCall struct {
		result *ImportResult
		err    error
	}
	startImports := make(chan struct{})
	importCalls := make(chan importCall, 2)
	for range 2 {
		go func() {
			<-startImports
			reader := bytes.NewReader(bundle.Bytes())
			result, callErr := service.Import(ctx, ImportRequest{
				WorkspaceID: targetWorkspace,
				Mode:        RestoreModeFresh,
				Reader:      reader,
				Size:        int64(reader.Len()),
			})
			importCalls <- importCall{result: result, err: callErr}
		}()
	}
	close(startImports)
	var (
		imported    *ImportResult
		replayCount int
	)
	for range 2 {
		call := <-importCalls
		if call.err != nil {
			t.Fatalf("concurrent target import: %v", call.err)
		}
		if call.result.Replayed {
			replayCount++
		} else {
			imported = call.result
		}
	}
	if imported == nil ||
		replayCount != 1 ||
		imported.BundleID != fixture.bundleID ||
		imported.SourceWorkspaceID != sourceWorkspace ||
		len(store.puts) != 4 {
		t.Fatalf(
			"import result=%+v replays=%d puts=%v",
			imported,
			replayCount,
			store.puts,
		)
	}
	assertImportedState(
		t,
		ctx,
		database,
		store,
		targetUser,
		targetWorkspace,
		fixture,
	)

	retryReader := bytes.NewReader(bundle.Bytes())
	replayed, err := service.Import(ctx, ImportRequest{
		WorkspaceID: targetWorkspace,
		Mode:        RestoreModeFresh,
		Reader:      retryReader,
		Size:        int64(retryReader.Len()),
	})
	if err != nil {
		t.Fatalf("replay target import: %v", err)
	}
	if !replayed.Replayed || replayed.ImportedAt != imported.ImportedAt ||
		len(store.puts) != 4 {
		t.Fatalf("replay=%+v first=%+v puts=%v", replayed, imported, store.puts)
	}

	// Exercise the ambiguous-commit verifier against a real pool connection.
	// A matching durable ledger is success and must preserve uploaded objects.
	commitErr := errors.New("commit acknowledgement lost")
	committedCleanupCalls := 0
	verified, err := service.resolveAmbiguousImportCommit(
		importTarget{
			WorkspaceID: targetWorkspace,
			OwnerID:     targetUser,
		},
		importReplay{
			BundleID:          fixture.bundleID,
			ArchiveSHA256:     imported.ArchiveSHA256,
			SourceWorkspaceID: sourceWorkspace,
		},
		imported.Counts,
		commitErr,
		func(cause error) error {
			committedCleanupCalls++
			return cause
		},
	)
	if err != nil || verified == nil || !verified.Replayed ||
		verified.BundleID != fixture.bundleID ||
		verified.ArchiveSHA256 != imported.ArchiveSHA256 ||
		verified.ImportedAt != imported.ImportedAt ||
		committedCleanupCalls != 0 {
		t.Fatalf(
			"verify committed import result=%+v cleanup=%d err=%v",
			verified,
			committedCleanupCalls,
			err,
		)
	}

	// Once the same workspace lock is acquired, an empty ledger confirms that
	// PostgreSQL did not commit and compensation is safe.
	absentCleanupCalls := 0
	absentCommitErr := errors.New("commit rejected")
	verified, err = service.resolveAmbiguousImportCommit(
		importTarget{
			WorkspaceID: failureWorkspace,
			OwnerID:     failureUser,
		},
		importReplay{
			BundleID:          uuid.New(),
			ArchiveSHA256:     strings.Repeat("f", 64),
			SourceWorkspaceID: sourceWorkspace,
		},
		workspacebundle.ObjectCounts{},
		absentCommitErr,
		func(cause error) error {
			absentCleanupCalls++
			return cause
		},
	)
	if verified != nil || !errors.Is(err, absentCommitErr) ||
		absentCleanupCalls != 1 {
		t.Fatalf(
			"verify absent import result=%+v cleanup=%d err=%v",
			verified,
			absentCleanupCalls,
			err,
		)
	}

	// The restored target is itself exportable, proving imported local storage
	// keys and target-bound request hashes form another valid bundle.
	var targetBundle bytes.Buffer
	if _, err := service.Export(ctx, ExportRequest{
		WorkspaceID: targetWorkspace,
		Writer:      &targetBundle,
	}); err != nil {
		t.Fatalf("re-export imported workspace: %v", err)
	}
	targetReader := bytes.NewReader(targetBundle.Bytes())
	targetArchive, err := workspacebundle.Open(
		targetReader,
		int64(targetReader.Len()),
		workspacebundle.ReaderOptions{},
	)
	if err != nil {
		t.Fatalf("open re-exported workspace: %v", err)
	}
	if targetArchive.Manifest.Source.WorkspaceID != targetWorkspace ||
		len(targetArchive.Files) != 2 ||
		len(targetArchive.Blobs) != 1 ||
		len(targetArchive.MemoryEvents) != 1 {
		t.Fatalf("re-exported archive = %+v", targetArchive.BundleData)
	}
	reexportedFiles := make(map[uuid.UUID]struct{}, len(targetArchive.Files))
	for _, file := range targetArchive.Files {
		reexportedFiles[file.ID] = struct{}{}
	}
	if _, ok := reexportedFiles[fixture.fileID]; !ok {
		t.Fatalf("re-export omitted first shared-content file %s", fixture.fileID)
	}
	if _, ok := reexportedFiles[fixture.secondFileID]; !ok {
		t.Fatalf("re-export omitted second shared-content file %s", fixture.secondFileID)
	}

	assertBoundedPreflightConflicts(
		t,
		ctx,
		database,
		failureUser,
		failureWorkspace,
	)
}

type transferZIPEntry struct {
	header zip.FileHeader
	data   []byte
}

func historicalV1Bundle(t *testing.T, raw []byte, captionFileID uuid.UUID) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open current bundle for v1 downgrade: %v", err)
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

	for index := range entries {
		switch entries[index].header.Name {
		case workspacebundle.ManifestPath:
			var manifest map[string]any
			if err := json.Unmarshal(entries[index].data, &manifest); err != nil {
				t.Fatalf("decode current manifest: %v", err)
			}
			manifest["schema_version"] = float64(workspacebundle.SchemaVersionV1)
			manifest["exclusions"] = workspacebundle.ExclusionsV1()
			entries[index].data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatalf("encode historical v1 manifest: %v", err)
			}
		case workspacebundle.FilesIndexPath:
			lines := bytes.Split(
				bytes.TrimSuffix(entries[index].data, []byte("\n")),
				[]byte("\n"),
			)
			for lineIndex := range lines {
				var record map[string]any
				if err := json.Unmarshal(lines[lineIndex], &record); err != nil {
					t.Fatalf("decode current file record: %v", err)
				}
				delete(record, "user_tags")
				delete(record, "geo")
				delete(record, "source_metadata")
				delete(record, "annotations")
				if record["id"] == captionFileID.String() {
					record["caption"] = " \u00a0historical visual caption\u3000 "
				}
				lines[lineIndex], err = json.Marshal(record)
				if err != nil {
					t.Fatalf("encode historical v1 file record: %v", err)
				}
			}
			entries[index].data = append(bytes.Join(lines, []byte("\n")), '\n')
		}
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
			t.Fatalf("create historical v1 entry %s: %v", entry.header.Name, err)
		}
		if _, err := stream.Write(entry.data); err != nil {
			t.Fatalf("write historical v1 entry %s: %v", entry.header.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close historical v1 bundle: %v", err)
	}
	return output.Bytes()
}

type transferFixture struct {
	bundleID            uuid.UUID
	folderID            uuid.UUID
	secondFolderID      uuid.UUID
	fileID              uuid.UUID
	secondFileID        uuid.UUID
	acceptedAnnotation  uuid.UUID
	acceptedDescription uuid.UUID
	rejectedAnnotation  uuid.UUID
	fileSHA             string
	blob                []byte
	memoryID            uuid.UUID
	memoryEventID       uuid.UUID
	memoryOriginRequest string
	memoryKey           string
	memoryEventKey      string
	eventAt             time.Time
	taskID              uuid.UUID
	baseCheckpointID    uuid.UUID
	checkpointID        uuid.UUID
	checkpointOrigin    string
	checkpointKey       string
	handoffPayload      handoff.HandoffV1
}

func seedTransferSource(
	t *testing.T,
	ctx context.Context,
	database *memdb.DB,
	store *fakeObjectStore,
	userID, workspaceID uuid.UUID,
) transferFixture {
	t.Helper()
	now := time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC)
	folderID := uuid.New()
	secondFolderID := uuid.New()
	fileID := uuid.New()
	secondFileID := uuid.New()
	acceptedAnnotation := uuid.New()
	acceptedDescription := uuid.New()
	rejectedAnnotation := uuid.New()
	blob := []byte("source object for a portable agent workspace\n")
	fileSHA := digestBytes(blob)
	storageKey := "users/" + userID.String() + "/" + fileID.String() + "/state.txt"
	secondStorageKey := "users/" + userID.String() + "/" + secondFileID.String() + "/state-copy.txt"
	store.objects[storageKey] = append([]byte(nil), blob...)
	store.objects[secondStorageKey] = append([]byte(nil), blob...)
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO folders (
			id, user_id, parent_id, path, name, created_at, updated_at
		)
		VALUES
			($1, $3, NULL, '/Project', 'Project', $4, $4),
			($2, $3, NULL, '/Archive', 'Archive', $4, $4)
	`, folderID, secondFolderID, userID, now); err != nil {
		t.Fatalf("insert source folders: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO files (
			id, user_id, name, path, folder_id, size, sha256, mime,
			storage_key, summary, caption, tags, user_tags, timeline_at, geo,
			source_metadata, processor_metadata, index_status, created_at, updated_at
		)
		VALUES
			(
				$1, $3, 'state.txt', '/Project', $4, $6, $7, 'text/plain',
				$8, 'portable summary', NULL, ARRAY['agent','reviewed'], ARRAY['agent'],
				$10, point(121.4737,31.2304),
				'{"captured_at":"2026-07-28T17:00:00+08:00","location":{"accuracy_m":5,"label":"Shanghai","lat":31.2304,"lon":121.4737},"source_kind":"mobile","source_name":"camera sync"}'::jsonb,
				'{"processor":"text"}'::jsonb, 'ready', $10, $10
			),
			(
				$2, $3, 'state-copy.txt', '/Archive', $5, $6, $7, 'text/plain',
				$9, 'portable copy', NULL, ARRAY['archive'], ARRAY['archive'],
				$10, NULL, '{"source_kind":"import"}'::jsonb, '{}'::jsonb,
				'ready', $10, $10
			)
	`, fileID, secondFileID, userID, folderID, secondFolderID, len(blob),
		fileSHA, storageKey, secondStorageKey, now); err != nil {
		t.Fatalf("insert source files with shared content: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO file_annotations (
			id, file_id, stable_key, kind, value_text, confidence,
			source, provider, processor, analysis_version, status,
			state_version, decided_by_user_id, decided_at, created_at, updated_at
		)
		VALUES
			($1,$4,$6,'tag','reviewed',0.91,'model','fixture:vlm','text',
			 'file-enrichment-v1','accepted',2,$5,$8,$8,$8),
			($2,$4,$7,'tag','discarded',0.42,'model','fixture:vlm','text',
			 'file-enrichment-v1','rejected',2,$5,$8,$8,$8),
			($3,$4,$9,'description','portable summary',0.84,'model',
			 'fixture:vlm','text','file-enrichment-v1','accepted',2,$5,$8,$8,$8)
	`, acceptedAnnotation, rejectedAnnotation, acceptedDescription, fileID, userID,
		enrichmentkey.Stable("tag", "model", "file-enrichment-v1", "reviewed"),
		enrichmentkey.Stable("tag", "model", "file-enrichment-v1", "discarded"),
		now,
		enrichmentkey.Stable(
			"description",
			"model",
			"file-enrichment-v1",
			"portable summary",
		)); err != nil {
		t.Fatalf("insert source file annotations: %v", err)
	}

	memoryService := memory.New(database.Pool)
	tokenID := uuid.New()
	memoryKey := "memory-" + uuid.NewString()
	remembered, err := memoryService.Remember(ctx, memory.Command{
		WorkspaceID:      workspaceID,
		CreatedByUserID:  &userID,
		CreatedByTokenID: &tokenID,
		Kind:             memory.KindDecision,
		Content:          "Preserve portable IDs and immutable lineage.",
		Attributes:       json.RawMessage(`{"confidence":"confirmed"}`),
		Path:             "/Project",
		EventAt:          &now,
		SourceType:       "file",
		SourceRef:        "state.txt",
		SourceFileID:     &fileID,
		SourceFileSHA256: fileSHA,
		SourceLocator:    json.RawMessage(`{"line":1}`),
		ProducerAgent:    "claude-code",
		ProducerSession:  "source-session",
		ProducerTask:     "portable-task",
		IdempotencyKey:   memoryKey,
	})
	if err != nil {
		t.Fatalf("remember source memory: %v", err)
	}
	eventToken := uuid.New()
	memoryEventKey := "pin-" + uuid.NewString()
	mutated, err := memoryService.Feedback(ctx, memory.FeedbackCommand{
		WorkspaceID:     workspaceID,
		MemoryID:        remembered.Memory.ID,
		ActorUserID:     &userID,
		ActorTokenID:    &eventToken,
		Action:          memory.FeedbackPin,
		IdempotencyKey:  memoryEventKey,
		ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("pin source memory: %v", err)
	}

	required := true
	firstPayload := handoff.HandoffV1{
		Contract:       handoff.ContractName,
		SchemaVersion:  handoff.SchemaVersionV1,
		CheckpointKind: handoff.CheckpointKindCheckpoint,
		TaskKey:        "portable-task",
		ScopePath:      "/Project",
		State: handoff.StateV1{
			Status: handoff.TaskStatusReady,
			Goal:   "Move the task between Agent hosts.",
			Progress: handoff.ProgressV1{
				Summary:   "Source workspace is ready to transfer.",
				Completed: []string{"Stored state in mem."},
			},
			Decisions: []handoff.DecisionV1{{
				Summary: "Preserve portable IDs.",
				References: []string{
					"mem://memories/" + remembered.Memory.ID.String(),
				},
			}},
			NextSteps:     []handoff.NextStepV1{},
			Blockers:      []handoff.BlockerV1{},
			OpenQuestions: []string{},
			Artifacts: []handoff.ArtifactV1{{
				URI:      "mem://files/" + fileID.String(),
				Role:     "workspace-state",
				SHA256:   fileSHA,
				Required: &required,
			}},
		},
		Producer: handoff.ProducerV1{
			AgentID:   "claude-code",
			SessionID: "source-session",
		},
	}
	handoffService := handoff.New(database.Pool)
	firstCheckpoint, err := handoffService.Checkpoint(ctx, handoff.CheckpointCommand{
		WorkspaceID:      workspaceID,
		CreatedByUserID:  &userID,
		CreatedByTokenID: &tokenID,
		TaskKey:          "portable-task",
		IdempotencyKey:   "checkpoint-base-" + uuid.NewString(),
		Handoff:          firstPayload,
	})
	if err != nil {
		t.Fatalf("create source base checkpoint: %v", err)
	}
	handoffPayload := firstPayload
	handoffPayload.CheckpointKind = handoff.CheckpointKindHandoff
	handoffPayload.BaseCheckpointID = &firstCheckpoint.Checkpoint.ID
	handoffPayload.State.Progress.Summary = "Source workspace is ready for another Agent."
	checkpointKey := "checkpoint-" + uuid.NewString()
	checkpoint, err := handoffService.Checkpoint(ctx, handoff.CheckpointCommand{
		WorkspaceID:      workspaceID,
		CreatedByUserID:  &userID,
		CreatedByTokenID: &tokenID,
		TaskKey:          "portable-task",
		IdempotencyKey:   checkpointKey,
		Handoff:          handoffPayload,
	})
	if err != nil {
		t.Fatalf("checkpoint source task: %v", err)
	}
	return transferFixture{
		bundleID:            uuid.New(),
		folderID:            folderID,
		secondFolderID:      secondFolderID,
		fileID:              fileID,
		secondFileID:        secondFileID,
		acceptedAnnotation:  acceptedAnnotation,
		acceptedDescription: acceptedDescription,
		rejectedAnnotation:  rejectedAnnotation,
		fileSHA:             fileSHA,
		blob:                blob,
		memoryID:            remembered.Memory.ID,
		memoryEventID:       mutated.Event.ID,
		memoryOriginRequest: remembered.Memory.RequestSHA256,
		memoryKey:           memoryKey,
		memoryEventKey:      memoryEventKey,
		eventAt:             now,
		taskID:              checkpoint.Checkpoint.TaskID,
		baseCheckpointID:    firstCheckpoint.Checkpoint.ID,
		checkpointID:        checkpoint.Checkpoint.ID,
		checkpointOrigin:    checkpoint.Checkpoint.RequestSHA256,
		checkpointKey:       checkpointKey,
		handoffPayload:      handoffPayload,
	}
}

func assertImportedState(
	t *testing.T,
	ctx context.Context,
	database *memdb.DB,
	store *fakeObjectStore,
	targetUser, targetWorkspace uuid.UUID,
	fixture transferFixture,
) {
	t.Helper()
	rows, err := database.Pool.Query(ctx, `
		SELECT id, user_id, storage_key, index_status
		  FROM files
		 WHERE id = ANY($1::uuid[])
		 ORDER BY id
	`, []uuid.UUID{fixture.fileID, fixture.secondFileID})
	if err != nil {
		t.Fatalf("load imported files: %v", err)
	}
	defer rows.Close()
	storageKeys := make(map[uuid.UUID]string, 2)
	for rows.Next() {
		var (
			fileID     uuid.UUID
			fileUser   uuid.UUID
			storageKey string
			status     string
		)
		if err := rows.Scan(&fileID, &fileUser, &storageKey, &status); err != nil {
			t.Fatalf("scan imported file: %v", err)
		}
		if fileUser != targetUser || status != "pending" ||
			!strings.Contains(storageKey, "/imports/"+fixture.bundleID.String()+"/") {
			t.Fatalf(
				"file=%s user=%s key=%q status=%q",
				fileID,
				fileUser,
				storageKey,
				status,
			)
		}
		object, err := store.Get(ctx, storageKey)
		if err != nil {
			t.Fatalf("download imported file %s: %v", fileID, err)
		}
		raw, readErr := io.ReadAll(object)
		closeErr := object.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read imported file %s: read=%v close=%v", fileID, readErr, closeErr)
		}
		if !bytes.Equal(raw, fixture.blob) {
			t.Fatalf("file %s bytes=%q, want %q", fileID, raw, fixture.blob)
		}
		storageKeys[fileID] = storageKey
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate imported files: %v", err)
	}
	if len(storageKeys) != 2 ||
		storageKeys[fixture.fileID] == storageKeys[fixture.secondFileID] {
		t.Fatalf("shared-content file storage keys are not independent: %v", storageKeys)
	}
	var (
		effectiveTags     []string
		userTags          []string
		timelineAt        time.Time
		geo               pgtype.Point
		sourceMetadata    string
		processorMetadata string
		summary           *string
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT tags, user_tags, timeline_at, geo, summary,
		       source_metadata::text, processor_metadata::text
		  FROM files
		 WHERE id = $1
	`, fixture.fileID).Scan(
		&effectiveTags,
		&userTags,
		&timelineAt,
		&geo,
		&summary,
		&sourceMetadata,
		&processorMetadata,
	); err != nil {
		t.Fatalf("load imported file enrichment: %v", err)
	}
	if !slices.Equal(effectiveTags, []string{"agent", "reviewed"}) ||
		!slices.Equal(userTags, []string{"agent"}) ||
		!timelineAt.Equal(fixture.eventAt) ||
		!geo.Valid || geo.P.X != 121.4737 || geo.P.Y != 31.2304 ||
		summary == nil || *summary != "portable summary" ||
		!strings.Contains(sourceMetadata, `"source_kind": "mobile"`) ||
		processorMetadata != "{}" {
		t.Fatalf(
			"file enrichment tags=%v user_tags=%v summary=%v timeline=%s geo=%+v source=%s processor=%s",
			effectiveTags,
			userTags,
			summary,
			timelineAt,
			geo,
			sourceMetadata,
			processorMetadata,
		)
	}
	annotationRows, err := database.Pool.Query(ctx, `
		SELECT id, status, decided_by_user_id, decided_at
		  FROM file_annotations
		 WHERE file_id = $1
		 ORDER BY id
	`, fixture.fileID)
	if err != nil {
		t.Fatalf("load imported file annotations: %v", err)
	}
	importedDecisions := make(map[uuid.UUID]string)
	for annotationRows.Next() {
		var (
			annotationID uuid.UUID
			status       string
			actorID      *uuid.UUID
			decidedAt    *time.Time
		)
		if err := annotationRows.Scan(&annotationID, &status, &actorID, &decidedAt); err != nil {
			annotationRows.Close()
			t.Fatalf("scan imported file annotation: %v", err)
		}
		if actorID != nil || decidedAt == nil || !decidedAt.Equal(fixture.eventAt) {
			annotationRows.Close()
			t.Fatalf(
				"annotation %s actor=%v decided_at=%v",
				annotationID,
				actorID,
				decidedAt,
			)
		}
		importedDecisions[annotationID] = status
	}
	if err := annotationRows.Err(); err != nil {
		annotationRows.Close()
		t.Fatalf("iterate imported file annotations: %v", err)
	}
	annotationRows.Close()
	if importedDecisions[fixture.acceptedAnnotation] != "accepted" ||
		importedDecisions[fixture.acceptedDescription] != "accepted" ||
		importedDecisions[fixture.rejectedAnnotation] != "rejected" ||
		len(importedDecisions) != 3 {
		t.Fatalf("imported annotation decisions = %v", importedDecisions)
	}
	var (
		memoryWorkspace uuid.UUID
		memoryCreatedBy *uuid.UUID
		memoryToken     *uuid.UUID
		memoryRequest   string
		stateVersion    int64
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT workspace_id, created_by_user_id, created_by_token_id,
		       request_sha256, state_version
		  FROM memories
		 WHERE id = $1
	`, fixture.memoryID).Scan(
		&memoryWorkspace,
		&memoryCreatedBy,
		&memoryToken,
		&memoryRequest,
		&stateVersion,
	); err != nil {
		t.Fatalf("load imported memory: %v", err)
	}
	if memoryWorkspace != targetWorkspace ||
		memoryCreatedBy != nil ||
		memoryToken != nil ||
		memoryRequest == fixture.memoryOriginRequest ||
		stateVersion != 2 {
		t.Fatalf(
			"memory workspace=%s user=%v token=%v request=%q version=%d",
			memoryWorkspace,
			memoryCreatedBy,
			memoryToken,
			memoryRequest,
			stateVersion,
		)
	}
	var (
		eventWorkspace uuid.UUID
		eventUser      *uuid.UUID
		eventToken     *uuid.UUID
		eventRequest   string
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT workspace_id, actor_user_id, actor_token_id, request_sha256
		  FROM memory_events
		 WHERE id = $1
	`, fixture.memoryEventID).Scan(
		&eventWorkspace,
		&eventUser,
		&eventToken,
		&eventRequest,
	); err != nil {
		t.Fatalf("load imported memory event: %v", err)
	}
	eventRecord := workspacebundle.MemoryEventRecord{
		ID:               fixture.memoryEventID,
		MemoryID:         fixture.memoryID,
		Action:           memory.FeedbackPin,
		ExpectedVersion:  1,
		ResultingVersion: 2,
	}
	expectedEventRequest, err := workspacebundle.MemoryEventRequestSHA256(
		targetWorkspace,
		eventRecord,
	)
	if err != nil {
		t.Fatal(err)
	}
	if eventWorkspace != targetWorkspace ||
		eventUser != nil ||
		eventToken != nil ||
		eventRequest != expectedEventRequest {
		t.Fatalf(
			"event workspace=%s user=%v token=%v request=%q want=%q",
			eventWorkspace,
			eventUser,
			eventToken,
			eventRequest,
			expectedEventRequest,
		)
	}
	var (
		taskWorkspace     uuid.UUID
		headCheckpointID  uuid.UUID
		headSequence      int64
		baseCheckpointID  uuid.UUID
		checkpointCount   int
		checkpointRequest string
		checkpointUser    *uuid.UUID
		checkpointToken   *uuid.UUID
	)
	if err := database.Pool.QueryRow(ctx, `
		SELECT t.workspace_id, t.head_checkpoint_id, t.head_sequence,
		       c.base_checkpoint_id,
		       (SELECT count(*) FROM task_checkpoints WHERE task_id = t.id),
		       c.request_sha256, c.created_by_user_id, c.created_by_token_id
		  FROM agent_tasks AS t
		  JOIN task_checkpoints AS c ON c.id = t.head_checkpoint_id
		 WHERE t.id = $1 AND c.id = $2
	`, fixture.taskID, fixture.checkpointID).Scan(
		&taskWorkspace,
		&headCheckpointID,
		&headSequence,
		&baseCheckpointID,
		&checkpointCount,
		&checkpointRequest,
		&checkpointUser,
		&checkpointToken,
	); err != nil {
		t.Fatalf("load imported checkpoint lineage: %v", err)
	}
	if taskWorkspace != targetWorkspace ||
		headCheckpointID != fixture.checkpointID ||
		headSequence != 2 ||
		baseCheckpointID != fixture.baseCheckpointID ||
		checkpointCount != 2 ||
		checkpointRequest == fixture.checkpointOrigin ||
		checkpointUser != nil ||
		checkpointToken != nil {
		t.Fatalf(
			"task workspace=%s head=%s/%d base=%s count=%d request=%q user=%v token=%v",
			taskWorkspace,
			headCheckpointID,
			headSequence,
			baseCheckpointID,
			checkpointCount,
			checkpointRequest,
			checkpointUser,
			checkpointToken,
		)
	}
	var refs int
	if err := database.Pool.QueryRow(ctx, `
		SELECT count(*) FROM task_checkpoint_refs WHERE checkpoint_id = $1
	`, fixture.checkpointID).Scan(&refs); err != nil {
		t.Fatalf("count imported checkpoint refs: %v", err)
	}
	if refs != 2 {
		t.Fatalf("imported checkpoint refs = %d, want 2", refs)
	}

	// Imported request hashes must preserve the ordinary service replay
	// contracts after rebinding to the target workspace.
	targetMemoryService := memory.New(database.Pool)
	replayedMemory, err := targetMemoryService.Remember(ctx, memory.Command{
		WorkspaceID:      targetWorkspace,
		CreatedByUserID:  &targetUser,
		Kind:             memory.KindDecision,
		Content:          "Preserve portable IDs and immutable lineage.",
		Attributes:       json.RawMessage(`{"confidence":"confirmed"}`),
		Path:             "/Project",
		EventAt:          &fixture.eventAt,
		SourceType:       "file",
		SourceRef:        "state.txt",
		SourceFileID:     &fixture.fileID,
		SourceFileSHA256: fixture.fileSHA,
		SourceLocator:    json.RawMessage(`{"line":1}`),
		ProducerAgent:    "claude-code",
		ProducerSession:  "source-session",
		ProducerTask:     "portable-task",
		IdempotencyKey:   fixture.memoryKey,
	})
	if err != nil {
		t.Fatalf("replay imported memory through service: %v", err)
	}
	if !replayedMemory.Replayed || replayedMemory.Memory.ID != fixture.memoryID {
		t.Fatalf("memory replay = %+v", replayedMemory)
	}
	replayedEvent, err := targetMemoryService.Feedback(ctx, memory.FeedbackCommand{
		WorkspaceID:     targetWorkspace,
		MemoryID:        fixture.memoryID,
		ActorUserID:     &targetUser,
		Action:          memory.FeedbackPin,
		IdempotencyKey:  fixture.memoryEventKey,
		ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("replay imported memory event through service: %v", err)
	}
	if !replayedEvent.Replayed || replayedEvent.Event.ID != fixture.memoryEventID {
		t.Fatalf("memory event replay = %+v", replayedEvent)
	}
	replayedCheckpoint, err := handoff.New(database.Pool).Checkpoint(
		ctx,
		handoff.CheckpointCommand{
			WorkspaceID:     targetWorkspace,
			CreatedByUserID: &targetUser,
			TaskKey:         "portable-task",
			Handoff:         fixture.handoffPayload,
			IdempotencyKey:  fixture.checkpointKey,
		},
	)
	if err != nil {
		t.Fatalf("replay imported checkpoint through service: %v", err)
	}
	if !replayedCheckpoint.Replayed ||
		replayedCheckpoint.Checkpoint.ID != fixture.checkpointID {
		t.Fatalf("checkpoint replay = %+v", replayedCheckpoint)
	}
}

func createTransferTenant(
	t *testing.T,
	ctx context.Context,
	database *memdb.DB,
	prefix string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var userID uuid.UUID
	email := prefix + "-" + uuid.NewString() + "@example.com"
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'integration-test')
		RETURNING id
	`, email).Scan(&userID); err != nil {
		t.Fatalf("create transfer user: %v", err)
	}
	var workspaceID uuid.UUID
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO workspaces (name, resource_owner_user_id)
		VALUES ($1, $2)
		RETURNING id
	`, prefix, userID).Scan(&workspaceID); err != nil {
		t.Fatalf("create transfer workspace: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO workspace_memberships (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("create transfer workspace membership: %v", err)
	}
	return userID, workspaceID
}

func assertBoundedPreflightConflicts(
	t *testing.T,
	ctx context.Context,
	database *memdb.DB,
	targetUser, targetWorkspace uuid.UUID,
) {
	t.Helper()
	const conflictRows = MaxConflictDetails + 75
	now := time.Date(2026, time.July, 28, 13, 0, 0, 0, time.UTC)
	records := make([]workspacebundle.FolderRecord, conflictRows)
	copyRows := make([][]any, conflictRows)
	for index := range conflictRows {
		id := uuid.New()
		path := fmt.Sprintf("/conflict-%03d", index)
		records[index] = workspacebundle.FolderRecord{
			ID:        id,
			Path:      path,
			Name:      fmt.Sprintf("conflict-%03d", index),
			CreatedAt: now,
			UpdatedAt: now,
		}
		copyRows[index] = []any{
			id,
			targetUser,
			nil,
			path,
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
		pgx.CopyFromRows(copyRows),
	); err != nil || copied != conflictRows {
		t.Fatalf("seed large conflict set: copied=%d err=%v", copied, err)
	}

	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin bounded preflight: %v", err)
	}
	defer rollback(tx)
	summary, err := preflightFresh(
		ctx,
		tx,
		importTarget{
			WorkspaceID: targetWorkspace,
			OwnerID:     targetUser,
		},
		workspacebundle.BundleData{Folders: records},
	)
	if err != nil {
		t.Fatalf("bounded preflight: %v", err)
	}
	if len(summary.Conflicts) != MaxConflictDetails ||
		summary.Total != MaxConflictDetails+1 ||
		!summary.Truncated {
		t.Fatalf(
			"bounded preflight details=%d total=%d truncated=%t",
			len(summary.Conflicts),
			summary.Total,
			summary.Truncated,
		)
	}
	for index := 1; index < len(summary.Conflicts); index++ {
		left := summary.Conflicts[index-1]
		right := summary.Conflicts[index]
		if left.Kind > right.Kind ||
			(left.Kind == right.Kind && left.Resource > right.Resource) ||
			(left.Kind == right.Kind &&
				left.Resource == right.Resource &&
				left.Value > right.Value) {
			t.Fatalf("bounded conflict details are not sorted at %d", index)
		}
	}
	raw, err := json.Marshal(ConflictError{
		Conflicts: summary.Conflicts,
		Total:     summary.Total,
		Truncated: summary.Truncated,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 64<<10 {
		t.Fatalf("bounded service conflict payload = %d bytes", len(raw))
	}
}
