package workspacebundle

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestWriteOpenRoundTripIsDeterministicAndStreamsBlobs(t *testing.T) {
	fixture := validFixture(t)
	first := writeFixture(t, fixture)
	second := writeFixture(t, fixture)
	if !bytes.Equal(first, second) {
		t.Fatal("same bundle input produced different ZIP bytes")
	}

	archive, err := Open(bytes.NewReader(first), int64(len(first)), ReaderOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if archive.Manifest.Contract != ContractName {
		t.Fatalf("contract = %q", archive.Manifest.Contract)
	}
	if got, want := len(archive.Folders), 1; got != want {
		t.Fatalf("folders = %d, want %d", got, want)
	}
	if got, want := len(archive.Checkpoints), 2; got != want {
		t.Fatalf("checkpoints = %d, want %d", got, want)
	}
	if got, want := len(archive.MemoryEvents), 1; got != want {
		t.Fatalf("memory events = %d, want %d", got, want)
	}
	var blob bytes.Buffer
	written, err := archive.CopyBlob(&blob, fixture.Files[0].SHA256)
	if err != nil {
		t.Fatalf("CopyBlob: %v", err)
	}
	if written != fixture.Files[0].Size || blob.String() != "portable file bytes\n" {
		t.Fatalf("copied blob = %q (%d bytes)", blob.String(), written)
	}
}

func TestWriteOpenRoundTripDeduplicatesSharedFileContent(t *testing.T) {
	fixture := sharedContentFixture(t)
	raw := writeFixture(t, fixture)
	archive, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
	if err != nil {
		t.Fatalf("Open shared content bundle: %v", err)
	}
	if len(archive.Files) != 2 || len(archive.Blobs) != 1 {
		t.Fatalf("files=%d blobs=%d", len(archive.Files), len(archive.Blobs))
	}
	blobEntries := 0
	for _, entry := range readTestZIP(t, raw) {
		if strings.HasPrefix(entry.Header.Name, ContentAddressedBlobRoot) {
			blobEntries++
		}
	}
	if blobEntries != 1 {
		t.Fatalf("blob ZIP entries = %d, want 1", blobEntries)
	}
}

func TestWriteRejectsBlobContentMismatch(t *testing.T) {
	fixture := validFixture(t)
	fixture.BlobSources[0].Open = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("same declared length!!\n")), nil
	}
	var output bytes.Buffer
	err := Write(&output, fixture, WriterOptions{})
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Write error = %v, want ErrIntegrity", err)
	}
}

func TestOpenRejectsTamperedChecksummedEntry(t *testing.T) {
	raw := writeFixture(t, validFixture(t))
	entries := readTestZIP(t, raw)
	for index := range entries {
		if entries[index].Header.Name == FilesIndexPath {
			entries[index].Data = append(entries[index].Data, ' ')
		}
	}
	tampered := writeTestZIP(t, entries)
	_, err := Open(bytes.NewReader(tampered), int64(len(tampered)), ReaderOptions{})
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Open error = %v, want ErrIntegrity", err)
	}
}

func TestMemoryIndexNeverCarriesRawIdempotencyKey(t *testing.T) {
	raw := writeFixture(t, validFixture(t))
	entries := readTestZIP(t, raw)
	for index := range entries {
		if entries[index].Header.Name != MemoriesIndexPath {
			continue
		}
		if bytes.Contains(entries[index].Data, []byte(`"idempotency_key":`)) ||
			!bytes.Contains(entries[index].Data, []byte(`"idempotency_key_sha256":`)) {
			t.Fatalf("memory index exposed the wrong idempotency field: %s", entries[index].Data)
		}

		var record map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(entries[index].Data), &record); err != nil {
			t.Fatal(err)
		}
		delete(record, "idempotency_key_sha256")
		record["idempotency_key"] = "raw-secret-must-not-cross-boundary"
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		entries[index].Data = append(encoded, '\n')
	}
	recomputeTestChecksums(entries)
	unsafe := writeTestZIP(t, entries)
	_, err := Open(bytes.NewReader(unsafe), int64(len(unsafe)), ReaderOptions{})
	if !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("Open raw memory idempotency field error = %v, want ErrInvalidBundle", err)
	}
}

