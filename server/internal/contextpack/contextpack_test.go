package contextpack

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/search"
)

type fakeSearcher struct {
	query search.Query
	hits  []search.Hit
	err   error
}

func (f *fakeSearcher) Search(_ context.Context, q search.Query) ([]search.Hit, error) {
	f.query = q
	return f.hits, f.err
}

type fakeMemorySearcher struct {
	query MemoryQuery
	hits  []MemoryHit
	err   error
}

func (f *fakeMemorySearcher) Recall(_ context.Context, q MemoryQuery) ([]MemoryHit, error) {
	f.query = q
	return f.hits, f.err
}

func TestBuildReturnsBoundedVerifiableEvidence(t *testing.T) {
	fileID := uuid.New()
	fs := &fakeSearcher{hits: []search.Hit{{
		EvidenceID:    "chunk-1",
		FileID:        fileID,
		Name:          "notes.md",
		Path:          "/Work",
		MIME:          "text/markdown",
		ContentSHA256: "abc123",
		ChunkIndex:    2,
		Score:         0.91,
		Snippet:       "这是一段可以被外部 Agent 使用的证据",
		Source:        search.RouteText,
	}}}

	pack, err := New(fs).Build(context.Background(), Request{
		UserID:       uuid.New(),
		Query:        " 项目决定 ",
		Scope:        "/Work/",
		AllowedPaths: []string{"/Work"},
		Limit:        5,
		MaxChars:     12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fs.query.PathPrefix != "/Work" || len(fs.query.AllowedPaths) != 1 {
		t.Fatalf("path constraints not propagated: %+v", fs.query)
	}
	if len(pack.Evidence) != 1 {
		t.Fatalf("evidence count = %d", len(pack.Evidence))
	}
	ev := pack.Evidence[0]
	if ev.Locator.ChunkIndex == nil || *ev.Locator.ChunkIndex != 2 {
		t.Fatalf("locator = %+v", ev.Locator)
	}
	if ev.Citation != "mem://files/"+fileID.String()+"#chunk=2" {
		t.Fatalf("citation = %q", ev.Citation)
	}
	if pack.TotalChars > 12 {
		t.Fatalf("char budget exceeded: %d", pack.TotalChars)
	}
}

func TestBuildVisualEvidenceUsesWholeFileLocator(t *testing.T) {
	fileID := uuid.New()
	fs := &fakeSearcher{hits: []search.Hit{{
		EvidenceID: "visual:" + fileID.String(),
		FileID:     fileID,
		ChunkIndex: -1,
		Snippet:    "一只草地上的金毛",
		Source:     search.RouteVisual,
	}}}
	pack, err := New(fs).Build(context.Background(), Request{
		UserID: uuid.New(), Query: "金毛",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := pack.Evidence[0]
	if ev.Locator.Kind != "visual_caption" || ev.Locator.ChunkIndex != nil {
		t.Fatalf("locator = %+v", ev.Locator)
	}
}

func TestBuildIncludesStructuredMemoryWithoutFileModel(t *testing.T) {
	memoryID := uuid.New()
	workspaceID := uuid.New()
	ms := &fakeMemorySearcher{hits: []MemoryHit{{
		MemoryID:      memoryID,
		Kind:          "decision",
		Content:       "mem returns evidence; the external Agent owns the answer.",
		Path:          "/Projects/mem",
		ContentSHA256: "memory-sha",
		Score:         1,
		Reason:        "exact_phrase",
		Provenance: MemoryProvenance{
			SourceType: "agent",
			AgentID:    "codex",
			SessionID:  "session-1",
		},
	}}}

	pack, err := New(nil, ms).Build(context.Background(), Request{
		WorkspaceID:  workspaceID,
		Query:        "external Agent owns the answer",
		Scope:        "/Projects/mem",
		AllowedPaths: []string{"/Projects"},
		Source:       SourceMemory,
		MemoryKind:   "decision",
		MaxChars:     1_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ms.query.WorkspaceID != workspaceID || ms.query.Kind != "decision" {
		t.Fatalf("memory query not propagated: %+v", ms.query)
	}
	if len(pack.Evidence) != 1 {
		t.Fatalf("evidence count = %d", len(pack.Evidence))
	}
	ev := pack.Evidence[0]
	if ev.SourceKind != SourceMemory || ev.MemoryID == nil || *ev.MemoryID != memoryID {
		t.Fatalf("memory identity = %+v", ev)
	}
	if ev.FileID != nil {
		t.Fatalf("memory evidence leaked file_id: %+v", ev.FileID)
	}
	if ev.Citation != "mem://memories/"+memoryID.String() {
		t.Fatalf("citation = %q", ev.Citation)
	}
	if ev.Locator.Kind != "memory_text" || ev.Route != "memory_lexical" {
		t.Fatalf("memory locator/route = %+v / %q", ev.Locator, ev.Route)
	}
	if pack.Partial || len(pack.Warnings) != 0 {
		t.Fatalf("unexpected partial pack: %+v", pack)
	}
}

func TestBuildReportsPartialFileFailureWhenMemoryEvidenceExists(t *testing.T) {
	fs := &fakeSearcher{err: errors.New("worker offline")}
	ms := &fakeMemorySearcher{hits: []MemoryHit{{
		MemoryID: uuid.New(), Kind: "preference", Content: "周报默认使用中文",
		Path: "/", Score: 0.8,
	}}}
	pack, err := New(fs, ms).Build(context.Background(), Request{
		UserID: uuid.New(), WorkspaceID: uuid.New(), Query: "周报语言",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !pack.Partial || len(pack.Warnings) != 1 {
		t.Fatalf("partial metadata = %+v", pack)
	}
	if pack.Warnings[0].Source != SourceFile {
		t.Fatalf("warning = %+v", pack.Warnings[0])
	}
	if len(pack.Evidence) != 1 || pack.Evidence[0].SourceKind != SourceMemory {
		t.Fatalf("evidence = %+v", pack.Evidence)
	}
}

func TestBuildFailsClosedWhenOnlyAvailableLaneFails(t *testing.T) {
	_, err := New(&fakeSearcher{err: errors.New("worker offline")}).Build(
		context.Background(),
		Request{UserID: uuid.New(), Query: "anything", Source: SourceFile},
	)
	if err == nil {
		t.Fatal("expected file retrieval failure")
	}
}

func TestBuildTypeFilterPreservesFileOnlySemantics(t *testing.T) {
	fs := &fakeSearcher{}
	ms := &fakeMemorySearcher{hits: []MemoryHit{{
		MemoryID: uuid.New(), Kind: "note", Content: "must not be called",
	}}}
	_, err := New(fs, ms).Build(context.Background(), Request{
		UserID: uuid.New(), WorkspaceID: uuid.New(), Query: "photo", Type: "image",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ms.query.WorkspaceID != uuid.Nil {
		t.Fatalf("memory lane must not run for a MIME-filtered request: %+v", ms.query)
	}
}
