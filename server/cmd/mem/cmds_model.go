package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/PeterGuy326/mem/server/internal/modelcatalog"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const defaultOllamaBaseURL = "http://localhost:11434"

type localModelRuntime interface {
	State(context.Context) (modelcatalog.RuntimeState, error)
	Pull(
		context.Context,
		modelcatalog.Profile,
		func(modelcatalog.PullProgress) error,
	) error
	Probe(context.Context, modelcatalog.Profile) error
}

type modelCommandDeps struct {
	loadCatalog func() (modelcatalog.Catalog, error)
	inspect     func(context.Context, string) modelcatalog.Device
	newRuntime  func(string) (localModelRuntime, error)
	isTerminal  func(io.Reader) bool
	activate    func(context.Context, modelcatalog.Profile) (providerSetResp, error)
}

type modelListOutput struct {
	SchemaVersion   string                       `json:"schema_version"`
	CatalogVersion  string                       `json:"catalog_version"`
	CorpusDimension int                          `json:"corpus_dimension"`
	Device          modelcatalog.Device          `json:"device"`
	Profiles        []modelcatalog.ProfileStatus `json:"profiles"`
}

type modelRecommendOutput struct {
	CatalogVersion  string                        `json:"catalog_version"`
	Language        string                        `json:"language"`
	Device          modelcatalog.Device           `json:"device"`
	Recommendations []modelcatalog.Recommendation `json:"recommendations"`
}

