package main

import (
	"context"

	"github.com/PeterGuy326/mem/server/internal/contextpack"
	"github.com/PeterGuy326/mem/server/internal/memory"
)

// memoryRecallAdapter keeps contextpack's retrieval port independent from the
// PostgreSQL domain service while preserving the full provenance projection.
type memoryRecallAdapter struct {
	service *memory.Service
}

func (a *memoryRecallAdapter) Recall(
	ctx context.Context,
	q contextpack.MemoryQuery,
) ([]contextpack.MemoryHit, error) {
	kinds := []string(nil)
	if q.Kind != "" {
		kinds = []string{q.Kind}
	}
	hits, err := a.service.Recall(ctx, memory.RecallQuery{
		WorkspaceID:     q.WorkspaceID,
		Text:            q.Query,
		Scope:           q.Scope,
		AllowedPaths:    q.AllowedPaths,
		Since:           q.Since,
		Until:           q.Until,
		Kinds:           kinds,
		LifecycleStatus: memory.StatusActive,
		Limit:           q.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]contextpack.MemoryHit, 0, len(hits))
	for _, hit := range hits {
		p := hit.Provenance
		out = append(out, contextpack.MemoryHit{
			MemoryID:      hit.Memory.ID,
			Kind:          hit.Memory.Kind,
			Content:       hit.Memory.Content,
			Path:          hit.Memory.Path,
			ContentSHA256: hit.Memory.ContentSHA256,
			Score:         float32(hit.Score),
			Reason:        hit.Reason,
			EventAt:       hit.Memory.EventAt,
			CreatedAt:     hit.Memory.CreatedAt,
			Provenance: contextpack.MemoryProvenance{
				SourceType:       p.SourceType,
				SourceRef:        p.SourceRef,
				SourceFileID:     p.SourceFileID,
				SourceFileSHA256: p.SourceFileSHA256,
				SourceLocator:    append([]byte(nil), p.SourceLocator...),
				AgentID:          p.ProducerAgent,
				SessionID:        p.ProducerSession,
				TaskID:           p.ProducerTask,
			},
		})
	}
	return out, nil
}