func TestOpenRejectsBlobWhoseContentDoesNotMatchAddress(t *testing.T) {
	raw := writeFixture(t, validFixture(t))
	entries := readTestZIP(t, raw)
	for index := range entries {
		if strings.HasPrefix(entries[index].Header.Name, ContentAddressedBlobRoot) {
			entries[index].Data[0] ^= 0xff
		}
	}
	recomputeTestChecksums(entries)
	tampered := writeTestZIP(t, entries)
	_, err := Open(bytes.NewReader(tampered), int64(len(tampered)), ReaderOptions{})
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Open error = %v, want ErrIntegrity", err)
	}
}

func TestOpenRejectsFutureSchemaEvenWithValidChecksums(t *testing.T) {
	raw := writeFixture(t, validFixture(t))
	entries := readTestZIP(t, raw)
	for index := range entries {
		if entries[index].Header.Name != ManifestPath {
			continue
		}
		var manifest map[string]any
		if err := json.Unmarshal(entries[index].Data, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest["schema_version"] = float64(CurrentSchemaVersion + 1)
		entries[index].Data, _ = json.Marshal(manifest)
	}
	recomputeTestChecksums(entries)
	future := writeTestZIP(t, entries)
	_, err := Open(bytes.NewReader(future), int64(len(future)), ReaderOptions{})
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Open error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestOpenAcceptsHistoricalV1WithoutEnrichmentFields(t *testing.T) {
	raw := writeFixture(t, validFixture(t))
	entries := readTestZIP(t, raw)
	for index := range entries {
		switch entries[index].Header.Name {
		case ManifestPath:
			var manifest map[string]any
			if err := json.Unmarshal(entries[index].Data, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["schema_version"] = float64(SchemaVersionV1)
			manifest["exclusions"] = ExclusionsV1()
			entries[index].Data, _ = json.Marshal(manifest)
		case FilesIndexPath:
			lines := bytes.Split(bytes.TrimSuffix(entries[index].Data, []byte("\n")), []byte("\n"))
			for lineIndex := range lines {
				var record map[string]any
				if err := json.Unmarshal(lines[lineIndex], &record); err != nil {
					t.Fatal(err)
				}
				delete(record, "user_tags")
				delete(record, "geo")
				delete(record, "source_metadata")
				delete(record, "annotations")
				lines[lineIndex], _ = json.Marshal(record)
			}
			entries[index].Data = append(bytes.Join(lines, []byte("\n")), '\n')
		}
	}
	recomputeTestChecksums(entries)
	legacy := writeTestZIP(t, entries)

	archive, err := Open(bytes.NewReader(legacy), int64(len(legacy)), ReaderOptions{})
	if err != nil {
		t.Fatalf("Open historical v1: %v", err)
	}
	if archive.Manifest.SchemaVersion != SchemaVersionV1 ||
		archive.Files[0].UserTags != nil ||
		archive.Files[0].Annotations != nil {
		t.Fatalf("legacy archive = %+v", archive.BundleData)
	}
}

func TestOpenRejectsMissingRequiredV2NestedFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, record map[string]any)
	}{
		{
			name: "annotation confidence",
			mutate: func(t *testing.T, record map[string]any) {
				t.Helper()
				annotations, ok := record["annotations"].([]any)
				if !ok || len(annotations) == 0 {
					t.Fatalf("annotations = %#v", record["annotations"])
				}
				annotation, ok := annotations[0].(map[string]any)
				if !ok {
					t.Fatalf("annotation = %#v", annotations[0])
				}
				delete(annotation, "confidence")
			},
		},
		{
			name: "geo latitude",
			mutate: func(t *testing.T, record map[string]any) {
				t.Helper()
				geo, ok := record["geo"].(map[string]any)
				if !ok {
					t.Fatalf("geo = %#v", record["geo"])
				}
				delete(geo, "lat")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := readTestZIP(t, writeFixture(t, validFixture(t)))
			for index := range entries {
				if entries[index].Header.Name != FilesIndexPath {
					continue
				}
				lines := bytes.Split(
					bytes.TrimSuffix(entries[index].Data, []byte("\n")),
					[]byte("\n"),
				)
				var record map[string]any
				if err := json.Unmarshal(lines[0], &record); err != nil {
					t.Fatal(err)
				}
				test.mutate(t, record)
				lines[0], _ = json.Marshal(record)
				entries[index].Data = append(bytes.Join(lines, []byte("\n")), '\n')
			}
			recomputeTestChecksums(entries)
			raw := writeTestZIP(t, entries)
			_, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
			if !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("Open error = %v, want ErrInvalidBundle", err)
			}
		})
	}
}

