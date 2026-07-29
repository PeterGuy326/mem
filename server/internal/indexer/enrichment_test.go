package indexer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/PeterGuy326/mem/server/internal/workerpb"
)

func TestParseWorkerEnrichmentSeparatesFactsAndSuggestions(t *testing.T) {
	response := &workerpb.ProcessResponse{
		Processor: "image",
		Status:    workerpb.ProcessStatus_STATUS_PARTIAL,
		MetadataJson: []byte(`{
			"format":"JPEG",
			"width":4032,
			"height":3024,
			"timeline_at":"2026-07-29T08:00:00+08:00",
			"gps":{"lat":31.2304,"lng":121.4737},
			"vlm_error":"secret backend detail",
			"annotations":[
				{"kind":"description","value":"A street scene","confidence":0.82,"source":"model","provider":"test:vlm","processor":"image","analysis_version":"file-enrichment-v1"},
				{"kind":"tag","value":"Shanghai","confidence":0.76,"source":"model","provider":"test:vlm","processor":"image","analysis_version":"file-enrichment-v1"}
			]
		}`),
	}

	enrichment := parseWorkerEnrichment(response)
	if len(enrichment.Annotations) != 2 {
		t.Fatalf("annotations = %#v", enrichment.Annotations)
	}
	if enrichment.Annotations[0].Kind != "description" ||
		enrichment.Annotations[1].Value != "Shanghai" ||
		!strings.HasPrefix(enrichment.Annotations[1].StableKey, "sha256:") {
		t.Fatalf("annotations = %#v", enrichment.Annotations)
	}
	timeline, ok := enrichment.Timeline.(time.Time)
	if !ok || timeline.Format(time.RFC3339) != "2026-07-29T08:00:00+08:00" {
		t.Fatalf("timeline = %#v", enrichment.Timeline)
	}
	geo, ok := enrichment.Geo.(pgtype.Point)
	if !ok || !geo.Valid || geo.P.X != 121.4737 || geo.P.Y != 31.2304 {
		t.Fatalf("geo = %#v", enrichment.Geo)
	}

	var metadata map[string]any
	if err := json.Unmarshal(enrichment.ProcessorMetadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["format"] != "JPEG" || metadata["processor"] != "image" ||
		metadata["status"] != "partial" {
		t.Fatalf("processor metadata = %#v", metadata)
	}
	if _, leaked := metadata["vlm_error"]; leaked {
		t.Fatalf("processor metadata leaked provider error: %#v", metadata)
	}
	if _, leaked := metadata["annotations"]; leaked {
		t.Fatalf("processor metadata duplicated annotations: %#v", metadata)
	}
	if degraded, ok := metadata["degraded_steps"].([]any); !ok ||
		len(degraded) != 1 || degraded[0] != "vision_model" {
		t.Fatalf("degraded_steps = %#v", metadata["degraded_steps"])
	}
}

func TestParseWorkerEnrichmentRetainsNaiveTimelineAsUncertainMetadata(t *testing.T) {
	response := &workerpb.ProcessResponse{
		Processor:    "image",
		Status:       workerpb.ProcessStatus_STATUS_OK,
		MetadataJson: []byte(`{"timeline_at":"2026-07-29T08:00:00","annotations":[]}`),
	}
	enrichment := parseWorkerEnrichment(response)
	if enrichment.Timeline != nil {
		t.Fatalf("naive timeline must not be projected: %#v", enrichment.Timeline)
	}
	var metadata map[string]any
	if err := json.Unmarshal(enrichment.ProcessorMetadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["timeline_at"] != "2026-07-29T08:00:00" ||
		metadata["timeline_timezone_unknown"] != true {
		t.Fatalf("processor metadata = %#v", metadata)
	}
}

func TestParseWorkerEnrichmentDistinguishesCompletedEmptyAnalysis(t *testing.T) {
	annotation := `{"kind":"tag","value":"safe","confidence":0.9,` +
		`"source":"model","provider":"test","processor":"image",` +
		`"analysis_version":"file-enrichment-v1"}`
	tests := []struct {
		name          string
		metadata      string
		wantComplete  bool
		wantReconcile bool
		wantCaption   bool
		wantPartial   bool
	}{
		{
			name:     "legacy marker absent preserves review state",
			metadata: `{"annotations":[]}`,
		},
		{
			name:     "explicit incomplete preserves review state",
			metadata: `{"annotations":[],"annotations_complete":false}`,
		},
		{
			name:          "completed empty analysis reconciles review state",
			metadata:      `{"annotations":[],"annotations_complete":true}`,
			wantComplete:  true,
			wantReconcile: true,
			wantCaption:   true,
		},
		{
			name:        "complete marker requires annotation payload",
			metadata:    `{"annotations_complete":true}`,
			wantPartial: true,
		},
		{
			name:        "complete marker must be boolean",
			metadata:    `{"annotations":[],"annotations_complete":"true"}`,
			wantPartial: true,
		},
		{
			name:          "legacy nonempty result reconciles",
			metadata:      `{"annotations":[` + annotation + `]}`,
			wantReconcile: true,
			wantCaption:   true,
		},
		{
			name: "completed nonempty result reconciles",
			metadata: `{"annotations":[` + annotation + `],` +
				`"annotations_complete":true}`,
			wantComplete:  true,
			wantReconcile: true,
			wantCaption:   true,
		},
		{
			name: "explicit incomplete nonempty result is nondestructive",
			metadata: `{"annotations":[` + annotation + `],` +
				`"annotations_complete":false}`,
			wantPartial: true,
		},
		{
			name: "invalid completion marker is nondestructive",
			metadata: `{"annotations":[` + annotation + `],` +
				`"annotations_complete":"true"}`,
			wantPartial: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
				MetadataJson: []byte(test.metadata),
				Processor:    "image",
				Status:       workerpb.ProcessStatus_STATUS_OK,
			})
			if enrichment.AnnotationsComplete != test.wantComplete ||
				enrichment.ReconcileAnnotations != test.wantReconcile ||
				enrichment.CaptionSet != test.wantCaption ||
				enrichment.Partial != test.wantPartial {
				t.Fatalf("enrichment = %#v", enrichment)
			}
		})
	}
}

