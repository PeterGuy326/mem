package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/PeterGuy326/mem/server/internal/modeltext"
)

const (
	SourceKindAPI      = "api"
	SourceKindWeb      = "web"
	SourceKindCLI      = "cli"
	SourceKindMCP      = "mcp"
	SourceKindMobile   = "mobile"
	SourceKindAIDevice = "ai_device"
	SourceKindImport   = "import"
	SourceKindOther    = "other"

	maxSourceMetadataTextRunes = 512
	maxLocationAccuracyMeters  = 40_100_000
)

var validSourceKinds = map[string]struct{}{
	SourceKindAPI:      {},
	SourceKindWeb:      {},
	SourceKindCLI:      {},
	SourceKindMCP:      {},
	SourceKindMobile:   {},
	SourceKindAIDevice: {},
	SourceKindImport:   {},
	SourceKindOther:    {},
}

// Geo is the API-safe projection of PostgreSQL point(lon, lat).
type Geo struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// SourceLocation is explicit, caller-provided capture location metadata.
// AccuracyMeters and Label are optional; latitude and longitude are required
// whenever a location object is supplied.
type SourceLocation struct {
	Lat            float64  `json:"lat"`
	Lon            float64  `json:"lon"`
	AccuracyMeters *float64 `json:"accuracy_m,omitempty"`
	Label          string   `json:"label,omitempty"`
}

// SourceMetadata is bounded provenance supplied at ingestion. It is distinct
// from processor metadata so a model can never rewrite caller evidence.
type SourceMetadata struct {
	CapturedAt *time.Time      `json:"captured_at,omitempty"`
	Location   *SourceLocation `json:"location,omitempty"`
	SourceKind string          `json:"source_kind,omitempty"`
	SourceName string          `json:"source_name,omitempty"`
}

// Validate applies the canonical semantic checks after transport decoding.
func (metadata SourceMetadata) Validate() error {
	if metadata.CapturedAt != nil && metadata.CapturedAt.IsZero() {
		return errors.New("captured_at must not be zero")
	}
	if metadata.SourceKind != "" {
		if _, ok := validSourceKinds[metadata.SourceKind]; !ok {
			return fmt.Errorf("source_kind %q is not supported", metadata.SourceKind)
		}
	}
	if err := validateMetadataText("source_name", metadata.SourceName); err != nil {
		return err
	}
	if metadata.Location == nil {
		return nil
	}
	location := metadata.Location
	if math.IsNaN(location.Lat) || math.IsInf(location.Lat, 0) ||
		location.Lat < -90 || location.Lat > 90 {
		return errors.New("location.lat must be between -90 and 90")
	}
	if math.IsNaN(location.Lon) || math.IsInf(location.Lon, 0) ||
		location.Lon < -180 || location.Lon > 180 {
		return errors.New("location.lon must be between -180 and 180")
	}
	if location.AccuracyMeters != nil {
		accuracy := *location.AccuracyMeters
		if math.IsNaN(accuracy) || math.IsInf(accuracy, 0) ||
			accuracy < 0 || accuracy > maxLocationAccuracyMeters {
			return fmt.Errorf(
				"location.accuracy_m must be between 0 and %d",
				maxLocationAccuracyMeters,
			)
		}
	}
	return validateMetadataText("location.label", location.Label)
}

func (metadata SourceMetadata) canonicalJSON() ([]byte, error) {
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode source metadata: %w", err)
	}
	return encoded, nil
}

func validateMetadataText(name, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if utf8.RuneCountInString(value) > maxSourceMetadataTextRunes {
		return fmt.Errorf("%s exceeds %d characters", name, maxSourceMetadataTextRunes)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 ||
		modeltext.ContainsNonDisplay(value) {
		return fmt.Errorf("%s must not contain control or non-display characters", name)
	}
	return nil
}
