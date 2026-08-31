package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// doctorStub answers the three endpoints mem doctor probes, and records every
// request. An unexpected method or path fails the test, which is how AC-002
// ("performs no write request of any kind") is enforced.
type doctorStub struct {
	mu       sync.Mutex
	requests []string
	healthz  func(http.ResponseWriter)
	caps     func(http.ResponseWriter)
	version  func(http.ResponseWriter)
}

func newDoctorStub() *doctorStub {
	s := &doctorStub{}
	s.healthz = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	s.caps = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"workspace":{"id":"11111111-1111-1111-1111-111111111111","name":"Personal","role":"owner"}}`))
	}
	s.version = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"version":"0.1.0"}`))
	}
	return s
}

func (s *doctorStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			t.Errorf("%s %s carried a request body: %s", r.Method, r.URL.Path, raw)
		}
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/healthz":
			s.healthz(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities":
			s.caps(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/version":
			s.version(w)
		default:
			t.Errorf("doctor made an unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
}

func (s *doctorStub) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// configureDoctor points the CLI at server with the given credential state.
// writeConfig controls whether a config file exists on disk at all, which is the
// first-run distinction REQ-002 turns on.
func configureDoctor(t *testing.T, server, token string, writeConfig bool) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if writeConfig {
		if err := os.WriteFile(cfgPath, []byte("server: "+server+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MEM_CONFIG", cfgPath)
	t.Setenv("MEM_SERVER", server)
	t.Setenv("MEM_WORKSPACE", "")
	t.Setenv("MEM_TOKEN", token)
}

// execDoctor runs `mem doctor` with args and returns what it printed on stdout,
// what it printed on stderr (cobra's own error and usage text), and the error.
// main.go merges the two, but the report and cobra's noise are different surfaces
// and the assertions need to tell them apart.
func execDoctor(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"doctor"}, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func decodeReport(t *testing.T, out string) doctorReport {
	t.Helper()
	// A failing command's output buffer also carries cobra's own "Error:" and
	// usage block: cobra writes them via OutOrStderr, which is this same writer
	// when a test routes output into a buffer. In production the report is on
	// stdout and cobra's noise is on stderr. Decoding the first JSON value keeps
	// the assertion about the report itself.
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(out)))
	var rep doctorReport
	if err := dec.Decode(&rep); err != nil {
		t.Fatalf("decode doctor json: %v\n%s", err, out)
	}
	return rep
}

func checkByName(t *testing.T, rep doctorReport, name string) doctorCheck {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, rep.Checks)
	return doctorCheck{}
}

func cliCode(t *testing.T, err error) int {
	t.Helper()
	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %#v, want *cliError", err)
	}
	return ce.code
}

func TestDoctorHealthyReportsAllChecksAndExitsZero(t *testing.T) {
	stub := newDoctorStub()
	srv := stub.server(t)
	defer srv.Close()
	configureDoctor(t, srv.URL, "secret-token-value", true)
	t.Cleanup(func() { cliVersion = devCLIVersion })
	cliVersion = "0.1.0"

	out, _, err := execDoctor(t, "--format", "json")
	if err != nil {
		t.Fatalf("healthy doctor returned %v\n%s", err, out)
	}
	rep := decodeReport(t, out)
	if rep.ExitCode != exitOK {
		t.Errorf("report exit_code = %d, want 0", rep.ExitCode)
	}
	want := []string{"server_reachability", "credential", "workspace", "version_skew"}
	if len(rep.Checks) != len(want) {
		t.Fatalf("checks = %d, want %d: %+v", len(rep.Checks), len(want), rep.Checks)
	}
	for i, name := range want {
		if rep.Checks[i].Name != name {
			t.Errorf("check %d = %q, want %q", i, rep.Checks[i].Name, name)
		}
		if rep.Checks[i].Status != doctorOK {
			t.Errorf("%s status = %q (%s), want ok", name, rep.Checks[i].Status, rep.Checks[i].Detail)
		}
	}
	// AC-002: exactly the three read probes, in order.
	if got, wantReq := stub.seen(), []string{"GET /healthz", "GET /v1/capabilities", "GET /v1/version"}; strings.Join(got, ",") != strings.Join(wantReq, ",") {
		t.Errorf("requests = %v, want %v", got, wantReq)
	}
	if strings.Contains(out, "secret-token-value") {
		t.Errorf("report leaked the token value:\n%s", out)
	}
}

