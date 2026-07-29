package workspacebundle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type WriterOptions struct {
	Limits                     Limits
	CheckpointPayloadValidator CheckpointPayloadValidator
}

type ReaderOptions struct {
	Limits                     Limits
	CheckpointPayloadValidator CheckpointPayloadValidator
}

type checksumRecord struct {
	SHA256 string
	Size   uint64
	Path   string
}

type namedBytes struct {
	path string
	data []byte
}

// Write validates input, then emits the deterministic fixed-layout current
// schema ZIP stream. Legacy schemas are read-only: serializing them with the
// current structs would add fields that historical strict readers reject.
func Write(w io.Writer, input WriteInput, options WriterOptions) error {
	if w == nil {
		return fmt.Errorf("%w: writer is nil", ErrInvalidBundle)
	}
	if input.Manifest.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf(
			"%w: writer requires schema_version %d",
			ErrUnsupportedVersion,
			CurrentSchemaVersion,
		)
	}
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return err
	}
	if err := matchBlobSources(input.BundleData.Blobs, input.BlobSources); err != nil {
		return err
	}
	if err := Validate(input.BundleData, ValidationOptions{
		Limits:                     limits,
		CheckpointPayloadValidator: options.CheckpointPayloadValidator,
	}); err != nil {
		return err
	}
	metadata, err := buildMetadataEntries(input.BundleData)
	if err != nil {
		return err
	}
	entryCount := len(metadata) + len(input.BlobSources) + 1 // checksums.sha256
	if entryCount > limits.MaxEntries {
		return fmt.Errorf(
			"%w: archive requires %d entries, limit is %d",
			ErrLimitExceeded,
			entryCount,
			limits.MaxEntries,
		)
	}

	zipWriter := zip.NewWriter(w)
	var (
		checksums    []checksumRecord
		totalSize    uint64
		metadataSize uint64
	)
	for _, entry := range metadata {
		if err := accountEntry(
			entry.path,
			uint64(len(entry.data)),
			true,
			limits,
			&totalSize,
			&metadataSize,
		); err != nil {
			_ = zipWriter.Close()
			return err
		}
		record, err := writeBytesEntry(zipWriter, entry.path, entry.data, zip.Deflate)
		if err != nil {
			_ = zipWriter.Close()
			return err
		}
		checksums = append(checksums, record)
	}

	sources := append([]BlobSource(nil), input.BlobSources...)
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].SHA256 < sources[j].SHA256
	})
	for _, source := range sources {
		if source.Size < 0 {
			_ = zipWriter.Close()
			return fmt.Errorf("%w: blob %s size is negative", ErrInvalidBundle, source.SHA256)
		}
		if err := accountEntry(
			source.Path,
			uint64(source.Size),
			false,
			limits,
			&totalSize,
			&metadataSize,
		); err != nil {
			_ = zipWriter.Close()
			return err
		}
		record, err := writeBlobEntry(zipWriter, source)
		if err != nil {
			_ = zipWriter.Close()
			return err
		}
		checksums = append(checksums, record)
	}

	checksumBytes := marshalChecksums(checksums)
	if err := accountEntry(
		ChecksumsPath,
		uint64(len(checksumBytes)),
		true,
		limits,
		&totalSize,
		&metadataSize,
	); err != nil {
		_ = zipWriter.Close()
		return err
	}
	if _, err := writeBytesEntry(zipWriter, ChecksumsPath, checksumBytes, zip.Deflate); err != nil {
		_ = zipWriter.Close()
		return err
	}
	if err := zipWriter.Close(); err != nil {
		return fmt.Errorf("close workspace bundle ZIP: %w", err)
	}
	return nil
}

