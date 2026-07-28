package workspacebundle

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sourceTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
)

// CheckpointPayloadEntryPath returns the fixed v1 payload entry for id.
func CheckpointPayloadEntryPath(id uuid.UUID) string {
	return CheckpointPayloadPrefix + id.String() + ".json"
}

func checkpointPayloadPath(id uuid.UUID) string {
	return CheckpointPayloadEntryPath(id)
}

func blobPath(digest string) string {
	if len(digest) < 2 {
		return ""
	}
	return ContentAddressedBlobRoot + digest[:2] + "/" + digest
}

// BlobEntryPath returns the fixed v1 content-addressed entry for digest.
func BlobEntryPath(digest string) (string, error) {
	if err := validateSHA256("blob sha256", digest, false); err != nil {
		return "", err
	}
	return blobPath(digest), nil
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// MemoryEventRequestSHA256 computes the v1 request hash for a source or target
// workspace without requiring the unavailable raw idempotency key.
func MemoryEventRequestSHA256(
	workspaceID uuid.UUID,
	event MemoryEventRecord,
) (string, error) {
	if workspaceID == uuid.Nil {
		return "", fmt.Errorf("%w: workspace_id is required", ErrInvalidBundle)
	}
	payload := struct {
		WorkspaceID     string `json:"workspace_id"`
		MemoryID        string `json:"memory_id"`
		Action          string `json:"action"`
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason,omitempty"`
	}{
		WorkspaceID:     workspaceID.String(),
		MemoryID:        event.MemoryID.String(),
		Action:          event.Action,
		ExpectedVersion: event.ExpectedVersion,
		Reason:          event.Reason,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal memory event request hash: %w", err)
	}
	return sha256Hex(raw), nil
}

func decodeStrictOne[T any](raw []byte, label string) (T, error) {
	var out T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, fmt.Errorf("%w: decode %s: %v", ErrInvalidBundle, label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return out, fmt.Errorf(
				"%w: %s must contain exactly one JSON value",
				ErrInvalidBundle,
				label,
			)
		}
		return out, fmt.Errorf("%w: decode %s: %v", ErrInvalidBundle, label, err)
	}
	return out, nil
}

func validateJSONDepth(raw []byte, maxDepth int, label string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: decode %s: %v", ErrInvalidBundle, label, err)
		}
		delim, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delim {
		case '{', '[':
			depth++
			if depth > maxDepth {
				return fmt.Errorf(
					"%w: %s exceeds JSON depth %d",
					ErrLimitExceeded,
					label,
					maxDepth,
				)
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return fmt.Errorf("%w: malformed %s", ErrInvalidBundle, label)
			}
		}
	}
}

func canonicalJSONObject(raw json.RawMessage, maxDepth int, label string) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: %s must be a JSON object", ErrInvalidBundle, label)
	}
	if err := validateJSONDepth(raw, maxDepth, label); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: decode %s: %v", ErrInvalidBundle, label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf(
				"%w: %s must contain exactly one JSON value",
				ErrInvalidBundle,
				label,
			)
		}
		return nil, fmt.Errorf("%w: decode %s: %v", ErrInvalidBundle, label, err)
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("%w: %s must be a JSON object", ErrInvalidBundle, label)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize %s: %v", ErrInvalidBundle, label, err)
	}
	return canonical, nil
}

func decodeNDJSON[T any](raw []byte, limits Limits, label string) ([]T, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	initial := 64 * 1024
	if limits.MaxIndexLineBytes < initial {
		initial = limits.MaxIndexLineBytes
	}
	scanner.Buffer(make([]byte, initial), limits.MaxIndexLineBytes)

	out := make([]T, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSuffix(scanner.Bytes(), []byte{'\r'})
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, fmt.Errorf(
				"%w: %s line %d is empty",
				ErrInvalidBundle,
				label,
				lineNumber,
			)
		}
		if err := validateJSONDepth(line, limits.MaxJSONDepth, fmt.Sprintf("%s line %d", label, lineNumber)); err != nil {
			return nil, err
		}
		record, err := decodeStrictOne[T](line, fmt.Sprintf("%s line %d", label, lineNumber))
		if err != nil {
			return nil, err
		}
		out = append(out, record)
		if len(out) > limits.MaxRecordsPerIndex {
			return nil, fmt.Errorf(
				"%w: %s exceeds %d records",
				ErrLimitExceeded,
				label,
				limits.MaxRecordsPerIndex,
			)
		}
	}
	if err := scanner.Err(); err != nil {
		if errorsIsBufferTooLong(err) {
			return nil, fmt.Errorf(
				"%w: %s line exceeds %d bytes",
				ErrLimitExceeded,
				label,
				limits.MaxIndexLineBytes,
			)
		}
		return nil, fmt.Errorf("%w: scan %s: %v", ErrInvalidBundle, label, err)
	}
	return out, nil
}

// Scanner intentionally does not expose ErrTooLong. Its stable error text is
// only used to preserve the public sentinel; all other scanner errors retain
// their original detail.
func errorsIsBufferTooLong(err error) bool {
	return strings.Contains(err.Error(), "token too long") ||
		strings.Contains(err.Error(), "too long")
}

func marshalNDJSON[T any](records []T) ([]byte, error) {
	var out bytes.Buffer
	for i := range records {
		raw, err := json.Marshal(records[i])
		if err != nil {
			return nil, fmt.Errorf("marshal NDJSON record %d: %w", i, err)
		}
		out.Write(raw)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func validateSHA256(field, value string, allowEmpty bool) error {
	if allowEmpty && value == "" {
		return nil
	}
	if !sha256Pattern.MatchString(value) {
		return fmt.Errorf(
			"%w: %s must be a lowercase 64-character SHA-256 digest",
			ErrInvalidBundle,
			field,
		)
	}
	return nil
}

func validateText(field, value string, minRunes, maxRunes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s must be valid UTF-8", ErrInvalidBundle, field)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%w: %s must not contain NUL", ErrInvalidBundle, field)
	}
	count := utf8.RuneCountInString(value)
	if count < minRunes || count > maxRunes {
		return fmt.Errorf(
			"%w: %s length must be between %d and %d characters",
			ErrInvalidBundle,
			field,
			minRunes,
			maxRunes,
		)
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
