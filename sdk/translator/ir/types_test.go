package ir

import (
	"encoding/json"
	"testing"
)

func TestIRRequest_Serialization(t *testing.T) {
	maxTokens := int64(4096)
	temp := 0.7
	budget := int64(8192)

	req := IRRequest{
		Model: "claude-3-opus",
		System: []SystemBlock{
			{Type: "text", Text: "You are helpful."},
		},
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "Hello"},
				},
			},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "text", Text: "Hi there!"},
				},
			},
		},
		Tools: []Tool{
			{
				Name:        "get_weather",
				Description: "Get weather info",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
				Type:        "function",
			},
		},
		ToolChoice:  &ToolChoice{Type: "auto"},
		MaxTokens:   &maxTokens,
		Temperature: &temp,
		Stream:      true,
		Thinking: &ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip IRRequest
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if roundTrip.Model != req.Model {
		t.Errorf("Model = %q, want %q", roundTrip.Model, req.Model)
	}
	if len(roundTrip.Messages) != len(req.Messages) {
		t.Fatalf("Messages len = %d, want %d", len(roundTrip.Messages), len(req.Messages))
	}
	if roundTrip.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want %q", roundTrip.Messages[0].Role, "user")
	}
	if len(roundTrip.Tools) != 1 {
		t.Fatalf("Tools len = %d, want 1", len(roundTrip.Tools))
	}
	if roundTrip.Tools[0].Name != "get_weather" {
		t.Errorf("Tools[0].Name = %q, want %q", roundTrip.Tools[0].Name, "get_weather")
	}
	if roundTrip.MaxTokens == nil || *roundTrip.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %v, want 4096", roundTrip.MaxTokens)
	}
	if roundTrip.Temperature == nil || *roundTrip.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", roundTrip.Temperature)
	}
	if !roundTrip.Stream {
		t.Error("Stream = false, want true")
	}
	if roundTrip.Thinking == nil || roundTrip.Thinking.BudgetTokens == nil || *roundTrip.Thinking.BudgetTokens != 8192 {
		t.Errorf("Thinking.BudgetTokens = %v, want 8192", roundTrip.Thinking)
	}
}

func TestToolCall_Serialization(t *testing.T) {
	tc := ToolCall{
		ID:        "call_abc123",
		Type:      "function",
		Name:      "search",
		Arguments: json.RawMessage(`{"query":"test"}`),
	}

	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip ToolCall
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if roundTrip.ID != tc.ID {
		t.Errorf("ID = %q, want %q", roundTrip.ID, tc.ID)
	}
	if roundTrip.Name != tc.Name {
		t.Errorf("Name = %q, want %q", roundTrip.Name, tc.Name)
	}
}

func TestToolResult_Serialization(t *testing.T) {
	tr := ToolResult{
		ToolCallID: "call_abc123",
		Content:    `{"temperature": 72}`,
		IsError:    false,
	}

	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip ToolResult
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if roundTrip.ToolCallID != tr.ToolCallID {
		t.Errorf("ToolCallID = %q, want %q", roundTrip.ToolCallID, tr.ToolCallID)
	}
	if roundTrip.Content != tr.Content {
		t.Errorf("Content = %q, want %q", roundTrip.Content, tr.Content)
	}
}

func TestTool_OriginalNameNotSerialized(t *testing.T) {
	tool := Tool{
		Name:         "short",
		OriginalName: "a_very_long_tool_name_that_was_shortened",
	}

	data, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip Tool
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if roundTrip.OriginalName != "" {
		t.Errorf("OriginalName = %q, want empty (should not be serialized)", roundTrip.OriginalName)
	}
}

func TestIRResponse_Serialization(t *testing.T) {
	resp := IRResponse{
		ID:    "resp_123",
		Model: "claude-3-opus",
		Content: []ContentBlock{
			{Type: "text", Text: "Hello!"},
		},
		FinishReason: "end_turn",
		Usage: &Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip IRResponse
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if roundTrip.ID != resp.ID {
		t.Errorf("ID = %q, want %q", roundTrip.ID, resp.ID)
	}
	if roundTrip.Usage == nil || roundTrip.Usage.TotalTokens != 150 {
		t.Errorf("Usage.TotalTokens = %v, want 150", roundTrip.Usage)
	}
}

func TestMessage_WithToolCalls(t *testing.T) {
	msg := Message{
		Role: "assistant",
		ToolCalls: []ToolCall{
			{
				ID:        "call_001",
				Type:      "function",
				Name:      "search",
				Arguments: json.RawMessage(`{"q":"hello"}`),
			},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip Message
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(roundTrip.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(roundTrip.ToolCalls))
	}
	if roundTrip.ToolCalls[0].Name != "search" {
		t.Errorf("ToolCalls[0].Name = %q, want %q", roundTrip.ToolCalls[0].Name, "search")
	}
}

func TestContentBlock_WithToolUse(t *testing.T) {
	block := ContentBlock{
		Type: "tool_use",
		ToolUse: &ToolCall{
			ID:        "call_001",
			Type:      "function",
			Name:      "search",
			Arguments: json.RawMessage(`{"q":"hello"}`),
		},
	}

	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip ContentBlock
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if roundTrip.ToolUse == nil {
		t.Fatal("ToolUse is nil")
	}
	if roundTrip.ToolUse.Name != "search" {
		t.Errorf("ToolUse.Name = %q, want %q", roundTrip.ToolUse.Name, "search")
	}
}

func TestContentBlock_WithToolResult(t *testing.T) {
	block := ContentBlock{
		Type: "tool_result",
		ToolResult: &ToolResult{
			ToolCallID: "call_001",
			Content:    "result text",
			IsError:    true,
		},
	}

	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip ContentBlock
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if roundTrip.ToolResult == nil {
		t.Fatal("ToolResult is nil")
	}
	if !roundTrip.ToolResult.IsError {
		t.Error("ToolResult.IsError = false, want true")
	}
}

func TestThinkingConfig_WithEffort(t *testing.T) {
	tc := ThinkingConfig{
		Type:   "enabled",
		Effort: "high",
	}

	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip ThinkingConfig
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if roundTrip.Effort != "high" {
		t.Errorf("Effort = %q, want %q", roundTrip.Effort, "high")
	}
}
