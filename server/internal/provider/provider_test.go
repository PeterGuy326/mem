package provider

import (
	"errors"
	"strings"
	"testing"
)

func TestValidKindsIncludesVisualEmbedding(t *testing.T) {
	if !validKind(KindVisualEmbedding) {
		t.Fatal("visual_embedding must be accepted by service and migration")
	}
}

func TestCheckEmbeddingDimension(t *testing.T) {
	if err := checkEmbeddingDimension(768, 768); err != nil {
		t.Fatalf("matching dimension rejected: %v", err)
	}
	err := checkEmbeddingDimension(1024, 768)
	if !errors.Is(err, ErrEmbeddingDimConflict) {
		t.Fatalf("expected conflict sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "1024") || !strings.Contains(err.Error(), "768") {
		t.Fatalf("conflict lacks dimensions: %v", err)
	}
}