func TestParseWorkerEnrichmentSuppressesLegacyFieldsWhenCompletionIsPresent(t *testing.T) {
	for _, metadata := range []string{
		`{"annotations_complete":false}`,
		`{"annotations_complete":"false"}`,
		`{"annotations_complete":true}`,
	} {
		enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
			Summary:      "private summary",
			Caption:      "private caption",
			Tags:         []string{"private tag"},
			MetadataJson: []byte(metadata),
			Processor:    "image",
			Status:       workerpb.ProcessStatus_STATUS_OK,
		})
		if len(enrichment.Annotations) != 0 ||
			enrichment.CaptionSet ||
			enrichment.Caption != nil ||
			!enrichment.Partial {
			t.Fatalf("enrichment = %#v", enrichment)
		}
		if strings.Contains(string(enrichment.ProcessorMetadata), "private") ||
			!strings.Contains(
				string(enrichment.ProcessorMetadata),
				`"legacy_fields_suppressed":true`,
			) {
			t.Fatalf("processor metadata = %s", enrichment.ProcessorMetadata)
		}
	}
}

func TestParseWorkerEnrichmentRejectsMalformedAnnotationPayload(t *testing.T) {
	response := &workerpb.ProcessResponse{
		Processor: "image",
		Status:    workerpb.ProcessStatus_STATUS_OK,
		MetadataJson: []byte(`{"annotations":[{
			"kind":"tag",
			"value":"safe",
			"confidence":0.9,
			"source":"model",
			"provider":"test",
			"processor":"image",
			"analysis_version":"file-enrichment-v1",
			"hidden_reasoning":"do not persist"
		}]}`),
	}
	enrichment := parseWorkerEnrichment(response)
	if len(enrichment.Annotations) != 0 {
		t.Fatalf("malformed suggestions persisted: %#v", enrichment.Annotations)
	}
	if !strings.Contains(string(enrichment.ProcessorMetadata), `"annotation_payload_invalid":true`) {
		t.Fatalf("processor metadata = %s", enrichment.ProcessorMetadata)
	}
	if !enrichment.Partial {
		t.Fatal("invalid annotation payload must make indexing partial")
	}
}

