// Package enrichmentkey owns the stable identity algorithm for model
// suggestions. Indexing and untrusted bundle validation must use the same
// implementation so an imported record cannot poison a future reindex.
package enrichmentkey

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// Stable returns the cross-analysis SHA-256 identity for an annotation
// suggestion. Value matching is intentionally case-insensitive and
// whitespace-normalized; user-facing tag projection remains case-sensitive.
// analysisVersion remains an argument so callers make provenance explicit,
// but it is deliberately not part of decision identity: a model upgrade must
// not resurrect a value that a person already accepted or rejected.
func Stable(kind, source, _ string, value string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(value), " "))
	sum := sha256.Sum256([]byte(
		kind + "\x00" + source + "\x00" + normalized,
	))
	return fmt.Sprintf("sha256:%x", sum[:])
}
