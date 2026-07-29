package workspacebundle

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PeterGuy326/mem/server/internal/enrichmentkey"
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

func TestValidateFileEnrichmentState(t *testing.T) {
	fixture := validFixture(t)
	if err := Validate(fixture.BundleData, ValidationOptions{}); err != nil {
		t.Fatalf("Validate enriched fixture: %v", err)
	}

	t.Run("unknown source metadata field", func(t *testing.T) {
		broken := validFixture(t)
		broken.Files[0].SourceMetadata = []byte(
			`{"captured_at":"2026-07-28T20:00:00+08:00","prompt":"untrusted"}`,
		)
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})

	t.Run("null source metadata", func(t *testing.T) {
		broken := validFixture(t)
		broken.Files[0].SourceMetadata = []byte(`null`)
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})

	nullSourceFields := []struct {
		name string
		raw  string
		path string
	}{
		{"captured at", `{"captured_at":null}`, "source_metadata.captured_at"},
		{"source kind", `{"source_kind":null}`, "source_metadata.source_kind"},
		{"source name", `{"source_name":null}`, "source_metadata.source_name"},
		{"location", `{"location":null}`, "source_metadata.location"},
		{
			"location latitude",
			`{"location":{"lat":null,"lon":121.4737}}`,
			"source_metadata.location.lat",
		},
		{
			"location longitude",
			`{"location":{"lat":31.2304,"lon":null}}`,
			"source_metadata.location.lon",
		},
		{
			"location accuracy",
			`{"location":{"accuracy_m":null,"lat":31.2304,"lon":121.4737}}`,
			"source_metadata.location.accuracy_m",
		},
		{
			"location label",
			`{"location":{"label":null,"lat":31.2304,"lon":121.4737}}`,
			"source_metadata.location.label",
		},
	}
	for _, test := range nullSourceFields {
		t.Run("null "+test.name, func(t *testing.T) {
			broken := validFixture(t)
			broken.Files[0].SourceMetadata = []byte(test.raw)
			err := Validate(broken.BundleData, ValidationOptions{})
			if !errors.Is(err, ErrInvalidBundle) ||
				!strings.Contains(err.Error(), test.path+" must not be null") {
				t.Fatalf(
					"Validate error = %v, want ErrInvalidBundle for %s",
					err,
					test.path,
				)
			}
		})
	}

	for _, test := range []struct {
		name     string
		metadata map[string]any
		path     string
	}{
		{
			"source name format character",
			map[string]any{"source_name": "phone\u200bsync"},
			"source_metadata.source_name",
		},
		{
			"source name default ignorable",
			map[string]any{"source_name": "phone\u034fsync"},
			"source_metadata.source_name",
		},
		{
			"location label variation selector",
			map[string]any{
				"location": map[string]any{
					"label": "home\ufe0f",
					"lat":   31.2304,
					"lon":   121.4737,
				},
			},
			"source_metadata.location.label",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			broken := validFixture(t)
			raw, err := json.Marshal(test.metadata)
			if err != nil {
				t.Fatalf("marshal source metadata fixture: %v", err)
			}
			broken.Files[0].SourceMetadata = raw
			err = Validate(broken.BundleData, ValidationOptions{})
			if !errors.Is(err, ErrInvalidBundle) ||
				!strings.Contains(err.Error(), test.path+" is invalid") {
				t.Fatalf("Validate error = %v, want ErrInvalidBundle for %s", err, test.path)
			}
		})
	}

	t.Run("effective tag injected without provenance", func(t *testing.T) {
		broken := validFixture(t)
		broken.Files[0].Tags = append(broken.Files[0].Tags, "injected")
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})

	t.Run("accepted tag missing from effective projection", func(t *testing.T) {
		broken := validFixture(t)
		broken.Files[0].Tags = []string{"portable"}
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})

	t.Run("summary injected without accepted description", func(t *testing.T) {
		broken := validFixture(t)
		value := "unreviewed injected summary"
		broken.Files[0].Summary = &value
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})

	t.Run("caption injected without reviewable description", func(t *testing.T) {
		broken := validFixture(t)
		value := "unreviewed injected caption"
		broken.Files[0].Caption = &value
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})

	for _, test := range []struct {
		name    string
		caption string
	}{
		{name: "overlong v2 caption", caption: strings.Repeat("x", 2001)},
		{name: "control-bearing v2 caption", caption: "visible\nprivate"},
		{name: "JSON-like v2 caption", caption: `{"analysis":"private"}`},
		{name: "reasoning-bearing v2 caption", caption: "<think>private</think>visible"},
		{name: "BOM-prefixed JSON-like v2 caption", caption: "\ufeff{\"analysis\":\"private\"}"},
		{name: "embedded word joiner v2 caption", caption: "visible\u2060private"},
		{name: "variation-selector v2 caption", caption: "visible\ufe0fprivate"},
		{name: "combining-grapheme-joiner v2 caption", caption: "visible\u034fprivate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			broken := validFixture(t)
			broken.Files[0].Caption = &test.caption
			err := Validate(broken.BundleData, ValidationOptions{})
			if !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
			}
		})
	}

	t.Run("reasoning-bearing annotation", func(t *testing.T) {
		broken := validFixture(t)
		annotation := &broken.Files[0].Annotations[0]
		annotation.ValueText = `<reasoning visibility="hidden">private</reasoning>reviewed`
		annotation.StableKey = enrichmentkey.Stable(
			annotation.Kind,
			annotation.Source,
			annotation.AnalysisVersion,
			annotation.ValueText,
		)
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})

	for _, test := range []struct {
		name  string
		kind  string
		value string
	}{
		{
			name:  "JSON-like description annotation",
			kind:  "description",
			value: `{"analysis":"private","answer":"public"}`,
		},
		{
			name:  "JSON-like tag annotation",
			kind:  "tag",
			value: `{"analysis":"private"}`,
		},
		{
			name:  "BOM-prefixed JSON-like description annotation",
			kind:  "description",
			value: "\ufeff{\"analysis\":\"private\",\"answer\":\"public\"}",
		},
		{
			name:  "zero-width-prefixed array tag annotation",
			kind:  "tag",
			value: "\u200b[\"private\"]",
		},
		{
			name:  "combining-grapheme-joiner description annotation",
			kind:  "description",
			value: "\u034f{\"analysis\":\"private\"}",
		},
		{
			name:  "variation-selector tag annotation",
			kind:  "tag",
			value: "\ufe0f[\"private\"]",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			broken := validFixture(t)
			annotation := &broken.Files[0].Annotations[0]
			annotation.Kind = test.kind
			annotation.ValueText = test.value
			annotation.StableKey = enrichmentkey.Stable(
				annotation.Kind,
				annotation.Source,
				annotation.AnalysisVersion,
				annotation.ValueText,
			)
			err := Validate(broken.BundleData, ValidationOptions{})
			if !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
			}
		})
	}

	for _, test := range []struct {
		name  string
		field string
		value string
	}{
		{
			name:  "provider word joiner",
			field: "provider",
			value: "test\u2060private-provider",
		},
		{
			name:  "processor variation selector",
			field: "processor",
			value: "image\ufe0fprivate-processor",
		},
		{
			name:  "analysis version combining grapheme joiner",
			field: "analysis_version",
			value: "file-enrichment-\u034fprivate-version",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			broken := validFixture(t)
			annotation := &broken.Files[0].Annotations[0]
			switch test.field {
			case "provider":
				annotation.Provider = test.value
			case "processor":
				annotation.Processor = test.value
			case "analysis_version":
				annotation.AnalysisVersion = test.value
			}
			annotation.StableKey = enrichmentkey.Stable(
				annotation.Kind,
				annotation.Source,
				annotation.AnalysisVersion,
				annotation.ValueText,
			)
			err := Validate(broken.BundleData, ValidationOptions{})
			if !errors.Is(err, ErrInvalidBundle) ||
				!strings.Contains(err.Error(), "."+test.field) {
				t.Fatalf(
					"Validate error = %v, want ErrInvalidBundle for %s",
					err,
					test.field,
				)
			}
		})
	}

	t.Run("terminal decision missing timestamp", func(t *testing.T) {
		broken := validFixture(t)
		broken.Files[0].Annotations[0].DecidedAt = nil
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})

	t.Run("source capture time contradicts projection", func(t *testing.T) {
		broken := validFixture(t)
		value := broken.Files[0].TimelineAt.Add(time.Minute)
		broken.Files[0].TimelineAt = &value
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})

	t.Run("source capture nanoseconds project to database microseconds", func(t *testing.T) {
		valid := validFixture(t)
		value := time.Date(2026, time.July, 29, 8, 0, 0, 123456000, time.UTC)
		valid.Files[0].TimelineAt = &value
		valid.Files[0].SourceMetadata = []byte(
			`{"captured_at":"2026-07-29T08:00:00.123456789Z"}`,
		)
		if err := Validate(valid.BundleData, ValidationOptions{}); err != nil {
			t.Fatalf("Validate error = %v", err)
		}
	})

	t.Run("timeline projection exceeds database precision", func(t *testing.T) {
		broken := validFixture(t)
		value := broken.Files[0].TimelineAt.Add(time.Nanosecond)
		broken.Files[0].TimelineAt = &value
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})

	t.Run("source location contradicts projection", func(t *testing.T) {
		broken := validFixture(t)
		broken.Files[0].Geo = &FileGeoRecord{Lat: 0, Lon: 0}
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})

	t.Run("stable key contradicts annotation identity", func(t *testing.T) {
		broken := validFixture(t)
		broken.Files[0].Annotations[0].StableKey = "sha256:" + strings.Repeat("f", 64)
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Validate error = %v, want ErrIntegrity", err)
		}
	})

	t.Run("annotation time exceeds database precision", func(t *testing.T) {
		broken := validFixture(t)
		value := broken.Files[0].Annotations[0].DecidedAt.Add(time.Nanosecond)
		broken.Files[0].Annotations[0].DecidedAt = &value
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})

	t.Run("annotation control character reaches identity check", func(t *testing.T) {
		broken := validFixture(t)
		annotation := &broken.Files[0].Annotations[0]
		annotation.ValueText = "reviewed\n"
		annotation.StableKey = enrichmentkey.Stable(
			annotation.Kind,
			annotation.Source,
			annotation.AnalysisVersion,
			annotation.ValueText,
		)
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})

	t.Run("v2 fields cannot masquerade as v1", func(t *testing.T) {
		broken := validFixture(t)
		broken.Manifest.SchemaVersion = SchemaVersionV1
		broken.Manifest.Exclusions = ExclusionsV1()
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("Validate error = %v, want ErrUnsupportedVersion", err)
		}
	})

	t.Run("v2 enrichment fields are required", func(t *testing.T) {
		broken := validFixture(t)
		broken.Files[0].UserTags = nil
		err := Validate(broken.BundleData, ValidationOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Validate error = %v, want ErrInvalidBundle", err)
		}
	})
}

func TestValidateAcceptsLegacyV1FileRecord(t *testing.T) {
	fixture := validFixture(t)
	fixture.Manifest.SchemaVersion = SchemaVersionV1
	fixture.Manifest.Exclusions = ExclusionsV1()
	for index := range fixture.Files {
		fixture.Files[index].UserTags = nil
		fixture.Files[index].Geo = nil
		fixture.Files[index].SourceMetadata = nil
		fixture.Files[index].Annotations = nil
	}

	if err := Validate(fixture.BundleData, ValidationOptions{}); err != nil {
		t.Fatalf("Validate legacy v1 fixture: %v", err)
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
	t.Run("v1 exclusion declaration on v2", func(t *testing.T) {
		fixture := validFixture(t)
		fixture.Manifest.Exclusions = ExclusionsV1()
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
	second.Annotations = []FileAnnotationRecord{}
	second.Tags = append([]string{}, second.UserTags...)
	second.Summary = nil
	fixture.Files = append(fixture.Files, second)
	fixture.Manifest.Indexes.Folders.Count++
	fixture.Manifest.Indexes.Files.Count++
	// The archive catalog and BlobSources intentionally remain one-per-digest.
	return fixture
}
