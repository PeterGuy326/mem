package aiprofile

import (
	"errors"
	"strings"
)

const (
	DeploymentPrivate = "private"
	DeploymentSaaS    = "saas"
)

var (
	// ErrProfileActive prevents a legacy per-kind setting from partially
	// overriding a fixed profile.
	ErrProfileActive = errors.New("workspace AI profile is active")

	// ErrManagedProviderRequiresProfile prevents SaaS callers from selecting
	// a platform credential/model through the old arbitrary provider surface.
	ErrManagedProviderRequiresProfile = errors.New("managed provider requires an allowlisted workspace AI profile")

	// ErrInvalidDeploymentMode is intentionally distinct from invalid input so
	// callers fail closed when configuration is malformed.
	ErrInvalidDeploymentMode = errors.New("invalid deployment mode")

	// ErrInvalidProviderSpec is returned without echoing the supplied value;
	// callers may have accidentally put a credential or URL in that field.
	ErrInvalidProviderSpec = errors.New("invalid provider spec")
)

// ValidateLegacyProviderMutation applies the server-side policy shared by
// legacy provider Set and Test endpoints.  Client UI, CLI, and MCP must not
// be relied on for this boundary: a test is a real provider invocation too.
//
// Private installations keep the advanced local/BYOM path when no fixed
// profile is selected.  SaaS has no per-workspace BYOM credential store yet,
// so it permits only local runtimes and rejects cloud/model-gateway specs.
func ValidateLegacyProviderMutation(
	deploymentMode string,
	activeProfile *Selection,
	spec string,
) error {
	if activeProfile != nil {
		return ErrProfileActive
	}
	if !safeProviderSpec(spec) {
		return ErrInvalidProviderSpec
	}
	// The dedicated Idealab namespace is backed by a platform credential and
	// is reachable only through an immutable, entitlement-gated profile.
	// Private BYOM remains available through its existing provider namespaces.
	if isIdealabProvider(spec) {
		return ErrManagedProviderRequiresProfile
	}
	switch strings.TrimSpace(deploymentMode) {
	case DeploymentPrivate:
		return nil
	case DeploymentSaaS:
		if isLocalProvider(spec) {
			return nil
		}
		return ErrManagedProviderRequiresProfile
	default:
		return ErrInvalidDeploymentMode
	}
}

// IsManagedCatalogProvider reports whether a concrete provider/model appears
// in one of the compiled managed profiles.  It is useful for safe diagnostics
// and future entitlement routing; it intentionally does not classify an
// arbitrary OpenAI-compatible spec as a permissible managed call. The two
// exact OpenAI-compatible V1 providers remain managed because that immutable
// published snapshot can still be active in an existing workspace.
func IsManagedCatalogProvider(spec string) bool {
	for _, definition := range compiledCatalog {
		if definition.DataEgress != DataEgressManagedIdealab {
			continue
		}
		for _, stage := range []Stage{
			definition.Embedding,
			definition.VisualEmbedding,
			definition.LLM,
			definition.VLM,
			definition.ASR,
			definition.Rerank,
		} {
			if stage.Enabled && stage.Provider == spec {
				return true
			}
		}
	}
	return false
}