func matchBlobSources(expected []BlobInfo, sources []BlobSource) error {
	if len(expected) != len(sources) {
		return fmt.Errorf(
			"%w: blob source count %d does not match blob catalog count %d",
			ErrInvalidBundle,
			len(sources),
			len(expected),
		)
	}
	expectedByDigest := make(map[string]BlobInfo, len(expected))
	for _, info := range expected {
		expectedByDigest[info.SHA256] = info
	}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if source.Open == nil {
			return fmt.Errorf("%w: blob %s has no Open function", ErrInvalidBundle, source.SHA256)
		}
		if _, duplicate := seen[source.SHA256]; duplicate {
			return fmt.Errorf("%w: duplicate blob source %s", ErrInvalidBundle, source.SHA256)
		}
		info, exists := expectedByDigest[source.SHA256]
		if !exists || info != source.BlobInfo {
			return fmt.Errorf(
				"%w: blob source %s does not match bundle catalog",
				ErrInvalidBundle,
				source.SHA256,
			)
		}
		seen[source.SHA256] = struct{}{}
	}
	return nil
}

func buildMetadataEntries(data BundleData) ([]namedBytes, error) {
	manifest, err := json.Marshal(data.Manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	folders := append([]FolderRecord(nil), data.Folders...)
	sort.Slice(folders, func(i, j int) bool {
		if folders[i].Path == folders[j].Path {
			return folders[i].ID.String() < folders[j].ID.String()
		}
		return folders[i].Path < folders[j].Path
	})
	files := append([]FileRecord(nil), data.Files...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].ID.String() < files[j].ID.String()
	})
	memories := append([]MemoryRecord(nil), data.Memories...)
	sort.Slice(memories, func(i, j int) bool {
		return memories[i].ID.String() < memories[j].ID.String()
	})
	memoryEvents := append([]MemoryEventRecord(nil), data.MemoryEvents...)
	sort.Slice(memoryEvents, func(i, j int) bool {
		if memoryEvents[i].MemoryID == memoryEvents[j].MemoryID {
			if memoryEvents[i].ExpectedVersion == memoryEvents[j].ExpectedVersion {
				return memoryEvents[i].ID.String() < memoryEvents[j].ID.String()
			}
			return memoryEvents[i].ExpectedVersion < memoryEvents[j].ExpectedVersion
		}
		return memoryEvents[i].MemoryID.String() < memoryEvents[j].MemoryID.String()
	})
	tasks := append([]TaskRecord(nil), data.Tasks...)
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].TaskKey == tasks[j].TaskKey {
			return tasks[i].ID.String() < tasks[j].ID.String()
		}
		return tasks[i].TaskKey < tasks[j].TaskKey
	})
	checkpoints := append([]CheckpointRecord(nil), data.Checkpoints...)
	sort.Slice(checkpoints, func(i, j int) bool {
		if checkpoints[i].TaskID == checkpoints[j].TaskID {
			if checkpoints[i].Sequence == checkpoints[j].Sequence {
				return checkpoints[i].ID.String() < checkpoints[j].ID.String()
			}
			return checkpoints[i].Sequence < checkpoints[j].Sequence
		}
		return checkpoints[i].TaskID.String() < checkpoints[j].TaskID.String()
	})
	refs := append([]CheckpointRefRecord(nil), data.CheckpointRefs...)
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].CheckpointID == refs[j].CheckpointID {
			return refs[i].Ordinal < refs[j].Ordinal
		}
		return refs[i].CheckpointID.String() < refs[j].CheckpointID.String()
	})

	foldersRaw, err := marshalNDJSON(folders)
	if err != nil {
		return nil, err
	}
	filesRaw, err := marshalNDJSON(files)
	if err != nil {
		return nil, err
	}
	memoriesRaw, err := marshalNDJSON(memories)
	if err != nil {
		return nil, err
	}
	memoryEventsRaw, err := marshalNDJSON(memoryEvents)
	if err != nil {
		return nil, err
	}
	tasksRaw, err := marshalNDJSON(tasks)
	if err != nil {
		return nil, err
	}
	checkpointsRaw, err := marshalNDJSON(checkpoints)
	if err != nil {
		return nil, err
	}
	refsRaw, err := marshalNDJSON(refs)
	if err != nil {
		return nil, err
	}

	entries := []namedBytes{
		{path: ManifestPath, data: manifest},
		{path: FoldersIndexPath, data: foldersRaw},
		{path: FilesIndexPath, data: filesRaw},
		{path: MemoriesIndexPath, data: memoriesRaw},
		{path: MemoryEventsIndexPath, data: memoryEventsRaw},
		{path: TasksIndexPath, data: tasksRaw},
		{path: CheckpointsIndexPath, data: checkpointsRaw},
		{path: CheckpointRefsIndexPath, data: refsRaw},
	}
	payloadIDs := make([]uuid.UUID, 0, len(data.CheckpointPayloads))
	for id := range data.CheckpointPayloads {
		payloadIDs = append(payloadIDs, id)
	}
	sort.Slice(payloadIDs, func(i, j int) bool {
		return payloadIDs[i].String() < payloadIDs[j].String()
	})
	for _, id := range payloadIDs {
		entries = append(entries, namedBytes{
			path: checkpointPayloadPath(id),
			data: data.CheckpointPayloads[id],
		})
	}
	return entries, nil
}

