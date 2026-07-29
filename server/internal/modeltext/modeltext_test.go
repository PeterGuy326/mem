package modeltext

import (
	"strings"
	"testing"
	"unicode"
)

func TestNormalizePlainRejectsUnsafeModelOutput(t *testing.T) {
	for _, value := range []string{
		"",
		"   ",
		`{"analysis":"private"}`,
		"[reasoning]",
		"```json",
		"<think>private</think>visible",
		`<reasoning visibility="hidden">private</reasoning>visible`,
		"visible</reasoning>",
		" ANALYSIS: private",
		"visible\nprivate",
		"\ufeff{\"analysis\":\"private\",\"answer\":\"public\"}",
		"\u200b[\"private\"]",
		"visible\u2060private",
		"visible\U00013439private",
		"\ufe0f{\"analysis\":\"private\",\"answer\":\"public\"}",
		"\u034f[\"private\"]",
		strings.Repeat("x", 2001),
	} {
		if normalized, ok := NormalizePlain(value, 2000); ok {
			t.Fatalf("NormalizePlain(%q) = %q, want rejection", value, normalized)
		}
	}
	if normalized, ok := NormalizePlain("  A plain observation.  ", 2000); !ok ||
		normalized != "A plain observation." {
		t.Fatalf("NormalizePlain safe = %q, %t", normalized, ok)
	}

	boundary := strings.Repeat("x", 2000)
	if !Valid(" \u00a0"+boundary+"\u3000 ", 2000, false) {
		t.Fatal("Valid rejected a trimmed value at the persistence boundary")
	}
	if normalized, ok := NormalizePlain(" \u00a0"+boundary+"\u3000 ", 2000); !ok ||
		normalized != boundary {
		t.Fatalf("NormalizePlain trimmed boundary length = %d, %t", len([]rune(normalized)), ok)
	}
	if normalized, ok := NormalizePlain(" "+boundary+"x ", 2000); ok {
		t.Fatalf("NormalizePlain overlong trimmed candidate = %q, want rejection", normalized)
	}
	if normalized, ok := NormalizePlain("\n"+boundary, 2000); ok {
		t.Fatalf("NormalizePlain raw control prefix = %q, want rejection", normalized)
	}
}

func TestValidRejectsReasoningAndFormatCharactersButAllowsStructuredLookingValues(t *testing.T) {
	for _, value := range []string{
		"<ANALYSIS>private</ANALYSIS>",
		`<ReAsOnInG visibility="hidden">private`,
		"visible</ReAsOnInG>",
		"visible\u200btext",
		"visible\ufefftext",
		"visible\u2060text",
		"visible\U00013439text",
		"visible\ufe0ftext",
		"visible\u034ftext",
	} {
		if Valid(value, 2000, false) {
			t.Fatalf("unsafe display value %q accepted", value)
		}
	}
	if !Valid(`{"visible":"literal description"}`, 2000, false) {
		t.Fatal("bounded structured-looking annotation value rejected")
	}
}

func TestValidRejectsEveryUnicodeNonDisplayPropertyRune(t *testing.T) {
	tested := 0
	for value := rune(0); value <= unicode.MaxRune; value++ {
		if !unicode.Is(unicode.Cf, value) &&
			!unicode.Is(unicode.Variation_Selector, value) &&
			!unicode.Is(unicode.Other_Default_Ignorable_Code_Point, value) {
			continue
		}
		tested++
		if Valid("visible"+string(value)+"text", 2000, false) {
			t.Fatalf("Unicode non-display rune U+%04X accepted", value)
		}
	}
	if tested != 4206 {
		t.Fatalf("tested %d Unicode non-display runes, want pinned Unicode 15 count 4206", tested)
	}
}
