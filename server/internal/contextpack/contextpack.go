// Package contextpack builds bounded, source-verifiable evidence packs for
// external agents. It deliberately does not call a chat model: mem retrieves
// memory, while the caller's agent owns reasoning and answer generation.
package contextpack

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/pathx"
	"github.com/PeterGuy326/mem/server/internal/search"
)

const (
	SourceAll    = "all"
	SourceFile   = "file"
	SourceMemory = "memory"
)

// Searcher is the file-retrieval port used by the context builder.
type Searcher interface {
	Search(context.Context, search.Query) ([]search.Hit, error)
}

// MemorySearcher is the structured-memory retrieval port. Keeping this port
// owned by contextpack avoids coupling the evidence contract to persistence.
type MemorySearcher interface {
	Recall(context.Context, MemoryQuery) ([]MemoryHit, error)
}

// MemoryQuery describes one deterministic structured-memory recall.
type MemoryQuery struct {
	WorkspaceID  uuid.UUID
	Query        string
	Scope        string
	AllowedPaths []string
	Kind         string
	Since        *time.Time
	Until        *time.Time
	Limit        int
	SnippetChars int
}

// MemoryProvenance is the source and producer identity retained by remember.
type MemoryProvenance struct {
	SourceType       string          `json:"source_type"`
	SourceRef        string          `json:"source_ref,omitempty"`
	SourceFileID     *uuid.UUID      `json:"source_file_id,omitempty"`
	SourceFileSHA256 string          `json:"source_file_sha256,omitempty"`
	SourceLocator    json.RawMessage `json:"source_locator,omitempty"`
	AgentID          string          `json:"agent_id,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	TaskID           string          `json:"task_id,omitempty"`
}

// MemoryHit is one structured-memory candidate returned by MemorySearcher.
type MemoryHit struct {
	MemoryID      uuid.UUID
	Kind          string
	Content       string
	Path          string
	ContentSHA256 string
	Score         float32
	Reason        string
	EventAt       *time.Time
	CreatedAt     time.Time
	Provenance    MemoryProvenance
}

// Service builds agent-ready evidence packs.
type Service struct {
	search   Searcher
	memories MemorySearcher
}

// New constructs a context-pack service. The variadic memory port preserves
// source compatibility for file-only callers while the Memory Plane rolls out.
func New(s Searcher, memories ...MemorySearcher) *Service {
	svc := &Service{search: s}
	if len(memories) > 0 {
		svc.memories = memories[0]
	}
	return svc
}

// Request describes one bounded recall operation.
type Request struct {
	UserID       uuid.UUID
	WorkspaceID  uuid.UUID
	Query        string
	Scope        string
	AllowedPaths []string
	Source       string
	Type         string
	MemoryKind   string
	Since        *time.Time
	Until        *time.Time
	Limit        int
	MaxChars     int
}

// Locator points back to the exact derived representation used for recall.
type Locator struct {
	Kind       string `json:"kind"` // text_chunk | visual_caption | memory_text
	ChunkIndex *int   `json:"chunk_index,omitempty"`
}

// Evidence is one source-verifiable memory fragment. File-only fields remain
// stable and are omitted for structured memories.
type Evidence struct {
	EvidenceID    string            `json:"evidence_id"`
	SourceKind    string            `json:"source_kind"` // file | memory
	SourceID      uuid.UUID         `json:"source_id"`
	Citation      string            `json:"citation"`
	FileID        *uuid.UUID        `json:"file_id,omitempty"`
	MemoryID      *uuid.UUID        `json:"memory_id,omitempty"`
	MemoryKind    string            `json:"memory_kind,omitempty"`
	Name          string            `json:"name,omitempty"`
	Path          string            `json:"path"`
	MIME          string            `json:"mime,omitempty"`
	ContentSHA256 string            `json:"content_sha256"`
	ContentURL    string            `json:"content_url"`
	Locator       Locator           `json:"locator"`
	Excerpt       string            `json:"excerpt"`
	Score         float32           `json:"score"`
	Route         string            `json:"route"`
	Reason        string            `json:"reason,omitempty"`
	Provenance    *MemoryProvenance `json:"provenance,omitempty"`
	TimelineAt    *time.Time        `json:"timeline_at,omitempty"`
}

// Warning makes a partial retrieval lane explicit to Agent callers.
type Warning struct {
	Source  string `json:"source"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Pack is the stable response contract consumed by API, CLI, and MCP.
type Pack struct {
	Query      string     `json:"query"`
	Scope      string     `json:"scope"`
	Source     string     `json:"source"`
	Evidence   []Evidence `json:"evidence"`
	TotalChars int        `json:"total_chars"`
	Partial    bool       `json:"partial"`
	Warnings   []Warning  `json:"warnings,omitempty"`
	Retrieved  time.Time  `json:"retrieved_at"`
}

type fileResult struct {
	hits []search.Hit
	err  error
}

type memoryResult struct {
	hits []MemoryHit
	err  error
}

// Build retrieves and bounds evidence without synthesizing an answer.
func (s *Service) Build(ctx context.Context, req Request) (*Pack, error) {
	if s == nil {
		return nil, fmt.Errorf("context retrieval is not configured")
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return nil, fmt.Errorf("query is empty")
	}
	scope, err := pathx.Normalize(req.Scope)
	if err != nil {
		return nil, fmt.Errorf("scope: %w", err)
	}
	allowed, err := normalizePaths(req.AllowedPaths)
	if err != nil {
		return nil, err
	}
	source := strings.ToLower(strings.TrimSpace(req.Source))
	if source == "" {
		source = SourceAll
	}
	if source != SourceAll && source != SourceFile && source != SourceMemory {
		return nil, fmt.Errorf("source must be one of all|file|memory")
	}
	if source == SourceMemory && req.Type != "" {
		return nil, fmt.Errorf("type filters file evidence and cannot be used with source=memory")
	}
	if source == SourceFile && req.MemoryKind != "" {
		return nil, fmt.Errorf("memory_kind cannot be used with source=file")
	}
	if req.Limit <= 0 {
		req.Limit = 8
	}
	if req.Limit > 50 {
		req.Limit = 50
	}
	if req.MaxChars <= 0 {
		req.MaxChars = 12_000
	}
	if req.MaxChars > 100_000 {
		req.MaxChars = 100_000
	}
	perEvidence := req.MaxChars / req.Limit
	if perEvidence < 400 {
		perEvidence = 400
	}
	if perEvidence > 4_000 {
		perEvidence = 4_000
	}

	runFiles := source == SourceFile || (source == SourceAll && s.search != nil)
	// An existing MIME filter retains its historical file-only meaning.
	runMemories := source == SourceMemory ||
		(source == SourceAll && req.Type == "" && s.memories != nil)
	if source == SourceFile && s.search == nil {
		return nil, fmt.Errorf("file retrieval is not configured")
	}
	if source == SourceMemory && s.memories == nil {
		return nil, fmt.Errorf("structured memory retrieval is not configured")
	}
	if runMemories && req.WorkspaceID == uuid.Nil {
		return nil, fmt.Errorf("workspace_id required for structured memory retrieval")
	}
	if !runFiles && !runMemories {
		return nil, fmt.Errorf("no requested retrieval source is configured")
	}

	var files fileResult
	var memories memoryResult
	var wg sync.WaitGroup
	if runFiles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			files.hits, files.err = s.search.Search(ctx, search.Query{
				UserID:       req.UserID,
				Text:         req.Query,
				Route:        search.RouteAuto,
				RequireText:  true,
				Type:         req.Type,
				PathPrefix:   scope,
				AllowedPaths: allowed,
				Since:        req.Since,
				Until:        req.Until,
				Limit:        req.Limit,
				SnippetChars: perEvidence,
			})
		}()
	}
	if runMemories {
		wg.Add(1)
		go func() {
			defer wg.Done()
			memories.hits, memories.err = s.memories.Recall(ctx, MemoryQuery{
				WorkspaceID:  req.WorkspaceID,
				Query:        req.Query,
				Scope:        scope,
				AllowedPaths: allowed,
				Kind:         req.MemoryKind,
				Since:        req.Since,
				Until:        req.Until,
				Limit:        req.Limit,
				SnippetChars: perEvidence,
			})
		}()
	}
	wg.Wait()

	candidates := make([]Evidence, 0, len(files.hits)+len(memories.hits))
	for _, h := range files.hits {
		candidates = append(candidates, fileEvidence(h))
	}
	for _, h := range memories.hits {
		candidates = append(candidates, memoryEvidence(h))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].EvidenceID < candidates[j].EvidenceID
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > req.Limit {
		candidates = candidates[:req.Limit]
	}

	pack := &Pack{
		Query:     req.Query,
		Scope:     scope,
		Source:    source,
		Evidence:  make([]Evidence, 0, len(candidates)),
		Retrieved: time.Now().UTC(),
	}
	if runFiles && files.err != nil {
		pack.Warnings = append(pack.Warnings, Warning{
			Source: SourceFile, Code: "file_retrieval_unavailable",
			Message: "file evidence could not be retrieved",
		})
	}
	if runMemories && memories.err != nil {
		pack.Warnings = append(pack.Warnings, Warning{
			Source: SourceMemory, Code: "memory_retrieval_unavailable",
			Message: "structured memory could not be retrieved",
		})
	}
	pack.Partial = len(pack.Warnings) > 0
	if pack.Partial && len(candidates) == 0 {
		return nil, retrievalError(files.err, memories.err)
	}

	for _, candidate := range candidates {
		remaining := req.MaxChars - pack.TotalChars
		if remaining <= 0 {
			break
		}
		candidate.Excerpt = truncateRunes(candidate.Excerpt, remaining)
		pack.Evidence = append(pack.Evidence, candidate)
		pack.TotalChars += len([]rune(candidate.Excerpt))
	}
	return pack, nil
}

