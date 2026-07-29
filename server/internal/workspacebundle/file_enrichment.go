package workspacebundle

import (
	"sort"
	"time"
)

// FileEnrichmentProjection is the only portable, searchable projection that
// can be derived from explicit user tags and accepted model suggestions.
// Legacy identifies pre-enrichment v1 records whose tags must be promoted to
// user provenance during import; their unreviewed summary is never projected.
type FileEnrichmentProjection struct {
	UserTags []string
	Tags     []string
	Summary  *string
	Caption  *string
	Legacy   bool
}

// DeriveFileEnrichmentProjection reconstructs effective file values without
// trusting the redundant tags/summary fields in an imported archive.
func DeriveFileEnrichmentProjection(record FileRecord) FileEnrichmentProjection {
	legacy := record.UserTags == nil && record.Annotations == nil
	userTags := record.UserTags
	if legacy {
		legacyTags := append([]string{}, record.Tags...)
		return FileEnrichmentProjection{
			UserTags: legacyTags,
			Tags:     append([]string{}, legacyTags...),
			Caption:  record.Caption,
			Legacy:   true,
		}
	}
	userTags = mergePortableTags(userTags)

	acceptedTags := make([]FileAnnotationRecord, 0)
	acceptedDescriptions := make([]FileAnnotationRecord, 0)
	for _, annotation := range record.Annotations {
		if annotation.Status != "accepted" {
			continue
		}
		switch annotation.Kind {
		case "tag":
			acceptedTags = append(acceptedTags, annotation)
		case "description":
			acceptedDescriptions = append(acceptedDescriptions, annotation)
		}
	}
	sort.Slice(acceptedTags, func(i, j int) bool {
		left, right := acceptedTags[i], acceptedTags[j]
		if comparison := annotationDecisionTime(left).Compare(annotationDecisionTime(right)); comparison != 0 {
			return comparison < 0
		}
		if comparison := left.CreatedAt.Compare(right.CreatedAt); comparison != 0 {
			return comparison < 0
		}
		return left.ID.String() < right.ID.String()
	})
	effectiveTags := append([]string{}, userTags...)
	for _, annotation := range acceptedTags {
		effectiveTags = appendUniquePortableTag(effectiveTags, annotation.ValueText)
	}

	sort.Slice(acceptedDescriptions, func(i, j int) bool {
		left, right := acceptedDescriptions[i], acceptedDescriptions[j]
		if comparison := annotationDecisionTime(left).Compare(annotationDecisionTime(right)); comparison != 0 {
			return comparison > 0
		}
		if left.Confidence != right.Confidence {
			return left.Confidence > right.Confidence
		}
		if comparison := left.CreatedAt.Compare(right.CreatedAt); comparison != 0 {
			return comparison > 0
		}
		return left.ID.String() > right.ID.String()
	})
	var summary *string
	if len(acceptedDescriptions) > 0 {
		value := acceptedDescriptions[0].ValueText
		summary = &value
	}
	caption := summary
	if caption == nil {
		pendingDescriptions := make([]FileAnnotationRecord, 0)
		for _, annotation := range record.Annotations {
			if annotation.Kind == "description" && annotation.Status == "pending" {
				pendingDescriptions = append(pendingDescriptions, annotation)
			}
		}
		sort.Slice(pendingDescriptions, func(i, j int) bool {
			left, right := pendingDescriptions[i], pendingDescriptions[j]
			if comparison := left.UpdatedAt.Compare(right.UpdatedAt); comparison != 0 {
				return comparison > 0
			}
			if left.Confidence != right.Confidence {
				return left.Confidence > right.Confidence
			}
			if comparison := left.CreatedAt.Compare(right.CreatedAt); comparison != 0 {
				return comparison > 0
			}
			return left.ID.String() > right.ID.String()
		})
		if len(pendingDescriptions) > 0 {
			value := pendingDescriptions[0].ValueText
			caption = &value
		}
	}
	return FileEnrichmentProjection{
		UserTags: userTags,
		Tags:     effectiveTags,
		Summary:  summary,
		Caption:  caption,
		Legacy:   false,
	}
}

func annotationDecisionTime(annotation FileAnnotationRecord) time.Time {
	if annotation.DecidedAt != nil {
		return *annotation.DecidedAt
	}
	return annotation.UpdatedAt
}

func mergePortableTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		out = appendUniquePortableTag(out, tag)
	}
	return out
}

func appendUniquePortableTag(tags []string, value string) []string {
	for _, existing := range tags {
		if existing == value {
			return tags
		}
	}
	return append(tags, value)
}