var zipEpoch = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

func deterministicHeader(name string, method uint16) *zip.FileHeader {
	header := &zip.FileHeader{
		Name:     name,
		Method:   method,
		Modified: zipEpoch,
	}
	header.SetMode(0o600)
	return header
}

func writeBytesEntry(
	writer *zip.Writer,
	name string,
	data []byte,
	method uint16,
) (checksumRecord, error) {
	entry, err := writer.CreateHeader(deterministicHeader(name, method))
	if err != nil {
		return checksumRecord{}, fmt.Errorf("create ZIP entry %s: %w", name, err)
	}
	if _, err := entry.Write(data); err != nil {
		return checksumRecord{}, fmt.Errorf("write ZIP entry %s: %w", name, err)
	}
	return checksumRecord{
		SHA256: sha256Hex(data),
		Size:   uint64(len(data)),
		Path:   name,
	}, nil
}

func writeBlobEntry(writer *zip.Writer, source BlobSource) (checksumRecord, error) {
	reader, err := source.Open()
	if err != nil {
		return checksumRecord{}, fmt.Errorf("open blob %s: %w", source.SHA256, err)
	}
	header := deterministicHeader(source.Path, zip.Store)
	header.UncompressedSize64 = uint64(source.Size)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		_ = reader.Close()
		return checksumRecord{}, fmt.Errorf("create blob ZIP entry %s: %w", source.Path, err)
	}
	hasher := sha256.New()
	limited := &io.LimitedReader{R: reader, N: source.Size + 1}
	written, copyErr := io.Copy(io.MultiWriter(entry, hasher), limited)
	closeErr := reader.Close()
	if copyErr != nil {
		return checksumRecord{}, fmt.Errorf("stream blob %s: %w", source.SHA256, copyErr)
	}
	if closeErr != nil {
		return checksumRecord{}, fmt.Errorf("close blob %s: %w", source.SHA256, closeErr)
	}
	if written != source.Size {
		return checksumRecord{}, fmt.Errorf(
			"%w: blob %s declared size %d, read %d",
			ErrIntegrity,
			source.SHA256,
			source.Size,
			written,
		)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if digest != source.SHA256 {
		return checksumRecord{}, fmt.Errorf(
			"%w: blob %s content hashes to %s",
			ErrIntegrity,
			source.SHA256,
			digest,
		)
	}
	return checksumRecord{
		SHA256: digest,
		Size:   uint64(written),
		Path:   source.Path,
	}, nil
}

func accountEntry(
	name string,
	size uint64,
	metadata bool,
	limits Limits,
	total *uint64,
	metadataTotal *uint64,
) error {
	if size > limits.MaxEntrySize {
		return fmt.Errorf(
			"%w: entry %s size %d exceeds %d",
			ErrLimitExceeded,
			name,
			size,
			limits.MaxEntrySize,
		)
	}
	if metadata && size > limits.MaxMetadataEntrySize {
		return fmt.Errorf(
			"%w: metadata entry %s size %d exceeds %d",
			ErrLimitExceeded,
			name,
			size,
			limits.MaxMetadataEntrySize,
		)
	}
	if size > limits.MaxTotalSize-*total {
		return fmt.Errorf("%w: total uncompressed size exceeds limit", ErrLimitExceeded)
	}
	*total += size
	if metadata {
		if size > limits.MaxTotalMetadataSize-*metadataTotal {
			return fmt.Errorf("%w: total metadata size exceeds limit", ErrLimitExceeded)
		}
		*metadataTotal += size
	}
	return nil
}

