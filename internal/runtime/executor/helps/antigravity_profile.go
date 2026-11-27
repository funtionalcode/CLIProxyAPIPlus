package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// AntigravityProfile resolves one profile for generation, token count and telemetry.
func AntigravityProfile(cfg *config.Config, auth *cliproxyauth.Auth) misc.AntigravityDeviceProfile {
	identity, userAgent := "", ""
	var stored any
	if auth != nil {
		identity = strings.TrimSpace(auth.ID)
		if identity == "" {
			identity = strings.TrimSpace(auth.FileName)
		}
		if identity == "" {
			identity, _ = auth.Metadata["email"].(string)
		}
		userAgent = strings.TrimSpace(auth.Attributes["user_agent"])
		if userAgent == "" {
			userAgent, _ = auth.Metadata["user_agent"].(string)
		}
		stored = auth.Metadata["device_profile"]
	}
	enabled := cfg == nil || cfg.Antigravity.DeviceProfile.Enabled == nil || *cfg.Antigravity.DeviceProfile.Enabled
	return misc.ResolveAntigravityDeviceProfile(identity, userAgent, stored, enabled)
}