func TestDoctorUnreachableServer(t *testing.T) {
	closed := httptest.NewServer(nil)
	addr := closed.URL
	closed.Close()
	configureDoctor(t, addr, "tok", true)

	out, _, err := execDoctor(t, "--format", "json")
	if err == nil {
		t.Fatalf("doctor should exit non-zero for an unreachable server\n%s", out)
	}
	if code := cliCode(t, err); code != exitProvider {
		t.Fatalf("exit code = %d, want %d", code, exitProvider)
	}
	rep := decodeReport(t, out)
	c := checkByName(t, rep, "server_reachability")
	if c.Status != doctorFail || c.ExitCode != exitProvider {
		t.Errorf("reachability = %s/%d, want fail/%d", c.Status, c.ExitCode, exitProvider)
	}
	// The hint must name the documented container path, not a host recipe.
	if !strings.Contains(c.Hint, "deploy/compose") || !strings.Contains(c.Hint, "docs/DEPLOYMENT.md") {
		t.Errorf("hint = %q, want the documented deployment path", c.Hint)
	}
	for _, name := range []string{"workspace", "version_skew"} {
		got := checkByName(t, rep, name)
		if got.Status != doctorSkipped {
			t.Errorf("%s = %s, want skipped", name, got.Status)
		}
		if !strings.Contains(got.Detail, "server_reachability") {
			t.Errorf("%s detail = %q, want it to name the blocking check", name, got.Detail)
		}
	}
}

func TestDoctorMissingCredential(t *testing.T) {
	stub := newDoctorStub()
	srv := stub.server(t)
	defer srv.Close()
	configureDoctor(t, srv.URL, "", false) // no config file: first run

	out, _, err := execDoctor(t, "--format", "json")
	if err == nil {
		t.Fatalf("doctor should exit non-zero without a credential\n%s", out)
	}
	if code := cliCode(t, err); code != exitAuth {
		t.Fatalf("exit code = %d, want %d", code, exitAuth)
	}
	rep := decodeReport(t, out)
	c := checkByName(t, rep, "credential")
	if c.Status != doctorFail || c.ExitCode != exitAuth {
		t.Errorf("credential = %s/%d, want fail/%d", c.Status, c.ExitCode, exitAuth)
	}
	if !strings.Contains(c.Hint, "mem auth login") {
		t.Errorf("hint = %q, want it to name `mem auth login`", c.Hint)
	}
	if !strings.Contains(c.Hint, "deploy/compose") {
		t.Errorf("hint = %q, want first-run guidance naming the documented path", c.Hint)
	}
	if got, wantReq := stub.seen(), "GET /healthz"; strings.Join(got, ",") != wantReq {
		t.Errorf("requests = %v, want only the health probe", got)
	}
}

// A machine that already has a config is not a first run: it must not be told to
// deploy a stack it is evidently already talking to.
func TestDoctorMissingCredentialOnConfiguredHost(t *testing.T) {
	stub := newDoctorStub()
	srv := stub.server(t)
	defer srv.Close()
	configureDoctor(t, srv.URL, "", true)

	out, _, err := execDoctor(t, "--format", "json")
	if err == nil {
		t.Fatalf("want non-zero exit\n%s", out)
	}
	c := checkByName(t, decodeReport(t, out), "credential")
	if !strings.Contains(c.Hint, "mem auth login") {
		t.Errorf("hint = %q, want the login step", c.Hint)
	}
	if strings.Contains(c.Hint, "deploy/compose") {
		t.Errorf("hint = %q, must not suggest deploying on an already-configured host", c.Hint)
	}
}

func TestDoctorNoWorkspaceSelected(t *testing.T) {
	stub := newDoctorStub()
	stub.caps = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"workspace":{"id":"","name":"","role":""}}`))
	}
	srv := stub.server(t)
	defer srv.Close()
	configureDoctor(t, srv.URL, "tok", true)

	out, _, err := execDoctor(t, "--format", "json")
	if err == nil {
		t.Fatalf("doctor should exit non-zero when no workspace resolves\n%s", out)
	}
	if code := cliCode(t, err); code != exitNotFound {
		t.Fatalf("exit code = %d, want %d", code, exitNotFound)
	}
	c := checkByName(t, decodeReport(t, out), "workspace")
	if c.Status != doctorFail || c.ExitCode != exitNotFound {
		t.Errorf("workspace = %s/%d, want fail/%d", c.Status, c.ExitCode, exitNotFound)
	}
}

func TestDoctorRejectedCredential(t *testing.T) {
	stub := newDoctorStub()
	stub.caps = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"missing_bearer","code":"unauthorized"}`))
	}
	srv := stub.server(t)
	defer srv.Close()
	configureDoctor(t, srv.URL, "expired-token", true)

	out, _, err := execDoctor(t, "--format", "json")
	if err == nil {
		t.Fatalf("doctor should exit non-zero on a rejected token\n%s", out)
	}
	if code := cliCode(t, err); code != exitAuth {
		t.Fatalf("exit code = %d, want %d", code, exitAuth)
	}
	c := checkByName(t, decodeReport(t, out), "workspace")
	if c.Status != doctorFail || c.ExitCode != exitAuth {
		t.Errorf("workspace = %s/%d, want fail/%d", c.Status, c.ExitCode, exitAuth)
	}
}

