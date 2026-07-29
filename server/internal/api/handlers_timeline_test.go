package api

import "testing"

func TestSanitizeTimelineDerivedTextRejectsFormatControls(t *testing.T) {
	for _, value := range []string{
		"\ufeff{\"analysis\":\"private\",\"answer\":\"public\"}",
		"visible\u200bprivate",
		"visible\u2060private",
		"visible\U0001343fprivate",
		"visible\ufe0fprivate",
		"visible\u034fprivate",
	} {
		value := value
		if got := sanitizeTimelineDerivedText(&value); got != nil {
			t.Fatalf("sanitizeTimelineDerivedText(%q) = %q, want nil", value, *got)
		}
	}
}

func TestSanitizeTimelineDerivedTextNormalizesSafeValue(t *testing.T) {
	value := "  Safe timeline summary.  "
	got := sanitizeTimelineDerivedText(&value)
	if got == nil || *got != "Safe timeline summary." {
		t.Fatalf("sanitizeTimelineDerivedText() = %v", got)
	}
	if sanitizeTimelineDerivedText(nil) != nil {
		t.Fatal("nil value became non-nil")
	}
}
