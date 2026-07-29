// Package modeltext validates untrusted model-generated display text at
// persistence and portability boundaries.
package modeltext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

var hiddenReasoningMarkers = []string{
	"<analysis",
	"</analysis",
	"<think",
	"</think",
	"<reasoning",
	"</reasoning",
	"[analysis]",
	"[reasoning]",
}

// ContainsHiddenReasoning detects the explicit reasoning wrappers rejected by
// the Worker contract. It deliberately operates on display text, not provider
// or processor identifiers.
func ContainsHiddenReasoning(value string) bool {
	lowered := strings.ToLower(value)
	for _, marker := range hiddenReasoningMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	trimmed := strings.TrimLeftFunc(lowered, unicode.IsSpace)
	return strings.HasPrefix(trimmed, "analysis:") ||
		strings.HasPrefix(trimmed, "reasoning:")
}

// Valid reports whether model-generated text is safe to persist as a bounded
// display value. Empty text is permitted only when allowEmpty is true.
func Valid(value string, maxRunes int, allowEmpty bool) bool {
	if maxRunes < 0 ||
		!utf8.ValidString(value) ||
		ContainsHiddenReasoning(value) ||
		ContainsNonDisplay(value) {
		return false
	}
	candidate := strings.TrimSpace(value)
	if utf8.RuneCountInString(candidate) > maxRunes {
		return false
	}
	return allowEmpty || candidate != ""
}

// ContainsNonDisplay reports whether value contains a control, format, or
// default-ignorable rune that must not cross a display-text boundary.
func ContainsNonDisplay(value string) bool {
	return strings.IndexFunc(value, isNonDisplayRune) >= 0
}

func isNonDisplayRune(value rune) bool {
	return unicode.IsControl(value) ||
		unicode.Is(unicode.Cf, value) ||
		unicode.Is(unicode.Variation_Selector, value) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, value)
}

// NormalizePlain validates and trims a legacy plain-text model response.
// JSON-like or fenced output is rejected because it may be a malformed
// structured response containing fields that must never become display text.
func NormalizePlain(value string, maxRunes int) (string, bool) {
	// Length applies to the normalized value that persistence receives. Raw
	// controls, default-ignorables, and reasoning markers still fail closed
	// even when TrimSpace would otherwise remove or obscure them.
	if !Valid(value, maxRunes, true) {
		return "", false
	}
	candidate := strings.TrimSpace(value)
	if candidate == "" ||
		strings.HasPrefix(candidate, "{") ||
		strings.HasPrefix(candidate, "[") ||
		strings.HasPrefix(candidate, `"`) ||
		strings.HasPrefix(candidate, "```") {
		return "", false
	}
	return candidate, true
}
