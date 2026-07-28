package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/PeterGuy326/mem/server/internal/pathx"
	"github.com/google/uuid"
)

const (
	maxContentBytes        = 64 * 1024
	maxIdempotencyKeyRunes = 200
	maxJSONObjectBytes     = 64 * 1024
	maxSourceRefBytes      = 8 * 1024
	maxProducerIDRunes     = 255
)

var sourceTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var validKinds = map[string]struct{}{
	KindObservation: {},
	KindDecision:    {},
	KindPreference:  {},
	KindTaskState:   {},
	KindFact:        {},
	KindNote:        {},
	KindArtifact:    {},
}

type normalizedCommand struct {
	Command
	idempotencyKeySHA256 string
	requestSHA256        string
	contentSHA256        string
}

func normalizeCommand(cmd Command) (normalizedCommand, error) {
	if cmd.WorkspaceID == uuid.Nil {
		return normalizedCommand{}, invalid("workspace_id is required")
	}

	cmd.Kind = strings.ToLower(strings.TrimSpace(cmd.Kind))
	if _, ok := validKinds[cmd.Kind]; !ok {
		return normalizedCommand{}, invalid(
			"kind must be one of observation|decision|preference|task_state|fact|note|artifact",
		)
	}

	cmd.Content = strings.TrimSpace(cmd.Content)
	if cmd.Content == "" {
		return normalizedCommand{}, invalid("content must not be empty")
	}
	if !utf8.ValidString(cmd.Content) {
		return normalizedCommand{}, invalid("content must be valid UTF-8")
	}
	if len([]byte(cmd.Content)) > maxContentBytes {
		return normalizedCommand{}, invalid("content exceeds 65536 bytes")
	}

	rawPath := strings.TrimSpace(cmd.Path)
	if rawPath == "" {
		return normalizedCommand{}, invalid("path is required")
	}
	path, err := pathx.Normalize(rawPath)
	if err != nil {
		return normalizedCommand{}, invalid("path: %v", err)
	}
	cmd.Path = path

	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	if !utf8.ValidString(cmd.IdempotencyKey) {
		return normalizedCommand{}, invalid("idempotency key must be valid UTF-8")
	}
	keyLen := utf8.RuneCountInString(cmd.IdempotencyKey)
	if keyLen < 1 || keyLen > maxIdempotencyKeyRunes {
		return normalizedCommand{}, invalid("idempotency key length must be between 1 and 200 characters")
	}

	cmd.SourceType = strings.ToLower(strings.TrimSpace(cmd.SourceType))
	if !sourceTypePattern.MatchString(cmd.SourceType) {
		return normalizedCommand{}, invalid(
			"source_type must match [a-z][a-z0-9_.-]{0,63}",
		)
	}
	cmd.SourceRef = strings.TrimSpace(cmd.SourceRef)
	if !utf8.ValidString(cmd.SourceRef) {
		return normalizedCommand{}, invalid("source_ref must be valid UTF-8")
	}
	if len([]byte(cmd.SourceRef)) > maxSourceRefBytes {
		return normalizedCommand{}, invalid("source_ref exceeds 8192 bytes")
	}
	cmd.SourceFileSHA256 = strings.ToLower(strings.TrimSpace(cmd.SourceFileSHA256))
	if cmd.SourceFileSHA256 != "" && !sha256Pattern.MatchString(cmd.SourceFileSHA256) {
		return normalizedCommand{}, invalid(
			"source_file_sha256 must be a 64-character hexadecimal SHA-256 digest",
		)
	}
	cmd.ProducerAgent = strings.TrimSpace(cmd.ProducerAgent)
	cmd.ProducerSession = strings.TrimSpace(cmd.ProducerSession)
	cmd.ProducerTask = strings.TrimSpace(cmd.ProducerTask)
	for field, value := range map[string]string{
		"producer_agent":   cmd.ProducerAgent,
		"producer_session": cmd.ProducerSession,
		"producer_task":    cmd.ProducerTask,
	} {
		if !utf8.ValidString(value) {
			return normalizedCommand{}, invalid("%s must be valid UTF-8", field)
		}
		if utf8.RuneCountInString(value) > maxProducerIDRunes {
			return normalizedCommand{}, invalid(
				"%s exceeds 255 characters", field,
			)
		}
	}

	cmd.Attributes, err = normalizeJSONObject(cmd.Attributes, "attributes")
	if err != nil {
		return normalizedCommand{}, err
	}
	cmd.SourceLocator, err = normalizeJSONObject(cmd.SourceLocator, "source_locator")
	if err != nil {
		return normalizedCommand{}, err
	}
	if cmd.EventAt != nil {
		eventAt := cmd.EventAt.UTC()
		cmd.EventAt = &eventAt
	}

	contentHash := sha256.Sum256([]byte(cmd.Content))
	keyHash := sha256.Sum256([]byte(cmd.IdempotencyKey))
	requestHash, err := normalizedRequestHash(cmd)
	if err != nil {
		return normalizedCommand{}, fmt.Errorf("hash normalized memory request: %w", err)
	}
	return normalizedCommand{
		Command:              cmd,
		idempotencyKeySHA256: hex.EncodeToString(keyHash[:]),
		requestSHA256:        hex.EncodeToString(requestHash[:]),
		contentSHA256:        hex.EncodeToString(contentHash[:]),
	}, nil
}

