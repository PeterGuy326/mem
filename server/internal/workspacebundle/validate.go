package workspacebundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/PeterGuy326/mem/server/internal/enrichmentkey"
	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/PeterGuy326/mem/server/internal/modeltext"
	"github.com/PeterGuy326/mem/server/internal/pathx"
	"github.com/google/uuid"
)

type ValidationOptions struct {
	Limits                     Limits
	CheckpointPayloadValidator CheckpointPayloadValidator
}

type validatedGraph struct {
	folders     map[uuid.UUID]FolderRecord
	files       map[uuid.UUID]FileRecord
	memories    map[uuid.UUID]MemoryRecord
	tasks       map[uuid.UUID]TaskRecord
	checkpoints map[uuid.UUID]CheckpointRecord
}

// Validate checks a fully decoded supported object graph without touching
// external state. It is safe to use both before archive creation and after
// archive integrity verification.
func Validate(data BundleData, options ValidationOptions) error {
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return err
	}
	payloadValidator := options.CheckpointPayloadValidator
	if payloadValidator == nil {
		payloadValidator = HandoffV1PayloadValidator{}
	}
	if err := validateManifest(data); err != nil {
		return err
	}
	for label, count := range map[string]int{
		"folders":         len(data.Folders),
		"files":           len(data.Files),
		"memories":        len(data.Memories),
		"memory_events":   len(data.MemoryEvents),
		"tasks":           len(data.Tasks),
		"checkpoints":     len(data.Checkpoints),
		"checkpoint_refs": len(data.CheckpointRefs),
	} {
		if count > limits.MaxRecordsPerIndex {
			return fmt.Errorf(
				"%w: %s exceeds %d records",
				ErrLimitExceeded,
				label,
				limits.MaxRecordsPerIndex,
			)
		}
	}

	graph := validatedGraph{
		folders:     make(map[uuid.UUID]FolderRecord, len(data.Folders)),
		files:       make(map[uuid.UUID]FileRecord, len(data.Files)),
		memories:    make(map[uuid.UUID]MemoryRecord, len(data.Memories)),
		tasks:       make(map[uuid.UUID]TaskRecord, len(data.Tasks)),
		checkpoints: make(map[uuid.UUID]CheckpointRecord, len(data.Checkpoints)),
	}
	if err := validateFolders(data.Folders, graph.folders); err != nil {
		return err
	}
	if err := validateFiles(
		data.Files,
		graph,
		data.Manifest.SchemaVersion,
		limits.MaxRecordsPerIndex,
	); err != nil {
		return err
	}
	if err := validateMemories(data.Memories, graph, limits); err != nil {
		return err
	}
	if err := validateMemoryEvents(
		data.MemoryEvents,
		graph,
		data.Manifest.Source.WorkspaceID,
	); err != nil {
		return err
	}
	if err := validateTasks(data.Tasks, graph.tasks); err != nil {
		return err
	}
	projections, err := validateCheckpoints(
		data.Checkpoints,
		data.CheckpointPayloads,
		graph,
		limits,
		payloadValidator,
	)
	if err != nil {
		return err
	}
	if err := validateLineage(data.Tasks, data.Checkpoints); err != nil {
		return err
	}
	if err := validateCheckpointReferences(
		data.CheckpointRefs,
		projections,
		graph,
		limits,
	); err != nil {
		return err
	}
	if err := validateBlobs(data.Files, data.Blobs, data.Manifest); err != nil {
		return err
	}
	return nil
}

func validateManifest(data BundleData) error {
	manifest := data.Manifest
	if manifest.Contract != ContractName {
		return fmt.Errorf(
			"%w: manifest contract must be %q",
			ErrInvalidBundle,
			ContractName,
		)
	}
	if manifest.SchemaVersion != SchemaVersionV1 &&
		manifest.SchemaVersion != SchemaVersionV2 {
		return fmt.Errorf(
			"%w: schema_version %d",
			ErrUnsupportedVersion,
			manifest.SchemaVersion,
		)
	}
	if manifest.BundleID == uuid.Nil {
		return fmt.Errorf("%w: manifest bundle_id is required", ErrInvalidBundle)
	}
	if err := validateTimestamp("manifest.created_at", manifest.CreatedAt); err != nil {
		return err
	}
	if manifest.Archive != (ArchiveDescriptor{
		Format:    ArchiveFormatZIP64,
		Layout:    ArchiveLayoutV1,
		MediaType: BundleMediaType,
	}) {
		return fmt.Errorf("%w: manifest archive descriptor is not v1", ErrInvalidBundle)
	}
	if manifest.Source.WorkspaceID == uuid.Nil {
		return fmt.Errorf("%w: source.workspace_id is required", ErrInvalidBundle)
	}
	if err := validateText("source.workspace_name", manifest.Source.WorkspaceName, 1, 1024); err != nil {
		return err
	}
	if err := validateText("source.exporter", manifest.Source.Exporter, 1, 200); err != nil {
		return err
	}
	if err := validateText("source.exporter_version", manifest.Source.ExporterVersion, 1, 200); err != nil {
		return err
	}
	if manifest.Scope.Path != pathx.Root || !manifest.Scope.Complete {
		return fmt.Errorf(
			"%w: v1 only supports a complete root scope",
			ErrInvalidBundle,
		)
	}
	if !equalStrings(
		manifest.Restore.Modes,
		[]string{RestoreModeFresh, RestoreModeMergeConservative},
	) ||
		!manifest.Restore.PreserveObjectIDs ||
		manifest.Restore.PathRewrite {
		return fmt.Errorf(
			"%w: v1 restore policy must preserve IDs and forbid path rewriting",
			ErrInvalidBundle,
		)
	}
	expectedExclusions := requiredExclusionsV2
	if manifest.SchemaVersion == SchemaVersionV1 {
		expectedExclusions = requiredExclusionsV1
	}
	if !equalStrings(manifest.Exclusions, expectedExclusions) {
		return fmt.Errorf(
			"%w: manifest exclusions must exactly match the v%d exclusion set",
			ErrInvalidBundle,
			manifest.SchemaVersion,
		)
	}
	expectedIndexes := []struct {
		label      string
		descriptor IndexDescriptor
		path       string
		count      int
	}{
		{"folders", manifest.Indexes.Folders, FoldersIndexPath, len(data.Folders)},
		{"files", manifest.Indexes.Files, FilesIndexPath, len(data.Files)},
		{"memories", manifest.Indexes.Memories, MemoriesIndexPath, len(data.Memories)},
		{
			"memory_events",
			manifest.Indexes.MemoryEvents,
			MemoryEventsIndexPath,
			len(data.MemoryEvents),
		},
		{"tasks", manifest.Indexes.Tasks, TasksIndexPath, len(data.Tasks)},
		{"checkpoints", manifest.Indexes.Checkpoints, CheckpointsIndexPath, len(data.Checkpoints)},
		{
			"checkpoint_refs",
			manifest.Indexes.CheckpointRefs,
			CheckpointRefsIndexPath,
			len(data.CheckpointRefs),
		},
	}
	for _, item := range expectedIndexes {
		if item.descriptor.Path != item.path || item.descriptor.Count != int64(item.count) {
			return fmt.Errorf(
				"%w: manifest index %s descriptor/count mismatch",
				ErrInvalidBundle,
				item.label,
			)
		}
	}
	if manifest.Payloads.PathPrefix != CheckpointPayloadPrefix ||
		manifest.Payloads.Count != int64(len(data.CheckpointPayloads)) {
		return fmt.Errorf(
			"%w: manifest checkpoint payload descriptor/count mismatch",
			ErrInvalidBundle,
		)
	}
	if manifest.Blobs.PathPrefix != ContentAddressedBlobRoot ||
		manifest.Blobs.Count != int64(len(data.Blobs)) {
		return fmt.Errorf(
			"%w: manifest blob descriptor/count mismatch",
			ErrInvalidBundle,
		)
	}
	if manifest.Checksums.Path != ChecksumsPath ||
		manifest.Checksums.Algorithm != "sha256" {
		return fmt.Errorf("%w: manifest checksum descriptor is not v1", ErrInvalidBundle)
	}
	return nil
}

