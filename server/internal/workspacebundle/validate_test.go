package workspacebundle

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidateRejectsBrokenFolderAndFileDependencies(t *testing.T) {
	t.Run("folder parent", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Folders[0].ParentID = uuidPointer(
			uuid.MustParse("20000000-0000-0000-0000-000000000099"),
		)
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrDependency) {
			t.Fatalf("Validate error = %v, want ErrDependency", err)
		}
	})
	t.Run("file folder", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Files[0].FolderID = uuidPointer(
			uuid.MustParse("20000000-0000-0000-0000-000000000099"),
		)
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrDependency) {
			t.Fatalf("Validate error = %v, want ErrDependency", err)
		}
	})
}

func TestValidateAllowsFilesToShareContentAddressedBlob(t *testing.T) {
	fixture := sharedContentFixture(t)
	if err := Validate(fixture.BundleData, ValidationOptions{}); err != nil {
		t.Fatalf("Validate shared content fixture: %v", err)
	}
	if fixture.Manifest.Indexes.Files.Count != 2 ||
		fixture.Manifest.Blobs.Count != 1 ||
		fixture.Manifest.Blobs.TotalSize != fixture.Blobs[0].Size {
		t.Fatalf(
			"manifest files=%d blobs=%d bytes=%d",
			fixture.Manifest.Indexes.Files.Count,
			fixture.Manifest.Blobs.Count,
			fixture.Manifest.Blobs.TotalSize,
		)
	}

	broken := sharedContentFixture(t)
	broken.Files[1].Size++
	err := Validate(broken.BundleData, ValidationOptions{})
	if !errors.Is(err, ErrDependency) {
		t.Fatalf("mismatched shared blob error = %v, want ErrDependency", err)
	}
}

func TestValidateRejectsDuplicateUUIDAndIdempotencyKey(t *testing.T) {
	t.Run("file UUID", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Files = append(fixture.Files, fixture.Files[0])
		fixture.Manifest.Indexes.Files.Count++
		fixture.Blobs = append(fixture.Blobs, fixture.Blobs[0])
		fixture.Manifest.Blobs.Count++
		fixture.Manifest.Blobs.TotalSize += fixture.Blobs[0].Size
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})
	t.Run("memory idempotency", func(t *testing.T) {
		fixture := validFixture(t)
		second := fixture.Memories[0]
		second.ID = uuid.MustParse("40000000-0000-0000-0000-000000000099")
		fixture.Memories = append(fixture.Memories, second)
		fixture.Manifest.Indexes.Memories.Count++
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})
}

func TestValidateRejectsBrokenMemorySource(t *testing.T) {
	fixture := validFixture(t)
	fixture.Memories[0].SourceFileID = uuidPointer(
		uuid.MustParse("30000000-0000-0000-0000-000000000099"),
	)
	err := Validate(fixture.BundleData, ValidationOptions{})
	if !errors.Is(err, ErrDependency) {
		t.Fatalf("Validate error = %v, want ErrDependency", err)
	}
}

func TestValidateRejectsRawMemoryIdempotencyValue(t *testing.T) {
	fixture := validFixture(t)
	fixture.Memories[0].IdempotencyKeySHA256 = "raw-secret"
	err := Validate(fixture.BundleData, ValidationOptions{})
	if !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
	}
}

func TestValidateMemoryControlPlaneAndForgottenTombstone(t *testing.T) {
	t.Run("event version gap", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.MemoryEvents[0].ExpectedVersion = 2
		fixture.MemoryEvents[0].ResultingVersion = 3
		fixture.MemoryEvents[0].OriginRequestSHA256, _ = MemoryEventRequestSHA256(
			fixtureWorkspaceID,
			fixture.MemoryEvents[0],
		)
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrDependency) {
			t.Fatalf("Validate error = %v, want ErrDependency", err)
		}
	})
	t.Run("projection mismatch", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Memories[0].PinnedAt = nil
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})
	t.Run("forgotten tombstone", func(t *testing.T) {
		fixture := forgottenFixture(t)
		if err := Validate(fixture.BundleData, ValidationOptions{}); err != nil {
			t.Fatalf("Validate forgotten fixture: %v", err)
		}
		fixture.Memories[0].Content = "erased payload leaked"
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})
	t.Run("duplicate event UUID", func(t *testing.T) {
		fixture := validFixture(t)
		second := fixture.MemoryEvents[0]
		second.IdempotencyKeySHA256 = strings.Repeat("1", 64)
		second.ExpectedVersion = 2
		second.ResultingVersion = 3
		fixture.MemoryEvents = append(fixture.MemoryEvents, second)
		fixture.Manifest.Indexes.MemoryEvents.Count++
		fixture.Memories[0].StateVersion = 3
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})
}

