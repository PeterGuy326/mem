package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const (
	testFileAnnotationFileID = "9baadf78-6ad1-47a7-a719-57122f352a67"
	testFileAnnotationID     = "441bcc02-9fe2-44bb-a68b-8dd9a190fb6e"
)

func TestDecideFileAnnotationMapsRequestScopeAndResponse(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut ||
			r.URL.Path != "/v1/files/"+testFileAnnotationFileID+
				"/annotations/"+testFileAnnotationID {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "workspace-1" {
			t.Errorf("X-Workspace-ID = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"annotation":{
				"id":"`+testFileAnnotationID+`",
				"file_id":"`+testFileAnnotationFileID+`",
				"kind":"tag",
				"value_text":"travel",
				"status":"accepted",
				"state_version":2
			},
			"replayed":false
		}`)
	}))
	defer server.Close()

	client := New(server.URL, "token-1").WithWorkspace("workspace-1")
	response, err := client.DecideFileAnnotation(
		context.Background(),
		testFileAnnotationFileID,
		testFileAnnotationID,
		FileAnnotationDecisionRequest{
			Decision:        FileAnnotationDecisionAccepted,
			ExpectedVersion: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if requestBody["decision"] != "accepted" ||
		requestBody["expected_version"] != float64(1) {
		t.Fatalf("request body = %#v", requestBody)
	}
	if response.Annotation.ID != testFileAnnotationID ||
		response.Annotation.FileID != testFileAnnotationFileID ||
		response.Annotation.Status != "accepted" ||
		response.Annotation.StateVersion != 2 ||
		response.Replayed {
		t.Fatalf("response = %#v", response)
	}
}

func TestDecideFileAnnotationPreservesAPIConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(
			w,
			`{"error":"annotation_version_conflict","hint":"reload and retry"}`,
		)
	}))
	defer server.Close()

	_, err := New(server.URL, "token").DecideFileAnnotation(
		context.Background(),
		testFileAnnotationFileID,
		testFileAnnotationID,
		FileAnnotationDecisionRequest{
			Decision:        FileAnnotationDecisionRejected,
			ExpectedVersion: 2,
		},
	)
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error = %T %v, want *APIError", err, err)
	}
	if apiError.StatusCode != http.StatusConflict ||
		apiError.Code != "annotation_version_conflict" ||
		apiError.Hint != "reload and retry" {
		t.Fatalf("API error = %#v", apiError)
	}
}

func TestDecideFileAnnotationRejectsInvalidInputBeforeRequest(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := New(server.URL, "token")

	tests := []struct {
		name         string
		fileID       string
		annotationID string
		request      FileAnnotationDecisionRequest
	}{
		{
			name:         "decision",
			fileID:       testFileAnnotationFileID,
			annotationID: testFileAnnotationID,
			request: FileAnnotationDecisionRequest{
				Decision:        "pending",
				ExpectedVersion: 1,
			},
		},
		{
			name:         "version",
			fileID:       testFileAnnotationFileID,
			annotationID: testFileAnnotationID,
			request: FileAnnotationDecisionRequest{
				Decision: FileAnnotationDecisionAccepted,
			},
		},
		{
			name:         "file id",
			fileID:       "not-a-uuid",
			annotationID: testFileAnnotationID,
			request: FileAnnotationDecisionRequest{
				Decision:        FileAnnotationDecisionAccepted,
				ExpectedVersion: 1,
			},
		},
		{
			name:         "annotation id",
			fileID:       testFileAnnotationFileID,
			annotationID: "not-a-uuid",
			request: FileAnnotationDecisionRequest{
				Decision:        FileAnnotationDecisionRejected,
				ExpectedVersion: 1,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.DecideFileAnnotation(
				context.Background(),
				test.fileID,
				test.annotationID,
				test.request,
			); err == nil {
				t.Fatal("expected local validation error")
			}
		})
	}
	if requestCount.Load() != 0 {
		t.Fatalf("invalid input made %d request(s)", requestCount.Load())
	}
}