func marshalChecksums(records []checksumRecord) []byte {
	records = append([]checksumRecord(nil), records...)
	sort.Slice(records, func(i, j int) bool {
		return records[i].Path < records[j].Path
	})
	var out strings.Builder
	for _, record := range records {
		out.WriteString(record.SHA256)
		out.WriteByte('\t')
		out.WriteString(strconv.FormatUint(record.Size, 10))
		out.WriteByte('\t')
		out.WriteString(record.Path)
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

// Archive is a fully validated bundle backed by its original io.ReaderAt.
// Metadata and checkpoint payloads are decoded in memory; blob bytes remain in
// the ZIP and can be opened individually without extraction. The caller must
// keep the underlying ReaderAt usable until all blob readers are closed.
type Archive struct {
	BundleData
	blobFiles map[string]*zip.File
}

// Open validates a .membundle from an io.ReaderAt and returns its portable
// object graph plus streaming access to validated blobs.
func Open(
	reader io.ReaderAt,
	size int64,
	options ReaderOptions,
) (*Archive, error) {
	if reader == nil || size <= 0 {
		return nil, fmt.Errorf("%w: reader and positive size are required", ErrInvalidBundle)
	}
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return nil, err
	}
	zipReader, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, fmt.Errorf("%w: open ZIP: %v", ErrInvalidBundle, err)
	}
	if len(zipReader.File) > limits.MaxEntries {
		return nil, fmt.Errorf(
			"%w: archive has %d entries, limit is %d",
			ErrLimitExceeded,
			len(zipReader.File),
			limits.MaxEntries,
		)
	}

	entryFiles := make(map[string]*zip.File, len(zipReader.File))
	entryClasses := make(map[string]archiveEntry, len(zipReader.File))
	required := map[string]bool{
		ManifestPath:            false,
		ChecksumsPath:           false,
		FoldersIndexPath:        false,
		FilesIndexPath:          false,
		MemoriesIndexPath:       false,
		MemoryEventsIndexPath:   false,
		TasksIndexPath:          false,
		CheckpointsIndexPath:    false,
		CheckpointRefsIndexPath: false,
	}
	var (
		totalSize    uint64
		metadataSize uint64
	)
	for _, file := range zipReader.File {
		classification, err := inspectArchiveEntry(file, limits)
		if err != nil {
			return nil, err
		}
		if _, duplicate := entryFiles[file.Name]; duplicate {
			return nil, fmt.Errorf(
				"%w: duplicate ZIP entry %q",
				ErrUnsafeArchive,
				file.Name,
			)
		}
		if err := accountEntry(
			file.Name,
			file.UncompressedSize64,
			classification.kind != entryBlob,
			limits,
			&totalSize,
			&metadataSize,
		); err != nil {
			return nil, err
		}
		entryFiles[file.Name] = file
		entryClasses[file.Name] = classification
		if _, ok := required[file.Name]; ok {
			required[file.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			return nil, fmt.Errorf(
				"%w: required entry %q is missing",
				ErrInvalidBundle,
				name,
			)
		}
	}

	checksumBytes, err := readZIPEntry(entryFiles[ChecksumsPath], limits.MaxMetadataEntrySize)
	if err != nil {
		return nil, err
	}
	checksums, err := parseChecksums(checksumBytes)
	if err != nil {
		return nil, err
	}
	if err := validateChecksumCoverage(entryFiles, checksums); err != nil {
		return nil, err
	}

	metadataBytes := make(map[string][]byte)
	for name, file := range entryFiles {
		if name == ChecksumsPath {
			continue
		}
		expected := checksums[name]
		classification := entryClasses[name]
		raw, digest, actualSize, err := hashZIPEntry(
			file,
			expected,
			classification.kind != entryBlob,
			limits,
		)
		if err != nil {
			return nil, err
		}
		if classification.kind == entryBlob && digest != classification.digest {
			return nil, fmt.Errorf(
				"%w: blob entry %s content hashes to %s",
				ErrIntegrity,
				name,
				digest,
			)
		}
		if classification.kind != entryBlob {
			metadataBytes[name] = raw
		}
		if actualSize != file.UncompressedSize64 {
			return nil, fmt.Errorf(
				"%w: entry %s header size mismatch",
				ErrIntegrity,
				name,
			)
		}
	}

	if err := validateJSONDepth(
		metadataBytes[ManifestPath],
		limits.MaxJSONDepth,
		ManifestPath,
	); err != nil {
		return nil, err
	}
	manifest, err := decodeStrictOne[Manifest](metadataBytes[ManifestPath], ManifestPath)
	if err != nil {
		return nil, err
	}
	folders, err := decodeNDJSON[FolderRecord](
		metadataBytes[FoldersIndexPath],
		limits,
		FoldersIndexPath,
	)
	if err != nil {
		return nil, err
	}
	files, err := decodeNDJSON[FileRecord](
		metadataBytes[FilesIndexPath],
		limits,
		FilesIndexPath,
	)
	if err != nil {
		return nil, err
	}
	memories, err := decodeNDJSON[MemoryRecord](
		metadataBytes[MemoriesIndexPath],
		limits,
		MemoriesIndexPath,
	)
	if err != nil {
		return nil, err
	}
	memoryEvents, err := decodeNDJSON[MemoryEventRecord](
		metadataBytes[MemoryEventsIndexPath],
		limits,
		MemoryEventsIndexPath,
	)
	if err != nil {
		return nil, err
	}
	tasks, err := decodeNDJSON[TaskRecord](
		metadataBytes[TasksIndexPath],
		limits,
		TasksIndexPath,
	)
	if err != nil {
		return nil, err
	}
	checkpoints, err := decodeNDJSON[CheckpointRecord](
		metadataBytes[CheckpointsIndexPath],
		limits,
		CheckpointsIndexPath,
	)
	if err != nil {
		return nil, err
	}
	refs, err := decodeNDJSON[CheckpointRefRecord](
		metadataBytes[CheckpointRefsIndexPath],
		limits,
		CheckpointRefsIndexPath,
	)
	if err != nil {
		return nil, err
	}

	payloads := make(map[uuid.UUID][]byte)
	blobInfos := make([]BlobInfo, 0)
	blobFiles := make(map[string]*zip.File)
	for name, classification := range entryClasses {
		switch classification.kind {
		case entryCheckpointPayload:
			payloads[classification.id] = metadataBytes[name]
		case entryBlob:
			file := entryFiles[name]
			blobInfos = append(blobInfos, BlobInfo{
				SHA256: classification.digest,
				Path:   name,
				Size:   int64(file.UncompressedSize64),
			})
			blobFiles[classification.digest] = file
		}
	}
	sort.Slice(blobInfos, func(i, j int) bool {
		return blobInfos[i].SHA256 < blobInfos[j].SHA256
	})
	data := BundleData{
		Manifest:           manifest,
		Folders:            folders,
		Files:              files,
		Memories:           memories,
		MemoryEvents:       memoryEvents,
		Tasks:              tasks,
		Checkpoints:        checkpoints,
		CheckpointRefs:     refs,
		CheckpointPayloads: payloads,
		Blobs:              blobInfos,
	}
	if err := Validate(data, ValidationOptions{
		Limits:                     limits,
		CheckpointPayloadValidator: options.CheckpointPayloadValidator,
	}); err != nil {
		return nil, err
	}
	return &Archive{
		BundleData: data,
		blobFiles:  blobFiles,
	}, nil
}

type archiveEntryKind int

const (
	entryManifest archiveEntryKind = iota
	entryChecksums
	entryIndex
	entryCheckpointPayload
	entryBlob
)

type archiveEntry struct {
	kind   archiveEntryKind
	id     uuid.UUID
	digest string
}

var windowsVolumePattern = regexp.MustCompile(`^[A-Za-z]:`)

func inspectArchiveEntry(file *zip.File, limits Limits) (archiveEntry, error) {
	name := file.Name
	if !utf8.ValidString(name) ||
		name == "" ||
		strings.IndexByte(name, 0) >= 0 ||
		strings.Contains(name, "\\") ||
		strings.Contains(name, ":") ||
		path.IsAbs(name) ||
		windowsVolumePattern.MatchString(name) ||
		path.Clean(name) != name {
		return archiveEntry{}, fmt.Errorf(
			"%w: invalid ZIP entry path %q",
			ErrUnsafeArchive,
			name,
		)
	}
	parts := strings.Split(name, "/")
	if len(parts) > limits.MaxPathDepth {
		return archiveEntry{}, fmt.Errorf(
			"%w: ZIP entry path %q exceeds depth %d",
			ErrLimitExceeded,
			name,
			limits.MaxPathDepth,
		)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return archiveEntry{}, fmt.Errorf(
				"%w: invalid ZIP entry path %q",
				ErrUnsafeArchive,
				name,
			)
		}
	}
	mode := file.Mode()
	if file.FileInfo().IsDir() ||
		mode&os.ModeSymlink != 0 ||
		mode.Type() != 0 {
		return archiveEntry{}, fmt.Errorf(
			"%w: ZIP entry %q is not a regular file",
			ErrUnsafeArchive,
			name,
		)
	}
	if file.Method != zip.Store && file.Method != zip.Deflate {
		return archiveEntry{}, fmt.Errorf(
			"%w: ZIP entry %q uses unsupported compression method %d",
			ErrUnsafeArchive,
			name,
			file.Method,
		)
	}
	if file.UncompressedSize64 > limits.MaxEntrySize {
		return archiveEntry{}, fmt.Errorf(
			"%w: entry %q exceeds maximum size",
			ErrLimitExceeded,
			name,
		)
	}
	if exceedsCompressionRatio(
		file.UncompressedSize64,
		file.CompressedSize64,
		limits.MaxCompressionRatio,
	) {
		return archiveEntry{}, fmt.Errorf(
			"%w: entry %q exceeds compression ratio %d",
			ErrLimitExceeded,
			name,
			limits.MaxCompressionRatio,
		)
	}

	switch name {
	case ManifestPath:
		return archiveEntry{kind: entryManifest}, nil
	case ChecksumsPath:
		return archiveEntry{kind: entryChecksums}, nil
	case FoldersIndexPath,
		FilesIndexPath,
		MemoriesIndexPath,
		MemoryEventsIndexPath,
		TasksIndexPath,
		CheckpointsIndexPath,
		CheckpointRefsIndexPath:
		return archiveEntry{kind: entryIndex}, nil
	}
	if strings.HasPrefix(name, CheckpointPayloadPrefix) {
		value := strings.TrimPrefix(name, CheckpointPayloadPrefix)
		if !strings.HasSuffix(value, ".json") {
			return archiveEntry{}, unknownEntry(name)
		}
		value = strings.TrimSuffix(value, ".json")
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			return archiveEntry{}, fmt.Errorf(
				"%w: invalid checkpoint payload path %q",
				ErrUnsafeArchive,
				name,
			)
		}
		return archiveEntry{kind: entryCheckpointPayload, id: id}, nil
	}
	if strings.HasPrefix(name, ContentAddressedBlobRoot) {
		value := strings.TrimPrefix(name, ContentAddressedBlobRoot)
		blobParts := strings.Split(value, "/")
		if len(blobParts) != 2 ||
			len(blobParts[0]) != 2 ||
			!sha256Pattern.MatchString(blobParts[1]) ||
			blobParts[0] != blobParts[1][:2] {
			return archiveEntry{}, fmt.Errorf(
				"%w: invalid content-addressed blob path %q",
				ErrUnsafeArchive,
				name,
			)
		}
		return archiveEntry{kind: entryBlob, digest: blobParts[1]}, nil
	}
	return archiveEntry{}, unknownEntry(name)
}

