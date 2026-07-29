package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/file"
)

func TestParseSourceMetadataJSON(t *testing.T) {
	t.Parallel()

	raw := `{
		"captured_at":"2026-07-29T12:30:45.123+08:00",
		"location":{
			"lat":31.2304,
			"lon":121.4737,
			"accuracy_m":8.5,
			"label":"Shanghai"
		},
		"source_kind":"mobile",
		"source_name":"phone sync"
	}`
	got, err := ParseSourceMetadataJSON(raw)
	if err != nil {
		t.Fatalf("ParseSourceMetadataJSON: %v", err)
	}
	wantTime, err := time.Parse(time.RFC3339, "2026-07-29T12:30:45.123+08:00")
	if err != nil {
		t.Fatalf("parse test timestamp: %v", err)
	}
	if got.CapturedAt == nil || !got.CapturedAt.Equal(wantTime) {
		t.Fatalf("captured_at = %v, want %v", got.CapturedAt, wantTime)
	}
	if got.SourceKind != file.SourceKindMobile || got.SourceName != "phone sync" {
		t.Fatalf("source fields = %+v", got)
	}
	if got.Location == nil ||
		got.Location.Lat != 31.2304 ||
		got.Location.Lon != 121.4737 ||
		got.Location.AccuracyMeters == nil ||
		*got.Location.AccuracyMeters != 8.5 ||
		got.Location.Label != "Shanghai" {
		t.Fatalf("location = %+v", got.Location)
	}

	empty, err := ParseSourceMetadataJSON(" \n\t")
	if err != nil {
		t.Fatalf("empty source metadata: %v", err)
	}
	if !reflect.DeepEqual(empty, file.SourceMetadata{}) {
		t.Fatalf("empty metadata = %+v, want zero value", empty)
	}
}

func TestParseSourceMetadataJSONRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{"too large", `{"source_name":"` + strings.Repeat("a", MaxSourceMetadataBytes) + `"}`},
		{"not object", `[]`},
		{"null object", `null`},
		{"null captured at", `{"captured_at":null}`},
		{"null source kind", `{"source_kind":null}`},
		{"null location", `{"location":null}`},
		{"null latitude", `{"location":{"lat":null,"lon":0}}`},
		{"duplicate top level", `{"source_kind":"cli","source_kind":"mobile"}`},
		{"duplicate location", `{"location":{"lat":0,"lat":1,"lon":0}}`},
		{"unknown top level field", `{"device_id":"abc"}`},
		{"unknown location field", `{"location":{"lat":0,"lon":0,"altitude":3}}`},
		{"timezone missing", `{"captured_at":"2026-07-29T12:30:45"}`},
		{"latitude missing", `{"location":{"lon":121}}`},
		{"longitude missing", `{"location":{"lat":31}}`},
		{"latitude out of range", `{"location":{"lat":91,"lon":0}}`},
		{"longitude out of range", `{"location":{"lat":0,"lon":181}}`},
		{"negative accuracy", `{"location":{"lat":0,"lon":0,"accuracy_m":-1}}`},
		{"source kind unsupported", `{"source_kind":"camera"}`},
		{"source name control character", `{"source_name":"phone\nsync"}`},
		{"source name format character", `{"source_name":"phone\u200bsync"}`},
		{"source name default ignorable", `{"source_name":"phone\u034fsync"}`},
		{"location label control character", `{"location":{"lat":0,"lon":0,"label":"home\u007f"}}`},
		{"location label variation selector", `{"location":{"lat":0,"lon":0,"label":"home\ufe0f"}}`},
		{"multiple json values", `{} {}`},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseSourceMetadataJSON(test.raw); err == nil {
				t.Fatalf("ParseSourceMetadataJSON(%q) succeeded", test.raw)
			}
		})
	}
}

type fileAnnotationServiceStub struct {
	ownerID      uuid.UUID
	actorID      uuid.UUID
	fileID       uuid.UUID
	annotationID uuid.UUID
	command      file.AnnotationDecisionCommand
	result       *file.AnnotationDecisionResult
	err          error
	calls        int
}

func (stub *fileAnnotationServiceStub) DecideAnnotation(
	_ context.Context,
	ownerID, actorID, fileID, annotationID uuid.UUID,
	command file.AnnotationDecisionCommand,
) (*file.AnnotationDecisionResult, error) {
	stub.calls++
	stub.ownerID = ownerID
	stub.actorID = actorID
	stub.fileID = fileID
	stub.annotationID = annotationID
	stub.command = command
	return stub.result, stub.err
}

