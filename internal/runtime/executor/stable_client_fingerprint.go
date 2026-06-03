package executor

import (
	"fmt"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func shouldApplyStableClientFingerprint(auth *cliproxyauth.Auth, provider string) bool {
	if auth == nil {
		return true
	}
	if metadataHasDuoGateway(auth.Metadata) {
		return false
	}
	if strings.TrimSpace(auth.Provider) == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), provider)
}

func metadataHasDuoGateway(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	for _, key := range []string{"duo_gateway_base_url", "duo_gateway_token"} {
		value, ok := metadata[key]
		if ok && strings.TrimSpace(fmt.Sprint(value)) != "" {
			return true
		}
	}
	return false
}