func unknownEntry(name string) error {
	return fmt.Errorf(
		"%w: unknown v1 entry %q",
		ErrUnsafeArchive,
		name,
	)
}

func exceedsCompressionRatio(uncompressed, compressed, limit uint64) bool {
	if uncompressed == 0 {
		return false
	}
	if compressed == 0 {
		return true
	}
	quotient := uncompressed / compressed
	remainder := uncompressed % compressed
	return quotient > limit || (quotient == limit && remainder != 0)
}

func readZIPEntry(file *zip.File, limit uint64) ([]byte, error) {
	if file == nil {
		return nil, fmt.Errorf("%w: ZIP entry is missing", ErrInvalidBundle)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open ZIP entry %s: %v", ErrInvalidBundle, file.Name, err)
	}
	limited := &io.LimitedReader{R: reader, N: int64(limit) + 1}
	raw, readErr := io.ReadAll(limited)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("%w: read ZIP entry %s: %v", ErrIntegrity, file.Name, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("%w: close ZIP entry %s: %v", ErrIntegrity, file.Name, closeErr)
	}
	if uint64(len(raw)) > limit {
		return nil, fmt.Errorf(
			"%w: ZIP entry %s exceeds %d bytes",
			ErrLimitExceeded,
			file.Name,
			limit,
		)
	}
	if uint64(len(raw)) != file.UncompressedSize64 {
		return nil, fmt.Errorf(
			"%w: ZIP entry %s size mismatch",
			ErrIntegrity,
			file.Name,
		)
	}
	return raw, nil
}

