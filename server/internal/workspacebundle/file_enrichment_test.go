package workspacebundle

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDeriveFileEnrichmentProjectionUsesOnlyPortableProvenance(t *testing.T) {
	early := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	record := FileRecord{
		Tags:     []string{"untrusted redundant value"},
		UserTags: []string{"manual", "duplicate", "manual"},
		Summary:  stringPointer("untrusted redundant summary"),
		Caption:  stringPointer("untrusted redundant caption"),
		Annotations: []FileAnnotationRecord{
			{
				ID:   uuid.MustParse("10000000-0000-0000-0000-000000000001"),
				Kind: "tag", ValueText: "reviewed", Status: "accepted",
				DecidedAt: &early, CreatedAt: early,
			},
			{
				ID:   uuid.MustParse("10000000-0000-0000-0000-000000000002"),
				Kind: "tag", ValueText: "manual", Status: "accepted",
				DecidedAt: &late, CreatedAt: late,
			},
			{
				ID:   uuid.MustParse("10000000-0000-0000-0000-000000000003"),
				Kind: "tag", ValueText: "rejected", Status: "rejected",
				DecidedAt: &late, CreatedAt: late,
			},
			{
				ID:   uuid.MustParse("10000000-0000-0000-0000-000000000004"),
				Kind: "description", ValueText: "older", Confidence: 0.99,
				Status: "accepted", DecidedAt: &early, CreatedAt: early,
			},
			{
				ID:   uuid.MustParse("10000000-0000-0000-0000-000000000005"),
				Kind: "description", ValueText: "newer", Confidence: 0.4,
				Status: "accepted", DecidedAt: &late, CreatedAt: late,
			},
		},
	}

	projection := DeriveFileEnrichmentProjection(record)

	if projection.Legacy {
		t.Fatal("enriched record classified as legacy")
	}
	if !slices.Equal(projection.UserTags, []string{"manual", "duplicate"}) {
		t.Fatalf("user tags = %v", projection.UserTags)
	}
	if !slices.Equal(projection.Tags, []string{"manual", "duplicate", "reviewed"}) {
		t.Fatalf("effective tags = %v", projection.Tags)
	}
	if projection.Summary == nil || *projection.Summary != "newer" {
		t.Fatalf("summary = %v", projection.Summary)
	}
	if projection.Caption == nil || *projection.Caption != "newer" {
		t.Fatalf("caption = %v", projection.Caption)
	}
}

func TestDeriveLegacyFileEnrichmentPromotesTagsAndDropsSummary(t *testing.T) {
	record := FileRecord{
		Tags:    []string{"legacy", "legacy"},
		Summary: stringPointer("unreviewed legacy processor summary"),
		Caption: stringPointer("legacy visual caption"),
	}

	projection := DeriveFileEnrichmentProjection(record)

	if !projection.Legacy {
		t.Fatal("legacy record was not detected")
	}
	if !slices.Equal(projection.UserTags, []string{"legacy", "legacy"}) ||
		!slices.Equal(projection.Tags, []string{"legacy", "legacy"}) {
		t.Fatalf("projection = %+v", projection)
	}
	if projection.Summary != nil {
		t.Fatalf("legacy summary was trusted: %q", *projection.Summary)
	}
	if projection.Caption == nil || *projection.Caption != "legacy visual caption" {
		t.Fatalf("legacy caption = %v", projection.Caption)
	}
}

func TestDeriveFileEnrichmentCaptionExcludesRejectedDescriptions(t *testing.T) {
	early := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	record := FileRecord{
		UserTags: []string{},
		Caption:  stringPointer("rejected redundant caption"),
		Annotations: []FileAnnotationRecord{
			{
				ID:   uuid.MustParse("10000000-0000-0000-0000-000000000010"),
				Kind: "description", ValueText: "rejected", Confidence: 0.99,
				Status: "rejected", UpdatedAt: late, CreatedAt: late,
			},
			{
				ID:   uuid.MustParse("10000000-0000-0000-0000-000000000011"),
				Kind: "description", ValueText: "pending", Confidence: 0.4,
				Status: "pending", UpdatedAt: early, CreatedAt: early,
			},
		},
	}

	projection := DeriveFileEnrichmentProjection(record)

	if projection.Caption == nil || *projection.Caption != "pending" {
		t.Fatalf("caption = %v, want pending description", projection.Caption)
	}
}

func stringPointer(value string) *string {
	return &value
}
