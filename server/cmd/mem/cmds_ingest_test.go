package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// writeTranscript writes a small valid JSONL transcript containing three
// ingestible conversation turns with realistic Qoder-style fields.
func writeTranscript(t *testing.T, dir, name string) string {
	t.Helper()
	lines := []string{
		`{"role":"system","content":"you are a scoring assistant","model":"qoder-default"}`,
		`{"role":"user","content":"Score candidate A on FY27 campus recruiting","timestamp":"2026-08-20T09:00:00Z"}`,
		`{"role":"assistant","content":"Candidate A: score 4/5, strong leadership evidence.","model":"claude-4-5","created_at":1724169600}`,
		`not-json-garbage`,
		`{"role":"user","content":"","model":"x"}`,
	}
	// line 4 (not-json-garbage) is unparseable; line 5 has empty content — both
	// must be dropped by the parser.
	body := strings.Join(lines, "\n")
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseQoderTranscript(t *testing.T) {
	dir := t.TempDir()
	abs := writeTranscript(t, dir, "campus-2027/sessions/recruit-s3e0a.jsonl")

	turns, skipped, err := parseQoderTranscript(abs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want 3\n%v", len(turns), turns)
	}
	// line 4 (garbage) and line 5 (empty content) are unparseable.
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if turns[0].Content != "you are a scoring assistant" || turns[0].Role != "system" ||
		turns[0].AgentID != "qoder-default" {
		t.Errorf("turn0 = %+v", turns[0])
	}
	if turns[1].Content != "Score candidate A on FY27 campus recruiting" || turns[1].Role != "user" {
		t.Errorf("turn1 = %+v", turns[1])
	}
	if turns[1].EventAt == nil || turns[1].EventAt.Format("2006-01-02") != "2026-08-20" {
		t.Errorf("turn1 eventAt = %v", turns[1].EventAt)
	}
	if turns[2].AgentID != "claude-4-5" {
		t.Errorf("turn2 = %+v", turns[2])
	}
	// line numbers must be 1-based and skip non-JSON / empty-content lines
	if turns[1].Line != 2 || turns[2].Line != 3 {
		t.Errorf("lines = %d,%d", turns[1].Line, turns[2].Line)
	}
}

func TestParseQoderTranscriptSkipBefore(t *testing.T) {
	dir := t.TempDir()
	abs := writeTranscript(t, dir, "p/s.jsonl")

	// Skip the first two ingestible lines (system + first user turn): lines 1-2.
	// The assistant decision line (3) must remain.
	turns, _, err := parseQoderTranscript(abs, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}
	if !strings.Contains(turns[0].Content, "leadership evidence") {
		t.Errorf("turn = %+v", turns[0])
	}
	// epoch-second created_at on the assistant line must have parsed.
	if turns[0].EventAt == nil {
		t.Errorf("expected epoch timestamp to parse, got nil")
	}
}

func TestIngestQoderPostsAndCheckpoints(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "store")
	abs := writeTranscript(t, transcriptDir, "campus-2027/sessions/recruit-s3e0a.jsonl")

	var (
		mu       sync.Mutex
		bodies   []map[string]any
		keys     []string
		requests atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/memories" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		bodies = append(bodies, b)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"memory":{"id":"m-1"},"replayed":false}`))
	}))
	defer srv.Close()

	stateDir := filepath.Join(dir, "state")
	t.Setenv("MEM_CONFIG", filepath.Join(dir, "missing.yaml"))
	t.Setenv("MEM_SERVER", srv.URL)
	t.Setenv("MEM_TOKEN", "tok")
	t.Setenv("MEM_WORKSPACE", "ws-1")
	t.Setenv("MEM_STATE_DIR", stateDir)

	run := func() (string, error) {
		root := newRootCmd()
		var stdout bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stdout)
		root.SetArgs([]string{"ingest", "qoder", "--root", transcriptDir})
		return stdout.String(), root.Execute()
	}

	// First run: three memories must be posted.
	if out, err := run(); err != nil {
		t.Fatal(out, err)
	}
	if requests.Load() != 3 {
		t.Fatalf("first run requests = %d, want 3", requests.Load())
	}

	mu.Lock()
	if len(bodies) != 3 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	// source flags + producer + path + kind
	b := bodies[1] // user turn
	src, _ := b["source"].(map[string]any)
	if src["type"] != "qoder" || src["ref"] != abs {
		t.Errorf("source = %#v", src)
	}
	if loc, _ := src["locator"].(map[string]any); loc["line"].(float64) != 2 {
		t.Errorf("locator = %#v", src["locator"])
	}
	prod, _ := b["producer"].(map[string]any)
	if prod["session_id"] != "recruit-s3e0a" {
		t.Errorf("producer = %#v", prod)
	}
	// the user turn has no model; the system turn (body[0]) must carry the model id
	prod0, _ := bodies[0]["producer"].(map[string]any)
	if prod0["agent_id"] != "qoder-default" {
		t.Errorf("producer[0] = %#v", prod0)
	}
	if b["kind"] != "observation" {
		t.Errorf("kind = %v", b["kind"])
	}
	if !strings.HasPrefix(b["path"].(string), "/AgentTranscripts/campus-2027/recruit-s3e0a") {
		t.Errorf("path = %v", b["path"])
	}
	if _, exists := b["idempotency_key"]; exists {
		t.Errorf("idempotency_key must not be in body: %#v", b)
	}
	// every write must carry a stable idempotency key
	for i, k := range keys {
		if !strings.HasPrefix(k, "qoder:") {
			t.Errorf("key[%d] = %q", i, k)
		}
	}
	mu.Unlock()

	// Second run: checkpoint advances, so nothing newer is posted again.
	requests.Store(0)
	if out, err := run(); err != nil {
		t.Fatal(out, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("second run requests = %d, want 0 (checkpoint must skip already-ingested lines)", requests.Load())
	}

	// Append a new line and confirm only it is ingested.
	f, err := os.OpenFile(abs, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"role":"assistant","content":"Decision: adopt the 4-score rubric.","model":"claude-4-5"}` + "\n")
	_ = f.Close()

	requests.Store(0)
	if out, err := run(); err != nil {
		t.Fatal(out, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("post-append requests = %d, want 1", requests.Load())
	}
	mu.Lock()
	last := bodies[len(bodies)-1]
	mu.Unlock()
	if !strings.Contains(last["content"].(string), "4-score rubric") {
		t.Errorf("appended memory content = %v", last["content"])
	}
}

func TestIngestQoderDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "store")
	writeTranscript(t, transcriptDir, "p/s.jsonl")

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"memory":{"id":"m-1"},"replayed":false}`))
	}))
	defer srv.Close()

	stateDir := filepath.Join(dir, "state")
	t.Setenv("MEM_CONFIG", filepath.Join(dir, "missing.yaml"))
	t.Setenv("MEM_SERVER", srv.URL)
	t.Setenv("MEM_TOKEN", "tok")
	t.Setenv("MEM_STATE_DIR", stateDir)

	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"ingest", "qoder", "--root", transcriptDir, "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("dry-run made %d request(s)", requests.Load())
	}
	if !strings.Contains(stdout.String(), "dry-run") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	// No checkpoint files must be written after a dry run.
	checkpointFiles, _ := filepath.Glob(filepath.Join(stateDir, "ingest", "qoder", "*.json"))
	if len(checkpointFiles) != 0 {
		t.Fatalf("dry-run left %d checkpoint file(s): %v", len(checkpointFiles), checkpointFiles)
	}

	// A dry run must not consume the transcript: the following real run has to
	// post every ingestible line. (Fresh root: cobra retains parsed flag values.)
	real := newRootCmd()
	real.SetOut(&stdout)
	real.SetArgs([]string{"ingest", "qoder", "--root", transcriptDir})
	if err := real.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests after dry-run + real run = %d, want 3 (dry run advanced the cursor)", got)
	}
}

