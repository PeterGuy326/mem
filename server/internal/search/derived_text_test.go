package search

import "testing"

func TestSanitizeDerivedDisplayTextRejectsUnsafeSummaryAndVisualCaption(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		caption string
	}{
		{
			name:    "format controls",
			summary: "\ufeff{\"analysis\":\"private\",\"answer\":\"public\"}",
			caption: "\u200b[\"private\"]",
		},
		{
			name:    "default ignorables",
			summary: "\ufe0f{\"analysis\":\"private\",\"answer\":\"public\"}",
			caption: "\u034f[\"private\"]",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hit := Hit{
				Summary: &test.summary,
				Snippet: test.caption,
			}
			sanitizeDerivedDisplayText(&hit, RouteVisual)
			if hit.Summary != nil || hit.Snippet != "" {
				t.Fatalf(
					"unsafe derived text survived: summary=%v snippet=%q",
					hit.Summary,
					hit.Snippet,
				)
			}
		})
	}
}

func TestSanitizeDerivedDisplayTextPreservesSourceSnippetAndNormalizesSafeSummary(t *testing.T) {
	summary := "  Safe summary.  "
	hit := Hit{
		Summary: &summary,
		Snippet: "\ufeffsource document evidence",
	}

	sanitizeDerivedDisplayText(&hit, RouteText)

	if hit.Summary == nil || *hit.Summary != "Safe summary." {
		t.Fatalf("summary = %v", hit.Summary)
	}
	if hit.Snippet != "\ufeffsource document evidence" {
		t.Fatalf("source snippet = %q", hit.Snippet)
	}
}