func TestWriteRejectsLegacySchema(t *testing.T) {
	fixture := validFixture(t)
	fixture.Manifest.SchemaVersion = SchemaVersionV1
	var output bytes.Buffer

	err := Write(&output, fixture, WriterOptions{})

	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Write error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestOpenRejectsUnsafeAndUnknownEntries(t *testing.T) {
	base := readTestZIP(t, writeFixture(t, validFixture(t)))
	tests := []struct {
		name  string
		entry testZIPEntry
	}{
		{
			name: "zip slip",
			entry: testZIPEntry{
				Header: zip.FileHeader{Name: "../escape", Method: zip.Store},
				Data:   []byte("escape"),
			},
		},
		{
			name: "absolute",
			entry: testZIPEntry{
				Header: zip.FileHeader{Name: "/escape", Method: zip.Store},
				Data:   []byte("escape"),
			},
		},
		{
			name: "windows separator",
			entry: testZIPEntry{
				Header: zip.FileHeader{Name: `objects\escape`, Method: zip.Store},
				Data:   []byte("escape"),
			},
		},
		{
			name: "unknown v1 entry",
			entry: testZIPEntry{
				Header: zip.FileHeader{Name: "objects/future.ndjson", Method: zip.Store},
				Data:   []byte("{}\n"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := append([]testZIPEntry(nil), base...)
			entries = append(entries, test.entry)
			raw := writeTestZIP(t, entries)
			_, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("Open error = %v, want ErrUnsafeArchive", err)
			}
		})
	}
}

func TestOpenRejectsSymlinkAndDuplicateEntry(t *testing.T) {
	base := readTestZIP(t, writeFixture(t, validFixture(t)))
	t.Run("symlink", func(t *testing.T) {
		entries := append([]testZIPEntry(nil), base...)
		header := zip.FileHeader{
			Name:   checkpointPayloadPath(uuid.MustParse("60000000-0000-0000-0000-000000000099")),
			Method: zip.Store,
		}
		header.SetMode(0o777 | os.ModeSymlink)
		entries = append(entries, testZIPEntry{Header: header, Data: []byte("target")})
		raw := writeTestZIP(t, entries)
		_, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
		if !errors.Is(err, ErrUnsafeArchive) {
			t.Fatalf("Open error = %v, want ErrUnsafeArchive", err)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		entries := append([]testZIPEntry(nil), base...)
		for _, entry := range base {
			if entry.Header.Name == ManifestPath {
				entries = append(entries, entry)
				break
			}
		}
		raw := writeTestZIP(t, entries)
		_, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
		if !errors.Is(err, ErrUnsafeArchive) {
			t.Fatalf("Open error = %v, want ErrUnsafeArchive", err)
		}
	})
}

func TestOpenRejectsMissingRequiredEntry(t *testing.T) {
	entries := readTestZIP(t, writeFixture(t, validFixture(t)))
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.Header.Name != FoldersIndexPath {
			filtered = append(filtered, entry)
		}
	}
	recomputeTestChecksums(filtered)
	raw := writeTestZIP(t, filtered)
	_, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
	if !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("Open error = %v, want ErrInvalidBundle", err)
	}
}

func TestOpenRejectsBombLikeCompressionAndJSONDepth(t *testing.T) {
	t.Run("compression ratio", func(t *testing.T) {
		entries := readTestZIP(t, writeFixture(t, validFixture(t)))
		entries = append(entries, testZIPEntry{
			Header: zip.FileHeader{
				Name:   checkpointPayloadPath(uuid.MustParse("60000000-0000-0000-0000-000000000099")),
				Method: zip.Deflate,
			},
			Data: bytes.Repeat([]byte{'0'}, 2<<20),
		})
		raw := writeTestZIP(t, entries)
		_, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("Open error = %v, want ErrLimitExceeded", err)
		}
	})
	t.Run("JSON depth", func(t *testing.T) {
		entries := readTestZIP(t, writeFixture(t, validFixture(t)))
		for index := range entries {
			if entries[index].Header.Name == ManifestPath {
				entries[index].Data = []byte(
					`{"deep":{"a":{"b":{"c":{"d":{"e":1}}}}}}`,
				)
			}
		}
		recomputeTestChecksums(entries)
		raw := writeTestZIP(t, entries)
		limits := DefaultLimits()
		limits.MaxJSONDepth = 4
		_, err := Open(
			bytes.NewReader(raw),
			int64(len(raw)),
			ReaderOptions{Limits: limits},
		)
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("Open error = %v, want ErrLimitExceeded", err)
		}
	})
}

