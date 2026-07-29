package modelcatalog

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Device struct {
	OperatingSystem   string       `json:"operating_system"`
	Architecture      string       `json:"architecture"`
	MemoryAvailable   uint64       `json:"memory_available_bytes,omitempty"`
	DiskAvailable     uint64       `json:"disk_available_bytes,omitempty"`
	Ollama            RuntimeState `json:"ollama"`
	DetectionWarnings []string     `json:"detection_warnings"`
}

func Inspect(ctx context.Context, baseURL string) Device {
	device := Device{
		OperatingSystem:   runtime.GOOS,
		Architecture:      runtime.GOARCH,
		DetectionWarnings: []string{},
	}
	resourcePath := strings.TrimSpace(os.Getenv("OLLAMA_MODELS"))
	var err error
	if resourcePath == "" {
		resourcePath, err = os.UserHomeDir()
	}
	if err != nil || strings.TrimSpace(resourcePath) == "" {
		resourcePath, err = os.Getwd()
	}
	if err != nil {
		resourcePath = "."
		device.DetectionWarnings = append(
			device.DetectionWarnings,
			"could not determine the Ollama storage filesystem for disk detection",
		)
	}
	memory, disk, warnings := queryResources(ctx, nearestExistingPath(resourcePath))
	device.MemoryAvailable = memory
	device.DiskAvailable = disk
	device.DetectionWarnings = append(device.DetectionWarnings, warnings...)

	client, err := NewOllamaClient(baseURL, &http.Client{Timeout: 3 * time.Second})
	if err != nil {
		device.Ollama = RuntimeState{BaseURL: baseURL, Models: []InstalledModel{}}
		device.DetectionWarnings = append(device.DetectionWarnings, err.Error())
		return device
	}
	state, err := client.State(ctx)
	if err != nil {
		device.Ollama = RuntimeState{BaseURL: baseURL, Models: []InstalledModel{}}
		device.DetectionWarnings = append(
			device.DetectionWarnings,
			fmt.Sprintf("Ollama is unavailable at %s", client.baseURL),
		)
		return device
	}
	device.Ollama = state
	return device
}

func nearestExistingPath(path string) string {
	path = filepath.Clean(path)
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "."
		}
		path = parent
	}
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
