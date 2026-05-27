package ir

import (
	"encoding/json"
	"testing"
)

func TestParseOpenAIResponsesRequest_Basic(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"stream": true,
		"instructions": "You are helpful.",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hello"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Hi!"}]}
		],
		"max_output_tokens": 4096,
		"temperature": 0.7,
		"top_p": 0.9
	}`)

	ir, err := ParseOpenAIResponsesRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ir.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", ir.Model, "gpt-4o")
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

func TestParseOpenAIResponsesRequest_StringInput(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"input": "Hello world"
	}`)

	ir, err := ParseOpenAIResponsesRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(ir.Messages) != 1 {
		t.Fatalf("Messages len = %d, want 1", len(ir.Messages))
	}
	if ir.Messages[0].Role != "user" {
		t.Errorf("Role = %q, want user", ir.Messages[0].Role)
	}
	if len(ir.Messages[0].Content) != 1 || ir.Messages[0].Content[0].Text != "Hello world" {
		t.Errorf("Content = %v", ir.Messages[0].Content)
	}
}

func TestParseOpenAIResponsesRequest_FunctionCall(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"input": [
			{"type": "function_call", "id": "call_001", "name": "search", "arguments": "{\"q\":\"hello\"}"},
			{"type": "function_call_output", "call_id": "call_001", "output": "result text"}
		]
	}`)

	ir, err := ParseOpenAIResponsesRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(ir.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(ir.Messages))
	}

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

	tool := ir.Messages[1]
	if tool.ToolCallID != "call_001" {
		t.Errorf("ToolCallID = %q", tool.ToolCallID)
	}
}

func TestParseOpenAIResponsesRequest_Reasoning(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"input": [],
		"reasoning": {"effort": "high", "summary": "auto"}
	}`)

	ir, err := ParseOpenAIResponsesRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ir.Thinking == nil {
		t.Fatal("Thinking is nil")
	}
	if ir.Thinking.Type != "enabled" {
		t.Errorf("Thinking.Type = %q", ir.Thinking.Type)
	}
	if ir.Thinking.Effort != "high" {
		t.Errorf("Thinking.Effort = %q", ir.Thinking.Effort)
	}
}

func TestParseOpenAIResponsesRequest_Tools(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"input": [],
		"tools": [
			{
				"type": "function",
				"name": "search",
				"description": "Search the web",
				"parameters": {"type": "object"}
			}
		],
		"tool_choice": "auto"
	}`)

	ir, err := ParseOpenAIResponsesRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(ir.Tools) != 1 {
		t.Fatalf("Tools len = %d, want 1", len(ir.Tools))
	}
	if ir.Tools[0].Name != "search" {
		t.Errorf("Tools[0].Name = %q", ir.Tools[0].Name)
	}
	if ir.ToolChoice == nil || ir.ToolChoice.Type != "auto" {
		t.Errorf("ToolChoice = %v", ir.ToolChoice)
	}
}

func TestParseOpenAIResponsesRequest_Passthrough(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"input": [],
		"store": true,
		"user": "user-123"
	}`)

	ir, err := ParseOpenAIResponsesRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ir.Passthrough == nil {
		t.Fatal("Passthrough is nil")
	}
	if _, ok := ir.Passthrough["store"]; !ok {
		t.Error("missing store")
	}
	if _, ok := ir.Passthrough["user"]; !ok {
		t.Error("missing user")
	}
}

func TestSerializeOpenAIResponsesRequest_Basic(t *testing.T) {
	maxTokens := int64(4096)
	ir := &IRRequest{
		Model:     "gpt-4o",
		Stream:    true,
		System:    []SystemBlock{{Type: "text", Text: "You are helpful."}},
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hello"}}}},
		MaxTokens: &maxTokens,
	}

	data, err := SerializeOpenAIResponsesRequest(ir)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["model"] != "gpt-4o" {
		t.Errorf("model = %v", obj["model"])
	}
	if obj["instructions"] != "You are helpful." {
		t.Errorf("instructions = %v", obj["instructions"])
	}
	if obj["max_output_tokens"] != float64(4096) {
		t.Errorf("max_output_tokens = %v", obj["max_output_tokens"])
	}
}

func TestOpenAIResponsesRequest_RoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"stream": true,
		"instructions": "You are helpful.",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hello"}]}
		],
		"store": true
	}`)

	ir, err := ParseOpenAIResponsesRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := SerializeOpenAIResponsesRequest(ir)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["model"] != "gpt-4o" {
		t.Errorf("model = %v", obj["model"])
	}
	if obj["instructions"] != "You are helpful." {
		t.Errorf("instructions = %v", obj["instructions"])
	}
	if obj["store"] == nil {
		t.Error("missing store after round-trip")
	}
}

func TestOpenAIResponsesRequest_CrossFormat(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"instructions": "You are helpful.",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hello"}]}
		]
	}`)

	ir, err := ParseOpenAIResponsesRequest(raw)
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
	if obj["system"] != "You are helpful." {
		t.Errorf("system = %v", obj["system"])
	}
}
