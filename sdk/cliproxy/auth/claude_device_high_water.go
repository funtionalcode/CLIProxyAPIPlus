package auth

import (
	"regexp"
	"strconv"
	"strings"
)

// ClaudeDeviceHighWaterMetadataKey stores the persisted Claude client
// device-profile high-water mark in Auth.Metadata.
const ClaudeDeviceHighWaterMetadataKey = "claude_device_high_water"

var claudeHighWaterVersionPattern = regexp.MustCompile(`^claude-cli/(\d+)\.(\d+)\.(\d+)`)

// ClaudeDeviceHighWater is the serializable persisted form of one real Claude
// client profile observation. It keeps the version-bearing User-Agent together
// with the matching package/runtime fields from the same observation.
type ClaudeDeviceHighWater struct {
	UserAgent      string `json:"user_agent,omitempty"`
	Version        string `json:"version,omitempty"`
	PackageVersion string `json:"package_version,omitempty"`
	RuntimeVersion string `json:"runtime_version,omitempty"`
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
	Source         string `json:"source,omitempty"`
	LastSeenAt     string `json:"last_seen_at,omitempty"`
}

func (h ClaudeDeviceHighWater) parsedVersion() (claudeHighWaterVersion, bool) {
	if v, ok := parseClaudeHighWaterVersionString(h.Version); ok {
		return v, true
	}
	return parseClaudeHighWaterUserAgent(h.UserAgent)
}

func (h ClaudeDeviceHighWater) valid() bool {
	if strings.TrimSpace(h.UserAgent) == "" {
		return false
	}
	_, ok := h.parsedVersion()
	return ok
}

type claudeHighWaterVersion struct {
	major int
	minor int
	patch int
}

func (v claudeHighWaterVersion) compare(other claudeHighWaterVersion) int {
	switch {
	case v.major != other.major:
		if v.major > other.major {
			return 1
		}
		return -1
	case v.minor != other.minor:
		if v.minor > other.minor {
			return 1
		}
		return -1
	case v.patch != other.patch:
		if v.patch > other.patch {
			return 1
		}
		return -1
	default:
		return 0
	}
}

func parseClaudeHighWaterUserAgent(userAgent string) (claudeHighWaterVersion, bool) {
	matches := claudeHighWaterVersionPattern.FindStringSubmatch(strings.TrimSpace(userAgent))
	if len(matches) != 4 {
		return claudeHighWaterVersion{}, false
	}
	return claudeHighWaterTriadFromStrings(matches[1], matches[2], matches[3])
}

func parseClaudeHighWaterVersionString(version string) (claudeHighWaterVersion, bool) {
	parts := strings.SplitN(strings.TrimSpace(version), ".", 3)
	if len(parts) != 3 {
		return claudeHighWaterVersion{}, false
	}
	return claudeHighWaterTriadFromStrings(parts[0], parts[1], parts[2])
}

func claudeHighWaterTriadFromStrings(rawMajor, rawMinor, rawPatch string) (claudeHighWaterVersion, bool) {
	major, err := strconv.Atoi(strings.TrimSpace(rawMajor))
	if err != nil {
		return claudeHighWaterVersion{}, false
	}
	minor, err := strconv.Atoi(strings.TrimSpace(rawMinor))
	if err != nil {
		return claudeHighWaterVersion{}, false
	}
	patch, err := strconv.Atoi(strings.TrimSpace(rawPatch))
	if err != nil {
		return claudeHighWaterVersion{}, false
	}
	return claudeHighWaterVersion{major: major, minor: minor, patch: patch}, true
}

// ClaudeDeviceHighWaterFromMetadata reads a persisted high-water entry from
// Auth.Metadata. It accepts the in-memory map[string]any form and the
// decoded-from-disk map[string]string form.
func ClaudeDeviceHighWaterFromMetadata(metadata map[string]any) (ClaudeDeviceHighWater, bool) {
	if len(metadata) == 0 {
		return ClaudeDeviceHighWater{}, false
	}
	raw, ok := metadata[ClaudeDeviceHighWaterMetadataKey]
	if !ok || raw == nil {
		return ClaudeDeviceHighWater{}, false
	}
	hw := ClaudeDeviceHighWater{}
	switch m := raw.(type) {
	case ClaudeDeviceHighWater:
		hw = m
	case map[string]any:
		hw = ClaudeDeviceHighWater{
			UserAgent:      stringFromMetadataAny(m["user_agent"]),
			Version:        stringFromMetadataAny(m["version"]),
			PackageVersion: stringFromMetadataAny(m["package_version"]),
			RuntimeVersion: stringFromMetadataAny(m["runtime_version"]),
			OS:             stringFromMetadataAny(m["os"]),
			Arch:           stringFromMetadataAny(m["arch"]),
			Source:         stringFromMetadataAny(m["source"]),
			LastSeenAt:     stringFromMetadataAny(m["last_seen_at"]),
		}
	case map[string]string:
		hw = ClaudeDeviceHighWater{
			UserAgent:      m["user_agent"],
			Version:        m["version"],
			PackageVersion: m["package_version"],
			RuntimeVersion: m["runtime_version"],
			OS:             m["os"],
			Arch:           m["arch"],
			Source:         m["source"],
			LastSeenAt:     m["last_seen_at"],
		}
	default:
		return ClaudeDeviceHighWater{}, false
	}
	if !hw.valid() {
		return ClaudeDeviceHighWater{}, false
	}
	return hw, true
}

func stringFromMetadataAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func claudeDeviceHighWaterToMetadataMap(hw ClaudeDeviceHighWater) map[string]any {
	out := make(map[string]any, 8)
	if v := strings.TrimSpace(hw.UserAgent); v != "" {
		out["user_agent"] = v
	}
	if v := strings.TrimSpace(hw.Version); v != "" {
		out["version"] = v
	}
	if v := strings.TrimSpace(hw.PackageVersion); v != "" {
		out["package_version"] = v
	}
	if v := strings.TrimSpace(hw.RuntimeVersion); v != "" {
		out["runtime_version"] = v
	}
	if v := strings.TrimSpace(hw.OS); v != "" {
		out["os"] = v
	}
	if v := strings.TrimSpace(hw.Arch); v != "" {
		out["arch"] = v
	}
	if v := strings.TrimSpace(hw.Source); v != "" {
		out["source"] = v
	}
	if v := strings.TrimSpace(hw.LastSeenAt); v != "" {
		out["last_seen_at"] = v
	}
	return out
}
