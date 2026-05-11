// Package pathx centralises mem's virtual-path rules.
//
// Path rules (SPEC §6.3):
//   - Always absolute, starting with "/"
//   - No trailing "/" (except the root itself, which is exactly "/")
//   - Segments may not contain "/", NUL ("\x00"), be empty, "." or ".."
//   - Case-sensitive (to avoid cross-OS ambiguity)
//
// Both the folder and file packages depend on this; nothing here may import
// from those packages.
package pathx

import (
	"errors"
	"fmt"
	"strings"
)

// Root is the canonical root path.
const Root = "/"

// Errors returned by validation helpers.
var (
	ErrEmpty       = errors.New("path is empty")
	ErrNotAbsolute = errors.New("path must be absolute (start with '/')")
	ErrBadSegment  = errors.New("path segment is invalid")
	ErrBadName     = errors.New("name is invalid")
)

// Normalize takes a user-supplied folder path and returns the canonical form.
//
// Rules:
//   - "" → "/"
//   - duplicate slashes collapsed
//   - trailing slash stripped (root preserved as "/")
//   - each segment validated via ValidateName
//
// Returns an error if any segment is illegal.
func Normalize(p string) (string, error) {
	if p == "" {
		return Root, nil
	}
	if !strings.HasPrefix(p, "/") {
		return "", ErrNotAbsolute
	}
	// strip trailing slashes, but allow root
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	if p == "/" {
		return Root, nil
	}
	parts := strings.Split(p[1:], "/")
	out := make([]string, 0, len(parts))
	for _, seg := range parts {
		if seg == "" {
			// collapse "//"
			continue
		}
		if err := ValidateName(seg); err != nil {
			return "", fmt.Errorf("segment %q: %w", seg, err)
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return Root, nil
	}
	return "/" + strings.Join(out, "/"), nil
}

// ValidateName checks that a single path segment is legal.
//
// Forbidden: empty, ".", "..", anything containing "/" or NUL.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrBadName)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: %q is reserved", ErrBadName, name)
	}
	if strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("%w: contains '/' or NUL", ErrBadName)
	}
	return nil
}

// IsRoot reports whether p is the canonical root path.
func IsRoot(p string) bool { return p == Root }

// Parent returns the parent path of p, or Root if p is a top-level entry
// (or already root).
func Parent(p string) string {
	if p == Root || p == "" {
		return Root
	}
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return Root
	}
	return p[:idx]
}

// Base returns the last segment of p ("" if p is root).
func Base(p string) string {
	if p == Root || p == "" {
		return ""
	}
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return p
	}
	return p[idx+1:]
}

// Join concatenates a parent directory path and a child name, returning the
// normalized result. Errors propagate from ValidateName / Normalize.
func Join(parent, name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	parent, err := Normalize(parent)
	if err != nil {
		return "", err
	}
	if parent == Root {
		return Root + name, nil
	}
	return parent + "/" + name, nil
}

// Ancestors returns every prefix of p excluding root, ordered from top to
// bottom. Useful for mkdir -p style folder creation.
//
//	Ancestors("/a/b/c") -> ["/a", "/a/b", "/a/b/c"]
//	Ancestors("/")      -> nil
func Ancestors(p string) []string {
	if p == Root || p == "" {
		return nil
	}
	parts := strings.Split(p[1:], "/")
	out := make([]string, 0, len(parts))
	cur := ""
	for _, seg := range parts {
		cur = cur + "/" + seg
		out = append(out, cur)
	}
	return out
}

// IsDescendantOrSelf reports whether candidate equals ancestor or sits below
// it in the tree (ancestor="/a" matches "/a", "/a/b", "/a/b/c" but not
// "/abc").
func IsDescendantOrSelf(candidate, ancestor string) bool {
	if ancestor == Root {
		return true
	}
	if candidate == ancestor {
		return true
	}
	return strings.HasPrefix(candidate, ancestor+"/")
}

// ReplacePrefix rewrites the path prefix from oldPrefix to newPrefix when p is
// equal to oldPrefix or strictly under it. Returns p unchanged otherwise.
//
//	ReplacePrefix("/a/b/c", "/a", "/x") -> "/x/b/c"
//	ReplacePrefix("/a",     "/a", "/x") -> "/x"
//	ReplacePrefix("/ab",    "/a", "/x") -> "/ab"
func ReplacePrefix(p, oldPrefix, newPrefix string) string {
	if p == oldPrefix {
		return newPrefix
	}
	if !strings.HasPrefix(p, oldPrefix+"/") {
		return p
	}
	return newPrefix + p[len(oldPrefix):]
}
