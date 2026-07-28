package apiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestListMemoriesMapsFiltersAndScopeHeaders(t *testing.T) {
	var (
		gotQuery  url.Values
		gotHeader http.Header
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/memories" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.Query()
		gotHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"memories":[{"id":"memory-1","kind":"decision","state_version":3}],
			"next_cursor":"cursor/+ ="
		}`)
	}))
	defer server.Close()

	recursive := false
	pinned := true
	client := New(server.URL, "token-1").WithWorkspace("workspace-1")
	response, err := client.ListMemories(context.Background(), MemoryListOptions{
		Scope:     "/Projects/mem α",
		Recursive: &recursive,
		Kinds:     []string{"decision", "fact"},
		Lifecycle: "all",
		Pinned:    &pinned,
		Limit:     25,
		Cursor:    "cursor/+ =",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader.Get("Authorization") != "Bearer token-1" ||
		gotHeader.Get("X-Workspace-ID") != "workspace-1" {
		t.Fatalf("scope headers = %#v", gotHeader)
	}
	if gotQuery.Get("scope") != "/Projects/mem α" ||
		gotQuery.Get("recursive") != "false" ||
		gotQuery.Get("lifecycle") != "all" ||
		gotQuery.Get("pinned") != "true" ||
		gotQuery.Get("limit") != "25" ||
		gotQuery.Get("cursor") != "cursor/+ =" {
		t.Fatalf("query = %#v", gotQuery)
	}
	kinds := gotQuery["kind"]
	if len(kinds) != 2 || kinds[0] != "decision" || kinds[1] != "fact" {
		t.Fatalf("kind query = %#v", kinds)
	}
	if len(response.Memories) != 1 ||
		response.Memories[0].ID != "memory-1" ||
		response.Memories[0].StateVersion != 3 ||
		response.NextCursor != "cursor/+ =" {
		t.Fatalf("response = %#v", response)
	}
}

func TestMemoryMutationMethodsMapHeaderPathAndBody(t *testing.T) {
	const memoryID = "9baadf78-6ad1-47a7-a719-57122f352a67"
	tests := []struct {
		name     string
		action   string
		response string
		call     func(*Client) error
		wantBody map[string]any
	}{
		{
			name:     "feedback",
			action:   "feedback",
			response: `{"memory":{"id":"memory-1","state_version":4},"replayed":false}`,
			call: func(client *Client) error {
				_, err := client.FeedbackMemory(
					context.Background(),
					memoryID,
					"feedback-key",
					MemoryFeedbackRequest{Action: "pin", ExpectedVersion: 3},
				)
				return err
			},
			wantBody: map[string]any{"action": "pin", "expected_version": float64(3)},
		},
		{
			name:     "archive",
			action:   "archive",
			response: `{"memory":{"id":"memory-1","state_version":4},"replayed":false}`,
			call: func(client *Client) error {
				_, err := client.ArchiveMemory(
					context.Background(),
					memoryID,
					"archive-key",
					MemoryVersionRequest{ExpectedVersion: 3},
				)
				return err
			},
			wantBody: map[string]any{"expected_version": float64(3)},
		},
		{
			name:     "restore",
			action:   "restore",
			response: `{"memory":{"id":"memory-1","state_version":4},"replayed":true}`,
			call: func(client *Client) error {
				_, err := client.RestoreMemory(
					context.Background(),
					memoryID,
					"restore-key",
					MemoryVersionRequest{ExpectedVersion: 3},
				)
				return err
			},
			wantBody: map[string]any{"expected_version": float64(3)},
		},
		{
			name:   "forget",
			action: "forget",
			response: `{
				"memory_id":"9baadf78-6ad1-47a7-a719-57122f352a67",
				"state_version":4,
				"forgotten_at":"2026-07-28T12:00:00Z",
				"event":{},
				"replayed":false
			}`,
			call: func(client *Client) error {
				_, err := client.ForgetMemory(
					context.Background(),
					memoryID,
					"forget-key",
					MemoryForgetRequest{ExpectedVersion: 3, Reason: "sensitive"},
				)
				return err
			},
			wantBody: map[string]any{
				"expected_version": float64(3),
				"reason":           "sensitive",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				gotPath   string
				gotHeader string
				gotBody   map[string]any
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.EscapedPath()
				gotHeader = r.Header.Get("Idempotency-Key")
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Fatal(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()

			if err := test.call(New(server.URL, "token")); err != nil {
				t.Fatal(err)
			}
			if gotPath != "/v1/memories/"+memoryID+"/"+test.action {
				t.Fatalf("path = %q", gotPath)
			}
			if gotHeader != test.name+"-key" {
				t.Fatalf("Idempotency-Key = %q", gotHeader)
			}
			if !equalJSONMaps(gotBody, test.wantBody) {
				t.Fatalf("body = %#v, want %#v", gotBody, test.wantBody)
			}
		})
	}
}

func equalJSONMaps(left, right map[string]any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}
