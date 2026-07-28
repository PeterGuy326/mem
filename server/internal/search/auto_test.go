package search

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestMergeAutoRejectsFailedTextRouteWithEmptyVisualFallback(t *testing.T) {
	s := &Service{}
	q := Query{Limit: 10}

	_, err := s.mergeAutoResults(
		q,
		autoResult{err: errors.New("legacy:unknown corpus requires reindex")},
		autoResult{hits: nil},
	)
	if err == nil {
		t.Fatal("failed text retrieval plus empty visual fallback must not look like no memory")
	}
}

func TestMergeAutoRequireTextRejectsVisualFallback(t *testing.T) {
	s := &Service{}
	q := Query{Limit: 10, RequireText: true}
	visual := Hit{FileID: uuid.New(), Source: RouteVisual, Score: 0.8}

	_, err := s.mergeAutoResults(
		q,
		autoResult{err: errors.New("text backend unavailable")},
		autoResult{hits: []Hit{visual}},
	)
	if err == nil {
		t.Fatal("agent context must reject a failed primary text route")
	}
}

func TestMergeAutoRequireTextAllowsTextOnlyDeployment(t *testing.T) {
	s := &Service{}
	q := Query{Limit: 10, RequireText: true}
	textHit := Hit{FileID: uuid.New(), Source: RouteText, Score: 0.8}

	hits, err := s.mergeAutoResults(
		q,
		autoResult{hits: []Hit{textHit}},
		autoResult{err: errors.New("CLIP is not installed")},
	)
	if err != nil {
		t.Fatalf("text-only context failed: %v", err)
	}
	if len(hits) != 1 || hits[0].FileID != textHit.FileID {
		t.Fatalf("hits = %#v", hits)
	}
}

func TestMergeAutoInteractiveSearchCanReturnNonEmptyFallback(t *testing.T) {
	s := &Service{}
	q := Query{Limit: 10}
	visual := Hit{FileID: uuid.New(), Source: RouteVisual, Score: 0.8}

	hits, err := s.mergeAutoResults(
		q,
		autoResult{err: errors.New("text backend unavailable")},
		autoResult{hits: []Hit{visual}},
	)
	if err != nil {
		t.Fatalf("interactive fallback failed: %v", err)
	}
	if len(hits) != 1 || hits[0].FileID != visual.FileID {
		t.Fatalf("hits = %#v", hits)
	}
}
