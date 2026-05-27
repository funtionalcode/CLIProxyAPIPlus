package ir

import (
	"encoding/json"
	"testing"
)

func TestParseCodexRequest_DelegatesToOpenAIResponses(t *testing.T) {
	raw := []byte(`{
		"model": "codex-mini",
		"stream": true,
		"instructions": "You are a coding assistant.",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Write hello world"}]}
		],
		"max_output_tokens": 2048
	}`)

	ir, err := ParseCodexRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if ir.Model != "codex-mini" {
		t.Errorf("Model = %q", ir.Model)
	}
	if len(ir.System) != 1 || ir.System[0].Text != "You are a coding assistant." {
		t.Errorf("System = %v", ir.System)
	}
	if len(ir.Messages) != 1 {
		t.Fatalf("Messages len = %d, want 1", len(ir.Messages))
	}
	if ir.MaxTokens == nil || *ir.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %v", ir.MaxTokens)
	}
}

func TestSerializeCodexRequest_DelegatesToOpenAIResponses(t *testing.T) {
	maxTokens := int64(2048)
	ir := &IRRequest{
		Model:     "codex-mini",
		Stream:    true,
		System:    []SystemBlock{{Type: "text", Text: "You are a coding assistant."}},
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Write hello world"}}}},
		MaxTokens: &maxTokens,
	}

	data, err := SerializeCodexRequest(ir)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["model"] != "codex-mini" {
		t.Errorf("model = %v", obj["model"])
	}
	if obj["instructions"] != "You are a coding assistant." {
		t.Errorf("instructions = %v", obj["instructions"])
	}
}

func TestParseCodexResponse_Basic(t *testing.T) {
	raw := []byte(`{
		"id": "resp_001",
		"model": "codex-mini",
		"output": [
			{
				"type": "message",
				"role": "assistant",
				"content": [
					{"type": "output_text", "text": "Hello world code"}
				]
			}
		],
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"total_tokens": 150
		},
		"stop_reason": "completed"
	}`)

	resp, err := ParseCodexResponse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if resp.ID != "resp_001" {
		t.Errorf("ID = %q", resp.ID)
	}
	if resp.Model != "codex-mini" {
		t.Errorf("Model = %q", resp.Model)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content len = %d, want 1", len(resp.Content))
	}
	if resp.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q", resp.Content[0].Type)
	}
	if resp.Content[0].Text != "Hello world code" {
		t.Errorf("Content[0].Text = %q", resp.Content[0].Text)
	}
	if resp.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if resp.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d", resp.Usage.CompletionTokens)
	}
	if resp.FinishReason != "completed" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
}

func TestParseCodexResponse_ToolCalls(t *testing.T) {
	raw := []byte(`{
		"id": "resp_002",
		"model": "codex-mini",
		"output": [
			{
				"type": "function_call",
				"call_id": "call_001",
				"name": "read_file",
				"arguments": "{\"path\":\"main.go\"}"
			}
		]
	}`)

	resp, err := ParseCodexResponse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_001" {
		t.Errorf("ID = %q", tc.ID)
	}
	if tc.Name != "read_file" {
		t.Errorf("Name = %q", tc.Name)
	}
	if tc.Type != "function" {
		t.Errorf("Type = %q", tc.Type)
	}
}

func TestParseCodexResponse_Reasoning(t *testing.T) {
	raw := []byte(`{
		"id": "resp_003",
		"model": "codex-mini",
		"output": [
			{
				"type": "reasoning",
				"summary": [{"type": "summary_text", "text": "Thinking about the problem..."}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Here is the solution"}]
			}
		]
	}`)

	resp, err := ParseCodexResponse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("Content len = %d, want 2", len(resp.Content))
	}
	if resp.Content[0].Type != "thinking" {
		t.Errorf("Content[0].Type = %q, want thinking", resp.Content[0].Type)
	}
	if resp.Content[1].Type != "text" {
		t.Errorf("Content[1].Type = %q, want text", resp.Content[1].Type)
	}
}

func TestParseCodexResponse_Passthrough(t *testing.T) {
	raw := []byte(`{
		"id": "resp_004",
		"model": "codex-mini",
		"status": "completed",
		"output": []
	}`)

	resp, err := ParseCodexResponse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if resp.FinishReason != "completed" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
}

func TestSerializeCodexResponse_Basic(t *testing.T) {
	resp := &IRResponse{
		ID:    "resp_001",
		Model: "codex-mini",
		Content: []ContentBlock{
			{Type: "text", Text: "Hello world code"},
		},
		Usage: &Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
		FinishReason: "completed",
	}

	data, err := SerializeCodexResponse(resp)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["id"] != "resp_001" {
		t.Errorf("id = %v", obj["id"])
	}
	if obj["model"] != "codex-mini" {
		t.Errorf("model = %v", obj["model"])
	}
	if obj["stop_reason"] != "completed" {
		t.Errorf("stop_reason = %v", obj["stop_reason"])
	}

	output, ok := obj["output"].([]any)
	if !ok {
		t.Fatal("output is not array")
	}
	if len(output) != 1 {
		t.Fatalf("output len = %d, want 1", len(output))
	}
}

func TestSerializeCodexResponse_ToolCalls(t *testing.T) {
	resp := &IRResponse{
		ID:    "resp_002",
		Model: "codex-mini",
		ToolCalls: []ToolCall{
			{ID: "call_001", Type: "function", Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`)},
		},
	}

	data, err := SerializeCodexResponse(resp)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	output, ok := obj["output"].([]any)
	if !ok {
		t.Fatal("output is not array")
	}
	if len(output) != 1 {
		t.Fatalf("output len = %d, want 1", len(output))
	}

	item := output[0].(map[string]any)
	if item["type"] != "function_call" {
		t.Errorf("type = %v", item["type"])
	}
	if item["name"] != "read_file" {
		t.Errorf("name = %v", item["name"])
	}
	if item["call_id"] != "call_001" {
		t.Errorf("call_id = %v", item["call_id"])
	}
}

func TestCodexResponse_RoundTrip(t *testing.T) {
	raw := []byte(`{
		"id": "resp_001",
		"model": "codex-mini",
		"output": [
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Hello world"}]
			},
			{
				"type": "function_call",
				"call_id": "call_001",
				"name": "search",
				"arguments": "{\"q\":\"test\"}"
			}
		],
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"total_tokens": 150
		},
		"stop_reason": "completed"
	}`)

	resp, err := ParseCodexResponse(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := SerializeCodexResponse(resp)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["id"] != "resp_001" {
		t.Errorf("id = %v", obj["id"])
	}
	if obj["stop_reason"] != "completed" {
		t.Errorf("stop_reason = %v", obj["stop_reason"])
	}

	output := obj["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("output len = %d, want 2", len(output))
	}
}

func TestCodexRequest_RoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "codex-mini",
		"stream": true,
		"instructions": "You are a coding assistant.",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Write hello world"}]}
		],
		"max_output_tokens": 2048,
		"store": true
	}`)

	ir, err := ParseCodexRequest(raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := SerializeCodexRequest(ir)
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["model"] != "codex-mini" {
		t.Errorf("model = %v", obj["model"])
	}
	if obj["instructions"] != "You are a coding assistant." {
		t.Errorf("instructions = %v", obj["instructions"])
	}
	if obj["store"] == nil {
		t.Error("missing store after round-trip")
	}
}
