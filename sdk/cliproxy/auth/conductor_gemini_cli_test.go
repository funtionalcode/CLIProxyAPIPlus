package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestRequestToFormatPreservesGeminiCLIEnvelope(t *testing.T) {
	got := requestToFormat("gemini-cli", nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
	if got != sdktranslator.FormatGeminiCLI {
		t.Fatalf("requestToFormat(gemini-cli) = %q, want %q", got, sdktranslator.FormatGeminiCLI)
	}
}
