package management

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/geminicli"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestTokenValueForAuthUsesGeminiCLISharedCredential(t *testing.T) {
	shared := geminicli.NewSharedCredential("primary", "user@example.com", map[string]any{
		"access_token": "shared-token",
	}, []string{"project-a"})
	auth := &coreauth.Auth{
		Provider: "gemini-cli",
		Runtime:  geminicli.NewVirtualCredential("project-a", shared),
	}

	if got := tokenValueForAuth(auth); got != "shared-token" {
		t.Fatalf("tokenValueForAuth() = %q, want shared-token", got)
	}
}

func TestResolveTokenForAuthUsesGeminiCLIRefreshPath(t *testing.T) {
	h := &Handler{}
	_, err := h.resolveTokenForAuth(context.Background(), &coreauth.Auth{Provider: "gemini-cli"})
	if err == nil || err.Error() != "gemini oauth metadata missing" {
		t.Fatalf("resolveTokenForAuth() error = %v, want missing Gemini OAuth metadata", err)
	}
}

func TestGeminiOAuthMetadataUpdatesSharedCredential(t *testing.T) {
	shared := geminicli.NewSharedCredential("primary", "user@example.com", map[string]any{
		"access_token": "old-token",
	}, []string{"project-a"})
	auth := &coreauth.Auth{
		Provider: "gemini-cli",
		Runtime:  geminicli.NewVirtualCredential("project-a", shared),
	}

	metadata, update := geminiOAuthMetadata(auth)
	if got := tokenValueFromMetadata(metadata); got != "old-token" {
		t.Fatalf("metadata access token = %q, want old-token", got)
	}
	update(map[string]any{"access_token": "new-token"})
	if got := tokenValueFromMetadata(shared.MetadataSnapshot()); got != "new-token" {
		t.Fatalf("shared access token = %q, want new-token", got)
	}
}
