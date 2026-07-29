package modelcatalog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	maxResponseBytes = 2 << 20
	maxPullLineBytes = 256 << 10
)

type OllamaClient struct {
	baseURL string
	http    *http.Client
}

type RuntimeState struct {
	Available bool             `json:"available"`
	BaseURL   string           `json:"base_url"`
	Version   string           `json:"version,omitempty"`
	Models    []InstalledModel `json:"models"`
}

type InstalledModel struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   uint64 `json:"size_bytes"`
}

type InstallationState struct {
	Status         string `json:"status"`
	Installed      bool   `json:"installed"`
	DigestVerified bool   `json:"digest_verified"`
	ActualDigest   string `json:"actual_digest,omitempty"`
}

type PullProgress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Total     int64  `json:"total,omitempty"`
}

func NewOllamaClient(baseURL string, client *http.Client) (*OllamaClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid Ollama base URL %q", baseURL)
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &OllamaClient{baseURL: baseURL, http: client}, nil
}

func (c *OllamaClient) State(ctx context.Context) (RuntimeState, error) {
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Model  string `json:"model"`
			Digest string `json:"digest"`
			Size   uint64 `json:"size"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, "/api/tags", &tags); err != nil {
		return RuntimeState{BaseURL: c.baseURL, Models: []InstalledModel{}}, err
	}
	state := RuntimeState{
		Available: true,
		BaseURL:   c.baseURL,
		Models:    make([]InstalledModel, 0, len(tags.Models)),
	}
	for _, model := range tags.Models {
		name := model.Name
		if name == "" {
			name = model.Model
		}
		state.Models = append(state.Models, InstalledModel{
			Name:   name,
			Digest: strings.ToLower(model.Digest),
			Size:   model.Size,
		})
	}
	sort.Slice(state.Models, func(i, j int) bool {
		if state.Models[i].Name == state.Models[j].Name {
			return state.Models[i].Digest < state.Models[j].Digest
		}
		return state.Models[i].Name < state.Models[j].Name
	})

	var version struct {
		Version string `json:"version"`
	}
	if err := c.getJSON(ctx, "/api/version", &version); err == nil {
		state.Version = version.Version
	}
	return state, nil
}

func (c *OllamaClient) Pull(
	ctx context.Context,
	profile Profile,
	progress func(PullProgress) error,
) error {
	if !profile.Installable {
		return fmt.Errorf("profile %q is unavailable: %s", profile.ID, profile.UnavailableReason)
	}
	body, err := json.Marshal(map[string]any{
		"model":  profile.Model,
		"stream": true,
	})
	if err != nil {
		return fmt.Errorf("encode Ollama pull request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/api/pull",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create Ollama pull request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return fmt.Errorf("Ollama pull request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf(
			"Ollama pull returned HTTP %d: %s",
			response.StatusCode,
			boundedText(string(message), 300),
		)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 16<<10), maxPullLineBytes)
	sawEvent := false
	succeeded := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		sawEvent = true
		var event struct {
			Status    string `json:"status"`
			Digest    string `json:"digest"`
			Completed int64  `json:"completed"`
			Total     int64  `json:"total"`
			Error     string `json:"error"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("Ollama pull returned malformed progress: %w", err)
		}
		if event.Error != "" {
			return fmt.Errorf("Ollama pull failed: %s", boundedText(event.Error, 300))
		}
		event.Status = boundedText(event.Status, 160)
		event.Digest = boundedText(event.Digest, 96)
		if event.Status == "success" {
			succeeded = true
		}
		if progress != nil {
			if err := progress(PullProgress{
				Status:    event.Status,
				Digest:    event.Digest,
				Completed: event.Completed,
				Total:     event.Total,
			}); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return fmt.Errorf("read Ollama pull progress: %w", err)
	}
	if !sawEvent {
		return errors.New("Ollama pull returned an empty progress stream")
	}
	if !succeeded {
		return errors.New("Ollama pull ended before reporting success")
	}
	return nil
}

func (c *OllamaClient) Probe(ctx context.Context, profile Profile) error {
	body, err := json.Marshal(map[string]any{
		"model":      profile.Model,
		"input":      []string{"mem local embedding compatibility probe"},
		"dimensions": CorpusDimension,
		"truncate":   false,
	})
	if err != nil {
		return fmt.Errorf("encode Ollama embed probe: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/api/embed",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create Ollama embed probe: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Ollama embed probe failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf(
			"Ollama embed probe returned HTTP %d: %s",
			response.StatusCode,
			boundedText(string(message), 300),
		)
	}
	var payload struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := decodeBoundedJSON(response.Body, &payload); err != nil {
		return fmt.Errorf("decode Ollama embed probe: %w", err)
	}
	if len(payload.Embeddings) != 1 {
		return fmt.Errorf(
			"Ollama embed probe returned %d vectors, want 1",
			len(payload.Embeddings),
		)
	}
	if got := len(payload.Embeddings[0]); got != CorpusDimension {
		return fmt.Errorf(
			"Ollama embed probe returned dimension %d, want %d",
			got,
			CorpusDimension,
		)
	}
	return nil
}

func InstallationFor(profile Profile, state RuntimeState) InstallationState {
	for _, installed := range state.Models {
		if installed.Name != profile.Model {
			continue
		}
		if strings.EqualFold(installed.Digest, profile.ManifestDigest) {
			return InstallationState{
				Status:         "verified",
				Installed:      true,
				DigestVerified: true,
				ActualDigest:   installed.Digest,
			}
		}
		return InstallationState{
			Status:       "digest_mismatch",
			Installed:    true,
			ActualDigest: installed.Digest,
		}
	}
	return InstallationState{Status: "not_installed"}
}

func (c *OllamaClient) getJSON(ctx context.Context, path string, out any) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+path,
		nil,
	)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("GET %s failed: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("GET %s returned HTTP %d", path, response.StatusCode)
	}
	return decodeBoundedJSON(response.Body, out)
}

func decodeBoundedJSON(reader io.Reader, out any) error {
	limited := &io.LimitedReader{R: reader, N: maxResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(out); err != nil {
		if limited.N <= 0 {
			return errors.New("response exceeds size limit")
		}
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if limited.N <= 0 {
			return errors.New("response exceeds size limit")
		}
		if err == nil {
			return errors.New("response contains more than one JSON document")
		}
		return err
	}
	if limited.N <= 0 {
		return errors.New("response exceeds size limit")
	}
	return nil
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
