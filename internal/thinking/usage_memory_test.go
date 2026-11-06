package thinking_test

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

func TestReasoningEffortDoesNotRetainRequestBody(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		fields   string
		want     string
	}{
		{"codex", "codex", `"reasoning":{"effort":"xhigh"}`, "xhigh"},
		{"responses", "openai-response", `"reasoning":{"effort":"high"}`, "high"},
		{"xai", "xai", `"reasoning":{"effort":"high"}`, "high"},
		{"openai", "openai", `"reasoning_effort":"medium"`, "medium"},
		{"claude_adaptive", "claude", `"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}`, "max"},
		{"claude_enabled", "claude", `"thinking":{"type":"enabled"},"output_config":{"effort":"high"}`, "high"},
		{"claude_effort_only", "claude", `"output_config":{"effort":"low"}`, "low"},
		{"kimi", "kimi", `"thinking":{"type":"enabled","effort":"high"}`, "high"},
		{"configuration_update", "codex", `"reasoning":{"effort":"high"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]`, "low"},
		{"trimmed", "codex", `"reasoning":{"effort":" high "}`, "high"},
		{"normalized", "codex", `"reasoning":{"effort":" HIGH "}`, "high"},
		{"escaped", "codex", `"reasoning":{"effort":"h\u0069gh"}`, "high"},
		{"none", "codex", `"reasoning":{"effort":"none"}`, "none"},
		{"auto", "claude", `"thinking":{"type":"adaptive"},"output_config":{"effort":"auto"}`, "auto"},
		{"missing", "codex", `"input":[]`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := append([]byte(`{"padding":"`), bytes.Repeat([]byte("p"), 512<<10)...)
			body = append(body, []byte(`",`+tt.fields+`}`)...)
			requested := thinking.ExtractReasoningEffort(body, tt.provider, "test-model")
			translated := thinking.ExtractTranslatedReasoningEffort(body, tt.provider)
			if requested != tt.want || translated != tt.want {
				t.Fatalf("before buffer reuse: requested=%q translated=%q, want %q", requested, translated, tt.want)
			}
			for i := range body {
				body[i] = 'z'
			}
			if requested != tt.want || translated != tt.want {
				t.Fatalf("effort aliases the request buffer: requested=%q translated=%q, want %q", requested, translated, tt.want)
			}
		})
	}
}