// TestDoctorQuotaIsItsOwnCode pins the 4 (plan/quota) arm of the SPEC §7.1 map.
func TestDoctorQuotaIsItsOwnCode(t *testing.T) {
	stub := newDoctorStub()
	stub.caps = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"quota_exceeded","code":"quota"}`))
	}
	srv := stub.server(t)
	defer srv.Close()
	configureDoctor(t, srv.URL, "tok", true)

	out, _, err := execDoctor(t, "--format", "json")
	if err == nil {
		t.Fatalf("want non-zero exit\n%s", out)
	}
	if code := cliCode(t, err); code != exitPlanQuota {
		t.Fatalf("exit code = %d, want %d\n%s", code, exitPlanQuota, out)
	}
}

func TestDoctorVersionSkew(t *testing.T) {
	stub := newDoctorStub()
	srv := stub.server(t)
	defer srv.Close()
	configureDoctor(t, srv.URL, "tok", true)
	t.Cleanup(func() { cliVersion = devCLIVersion })

	// A dev build does not know its own version, so the check must say the
	// comparison is impossible instead of claiming agreement.
	cliVersion = devCLIVersion
	out, _, err := execDoctor(t, "--format", "json")
	if err != nil {
		t.Fatalf("an advisory skew must not fail the run: %v\n%s", err, out)
	}
	c := checkByName(t, decodeReport(t, out), "version_skew")
	if c.Status != doctorWarn || !strings.Contains(c.Detail, "not computable") {
		t.Errorf("dev-build skew = %s (%s), want warn / not computable", c.Status, c.Detail)
	}

	cliVersion = "0.0.9"
	out, _, err = execDoctor(t, "--format", "json")
	if err != nil {
		t.Fatalf("skew should stay advisory: %v\n%s", err, out)
	}
	rep := decodeReport(t, out)
	c = checkByName(t, rep, "version_skew")
	if c.Status != doctorWarn || !strings.Contains(c.Detail, "0.0.9") || !strings.Contains(c.Detail, "0.1.0") {
		t.Errorf("skew = %s (%s), want both versions named", c.Status, c.Detail)
	}
	if c.ExitCode != exitOK {
		t.Errorf("skew exit contribution = %d, want 0 (advisory)", c.ExitCode)
	}
	if rep.ServerVersion != "0.1.0" {
		t.Errorf("server_version = %q, want 0.1.0", rep.ServerVersion)
	}

	cliVersion = "0.1.0"
	out, _, _ = execDoctor(t, "--format", "json")
	if c = checkByName(t, decodeReport(t, out), "version_skew"); c.Status != doctorOK {
		t.Errorf("matching skew = %s (%s), want ok", c.Status, c.Detail)
	}
}

func TestDoctorNeverPrintsSecretValues(t *testing.T) {
	const token = "sup3r-s3cret-token"
	stub := newDoctorStub()
	stub.caps = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"workspace_forbidden","code":"forbidden"}`))
	}
	srv := stub.server(t)
	defer srv.Close()

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	body := fmt.Sprintf("server: %s\nemail: ops@corp\ntoken: %s\nworkspace: w-1\n", srv.URL, token)
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEM_CONFIG", cfg)
	t.Setenv("MEM_SERVER", srv.URL)
	t.Setenv("MEM_TOKEN", token)
	t.Setenv("MEM_WORKSPACE", "")

	for _, format := range []string{"text", "json"} {
		stdout, stderr, _ := execDoctor(t, "--format", format)
		if stdout == "" {
			t.Fatalf("%s run produced no output", format)
		}
		for label, out := range map[string]string{"stdout": stdout, "stderr": stderr} {
			if strings.Contains(out, token) {
				t.Errorf("%s %s leaks the token value:\n%s", format, label, out)
			}
		}
	}
}