func parseChecksums(raw []byte) (map[string]checksumRecord, error) {
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, fmt.Errorf(
			"%w: %s must be non-empty and newline terminated",
			ErrInvalidBundle,
			ChecksumsPath,
		)
	}
	lines := strings.Split(string(raw[:len(raw)-1]), "\n")
	out := make(map[string]checksumRecord, len(lines))
	previousPath := ""
	for index, line := range lines {
		if line == "" || strings.ContainsRune(line, '\r') {
			return nil, fmt.Errorf(
				"%w: %s line %d is malformed",
				ErrInvalidBundle,
				ChecksumsPath,
				index+1,
			)
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf(
				"%w: %s line %d must contain digest, size, and path",
				ErrInvalidBundle,
				ChecksumsPath,
				index+1,
			)
		}
		if err := validateSHA256("checksum digest", fields[0], false); err != nil {
			return nil, err
		}
		size, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"%w: %s line %d has invalid size",
				ErrInvalidBundle,
				ChecksumsPath,
				index+1,
			)
		}
		if fields[2] == ChecksumsPath {
			return nil, fmt.Errorf(
				"%w: %s must not checksum itself",
				ErrInvalidBundle,
				ChecksumsPath,
			)
		}
		if index > 0 && fields[2] <= previousPath {
			return nil, fmt.Errorf(
				"%w: %s paths must be strictly sorted",
				ErrInvalidBundle,
				ChecksumsPath,
			)
		}
		if _, duplicate := out[fields[2]]; duplicate {
			return nil, fmt.Errorf(
				"%w: duplicate checksum path %q",
				ErrInvalidBundle,
				fields[2],
			)
		}
		out[fields[2]] = checksumRecord{
			SHA256: fields[0],
			Size:   size,
			Path:   fields[2],
		}
		previousPath = fields[2]
	}
	return out, nil
}

