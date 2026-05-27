// Package ir defines the intermediate representation (IR) for chat request and
// response translation between different AI API formats. The IR provides a
// unified, format-agnostic structure that eliminates per-translator JSON field
// manipulation and enables shared middleware for common operations such as tool
// name shortening, call ID normalization, and compact passthrough.
package ir

import "encoding/json"

// IRRequest is the unified intermediate representation for all chat request formats.
type IRRequest struct {
	Model         string          `json:"model"`
	System        []SystemBlock   `json:"system,omitempty"`
	Messages      []Message       `json:"messages,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    *ToolChoice     `json:"tool_choice,omitempty"`
	MaxTokens     *int64          `json:"max_tokens,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream"`
	Thinking      *ThinkingConfig `json:"thinking,omitempty"`
	// Passthrough holds fields that must survive translation without interpretation
	// (e.g. context_management, truncation, user, service_tier).
	Passthrough map[string]json.RawMessage `json:"passthrough,omitempty"`
	// Metadata carries translator-specific hints that do not map to any API field.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// SystemBlock represents a system instruction block.
type SystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// CacheControl represents caching hints for providers that support it.
type CacheControl struct {
	Type string `json:"type"`
}

// Message represents a single conversation message.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content,omitempty"`
	// ToolCalls is non-nil only for assistant messages that invoke tools.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID is set only for tool result messages (role=tool).
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name is an optional sender name (OpenAI extension).
	Name string `json:"name,omitempty"`
}

// ContentBlock represents a typed content element within a message.
type ContentBlock struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	ImageURL   string      `json:"image_url,omitempty"`
	Thinking   string      `json:"thinking,omitempty"`
	ToolUse    *ToolCall   `json:"tool_use,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	// Passthrough holds format-specific fields that do not map to the IR.
	Passthrough map[string]json.RawMessage `json:"passthrough,omitempty"`
}

// ToolCall represents a tool invocation.
type ToolCall struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"` // "function"
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolResult represents the output of a tool invocation.
type ToolResult struct {
	ToolCallID string `json:"tool_use_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Tool represents a tool definition.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	// Type distinguishes "function" from format-specific tool types (e.g. "web_search").
	Type string `json:"type,omitempty"`
	// OriginalName preserves the pre-shortening name for response translation.
	// This field is not serialized to JSON.
	OriginalName string `json:"-"`
}

// ToolChoice represents the tool selection strategy.
type ToolChoice struct {
	Type string `json:"type"` // "auto", "any", "none", "required", "function"
	Name string `json:"name,omitempty"`
	// DisableParallelToolUse is set when the client opts out of parallel tool calls.
	DisableParallelToolUse *bool `json:"disable_parallel_tool_use,omitempty"`
}

// ThinkingConfig represents thinking/extended reasoning configuration.
type ThinkingConfig struct {
	// Type is "enabled", "disabled", "adaptive", or "auto".
	Type string `json:"type"`
	// BudgetTokens is the Claude-style numeric budget.
	BudgetTokens *int64 `json:"budget_tokens,omitempty"`
	// Effort is the discrete level for level-based providers (low/medium/high/xhigh).
	Effort string `json:"effort,omitempty"`
}

// IRResponse is the unified intermediate representation for chat responses.
type IRResponse struct {
	ID           string         `json:"id"`
	Model        string         `json:"model"`
	Content      []ContentBlock `json:"content,omitempty"`
	ToolCalls    []ToolCall     `json:"tool_calls,omitempty"`
	FinishReason string         `json:"finish_reason,omitempty"`
	Usage        *Usage         `json:"usage,omitempty"`
	// Passthrough holds provider-specific response fields.
	Passthrough map[string]json.RawMessage `json:"passthrough,omitempty"`
}

// Usage represents token usage statistics.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}
