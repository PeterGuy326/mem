package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// writeJSON serializes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeError emits {"error": code, "hint": hint} per SPEC §8.2 Agent-friendly
// convention.
func writeError(w http.ResponseWriter, status int, code, hint string) {
	writeJSON(w, status, map[string]any{
		"error": code,
		"hint":  hint,
	})
}

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// copyTo is a thin wrapper around io.Copy for readability at the call site.
func copyTo(dst io.Writer, src io.Reader) (int64, error) { return io.Copy(dst, src) }