func validateChecksumCoverage(
	entries map[string]*zip.File,
	checksums map[string]checksumRecord,
) error {
	if len(checksums) != len(entries)-1 {
		return fmt.Errorf(
			"%w: checksum entry count does not cover the archive",
			ErrIntegrity,
		)
	}
	for name := range entries {
		if name == ChecksumsPath {
			continue
		}
		if _, exists := checksums[name]; !exists {
			return fmt.Errorf(
				"%w: ZIP entry %s has no checksum",
				ErrIntegrity,
				name,
			)
		}
	}
	for name := range checksums {
		if _, exists := entries[name]; !exists {
			return fmt.Errorf(
				"%w: checksum references missing entry %s",
				ErrIntegrity,
				name,
			)
		}
	}
	return nil
}

func hashZIPEntry(
	file *zip.File,
	expected checksumRecord,
	retain bool,
	limits Limits,
) ([]byte, string, uint64, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, "", 0, fmt.Errorf("%w: open ZIP entry %s: %v", ErrIntegrity, file.Name, err)
	}
	hasher := sha256.New()
	var destination io.Writer = hasher
	var buffer bytes.Buffer
	limit := limits.MaxEntrySize
	if retain {
		limit = limits.MaxMetadataEntrySize
		destination = io.MultiWriter(hasher, &buffer)
	}
	limited := &io.LimitedReader{R: reader, N: int64(limit) + 1}
	read, copyErr := io.Copy(destination, limited)
	closeErr := reader.Close()
	if copyErr != nil {
		return nil, "", 0, fmt.Errorf(
			"%w: read ZIP entry %s: %v",
			ErrIntegrity,
			file.Name,
			copyErr,
		)
	}
	if closeErr != nil {
		return nil, "", 0, fmt.Errorf(
			"%w: close ZIP entry %s: %v",
			ErrIntegrity,
			file.Name,
			closeErr,
		)
	}
	if uint64(read) > limit {
		return nil, "", 0, fmt.Errorf(
			"%w: ZIP entry %s exceeds size limit",
			ErrLimitExceeded,
			file.Name,
		)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if digest != expected.SHA256 || uint64(read) != expected.Size {
		return nil, "", 0, fmt.Errorf(
			"%w: checksum or size mismatch for %s",
			ErrIntegrity,
			file.Name,
		)
	}
	return buffer.Bytes(), digest, uint64(read), nil
}

