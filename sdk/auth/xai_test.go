package auth

import (
	"testing"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
)

func TestXAIAuthenticatorProviderAndRefreshLead(t *testing.T) {
	authenticator := NewXAIAuthenticator()
	if authenticator.Provider() != "xai" {
		t.Fatalf("Provider() = %q, want xai", authenticator.Provider())
	}
	lead := authenticator.RefreshLead()
	if lead == nil || *lead <= 0 {
		t.Fatalf("RefreshLead() = %v, want positive duration", lead)
	}
}

func TestBuildXAIAuthRecordPersistsProxyURL(t *testing.T) {
	record := buildXAIAuthRecord("xai", &xaiauth.TokenStorage{
		AccessToken:   "access",
		RefreshToken:  "refresh",
		Email:         "user@example.com",
		BaseURL:       xaiauth.DefaultAPIBaseURL,
		TokenEndpoint: "https://auth.x.ai/oauth/token",
	}, " socks5://proxy.example.com:1080 ")

	if record == nil {
		t.Fatal("buildXAIAuthRecord() returned nil")
	}
	if record.ProxyURL != "socks5://proxy.example.com:1080" {
		t.Fatalf("ProxyURL = %q, want trimmed proxy", record.ProxyURL)
	}
	if got, _ := record.Metadata["proxy_url"].(string); got != "socks5://proxy.example.com:1080" {
		t.Fatalf("metadata proxy_url = %q, want trimmed proxy", got)
	}
}
