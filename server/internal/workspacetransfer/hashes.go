package workspacetransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/google/uuid"
)

const memoryTimeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func checkpointRequestSHA256(
	workspaceID uuid.UUID,
	taskKey string,
	payload []byte,
) (string, error) {
	value := struct {
		WorkspaceID string          `json:"workspace_id"`
		TaskKey     string          `json:"task_key"`
		Handoff     json.RawMessage `json:"handoff"`
	}{
		WorkspaceID: workspaceID.String(),
		TaskKey:     taskKey,
		Handoff:     payload,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digestBytes(raw), nil
}

func memoryRequestSHA256(
	workspaceID uuid.UUID,
	record workspacebundle.MemoryRecord,
) (string, error) {
	// A forgotten memory intentionally no longer contains the original request
	// payload. Its API replay path is terminal (ErrForgotten), so use a
	// workspace-bound tombstone hash rather than pretending the erased payload
	// can be reconstructed.
	if record.LifecycleStatus == "forgotten" {
		// Privacy-hardened tombstones deliberately erase the original request
		// payload and request hash. Never manufacture a new correlation value.
		return strings.Repeat("0", 64), nil
	}
	value := struct {
		WorkspaceID      string          `json:"workspace_id"`
		Kind             string          `json:"kind"`
		Content          string          `json:"content"`
		Attributes       json.RawMessage `json:"attributes"`
		Path             string          `json:"path"`
		EventAt          string          `json:"event_at,omitempty"`
		SourceType       string          `json:"source_type"`
		SourceRef        string          `json:"source_ref,omitempty"`
		SourceFileID     string          `json:"source_file_id,omitempty"`
		SourceFileSHA256 string          `json:"source_file_sha256,omitempty"`
		SourceLocator    json.RawMessage `json:"source_locator"`
		ProducerAgent    string          `json:"producer_agent,omitempty"`
		ProducerSession  string          `json:"producer_session,omitempty"`
		ProducerTask     string          `json:"producer_task,omitempty"`
	}{
		WorkspaceID:      workspaceID.String(),
		Kind:             record.Kind,
		Content:          record.Content,
		Attributes:       record.Attributes,
		Path:             record.Path,
		SourceType:       record.SourceType,
		SourceRef:        record.SourceRef,
		SourceFileSHA256: record.SourceFileSHA256,
		SourceLocator:    record.SourceLocator,
		ProducerAgent:    record.ProducerAgent,
		ProducerSession:  record.ProducerSession,
		ProducerTask:     record.ProducerTask,
	}
	if record.EventAt != nil {
		value.EventAt = record.EventAt.UTC().Format(memoryTimeFormat)
	}
	if record.SourceFileID != nil {
		value.SourceFileID = record.SourceFileID.String()
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digestBytes(raw), nil
}

func archiveSHA256(readerAt io.ReaderAt, size int64) (string, error) {
	hasher := sha256.New()
	reader := io.NewSectionReader(readerAt, 0, size)
	if _, err := io.Copy(hasher, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
