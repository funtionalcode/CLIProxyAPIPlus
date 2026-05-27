package ir

import (
	"encoding/json"
	"testing"
)

func TestParseClaudeRequest_Basic(t *testing.T) {
	raw := []byte(`{
		"model": "claude-3-opus",
		"stream": true,
		"system": "You are helpful.",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi!"}
		],
		"max_tokens": 4096,
		"temperature": 0.7
	}`)

	ir, err := ParseClaudeRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ir.Model != "claude-3-opus" {
		t.Errorf("Model = %q, want %q", ir.Model, "claude-3-opus")
	}
	if !ir.Stream {
		t.Error("Stream = false, want true")
	}
	if len(ir.System) != 1 || ir.System[0].Text != "You are helpful." {
		t.Errorf("System = %v, want single block", ir.System)
	}
	if len(ir.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(ir.Messages))
	}
	if ir.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want %q", ir.Messages[0].Role, "user")
	}
	if ir.Messages[1].Role != "assistant" {
		t.Errorf("Messages[1].Role = %q, want %q", ir.Messages[1].Role, "assistant")
	}
	if ir.MaxTokens == nil || *ir.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %v, want 4096", ir.MaxTokens)
	}
	if ir.Temperature == nil || *ir.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", ir.Temperature)
	}
}

func TestParseClaudeRequest_SystemArray(t *testing.T) {
	raw := []byte(`{
		"model": "test",
		"system": [
			{"type": "text", "text": "First instruction"},
			{"type": "text", "text": "Second instruction"}
		],
		"messages": []
	}`)

	ir, err := ParseClaudeRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(ir.System) != 2 {
		t.Fatalf("System len = %d, want 2", len(ir.System))
	}
	if ir.System[0].Text != "First instruction" {
		t.Errorf("System[0].Text = %q", ir.System[0].Text)
	}
	if ir.System[1].Text != "Second instruction" {
		t.Errorf("System[1].Text = %q", ir.System[1].Text)
	}
}

func TestParseClaudeRequest_ToolUse(t *testing.T) {
	raw := []byte(`{
		"model": "test",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_001", "name": "search", "input": {"q": "hello"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_001", "content": "result text"}
				]
			}
		]
	}`)

	ir, err := ParseClaudeRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(ir.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(ir.Messages))
	}

	// Assistant with tool_use
	assistant := ir.Messages[0]
	if len(assistant.Content) != 1 {
		t.Fatalf("assistant content len = %d, want 1", len(assistant.Content))
	}
	if assistant.Content[0].Type != "tool_use" {
		t.Errorf("content type = %q, want %q", assistant.Content[0].Type, "tool_use")
	}
	if assistant.Content[0].ToolUse == nil {
		t.Fatal("ToolUse is nil")
	}
	if assistant.Content[0].ToolUse.ID != "call_001" {
		t.Errorf("ToolUse.ID = %q", assistant.Content[0].ToolUse.ID)
	}
	if assistant.Content[0].ToolUse.Name != "search" {
		t.Errorf("ToolUse.Name = %q", assistant.Content[0].ToolUse.Name)
	}

	// User with tool_result
	user := ir.Messages[1]
	if len(user.Content) != 1 {
		t.Fatalf("user content len = %d, want 1", len(user.Content))
	}
	if user.Content[0].Type != "tool_result" {
		t.Errorf("content type = %q, want %q", user.Content[0].Type, "tool_result")
	}
	if user.Content[0].ToolResult == nil {
		t.Fatal("ToolResult is nil")
	}
	if user.Content[0].ToolResult.ToolCallID != "call_001" {
		t.Errorf("ToolResult.ToolCallID = %q", user.Content[0].ToolResult.ToolCallID)
	}
}

func TestParseClaudeRequest_Tools(t *testing.T) {
	raw := []byte(`{
		"model": "test",
		"messages": [],
		"tools": [
			{
				"name": "search",
				"description": "Search the web",
				"input_schema": {"type": "object", "properties": {"q": {"type": "string"}}}
			}
		],
		"tool_choice": {"type": "auto"}
	}`)

	ir, err := ParseClaudeRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(ir.Tools) != 1 {
		t.Fatalf("Tools len = %d, want 1", len(ir.Tools))
	}
	if ir.Tools[0].Name != "search" {
		t.Errorf("Tools[0].Name = %q", ir.Tools[0].Name)
	}
	if ir.Tools[0].Description != "Search the web" {
		t.Errorf("Tools[0].Description = %q", ir.Tools[0].Description)
	}
	if ir.ToolChoice == nil || ir.ToolChoice.Type != "auto" {
		t.Errorf("ToolChoice = %v, want auto", ir.ToolChoice)
	}
}

