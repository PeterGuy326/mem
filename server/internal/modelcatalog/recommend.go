package modelcatalog

import (
	"fmt"
	"sort"
)

type Compatibility struct {
	Status     string   `json:"status"`
	Compatible bool     `json:"compatible"`
	Reasons    []string `json:"reasons"`
	Advisories []string `json:"advisories"`
}

type ProfileStatus struct {
	Profile       Profile           `json:"profile"`
	Compatibility Compatibility     `json:"compatibility"`
	Installation  InstallationState `json:"installation"`
}

type Recommendation struct {
	ProfileID string   `json:"profile_id"`
	Score     int      `json:"score"`
	Rationale []string `json:"rationale"`
}

func Evaluate(catalog Catalog, device Device) []ProfileStatus {
	statuses := make([]ProfileStatus, 0, len(catalog.Profiles))
	for _, profile := range catalog.Profiles {
		installation := InstallationFor(profile, device.Ollama)
		statuses = append(statuses, ProfileStatus{
			Profile:       profile,
			Compatibility: evaluateProfile(profile, device, installation),
			Installation:  installation,
		})
	}
	return statuses
}

func evaluateProfile(
	profile Profile,
	device Device,
	installation InstallationState,
) Compatibility {
	result := Compatibility{
		Status:     "compatible",
		Compatible: true,
		Reasons:    []string{},
		Advisories: []string{},
	}
	if !profile.Installable {
		result.Status = "unavailable"
		result.Compatible = false
		result.Reasons = append(result.Reasons, profile.UnavailableReason)
		return result
	}
	if profile.ExpectedDimension != CorpusDimension {
		result.Reasons = append(
			result.Reasons,
			fmt.Sprintf(
				"model dimension %d does not match corpus dimension %d",
				profile.ExpectedDimension,
				CorpusDimension,
			),
		)
	}
	if !containsFold(profile.MinimumHardware.OperatingSystems, device.OperatingSystem) {
		result.Reasons = append(
			result.Reasons,
			fmt.Sprintf("operating system %s is not supported", device.OperatingSystem),
		)
	}
	if !containsFold(profile.MinimumHardware.Architectures, device.Architecture) {
		result.Reasons = append(
			result.Reasons,
			fmt.Sprintf("architecture %s is not supported", device.Architecture),
		)
	}
	if !device.Ollama.Available {
		result.Reasons = append(
			result.Reasons,
			fmt.Sprintf("Ollama is unavailable at %s", device.Ollama.BaseURL),
		)
	}
	if device.MemoryAvailable == 0 {
		result.Advisories = append(result.Advisories, "available memory is unknown")
	} else if device.MemoryAvailable < profile.MinimumHardware.MemoryBytes {
		result.Reasons = append(
			result.Reasons,
			fmt.Sprintf(
				"available memory %d is below the recommended minimum %d",
				device.MemoryAvailable,
				profile.MinimumHardware.MemoryBytes,
			),
		)
	}
	if installation.Status != "verified" {
		if device.DiskAvailable == 0 {
			result.Advisories = append(result.Advisories, "available disk space is unknown")
		} else if device.DiskAvailable < profile.MinimumHardware.DiskBytes {
			result.Reasons = append(
				result.Reasons,
				fmt.Sprintf(
					"available disk space %d is below the recommended minimum %d",
					device.DiskAvailable,
					profile.MinimumHardware.DiskBytes,
				),
			)
		}
	}
	if len(result.Reasons) != 0 {
		result.Status = "incompatible"
		result.Compatible = false
	}
	return result
}

func Recommend(statuses []ProfileStatus, language string, device Device) []Recommendation {
	recommendations := make([]Recommendation, 0, len(statuses))
	for _, status := range statuses {
		if !status.Compatibility.Compatible || !status.Profile.SupportsLanguage(language) {
			continue
		}
		profile := status.Profile
		score := 0
		rationale := []string{}

		if status.Installation.Status == "verified" {
			score += 100
			rationale = append(rationale, "already installed with the catalog-pinned digest")
		}
		if language != "" && language != "auto" {
			score += 60
			rationale = append(
				rationale,
				fmt.Sprintf("profile explicitly declares language %q", language),
			)
		}
		if device.MemoryAvailable >= profile.MinimumHardware.MemoryBytes*2 {
			score += 20
			rationale = append(rationale, "available memory is at least twice the coarse minimum")
		} else {
			rationale = append(rationale, "available memory meets the coarse minimum")
		}
		if device.DiskAvailable >= profile.MinimumHardware.DiskBytes*2 {
			score += 10
			rationale = append(rationale, "available disk is at least twice the coarse minimum")
		}

		// Efficiency is deliberately based on artifact size, not vendor name
		// or public download counts.
		switch {
		case profile.ArtifactSizeBytes <= 300_000_000:
			score += 30
			rationale = append(rationale, "smallest download tier")
		case profile.ArtifactSizeBytes <= 700_000_000:
			score += 20
			rationale = append(rationale, "compact download tier")
		default:
			score += 10
			rationale = append(rationale, "larger download tier")
		}
		recommendations = append(recommendations, Recommendation{
			ProfileID: profile.ID,
			Score:     score,
			Rationale: rationale,
		})
	}
	sort.Slice(recommendations, func(i, j int) bool {
		if recommendations[i].Score == recommendations[j].Score {
			return recommendations[i].ProfileID < recommendations[j].ProfileID
		}
		return recommendations[i].Score > recommendations[j].Score
	})
	return recommendations
}