func TestParseWorkerEnrichmentRejectsHiddenReasoningInStructuredValue(t *testing.T) {
	response := &workerpb.ProcessResponse{
		Caption:   "<think>private</think>A street scene",
		Processor: "image",
		Status:    workerpb.ProcessStatus_STATUS_OK,
		MetadataJson: []byte(`{"annotations":[{
			"kind":"description",
			"value":"<think>private</think>A street scene",
			"confidence":0.9,
			"source":"model",
			"provider":"test",
			"processor":"image",
			"analysis_version":"file-enrichment-v1"
		}]}`),
	}
	enrichment := parseWorkerEnrichment(response)
	if len(enrichment.Annotations) != 0 ||
		enrichment.CaptionSet ||
		enrichment.Caption != nil ||
		!enrichment.Partial {
		t.Fatalf("enrichment = %#v", enrichment)
	}
	if strings.Contains(string(enrichment.ProcessorMetadata), "private") {
		t.Fatalf("hidden reasoning leaked: %s", enrichment.ProcessorMetadata)
	}
}

func TestParseWorkerEnrichmentRejectsUnsafeAnnotationValuesWithoutRawLeak(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		value string
	}{
		{
			name:  "JSON-like description",
			kind:  "description",
			value: `{"analysis":"private-description-value","answer":"public"}`,
		},
		{
			name:  "JSON-like tag",
			kind:  "tag",
			value: `{"analysis":"private-tag-value"}`,
		},
		{
			name:  "reasoning-opening tag",
			kind:  "tag",
			value: `<reasoning visibility="hidden">private-reasoning-value`,
		},
		{
			name:  "reasoning-closing tag",
			kind:  "tag",
			value: `public</reasoning>private-reasoning-value`,
		},
		{
			name:  "BOM-prefixed JSON-like description",
			kind:  "description",
			value: "\ufeff{\"analysis\":\"private-format-description\",\"answer\":\"public\"}",
		},
		{
			name:  "zero-width-prefixed array tag",
			kind:  "tag",
			value: "\u200b[\"private-format-tag\"]",
		},
		{
			name:  "combining-grapheme-joiner description",
			kind:  "description",
			value: "\u034f{\"analysis\":\"private-ignorable-description\"}",
		},
		{
			name:  "variation-selector tag",
			kind:  "tag",
			value: "\ufe0f[\"private-ignorable-tag\"]",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata, err := json.Marshal(map[string]any{
				"annotations": []map[string]any{{
					"kind":             test.kind,
					"value":            test.value,
					"confidence":       0.9,
					"source":           "model",
					"provider":         "test",
					"processor":        "image",
					"analysis_version": "file-enrichment-v1",
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
				Processor:    "image",
				Status:       workerpb.ProcessStatus_STATUS_OK,
				MetadataJson: metadata,
			})
			if len(enrichment.Annotations) != 0 || !enrichment.Partial {
				t.Fatalf("enrichment = %#v", enrichment)
			}
			if strings.Contains(string(enrichment.ProcessorMetadata), "private") ||
				!strings.Contains(
					string(enrichment.ProcessorMetadata),
					`"annotation_payload_invalid":true`,
				) {
				t.Fatalf("processor metadata = %s", enrichment.ProcessorMetadata)
			}
		})
	}
}