func TestParseClaudeRequest_Thinking(t *testing.T) {
	raw := []byte(`{
		"model": "test",
		"messages": [],
		"thinking": {"type": "enabled", "budget_tokens": 8192}
	}`)

	ir, err := ParseClaudeRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ir.Thinking == nil {
		t.Fatal("Thinking is nil")
	}
	if ir.Thinking.Type != "enabled" {
		t.Errorf("Thinking.Type = %q", ir.Thinking.Type)
	}
	if ir.Thinking.BudgetTokens == nil || *ir.Thinking.BudgetTokens != 8192 {
		t.Errorf("Thinking.BudgetTokens = %v, want 8192", ir.Thinking.BudgetTokens)
	}
}

func TestParseClaudeRequest_Passthrough(t *testing.T) {
	raw := []byte(`{
		"model": "test",
		"messages": [],
		"context_management": {"compaction": {"enabled": true}},
		"truncation": {"type": "auto"},
		"custom_field": "preserved"
	}`)

	ir, err := ParseClaudeRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ir.Passthrough == nil {
		t.Fatal("Passthrough is nil")
	}
	if _, ok := ir.Passthrough["context_management"]; !ok {
		t.Error("missing context_management in passthrough")
	}
	if _, ok := ir.Passthrough["truncation"]; !ok {
		t.Error("missing truncation in passthrough")
	}
	if _, ok := ir.Passthrough["custom_field"]; !ok {
		t.Error("missing custom_field in passthrough")
	}
}

func TestSerializeClaudeRequest_Basic(t *testing.T) {
	maxTokens := int64(4096)
	temp := 0.7

	ir := &IRRequest{
		Model:  "claude-3-opus",
		Stream: true,
		System: []SystemBlock{{Type: "text", Text: "You are helpful."}},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hello"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "Hi!"}}},
		},
		MaxTokens:   &maxTokens,
		Temperature: &temp,
	}

	data, err := SerializeClaudeRequest(ir)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["model"] != "claude-3-opus" {
		t.Errorf("model = %v", obj["model"])
	}
	if obj["stream"] != true {
		t.Errorf("stream = %v", obj["stream"])
	}
	if obj["system"] != "You are helpful." {
		t.Errorf("system = %v", obj["system"])
	}
	if obj["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v", obj["max_tokens"])
	}
}

func TestSerializeClaudeRequest_Passthrough(t *testing.T) {
	ir := &IRRequest{
		Model:  "test",
		Stream: false,
		Passthrough: map[string]json.RawMessage{
			"context_management": json.RawMessage(`{"compaction":{"enabled":true}}`),
			"truncation":         json.RawMessage(`{"type":"auto"}`),
		},
	}

	data, err := SerializeClaudeRequest(ir)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["context_management"] == nil {
		t.Error("missing context_management")
	}
	if obj["truncation"] == nil {
		t.Error("missing truncation")
	}
}

func TestClaudeRequest_RoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "claude-3-opus",
		"stream": true,
		"system": "You are helpful.",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi!"}
		],
		"max_tokens": 4096,
		"temperature": 0.7,
		"context_management": {"compaction": {"enabled": true}},
		"truncation": {"type": "auto"}
	}`)

	ir, err := ParseClaudeRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := SerializeClaudeRequest(ir)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Core fields preserved
	if obj["model"] != "claude-3-opus" {
		t.Errorf("model = %v", obj["model"])
	}
	if obj["system"] != "You are helpful." {
		t.Errorf("system = %v", obj["system"])
	}

	// Passthrough preserved
	if obj["context_management"] == nil {
		t.Error("missing context_management after round-trip")
	}
	if obj["truncation"] == nil {
		t.Error("missing truncation after round-trip")
	}
}

func TestClaudeRequest_CrossFormat(t *testing.T) {
	// Parse Claude, serialize to OpenAI Chat
	raw := []byte(`{
		"model": "claude-3-opus",
		"stream": true,
		"system": "You are helpful.",
		"messages": [
			{"role": "user", "content": "Hello"}
		],
		"tools": [
			{"name": "search", "description": "Search", "input_schema": {"type": "object"}}
		],
		"tool_choice": {"type": "auto"}
	}`)

	ir, err := ParseClaudeRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := SerializeOpenAIChatRequest(ir)
	if err != nil {
		t.Fatalf("Serialize to OpenAI failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// System should become a system message
	msgs := obj["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("messages len = %d, want >= 2", len(msgs))
	}
	firstMsg := msgs[0].(map[string]any)
	if firstMsg["role"] != "system" {
		t.Errorf("first message role = %v, want system", firstMsg["role"])
	}
	if firstMsg["content"] != "You are helpful." {
		t.Errorf("first message content = %v", firstMsg["content"])
	}
}
