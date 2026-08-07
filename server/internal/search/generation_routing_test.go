package search

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type stubGenerationResolver struct {
	gen *ActiveGeneration
	err error
}

func (s *stubGenerationResolver) ActiveForOwner(_ context.Context, _ uuid.UUID, _ string) (*ActiveGeneration, error) {
	return s.gen, s.err
}

func TestResolveActiveGenerationNilResolver(t *testing.T) {
	s := &Service{}
	got := s.resolveActiveGeneration(context.Background(), uuid.New(), RouteText)
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestResolveActiveGenerationReturnsResult(t *testing.T) {
	genID := uuid.New()
	s := &Service{
		generations: &stubGenerationResolver{
			gen: &ActiveGeneration{GenerationID: genID, RouteKind: RouteText},
		},
	}
	got := s.resolveActiveGeneration(context.Background(), uuid.New(), RouteText)
	if got == nil || got.GenerationID != genID {
		t.Fatalf("expected generation %s, got %+v", genID, got)
	}
}

func TestResolveActiveGenerationReturnsNilOnError(t *testing.T) {
	s := &Service{
		generations: &stubGenerationResolver{err: errors.New("db down")},
	}
	got := s.resolveActiveGeneration(context.Background(), uuid.New(), RouteText)
	if got != nil {
		t.Fatalf("expected nil on error, got %+v", got)
	}
}

func TestResolveActiveGenerationNoActiveGeneration(t *testing.T) {
	s := &Service{
		generations: &stubGenerationResolver{gen: nil, err: nil},
	}
	got := s.resolveActiveGeneration(context.Background(), uuid.New(), RouteVisual)
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestSetGenerationResolverOnNilService(t *testing.T) {
	var s *Service
	// Should not panic
	s.SetGenerationResolver(&stubGenerationResolver{})
}
