package ir

import (
	"encoding/json"
	"testing"
)

func TestParseOpenAIChatRequest_Basic(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4",
		"stream": true,
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi!"}
		],
		"max_tokens": 4096,
		"temperature": 0.7,
		"top_p": 0.9
	}`)

	ir, err := ParseOpenAIChatRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ir.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", ir.Model, "gpt-4")
	}
	if !ir.Stream {
		t.Error("Stream = false, want true")
	}
	if len(ir.System) != 1 || ir.System[0].Text != "You are helpful." {
		t.Errorf("System = %v", ir.System)
	}
	if len(ir.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(ir.Messages))
	}
	if ir.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q", ir.Messages[0].Role)
	}
	if ir.Messages[1].Role != "assistant" {
		t.Errorf("Messages[1].Role = %q", ir.Messages[1].Role)
	}
	if ir.MaxTokens == nil || *ir.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %v", ir.MaxTokens)
	}
	if ir.TopP == nil || *ir.TopP != 0.9 {
		t.Errorf("TopP = %v", ir.TopP)
	}
}

func TestParseOpenAIChatRequest_ToolCalls(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4",
		"messages": [
			{
				"role": "assistant",
				"content": null,
				"tool_calls": [
					{
						"id": "call_001",
						"type": "function",
						"function": {"name": "search", "arguments": "{\"q\":\"hello\"}"}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_001",
				"content": "result text"
			}
		]
	}`)

	ir, err := ParseOpenAIChatRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(ir.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(ir.Messages))
	}

	// Assistant with tool_calls
	assistant := ir.Messages[0]
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].ID != "call_001" {
		t.Errorf("ToolCalls[0].ID = %q", assistant.ToolCalls[0].ID)
	}
	if assistant.ToolCalls[0].Name != "search" {
		t.Errorf("ToolCalls[0].Name = %q", assistant.ToolCalls[0].Name)
	}

	// Tool result
	tool := ir.Messages[1]
	if tool.ToolCallID != "call_001" {
		t.Errorf("ToolCallID = %q", tool.ToolCallID)
	}
}

func TestParseOpenAIChatRequest_Tools(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4",
		"messages": [],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "search",
					"description": "Search the web",
					"parameters": {"type": "object"}
				}
			}
		],
		"tool_choice": "auto"
	}`)

	ir, err := ParseOpenAIChatRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(ir.Tools) != 1 {
		t.Fatalf("Tools len = %d, want 1", len(ir.Tools))
	}
	if ir.Tools[0].Name != "search" {
		t.Errorf("Tools[0].Name = %q", ir.Tools[0].Name)
	}
	if ir.Tools[0].Type != "function" {
		t.Errorf("Tools[0].Type = %q", ir.Tools[0].Type)
	}
	if ir.ToolChoice == nil || ir.ToolChoice.Type != "auto" {
		t.Errorf("ToolChoice = %v", ir.ToolChoice)
	}
}

func TestParseOpenAIChatRequest_Passthrough(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4",
		"messages": [],
		"response_format": {"type": "json_object"},
		"user": "user-123",
		"n": 1
	}`)

	ir, err := ParseOpenAIChatRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ir.Passthrough == nil {
		t.Fatal("Passthrough is nil")
	}
	if _, ok := ir.Passthrough["response_format"]; !ok {
		t.Error("missing response_format")
	}
	if _, ok := ir.Passthrough["user"]; !ok {
		t.Error("missing user")
	}
}

func TestSerializeOpenAIChatRequest_Basic(t *testing.T) {
	maxTokens := int64(4096)
	ir := &IRRequest{
		Model:     "gpt-4",
		Stream:    true,
		System:    []SystemBlock{{Type: "text", Text: "You are helpful."}},
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hello"}}}},
		MaxTokens: &maxTokens,
	}

	data, err := SerializeOpenAIChatRequest(ir)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["model"] != "gpt-4" {
		t.Errorf("model = %v", obj["model"])
	}
	if obj["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v", obj["max_tokens"])
	}

	// Messages should include system message
	msgs := obj["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2", len(msgs))
	}
	firstMsg := msgs[0].(map[string]any)
	if firstMsg["role"] != "system" {
		t.Errorf("first message role = %v", firstMsg["role"])
	}
}

func TestOpenAIChatRequest_RoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4",
		"stream": true,
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		],
		"response_format": {"type": "json_object"}
	}`)

	ir, err := ParseOpenAIChatRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := SerializeOpenAIChatRequest(ir)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if obj["model"] != "gpt-4" {
		t.Errorf("model = %v", obj["model"])
	}
	// Passthrough preserved
	if obj["response_format"] == nil {
		t.Error("missing response_format after round-trip")
	}
}

func TestOpenAIChatRequest_CrossFormat(t *testing.T) {
	// Parse OpenAI Chat, serialize to Claude
	raw := []byte(`{
		"model": "gpt-4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		]
	}`)

	ir, err := ParseOpenAIChatRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := SerializeClaudeRequest(ir)
	if err != nil {
		t.Fatalf("Serialize to Claude failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// System should be top-level system field
	if obj["system"] != "You are helpful." {
		t.Errorf("system = %v", obj["system"])
	}
}