// OpenBlob opens one content-addressed blob without extracting it to disk.
// The returned reader verifies the size and SHA again at EOF.
func (archive *Archive) OpenBlob(digest string) (io.ReadCloser, error) {
	if err := validateSHA256("blob sha256", digest, false); err != nil {
		return nil, err
	}
	file, exists := archive.blobFiles[digest]
	if !exists {
		return nil, fmt.Errorf("%w: blob %s is not in the bundle", ErrDependency, digest)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open blob %s: %v", ErrIntegrity, digest, err)
	}
	return &verifiedBlobReader{
		reader:       reader,
		hasher:       sha256.New(),
		expectedSHA:  digest,
		expectedSize: int64(file.UncompressedSize64),
	}, nil
}

// CopyBlob guarantees complete re-verification while streaming to destination.
func (archive *Archive) CopyBlob(destination io.Writer, digest string) (int64, error) {
	reader, err := archive.OpenBlob(digest)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(destination, reader)
	closeErr := reader.Close()
	if copyErr != nil {
		return written, copyErr
	}
	if closeErr != nil {
		return written, closeErr
	}
	return written, nil
}

type verifiedBlobReader struct {
	reader       io.ReadCloser
	hasher       hash.Hash
	expectedSHA  string
	expectedSize int64
	read         int64
	pendingErr   error
	done         bool
}

func (reader *verifiedBlobReader) Read(buffer []byte) (int, error) {
	if reader.pendingErr != nil {
		err := reader.pendingErr
		reader.pendingErr = nil
		return 0, err
	}
	if reader.done {
		return 0, io.EOF
	}
	count, err := reader.reader.Read(buffer)
	if count > 0 {
		_, _ = reader.hasher.Write(buffer[:count])
		reader.read += int64(count)
	}
	if err == io.EOF {
		reader.done = true
		finalErr := reader.verify()
		if count > 0 {
			if finalErr != nil {
				reader.pendingErr = finalErr
			}
			return count, nil
		}
		if finalErr != nil {
			return 0, finalErr
		}
	}
	return count, err
}

func (reader *verifiedBlobReader) Close() error {
	return reader.reader.Close()
}

func (reader *verifiedBlobReader) verify() error {
	if reader.read != reader.expectedSize {
		return fmt.Errorf(
			"%w: blob %s size changed while reading",
			ErrIntegrity,
			reader.expectedSHA,
		)
	}
	actual := hex.EncodeToString(reader.hasher.Sum(nil))
	if actual != reader.expectedSHA {
		return fmt.Errorf(
			"%w: blob %s changed while reading",
			ErrIntegrity,
			reader.expectedSHA,
		)
	}
	return nil
}