func TestRedactURLStripsUserinfo(t *testing.T) {
	const secret = "dsn-p4ssw0rd"
	got := redactURL("http://admin:" + secret + "@mem.internal:8787")
	if strings.Contains(got, secret) {
		t.Errorf("redactURL = %q, still carries the password", got)
	}
	if !strings.Contains(got, "REDACTED@mem.internal:8787") {
		t.Errorf("redactURL = %q, want the userinfo replaced and the host kept", got)
	}
	plain := "http://localhost:8787"
	if redactURL(plain) != plain {
		t.Errorf("redactURL(%q) = %q, want it unchanged", plain, redactURL(plain))
	}
}

func TestDoctorTextOutputIsAFixedOrderedList(t *testing.T) {
	stub := newDoctorStub()
	srv := stub.server(t)
	defer srv.Close()
	configureDoctor(t, srv.URL, "tok", true)
	t.Cleanup(func() { cliVersion = devCLIVersion })

	out, _, err := execDoctor(t)
	if err != nil {
		t.Fatalf("healthy doctor returned %v\n%s", err, out)
	}
	names := []string{"server_reachability", "credential", "workspace", "version_skew"}
	prev := -1
	for _, n := range names {
		at := strings.Index(out, n)
		if at < 0 {
			t.Fatalf("text output missing %q:\n%s", n, out)
		}
		if at < prev {
			t.Errorf("check %q printed out of order:\n%s", n, out)
		}
		prev = at
	}
	if !strings.Contains(out, "mem doctor (mem.doctor v1)") {
		t.Errorf("text output missing the contract header:\n%s", out)
	}
	if strings.Contains(out, `"checks"`) {
		t.Errorf("text output contains JSON:\n%s", out)
	}
}

func TestDoctorRejectsBadFlags(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEM_CONFIG", filepath.Join(dir, "missing.yaml"))
	t.Setenv("MEM_TOKEN", "tok")

	if _, _, err := execDoctor(t, "--format", "yaml"); err == nil {
		t.Error("--format yaml should be rejected")
	}
	if _, _, err := execDoctor(t, "--timeout", "0s"); err == nil {
		t.Error("--timeout 0s should be rejected")
	}
}

// doctorSchema mirrors the parts of docs/schemas/mem-doctor.v1.schema.json that
// this test can enforce without a draft-2020-12 evaluator: required keys, the
// closed enums, key admission (additionalProperties:false) and the fixed check
// order.
type doctorSchema struct {
	Required   []string                    `json:"required"`
	Properties map[string]doctorSchemaNode `json:"properties"`
	Defs       map[string]doctorSchemaNode `json:"$defs"`
}

type doctorSchemaNode struct {
	Type        string                      `json:"type"`
	Enum        []json.RawMessage           `json:"enum"`
	Const       json.RawMessage             `json:"const"`
	Required    []string                    `json:"required"`
	Properties  map[string]doctorSchemaNode `json:"properties"`
	PrefixItems []json.RawMessage           `json:"prefixItems"`
}

func loadDoctorSchema(t *testing.T) doctorSchema {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "schemas", "mem-doctor.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s doctorSchema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("checked-in schema is not parseable: %v", err)
	}
	if len(s.Required) == 0 || len(s.Properties) == 0 {
		t.Fatalf("schema did not declare required keys or properties: %s", b)
	}
	if s.Defs["check"].Type != "object" {
		t.Fatalf("schema missing $defs.check object: %s", b)
	}
	return s
}