func validateFolders(records []FolderRecord, folders map[uuid.UUID]FolderRecord) error {
	paths := make(map[string]uuid.UUID, len(records))
	for i, record := range records {
		label := fmt.Sprintf("folders[%d]", i)
		if record.ID == uuid.Nil {
			return fmt.Errorf("%w: %s.id is required", ErrInvalidBundle, label)
		}
		if _, exists := folders[record.ID]; exists {
			return fmt.Errorf("%w: duplicate folder UUID %s", ErrInvalidBundle, record.ID)
		}
		if err := validateCanonicalPath(label+".path", record.Path, false); err != nil {
			return err
		}
		if record.Path == pathx.Root {
			return fmt.Errorf("%w: %s must not store the implicit root", ErrInvalidBundle, label)
		}
		if err := pathx.ValidateName(record.Name); err != nil {
			return fmt.Errorf("%w: %s.name: %v", ErrInvalidBundle, label, err)
		}
		if pathx.Base(record.Path) != record.Name {
			return fmt.Errorf(
				"%w: %s.name does not match the final path segment",
				ErrDependency,
				label,
			)
		}
		if prior, exists := paths[record.Path]; exists {
			return fmt.Errorf(
				"%w: folder path %q is shared by %s and %s",
				ErrInvalidBundle,
				record.Path,
				prior,
				record.ID,
			)
		}
		if record.ParentID != nil && *record.ParentID == uuid.Nil {
			return fmt.Errorf("%w: %s.parent_id must be non-zero", ErrInvalidBundle, label)
		}
		if err := validateTimestamp(label+".created_at", record.CreatedAt); err != nil {
			return err
		}
		if err := validateTimestamp(label+".updated_at", record.UpdatedAt); err != nil {
			return err
		}
		folders[record.ID] = record
		paths[record.Path] = record.ID
	}
	for _, record := range records {
		parentPath := pathx.Parent(record.Path)
		if parentPath == pathx.Root {
			if record.ParentID != nil {
				return fmt.Errorf(
					"%w: top-level folder %s must have a null parent_id",
					ErrDependency,
					record.ID,
				)
			}
			continue
		}
		if record.ParentID == nil {
			return fmt.Errorf(
				"%w: folder %s is missing parent %q",
				ErrDependency,
				record.ID,
				parentPath,
			)
		}
		parent, exists := folders[*record.ParentID]
		if !exists || parent.Path != parentPath {
			return fmt.Errorf(
				"%w: folder %s parent_id does not resolve to %q",
				ErrDependency,
				record.ID,
				parentPath,
			)
		}
	}
	return nil
}

func validateFiles(
	records []FileRecord,
	graph validatedGraph,
	schemaVersion int,
	maxAnnotations int,
) error {
	annotationIDs := make(map[uuid.UUID]struct{})
	annotationCount := 0
	for i, record := range records {
		label := fmt.Sprintf("files[%d]", i)
		if err := validateFileRecordVersion(label, record, schemaVersion); err != nil {
			return err
		}
		annotationCount += len(record.Annotations)
		if annotationCount > maxAnnotations {
			return fmt.Errorf(
				"%w: file annotations exceed %d records",
				ErrLimitExceeded,
				maxAnnotations,
			)
		}
		if record.ID == uuid.Nil {
			return fmt.Errorf("%w: %s.id is required", ErrInvalidBundle, label)
		}
		if _, exists := graph.files[record.ID]; exists {
			return fmt.Errorf("%w: duplicate file UUID %s", ErrInvalidBundle, record.ID)
		}
		if err := pathx.ValidateName(record.Name); err != nil {
			return fmt.Errorf("%w: %s.name: %v", ErrInvalidBundle, label, err)
		}
		if err := validateCanonicalPath(label+".path", record.Path, true); err != nil {
			return err
		}
		if record.FolderID == nil {
			if record.Path != pathx.Root {
				return fmt.Errorf(
					"%w: file %s has non-root path without folder_id",
					ErrDependency,
					record.ID,
				)
			}
		} else {
			if *record.FolderID == uuid.Nil {
				return fmt.Errorf("%w: %s.folder_id must be non-zero", ErrInvalidBundle, label)
			}
			folder, exists := graph.folders[*record.FolderID]
			if !exists || folder.Path != record.Path {
				return fmt.Errorf(
					"%w: file %s folder_id does not resolve to path %q",
					ErrDependency,
					record.ID,
					record.Path,
				)
			}
		}
		if record.Size < 0 {
			return fmt.Errorf("%w: %s.size must be non-negative", ErrInvalidBundle, label)
		}
		if err := validateSHA256(label+".sha256", record.SHA256, false); err != nil {
			return err
		}
		if record.BlobPath != blobPath(record.SHA256) {
			return fmt.Errorf(
				"%w: %s.blob_path is not content addressed by sha256",
				ErrIntegrity,
				label,
			)
		}
		if err := validateText(label+".mime", record.MIME, 1, 1024); err != nil {
			return err
		}
		if record.Tags == nil {
			return fmt.Errorf("%w: %s.tags is required", ErrInvalidBundle, label)
		}
		for tagIndex, tag := range record.Tags {
			if err := validateText(
				fmt.Sprintf("%s.tags[%d]", label, tagIndex),
				tag,
				1,
				1024,
			); err != nil {
				return err
			}
		}
		if record.UserTags != nil {
			for tagIndex, tag := range record.UserTags {
				if err := validateText(
					fmt.Sprintf("%s.user_tags[%d]", label, tagIndex),
					tag,
					1,
					1024,
				); err != nil {
					return err
				}
			}
		}
		for field, value := range map[string]*string{
			label + ".summary": record.Summary,
			label + ".caption": record.Caption,
		} {
			if value != nil {
				if schemaVersion == SchemaVersionV2 {
					if strings.HasSuffix(field, ".caption") {
						normalized, ok := modeltext.NormalizePlain(*value, 2000)
						if !ok || normalized != *value {
							return fmt.Errorf(
								"%w: %s is not safe bounded model display text",
								ErrInvalidBundle,
								field,
							)
						}
					} else {
						normalized, ok := modeltext.NormalizePlain(*value, 2000)
						if !ok || normalized != *value {
							return fmt.Errorf(
								"%w: %s is not safe bounded model display text",
								ErrInvalidBundle,
								field,
							)
						}
					}
				} else {
					if err := validateText(field, *value, 0, 1_000_000); err != nil {
						return err
					}
				}
			}
		}
		if record.TimelineAt != nil && record.TimelineAt.IsZero() {
			return fmt.Errorf("%w: %s.timeline_at must not be zero", ErrInvalidBundle, label)
		}
		if record.TimelineAt != nil && record.TimelineAt.Nanosecond()%1_000 != 0 {
			return fmt.Errorf(
				"%w: %s.timeline_at exceeds PostgreSQL microsecond precision",
				ErrInvalidBundle,
				label,
			)
		}
		if record.Geo != nil {
			if math.IsNaN(record.Geo.Lat) || math.IsInf(record.Geo.Lat, 0) ||
				record.Geo.Lat < -90 || record.Geo.Lat > 90 {
				return fmt.Errorf("%w: %s.geo.lat is invalid", ErrInvalidBundle, label)
			}
			if math.IsNaN(record.Geo.Lon) || math.IsInf(record.Geo.Lon, 0) ||
				record.Geo.Lon < -180 || record.Geo.Lon > 180 {
				return fmt.Errorf("%w: %s.geo.lon is invalid", ErrInvalidBundle, label)
			}
		}
		if err := validatePortableSourceMetadata(label, record.SourceMetadata); err != nil {
			return err
		}
		if err := validateSourceMetadataProjection(
			label,
			record.SourceMetadata,
			record.TimelineAt,
			record.Geo,
		); err != nil {
			return err
		}
		if err := validateFileAnnotations(label, record, annotationIDs); err != nil {
			return err
		}
		projection := DeriveFileEnrichmentProjection(record)
		if !slices.Equal(record.Tags, projection.Tags) {
			return fmt.Errorf(
				"%w: %s.tags does not match user tags plus accepted suggestions",
				ErrIntegrity,
				label,
			)
		}
		if record.UserTags != nil && !slices.Equal(record.UserTags, projection.UserTags) {
			return fmt.Errorf(
				"%w: %s.user_tags is not a canonical unique sequence",
				ErrIntegrity,
				label,
			)
		}
		if !projection.Legacy && !equalOptionalText(record.Summary, projection.Summary) {
			return fmt.Errorf(
				"%w: %s.summary does not match the selected accepted description",
				ErrIntegrity,
				label,
			)
		}
		if !projection.Legacy && !equalOptionalText(record.Caption, projection.Caption) {
			return fmt.Errorf(
				"%w: %s.caption does not match an accepted or pending description",
				ErrIntegrity,
				label,
			)
		}
		if err := validateTimestamp(label+".created_at", record.CreatedAt); err != nil {
			return err
		}
		if err := validateTimestamp(label+".updated_at", record.UpdatedAt); err != nil {
			return err
		}
		graph.files[record.ID] = record
	}
	return nil
}

