package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGeminiCLIThinkingProviderIsRegistered(t *testing.T) {
	if thinking.GetProviderApplier("gemini-cli") == nil {
		t.Fatal("Gemini CLI thinking provider is not registered")
	}
}

func TestNormalizePayloadFromProtocolMapsGeminiCLIToGemini(t *testing.T) {
	if got := normalizePayloadFromProtocol("gemini-cli"); got != "gemini" {
		t.Fatalf("normalizePayloadFromProtocol(gemini-cli) = %q, want gemini", got)
	}
}

func TestResolveUsageSourceUsesGeminiCLIAuthID(t *testing.T) {
	auth := &coreauth.Auth{ID: "gemini-user@example.com-project-a", Provider: "gemini-cli"}
	if got := resolveUsageSource(auth, "client-api-key"); got != auth.ID {
		t.Fatalf("resolveUsageSource() = %q, want %q", got, auth.ID)
	}
}