func TestHandleDecideFileAnnotation(t *testing.T) {
	t.Parallel()

	annotation := file.Annotation{
		ID:           uuid.New(),
		FileID:       uuid.New(),
		Kind:         file.AnnotationKindTag,
		ValueText:    "travel",
		Status:       file.AnnotationStatusAccepted,
		StateVersion: 2,
	}
	stub := &fileAnnotationServiceStub{
		result: &file.AnnotationDecisionResult{
			Annotation: annotation,
			Replayed:   false,
		},
	}
	server := &Server{FileAnnotations: stub}
	request := httptest.NewRequest(
		http.MethodPut,
		"/v1/files/"+annotation.FileID.String()+"/annotations/"+annotation.ID.String(),
		strings.NewReader(`{"decision":"accepted","expected_version":1}`),
	)
	request, ownerID, actorID := fileAnnotationHandlerContext(
		request,
		annotation.FileID,
		annotation.ID,
		[]string{"/Photos", "/Imports"},
	)
	recorder := httptest.NewRecorder()

	server.handleDecideFileAnnotation(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.calls != 1 {
		t.Fatalf("service calls = %d, want 1", stub.calls)
	}
	if stub.ownerID != ownerID ||
		stub.actorID != actorID ||
		stub.fileID != annotation.FileID ||
		stub.annotationID != annotation.ID {
		t.Fatalf("service identities = %+v", stub)
	}
	wantCommand := file.AnnotationDecisionCommand{
		Decision:        file.AnnotationStatusAccepted,
		ExpectedVersion: 1,
		AllowedPaths:    []string{"/Photos", "/Imports"},
	}
	if !reflect.DeepEqual(stub.command, wantCommand) {
		t.Fatalf("command = %+v, want %+v", stub.command, wantCommand)
	}

	var response file.AnnotationDecisionResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Annotation.ID != annotation.ID ||
		response.Annotation.Status != file.AnnotationStatusAccepted ||
		response.Annotation.StateVersion != 2 ||
		response.Replayed {
		t.Fatalf("response = %+v", response)
	}
}

func TestHandleDecideFileAnnotationMapsTerminalConflict(t *testing.T) {
	t.Parallel()

	fileID := uuid.New()
	annotationID := uuid.New()
	stub := &fileAnnotationServiceStub{err: file.ErrAnnotationDecisionConflict}
	server := &Server{FileAnnotations: stub}
	request := httptest.NewRequest(
		http.MethodPut,
		"/v1/files/"+fileID.String()+"/annotations/"+annotationID.String(),
		strings.NewReader(`{"decision":"rejected","expected_version":2}`),
	)
	request, _, _ = fileAnnotationHandlerContext(request, fileID, annotationID, nil)
	recorder := httptest.NewRecorder()

	server.handleDecideFileAnnotation(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["error"] != "annotation_decision_conflict" {
		t.Fatalf("error code = %#v", response["error"])
	}
}

func TestHandleDecideFileAnnotationRejectsInvalidBodyBeforeService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"invalid decision", `{"decision":"approve","expected_version":1}`, "bad_decision"},
		{"missing version", `{"decision":"accepted"}`, "bad_expected_version"},
		{"zero version", `{"decision":"accepted","expected_version":0}`, "bad_expected_version"},
		{"unknown field", `{"decision":"accepted","expected_version":1,"force":true}`, "bad_json"},
		{"trailing value", `{"decision":"accepted","expected_version":1} {}`, "bad_json"},
		{
			"too large",
			`{"decision":"accepted","expected_version":1,"padding":"` +
				strings.Repeat("a", maxAnnotationDecisionBodyBytes) + `"}`,
			"bad_json",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fileID := uuid.New()
			annotationID := uuid.New()
			stub := &fileAnnotationServiceStub{}
			server := &Server{FileAnnotations: stub}
			request := httptest.NewRequest(
				http.MethodPut,
				"/v1/files/"+fileID.String()+"/annotations/"+annotationID.String(),
				strings.NewReader(test.body),
			)
			request, _, _ = fileAnnotationHandlerContext(request, fileID, annotationID, nil)
			recorder := httptest.NewRecorder()

			server.handleDecideFileAnnotation(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if stub.calls != 0 {
				t.Fatalf("service calls = %d, want 0", stub.calls)
			}
			var response map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response["error"] != test.wantCode {
				t.Fatalf("error code = %#v, want %q", response["error"], test.wantCode)
			}
		})
	}
}

func TestWriteAnnotationDecisionError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not found", file.ErrAnnotationNotFound, http.StatusNotFound, "not_found"},
		{
			"wrapped terminal conflict",
			errors.Join(errors.New("context"), file.ErrAnnotationDecisionConflict),
			http.StatusConflict,
			"annotation_decision_conflict",
		},
		{
			"version conflict",
			file.ErrAnnotationVersionConflict,
			http.StatusConflict,
			"annotation_version_conflict",
		},
		{
			"bad decision",
			file.ErrInvalidAnnotationDecision,
			http.StatusBadRequest,
			"bad_decision",
		},
		{"unknown", errors.New("database unavailable"), http.StatusInternalServerError, "annotation_update_failed"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			writeAnnotationDecisionError(recorder, test.err)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			var response map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response["error"] != test.wantCode {
				t.Fatalf("error code = %#v, want %q", response["error"], test.wantCode)
			}
		})
	}
}

func TestFileAnnotationRouteIsRegistered(t *testing.T) {
	t.Parallel()

	routes, ok := (&Server{}).Router().(chi.Routes)
	if !ok {
		t.Fatal("Server.Router did not return chi.Routes")
	}
	found := false
	err := chi.Walk(routes, func(
		method string,
		route string,
		_ http.Handler,
		_ ...func(http.Handler) http.Handler,
	) error {
		if method == http.MethodPut &&
			route == "/v1/files/{fileID}/annotations/{annotationID}" {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	if !found {
		t.Fatal("file annotation decision route is not registered")
	}
}

func fileAnnotationHandlerContext(
	request *http.Request,
	fileID, annotationID uuid.UUID,
	paths []string,
) (*http.Request, uuid.UUID, uuid.UUID) {
	ownerID := uuid.New()
	actorID := uuid.New()
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("fileID", fileID.String())
	routeContext.URLParams.Add("annotationID", annotationID.String())

	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, ctxUser, &auth.User{ID: ownerID})
	ctx = context.WithValue(ctx, ctxActor, &auth.User{ID: actorID})
	ctx = context.WithValue(ctx, ctxToken, &auth.Token{Paths: append([]string(nil), paths...)})
	return request.WithContext(ctx), ownerID, actorID
}