func normalizeJSONObject(raw json.RawMessage, field string) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, invalid("%s must be a valid JSON object: %v", field, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, invalid("%s must contain exactly one JSON value", field)
		}
		return nil, invalid("%s must be a valid JSON object: %v", field, err)
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, invalid("%s must be a JSON object", field)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, invalid("%s must be a valid JSON object: %v", field, err)
	}
	if len(canonical) > maxJSONObjectBytes {
		return nil, invalid("%s exceeds 65536 bytes", field)
	}
	return json.RawMessage(canonical), nil
}

type hashPayload struct {
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
}

func normalizedRequestHash(cmd Command) ([sha256.Size]byte, error) {
	payload := hashPayload{
		WorkspaceID:      cmd.WorkspaceID.String(),
		Kind:             cmd.Kind,
		Content:          cmd.Content,
		Attributes:       cmd.Attributes,
		Path:             cmd.Path,
		SourceType:       cmd.SourceType,
		SourceRef:        cmd.SourceRef,
		SourceFileSHA256: cmd.SourceFileSHA256,
		SourceLocator:    cmd.SourceLocator,
		ProducerAgent:    cmd.ProducerAgent,
		ProducerSession:  cmd.ProducerSession,
		ProducerTask:     cmd.ProducerTask,
	}
	if cmd.EventAt != nil {
		payload.EventAt = cmd.EventAt.UTC().Format(timeFormat)
	}
	if cmd.SourceFileID != nil {
		payload.SourceFileID = cmd.SourceFileID.String()
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func normalizeScope(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return pathx.Root, nil
	}
	scope, err := pathx.Normalize(raw)
	if err != nil {
		return "", invalid("scope: %v", err)
	}
	return scope, nil
}

func normalizeAllowedPaths(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	paths := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, invalid("allowed paths contain an empty path")
		}
		path, err := pathx.Normalize(item)
		if err != nil {
			return nil, invalid("allowed path %q: %v", item, err)
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	if _, unrestricted := seen[pathx.Root]; unrestricted {
		return []string{pathx.Root}, nil
	}
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) == len(paths[j]) {
			return paths[i] < paths[j]
		}
		return len(paths[i]) < len(paths[j])
	})
	compacted := make([]string, 0, len(paths))
	for _, candidate := range paths {
		covered := false
		for _, ancestor := range compacted {
			if pathx.IsDescendantOrSelf(candidate, ancestor) {
				covered = true
				break
			}
		}
		if !covered {
			compacted = append(compacted, candidate)
		}
	}
	sort.Strings(compacted)
	return compacted, nil
}

func pathAllowed(candidate string, allowed []string) bool {
	if len(allowed) == 0 || (len(allowed) == 1 && allowed[0] == pathx.Root) {
		return true
	}
	for _, ancestor := range allowed {
		if pathx.IsDescendantOrSelf(candidate, ancestor) {
			return true
		}
	}
	return false
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidCommand, fmt.Sprintf(format, args...))
}
