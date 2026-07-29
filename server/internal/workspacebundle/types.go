package workspacebundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

const (
	ContractName         = "mem.workspace_bundle"
	SchemaVersionV1      = 1
	SchemaVersionV2      = 2
	CurrentSchemaVersion = SchemaVersionV2

	ArchiveFormatZIP64 = "zip64"
	ArchiveLayoutV1    = "fixed-v1"
	// BundleMediaType is intentionally parameter-free. The schema version is
	// carried and validated in manifest.json rather than an HTTP parameter.
	BundleMediaType = "application/vnd.mem.workspace-bundle+zip"

	ManifestPath             = "manifest.json"
	ChecksumsPath            = "checksums.sha256"
	FoldersIndexPath         = "objects/folders.ndjson"
	FilesIndexPath           = "objects/files.ndjson"
	MemoriesIndexPath        = "objects/memories.ndjson"
	MemoryEventsIndexPath    = "objects/memory_events.ndjson"
	TasksIndexPath           = "objects/tasks.ndjson"
	CheckpointsIndexPath     = "objects/checkpoints.ndjson"
	CheckpointRefsIndexPath  = "objects/checkpoint_refs.ndjson"
	CheckpointPayloadPrefix  = "payloads/checkpoints/"
	ContentAddressedBlobRoot = "blobs/sha256/"

	RestoreModeFresh             = "fresh"
	RestoreModeMergeConservative = "merge_conservative"
)

// requiredExclusionsV1 is the exact, ordered declaration of state that v1
// never transports. Authentication state and derived model data are
// target-local.
var requiredExclusionsV1 = []string{
	"auth.users",
	"auth.password_hashes",
	"auth.tokens",
	"auth.token_hashes",
	"auth.sessions",
	"auth.workspace_memberships",
	"provenance.user_ids",
	"provenance.token_ids",
	"memory.raw_idempotency_keys",
	"storage.storage_keys",
	"storage.presigned_urls",
	"providers.settings",
	"providers.credentials",
	"runtime.environment",
	"derived.embeddings_text",
	"derived.embeddings_visual",
	"derived.embeddings_face",
	"derived.entities",
	"derived.file_entities",
	"derived.file_relations",
	"runtime.worker_jobs",
	"runtime.index_state",
}

// v2 makes file enrichment provenance portable while deliberately leaving
// reproducible processor output and unsafe model internals target-local.
var requiredExclusionsV2 = func() []string {
	exclusions := append([]string(nil), requiredExclusionsV1...)
	return append(
		exclusions,
		"derived.file_processor_metadata",
		"derived.raw_processor_errors",
		"derived.raw_provider_errors",
		"derived.generated_reasoning",
	)
}()

var requiredIndexPathsV1 = []string{
	FoldersIndexPath,
	FilesIndexPath,
	MemoriesIndexPath,
	MemoryEventsIndexPath,
	TasksIndexPath,
	CheckpointsIndexPath,
	CheckpointRefsIndexPath,
}

// ExclusionsV1 returns a copy of the mandatory v1 exclusion declaration.
func ExclusionsV1() []string {
	return append([]string(nil), requiredExclusionsV1...)
}

// ExclusionsV2 returns a copy of the mandatory v2 exclusion declaration.
func ExclusionsV2() []string {
	return append([]string(nil), requiredExclusionsV2...)
}

// IndexPathsV1 returns the fixed NDJSON indexes present even for an empty
// workspace.
func IndexPathsV1() []string {
	return append([]string(nil), requiredIndexPathsV1...)
}

type Manifest struct {
	Contract      string            `json:"contract"`
	SchemaVersion int               `json:"schema_version"`
	BundleID      uuid.UUID         `json:"bundle_id"`
	CreatedAt     time.Time         `json:"created_at"`
	Archive       ArchiveDescriptor `json:"archive"`
	Source        SourceDescriptor  `json:"source"`
	Scope         ScopeDescriptor   `json:"scope"`
	Restore       RestorePolicy     `json:"restore"`
	Indexes       IndexCatalog      `json:"indexes"`
	Payloads      PayloadCatalog    `json:"checkpoint_payloads"`
	Blobs         BlobCatalog       `json:"blobs"`
	Checksums     ChecksumCatalog   `json:"checksums"`
	Exclusions    []string          `json:"exclusions"`
}