func TestParseWorkerEnrichmentRejectsNonDisplayAnnotationProvenance(t *testing.T) {
	tests := []struct {
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			annotation := map[string]any{
				"kind":             "tag",
				"value":            "safe",
				"confidence":       0.9,
				"source":           "model",
				"provider":         "test",
				"processor":        "image",
				"analysis_version": "file-enrichment-v1",
			}
			annotation[test.field] = test.value
			metadata, err := json.Marshal(map[string]any{
				"annotations": []map[string]any{annotation},
			})
			if err != nil {
				t.Fatal(err)
			}
			enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
				Processor:    "image",
				Status:       workerpb.ProcessStatus_STATUS_OK,
				MetadataJson: metadata,
			})
			if len(enrichment.Annotations) != 0 || !enrichment.Partial {
				t.Fatalf("enrichment = %#v", enrichment)
			}
			if strings.Contains(string(enrichment.ProcessorMetadata), "private") ||
				!strings.Contains(
					string(enrichment.ProcessorMetadata),
					`"annotation_payload_invalid":true`,
				) {
				t.Fatalf("processor metadata = %s", enrichment.ProcessorMetadata)
			}
		})
	}
}

func TestParseWorkerEnrichmentMarksInvalidAndDegradedMetadataPartial(t *testing.T) {
	tests := []struct {
		name     string
		metadata []byte
	}{
		{name: "invalid JSON", metadata: []byte(`{`)},
		{name: "oversize", metadata: []byte(strings.Repeat("x", maxWorkerMetadataBytes+1))},
		{name: "ASR error", metadata: []byte(`{"asr_error":"private provider detail"}`)},
		{name: "generic error", metadata: []byte(`{"error":"private processor detail"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
				Processor:    "test",
				Status:       workerpb.ProcessStatus_STATUS_OK,
				MetadataJson: test.metadata,
			})
			if !enrichment.Partial {
				t.Fatalf("enrichment = %#v", enrichment)
			}
			if strings.Contains(string(enrichment.ProcessorMetadata), "private") {
				t.Fatalf("raw error leaked: %s", enrichment.ProcessorMetadata)
			}
		})
	}
}

func TestParseWorkerEnrichmentKeepsOnlyBoundedAIProfileProvenance(t *testing.T) {
	valid := parseWorkerEnrichment(&workerpb.ProcessResponse{
		Processor: "text",
		Status:    workerpb.ProcessStatus_STATUS_OK,
		MetadataJson: []byte(`{
			"ai_profile":{
				"contract":"mem.ai-profile/v1",
				"id":"idealab-quality-v1",
				"revision":"2026-07-29",
				"pipeline_revision":"file-enrichment-v1"
			},
			"annotations":[]
		}`),
	})
	if valid.Partial {
		t.Fatalf("valid AI profile provenance marked partial: %#v", valid)
	}
	if !strings.Contains(string(valid.ProcessorMetadata), `"id":"idealab-quality-v1"`) ||
		!strings.Contains(string(valid.ProcessorMetadata), `"pipeline_revision":"file-enrichment-v1"`) {
		t.Fatalf("profile provenance was not retained: %s", valid.ProcessorMetadata)
	}

	invalid := parseWorkerEnrichment(&workerpb.ProcessResponse{
		Processor: "text",
		Status:    workerpb.ProcessStatus_STATUS_OK,
		MetadataJson: []byte(`{
			"ai_profile":{
				"contract":"mem.ai-profile/v1",
				"id":"idealab-quality-v1",
				"revision":"2026-07-29",
				"pipeline_revision":"file-enrichment-v1",
				"api_key":"secret-value"
			},
			"annotations":[]
		}`),
	})
	if !invalid.Partial || strings.Contains(string(invalid.ProcessorMetadata), "secret-value") ||
		!strings.Contains(string(invalid.ProcessorMetadata), `"invalid_metadata_fields":["ai_profile"]`) {
		t.Fatalf("unsafe profile provenance result = %s", invalid.ProcessorMetadata)
	}
}

func TestParseWorkerEnrichmentSuppressesLegacyFieldsForMalformedEnvelope(t *testing.T) {
	enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
		Summary:      "injected summary",
		Caption:      "injected caption",
		Tags:         []string{"injected"},
		MetadataJson: []byte(`{`),
		Processor:    "image",
		Status:       workerpb.ProcessStatus_STATUS_OK,
	})
	if len(enrichment.Annotations) != 0 ||
		enrichment.CaptionSet ||
		enrichment.Caption != nil ||
		!enrichment.Partial {
		t.Fatalf("enrichment = %#v", enrichment)
	}
	if strings.Contains(string(enrichment.ProcessorMetadata), "injected") ||
		!strings.Contains(
			string(enrichment.ProcessorMetadata),
			`"legacy_fields_suppressed":true`,
		) {
		t.Fatalf("processor metadata = %s", enrichment.ProcessorMetadata)
	}
}

func TestParseWorkerEnrichmentRejectsInvalidProcessorTimeWithoutRawLeak(t *testing.T) {
	enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
		MetadataJson: []byte(
			`{"timeline_at":"401 Unauthorized: token secret","annotations":[]}`,
		),
		Processor: "image",
		Status:    workerpb.ProcessStatus_STATUS_OK,
	})
	if !enrichment.Partial || enrichment.Timeline != nil {
		t.Fatalf("enrichment = %#v", enrichment)
	}
	if strings.Contains(string(enrichment.ProcessorMetadata), "Unauthorized") ||
		!strings.Contains(
			string(enrichment.ProcessorMetadata),
			`"invalid_metadata_fields":["timeline_at"]`,
		) {
		t.Fatalf("processor metadata = %s", enrichment.ProcessorMetadata)
	}
}

func TestParseWorkerEnrichmentTreatsUnknownWorkerStatusAsPartial(t *testing.T) {
	enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
		MetadataJson: []byte(`{"annotations":[]}`),
		Processor:    "image",
		Status:       workerpb.ProcessStatus_STATUS_UNSPECIFIED,
	})
	if !enrichment.Partial ||
		!strings.Contains(string(enrichment.ProcessorMetadata), `"status":"partial"`) {
		t.Fatalf("enrichment = %#v", enrichment)
	}
}

func TestParseWorkerEnrichmentConvertsLegacyWorkerOutputToPendingSuggestions(t *testing.T) {
	response := &workerpb.ProcessResponse{
		Summary:   "Short description",
		Caption:   "Short description",
		Tags:      []string{"Travel", "Travel", "Shanghai"},
		Processor: "text",
		Status:    workerpb.ProcessStatus_STATUS_OK,
	}
	enrichment := parseWorkerEnrichment(response)
	if len(enrichment.Annotations) != 3 {
		t.Fatalf("legacy annotations = %#v", enrichment.Annotations)
	}
	if !enrichment.CaptionSet ||
		!enrichment.CaptionFromReview ||
		enrichment.Caption == nil ||
		*enrichment.Caption != "Short description" {
		t.Fatalf("legacy caption projection = %#v", enrichment)
	}
	for _, annotation := range enrichment.Annotations {
		if annotation.Source != "model" ||
			annotation.AnalysisVersion != "legacy-worker-v1" ||
			annotation.Confidence != 0.5 {
			t.Fatalf("legacy annotation = %#v", annotation)
		}
	}
}

func TestParseWorkerEnrichmentDerivesBoundedCaptionFromStructuredDescription(t *testing.T) {
	response := &workerpb.ProcessResponse{
		Caption:   "untrusted mismatched caption",
		Processor: "image",
		Status:    workerpb.ProcessStatus_STATUS_OK,
		MetadataJson: []byte(`{"annotations":[{
			"kind":"description",
			"value":"A reviewed-size description",
			"confidence":0.9,
			"source":"model",
			"provider":"test",
			"processor":"image",
			"analysis_version":"file-enrichment-v1"
		}]}`),
	}
	enrichment := parseWorkerEnrichment(response)
	if !enrichment.CaptionSet ||
		!enrichment.CaptionFromReview ||
		enrichment.Caption == nil ||
		*enrichment.Caption != "A reviewed-size description" ||
		!enrichment.Partial {
		t.Fatalf("enrichment = %#v", enrichment)
	}
	if strings.Contains(
		string(enrichment.ProcessorMetadata),
		"untrusted mismatched caption",
	) || !strings.Contains(
		string(enrichment.ProcessorMetadata),
		`"caption_annotation_mismatch":true`,
	) {
		t.Fatalf("processor metadata = %s", enrichment.ProcessorMetadata)
	}
}

func TestParseWorkerEnrichmentRejectsUnsafeLegacyCaptions(t *testing.T) {
	for _, test := range []struct {
		name    string
		caption string
	}{
		{name: "overlong", caption: strings.Repeat("x", 2001)},
		{name: "control", caption: "visible\nprivate reasoning"},
		{name: "JSON-like", caption: `{"analysis":"private"}`},
		{name: "reasoning wrapper", caption: "<think>private</think>visible"},
		{name: "reasoning prefix", caption: " Analysis: private"},
		{name: "BOM-prefixed JSON-like", caption: "\ufeff{\"analysis\":\"private\"}"},
		{name: "embedded word joiner", caption: "visible\u2060private"},
		{name: "variation selector", caption: "visible\ufe0fprivate"},
		{name: "combining grapheme joiner", caption: "visible\u034fprivate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
				Caption:   test.caption,
				Processor: "image",
				Status:    workerpb.ProcessStatus_STATUS_OK,
			})
			if enrichment.CaptionSet || enrichment.Caption != nil || !enrichment.Partial {
				t.Fatalf("enrichment = %#v", enrichment)
			}
			if strings.Contains(string(enrichment.ProcessorMetadata), test.caption) ||
				!strings.Contains(
					string(enrichment.ProcessorMetadata),
					`"caption_payload_invalid":true`,
				) {
				t.Fatalf("processor metadata = %s", enrichment.ProcessorMetadata)
			}
		})
	}
}

func TestParseWorkerEnrichmentRejectsUnsafeLegacySummary(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		tags    []string
	}{
		{
			name:    "JSON-like summary",
			summary: `{"analysis":"private","answer":"visible"}`,
		},
		{
			name:    "BOM-prefixed JSON-like summary",
			summary: "\ufeff{\"analysis\":\"private\",\"answer\":\"visible\"}",
		},
		{
			name: "zero-width-prefixed tag",
			tags: []string{"\u200b[\"private\"]"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enrichment := parseWorkerEnrichment(&workerpb.ProcessResponse{
				Summary:   test.summary,
				Tags:      test.tags,
				Processor: "text",
				Status:    workerpb.ProcessStatus_STATUS_OK,
			})
			if len(enrichment.Annotations) != 0 {
				t.Fatalf("legacy annotations = %#v", enrichment.Annotations)
			}
			if !enrichment.Partial ||
				!strings.Contains(
					string(enrichment.ProcessorMetadata),
					`"legacy_annotation_payload_invalid":true`,
				) ||
				strings.Contains(string(enrichment.ProcessorMetadata), "private") {
				t.Fatalf("enrichment = %#v", enrichment)
			}
		})
	}
}

func TestAnnotationStableKeyNormalizesCaseAndWhitespace(t *testing.T) {
	first := annotationStableKey("tag", "model", "v1", " New   York ")
	second := annotationStableKey("tag", "model", "v2", "new york")
	if first != second {
		t.Fatalf("stable keys differ: %s != %s", first, second)
	}
}