func TestIngestQoderRequiresLogin(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "store")
	writeTranscript(t, transcriptDir, "p/s.jsonl")

	t.Setenv("MEM_CONFIG", filepath.Join(dir, "missing.yaml"))
	t.Setenv("MEM_TOKEN", "") // not logged in
	t.Setenv("MEM_STATE_DIR", filepath.Join(dir, "state"))

	root := newRootCmd()
	root.SetArgs([]string{"ingest", "qoder", "--root", transcriptDir})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected login error")
	}
	var ce *cliError
	if !errors.As(err, &ce) || ce.code != 3 {
		t.Fatalf("err = %v", err)
	}
}

func TestIngestQoderReplayedResponseCounted(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "store")
	writeTranscript(t, transcriptDir, "p/s.jsonl")

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"memory":{"id":"m-1"},"replayed":true}`))
	}))
	defer srv.Close()

	t.Setenv("MEM_CONFIG", filepath.Join(dir, "missing.yaml"))
	t.Setenv("MEM_SERVER", srv.URL)
	t.Setenv("MEM_TOKEN", "tok")
	t.Setenv("MEM_WORKSPACE", "ws-1")
	t.Setenv("MEM_STATE_DIR", filepath.Join(dir, "state"))

	root := newRootCmd()
	root.SetArgs([]string{"ingest", "qoder", "--root", transcriptDir})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want 3", requests.Load())
	}
}

func TestIngestQoder409ConflictDegradesPerFile(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "store")
	abs1 := writeTranscript(t, transcriptDir, "project-a/s1.jsonl")
	abs2 := writeTranscript(t, transcriptDir, "project-b/s2.jsonl")

	var (
		requests atomic.Int32
		first409 int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		src := b["source"].(map[string]any)
		loc := src["locator"].(map[string]any)
		line := int(loc["line"].(float64))
		if line == 2 && atomic.CompareAndSwapInt32(&first409, 0, 1) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"idempotency conflict","hint":"file rewritten"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"memory":{"id":"m-1"},"replayed":false}`))
	}))
	defer srv.Close()

	t.Setenv("MEM_CONFIG", filepath.Join(dir, "missing.yaml"))
	t.Setenv("MEM_SERVER", srv.URL)
	t.Setenv("MEM_TOKEN", "tok")
	t.Setenv("MEM_WORKSPACE", "ws-1")
	t.Setenv("MEM_STATE_DIR", filepath.Join(dir, "state"))

	stateDir := filepath.Join(dir, "state")
	cpDir := filepath.Join(stateDir, "ingest", "qoder")
	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"ingest", "qoder", "--root", transcriptDir})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	// project-a: line 1 (200), line 2 (409 → skip file, line 3 not attempted)
	// project-b: line 1 (200), line 2 (200), line 3 (200) — all 3
	// Total: 1 + 1(409) + 3 = 5 requests.
	if requests.Load() != 5 {
		t.Fatalf("requests = %d, want 5 (a1=200, a2=409→skip, b1-b3=200)", requests.Load())
	}
	if !strings.Contains(stdout.String(), "idempotency conflict") {
		t.Fatalf("expected conflict warning, got stdout = %q", stdout.String())
	}
	cp := loadQoderCheckpoint(cpDir, abs1)
	if cp.LastLine != 1 {
		t.Fatalf("project-a checkpoint LastLine = %d, want 1 (line 2 failed)", cp.LastLine)
	}
	cp2 := loadQoderCheckpoint(cpDir, abs2)
	if cp2.LastLine != 3 {
		t.Fatalf("project-b checkpoint LastLine = %d, want 3", cp2.LastLine)
	}
}

