package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
)

const (
	toolFileAnnotationFileID = "9baadf78-6ad1-47a7-a719-57122f352a67"
	toolFileAnnotationID     = "441bcc02-9fe2-44bb-a68b-8dd9a190fb6e"
)

func TestMemFileAnnotationDecideSchema(t *testing.T) {
	server := newFakeServer(`{}`, http.StatusOK, "application/json")
	defer server.Close()
	registry := tools.New()
	if err := registerFileAnnotationDecision(
		registry,
		apiclient.New(server.URL, "token"),
	); err != nil {
		t.Fatal(err)
	}

	tool, ok := registry.Get("mem_file_annotation_decide")
	if !ok {
		t.Fatal("tool is not registered")
	}
	if len(tool.InputSchema.Required) != 4 {
		t.Fatalf("required = %#v", tool.InputSchema.Required)
	}
	decision := tool.InputSchema.Properties["decision"]
	if len(decision.Enum) != 2 ||
		decision.Enum[0] != "accepted" ||
		decision.Enum[1] != "rejected" {
		t.Fatalf("decision schema = %#v", decision)
	}
	version := tool.InputSchema.Properties["expected_version"]
	if version.Minimum == nil || *version.Minimum != 1 {
		t.Fatalf("expected_version schema = %#v", version)
	}
}

func TestMemFileAnnotationDecideMapsAcceptedAndRejected(t *testing.T) {
	for _, decision := range []string{"accepted", "rejected"} {
		t.Run(decision, func(t *testing.T) {
			server := newFakeServer(
				`{
					"annotation":{
						"id":"`+toolFileAnnotationID+`",
						"file_id":"`+toolFileAnnotationFileID+`",
						"kind":"description",
						"value_text":"A day trip",
						"status":"`+decision+`",
						"state_version":2
					},
					"replayed":false
				}`,
				http.StatusOK,
				"application/json",
			)
			defer server.Close()
			registry := tools.New()
			client := apiclient.New(server.URL, "token-1").WithWorkspace("workspace-1")
			if err := registerFileAnnotationDecision(registry, client); err != nil {
				t.Fatal(err)
			}

			output, err := registry.Call(
				context.Background(),
				"mem_file_annotation_decide",
				map[string]any{
					"file_id":          toolFileAnnotationFileID,
					"annotation_id":    toolFileAnnotationID,
					"decision":         decision,
					"expected_version": float64(1),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if server.lastMethod != http.MethodPut ||
				server.lastPath != "/v1/files/"+toolFileAnnotationFileID+
					"/annotations/"+toolFileAnnotationID {
				t.Fatalf(
					"request = %s %s",
					server.lastMethod,
					server.lastPath,
				)
			}
			if server.lastHeaders.Get("Authorization") != "Bearer token-1" ||
				server.lastHeaders.Get("X-Workspace-ID") != "workspace-1" {
				t.Fatalf("scope headers = %#v", server.lastHeaders)
			}
			var body map[string]any
			if err := json.Unmarshal(server.lastBody, &body); err != nil {
				t.Fatal(err)
			}
			if body["decision"] != decision ||
				body["expected_version"] != float64(1) {
				t.Fatalf("body = %#v", body)
			}
			response, ok := output.(*apiclient.FileAnnotationDecisionResponse)
			if !ok ||
				response.Annotation.ID != toolFileAnnotationID ||
				response.Annotation.Status != decision ||
				response.Annotation.StateVersion != 2 ||
				response.Replayed {
				t.Fatalf("output = %#v", output)
			}
		})
	}
}

func TestMemFileAnnotationDecidePreservesConflict(t *testing.T) {
	server := newFakeServer(
		`{"error":"annotation_version_conflict","hint":"reload and retry"}`,
		http.StatusConflict,
		"application/json",
	)
	defer server.Close()
	registry := tools.New()
	if err := registerFileAnnotationDecision(
		registry,
		apiclient.New(server.URL, "token"),
	); err != nil {
		t.Fatal(err)
	}

	_, err := registry.Call(
		context.Background(),
		"mem_file_annotation_decide",
		map[string]any{
			"file_id":          toolFileAnnotationFileID,
			"annotation_id":    toolFileAnnotationID,
			"decision":         "accepted",
			"expected_version": float64(2),
		},
	)
	var apiError *apiclient.APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error = %T %v, want *APIError", err, err)
	}
	if apiError.StatusCode != http.StatusConflict ||
		apiError.Code != "annotation_version_conflict" ||
		apiError.Hint != "reload and retry" {
		t.Fatalf("API error = %#v", apiError)
	}
}

func TestMemFileAnnotationDecideRejectsInvalidInputBeforeRequest(t *testing.T) {
	tests := []map[string]any{
		{
			"file_id":          toolFileAnnotationFileID,
			"annotation_id":    toolFileAnnotationID,
			"decision":         "pending",
			"expected_version": float64(1),
		},
		{
			"file_id":          toolFileAnnotationFileID,
			"annotation_id":    toolFileAnnotationID,
			"decision":         "accepted",
			"expected_version": 1.5,
		},
		{
			"file_id":          "not-a-uuid",
			"annotation_id":    toolFileAnnotationID,
			"decision":         "rejected",
			"expected_version": float64(1),
		},
	}
	for _, args := range tests {
		server := newFakeServer(`{}`, http.StatusOK, "application/json")
		registry := tools.New()
		if err := registerFileAnnotationDecision(
			registry,
			apiclient.New(server.URL, "token"),
		); err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err := registry.Call(
			context.Background(),
			"mem_file_annotation_decide",
			args,
		)
		if err == nil {
			server.Close()
			t.Fatalf("expected local validation error for %#v", args)
		}
		if server.lastPath != "" {
			server.Close()
			t.Fatalf("invalid input reached %s", server.lastPath)
		}
		server.Close()
	}
}
