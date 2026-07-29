package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/modelcatalog"
)

type fakeLocalModelRuntime struct {
	calls    []string
	state    modelcatalog.RuntimeState
	stateErr error
	pullErr  error
	probeErr error
	onPull   func(modelcatalog.Profile)
	onProbe  func(modelcatalog.Profile)
}

func (f *fakeLocalModelRuntime) State(context.Context) (modelcatalog.RuntimeState, error) {
	f.calls = append(f.calls, "state")
	return f.state, f.stateErr
}

func (f *fakeLocalModelRuntime) Pull(
	_ context.Context,
	profile modelcatalog.Profile,
	progress func(modelcatalog.PullProgress) error,
) error {
	f.calls = append(f.calls, "pull")
	if f.onPull != nil {
		f.onPull(profile)
	}
	if f.pullErr != nil {
		return f.pullErr
	}
	if progress != nil {
		if err := progress(modelcatalog.PullProgress{Status: "pulling manifest"}); err != nil {
			return err
		}
		if err := progress(modelcatalog.PullProgress{
			Status: "downloading", Completed: 50, Total: 100,
		}); err != nil {
			return err
		}
		if err := progress(modelcatalog.PullProgress{
			Status: "downloading", Completed: 51, Total: 100,
		}); err != nil {
			return err
		}
		if err := progress(modelcatalog.PullProgress{Status: "success"}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeLocalModelRuntime) Probe(
	_ context.Context,
	profile modelcatalog.Profile,
) error {
	f.calls = append(f.calls, "probe")
	if f.onProbe != nil {
		f.onProbe(profile)
	}
	return f.probeErr
}

func TestModelCommandIsRegistered(t *testing.T) {
	root := newRootCmd()
	command, remaining, err := root.Find([]string{"model", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 || command.CommandPath() != "mem model list" {
		t.Fatalf("command = %q, remaining = %q", command.CommandPath(), remaining)
	}
}

func TestModelListJSONIncludesCatalogCompatibilityAndIntegrity(t *testing.T) {
	catalog := mustModelCatalog(t)
	nomic := mustModelProfile(t, catalog, "nomic-embed-text-v1.5-ollama")
	device := compatibleModelDevice()
	device.Ollama.Models = []modelcatalog.InstalledModel{{
		Name:   nomic.Model,
		Digest: nomic.ManifestDigest,
		Size:   nomic.ArtifactSizeBytes,
	}}
	deps := testModelDeps(catalog, device, &fakeLocalModelRuntime{})
	command := newModelCmdWithDeps(deps)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"list", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var output modelListOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, stdout.String())
	}
	if output.SchemaVersion != "mem.model-catalog/v1" ||
		output.CorpusDimension != 768 ||
		len(output.Profiles) != 4 {
		t.Fatalf("output = %#v", output)
	}
	nomicStatus, ok := findProfileStatus(output.Profiles, nomic.ID)
	if !ok ||
		nomicStatus.Installation.Status != "verified" ||
		!nomicStatus.Installation.DigestVerified {
		t.Fatalf("nomic status = %#v", nomicStatus)
	}
	bgeStatus, _ := findProfileStatus(output.Profiles, "bge-m3-567m-ollama")
	if bgeStatus.Compatibility.Status != "unavailable" ||
		bgeStatus.Compatibility.Compatible {
		t.Fatalf("bge status = %#v", bgeStatus)
	}
}

func TestModelRecommendRejectsIncompatibleHardwareWithoutDownload(t *testing.T) {
	catalog := mustModelCatalog(t)
	device := compatibleModelDevice()
	device.MemoryAvailable = 1
	runtimeCreated := false
	deps := testModelDeps(catalog, device, &fakeLocalModelRuntime{})
	deps.newRuntime = func(string) (localModelRuntime, error) {
		runtimeCreated = true
		return &fakeLocalModelRuntime{}, nil
	}
	command := newModelCmdWithDeps(deps)
	command.SetArgs([]string{"recommend", "--language", "zh"})
	err := command.Execute()
	assertLocalModelError(t, err, "no compatible local embedding profile")
	if runtimeCreated {
		t.Fatal("recommend created an installation runtime")
	}
}

func TestModelInstallNonInteractiveRequiresExplicitProfile(t *testing.T) {
	catalog := mustModelCatalog(t)
	runtimeCreated := false
	deps := testModelDeps(catalog, compatibleModelDevice(), &fakeLocalModelRuntime{})
	inspected := false
	deps.inspect = func(context.Context, string) modelcatalog.Device {
		inspected = true
		return compatibleModelDevice()
	}
	deps.isTerminal = func(io.Reader) bool { return false }
	deps.newRuntime = func(string) (localModelRuntime, error) {
		runtimeCreated = true
		return &fakeLocalModelRuntime{}, nil
	}
	command := newModelCmdWithDeps(deps)
	command.SetArgs([]string{"install"})
	err := command.Execute()
	assertLocalModelError(t, err, "profile ID is required in non-interactive mode")
	if runtimeCreated || inspected {
		t.Fatalf(
			"non-interactive validation touched runtime: created=%t inspected=%t",
			runtimeCreated,
			inspected,
		)
	}
}

func TestModelInstallRejectsUnknownProfileBeforeRuntimeInspection(t *testing.T) {
	catalog := mustModelCatalog(t)
	inspected := false
	deps := testModelDeps(catalog, compatibleModelDevice(), &fakeLocalModelRuntime{})
	deps.inspect = func(context.Context, string) modelcatalog.Device {
		inspected = true
		return compatibleModelDevice()
	}
	command := newModelCmdWithDeps(deps)
	command.SetArgs([]string{"install", "not-in-the-catalog"})
	err := command.Execute()
	assertLocalModelError(t, err, "unknown local model profile")
	if inspected {
		t.Fatal("unknown profile caused runtime inspection")
	}
}

func TestModelInstallRejectsUnavailableCatalogProfileBeforeRuntimeInspection(t *testing.T) {
	catalog := mustModelCatalog(t)
	inspected := false
	deps := testModelDeps(catalog, compatibleModelDevice(), &fakeLocalModelRuntime{})
	deps.inspect = func(context.Context, string) modelcatalog.Device {
		inspected = true
		return compatibleModelDevice()
	}
	command := newModelCmdWithDeps(deps)
	command.SetArgs([]string{"install", "bge-m3-567m-ollama"})
	err := command.Execute()
	assertLocalModelError(t, err, "is unavailable")
	if inspected {
		t.Fatal("unavailable profile caused runtime inspection")
	}
}

func TestModelInstallInteractiveSkipMakesNoRuntimeOrActivationCall(t *testing.T) {
	catalog := mustModelCatalog(t)
	runtimeCreated := false
	activated := false
	deps := testModelDeps(catalog, compatibleModelDevice(), &fakeLocalModelRuntime{})
	deps.isTerminal = func(io.Reader) bool { return true }
	deps.newRuntime = func(string) (localModelRuntime, error) {
		runtimeCreated = true
		return &fakeLocalModelRuntime{}, nil
	}
	deps.activate = func(context.Context, modelcatalog.Profile) (providerSetResp, error) {
		activated = true
		return providerSetResp{}, nil
	}
	command := newModelCmdWithDeps(deps)
	command.SetIn(strings.NewReader("0\n"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"install", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var output modelInstallOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Status != "skipped" || output.Activated || runtimeCreated || activated {
		t.Fatalf(
			"output = %#v, runtimeCreated=%t activated=%t",
			output,
			runtimeCreated,
			activated,
		)
	}
	if !strings.Contains(output.Diagnostic, "structured-memory lexical recall") {
		t.Fatalf("diagnostic = %q", output.Diagnostic)
	}
}

func TestModelInstallInteractiveSkipHumanOutputGolden(t *testing.T) {
	catalog := mustModelCatalog(t)
	deps := testModelDeps(
		catalog,
		compatibleModelDevice(),
		&fakeLocalModelRuntime{},
	)
	deps.isTerminal = func(io.Reader) bool { return true }
	command := newModelCmdWithDeps(deps)
	command.SetIn(strings.NewReader("0\n"))
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"install"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	const want = "status: skipped\n" +
		"digest_verified: false\n" +
		"activated: false\n" +
		"diagnostic: structured-memory lexical recall and model-independent operations remain available; no model activation was applied\n"
	if stdout.String() != want {
		t.Fatalf("human output:\n%s\nwant:\n%s", stdout.String(), want)
	}
}

func TestModelInstallCancellationNeverActivates(t *testing.T) {
	catalog := mustModelCatalog(t)
	runtime := &fakeLocalModelRuntime{pullErr: context.Canceled}
	activated := false
	deps := testModelDeps(catalog, compatibleModelDevice(), runtime)
	deps.activate = func(context.Context, modelcatalog.Profile) (providerSetResp, error) {
		activated = true
		return providerSetResp{}, nil
	}
	command := newModelCmdWithDeps(deps)
	command.SetArgs([]string{
		"install",
		"nomic-embed-text-v1.5-ollama",
		"--activate",
	})
	err := command.Execute()
	assertLocalModelError(t, err, "was cancelled")
	if activated || !reflect.DeepEqual(runtime.calls, []string{"pull"}) {
		t.Fatalf("activated=%t calls=%v", activated, runtime.calls)
	}
}

func TestModelInstallIncompatibleHardwareNeverPulls(t *testing.T) {
	catalog := mustModelCatalog(t)
	profile := mustModelProfile(t, catalog, "qwen3-embedding-0.6b-ollama")
	device := compatibleModelDevice()
	device.MemoryAvailable = 1 << 30
	runtime := &fakeLocalModelRuntime{}
	deps := testModelDeps(catalog, device, runtime)
	command := newModelCmdWithDeps(deps)
	command.SetArgs([]string{"install", profile.ID})
	err := command.Execute()
	assertLocalModelError(t, err, "is incompatible")
	if len(runtime.calls) != 0 {
		t.Fatalf("runtime calls = %v", runtime.calls)
	}
}

func TestModelInstallWrongDimensionNeverActivates(t *testing.T) {
	catalog := mustModelCatalog(t)
	profile := mustModelProfile(t, catalog, "qwen3-embedding-0.6b-ollama")
	runtime := verifiedFakeRuntime(profile)
	runtime.probeErr = errors.New("Ollama embed probe returned dimension 1024, want 768")
	activated := false
	deps := testModelDeps(catalog, compatibleModelDevice(), runtime)
	deps.activate = func(context.Context, modelcatalog.Profile) (providerSetResp, error) {
		activated = true
		return providerSetResp{}, nil
	}
	command := newModelCmdWithDeps(deps)
	command.SetArgs([]string{"install", profile.ID, "--activate"})
	err := command.Execute()
	assertLocalModelError(t, err, "dimension 1024, want 768")
	if activated || !reflect.DeepEqual(runtime.calls, []string{"pull", "state", "probe"}) {
		t.Fatalf("activated=%t calls=%v", activated, runtime.calls)
	}
}

func TestModelInstallDigestMismatchNeverProbesOrActivates(t *testing.T) {
	catalog := mustModelCatalog(t)
	profile := mustModelProfile(t, catalog, "nomic-embed-text-v1.5-ollama")
	runtime := &fakeLocalModelRuntime{
		state: modelcatalog.RuntimeState{
			Available: true,
			Models: []modelcatalog.InstalledModel{{
				Name:   profile.Model,
				Digest: "sha256:" + strings.Repeat("f", 64),
			}},
		},
	}
	activated := false
	deps := testModelDeps(catalog, compatibleModelDevice(), runtime)
	deps.activate = func(context.Context, modelcatalog.Profile) (providerSetResp, error) {
		activated = true
		return providerSetResp{}, nil
	}
	command := newModelCmdWithDeps(deps)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"install", profile.ID, "--activate"})
	err := command.Execute()
	assertLocalModelError(t, err, "failed integrity verification")
	if activated || !reflect.DeepEqual(runtime.calls, []string{"pull", "state"}) {
		t.Fatalf("activated=%t calls=%v", activated, runtime.calls)
	}
}

func TestModelInstallPullProbeThenCanonicalActivation(t *testing.T) {
	clearCLIOverrides(t)
	catalog := mustModelCatalog(t)
	profile := mustModelProfile(
		t,
		catalog,
		"granite-embedding-278m-multilingual-ollama",
	)
	runtime := verifiedFakeRuntime(profile)
	var serverCalls []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serverCalls = append(serverCalls, request.Method+" "+request.URL.Path)
		if got := request.Header.Get("Authorization"); got != "Bearer model-token" {
			t.Errorf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["spec"] != providerSpec(profile) {
			t.Errorf("body = %#v", body)
		}
		_, _ = io.WriteString(
			writer,
			`{"setting":{"kind":"embedding","spec":"ollama:granite-embedding:278m"},"dim_migration_ok":true}`,
		)
	}))
	defer server.Close()
	t.Setenv("MEM_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("MEM_SERVER", server.URL)
	t.Setenv("MEM_TOKEN", "model-token")

	deps := testModelDeps(catalog, compatibleModelDevice(), runtime)
	deps.activate = activateLocalModelProfile
	command := newModelCmdWithDeps(deps)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"install", profile.ID, "--activate", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var output modelInstallOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Status != "activated" ||
		!output.DigestVerified ||
		!output.Activated ||
		output.ProviderSpec != providerSpec(profile) {
		t.Fatalf("output = %#v", output)
	}
	wantJSON := "{\n" +
		"  \"profile_id\": \"granite-embedding-278m-multilingual-ollama\",\n" +
		"  \"model\": \"granite-embedding:278m\",\n" +
		"  \"status\": \"activated\",\n" +
		"  \"digest_verified\": true,\n" +
		"  \"activated\": true,\n" +
		"  \"provider_spec\": \"ollama:granite-embedding:278m\",\n" +
		"  \"diagnostic\": \"installed, verified, and accepted by the canonical server provider probe\"\n" +
		"}\n"
	if stdout.String() != wantJSON {
		t.Fatalf("JSON output:\n%s\nwant:\n%s", stdout.String(), wantJSON)
	}
	if !reflect.DeepEqual(runtime.calls, []string{"pull", "state", "probe"}) {
		t.Fatalf("runtime calls = %v", runtime.calls)
	}
	if !reflect.DeepEqual(serverCalls, []string{"PUT /v1/providers/embedding"}) {
		t.Fatalf("server calls = %v", serverCalls)
	}
	progress := stderr.String()
	if strings.Count(progress, "downloading") != 1 ||
		!strings.Contains(progress, "success") {
		t.Fatalf("bounded progress = %q", progress)
	}
}

func TestModelActivateFailsClosedOnDigestMismatch(t *testing.T) {
	catalog := mustModelCatalog(t)
	profile := mustModelProfile(t, catalog, "nomic-embed-text-v1.5-ollama")
	device := compatibleModelDevice()
	device.Ollama.Models = []modelcatalog.InstalledModel{{
		Name:   profile.Model,
		Digest: "sha256:" + strings.Repeat("f", 64),
	}}
	runtime := &fakeLocalModelRuntime{}
	activated := false
	deps := testModelDeps(catalog, device, runtime)
	deps.activate = func(context.Context, modelcatalog.Profile) (providerSetResp, error) {
		activated = true
		return providerSetResp{}, nil
	}
	command := newModelCmdWithDeps(deps)
	command.SetArgs([]string{"activate", profile.ID})
	err := command.Execute()
	assertLocalModelError(t, err, "not integrity-verified")
	if activated || len(runtime.calls) != 0 {
		t.Fatalf("activated=%t runtime calls=%v", activated, runtime.calls)
	}
}

func TestModelActivatePreservesAuthenticationExitCode(t *testing.T) {
	clearCLIOverrides(t)
	t.Setenv("MEM_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
	t.Setenv("MEM_TOKEN", "")
	catalog := mustModelCatalog(t)
	profile := mustModelProfile(t, catalog, "nomic-embed-text-v1.5-ollama")
	device := compatibleModelDevice()
	device.Ollama.Models = []modelcatalog.InstalledModel{{
		Name:   profile.Model,
		Digest: profile.ManifestDigest,
	}}
	runtime := verifiedFakeRuntime(profile)
	deps := testModelDeps(catalog, device, runtime)
	deps.activate = activateLocalModelProfile
	command := newModelCmdWithDeps(deps)
	command.SetArgs([]string{"activate", profile.ID})
	err := command.Execute()
	var cliErr *cliError
	if !errors.As(err, &cliErr) || cliErr.code != 3 {
		t.Fatalf("error = %#v, want auth cliError", err)
	}
	if !reflect.DeepEqual(runtime.calls, []string{"probe"}) {
		t.Fatalf("runtime calls = %v", runtime.calls)
	}
}

func TestModelProgressEscapesTerminalControlsAndBoundsLines(t *testing.T) {
	var output bytes.Buffer
	progress := boundedProgressWriter(&output)
	for index := 0; index < 250; index++ {
		if err := progress(modelcatalog.PullProgress{
			Status: strings.Repeat("x", index%3) + "\x1b[31m" + string(rune('a'+index%26)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := progress(modelcatalog.PullProgress{Status: "success"}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(output.String(), '\x1b') {
		t.Fatalf("progress contains terminal escape: %q", output.String())
	}
	if lines := strings.Count(output.String(), "\n"); lines > 201 {
		t.Fatalf("progress emitted %d lines, want at most 201", lines)
	}
	if !strings.HasSuffix(output.String(), "success\n") {
		t.Fatalf("progress omitted terminal success: %q", output.String())
	}
}

func testModelDeps(
	catalog modelcatalog.Catalog,
	device modelcatalog.Device,
	runtime localModelRuntime,
) modelCommandDeps {
	return modelCommandDeps{
		loadCatalog: func() (modelcatalog.Catalog, error) { return catalog, nil },
		inspect:     func(context.Context, string) modelcatalog.Device { return device },
		newRuntime: func(string) (localModelRuntime, error) {
			return runtime, nil
		},
		isTerminal: func(io.Reader) bool { return false },
		activate: func(context.Context, modelcatalog.Profile) (providerSetResp, error) {
			return providerSetResp{}, nil
		},
	}
}

func compatibleModelDevice() modelcatalog.Device {
	return modelcatalog.Device{
		OperatingSystem: "linux",
		Architecture:    "amd64",
		MemoryAvailable: 16 << 30,
		DiskAvailable:   20 << 30,
		Ollama: modelcatalog.RuntimeState{
			Available: true,
			BaseURL:   defaultOllamaBaseURL,
			Models:    []modelcatalog.InstalledModel{},
		},
		DetectionWarnings: []string{},
	}
}

func verifiedFakeRuntime(profile modelcatalog.Profile) *fakeLocalModelRuntime {
	return &fakeLocalModelRuntime{
		state: modelcatalog.RuntimeState{
			Available: true,
			BaseURL:   defaultOllamaBaseURL,
			Models: []modelcatalog.InstalledModel{{
				Name:   profile.Model,
				Digest: profile.ManifestDigest,
				Size:   profile.ArtifactSizeBytes,
			}},
		},
	}
}

func mustModelCatalog(t *testing.T) modelcatalog.Catalog {
	t.Helper()
	catalog, err := modelcatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func mustModelProfile(
	t *testing.T,
	catalog modelcatalog.Catalog,
	id string,
) modelcatalog.Profile {
	t.Helper()
	profile, ok := catalog.Find(id)
	if !ok {
		t.Fatalf("missing profile %q", id)
	}
	return profile
}

func assertLocalModelError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected command to fail")
	}
	var cliErr *cliError
	if !errors.As(err, &cliErr) {
		t.Fatalf("error type = %T, want *cliError: %v", err, err)
	}
	if cliErr.code != 5 || !strings.Contains(cliErr.msg, want) {
		t.Fatalf("cli error = %#v, want message containing %q", cliErr, want)
	}
	if !strings.Contains(cliErr.hint, "model-independent operations remain available") {
		t.Fatalf("hint = %q", cliErr.hint)
	}
}