func validateFileRecordVersion(label string, record FileRecord, schemaVersion int) error {
	switch schemaVersion {
	case SchemaVersionV1:
		// These fields were added in v2. Requiring their absence keeps v1
		// archives readable by strict historical v1 implementations.
		if record.UserTags != nil ||
			record.Geo != nil ||
			len(bytes.TrimSpace(record.SourceMetadata)) != 0 ||
			record.Annotations != nil {
			return fmt.Errorf(
				"%w: %s uses v2 enrichment fields with schema_version 1",
				ErrUnsupportedVersion,
				label,
			)
		}
	case SchemaVersionV2:
		if record.UserTags == nil ||
			len(bytes.TrimSpace(record.SourceMetadata)) == 0 ||
			record.Annotations == nil {
			return fmt.Errorf(
				"%w: %s is missing required v2 enrichment fields",
				ErrInvalidBundle,
				label,
			)
		}
	default:
		return fmt.Errorf(
			"%w: schema_version %d",
			ErrUnsupportedVersion,
			schemaVersion,
		)
	}
	return nil
}

func validatePortableSourceMetadata(label string, raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		// Compatibility with pre-enrichment v1 bundles.
		return nil
	}
	if len(raw) > 4<<10 {
		return fmt.Errorf("%w: %s.source_metadata exceeds 4096 bytes", ErrLimitExceeded, label)
	}
	canonical, err := canonicalJSONObject(raw, 4, label+".source_metadata")
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return fmt.Errorf(
			"%w: %s.source_metadata is not canonical JSON",
			ErrIntegrity,
			label,
		)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("%w: decode %s.source_metadata: %v", ErrInvalidBundle, label, err)
	}
	allowed := map[string]struct{}{
		"captured_at": {}, "location": {}, "source_kind": {}, "source_name": {},
	}
	for key := range object {
		if _, exists := allowed[key]; !exists {
			return fmt.Errorf(
				"%w: %s.source_metadata contains unknown field %q",
				ErrInvalidBundle,
				label,
				key,
			)
		}
	}
	for key, value := range object {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf(
				"%w: %s.source_metadata.%s must not be null",
				ErrInvalidBundle,
				label,
				key,
			)
		}
	}
	if value, exists := object["captured_at"]; exists {
		var capturedAt string
		if err := json.Unmarshal(value, &capturedAt); err != nil {
			return fmt.Errorf("%w: %s.source_metadata.captured_at is invalid", ErrInvalidBundle, label)
		}
		if _, err := time.Parse(time.RFC3339, capturedAt); err != nil {
			return fmt.Errorf(
				"%w: %s.source_metadata.captured_at must be offset-bearing RFC3339",
				ErrInvalidBundle,
				label,
			)
		}
	}
	if value, exists := object["source_kind"]; exists {
		var sourceKind string
		if err := json.Unmarshal(value, &sourceKind); err != nil {
			return fmt.Errorf("%w: %s.source_metadata.source_kind is invalid", ErrInvalidBundle, label)
		}
		switch sourceKind {
		case "api", "web", "cli", "mcp", "mobile", "ai_device", "import", "other":
		default:
			return fmt.Errorf("%w: %s.source_metadata.source_kind is invalid", ErrInvalidBundle, label)
		}
	}
	if value, exists := object["source_name"]; exists {
		var sourceName string
		if err := json.Unmarshal(value, &sourceName); err != nil ||
			!validMetadataText(sourceName, 512) {
			return fmt.Errorf("%w: %s.source_metadata.source_name is invalid", ErrInvalidBundle, label)
		}
	}
	if value, exists := object["location"]; exists {
		if err := validatePortableLocation(label, value); err != nil {
			return err
		}
	}
	return nil
}

