package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaPullStreamsPinnedModelWithoutShell(t *testing.T) {
	profile := testProfile(t, "qwen3-embedding-0.6b-ollama")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/pull" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != profile.Model || body["stream"] != true {
			t.Fatalf("pull body = %#v", body)
		}
		_, _ = io.WriteString(
			writer,
			"{\"status\":\"pulling manifest\"}\n"+
				"{\"status\":\"downloading\",\"digest\":\"sha256:abc\",\"completed\":5,\"total\":10}\n"+
				"{\"status\":\"success\"}\n",
		)
	}))
	defer server.Close()
	client := mustOllamaClient(t, server.URL)

	var progress []PullProgress
	err := client.Pull(context.Background(), profile, func(event PullProgress) error {
		progress = append(progress, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) != 3 || progress[1].Completed != 5 || progress[2].Status != "success" {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestOllamaPullRejectsMalformedAndInterruptedStreams(t *testing.T) {
	profile := testProfile(t, "nomic-embed-text-v1.5-ollama")
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "malformed", body: "{not-json}\n", want: "malformed progress"},
		{name: "ended early", body: "{\"status\":\"pulling manifest\"}\n", want: "before reporting success"},
		{name: "runtime error", body: "{\"error\":\"registry denied\"}\n", want: "registry denied"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(writer http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(writer, test.body)
				},
			))
			defer server.Close()
			err := mustOllamaClient(t, server.URL).Pull(
				context.Background(),
				profile,
				nil,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestOllamaPullRejectsOversizedProgressLine(t *testing.T) {
	profile := testProfile(t, "nomic-embed-text-v1.5-ollama")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, strings.Repeat("x", maxPullLineBytes+1)+"\n")
	}))
	defer server.Close()

	err := mustOllamaClient(t, server.URL).Pull(context.Background(), profile, nil)
	if err == nil || !strings.Contains(err.Error(), "read Ollama pull progress") {
		t.Fatalf("error = %v, want bounded progress error", err)
	}
}

func TestOllamaPullSupportsCancellation(t *testing.T) {
	profile := testProfile(t, "nomic-embed-text-v1.5-ollama")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(writer, "{\"status\":\"pulling manifest\"}\n")
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		<-request.Context().Done()
	}))
	defer server.Close()
	client := mustOllamaClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())

	err := client.Pull(ctx, profile, func(_ PullProgress) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestOllamaProbeUsesBatchDimensionsAndChecksOutput(t *testing.T) {
	profile := testProfile(t, "qwen3-embedding-0.6b-ollama")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/embed" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		var body struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
			Truncate   bool     `json:"truncate"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != profile.Model ||
			len(body.Input) != 1 ||
			body.Dimensions != CorpusDimension ||
			body.Truncate {
			t.Fatalf("probe body = %#v", body)
		}
		vector := make([]float64, CorpusDimension)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"embeddings": [][]float64{vector},
		})
	}))
	defer server.Close()
	if err := mustOllamaClient(t, server.URL).Probe(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
}

func TestOllamaProbeRejectsWrongDimension(t *testing.T) {
	profile := testProfile(t, "nomic-embed-text-v1.5-ollama")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"embeddings":[[0.1,0.2]]}`)
	}))
	defer server.Close()
	err := mustOllamaClient(t, server.URL).Probe(context.Background(), profile)
	if err == nil || !strings.Contains(err.Error(), "dimension 2, want 768") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallationForFailsClosedOnDigestMismatch(t *testing.T) {
	profile := testProfile(t, "nomic-embed-text-v1.5-ollama")
	state := RuntimeState{
		Available: true,
		Models: []InstalledModel{{
			Name:   profile.Model,
			Digest: "sha256:" + strings.Repeat("f", 64),
		}},
	}
	installation := InstallationFor(profile, state)
	if installation.Status != "digest_mismatch" ||
		!installation.Installed ||
		installation.DigestVerified {
		t.Fatalf("installation = %#v", installation)
	}
}

func TestOllamaStateReadsInstalledModels(t *testing.T) {
	profile := testProfile(t, "granite-embedding-278m-multilingual-ollama")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/tags":
			_, _ = fmt.Fprintf(
				writer,
				`{"models":[{"name":%q,"digest":%q,"size":123}]}`,
				profile.Model,
				profile.ManifestDigest,
			)
		case "/api/version":
			_, _ = io.WriteString(writer, `{"version":"1.2.3"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	state, err := mustOllamaClient(t, server.URL).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Available ||
		state.Version != "1.2.3" ||
		len(state.Models) != 1 ||
		state.Models[0].Digest != profile.ManifestDigest {
		t.Fatalf("state = %#v", state)
	}
}

func TestOllamaStateRejectsOversizedJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(
			writer,
			`{"models":[]}`+strings.Repeat(" ", maxResponseBytes+1),
		)
	}))
	defer server.Close()

	_, err := mustOllamaClient(t, server.URL).State(context.Background())
	if err == nil || !strings.Contains(err.Error(), "response exceeds size limit") {
		t.Fatalf("error = %v, want response size limit", err)
	}
}

func TestOllamaStateRejectsMultipleJSONDocuments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"models":[]} {"models":[]}`)
	}))
	defer server.Close()

	_, err := mustOllamaClient(t, server.URL).State(context.Background())
	if err == nil || !strings.Contains(err.Error(), "more than one JSON document") {
		t.Fatalf("error = %v, want single-document validation", err)
	}
}

func testProfile(t *testing.T, id string) Profile {
	t.Helper()
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := catalog.Find(id)
	if !ok {
		t.Fatalf("missing test profile %q", id)
	}
	return profile
}

func mustOllamaClient(t *testing.T, baseURL string) *OllamaClient {
	t.Helper()
	client, err := NewOllamaClient(baseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