func TestValidateRejectsBrokenCheckpointLineageAndHead(t *testing.T) {
	t.Run("base", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Checkpoints[1].BaseCheckpointID = uuidPointer(
			uuid.MustParse("60000000-0000-0000-0000-000000000099"),
		)
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) && !errors.Is(err, ErrDependency) {
			t.Fatalf("Validate error = %v, want integrity/dependency error", err)
		}
	})
	t.Run("head", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Tasks[0].HeadCheckpointID = uuidPointer(fixtureCheckpoint1)
		fixture.Tasks[0].HeadSequence = 1
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrDependency) {
			t.Fatalf("Validate error = %v, want ErrDependency", err)
		}
	})
	t.Run("gap", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Checkpoints[1].Sequence = 3
		fixture.Tasks[0].HeadSequence = 3
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrDependency) {
			t.Fatalf("Validate error = %v, want ErrDependency", err)
		}
	})
}

func TestValidateRejectsTamperedPayloadAndRefProjection(t *testing.T) {
	t.Run("non-canonical payload", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.CheckpointPayloads[fixtureCheckpoint1] = append(
			fixture.CheckpointPayloads[fixtureCheckpoint1],
			' ',
		)
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})
	t.Run("ref URI", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.CheckpointRefs[0].URI = "mem://files/" +
			uuid.MustParse("30000000-0000-0000-0000-000000000099").String()
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})
	t.Run("ref metadata", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.CheckpointRefs[0].Metadata = []byte(`{"item_index":1,"role":"source"}`)
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})
}

func TestValidateRejectsMissingMemReferenceDependency(t *testing.T) {
	fixture := validFixture(t)
	fixture.Memories = nil
	fixture.Manifest.Indexes.Memories.Count = 0
	err := Validate(fixture.BundleData, ValidationOptions{})
	if !errors.Is(err, ErrDependency) {
		t.Fatalf("Validate error = %v, want ErrDependency", err)
	}
}

func TestValidateRejectsManifestPathRewriteAndExclusionDrift(t *testing.T) {
	t.Run("path rewrite", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Manifest.Restore.PathRewrite = true
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})
	t.Run("missing exclusion", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Manifest.Exclusions = fixture.Manifest.Exclusions[:len(fixture.Manifest.Exclusions)-1]
		err := Validate(fixture.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})
}

func forgottenFixture(t *testing.T) WriteInput {
	t.Helper()
	fixture := validFixture(t)
	forgottenAt := fixtureTime.Add(time.Minute)
	memory := &fixture.Memories[0]
	memory.Kind = "forgotten"
	memory.Content = ""
	memory.Attributes = []byte(`{}`)
	memory.Path = "/"
	memory.EventAt = nil
	memory.SourceType = "forgotten"
	memory.SourceRef = ""
	memory.SourceFileID = nil
	memory.SourceFileSHA256 = ""
	memory.SourceLocator = []byte(`{}`)
	memory.ProducerAgent = ""
	memory.ProducerSession = ""
	memory.ProducerTask = ""
	memory.OriginRequestSHA256 = strings.Repeat("0", 64)
	memory.ContentSHA256 = strings.Repeat("0", 64)
	memory.LifecycleStatus = "forgotten"
	memory.StateVersion = 3
	memory.PinnedAt = nil
	memory.UsefulCount = 0
	memory.NotUsefulCount = 0
	memory.FeedbackAt = nil
	memory.ForgottenAt = timePointer(forgottenAt)
	memory.UpdatedAt = forgottenAt
	forgetEvent := MemoryEventRecord{
		ID:                   uuid.MustParse("45000000-0000-0000-0000-000000000002"),
		MemoryID:             fixtureMemoryID,
		Action:               "forget",
		IdempotencyKeySHA256: strings.Repeat("1", 64),
		ExpectedVersion:      2,
		ResultingVersion:     3,
		Reason:               "sensitive",
		CreatedAt:            forgottenAt,
	}
	var err error
	forgetEvent.OriginRequestSHA256, err = MemoryEventRequestSHA256(
		fixtureWorkspaceID,
		forgetEvent,
	)
	if err != nil {
		t.Fatalf("hash forget event fixture: %v", err)
	}
	fixture.MemoryEvents = append(fixture.MemoryEvents, forgetEvent)
	fixture.Manifest.Indexes.MemoryEvents.Count++
	return fixture
}

func sharedContentFixture(t *testing.T) WriteInput {
	t.Helper()
	fixture := validFixture(t)
	archiveFolderID := uuid.MustParse("20000000-0000-0000-0000-000000000002")
	fixture.Folders = append(fixture.Folders, FolderRecord{
		ID:        archiveFolderID,
		Path:      "/Archive",
		Name:      "Archive",
		CreatedAt: fixtureTime,
		UpdatedAt: fixtureTime,
	})
	second := fixture.Files[0]
	second.ID = uuid.MustParse("30000000-0000-0000-0000-000000000002")
	second.FolderID = uuidPointer(archiveFolderID)
	second.Name = "portable-copy.txt"
	second.Path = "/Archive"
	fixture.Files = append(fixture.Files, second)
	fixture.Manifest.Indexes.Folders.Count++
	fixture.Manifest.Indexes.Files.Count++
	// The archive catalog and BlobSources intentionally remain one-per-digest.
	return fixture
}
