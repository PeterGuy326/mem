package workspacebundle

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/google/uuid"
)

var (
	fixtureWorkspaceID = uuid.MustParse("10000000-0000-0000-0000-000000000001")
	fixtureBundleID    = uuid.MustParse("10000000-0000-0000-0000-000000000002")
	fixtureFolderID    = uuid.MustParse("20000000-0000-0000-0000-000000000001")
	fixtureFileID      = uuid.MustParse("30000000-0000-0000-0000-000000000001")
	fixtureMemoryID    = uuid.MustParse("40000000-0000-0000-0000-000000000001")
	fixtureMemoryEvent = uuid.MustParse("45000000-0000-0000-0000-000000000001")
	fixtureTaskID      = uuid.MustParse("50000000-0000-0000-0000-000000000001")
	fixtureCheckpoint1 = uuid.MustParse("60000000-0000-0000-0000-000000000001")
	fixtureCheckpoint2 = uuid.MustParse("60000000-0000-0000-0000-000000000002")
	fixtureTime        = time.Date(2026, time.July, 28, 12, 0, 0, 123_000_000, time.UTC)
)

func validFixture(t *testing.T) WriteInput {
	t.Helper()
	blob := []byte("portable file bytes\n")
	fileSHA := sha256Hex(blob)
	memoryContent := "Portable memory"
	memorySHA := sha256Hex([]byte(memoryContent))
	task := TaskRecord{
		ID:               fixtureTaskID,
		TaskKey:          "portable-agent-drive",
		ScopePath:        "/",
		HeadCheckpointID: uuidPointer(fixtureCheckpoint2),
		HeadSequence:     2,
		CreatedAt:        fixtureTime,
		UpdatedAt:        fixtureTime,
	}
	payload1 := normalizedPayload(t, handoff.HandoffV1{
		Contract:       handoff.ContractName,
		SchemaVersion:  handoff.SchemaVersionV1,
		CheckpointKind: handoff.CheckpointKindCheckpoint,
		TaskKey:        task.TaskKey,
		ScopePath:      "/",
		State: handoff.StateV1{
			Status: handoff.TaskStatusInProgress,
			Goal:   "Build a portable Agent drive.",
			Progress: handoff.ProgressV1{
				Summary:   "Bundle format is being implemented.",
				Completed: []string{},
			},
			Decisions:     []handoff.DecisionV1{},
			NextSteps:     []handoff.NextStepV1{},
			Blockers:      []handoff.BlockerV1{},
			OpenQuestions: []string{},
			Artifacts: []handoff.ArtifactV1{{
				URI:      "mem://files/" + fixtureFileID.String(),
				Role:     "source",
				SHA256:   fileSHA,
				Required: boolPointer(true),
			}},
		},
		Producer: handoff.ProducerV1{
			AgentID:   "codex",
			SessionID: "fixture-session",
		},
	}, task.TaskKey)
	payload2 := normalizedPayload(t, handoff.HandoffV1{
		Contract:         handoff.ContractName,
		SchemaVersion:    handoff.SchemaVersionV1,
		CheckpointKind:   handoff.CheckpointKindHandoff,
		TaskKey:          task.TaskKey,
		BaseCheckpointID: uuidPointer(fixtureCheckpoint1),
		ScopePath:        "/",
		State: handoff.StateV1{
			Status: handoff.TaskStatusReady,
			Goal:   "Build a portable Agent drive.",
			Progress: handoff.ProgressV1{
				Summary:   "Bundle format is ready to transfer.",
				Completed: []string{"Defined the portable archive."},
			},
			Decisions: []handoff.DecisionV1{{
				Summary:    "Preserve immutable IDs.",
				References: []string{"mem://memories/" + fixtureMemoryID.String()},
			}},
			NextSteps:     []handoff.NextStepV1{},
			Blockers:      []handoff.BlockerV1{},
			OpenQuestions: []string{},
			Artifacts:     []handoff.ArtifactV1{},
		},
		Producer: handoff.ProducerV1{
			AgentID:   "codex",
			SessionID: "fixture-session",
		},
	}, task.TaskKey)
	checkpoint1 := CheckpointRecord{
		ID:                  fixtureCheckpoint1,
		TaskID:              fixtureTaskID,
		Sequence:            1,
		CheckpointKind:      handoff.CheckpointKindCheckpoint,
		Contract:            handoff.ContractName,
		SchemaVersion:       handoff.SchemaVersionV1,
		ScopePath:           "/",
		PayloadPath:         checkpointPayloadPath(fixtureCheckpoint1),
		PayloadSHA256:       sha256Hex(payload1),
		OriginRequestSHA256: strings.Repeat("a", 64),
		IdempotencyKey:      "checkpoint-fixture-1",
		ProducerAgent:       "codex",
		ProducerSession:     "fixture-session",
		CreatedAt:           fixtureTime,
	}
	checkpoint2 := CheckpointRecord{
		ID:                  fixtureCheckpoint2,
		TaskID:              fixtureTaskID,
		Sequence:            2,
		CheckpointKind:      handoff.CheckpointKindHandoff,
		Contract:            handoff.ContractName,
		SchemaVersion:       handoff.SchemaVersionV1,
		BaseCheckpointID:    uuidPointer(fixtureCheckpoint1),
		ScopePath:           "/",
		PayloadPath:         checkpointPayloadPath(fixtureCheckpoint2),
		PayloadSHA256:       sha256Hex(payload2),
		OriginRequestSHA256: strings.Repeat("b", 64),
		IdempotencyKey:      "checkpoint-fixture-2",
		ProducerAgent:       "codex",
		ProducerSession:     "fixture-session",
		CreatedAt:           fixtureTime.Add(time.Minute),
	}
	memoryEvent := MemoryEventRecord{
		ID:                   fixtureMemoryEvent,
		MemoryID:             fixtureMemoryID,
		Action:               "pin",
		IdempotencyKeySHA256: strings.Repeat("f", 64),
		ExpectedVersion:      1,
		ResultingVersion:     2,
		CreatedAt:            fixtureTime,
	}
	var err error
	memoryEvent.OriginRequestSHA256, err = MemoryEventRequestSHA256(
		fixtureWorkspaceID,
		memoryEvent,
	)
	if err != nil {
		t.Fatalf("hash memory event fixture: %v", err)
	}
	payloads := map[uuid.UUID][]byte{
		fixtureCheckpoint1: payload1,
		fixtureCheckpoint2: payload2,
	}
	refs := append(
		projectFixtureRefs(t, task, checkpoint1, payload1),
		projectFixtureRefs(t, task, checkpoint2, payload2)...,
	)
	data := BundleData{
		Folders: []FolderRecord{{
			ID:        fixtureFolderID,
			Path:      "/Projects",
			Name:      "Projects",
			CreatedAt: fixtureTime,
			UpdatedAt: fixtureTime,
		}},
		Files: []FileRecord{{
			ID:        fixtureFileID,
			FolderID:  uuidPointer(fixtureFolderID),
			Name:      "portable.txt",
			Path:      "/Projects",
			Size:      int64(len(blob)),
			SHA256:    fileSHA,
			MIME:      "text/plain",
			BlobPath:  blobPath(fileSHA),
			Tags:      []string{"portable"},
			CreatedAt: fixtureTime,
			UpdatedAt: fixtureTime,
		}},
		Memories: []MemoryRecord{{
			ID:                   fixtureMemoryID,
			Kind:                 "decision",
			Content:              memoryContent,
			Attributes:           json.RawMessage(`{}`),
			Path:                 "/Projects",
			SourceType:           "file",
			SourceRef:            "portable.txt",
			SourceFileID:         uuidPointer(fixtureFileID),
			SourceFileSHA256:     fileSHA,
			SourceLocator:        json.RawMessage(`{}`),
			ProducerAgent:        "codex",
			ProducerSession:      "fixture-session",
			ProducerTask:         task.TaskKey,
			IdempotencyKeySHA256: sha256Hex([]byte("memory-fixture-1")),
			OriginRequestSHA256:  strings.Repeat("c", 64),
			ContentSHA256:        memorySHA,
			LifecycleStatus:      "active",
			StateVersion:         2,
			PinnedAt:             timePointer(fixtureTime),
			CreatedAt:            fixtureTime,
			UpdatedAt:            fixtureTime,
		}},
		MemoryEvents:       []MemoryEventRecord{memoryEvent},
		Tasks:              []TaskRecord{task},
		Checkpoints:        []CheckpointRecord{checkpoint1, checkpoint2},
		CheckpointRefs:     refs,
		CheckpointPayloads: payloads,
		Blobs: []BlobInfo{{
			SHA256: fileSHA,
			Path:   blobPath(fileSHA),
			Size:   int64(len(blob)),
		}},
	}
	data.Manifest = NewManifest(
		fixtureBundleID,
		fixtureTime,
		SourceDescriptor{
			WorkspaceID:     fixtureWorkspaceID,
			WorkspaceName:   "Fixture workspace",
			Exporter:        "memd",
			ExporterVersion: "v1-test",
		},
		ObjectCounts{
			Folders:            int64(len(data.Folders)),
			Files:              int64(len(data.Files)),
			Memories:           int64(len(data.Memories)),
			MemoryEvents:       int64(len(data.MemoryEvents)),
			Tasks:              int64(len(data.Tasks)),
			Checkpoints:        int64(len(data.Checkpoints)),
			CheckpointRefs:     int64(len(data.CheckpointRefs)),
			CheckpointPayloads: int64(len(data.CheckpointPayloads)),
			Blobs:              int64(len(data.Blobs)),
			BlobBytes:          int64(len(blob)),
		},
	)
	return WriteInput{
		BundleData: data,
		BlobSources: []BlobSource{{
			BlobInfo: data.Blobs[0],
			Open: func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(blob)), nil
			},
		}},
	}
}

