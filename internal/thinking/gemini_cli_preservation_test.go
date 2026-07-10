package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/geminicli"
	"github.com/tidwall/gjson"
)

func TestGeminiCLIThinkingBodyUsesStrictBudgetValidation(t *testing.T) {
	const model = "gemini-cli-thinking-test"
	registerGeminiCLIThinkingModel(t, model)
	body := []byte(`{"model":"gemini-cli-thinking-test","request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}}`)

	if _, err := thinking.ApplyThinking(body, model, "gemini-cli", "gemini-cli", "gemini-cli"); err == nil {
		t.Fatal("expected oversized Gemini CLI body budget to fail validation")
	}
}

func TestGeminiCLIThinkingIsInGeminiProviderFamily(t *testing.T) {
	const model = "gemini-cli-family-test"
	registerGeminiCLIThinkingModel(t, model)
	body := []byte(`{"model":"gemini-cli-family-test","request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}}`)

	if _, err := thinking.ApplyThinking(body, model, "gemini", "gemini-cli", "gemini-cli"); err == nil {
		t.Fatal("expected oversized same-family Gemini budget to fail validation")
	}
}

func TestStripThinkingConfig_GeminiCLI(t *testing.T) {
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}},"contents":[]}}`)

	out := thinking.StripThinkingConfig(body, "gemini-cli")
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig").Exists() {
		t.Fatalf("Gemini CLI thinking config was not removed: %s", out)
	}
}

func registerGeminiCLIThinkingModel(t *testing.T, model string) {
	t.Helper()
	registry.GetGlobalRegistry().RegisterClient(t.Name(), "gemini-cli", []*registry.ModelInfo{{
		ID:       model,
		Type:     "gemini-cli",
		Thinking: &registry.ThinkingSupport{Min: 128, Max: 20000, ZeroAllowed: true, DynamicAllowed: true},
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(t.Name()) })
}
