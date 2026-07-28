package main

import (
	"testing"
)

func TestFormatMarkdown_EmptyNoNote(t *testing.T) {
	resp := relatedResp{
		FileID:  "file-1",
		Related: nil,
	}
	out := formatMarkdown(resp)
	want := "(no related files)\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestFormatMarkdown_EmptyWithNote(t *testing.T) {
	resp := relatedResp{
		FileID:  "file-1",
		Related: nil,
		Note:    "indexing in progress",
	}
	out := formatMarkdown(resp)
	want := "(no related: indexing in progress)\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestFormatMarkdown_SingleHit(t *testing.T) {
	summary := "a test file"
	resp := relatedResp{
		FileID: "file-1",
		Related: []relatedHit{
			{
				FileID:  "file-2",
				Name:    "doc.md",
				Path:    "/docs/doc.md",
				MIME:    "text/markdown",
				Type:    "same_topic",
				Score:   0.9567,
				Summary: &summary,
			},
		},
	}
	out := formatMarkdown(resp)
	want := "| # | Score | Type | Name | Path |\n|---|-------|------|------|------|\n| 1 | 0.957 | same_topic | doc.md | /docs/doc.md |\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestFormatMarkdown_MultipleHits(t *testing.T) {
	s1 := "first file"
	s2 := "second file"
	resp := relatedResp{
		FileID: "file-1",
		Related: []relatedHit{
			{
				FileID:  "file-2",
				Name:    "alpha.txt",
				Path:    "/docs/alpha.txt",
				MIME:    "text/plain",
				Type:    "same_topic",
				Score:   0.9876,
				Summary: &s1,
			},
			{
				FileID:  "file-3",
				Name:    "beta.txt",
				Path:    "/docs/beta.txt",
				MIME:    "text/plain",
				Type:    "same_event",
				Score:   0.6543,
				Summary: &s2,
			},
		},
	}
	out := formatMarkdown(resp)
	want := "| # | Score | Type | Name | Path |\n|---|-------|------|------|------|\n| 1 | 0.988 | same_topic | alpha.txt | /docs/alpha.txt |\n| 2 | 0.654 | same_event | beta.txt | /docs/beta.txt |\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestFormatMarkdown_EscapesTableCells(t *testing.T) {
	resp := relatedResp{
		Related: []relatedHit{{
			Name:  "plan|notes.md",
			Path:  "/docs/line\nbreak",
			Type:  "same_topic",
			Score: 1,
		}},
	}
	out := formatMarkdown(resp)
	want := "| # | Score | Type | Name | Path |\n|---|-------|------|------|------|\n| 1 | 1.000 | same_topic | plan\\|notes.md | /docs/line<br>break |\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}