func validateSourceMetadataProjection(
	label string,
	raw json.RawMessage,
	timelineAt *time.Time,
	geo *FileGeoRecord,
) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var metadata struct {
		CapturedAt *string `json:"captured_at"`
		Location   *struct {
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
		} `json:"location"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return fmt.Errorf("%w: decode %s.source_metadata: %v", ErrInvalidBundle, label, err)
	}
	if metadata.CapturedAt != nil {
		capturedAt, err := time.Parse(time.RFC3339, *metadata.CapturedAt)
		if err != nil {
			return fmt.Errorf("%w: %s.source_metadata.captured_at is invalid", ErrInvalidBundle, label)
		}
		if timelineAt == nil || !timelineAt.Equal(capturedAt.Truncate(time.Microsecond)) {
			return fmt.Errorf(
				"%w: %s.timeline_at contradicts source_metadata.captured_at",
				ErrIntegrity,
				label,
			)
		}
	}
	if metadata.Location != nil {
		if geo == nil ||
			geo.Lat != metadata.Location.Lat ||
			geo.Lon != metadata.Location.Lon {
			return fmt.Errorf(
				"%w: %s.geo contradicts source_metadata.location",
				ErrIntegrity,
				label,
			)
		}
	}
	return nil
}

func validatePortableLocation(label string, raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("%w: %s.source_metadata.location must be an object", ErrInvalidBundle, label)
	}
	allowed := map[string]struct{}{"lat": {}, "lon": {}, "accuracy_m": {}, "label": {}}
	for key := range object {
		if _, exists := allowed[key]; !exists {
			return fmt.Errorf(
				"%w: %s.source_metadata.location contains unknown field %q",
				ErrInvalidBundle,
				label,
				key,
			)
		}
	}
	for key, value := range object {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf(
				"%w: %s.source_metadata.location.%s must not be null",
				ErrInvalidBundle,
				label,
				key,
			)
		}
	}
	var lat, lon float64
	if err := json.Unmarshal(object["lat"], &lat); err != nil ||
		math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
		return fmt.Errorf("%w: %s.source_metadata.location.lat is invalid", ErrInvalidBundle, label)
	}
	if err := json.Unmarshal(object["lon"], &lon); err != nil ||
		math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
		return fmt.Errorf("%w: %s.source_metadata.location.lon is invalid", ErrInvalidBundle, label)
	}
	if value, exists := object["accuracy_m"]; exists {
		var accuracy float64
		if err := json.Unmarshal(value, &accuracy); err != nil ||
			math.IsNaN(accuracy) || math.IsInf(accuracy, 0) ||
			accuracy < 0 || accuracy > 40_100_000 {
			return fmt.Errorf(
				"%w: %s.source_metadata.location.accuracy_m is invalid",
				ErrInvalidBundle,
				label,
			)
		}
	}
	if value, exists := object["label"]; exists {
		var locationLabel string
		if err := json.Unmarshal(value, &locationLabel); err != nil ||
			!validMetadataText(locationLabel, 512) {
			return fmt.Errorf(
				"%w: %s.source_metadata.location.label is invalid",
				ErrInvalidBundle,
				label,
			)
		}
	}
	return nil
}

func validMetadataText(value string, maxRunes int) bool {
	return len([]rune(value)) <= maxRunes &&
		strings.IndexFunc(value, unicode.IsControl) < 0 &&
		!modeltext.ContainsNonDisplay(value)
}

func validateFileAnnotations(
	label string,
	record FileRecord,
	annotationIDs map[uuid.UUID]struct{},
) error {
	stableKeys := make(map[string]struct{}, len(record.Annotations))
	for index, annotation := range record.Annotations {
		annotationLabel := fmt.Sprintf("%s.annotations[%d]", label, index)
		if annotation.ID == uuid.Nil {
			return fmt.Errorf("%w: %s.id is required", ErrInvalidBundle, annotationLabel)
		}
		if _, duplicate := annotationIDs[annotation.ID]; duplicate {
			return fmt.Errorf(
				"%w: duplicate file annotation UUID %s",
				ErrInvalidBundle,
				annotation.ID,
			)
		}
		annotationIDs[annotation.ID] = struct{}{}
		if err := validateText(annotationLabel+".stable_key", annotation.StableKey, 1, 255); err != nil {
			return err
		}
		expectedStableKey := enrichmentkey.Stable(
			annotation.Kind,
			annotation.Source,
			annotation.AnalysisVersion,
			annotation.ValueText,
		)
		if annotation.StableKey != expectedStableKey {
			return fmt.Errorf(
				"%w: %s.stable_key does not match its annotation identity",
				ErrIntegrity,
				annotationLabel,
			)
		}
		if _, duplicate := stableKeys[annotation.StableKey]; duplicate {
			return fmt.Errorf(
				"%w: %s has duplicate stable_key %q",
				ErrInvalidBundle,
				label,
				annotation.StableKey,
			)
		}
		stableKeys[annotation.StableKey] = struct{}{}
		switch annotation.Kind {
		case "description":
			if err := validateAnnotationText(
				annotationLabel+".value_text",
				annotation.ValueText,
				1,
				2000,
			); err != nil {
				return err
			}
			normalized, ok := modeltext.NormalizePlain(annotation.ValueText, 2000)
			if !ok || normalized != annotation.ValueText {
				return fmt.Errorf(
					"%w: %s.value_text is not safe plain model text",
					ErrInvalidBundle,
					annotationLabel,
				)
			}
		case "tag":
			if err := validateAnnotationText(
				annotationLabel+".value_text",
				annotation.ValueText,
				1,
				64,
			); err != nil {
				return err
			}
			normalized, ok := modeltext.NormalizePlain(annotation.ValueText, 64)
			if !ok || normalized != annotation.ValueText {
				return fmt.Errorf(
					"%w: %s.value_text is not safe plain model text",
					ErrInvalidBundle,
					annotationLabel,
				)
			}
		default:
			return fmt.Errorf("%w: %s.kind is invalid", ErrInvalidBundle, annotationLabel)
		}
		if modeltext.ContainsHiddenReasoning(annotation.ValueText) {
			return fmt.Errorf(
				"%w: %s.value_text contains hidden reasoning",
				ErrInvalidBundle,
				annotationLabel,
			)
		}
		if math.IsNaN(float64(annotation.Confidence)) ||
			math.IsInf(float64(annotation.Confidence), 0) ||
			annotation.Confidence < 0 || annotation.Confidence > 1 {
			return fmt.Errorf("%w: %s.confidence is invalid", ErrInvalidBundle, annotationLabel)
		}
		if annotation.Source != "model" {
			return fmt.Errorf("%w: %s.source is invalid", ErrInvalidBundle, annotationLabel)
		}
		for field, value := range map[string]string{
			"provider":         annotation.Provider,
			"processor":        annotation.Processor,
			"analysis_version": annotation.AnalysisVersion,
		} {
			minimum := 0
			if field == "analysis_version" {
				minimum = 1
			}
			maximum := 255
			if field != "provider" {
				maximum = 64
			}
			if err := validateAnnotationText(
				annotationLabel+"."+field,
				value,
				minimum,
				maximum,
			); err != nil {
				return err
			}
		}
		switch annotation.Status {
		case "pending", "superseded":
			if annotation.DecidedAt != nil {
				return fmt.Errorf(
					"%w: %s.decided_at must be empty for status %s",
					ErrInvalidBundle,
					annotationLabel,
					annotation.Status,
				)
			}
		case "accepted", "rejected":
			if annotation.DecidedAt == nil || annotation.DecidedAt.IsZero() {
				return fmt.Errorf(
					"%w: %s.decided_at is required for status %s",
					ErrInvalidBundle,
					annotationLabel,
					annotation.Status,
				)
			}
		default:
			return fmt.Errorf("%w: %s.status is invalid", ErrInvalidBundle, annotationLabel)
		}
		if annotation.StateVersion <= 0 {
			return fmt.Errorf("%w: %s.state_version must be positive", ErrInvalidBundle, annotationLabel)
		}
		if annotation.DecidedAt != nil && annotation.DecidedAt.Nanosecond()%1_000 != 0 {
			return fmt.Errorf(
				"%w: %s.decided_at exceeds PostgreSQL microsecond precision",
				ErrInvalidBundle,
				annotationLabel,
			)
		}
		if err := validateTimestamp(annotationLabel+".created_at", annotation.CreatedAt); err != nil {
			return err
		}
		if annotation.CreatedAt.Nanosecond()%1_000 != 0 {
			return fmt.Errorf(
				"%w: %s.created_at exceeds PostgreSQL microsecond precision",
				ErrInvalidBundle,
				annotationLabel,
			)
		}
		if err := validateTimestamp(annotationLabel+".updated_at", annotation.UpdatedAt); err != nil {
			return err
		}
		if annotation.UpdatedAt.Nanosecond()%1_000 != 0 {
			return fmt.Errorf(
				"%w: %s.updated_at exceeds PostgreSQL microsecond precision",
				ErrInvalidBundle,
				annotationLabel,
			)
		}
	}
	return nil
}

func validateAnnotationText(field, value string, minimum, maximum int) error {
	if err := validateText(field, value, minimum, maximum); err != nil {
		return err
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: %s contains a control character", ErrInvalidBundle, field)
	}
	if modeltext.ContainsNonDisplay(value) {
		return fmt.Errorf("%w: %s contains a non-display character", ErrInvalidBundle, field)
	}
	return nil
}

func equalOptionalText(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateMemories(records []MemoryRecord, graph validatedGraph, limits Limits) error {
	idempotencyKeys := make(map[string]uuid.UUID, len(records))
	validKinds := map[string]struct{}{
		"observation": {},
		"decision":    {},
		"preference":  {},
		"task_state":  {},
		"fact":        {},
		"note":        {},
		"artifact":    {},
		"forgotten":   {},
	}
	for i, record := range records {
		label := fmt.Sprintf("memories[%d]", i)
		if record.ID == uuid.Nil {
			return fmt.Errorf("%w: %s.id is required", ErrInvalidBundle, label)
		}
		if _, exists := graph.memories[record.ID]; exists {
			return fmt.Errorf("%w: duplicate memory UUID %s", ErrInvalidBundle, record.ID)
		}
		if _, ok := validKinds[record.Kind]; !ok {
			return fmt.Errorf("%w: %s.kind is invalid", ErrInvalidBundle, label)
		}
		if err := validateSHA256(label+".content_sha256", record.ContentSHA256, false); err != nil {
			return err
		}
		if err := validateSHA256(
			label+".origin_request_sha256",
			record.OriginRequestSHA256,
			false,
		); err != nil {
			return err
		}
		attributes, err := canonicalJSONObject(record.Attributes, limits.MaxJSONDepth, label+".attributes")
		if err != nil {
			return err
		}
		if !bytes.Equal(record.Attributes, attributes) {
			return fmt.Errorf("%w: %s.attributes is not canonical JSON", ErrIntegrity, label)
		}
		locator, err := canonicalJSONObject(
			record.SourceLocator,
			limits.MaxJSONDepth,
			label+".source_locator",
		)
		if err != nil {
			return err
		}
		if !bytes.Equal(record.SourceLocator, locator) {
			return fmt.Errorf("%w: %s.source_locator is not canonical JSON", ErrIntegrity, label)
		}
		if err := validateCanonicalPath(label+".path", record.Path, true); err != nil {
			return err
		}
		if !sourceTypePattern.MatchString(record.SourceType) {
			return fmt.Errorf("%w: %s.source_type is invalid", ErrInvalidBundle, label)
		}
		if len([]byte(record.SourceRef)) > 8192 {
			return fmt.Errorf("%w: %s.source_ref exceeds 8192 bytes", ErrInvalidBundle, label)
		}
		if err := validateText(label+".source_ref", record.SourceRef, 0, 8192); err != nil {
			return err
		}
		if record.SourceFileID != nil {
			if *record.SourceFileID == uuid.Nil {
				return fmt.Errorf("%w: %s.source_file_id must be non-zero", ErrInvalidBundle, label)
			}
			file, exists := graph.files[*record.SourceFileID]
			if !exists {
				return fmt.Errorf(
					"%w: memory %s source file %s is missing",
					ErrDependency,
					record.ID,
					*record.SourceFileID,
				)
			}
			if record.SourceFileSHA256 == "" || record.SourceFileSHA256 != file.SHA256 {
				return fmt.Errorf(
					"%w: memory %s source file sha256 mismatch",
					ErrDependency,
					record.ID,
				)
			}
		}
		if err := validateSHA256(
			label+".source_file_sha256",
			record.SourceFileSHA256,
			true,
		); err != nil {
			return err
		}
		for field, value := range map[string]string{
			label + ".producer_agent":   record.ProducerAgent,
			label + ".producer_session": record.ProducerSession,
			label + ".producer_task":    record.ProducerTask,
		} {
			if err := validateText(field, value, 0, 255); err != nil {
				return err
			}
		}
		if err := validateSHA256(
			label+".idempotency_key_sha256",
			record.IdempotencyKeySHA256,
			false,
		); err != nil {
			return err
		}
		if prior, exists := idempotencyKeys[record.IdempotencyKeySHA256]; exists {
			return fmt.Errorf(
				"%w: memories %s and %s share idempotency_key_sha256",
				ErrInvalidBundle,
				prior,
				record.ID,
			)
		}
		if record.StateVersion <= 0 {
			return fmt.Errorf("%w: %s.state_version must be positive", ErrInvalidBundle, label)
		}
		if record.UsefulCount < 0 || record.NotUsefulCount < 0 {
			return fmt.Errorf("%w: %s feedback counts must be non-negative", ErrInvalidBundle, label)
		}
		switch record.LifecycleStatus {
		case "active", "archived":
			if record.Kind == "forgotten" ||
				record.Content == "" ||
				record.Content != strings.TrimSpace(record.Content) ||
				len([]byte(record.Content)) > 64*1024 {
				return fmt.Errorf(
					"%w: %s.content must be canonical and at most 65536 bytes",
					ErrInvalidBundle,
					label,
				)
			}
			if err := validateText(label+".content", record.Content, 1, 65_536); err != nil {
				return err
			}
			if got := sha256Hex([]byte(record.Content)); got != record.ContentSHA256 {
				return fmt.Errorf(
					"%w: memory %s content sha256 mismatch",
					ErrIntegrity,
					record.ID,
				)
			}
			if record.ForgottenAt != nil {
				return fmt.Errorf(
					"%w: live memory %s must not have forgotten_at",
					ErrIntegrity,
					record.ID,
				)
			}
		case "forgotten":
			if err := validateForgottenMemoryTombstone(record); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %s.lifecycle_status is invalid", ErrInvalidBundle, label)
		}
		if err := validateTimestamp(label+".created_at", record.CreatedAt); err != nil {
			return err
		}
		if err := validateTimestamp(label+".updated_at", record.UpdatedAt); err != nil {
			return err
		}
		graph.memories[record.ID] = record
		idempotencyKeys[record.IdempotencyKeySHA256] = record.ID
	}
	return nil
}

func validateForgottenMemoryTombstone(record MemoryRecord) error {
	if record.Kind != "forgotten" ||
		record.Content != "" ||
		string(record.Attributes) != "{}" ||
		record.Path != pathx.Root ||
		record.EventAt != nil ||
		record.SourceType != "forgotten" ||
		record.SourceRef != "" ||
		record.SourceFileID != nil ||
		record.SourceFileSHA256 != "" ||
		string(record.SourceLocator) != "{}" ||
		record.ProducerAgent != "" ||
		record.ProducerSession != "" ||
		record.ProducerTask != "" ||
		record.OriginRequestSHA256 != strings.Repeat("0", 64) ||
		record.ContentSHA256 != strings.Repeat("0", 64) ||
		record.PinnedAt != nil ||
		record.UsefulCount != 0 ||
		record.NotUsefulCount != 0 ||
		record.FeedbackAt != nil ||
		record.ForgottenAt == nil {
		return fmt.Errorf(
			"%w: forgotten memory %s contains payload or invalid control state",
			ErrIntegrity,
			record.ID,
		)
	}
	if err := validateTimestamp("memory.forgotten_at", *record.ForgottenAt); err != nil {
		return err
	}
	return nil
}

func validateMemoryEvents(
	records []MemoryEventRecord,
	graph validatedGraph,
	sourceWorkspaceID uuid.UUID,
) error {
	eventIDs := make(map[uuid.UUID]struct{}, len(records))
	idempotencyKeys := make(map[string]uuid.UUID, len(records))
	grouped := make(map[uuid.UUID]map[int64]MemoryEventRecord, len(graph.memories))
	for i, record := range records {
		label := fmt.Sprintf("memory_events[%d]", i)
		if record.ID == uuid.Nil {
			return fmt.Errorf("%w: %s.id is required", ErrInvalidBundle, label)
		}
		if _, exists := eventIDs[record.ID]; exists {
			return fmt.Errorf("%w: duplicate memory event UUID %s", ErrInvalidBundle, record.ID)
		}
		if record.MemoryID == uuid.Nil {
			return fmt.Errorf("%w: %s.memory_id is required", ErrInvalidBundle, label)
		}
		if _, exists := graph.memories[record.MemoryID]; !exists {
			return fmt.Errorf(
				"%w: memory event %s targets missing memory %s",
				ErrDependency,
				record.ID,
				record.MemoryID,
			)
		}
		switch record.Action {
		case "pin", "unpin", "useful", "not_useful", "archive", "restore", "forget":
		default:
			return fmt.Errorf("%w: %s.action is invalid", ErrInvalidBundle, label)
		}
		if err := validateSHA256(
			label+".idempotency_key_sha256",
			record.IdempotencyKeySHA256,
			false,
		); err != nil {
			return err
		}
		if prior, exists := idempotencyKeys[record.IdempotencyKeySHA256]; exists {
			return fmt.Errorf(
				"%w: memory events %s and %s share idempotency_key",
				ErrInvalidBundle,
				prior,
				record.ID,
			)
		}
		if err := validateSHA256(
			label+".origin_request_sha256",
			record.OriginRequestSHA256,
			false,
		); err != nil {
			return err
		}
		expectedRequestSHA, err := MemoryEventRequestSHA256(sourceWorkspaceID, record)
		if err != nil {
			return err
		}
		if record.OriginRequestSHA256 != expectedRequestSHA {
			return fmt.Errorf(
				"%w: memory event %s origin request sha256 mismatch",
				ErrIntegrity,
				record.ID,
			)
		}
		if record.ExpectedVersion <= 0 ||
			record.ResultingVersion != record.ExpectedVersion+1 {
			return fmt.Errorf(
				"%w: %s version transition must be n -> n+1",
				ErrDependency,
				label,
			)
		}
		if record.Action == "forget" {
			switch record.Reason {
			case "user_request", "incorrect", "sensitive", "expired", "other":
			default:
				return fmt.Errorf("%w: %s forget reason is invalid", ErrInvalidBundle, label)
			}
		} else if record.Reason != "" {
			return fmt.Errorf(
				"%w: %s reason is only allowed for forget",
				ErrInvalidBundle,
				label,
			)
		}
		if err := validateTimestamp(label+".created_at", record.CreatedAt); err != nil {
			return err
		}
		versions := grouped[record.MemoryID]
		if versions == nil {
			versions = make(map[int64]MemoryEventRecord)
			grouped[record.MemoryID] = versions
		}
		if prior, exists := versions[record.ExpectedVersion]; exists {
			return fmt.Errorf(
				"%w: memory %s events %s and %s share expected_version %d",
				ErrDependency,
				record.MemoryID,
				prior.ID,
				record.ID,
				record.ExpectedVersion,
			)
		}
		versions[record.ExpectedVersion] = record
		eventIDs[record.ID] = struct{}{}
		idempotencyKeys[record.IdempotencyKeySHA256] = record.ID
	}

	for memoryID, memoryRecord := range graph.memories {
		versions := grouped[memoryID]
		expectedEvents := memoryRecord.StateVersion - 1
		if int64(len(versions)) != expectedEvents {
			return fmt.Errorf(
				"%w: memory %s state_version %d requires %d events, found %d",
				ErrDependency,
				memoryID,
				memoryRecord.StateVersion,
				expectedEvents,
				len(versions),
			)
		}
		var (
			pinnedAt       *time.Time
			usefulCount    int
			notUsefulCount int
			feedbackAt     *time.Time
			forgottenAt    *time.Time
			lifecycle      string
			previousTime   time.Time
		)
		for version := int64(1); version <= expectedEvents; version++ {
			event, exists := versions[version]
			if !exists || event.ResultingVersion != version+1 {
				return fmt.Errorf(
					"%w: memory %s event versions are not contiguous at %d",
					ErrDependency,
					memoryID,
					version,
				)
			}
			if !previousTime.IsZero() && event.CreatedAt.Before(previousTime) {
				return fmt.Errorf(
					"%w: memory %s event timestamps go backwards at version %d",
					ErrDependency,
					memoryID,
					version,
				)
			}
			eventTime := event.CreatedAt
			switch event.Action {
			case "pin":
				pinnedAt = &eventTime
			case "unpin":
				pinnedAt = nil
			case "useful":
				usefulCount++
				feedbackAt = &eventTime
			case "not_useful":
				notUsefulCount++
				feedbackAt = &eventTime
			case "archive":
				lifecycle = "archived"
			case "restore":
				lifecycle = "active"
			case "forget":
				if version != expectedEvents {
					return fmt.Errorf(
						"%w: memory %s forget event must be terminal",
						ErrDependency,
						memoryID,
					)
				}
				lifecycle = "forgotten"
				pinnedAt = nil
				usefulCount = 0
				notUsefulCount = 0
				feedbackAt = nil
				forgottenAt = &eventTime
			}
			previousTime = event.CreatedAt
		}
		if !sameOptionalTime(pinnedAt, memoryRecord.PinnedAt) ||
			usefulCount != memoryRecord.UsefulCount ||
			notUsefulCount != memoryRecord.NotUsefulCount ||
			!sameOptionalTime(feedbackAt, memoryRecord.FeedbackAt) ||
			!sameOptionalTime(forgottenAt, memoryRecord.ForgottenAt) {
			return fmt.Errorf(
				"%w: memory %s control projection disagrees with events",
				ErrIntegrity,
				memoryID,
			)
		}
		if lifecycle != "" && lifecycle != memoryRecord.LifecycleStatus {
			return fmt.Errorf(
				"%w: memory %s lifecycle projection disagrees with events",
				ErrIntegrity,
				memoryID,
			)
		}
		if memoryRecord.LifecycleStatus == "forgotten" && lifecycle != "forgotten" {
			return fmt.Errorf(
				"%w: forgotten memory %s is missing its terminal forget event",
				ErrDependency,
				memoryID,
			)
		}
		if !previousTime.IsZero() && memoryRecord.UpdatedAt.Before(previousTime) {
			return fmt.Errorf(
				"%w: memory %s updated_at precedes its latest event",
				ErrDependency,
				memoryID,
			)
		}
	}
	return nil
}

func sameOptionalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func validateTasks(records []TaskRecord, tasks map[uuid.UUID]TaskRecord) error {
	taskKeys := make(map[string]uuid.UUID, len(records))
	for i, record := range records {
		label := fmt.Sprintf("tasks[%d]", i)
		if record.ID == uuid.Nil {
			return fmt.Errorf("%w: %s.id is required", ErrInvalidBundle, label)
		}
		if _, exists := tasks[record.ID]; exists {
			return fmt.Errorf("%w: duplicate task UUID %s", ErrInvalidBundle, record.ID)
		}
		if err := validateText(label+".task_key", record.TaskKey, 1, 200); err != nil {
			return err
		}
		if record.TaskKey != strings.TrimSpace(record.TaskKey) {
			return fmt.Errorf("%w: %s.task_key is not canonical", ErrInvalidBundle, label)
		}
		if prior, exists := taskKeys[record.TaskKey]; exists {
			return fmt.Errorf(
				"%w: tasks %s and %s share task_key %q",
				ErrInvalidBundle,
				prior,
				record.ID,
				record.TaskKey,
			)
		}
		if err := validateCanonicalPath(label+".scope_path", record.ScopePath, true); err != nil {
			return err
		}
		if record.HeadSequence < 0 {
			return fmt.Errorf("%w: %s.head_sequence is negative", ErrInvalidBundle, label)
		}
		if (record.HeadSequence == 0) != (record.HeadCheckpointID == nil) {
			return fmt.Errorf(
				"%w: %s head ID and sequence must both be empty or populated",
				ErrDependency,
				label,
			)
		}
		if record.HeadCheckpointID != nil && *record.HeadCheckpointID == uuid.Nil {
			return fmt.Errorf("%w: %s.head_checkpoint_id must be non-zero", ErrInvalidBundle, label)
		}
		if err := validateTimestamp(label+".created_at", record.CreatedAt); err != nil {
			return err
		}
		if err := validateTimestamp(label+".updated_at", record.UpdatedAt); err != nil {
			return err
		}
		tasks[record.ID] = record
		taskKeys[record.TaskKey] = record.ID
	}
	return nil
}

func validateCheckpoints(
	records []CheckpointRecord,
	payloads map[uuid.UUID][]byte,
	graph validatedGraph,
	limits Limits,
	payloadValidator CheckpointPayloadValidator,
) (map[uuid.UUID]PayloadProjection, error) {
	idempotencyKeys := make(map[string]uuid.UUID, len(records))
	projections := make(map[uuid.UUID]PayloadProjection, len(records))
	for i, record := range records {
		label := fmt.Sprintf("checkpoints[%d]", i)
		if record.ID == uuid.Nil {
			return nil, fmt.Errorf("%w: %s.id is required", ErrInvalidBundle, label)
		}
		if _, exists := graph.checkpoints[record.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate checkpoint UUID %s", ErrInvalidBundle, record.ID)
		}
		task, exists := graph.tasks[record.TaskID]
		if record.TaskID == uuid.Nil || !exists {
			return nil, fmt.Errorf(
				"%w: checkpoint %s task %s is missing",
				ErrDependency,
				record.ID,
				record.TaskID,
			)
		}
		if record.Sequence <= 0 {
			return nil, fmt.Errorf("%w: %s.sequence must be positive", ErrInvalidBundle, label)
		}
		switch record.CheckpointKind {
		case handoff.CheckpointKindCheckpoint, handoff.CheckpointKindHandoff:
		default:
			return nil, fmt.Errorf("%w: %s.checkpoint_kind is invalid", ErrInvalidBundle, label)
		}
		if record.Contract != handoff.ContractName ||
			record.SchemaVersion != handoff.SchemaVersionV1 {
			return nil, fmt.Errorf(
				"%w: %s must use mem.handoff schema 1",
				ErrUnsupportedVersion,
				label,
			)
		}
		if record.BaseCheckpointID != nil && *record.BaseCheckpointID == uuid.Nil {
			return nil, fmt.Errorf("%w: %s.base_checkpoint_id must be non-zero", ErrInvalidBundle, label)
		}
		if err := validateCanonicalPath(label+".scope_path", record.ScopePath, true); err != nil {
			return nil, err
		}
		if record.PayloadPath != checkpointPayloadPath(record.ID) {
			return nil, fmt.Errorf(
				"%w: %s.payload_path is not the fixed v1 path",
				ErrInvalidBundle,
				label,
			)
		}
		if err := validateSHA256(label+".payload_sha256", record.PayloadSHA256, false); err != nil {
			return nil, err
		}
		if err := validateSHA256(
			label+".origin_request_sha256",
			record.OriginRequestSHA256,
			false,
		); err != nil {
			return nil, err
		}
		if err := validateText(label+".idempotency_key", record.IdempotencyKey, 1, 200); err != nil {
			return nil, err
		}
		if prior, exists := idempotencyKeys[record.IdempotencyKey]; exists {
			return nil, fmt.Errorf(
				"%w: checkpoints %s and %s share idempotency_key",
				ErrInvalidBundle,
				prior,
				record.ID,
			)
		}
		if err := validateText(label+".producer_agent", record.ProducerAgent, 1, 200); err != nil {
			return nil, err
		}
		if err := validateText(label+".producer_session", record.ProducerSession, 0, 200); err != nil {
			return nil, err
		}
		if err := validateTimestamp(label+".created_at", record.CreatedAt); err != nil {
			return nil, err
		}
		payload, exists := payloads[record.ID]
		if !exists {
			return nil, fmt.Errorf(
				"%w: checkpoint %s payload is missing",
				ErrDependency,
				record.ID,
			)
		}
		if uint64(len(payload)) > limits.MaxMetadataEntrySize {
			return nil, fmt.Errorf(
				"%w: checkpoint %s payload exceeds metadata limit",
				ErrLimitExceeded,
				record.ID,
			)
		}
		if err := validateJSONDepth(
			payload,
			limits.MaxJSONDepth,
			"checkpoint payload "+record.ID.String(),
		); err != nil {
			return nil, err
		}
		projection, err := payloadValidator.ValidateCheckpointPayload(payload, task, record)
		if err != nil {
			return nil, err
		}
		graph.checkpoints[record.ID] = record
		projections[record.ID] = projection
		idempotencyKeys[record.IdempotencyKey] = record.ID
	}
	for id := range payloads {
		if _, exists := graph.checkpoints[id]; !exists {
			return nil, fmt.Errorf(
				"%w: orphan checkpoint payload %s",
				ErrDependency,
				id,
			)
		}
	}
	return projections, nil
}

func validateLineage(tasks []TaskRecord, checkpoints []CheckpointRecord) error {
	grouped := make(map[uuid.UUID]map[int64]CheckpointRecord, len(tasks))
	for _, checkpoint := range checkpoints {
		sequences := grouped[checkpoint.TaskID]
		if sequences == nil {
			sequences = make(map[int64]CheckpointRecord)
			grouped[checkpoint.TaskID] = sequences
		}
		if prior, exists := sequences[checkpoint.Sequence]; exists {
			return fmt.Errorf(
				"%w: task %s checkpoints %s and %s share sequence %d",
				ErrDependency,
				checkpoint.TaskID,
				prior.ID,
				checkpoint.ID,
				checkpoint.Sequence,
			)
		}
		sequences[checkpoint.Sequence] = checkpoint
	}
	for _, task := range tasks {
		sequences := grouped[task.ID]
		if len(sequences) == 0 {
			if task.HeadSequence != 0 || task.HeadCheckpointID != nil {
				return fmt.Errorf(
					"%w: task %s has a head but no checkpoints",
					ErrDependency,
					task.ID,
				)
			}
			continue
		}
		for sequence := int64(1); sequence <= int64(len(sequences)); sequence++ {
			checkpoint, exists := sequences[sequence]
			if !exists {
				return fmt.Errorf(
					"%w: task %s checkpoint sequence is not contiguous at %d",
					ErrDependency,
					task.ID,
					sequence,
				)
			}
			if sequence == 1 {
				if checkpoint.BaseCheckpointID != nil {
					return fmt.Errorf(
						"%w: task %s sequence 1 must not have a base",
						ErrDependency,
						task.ID,
					)
				}
				continue
			}
			previous := sequences[sequence-1]
			if checkpoint.BaseCheckpointID == nil ||
				*checkpoint.BaseCheckpointID != previous.ID {
				return fmt.Errorf(
					"%w: task %s sequence %d does not extend %s",
					ErrDependency,
					task.ID,
					sequence,
					previous.ID,
				)
			}
		}
		lastSequence := int64(len(sequences))
		last := sequences[lastSequence]
		if task.HeadSequence != lastSequence ||
			task.HeadCheckpointID == nil ||
			*task.HeadCheckpointID != last.ID {
			return fmt.Errorf(
				"%w: task %s head does not identify its chain tail",
				ErrDependency,
				task.ID,
			)
		}
	}
	return nil
}

func validateCheckpointReferences(
	records []CheckpointRefRecord,
	projections map[uuid.UUID]PayloadProjection,
	graph validatedGraph,
	limits Limits,
) error {
	grouped := make(map[uuid.UUID]map[int]CheckpointRefRecord, len(projections))
	for i, record := range records {
		label := fmt.Sprintf("checkpoint_refs[%d]", i)
		if record.CheckpointID == uuid.Nil {
			return fmt.Errorf("%w: %s.checkpoint_id is required", ErrInvalidBundle, label)
		}
		if _, exists := graph.checkpoints[record.CheckpointID]; !exists {
			return fmt.Errorf(
				"%w: checkpoint ref targets missing checkpoint %s",
				ErrDependency,
				record.CheckpointID,
			)
		}
		if record.Ordinal < 0 {
			return fmt.Errorf("%w: %s.ordinal is negative", ErrInvalidBundle, label)
		}
		switch record.Relation {
		case "decision", "next_step", "blocker", "artifact":
		default:
			return fmt.Errorf("%w: %s.relation is invalid", ErrInvalidBundle, label)
		}
		if err := validateText(label+".uri", record.URI, 1, 2048); err != nil {
			return err
		}
		if err := validateSHA256(
			label+".expected_sha256",
			record.ExpectedSHA256,
			true,
		); err != nil {
			return err
		}
		metadata, err := canonicalJSONObject(record.Metadata, limits.MaxJSONDepth, label+".metadata")
		if err != nil {
			return err
		}
		if !bytes.Equal(record.Metadata, metadata) {
			return fmt.Errorf("%w: %s.metadata is not canonical JSON", ErrIntegrity, label)
		}
		ordinals := grouped[record.CheckpointID]
		if ordinals == nil {
			ordinals = make(map[int]CheckpointRefRecord)
			grouped[record.CheckpointID] = ordinals
		}
		if _, exists := ordinals[record.Ordinal]; exists {
			return fmt.Errorf(
				"%w: duplicate checkpoint %s ref ordinal %d",
				ErrInvalidBundle,
				record.CheckpointID,
				record.Ordinal,
			)
		}
		ordinals[record.Ordinal] = record
	}
	for checkpointID, projection := range projections {
		actual := grouped[checkpointID]
		if len(actual) != len(projection.References) {
			return fmt.Errorf(
				"%w: checkpoint %s reference count disagrees with payload",
				ErrIntegrity,
				checkpointID,
			)
		}
		for ordinal, expected := range projection.References {
			record, exists := actual[ordinal]
			if !exists {
				return fmt.Errorf(
					"%w: checkpoint %s reference ordinals are not contiguous",
					ErrIntegrity,
					checkpointID,
				)
			}
			actualMetadata, _ := canonicalJSONObject(
				record.Metadata,
				limits.MaxJSONDepth,
				"checkpoint ref metadata",
			)
			expectedMetadata, _ := canonicalJSONObject(
				expected.Metadata,
				limits.MaxJSONDepth,
				"projected checkpoint ref metadata",
			)
			if record.Relation != expected.Relation ||
				record.URI != expected.URI ||
				record.ExpectedSHA256 != expected.ExpectedSHA256 ||
				record.Required != expected.Required ||
				!bytes.Equal(actualMetadata, expectedMetadata) {
				return fmt.Errorf(
					"%w: checkpoint %s ref ordinal %d disagrees with payload",
					ErrIntegrity,
					checkpointID,
					ordinal,
				)
			}
			if err := validateReferenceDependency(record, graph); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateReferenceDependency(record CheckpointRefRecord, graph validatedGraph) error {
	parsed, err := url.Parse(record.URI)
	if err != nil {
		return fmt.Errorf("%w: reference URI %q: %v", ErrInvalidBundle, record.URI, err)
	}
	if parsed.Scheme != "mem" {
		return nil
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" {
		return fmt.Errorf(
			"%w: mem URI %q must contain only an authority and UUID path",
			ErrDependency,
			record.URI,
		)
	}
	escaped := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if escaped == "" || strings.Contains(escaped, "/") {
		return fmt.Errorf("%w: malformed mem URI %q", ErrDependency, record.URI)
	}
	value, err := url.PathUnescape(escaped)
	if err != nil || strings.Contains(value, "/") {
		return fmt.Errorf("%w: malformed mem URI %q", ErrDependency, record.URI)
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("%w: malformed mem URI UUID in %q", ErrDependency, record.URI)
	}
	var actualSHA string
	switch parsed.Host {
	case "folders":
		if _, exists := graph.folders[id]; !exists {
			return missingURI(record.URI)
		}
	case "files":
		value, exists := graph.files[id]
		if !exists {
			return missingURI(record.URI)
		}
		actualSHA = value.SHA256
	case "memories":
		value, exists := graph.memories[id]
		if !exists {
			return missingURI(record.URI)
		}
		actualSHA = value.ContentSHA256
	case "tasks":
		if _, exists := graph.tasks[id]; !exists {
			return missingURI(record.URI)
		}
	case "checkpoints":
		value, exists := graph.checkpoints[id]
		if !exists {
			return missingURI(record.URI)
		}
		actualSHA = value.PayloadSHA256
	default:
		return fmt.Errorf(
			"%w: unsupported mem URI authority %q",
			ErrDependency,
			parsed.Host,
		)
	}
	if record.ExpectedSHA256 != "" &&
		(actualSHA == "" || actualSHA != record.ExpectedSHA256) {
		return fmt.Errorf(
			"%w: reference %q expected sha256 does not match target",
			ErrDependency,
			record.URI,
		)
	}
	return nil
}

func missingURI(uri string) error {
	return fmt.Errorf("%w: reference target %q is missing", ErrDependency, uri)
}

func validateBlobs(files []FileRecord, blobs []BlobInfo, manifest Manifest) error {
	byDigest := make(map[string]BlobInfo, len(blobs))
	referenced := make(map[string]struct{}, len(blobs))
	var total int64
	for i, blob := range blobs {
		label := fmt.Sprintf("blobs[%d]", i)
		if err := validateSHA256(label+".sha256", blob.SHA256, false); err != nil {
			return err
		}
		if blob.Path != blobPath(blob.SHA256) {
			return fmt.Errorf("%w: %s.path is not content addressed", ErrIntegrity, label)
		}
		if blob.Size < 0 {
			return fmt.Errorf("%w: %s.size is negative", ErrInvalidBundle, label)
		}
		if _, exists := byDigest[blob.SHA256]; exists {
			return fmt.Errorf("%w: duplicate blob sha256 %s", ErrInvalidBundle, blob.SHA256)
		}
		if blob.Size > math.MaxInt64-total {
			return fmt.Errorf("%w: total blob size overflows int64", ErrLimitExceeded)
		}
		total += blob.Size
		byDigest[blob.SHA256] = blob
	}
	for _, file := range files {
		blob, exists := byDigest[file.SHA256]
		if !exists {
			return fmt.Errorf(
				"%w: file %s blob %s is missing",
				ErrDependency,
				file.ID,
				file.SHA256,
			)
		}
		if blob.Size != file.Size || blob.Path != file.BlobPath {
			return fmt.Errorf(
				"%w: file %s blob size/path mismatch",
				ErrDependency,
				file.ID,
			)
		}
		referenced[file.SHA256] = struct{}{}
	}
	if len(referenced) != len(byDigest) {
		extras := make([]string, 0, len(byDigest)-len(referenced))
		for digest := range byDigest {
			if _, exists := referenced[digest]; !exists {
				extras = append(extras, digest)
			}
		}
		sort.Strings(extras)
		return fmt.Errorf("%w: orphan blobs %s", ErrDependency, strings.Join(extras, ","))
	}
	if manifest.Blobs.TotalSize != total {
		return fmt.Errorf("%w: manifest total blob size mismatch", ErrInvalidBundle)
	}
	return nil
}

func validateCanonicalPath(field, value string, allowRoot bool) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidBundle, field)
	}
	normalized, err := pathx.Normalize(value)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidBundle, field, err)
	}
	if normalized != value || (!allowRoot && value == pathx.Root) {
		return fmt.Errorf("%w: %s is not a permitted canonical path", ErrInvalidBundle, field)
	}
	return nil
}

func validateTimestamp(field string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%w: %s is required", ErrInvalidBundle, field)
	}
	return nil
}