type ArchiveDescriptor struct {
	Format    string `json:"format"`
	Layout    string `json:"layout"`
	MediaType string `json:"media_type"`
}

type SourceDescriptor struct {
	WorkspaceID     uuid.UUID `json:"workspace_id"`
	WorkspaceName   string    `json:"workspace_name"`
	Exporter        string    `json:"exporter"`
	ExporterVersion string    `json:"exporter_version"`
}

type ScopeDescriptor struct {
	Path     string `json:"path"`
	Complete bool   `json:"complete"`
}

type RestorePolicy struct {
	Modes             []string `json:"modes"`
	PreserveObjectIDs bool     `json:"preserve_object_ids"`
	PathRewrite       bool     `json:"path_rewrite"`
}

type IndexCatalog struct {
	Folders        IndexDescriptor `json:"folders"`
	Files          IndexDescriptor `json:"files"`
	Memories       IndexDescriptor `json:"memories"`
	MemoryEvents   IndexDescriptor `json:"memory_events"`
	Tasks          IndexDescriptor `json:"tasks"`
	Checkpoints    IndexDescriptor `json:"checkpoints"`
	CheckpointRefs IndexDescriptor `json:"checkpoint_refs"`
}

type IndexDescriptor struct {
	Path  string `json:"path"`
	Count int64  `json:"count"`
}

type PayloadCatalog struct {
	PathPrefix string `json:"path_prefix"`
	Count      int64  `json:"count"`
}

type BlobCatalog struct {
	PathPrefix string `json:"path_prefix"`
	Count      int64  `json:"count"`
	TotalSize  int64  `json:"total_size"`
}

type ChecksumCatalog struct {
	Path      string `json:"path"`
	Algorithm string `json:"algorithm"`
}

// ObjectCounts is accepted by NewManifest so callers cannot accidentally
// diverge from the fixed index layout.
type ObjectCounts struct {
	Folders            int64
	Files              int64
	Memories           int64
	MemoryEvents       int64
	Tasks              int64
	Checkpoints        int64
	CheckpointRefs     int64
	CheckpointPayloads int64
	Blobs              int64
	BlobBytes          int64
}

// NewManifest constructs the invariant portions of a full-root manifest using
// the current writer schema. Readers continue to accept legacy v1 archives.
func NewManifest(
	bundleID uuid.UUID,
	createdAt time.Time,
	source SourceDescriptor,
	counts ObjectCounts,
) Manifest {
	return Manifest{
		Contract:      ContractName,
		SchemaVersion: CurrentSchemaVersion,
		BundleID:      bundleID,
		CreatedAt:     createdAt.UTC(),
		Archive: ArchiveDescriptor{
			Format:    ArchiveFormatZIP64,
			Layout:    ArchiveLayoutV1,
			MediaType: BundleMediaType,
		},
		Source: source,
		Scope: ScopeDescriptor{
			Path:     "/",
			Complete: true,
		},
		Restore: RestorePolicy{
			Modes:             []string{RestoreModeFresh, RestoreModeMergeConservative},
			PreserveObjectIDs: true,
			PathRewrite:       false,
		},
		Indexes: IndexCatalog{
			Folders:        IndexDescriptor{Path: FoldersIndexPath, Count: counts.Folders},
			Files:          IndexDescriptor{Path: FilesIndexPath, Count: counts.Files},
			Memories:       IndexDescriptor{Path: MemoriesIndexPath, Count: counts.Memories},
			MemoryEvents:   IndexDescriptor{Path: MemoryEventsIndexPath, Count: counts.MemoryEvents},
			Tasks:          IndexDescriptor{Path: TasksIndexPath, Count: counts.Tasks},
			Checkpoints:    IndexDescriptor{Path: CheckpointsIndexPath, Count: counts.Checkpoints},
			CheckpointRefs: IndexDescriptor{Path: CheckpointRefsIndexPath, Count: counts.CheckpointRefs},
		},
		Payloads: PayloadCatalog{
			PathPrefix: CheckpointPayloadPrefix,
			Count:      counts.CheckpointPayloads,
		},
		Blobs: BlobCatalog{
			PathPrefix: ContentAddressedBlobRoot,
			Count:      counts.Blobs,
			TotalSize:  counts.BlobBytes,
		},
		Checksums: ChecksumCatalog{
			Path:      ChecksumsPath,
			Algorithm: "sha256",
		},
		Exclusions: ExclusionsV2(),
	}
}