func normalizedPayload(t *testing.T, value handoff.HandoffV1, taskKey string) []byte {
	t.Helper()
	normalized, err := handoff.NormalizeV1(value, taskKey)
	if err != nil {
		t.Fatalf("normalize fixture payload: %v", err)
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal fixture payload: %v", err)
	}
	return raw
}

func projectFixtureRefs(
	t *testing.T,
	task TaskRecord,
	checkpoint CheckpointRecord,
	payload []byte,
) []CheckpointRefRecord {
	t.Helper()
	projection, err := (HandoffV1PayloadValidator{}).ValidateCheckpointPayload(
		payload,
		task,
		checkpoint,
	)
	if err != nil {
		t.Fatalf("project fixture refs: %v", err)
	}
	out := make([]CheckpointRefRecord, 0, len(projection.References))
	for _, reference := range projection.References {
		out = append(out, CheckpointRefRecord{
			CheckpointID:   checkpoint.ID,
			Ordinal:        reference.Ordinal,
			Relation:       reference.Relation,
			URI:            reference.URI,
			ExpectedSHA256: reference.ExpectedSHA256,
			Required:       reference.Required,
			Metadata:       append(json.RawMessage(nil), reference.Metadata...),
		})
	}
	return out
}

func uuidPointer(value uuid.UUID) *uuid.UUID {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}

func timePointer(value time.Time) *time.Time {
	return &value
}