func fileEvidence(h search.Hit) Evidence {
	id := h.FileID
	locator := Locator{Kind: "visual_caption"}
	citation := fmt.Sprintf("mem://files/%s#visual", h.FileID)
	reason := "semantic_visual"
	if h.ChunkIndex >= 0 {
		idx := h.ChunkIndex
		locator = Locator{Kind: "text_chunk", ChunkIndex: &idx}
		citation = fmt.Sprintf("mem://files/%s#chunk=%d", h.FileID, idx)
		reason = "semantic_text"
	}
	return Evidence{
		EvidenceID:    h.EvidenceID,
		SourceKind:    SourceFile,
		SourceID:      h.FileID,
		Citation:      citation,
		FileID:        &id,
		Name:          h.Name,
		Path:          h.Path,
		MIME:          h.MIME,
		ContentSHA256: h.ContentSHA256,
		ContentURL:    fmt.Sprintf("/v1/files/%s/content", h.FileID),
		Locator:       locator,
		Excerpt:       h.Snippet,
		Score:         h.Score,
		Route:         h.Source,
		Reason:        reason,
		TimelineAt:    h.TimelineAt,
	}
}

func memoryEvidence(h MemoryHit) Evidence {
	id := h.MemoryID
	timeline := h.EventAt
	if timeline == nil && !h.CreatedAt.IsZero() {
		created := h.CreatedAt
		timeline = &created
	}
	return Evidence{
		EvidenceID:    "memory:" + h.MemoryID.String(),
		SourceKind:    SourceMemory,
		SourceID:      h.MemoryID,
		Citation:      "mem://memories/" + h.MemoryID.String(),
		MemoryID:      &id,
		MemoryKind:    h.Kind,
		Path:          h.Path,
		ContentSHA256: h.ContentSHA256,
		ContentURL:    "/v1/memories/" + h.MemoryID.String(),
		Locator:       Locator{Kind: "memory_text"},
		Excerpt:       h.Content,
		Score:         h.Score,
		Route:         "memory_lexical",
		Reason:        h.Reason,
		Provenance:    &h.Provenance,
		TimelineAt:    timeline,
	}
}

func retrievalError(fileErr, memoryErr error) error {
	switch {
	case fileErr != nil && memoryErr != nil:
		return fmt.Errorf("all requested retrieval sources failed: file=%v; memory=%v", fileErr, memoryErr)
	case fileErr != nil:
		return fmt.Errorf("file retrieval failed and no other evidence was available: %w", fileErr)
	case memoryErr != nil:
		return fmt.Errorf("memory retrieval failed and no other evidence was available: %w", memoryErr)
	default:
		return fmt.Errorf("context retrieval failed")
	}
}

func normalizePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, raw := range paths {
		if raw == "" {
			return nil, fmt.Errorf("allowed path is empty")
		}
		p, err := pathx.Normalize(raw)
		if err != nil {
			return nil, fmt.Errorf("allowed path %q: %w", raw, err)
		}
		if p == pathx.Root {
			return []string{pathx.Root}, nil
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= limit {
		return s
	}
	return string(rs[:limit])
}