type FolderRecord struct {
	ID        uuid.UUID  `json:"id"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type FileRecord struct {
	ID             uuid.UUID              `json:"id"`
	FolderID       *uuid.UUID             `json:"folder_id,omitempty"`
	Name           string                 `json:"name"`
	Path           string                 `json:"path"`
	Size           int64                  `json:"size"`
	SHA256         string                 `json:"sha256"`
	MIME           string                 `json:"mime"`
	BlobPath       string                 `json:"blob_path"`
	Summary        *string                `json:"summary,omitempty"`
	Caption        *string                `json:"caption,omitempty"`
	Tags           []string               `json:"tags"`
	UserTags       []string               `json:"user_tags"`
	TimelineAt     *time.Time             `json:"timeline_at,omitempty"`
	Geo            *FileGeoRecord         `json:"geo,omitempty"`
	SourceMetadata json.RawMessage        `json:"source_metadata"`
	Annotations    []FileAnnotationRecord `json:"annotations"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

// FileGeoRecord is the portable API-facing latitude/longitude projection.
// PostgreSQL's point(lon, lat) storage order is intentionally not exported.
type FileGeoRecord struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

func (record *FileGeoRecord) UnmarshalJSON(data []byte) error {
	type wire FileGeoRecord
	var decoded wire
	if err := decodeRequiredObject(
		data,
		&decoded,
		"file geo",
		"lat",
		"lon",
	); err != nil {
		return err
	}
	*record = FileGeoRecord(decoded)
	return nil
}

// FileAnnotationRecord transports bounded suggestion provenance and human
// review state. Target-local actor user IDs are deliberately excluded.
type FileAnnotationRecord struct {
	ID              uuid.UUID  `json:"id"`
	StableKey       string     `json:"stable_key"`
	Kind            string     `json:"kind"`
	ValueText       string     `json:"value_text"`
	Confidence      float32    `json:"confidence"`
	Source          string     `json:"source"`
	Provider        string     `json:"provider"`
	Processor       string     `json:"processor"`
	AnalysisVersion string     `json:"analysis_version"`
	Status          string     `json:"status"`
	StateVersion    int64      `json:"state_version"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (record *FileAnnotationRecord) UnmarshalJSON(data []byte) error {
	type wire FileAnnotationRecord
	var decoded wire
	if err := decodeRequiredObject(
		data,
		&decoded,
		"file annotation",
		"id",
		"stable_key",
		"kind",
		"value_text",
		"confidence",
		"source",
		"provider",
		"processor",
		"analysis_version",
		"status",
		"state_version",
		"created_at",
		"updated_at",
	); err != nil {
		return err
	}
	*record = FileAnnotationRecord(decoded)
	return nil
}

func decodeRequiredObject(
	data []byte,
	target any,
	label string,
	required ...string,
) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return fmt.Errorf("%s must be an object", label)
	}
	for _, field := range required {
		value, exists := fields[field]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%s.%s is required and must not be null", label, field)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%s must contain exactly one JSON object", label)
		}
		return err
	}
	return nil
}

type MemoryRecord struct {
	ID                   uuid.UUID       `json:"id"`
	Kind                 string          `json:"kind"`
	Content              string          `json:"content"`
	Attributes           json.RawMessage `json:"attributes"`
	Path                 string          `json:"path"`
	EventAt              *time.Time      `json:"event_at,omitempty"`
	SourceType           string          `json:"source_type"`
	SourceRef            string          `json:"source_ref,omitempty"`
	SourceFileID         *uuid.UUID      `json:"source_file_id,omitempty"`
	SourceFileSHA256     string          `json:"source_file_sha256,omitempty"`
	SourceLocator        json.RawMessage `json:"source_locator"`
	ProducerAgent        string          `json:"producer_agent,omitempty"`
	ProducerSession      string          `json:"producer_session,omitempty"`
	ProducerTask         string          `json:"producer_task,omitempty"`
	IdempotencyKeySHA256 string          `json:"idempotency_key_sha256"`
	OriginRequestSHA256  string          `json:"origin_request_sha256"`
	ContentSHA256        string          `json:"content_sha256"`
	LifecycleStatus      string          `json:"lifecycle_status"`
	StateVersion         int64           `json:"state_version"`
	PinnedAt             *time.Time      `json:"pinned_at,omitempty"`
	UsefulCount          int             `json:"useful_count"`
	NotUsefulCount       int             `json:"not_useful_count"`
	FeedbackAt           *time.Time      `json:"feedback_at,omitempty"`
	ForgottenAt          *time.Time      `json:"forgotten_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

// MemoryEventRecord is the portable, non-secret projection of one append-only
// memory control-plane mutation. Actor user/token IDs are intentionally absent.
type MemoryEventRecord struct {
	ID                   uuid.UUID `json:"id"`
	MemoryID             uuid.UUID `json:"memory_id"`
	Action               string    `json:"action"`
	IdempotencyKeySHA256 string    `json:"idempotency_key_sha256"`
	OriginRequestSHA256  string    `json:"origin_request_sha256"`
	ExpectedVersion      int64     `json:"expected_version"`
	ResultingVersion     int64     `json:"resulting_version"`
	Reason               string    `json:"reason,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

type TaskRecord struct {
	ID               uuid.UUID  `json:"id"`
	TaskKey          string     `json:"task_key"`
	ScopePath        string     `json:"scope_path"`
	HeadCheckpointID *uuid.UUID `json:"head_checkpoint_id,omitempty"`
	HeadSequence     int64      `json:"head_sequence"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type CheckpointRecord struct {
	ID                  uuid.UUID  `json:"id"`
	TaskID              uuid.UUID  `json:"task_id"`
	Sequence            int64      `json:"sequence"`
	CheckpointKind      string     `json:"checkpoint_kind"`
	Contract            string     `json:"contract"`
	SchemaVersion       int        `json:"schema_version"`
	BaseCheckpointID    *uuid.UUID `json:"base_checkpoint_id,omitempty"`
	ScopePath           string     `json:"scope_path"`
	PayloadPath         string     `json:"payload_path"`
	PayloadSHA256       string     `json:"payload_sha256"`
	OriginRequestSHA256 string     `json:"origin_request_sha256"`
	IdempotencyKey      string     `json:"idempotency_key"`
	ProducerAgent       string     `json:"producer_agent"`
	ProducerSession     string     `json:"producer_session,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

type CheckpointRefRecord struct {
	CheckpointID   uuid.UUID       `json:"checkpoint_id"`
	Ordinal        int             `json:"ordinal"`
	Relation       string          `json:"relation"`
	URI            string          `json:"uri"`
	ExpectedSHA256 string          `json:"expected_sha256,omitempty"`
	Required       bool            `json:"required"`
	Metadata       json.RawMessage `json:"metadata"`
}

// BlobInfo is the validated portable description of one content-addressed
// archive object.
type BlobInfo struct {
	SHA256 string
	Path   string
	Size   int64
}

// BundleData is the fully decoded, storage-independent v1 object graph.
type BundleData struct {
	Manifest           Manifest
	Folders            []FolderRecord
	Files              []FileRecord
	Memories           []MemoryRecord
	MemoryEvents       []MemoryEventRecord
	Tasks              []TaskRecord
	Checkpoints        []CheckpointRecord
	CheckpointRefs     []CheckpointRefRecord
	CheckpointPayloads map[uuid.UUID][]byte
	Blobs              []BlobInfo
}

// BlobSource supplies one blob to Writer. Size and SHA256 are verified while
// streaming; callers should write to a temporary destination because a source
// mismatch necessarily leaves a partial ZIP stream.
type BlobSource struct {
	BlobInfo
	Open func() (io.ReadCloser, error)
}

type WriteInput struct {
	BundleData
	BlobSources []BlobSource
}
