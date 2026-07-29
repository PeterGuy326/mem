package file

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestSourceMetadataValidate(t *testing.T) {
	t.Parallel()

	capturedAt := time.Date(2026, time.July, 29, 12, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	accuracy := 12.5
	valid := SourceMetadata{
		CapturedAt: &capturedAt,
		Location: &SourceLocation{
			Lat:            31.2304,
			Lon:            121.4737,
			AccuracyMeters: &accuracy,
			Label:          "Shanghai",
		},
		SourceKind: SourceKindMobile,
		SourceName: "phone sync",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}

	tests := []struct {
		name     string
		metadata SourceMetadata
	}{
		{
			name:     "unknown source kind",
			metadata: SourceMetadata{SourceKind: "camera"},
		},
		{
			name:     "source name control character",
			metadata: SourceMetadata{SourceName: "phone\nsync"},
		},
		{
			name:     "source name format character",
			metadata: SourceMetadata{SourceName: "phone\u200bsync"},
		},
		{
			name:     "source name default ignorable",
			metadata: SourceMetadata{SourceName: "phone\u034fsync"},
		},
		{
			name:     "source name too long",
			metadata: SourceMetadata{SourceName: strings.Repeat("a", maxSourceMetadataTextRunes+1)},
		},
		{
			name: "latitude out of range",
			metadata: SourceMetadata{
				Location: &SourceLocation{Lat: 90.1, Lon: 0},
			},
		},
		{
			name: "longitude out of range",
			metadata: SourceMetadata{
				Location: &SourceLocation{Lat: 0, Lon: -180.1},
			},
		},
		{
			name: "non finite latitude",
			metadata: SourceMetadata{
				Location: &SourceLocation{Lat: math.NaN(), Lon: 0},
			},
		},
		{
			name: "negative accuracy",
			metadata: SourceMetadata{
				Location: &SourceLocation{
					Lat:            0,
					Lon:            0,
					AccuracyMeters: float64Pointer(-1),
				},
			},
		},
		{
			name: "implausibly large accuracy",
			metadata: SourceMetadata{
				Location: &SourceLocation{
					Lat:            0,
					Lon:            0,
					AccuracyMeters: float64Pointer(maxLocationAccuracyMeters + 1),
				},
			},
		},
		{
			name: "location label control character",
			metadata: SourceMetadata{
				Location: &SourceLocation{Lat: 0, Lon: 0, Label: "home\u007f"},
			},
		},
		{
			name: "location label variation selector",
			metadata: SourceMetadata{
				Location: &SourceLocation{Lat: 0, Lon: 0, Label: "home\ufe0f"},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.metadata.Validate(); err == nil {
				t.Fatalf("Validate() accepted invalid metadata: %+v", test.metadata)
			}
		})
	}
}

func float64Pointer(value float64) *float64 {
	return &value
}