func TestOpenRejectsUnknownManifestField(t *testing.T) {
	entries := readTestZIP(t, writeFixture(t, validFixture(t)))
	for index := range entries {
		if entries[index].Header.Name != ManifestPath {
			continue
		}
		var manifest map[string]any
		if err := json.Unmarshal(entries[index].Data, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest["future_required_field"] = true
		entries[index].Data, _ = json.Marshal(manifest)
	}
	recomputeTestChecksums(entries)
	raw := writeTestZIP(t, entries)
	_, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
	if !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("Open error = %v, want ErrInvalidBundle", err)
	}
}

func TestOpenRejectsManifestCountAndChecksumSizeMismatch(t *testing.T) {
	t.Run("manifest count", func(t *testing.T) {
		entries := readTestZIP(t, writeFixture(t, validFixture(t)))
		for index := range entries {
			if entries[index].Header.Name != ManifestPath {
				continue
			}
			var manifest map[string]any
			if err := json.Unmarshal(entries[index].Data, &manifest); err != nil {
				t.Fatal(err)
			}
			indexes := manifest["indexes"].(map[string]any)
			files := indexes["files"].(map[string]any)
			files["count"] = float64(2)
			entries[index].Data, _ = json.Marshal(manifest)
		}
		recomputeTestChecksums(entries)
		raw := writeTestZIP(t, entries)
		_, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
		if !errors.Is(err, ErrInvalidBundle) {
			t.Fatalf("Open error = %v, want ErrInvalidBundle", err)
		}
	})
	t.Run("checksum size", func(t *testing.T) {
		entries := readTestZIP(t, writeFixture(t, validFixture(t)))
		for index := range entries {
			if entries[index].Header.Name == ChecksumsPath {
				lines := strings.Split(string(entries[index].Data), "\n")
				for lineIndex, line := range lines {
					if strings.HasSuffix(line, "\t"+ManifestPath) {
						fields := strings.Split(line, "\t")
						fields[1] = "1"
						lines[lineIndex] = strings.Join(fields, "\t")
					}
				}
				entries[index].Data = []byte(strings.Join(lines, "\n"))
			}
		}
		raw := writeTestZIP(t, entries)
		_, err := Open(bytes.NewReader(raw), int64(len(raw)), ReaderOptions{})
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Open error = %v, want ErrIntegrity", err)
		}
	})
}

func writeFixture(t *testing.T, fixture WriteInput) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := Write(&output, fixture, WriterOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return output.Bytes()
}

type testZIPEntry struct {
	Header zip.FileHeader
	Data   []byte
}

func readTestZIP(t *testing.T, raw []byte) []testZIPEntry {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	out := make([]testZIPEntry, 0, len(reader.File))
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		header := file.FileHeader
		header.CompressedSize64 = 0
		header.UncompressedSize64 = 0
		header.CRC32 = 0
		out = append(out, testZIPEntry{Header: header, Data: data})
	}
	return out
}

func writeTestZIP(t *testing.T, entries []testZIPEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		header := entry.Header
		stream, err := writer.CreateHeader(&header)
		if err != nil {
			t.Fatalf("CreateHeader(%s): %v", header.Name, err)
		}
		if _, err := stream.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func recomputeTestChecksums(entries []testZIPEntry) {
	records := make([]checksumRecord, 0, len(entries)-1)
	for index := range entries {
		if entries[index].Header.Name == ChecksumsPath {
			continue
		}
		records = append(records, checksumRecord{
			SHA256: sha256Hex(entries[index].Data),
			Size:   uint64(len(entries[index].Data)),
			Path:   entries[index].Header.Name,
		})
	}
	for index := range entries {
		if entries[index].Header.Name == ChecksumsPath {
			entries[index].Data = marshalChecksums(records)
			return
		}
	}
}
