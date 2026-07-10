package translator_test

import (
	"testing"

	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	registry "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func TestGeminiCLITranslationsRemainRegistered(t *testing.T) {
	pairs := []struct {
		from string
		to   string
	}{
		{from: Claude, to: GeminiCLI},
		{from: GeminiCLI, to: Claude},
		{from: GeminiCLI, to: Codex},
		{from: Gemini, to: GeminiCLI},
		{from: GeminiCLI, to: Gemini},
		{from: OpenAI, to: GeminiCLI},
		{from: OpenaiResponse, to: GeminiCLI},
		{from: GeminiCLI, to: OpenAI},
	}

	for _, pair := range pairs {
		if !registry.NeedConvert(pair.from, pair.to) {
			t.Errorf("missing Gemini CLI translation registration: %s -> %s", pair.from, pair.to)
		}
	}
}
