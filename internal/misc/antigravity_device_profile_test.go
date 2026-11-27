package misc

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAntigravityDeviceProfileStableAndDistributed(t *testing.T) {
	profiles := make(map[string]bool)
	for i := range 100 {
		identity := fmt.Sprintf("account-%d", i)
		profile := ResolveAntigravityDeviceProfile(identity, "", nil, true)
		if again := ResolveAntigravityDeviceProfile(identity, "", nil, true); again != profile {
			t.Fatal("credential profile changed between requests")
		}
		profiles[profile.OS+"/"+profile.Arch+"/"+profile.Locale] = true
		parsed := ParseAntigravityDeviceProfile(profile.UserAgent)
		if parsed.Version != profile.Version || parsed.OS != profile.OS || parsed.Arch != profile.Arch {
			t.Fatalf("UA and profile disagree: %#v / %#v", profile, parsed)
		}
		headers := make(http.Header)
		profile.ApplyHeaders(headers)
		if headers.Get("Accept-Language") != profile.Locale || headers.Get("User-Agent") != profile.UserAgent {
			t.Fatal("profile headers lost")
		}
	}
	if len(profiles) < 5 {
		t.Fatalf("accounts were not distributed: %d profiles", len(profiles))
	}
}

func TestAntigravityDeviceProfileOverridesAndDisable(t *testing.T) {
	stored := map[string]any{"hub_version": "2.30.1", "cl": "123456", "os_type": "linux", "arch": "amd64", "locale": "ja-JP"}
	profile := ResolveAntigravityDeviceProfile("account", "", stored, true)
	if profile.Version != "2.30.1" || profile.OS != "linux" || profile.Arch != "amd64" || profile.Locale != "ja-JP" || !strings.Contains(profile.UserAgent, "cl=123456") {
		t.Fatalf("stored profile ignored: %#v", profile)
	}
	explicitUA := "antigravity/hub/2.29.0 (aidev_client; os_type=darwin; arch=arm64)"
	explicit := ResolveAntigravityDeviceProfile("account", explicitUA, stored, true)
	if explicit.UserAgent != explicitUA || explicit.Version != "2.29.0" || explicit.OS != "darwin" || explicit.Arch != "arm64" {
		t.Fatalf("explicit UA lost precedence: %#v", explicit)
	}
	disabled := ResolveAntigravityDeviceProfile("account", "", stored, false)
	if disabled.UserAgent != AntigravityUserAgent() || disabled.Locale != "" {
		t.Fatalf("disabled profile changed canonical headers: %#v", disabled)
	}
	base := ResolveAntigravityDeviceProfile("account", "", nil, true)
	invalid := ResolveAntigravityDeviceProfile("account", "", map[string]any{"hub_version": "2.30.1\r\nHeader: injected", "os_type": "invalid", "arch": "x64", "locale": "invalid"}, true)
	if invalid != base {
		t.Fatalf("invalid metadata changed profile: %#v", invalid)
	}
}
