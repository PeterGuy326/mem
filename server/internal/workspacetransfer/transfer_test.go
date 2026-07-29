package workspacetransfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/google/uuid"
)

type fakeObjectStore struct {
	objects      map[string][]byte
	puts         []string
	deletes      []string
	putErr       error
	deleteErr    error
	shortSuccess bool
	afterPut     func()
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: make(map[string][]byte)}
}

func (store *fakeObjectStore) Put(
	_ context.Context,
	key string,
	body io.Reader,
	_ int64,
	_ string,
) error {
	store.puts = append(store.puts, key)
	if store.shortSuccess {
		return nil
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	store.objects[key] = raw
	if store.afterPut != nil {
		store.afterPut()
	}
	return store.putErr
}

func (store *fakeObjectStore) Get(
	_ context.Context,
	key string,
) (io.ReadCloser, error) {
	raw, exists := store.objects[key]
	if !exists {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (store *fakeObjectStore) Delete(_ context.Context, key string) error {
	store.deletes = append(store.deletes, key)
	delete(store.objects, key)
	return store.deleteErr
}

func TestUploadArchiveBlobsVerifiesAndCleansUp(t *testing.T) {
	archive, blob, fileID := oneBlobArchive(t)
	target := importTarget{
		WorkspaceID: uuid.New(),
		OwnerID:     uuid.New(),
	}

	t.Run("success", func(t *testing.T) {
		store := newFakeObjectStore()
		service := &Service{store: store}
		keys, uploaded, err := service.uploadArchiveBlobs(
			context.Background(),
			target,
			archive,
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) != 1 || len(uploaded) != 1 ||
			!bytes.Equal(store.objects[keys[fileID]], blob) {
			t.Fatalf(
				"keys=%v uploaded=%v objects=%v",
				keys,
				uploaded,
				store.objects,
			)
		}
	})

	t.Run("put failure deletes current object", func(t *testing.T) {
		store := newFakeObjectStore()
		store.putErr = errors.New("storage unavailable")
		service := &Service{store: store}
		_, uploaded, err := service.uploadArchiveBlobs(
			context.Background(),
			target,
			archive,
		)
		if err == nil || !strings.Contains(err.Error(), "storage unavailable") {
			t.Fatalf("error = %v", err)
		}
		if len(uploaded) != 1 || len(store.deletes) != 1 ||
			len(store.objects) != 0 {
			t.Fatalf(
				"uploaded=%v deletes=%v objects=%v",
				uploaded,
				store.deletes,
				store.objects,
			)
		}
	})

	t.Run("store success without consuming stream is integrity failure", func(t *testing.T) {
		store := newFakeObjectStore()
		store.shortSuccess = true
		service := &Service{store: store}
		_, uploaded, err := service.uploadArchiveBlobs(
			context.Background(),
			target,
			archive,
		)
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error = %v", err)
		}
		if len(uploaded) != 1 || len(store.deletes) != 1 {
			t.Fatalf("uploaded=%v deletes=%v", uploaded, store.deletes)
		}
	})
}

func TestUploadArchiveBlobCreatesIndependentObjectsForSharedContent(t *testing.T) {
	archive, blob, fileIDs := sharedBlobArchive(t)
	store := newFakeObjectStore()
	service := &Service{store: store}
	keys, uploaded, err := service.uploadArchiveBlobs(
		context.Background(),
		importTarget{
			WorkspaceID: uuid.New(),
			OwnerID:     uuid.New(),
		},
		archive,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstKey := keys[fileIDs[0]]
	secondKey := keys[fileIDs[1]]
	if len(keys) != 2 ||
		len(uploaded) != 2 ||
		len(store.puts) != 2 ||
		firstKey == secondKey ||
		!bytes.Equal(store.objects[firstKey], blob) ||
		!bytes.Equal(store.objects[secondKey], blob) {
		t.Fatalf(
			"keys=%v uploaded=%v puts=%v objects=%v",
			keys,
			uploaded,
			store.puts,
			store.objects,
		)
	}

	if err := store.Delete(context.Background(), firstKey); err != nil {
		t.Fatal(err)
	}
	second, err := store.Get(context.Background(), secondKey)
	if err != nil {
		t.Fatalf("read second object after deleting first: %v", err)
	}
	defer second.Close()
	got, err := io.ReadAll(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("second object = %q, want %q", got, blob)
	}
}

func TestConflictCollectorBoundsLargeConflictSet(t *testing.T) {
	collector := newConflictCollector(MaxConflictDetails)
	inspected := 0
	for index := 100_000; index >= 0; index-- {
		inspected++
		if !collector.add(Conflict{
			Kind:     "global_id",
			Resource: "file",
			Value:    fmt.Sprintf("%06d", index),
			Detail:   strings.Repeat("x", 128),
		}) {
			break
		}
	}
	summary := collector.summary()
	if inspected != MaxConflictDetails+1 ||
		len(summary.Conflicts) != MaxConflictDetails ||
		len(collector.seen) != MaxConflictDetails ||
		summary.Total != MaxConflictDetails+1 ||
		!summary.Truncated {
		t.Fatalf(
			"inspected=%d details=%d seen=%d total=%d truncated=%t",
			inspected,
			len(summary.Conflicts),
			len(collector.seen),
			summary.Total,
			summary.Truncated,
		)
	}
	for index := 1; index < len(summary.Conflicts); index++ {
		if summary.Conflicts[index-1].Value > summary.Conflicts[index].Value {
			t.Fatalf(
				"conflicts are not stably sorted at %d: %q > %q",
				index,
				summary.Conflicts[index-1].Value,
				summary.Conflicts[index].Value,
			)
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
		t.Fatalf("bounded conflict response is unexpectedly large: %d bytes", len(raw))
	}
}

func TestTargetRequestHashesAreWorkspaceBound(t *testing.T) {
	workspaceA := uuid.New()
	workspaceB := uuid.New()
	eventAt := time.Date(2026, time.July, 28, 1, 2, 3, 4, time.UTC)
	memory := workspacebundle.MemoryRecord{
		ID:                   uuid.New(),
		Kind:                 "decision",
		Content:              "portable",
		Attributes:           []byte(`{"confirmed":true}`),
		Path:                 "/",
		EventAt:              &eventAt,
		SourceType:           "agent",
		SourceLocator:        []byte(`{}`),
		ProducerAgent:        "codex",
		IdempotencyKeySHA256: digestBytes([]byte("memory-key")),
		LifecycleStatus:      "active",
	}
	hashA, err := memoryRequestSHA256(workspaceA, memory)
	if err != nil {
		t.Fatal(err)
	}
	hashAReplay, err := memoryRequestSHA256(workspaceA, memory)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := memoryRequestSHA256(workspaceB, memory)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashAReplay || hashA == hashB ||
		len(hashA) != 64 || len(hashB) != 64 {
		t.Fatalf("hashA=%q replay=%q hashB=%q", hashA, hashAReplay, hashB)
	}

	payload := []byte(`{"contract":"mem.handoff"}`)
	checkpointA, err := checkpointRequestSHA256(workspaceA, "task", payload)
	if err != nil {
		t.Fatal(err)
	}
	checkpointB, err := checkpointRequestSHA256(workspaceB, "task", payload)
	if err != nil {
		t.Fatal(err)
	}
	if checkpointA == checkpointB {
		t.Fatal("checkpoint request hash was not rebound to target workspace")
	}
}

func TestImportedStorageKeyIsTargetLocalAndTraversalSafe(t *testing.T) {
	ownerID := uuid.New()
	bundleID := uuid.New()
	fileID := uuid.New()
	key := importedStorageKey(ownerID, bundleID, fileID, "../secret.txt")
	wantSuffix := "/" + fileID.String() + "/secret.txt"
	if !strings.HasPrefix(key, "users/"+ownerID.String()+"/imports/"+
		bundleID.String()+"/") ||
		!strings.HasSuffix(key, wantSuffix) ||
		strings.Contains(key, "..") {
		t.Fatalf("storage key = %q", key)
	}
}

func TestCleanupUploadedPreservesCauseAndReportsDeleteFailure(t *testing.T) {
	store := newFakeObjectStore()
	store.deleteErr = errors.New("delete unavailable")
	cause := errors.New("database failure")
	err := cleanupUploaded(
		store,
		[]string{"one", "two"},
		cause,
	)
	if !errors.Is(err, cause) ||
		!strings.Contains(err.Error(), "delete unavailable") {
		t.Fatalf("error = %v", err)
	}
	if strings.Join(store.deletes, ",") != "two,one" {
		t.Fatalf("cleanup order = %v", store.deletes)
	}
}

func TestClassifyImportCommitVerification(t *testing.T) {
	expected := importReplay{
		BundleID:          uuid.New(),
		ArchiveSHA256:     strings.Repeat("a", 64),
		SourceWorkspaceID: uuid.New(),
	}
	committed := expected
	committed.ImportedAt = time.Date(2026, time.July, 28, 8, 0, 0, 0, time.UTC)
	queryErr := errors.New("ledger unavailable")

	tests := []struct {
		name       string
		replay     importReplay
		found      bool
		err        error
		want       importCommitOutcome
		wantErr    error
		wantReplay bool
	}{
		{
			name:       "committed matching ledger",
			replay:     committed,
			found:      true,
			want:       importCommitCommitted,
			wantReplay: true,
		},
		{
			name:  "confirmed absent ledger",
			want:  importCommitAbsent,
			found: false,
		},
		{
			name:    "unknown ledger",
			err:     queryErr,
			want:    importCommitUnknown,
			wantErr: queryErr,
		},
		{
			name: "conflicting ledger",
			replay: importReplay{
				BundleID:          expected.BundleID,
				ArchiveSHA256:     strings.Repeat("b", 64),
				SourceWorkspaceID: expected.SourceWorkspaceID,
			},
			found:   true,
			want:    importCommitConflict,
			wantErr: ErrConflict,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyImportCommitVerification(
				expected,
				test.replay,
				test.found,
				test.err,
			)
			if got.Outcome != test.want {
				t.Fatalf("outcome = %d, want %d", got.Outcome, test.want)
			}
			if test.wantErr != nil && !errors.Is(got.Err, test.wantErr) {
				t.Fatalf("error = %v, want %v", got.Err, test.wantErr)
			}
			if test.wantReplay && got.Replay != committed {
				t.Fatalf("replay = %+v, want %+v", got.Replay, committed)
			}
		})
	}
}

func TestResolveImportCommitVerificationCleansOnlyConfirmedAbsence(t *testing.T) {
	commitErr := errors.New("commit acknowledgement lost")
	ledgerErr := errors.New("ledger unavailable")
	conflictErr := &ConflictError{Conflicts: []Conflict{{
		Kind:     "bundle_identity",
		Resource: "workspace_imports",
		Value:    uuid.NewString(),
	}}, Total: 1}
	replay := importReplay{
		BundleID:          uuid.New(),
		ArchiveSHA256:     strings.Repeat("c", 64),
		SourceWorkspaceID: uuid.New(),
		ImportedAt:        time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC),
	}
	counts := workspacebundle.ObjectCounts{Files: 2, Blobs: 1}

	tests := []struct {
		name         string
		verification importCommitVerification
		wantCleanup  bool
		wantErr      error
		wantSuccess  bool
	}{
		{
			name: "committed matching ledger preserves objects",
			verification: importCommitVerification{
				Outcome: importCommitCommitted,
				Replay:  replay,
			},
			wantSuccess: true,
		},
		{
			name: "confirmed absence cleans objects",
			verification: importCommitVerification{
				Outcome: importCommitAbsent,
			},
			wantCleanup: true,
			wantErr:     commitErr,
		},
		{
			name: "unknown ledger preserves objects",
			verification: importCommitVerification{
				Outcome: importCommitUnknown,
				Err:     ledgerErr,
			},
			wantErr: ErrCommitIndeterminate,
		},
		{
			name: "conflicting ledger preserves objects",
			verification: importCommitVerification{
				Outcome: importCommitConflict,
				Err:     conflictErr,
			},
			wantErr: ErrConflict,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cleanupCalls := 0
			result, err := resolveImportCommitVerification(
				test.verification,
				counts,
				commitErr,
				func(cause error) error {
					cleanupCalls++
					return cause
				},
			)
			if (cleanupCalls == 1) != test.wantCleanup {
				t.Fatalf(
					"cleanup calls = %d, want cleanup %t",
					cleanupCalls,
					test.wantCleanup,
				)
			}
			if test.wantSuccess {
				if err != nil {
					t.Fatalf("resolve committed import: %v", err)
				}
				if result == nil || !result.Replayed ||
					result.BundleID != replay.BundleID ||
					result.ArchiveSHA256 != replay.ArchiveSHA256 ||
					result.SourceWorkspaceID != replay.SourceWorkspaceID ||
					!result.ImportedAt.Equal(replay.ImportedAt) ||
					result.Counts != counts {
					t.Fatalf("result = %+v", result)
				}
				return
			}
			if result != nil {
				t.Fatalf("unexpected result = %+v", result)
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if errors.Is(test.wantErr, ErrCommitIndeterminate) &&
				!strings.Contains(err.Error(), "retry the same bundle") {
				t.Fatalf("indeterminate error is not actionable: %v", err)
			}
			if test.verification.Outcome == importCommitUnknown &&
				(!errors.Is(err, commitErr) || !errors.Is(err, ledgerErr)) {
				t.Fatalf("indeterminate error lost causes: %v", err)
			}
		})
	}
}

func oneBlobArchive(
	t *testing.T,
) (*workspacebundle.Archive, []byte, uuid.UUID) {
	t.Helper()
	archive, blob, fileIDs := blobArchive(t, 1)
	return archive, blob, fileIDs[0]
}

func sharedBlobArchive(
	t *testing.T,
) (*workspacebundle.Archive, []byte, []uuid.UUID) {
	t.Helper()
	return blobArchive(t, 2)
}

func blobArchive(
	t *testing.T,
	fileCount int,
) (*workspacebundle.Archive, []byte, []uuid.UUID) {
	t.Helper()
	blob := []byte("portable workspace bytes\n")
	digest := digestBytes(blob)
	blobPath, err := workspacebundle.BlobEntryPath(digest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	workspaceID := uuid.New()
	bundleID := uuid.New()
	fileIDs := make([]uuid.UUID, fileCount)
	data := workspacebundle.BundleData{
		Files:              make([]workspacebundle.FileRecord, 0, fileCount),
		CheckpointPayloads: map[uuid.UUID][]byte{},
		Blobs: []workspacebundle.BlobInfo{{
			SHA256: digest,
			Path:   blobPath,
			Size:   int64(len(blob)),
		}},
	}
	for index := range fileCount {
		fileIDs[index] = uuid.New()
		data.Files = append(data.Files, workspacebundle.FileRecord{
			ID:             fileIDs[index],
			Name:           fmt.Sprintf("portable-%d.txt", index),
			Path:           "/",
			Size:           int64(len(blob)),
			SHA256:         digest,
			MIME:           "text/plain",
			BlobPath:       blobPath,
			Tags:           []string{},
			UserTags:       []string{},
			SourceMetadata: []byte(`{}`),
			Annotations:    []workspacebundle.FileAnnotationRecord{},
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}
	data.Manifest = workspacebundle.NewManifest(
		bundleID,
		now,
		workspacebundle.SourceDescriptor{
			WorkspaceID:     workspaceID,
			WorkspaceName:   "source",
			Exporter:        "test",
			ExporterVersion: "test",
		},
		countsFor(data),
	)
	var buffer bytes.Buffer
	if err := workspacebundle.Write(
		&buffer,
		workspacebundle.WriteInput{
			BundleData: data,
			BlobSources: []workspacebundle.BlobSource{{
				BlobInfo: data.Blobs[0],
				Open: func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(blob)), nil
				},
			}},
		},
		workspacebundle.WriterOptions{},
	); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	reader := bytes.NewReader(buffer.Bytes())
	archive, err := workspacebundle.Open(
		reader,
		int64(reader.Len()),
		workspacebundle.ReaderOptions{},
	)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	return archive, blob, fileIDs
}
