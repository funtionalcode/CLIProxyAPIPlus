package tui

import "testing"

func TestOAuthProvidersIncludesGeminiCLI(t *testing.T) {
	for _, provider := range oauthProviders {
		if provider.apiPath == "gemini-cli-auth-url" {
			if provider.name != "Gemini CLI" {
				t.Fatalf("Gemini CLI provider name = %q", provider.name)
			}
			return
		}
	}
	t.Fatal("Gemini CLI OAuth provider is missing")
}

func TestOAuthCallbackProviderKeyMapsGeminiCLIToGemini(t *testing.T) {
	if got := oauthCallbackProviderKey("gemini-cli-auth-url"); got != "gemini" {
		t.Fatalf("oauthCallbackProviderKey(gemini-cli-auth-url) = %q, want gemini", got)
	}
}