type modelInstallOutput struct {
	ProfileID      string `json:"profile_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Status         string `json:"status"`
	DigestVerified bool   `json:"digest_verified"`
	Activated      bool   `json:"activated"`
	ProviderSpec   string `json:"provider_spec,omitempty"`
	Diagnostic     string `json:"diagnostic"`
}

func newModelCmd() *cobra.Command {
	return newModelCmdWithDeps(defaultModelCommandDeps())
}

func newModelCmdWithDeps(deps modelCommandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Choose and verify a curated local embedding model",
		Long: `Inspect this machine, choose a catalog profile, explicitly install it
through Ollama, verify the pinned artifact and 768-dimensional output, then
activate it separately. No model is downloaded by list or recommend.`,
	}
	cmd.AddCommand(newModelListCmd(deps))
	cmd.AddCommand(newModelRecommendCmd(deps))
	cmd.AddCommand(newModelInstallCmd(deps))
	cmd.AddCommand(newModelActivateCmd(deps))
	return cmd
}

func defaultModelCommandDeps() modelCommandDeps {
	return modelCommandDeps{
		loadCatalog: modelcatalog.Load,
		inspect:     modelcatalog.Inspect,
		newRuntime: func(baseURL string) (localModelRuntime, error) {
			client := &http.Client{
				Transport: &http.Transport{
					Proxy:                 http.ProxyFromEnvironment,
					ResponseHeaderTimeout: 15 * time.Second,
				},
			}
			return modelcatalog.NewOllamaClient(baseURL, client)
		},
		isTerminal: func(reader io.Reader) bool {
			file, ok := reader.(*os.File)
			return ok && term.IsTerminal(int(file.Fd()))
		},
		activate: activateLocalModelProfile,
	}
}

func newModelListCmd(deps modelCommandDeps) *cobra.Command {
	var baseURL string
	var format string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List catalog profiles, compatibility, and installed state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			asJSON, err := modelOutputIsJSON(format, jsonOutput)
			if err != nil {
				return err
			}
			catalog, err := deps.loadCatalog()
			if err != nil {
				return localModelFailure("load local model catalog", err)
			}
			device := deps.inspect(cmd.Context(), baseURL)
			output := modelListOutput{
				SchemaVersion:   catalog.SchemaVersion,
				CatalogVersion:  catalog.CatalogVersion,
				CorpusDimension: catalog.CorpusDimension,
				Device:          device,
				Profiles:        modelcatalog.Evaluate(catalog, device),
			}
			if asJSON {
				return writeModelJSON(cmd.OutOrStdout(), output)
			}
			return writeModelList(cmd.OutOrStdout(), output)
		},
	}
	addModelRuntimeFlags(cmd, &baseURL)
	addModelOutputFlags(cmd, &format, &jsonOutput)
	return cmd
}

func newModelRecommendCmd(deps modelCommandDeps) *cobra.Command {
	var baseURL string
	var language string
	var format string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "recommend",
		Short: "Rank compatible profiles using local resources and language needs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			asJSON, err := modelOutputIsJSON(format, jsonOutput)
			if err != nil {
				return err
			}
			language = strings.ToLower(strings.TrimSpace(language))
			if language == "" {
				language = "auto"
			}
			catalog, err := deps.loadCatalog()
			if err != nil {
				return localModelFailure("load local model catalog", err)
			}
			device := deps.inspect(cmd.Context(), baseURL)
			recommendations := modelcatalog.Recommend(
				modelcatalog.Evaluate(catalog, device),
				language,
				device,
			)
			if len(recommendations) == 0 {
				return localModelFailure(
					"no compatible local embedding profile was found",
					errors.New("check Ollama availability, language, memory, disk, architecture, and catalog dimension"),
				)
			}
			output := modelRecommendOutput{
				CatalogVersion:  catalog.CatalogVersion,
				Language:        language,
				Device:          device,
				Recommendations: recommendations,
			}
			if asJSON {
				return writeModelJSON(cmd.OutOrStdout(), output)
			}
			return writeRecommendations(cmd.OutOrStdout(), output)
		},
	}
	addModelRuntimeFlags(cmd, &baseURL)
	addModelOutputFlags(cmd, &format, &jsonOutput)
	cmd.Flags().StringVar(
		&language,
		"language",
		"auto",
		"required retrieval language (for example en, zh, ja, or auto)",
	)
	return cmd
}

func newModelInstallCmd(deps modelCommandDeps) *cobra.Command {
	var baseURL string
	var format string
	var jsonOutput bool
	var activate bool
	cmd := &cobra.Command{
		Use:   "install [profile-id]",
		Short: "Explicitly pull, integrity-check, and probe one catalog profile",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := modelOutputIsJSON(format, jsonOutput)
			if err != nil {
				return err
			}
			catalog, err := deps.loadCatalog()
			if err != nil {
				return localModelFailure("load local model catalog", err)
			}
			var profile modelcatalog.Profile
			if len(args) == 1 {
				var ok bool
				profile, ok = catalog.Find(args[0])
				if !ok {
					return localModelFailure(
						fmt.Sprintf("unknown local model profile %q", args[0]),
						errors.New("run `mem model list` to see catalog profile IDs"),
					)
				}
				if !profile.Installable {
					return localModelFailure(
						fmt.Sprintf("profile %q is unavailable", profile.ID),
						errors.New(profile.UnavailableReason),
					)
				}
			} else {
				if !deps.isTerminal(cmd.InOrStdin()) {
					return localModelFailure(
						"a profile ID is required in non-interactive mode",
						errors.New("run `mem model install <profile-id>`; no model was downloaded"),
					)
				}
			}

			device := deps.inspect(cmd.Context(), baseURL)
			statuses := modelcatalog.Evaluate(catalog, device)
			if len(args) == 0 {
				selected, skipped, err := selectModelProfile(
					cmd.InOrStdin(),
					cmd.ErrOrStderr(),
					statuses,
				)
				if err != nil {
					return localModelFailure("select a local model profile", err)
				}
				if skipped {
					return writeInstallResult(cmd.OutOrStdout(), asJSON, modelInstallOutput{
						Status:         "skipped",
						Diagnostic:     modelIndependentDiagnostic(),
						DigestVerified: false,
						Activated:      false,
					})
				}
				profile = selected
			}

			status, ok := findProfileStatus(statuses, profile.ID)
			if !ok {
				return localModelFailure("evaluate local model compatibility", errors.New("profile status is missing"))
			}
			if !status.Compatibility.Compatible {
				return localModelFailure(
					fmt.Sprintf("profile %q is %s", profile.ID, status.Compatibility.Status),
					errors.New(strings.Join(status.Compatibility.Reasons, "; ")),
				)
			}

			runtimeClient, err := deps.newRuntime(baseURL)
			if err != nil {
				return localModelFailure("connect to the local Ollama runtime", err)
			}
			if status.Installation.Status != "verified" {
				progress := boundedProgressWriter(cmd.ErrOrStderr())
				if err := runtimeClient.Pull(cmd.Context(), profile, progress); err != nil {
					if errors.Is(err, context.Canceled) {
						return localModelFailure(
							fmt.Sprintf("installation of %q was cancelled", profile.ID),
							errors.New("no activation was attempted"),
						)
					}
					return localModelFailure(fmt.Sprintf("install profile %q", profile.ID), err)
				}
			}

			runtimeState, err := runtimeClient.State(cmd.Context())
			if err != nil {
				return localModelFailure("verify the installed Ollama artifact", err)
			}
			installation := modelcatalog.InstallationFor(profile, runtimeState)
			if installation.Status != "verified" {
				return localModelFailure(
					fmt.Sprintf("installed artifact for %q failed integrity verification", profile.ID),
					fmt.Errorf(
						"catalog digest %s, runtime digest %s",
						profile.ManifestDigest,
						installation.ActualDigest,
					),
				)
			}
			if err := probeLocalProfile(cmd.Context(), runtimeClient, profile); err != nil {
				return localModelFailure(fmt.Sprintf("probe profile %q", profile.ID), err)
			}

			result := modelInstallOutput{
				ProfileID:      profile.ID,
				Model:          profile.Model,
				Status:         "verified",
				DigestVerified: true,
				Activated:      false,
				ProviderSpec:   providerSpec(profile),
				Diagnostic:     "installed and verified; activation remains explicit",
			}
			if activate {
				if _, err := deps.activate(cmd.Context(), profile); err != nil {
					return localModelFailure(
						fmt.Sprintf("profile %q was installed but not activated", profile.ID),
						err,
					)
				}
				result.Activated = true
				result.Status = "activated"
				result.Diagnostic = "installed, verified, and accepted by the canonical server provider probe"
			}
			return writeInstallResult(cmd.OutOrStdout(), asJSON, result)
		},
	}
	addModelRuntimeFlags(cmd, &baseURL)
	addModelOutputFlags(cmd, &format, &jsonOutput)
	cmd.Flags().BoolVar(
		&activate,
		"activate",
		false,
		"after verification, explicitly ask memd to activate this embedding provider",
	)
	return cmd
}

func newModelActivateCmd(deps modelCommandDeps) *cobra.Command {
	var baseURL string
	var format string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "activate <profile-id>",
		Short: "Verify an installed profile, then activate it through memd",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := modelOutputIsJSON(format, jsonOutput)
			if err != nil {
				return err
			}
			catalog, err := deps.loadCatalog()
			if err != nil {
				return localModelFailure("load local model catalog", err)
			}
			profile, ok := catalog.Find(args[0])
			if !ok {
				return localModelFailure(
					fmt.Sprintf("unknown local model profile %q", args[0]),
					errors.New("run `mem model list` to see catalog profile IDs"),
				)
			}
			if !profile.Installable {
				return localModelFailure(
					fmt.Sprintf("profile %q is unavailable", profile.ID),
					errors.New(profile.UnavailableReason),
				)
			}
			device := deps.inspect(cmd.Context(), baseURL)
			status, ok := findProfileStatus(modelcatalog.Evaluate(catalog, device), profile.ID)
			if !ok {
				return localModelFailure("evaluate local model compatibility", errors.New("profile status is missing"))
			}
			if !status.Compatibility.Compatible {
				return localModelFailure(
					fmt.Sprintf("profile %q is incompatible", profile.ID),
					errors.New(strings.Join(status.Compatibility.Reasons, "; ")),
				)
			}
			if status.Installation.Status != "verified" {
				return localModelFailure(
					fmt.Sprintf("profile %q is not integrity-verified", profile.ID),
					fmt.Errorf("installation status is %s; run `mem model install %s`", status.Installation.Status, profile.ID),
				)
			}
			runtimeClient, err := deps.newRuntime(baseURL)
			if err != nil {
				return localModelFailure("connect to the local Ollama runtime", err)
			}
			if err := probeLocalProfile(cmd.Context(), runtimeClient, profile); err != nil {
				return localModelFailure(fmt.Sprintf("probe profile %q", profile.ID), err)
			}
			if _, err := deps.activate(cmd.Context(), profile); err != nil {
				return localModelFailure(fmt.Sprintf("activate profile %q", profile.ID), err)
			}
			return writeInstallResult(cmd.OutOrStdout(), asJSON, modelInstallOutput{
				ProfileID:      profile.ID,
				Model:          profile.Model,
				Status:         "activated",
				DigestVerified: true,
				Activated:      true,
				ProviderSpec:   providerSpec(profile),
				Diagnostic:     "installed artifact and 768-dimensional output were verified before memd activation",
			})
		},
	}
	addModelRuntimeFlags(cmd, &baseURL)
	addModelOutputFlags(cmd, &format, &jsonOutput)
	return cmd
}

func activateLocalModelProfile(
	ctx context.Context,
	profile modelcatalog.Profile,
) (providerSetResp, error) {
	cfg, err := resolveConfig("")
	if err != nil {
		return providerSetResp{}, err
	}
	if cfg.Token == "" {
		return providerSetResp{}, errNotLoggedIn()
	}
	var response providerSetResp
	client := newHTTPClient(cfg)
	err = fromAPIError(client.api.DoJSON(
		ctx,
		http.MethodPut,
		"/v1/providers/embedding",
		map[string]any{"spec": providerSpec(profile)},
		&response,
	))
	return response, err
}

func addModelRuntimeFlags(cmd *cobra.Command, baseURL *string) {
	defaultURL := strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL"))
	if defaultURL == "" {
		defaultURL = defaultOllamaBaseURL
	}
	cmd.Flags().StringVar(
		baseURL,
		"ollama-url",
		defaultURL,
		"Ollama base URL used by the local worker runtime",
	)
}

func addModelOutputFlags(cmd *cobra.Command, format *string, jsonOutput *bool) {
	cmd.Flags().StringVar(format, "format", "text", "output format: text|json")
	cmd.Flags().BoolVar(jsonOutput, "json", false, "emit stable machine-readable JSON")
}

func modelOutputIsJSON(format string, jsonOutput bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		return jsonOutput, nil
	case "json":
		return true, nil
	default:
		return false, fmt.Errorf("unsupported output format %q; use text or json", format)
	}
}

func providerSpec(profile modelcatalog.Profile) string {
	return profile.Runtime + ":" + profile.Model
}

func findProfileStatus(
	statuses []modelcatalog.ProfileStatus,
	id string,
) (modelcatalog.ProfileStatus, bool) {
	for _, status := range statuses {
		if status.Profile.ID == id {
			return status, true
		}
	}
	return modelcatalog.ProfileStatus{}, false
}

func selectModelProfile(
	input io.Reader,
	output io.Writer,
	statuses []modelcatalog.ProfileStatus,
) (modelcatalog.Profile, bool, error) {
	compatible := make([]modelcatalog.Profile, 0, len(statuses))
	fmt.Fprintln(output, "Compatible local embedding profiles:")
	for _, status := range statuses {
		if !status.Compatibility.Compatible {
			continue
		}
		compatible = append(compatible, status.Profile)
		fmt.Fprintf(
			output,
			"  %d) %s (%s, %s)\n",
			len(compatible),
			terminalSafe(status.Profile.DisplayName),
			terminalSafe(status.Profile.ID),
			formatBytes(status.Profile.ArtifactSizeBytes),
		)
	}
	if len(compatible) == 0 {
		return modelcatalog.Profile{}, false, errors.New("no compatible profiles are available")
	}
	fmt.Fprintln(output, "  0) Skip semantic model setup")
	fmt.Fprint(output, "Select a profile: ")
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return modelcatalog.Profile{}, false, err
	}
	selection, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || selection < 0 || selection > len(compatible) {
		return modelcatalog.Profile{}, false, fmt.Errorf(
			"invalid selection %q; enter 0 through %d",
			strings.TrimSpace(line),
			len(compatible),
		)
	}
	if selection == 0 {
		return modelcatalog.Profile{}, true, nil
	}
	return compatible[selection-1], false, nil
}

func probeLocalProfile(
	parent context.Context,
	runtimeClient localModelRuntime,
	profile modelcatalog.Profile,
) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	return runtimeClient.Probe(ctx, profile)
}

func boundedProgressWriter(output io.Writer) func(modelcatalog.PullProgress) error {
	const maxProgressLines = 200
	lastStatus := ""
	lastBucket := int64(-1)
	emitted := 0
	return func(progress modelcatalog.PullProgress) error {
		bucket := int64(-1)
		if progress.Total > 0 && progress.Completed >= 0 {
			bucket = (progress.Completed * 20 / progress.Total) * 5
		}
		if progress.Status == lastStatus && bucket == lastBucket {
			return nil
		}
		lastStatus = progress.Status
		lastBucket = bucket
		if emitted >= maxProgressLines && progress.Status != "success" {
			return nil
		}
		emitted++
		if bucket >= 0 {
			_, err := fmt.Fprintf(output, "%s: %d%%\n", terminalSafe(progress.Status), bucket)
			return err
		}
		_, err := fmt.Fprintln(output, terminalSafe(progress.Status))
		return err
	}
}

func writeModelJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeModelList(writer io.Writer, output modelListOutput) error {
	fmt.Fprintf(
		writer,
		"Catalog %s · corpus dimension %d · %s/%s\n",
		output.CatalogVersion,
		output.CorpusDimension,
		output.Device.OperatingSystem,
		output.Device.Architecture,
	)
	if !output.Device.Ollama.Available {
		fmt.Fprintf(
			writer,
			"Ollama unavailable at %s\n",
			terminalSafe(output.Device.Ollama.BaseURL),
		)
	}
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "PROFILE\tMODEL\tDIM\tDOWNLOAD\tCOMPATIBILITY\tINSTALLED")
	for _, status := range output.Profiles {
		fmt.Fprintf(
			table,
			"%s\t%s\t%d\t%s\t%s\t%s\n",
			terminalSafe(status.Profile.ID),
			terminalSafe(status.Profile.Model),
			status.Profile.ExpectedDimension,
			formatBytes(status.Profile.ArtifactSizeBytes),
			status.Compatibility.Status,
			status.Installation.Status,
		)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	for _, status := range output.Profiles {
		if status.Compatibility.Compatible {
			continue
		}
		fmt.Fprintf(
			writer,
			"- %s: %s\n",
			terminalSafe(status.Profile.ID),
			terminalSafe(strings.Join(status.Compatibility.Reasons, "; ")),
		)
	}
	for _, warning := range output.Device.DetectionWarnings {
		fmt.Fprintf(writer, "warning: %s\n", terminalSafe(warning))
	}
	return nil
}

func writeRecommendations(writer io.Writer, output modelRecommendOutput) error {
	fmt.Fprintf(writer, "Language: %s\n", terminalSafe(output.Language))
	for index, recommendation := range output.Recommendations {
		fmt.Fprintf(
			writer,
			"%d. %s (score %d)\n",
			index+1,
			terminalSafe(recommendation.ProfileID),
			recommendation.Score,
		)
		for _, reason := range recommendation.Rationale {
			fmt.Fprintf(writer, "   - %s\n", terminalSafe(reason))
		}
	}
	return nil
}

func writeInstallResult(
	writer io.Writer,
	asJSON bool,
	result modelInstallOutput,
) error {
	if asJSON {
		return writeModelJSON(writer, result)
	}
	fmt.Fprintf(writer, "status: %s\n", terminalSafe(result.Status))
	if result.ProfileID != "" {
		fmt.Fprintf(
			writer,
			"profile: %s\nmodel: %s\n",
			terminalSafe(result.ProfileID),
			terminalSafe(result.Model),
		)
	}
	fmt.Fprintf(
		writer,
		"digest_verified: %t\nactivated: %t\ndiagnostic: %s\n",
		result.DigestVerified,
		result.Activated,
		terminalSafe(result.Diagnostic),
	)
	return nil
}

func localModelFailure(action string, cause error) error {
	var cliErr *cliError
	if errors.As(cause, &cliErr) {
		return cliErr
	}
	message := terminalSafe(action)
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message += ": " + terminalSafe(cause.Error())
	}
	return newCliError(5, message, modelIndependentDiagnostic())
}

func modelIndependentDiagnostic() string {
	return "structured-memory lexical recall and model-independent operations remain available; no model activation was applied"
}

func formatBytes(value uint64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case value >= gib:
		return fmt.Sprintf("%.1f GiB", float64(value)/gib)
	case value >= mib:
		return fmt.Sprintf("%.0f MiB", float64(value)/mib)
	case value >= kib:
		return fmt.Sprintf("%.0f KiB", float64(value)/kib)
	default:
		return fmt.Sprintf("%d B", value)
	}
}