func TestIngestQoderLimitCheckpoint(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "store")
	abs := writeTranscript(t, transcriptDir, "p/s.jsonl")

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"memory":{"id":"m-1"},"replayed":false}`))
	}))
	defer srv.Close()

	t.Setenv("MEM_CONFIG", filepath.Join(dir, "missing.yaml"))
	t.Setenv("MEM_SERVER", srv.URL)
	t.Setenv("MEM_TOKEN", "tok")
	t.Setenv("MEM_WORKSPACE", "ws-1")
	t.Setenv("MEM_STATE_DIR", filepath.Join(dir, "state"))

	stateDir := filepath.Join(dir, "state")
	cpDir := filepath.Join(stateDir, "ingest", "qoder")

	// First run with --limit 2: only 2 of 3 lines are ingested.
	root := newRootCmd()
	root.SetArgs([]string{"ingest", "qoder", "--root", transcriptDir, "--limit", "2"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("first run requests = %d, want 2", requests.Load())
	}
	cp := loadQoderCheckpoint(cpDir, abs)
	if cp.LastLine != 2 {
		t.Fatalf("after limit checkpoint LastLine = %d, want 2", cp.LastLine)
	}

	// Second run without limit: processes the remaining line 3.
	requests.Store(0)
	root2 := newRootCmd()
	root2.SetArgs([]string{"ingest", "qoder", "--root", transcriptDir})
	if err := root2.Execute(); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("second run requests = %d, want 1", requests.Load())
	}
	cp2 := loadQoderCheckpoint(cpDir, abs)
	if cp2.LastLine != 3 {
		t.Fatalf("after second run LastLine = %d, want 3", cp2.LastLine)
	}
}
