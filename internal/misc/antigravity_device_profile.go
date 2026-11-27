package misc

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/regexp"
)

// AntigravityDeviceProfile describes the effective wire identity, not the host OS.
type AntigravityDeviceProfile struct {
	UserAgent string
	Version   string
	OS        string
	Arch      string
	Locale    string
}

// ApplyHeaders sets the profile headers before explicit credential overrides.
func (p AntigravityDeviceProfile) ApplyHeaders(headers http.Header) {
	headers.Set("User-Agent", p.UserAgent)
	if p.Locale != "" {
		headers.Set("Accept-Language", p.Locale)
	}
}

var (
	antigravityProfileVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	antigravityProfileCL      = regexp.MustCompile(`^[0-9]+$`)
	antigravityPlatforms      = [][2]string{{"linux", "amd64"}, {"darwin", "arm64"}, {"win32", "x64"}, {"linux", "arm64"}, {"darwin", "x64"}}
	antigravityLocales        = []string{"en-US", "en-GB", "zh-CN", "ja-JP", "de-DE", "fr-FR", "es-ES"}
)

// ResolveAntigravityDeviceProfile uses a stable credential hash for platform and
// locale selection. Versions still follow the existing updater unless pinned in
// device_profile. No credential metadata is mutated or persisted by this function.
func ResolveAntigravityDeviceProfile(identity, configuredUA string, stored any, enabled bool) AntigravityDeviceProfile {
	profile := ParseAntigravityDeviceProfile(AntigravityRequestUserAgent(configuredUA))
	if !enabled {
		return profile
	}
	identity = strings.TrimSpace(identity)
	if identity != "" {
		digest := sha256.Sum256([]byte("cli-proxy-api:antigravity:device:v1\x00" + identity))
		platform := antigravityPlatforms[int(digest[0])%len(antigravityPlatforms)]
		profile.OS, profile.Arch = platform[0], platform[1]
		profile.Locale = antigravityLocales[int(digest[1])%len(antigravityLocales)]
	}
	version := AntigravityLatestVersion()
	cl := antigravityHubChangelists[version]
	if raw, ok := stored.(map[string]any); ok {
		if value, okVersion := raw["hub_version"].(string); okVersion && antigravityProfileVersion.MatchString(value) {
			version = value
			cl = antigravityHubChangelists[version]
			if valueCL, okCL := raw["cl"].(string); okCL && antigravityProfileCL.MatchString(valueCL) {
				cl = valueCL
			}
		}
		os, _ := raw["os_type"].(string)
		arch, _ := raw["arch"].(string)
		for _, platform := range antigravityPlatforms {
			if os == platform[0] && arch == platform[1] {
				profile.OS, profile.Arch = os, arch
				break
			}
		}
		locale, _ := raw["locale"].(string)
		for _, allowed := range antigravityLocales {
			if locale == allowed {
				profile.Locale = locale
				break
			}
		}
	}
	if strings.TrimSpace(configuredUA) != "" {
		// An explicit UA is authoritative, including its version/platform pair.
		explicit := ParseAntigravityDeviceProfile(AntigravityRequestUserAgent(configuredUA))
		explicit.Locale = profile.Locale
		return explicit
	}
	profile.Version = version
	profile.UserAgent = fmt.Sprintf("antigravity/hub/%s (aidev_client; os_type=%s; arch=%s", version, profile.OS, profile.Arch)
	if cl != "" {
		profile.UserAgent += "; cl=" + cl
	}
	profile.UserAgent += ")"
	return profile
}

// ParseAntigravityDeviceProfile extracts only the declared UA environment fields.
func ParseAntigravityDeviceProfile(userAgent string) AntigravityDeviceProfile {
	profile := AntigravityDeviceProfile{UserAgent: userAgent}
	if !strings.HasPrefix(strings.ToLower(userAgent), "antigravity/hub/") {
		return profile
	}
	profile.Version = AntigravityVersionFromUserAgent(userAgent)
	_, metadata, _ := strings.Cut(userAgent, "(")
	for _, field := range strings.Split(strings.TrimSuffix(metadata, ")"), ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok {
			continue
		}
		switch key {
		case "os_type":
			profile.OS = strings.TrimSpace(value)
		case "arch":
			profile.Arch = strings.TrimSpace(value)
		}
	}
	return profile
}
