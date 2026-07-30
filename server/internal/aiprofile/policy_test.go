package aiprofile

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateLegacyProviderMutation(t *testing.T) {
	active := &Selection{ProfileID: IdealabQualityV1}
	secretURL := "openai:https://token:secret@example.invalid/v1"

	tests := []struct {
		name       string
		mode       string
		active     *Selection
		spec       string
		want       error
		mustRedact string
	}{
		{
			name: "private permits a safe advanced provider when inactive",
			mode: DeploymentPrivate,
			spec: "openai:enterprise-embedding-2026-06-08",
		},
		{
			name:   "private profile blocks a legacy mutation",
			mode:   DeploymentPrivate,
			active: active,
			spec:   "ollama:qwen3-embedding:0.6b",
			want:   ErrProfileActive,
		},
		{
			name: "saas permits an explicitly local runtime",
			mode: DeploymentSaaS,
			spec: "ollama:qwen3-embedding:0.6b",
		},
		{
			name: "saas blocks arbitrary cloud provider",
			mode: DeploymentSaaS,
			spec: "openai:enterprise-embedding-2026-06-08",
			want: ErrManagedProviderRequiresProfile,
		},
		{
			name:       "invalid provider spec is redacted",
			mode:       DeploymentPrivate,
			spec:       secretURL,
			want:       ErrInvalidProviderSpec,
			mustRedact: "secret",
		},
		{
			name: "invalid deployment fails closed",
			mode: "development",
			spec: "ollama:qwen3-embedding:0.6b",
			want: ErrInvalidDeploymentMode,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateLegacyProviderMutation(test.mode, test.active, test.spec)
			if test.want == nil {
				if err != nil {
					t.Fatalf("ValidateLegacyProviderMutation() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.mustRedact != "" && strings.Contains(err.Error(), test.mustRedact) {
				t.Fatalf("error leaked input material: %q", err)
			}
		})
	}
}

func TestIsManagedCatalogProviderIsExact(t *testing.T) {
	for _, spec := range []string{
		"openai:text-embedding-3-large",
		"openai:qwen3.7-max-2026-06-08",
	} {
		if !IsManagedCatalogProvider(spec) {
			t.Errorf("IsManagedCatalogProvider(%q) = false, want true", spec)
		}
	}
	for _, spec := range []string{
		"openai:text-embedding-3-large-preview",
		"openai:any-model",
		"openai:qwen3-rerank",
		"ollama:qwen3-embedding:0.6b",
	} {
		if IsManagedCatalogProvider(spec) {
			t.Errorf("IsManagedCatalogProvider(%q) = true, want false", spec)
		}
	}
}
