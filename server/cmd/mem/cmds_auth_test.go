package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAuthCommandTreeUsesCanonicalNamespaceAndHidesLegacyAliases(t *testing.T) {
	root := newRootCmd()

	for _, path := range [][]string{
		{"auth", "login"},
		{"auth", "logout"},
		{"auth", "status"},
		{"auth", "token", "create"},
		{"auth", "token", "list"},
		{"auth", "token", "revoke"},
	} {
		command, remaining, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %q: %v", strings.Join(path, " "), err)
		}
		if len(remaining) != 0 || command.CommandPath() != "mem "+strings.Join(path, " ") {
			t.Fatalf(
				"find %q = command %q, remaining %q",
				strings.Join(path, " "),
				command.CommandPath(),
				remaining,
			)
		}
	}

	for _, test := range []struct {
		path        []string
		replacement string
	}{
		{path: []string{"login"}, replacement: "mem auth login"},
		{path: []string{"logout"}, replacement: "mem auth logout"},
		{path: []string{"token"}, replacement: "mem auth token"},
		{path: []string{"token", "create"}, replacement: "mem auth token create"},
		{path: []string{"token", "list"}, replacement: "mem auth token list"},
		{path: []string{"token", "revoke"}, replacement: "mem auth token revoke"},
	} {
		command, _, err := root.Find(test.path)
		if err != nil {
			t.Fatalf("find legacy %q: %v", strings.Join(test.path, " "), err)
		}
		if !strings.Contains(command.Deprecated, test.replacement) {
			t.Fatalf(
				"legacy %q deprecation = %q, want replacement %q",
				strings.Join(test.path, " "),
				command.Deprecated,
				test.replacement,
			)
		}
		if len(test.path) == 1 && !command.Hidden {
			t.Fatalf("legacy top-level command %q is visible", test.path[0])
		}
	}

	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	if !strings.Contains(help, "\n  auth ") {
		t.Fatalf("root help does not expose auth:\n%s", help)
	}
	for _, legacy := range []string{"login", "logout", "token"} {
		if strings.Contains(help, "\n  "+legacy+" ") {
			t.Fatalf("root help exposes legacy %q:\n%s", legacy, help)
		}
	}
}

func TestAuthStatusVerifiesTokenAndPrintsJSON(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/capabilities" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer status-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"deployment_mode":"self_hosted",
			"registration_mode":"invite",
			"workspace":{
				"id":"workspace-current",
				"name":"Mem Team",
				"role":"admin"
			},
			"permissions":{
				"read":true,
				"search":true,
				"write":true,
				"delete":false
			}
		}`))
	}))
	defer server.Close()

	t.Setenv("MEM_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("MEM_SERVER", server.URL)
	t.Setenv("MEM_TOKEN", "status-token")

	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"auth", "status", "--format", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count = %d", requestCount.Load())
	}

	var status authStatus
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode status JSON: %v\n%s", err, stdout.String())
	}
	if !status.LoggedIn ||
		status.Server != server.URL ||
		status.Workspace.ID != "workspace-current" ||
		status.Workspace.Name != "Mem Team" ||
		status.Workspace.Role != "admin" ||
		!status.Permissions["write"] ||
		status.Permissions["delete"] {
		t.Fatalf("status = %#v", status)
	}
}

func TestAuthStatusWithoutTokenReturnsAuthExitCode(t *testing.T) {
	t.Setenv("MEM_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("MEM_TOKEN", "")

	root := newRootCmd()
	root.SetArgs([]string{"auth", "status"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected auth status to fail without a token")
	}
	var cliErr *cliError
	if !errors.As(err, &cliErr) {
		t.Fatalf("error type = %T, want *cliError", err)
	}
	if cliErr.code != 3 || cliErr.hint != "run `mem auth login` first" {
		t.Fatalf("cli error = %#v", cliErr)
	}
}