// TestDoctorJSONMatchesCheckedInSchema is AC-003.
func TestDoctorJSONMatchesCheckedInSchema(t *testing.T) {
	stub := newDoctorStub()
	srv := stub.server(t)
	defer srv.Close()
	configureDoctor(t, srv.URL, "tok", true)
	t.Cleanup(func() { cliVersion = devCLIVersion })
	cliVersion = "0.1.0"

	out, _, err := execDoctor(t, "--format", "json")
	if err != nil {
		t.Fatalf("healthy doctor returned %v\n%s", err, out)
	}
	schema := loadDoctorSchema(t)
	validateDoctorDoc(t, schema, []byte(out))

	// httptest assigns an ephemeral port, which the report echoes in two places
	// (server and the reachability detail). The golden pins everything except
	// that, so drift in shape, order, wording or codes still fails loudly.
	normalized := strings.ReplaceAll(out, srv.URL, "http://127.0.0.1:PORT")

	golden := filepath.Join("testdata", "doctor_healthy.golden.json")
	if os.Getenv("MEM_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(normalized), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v (create it with MEM_UPDATE_GOLDEN=1 go test ./cmd/mem/ -run TestDoctorJSON)", err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(normalized) {
		t.Errorf("doctor json drifted from %s\n--- want ---\n%s\n--- got ---\n%s", golden, want, normalized)
	}
}

func validateDoctorDoc(t *testing.T, s doctorSchema, doc []byte) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(doc, &obj); err != nil {
		t.Fatalf("report is not a JSON object: %v", err)
	}
	for _, req := range s.Required {
		if _, ok := obj[req]; !ok {
			t.Errorf("report missing required key %q", req)
		}
	}
	for key := range obj {
		if _, ok := s.Properties[key]; !ok {
			t.Errorf("report has key %q, which the schema forbids (additionalProperties:false)", key)
		}
	}
	for _, key := range []string{"contract", "schema_version"} {
		if !nodeAllows(s.Properties[key], obj[key]) {
			t.Errorf("%s = %s, outside the schema's const", key, obj[key])
		}
	}
	if !nodeAllows(s.Properties["exit_code"], obj["exit_code"]) {
		t.Errorf("exit_code = %s, outside the SPEC 7.1 set", obj["exit_code"])
	}

	var checks []map[string]json.RawMessage
	if err := json.Unmarshal(obj["checks"], &checks); err != nil {
		t.Fatalf("checks is not an array: %v", err)
	}
	if len(checks) != len(s.Properties["checks"].PrefixItems) {
		t.Fatalf("checks length = %d, want %d", len(checks), len(s.Properties["checks"].PrefixItems))
	}
	def := s.Defs["check"]
	for i, c := range checks {
		for _, req := range def.Required {
			if _, ok := c[req]; !ok {
				t.Errorf("checks[%d] missing required key %q", i, req)
			}
		}
		for key := range c {
			if _, ok := def.Properties[key]; !ok {
				t.Errorf("checks[%d] has key %q the schema forbids", i, key)
			}
		}
		for _, field := range []string{"name", "status", "exit_code"} {
			if !nodeAllows(def.Properties[field], c[field]) {
				t.Errorf("checks[%d].%s = %s, outside the schema's closed enum", i, field, c[field])
			}
		}
		// prefixItems pins the order, so a reordered report fails here.
		var slot struct {
			AllOf []struct {
				Properties map[string]doctorSchemaNode `json:"properties"`
			} `json:"allOf"`
		}
		if err := json.Unmarshal(s.Properties["checks"].PrefixItems[i], &slot); err != nil {
			t.Fatalf("prefixItems[%d] unreadable: %v", i, err)
		}
		for _, sub := range slot.AllOf {
			if want, ok := sub.Properties["name"]; ok && !nodeAllows(want, c["name"]) {
				t.Errorf("checks[%d].name = %s, want %s (order is part of the contract)", i, c["name"], want.Enum)
			}
		}
	}
}

// nodeAllows reports whether raw satisfies a leaf schema doctorSchemaNode that constrains by
// const or enum. A leaf with neither declares no value constraint.
func nodeAllows(n doctorSchemaNode, raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	if len(n.Const) > 0 {
		return strings.TrimSpace(string(n.Const)) == value
	}
	if len(n.Enum) > 0 {
		for _, e := range n.Enum {
			if strings.TrimSpace(string(e)) == value {
				return true
			}
		}
		return false
	}
	return true
}

// TestNotLoggedInGuidance is REQ-002 on an existing command surface: the hint
// that used to stop at `mem auth login` must additionally name the documented
// deployment path, but only when no credential exists at all.
func TestNotLoggedInGuidance(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	existing := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(existing, []byte("server: http://127.0.0.1:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		cfgPath string
		want    []string
		deny    string
	}{
		{name: "first run", cfgPath: missing, want: []string{"mem auth login", "deploy/compose", "docs/DEPLOYMENT.md"}},
		{name: "configured but logged out", cfgPath: existing, want: []string{"mem auth login"}, deny: "deploy/compose"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MEM_CONFIG", tc.cfgPath)
			t.Setenv("MEM_TOKEN", "")
			t.Setenv("MEM_SERVER", "")
			var out bytes.Buffer
			root := newRootCmd()
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"search", "fy27 recruiting"})
			err := root.Execute()
			var code int
			if code = cliCode(t, err); code != exitAuth {
				t.Fatalf("exit code = %d, want %d", code, exitAuth)
			}
			ce := err.(*cliError)
			for _, want := range tc.want {
				if !strings.Contains(ce.hint, want) {
					t.Errorf("hint = %q, want it to name %q", ce.hint, want)
				}
			}
			if tc.deny != "" && strings.Contains(ce.hint, tc.deny) {
				t.Errorf("hint = %q, must not suggest deploying where a config exists", ce.hint)
			}
		})
	}
}
