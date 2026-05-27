package ir

import (
	"encoding/json"
	"fmt"
)

// ParseClaudeRequest parses a Claude Messages API request into the IR.
func ParseClaudeRequest(rawJSON []byte) (*IRRequest, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &obj); err != nil {
		return nil, fmt.Errorf("ir: parse claude request: %w", err)
	}

	ir := &IRRequest{}

	// model
	if v, ok := obj["model"]; ok {
		_ = json.Unmarshal(v, &ir.Model)
		delete(obj, "model")
	}

	// stream
	if v, ok := obj["stream"]; ok {
		_ = json.Unmarshal(v, &ir.Stream)
		delete(obj, "stream")
	}

	// system — string or array of {type:"text", text:"..."}
	if v, ok := obj["system"]; ok {
		ir.System = parseClaudeSystem(v)
		delete(obj, "system")
	}

	// messages
	if v, ok := obj["messages"]; ok {
		msgs, err := parseClaudeMessages(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse claude messages: %w", err)
		}
		ir.Messages = msgs
		delete(obj, "messages")
	}

	// tools
	if v, ok := obj["tools"]; ok {
		tools, err := parseClaudeTools(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse claude tools: %w", err)
		}
		ir.Tools = tools
		delete(obj, "tools")
	}

	// tool_choice
	if v, ok := obj["tool_choice"]; ok {
		tc, err := parseClaudeToolChoice(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse claude tool_choice: %w", err)
		}
		ir.ToolChoice = tc
		delete(obj, "tool_choice")
	}

	// max_tokens
	if v, ok := obj["max_tokens"]; ok {
		var mt int64
		_ = json.Unmarshal(v, &mt)
		ir.MaxTokens = &mt
		delete(obj, "max_tokens")
	}

	// temperature
	if v, ok := obj["temperature"]; ok {
		var t float64
		_ = json.Unmarshal(v, &t)
		ir.Temperature = &t
		delete(obj, "temperature")
	}

	// top_p
	if v, ok := obj["top_p"]; ok {
		var tp float64
		_ = json.Unmarshal(v, &tp)
		ir.TopP = &tp
		delete(obj, "top_p")
	}

	// stop_sequences
	if v, ok := obj["stop_sequences"]; ok {
		_ = json.Unmarshal(v, &ir.StopSequences)
		delete(obj, "stop_sequences")
	}

	// thinking
	if v, ok := obj["thinking"]; ok {
		tc, err := parseClaudeThinking(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse claude thinking: %w", err)
		}
		ir.Thinking = tc
		delete(obj, "thinking")
	}

	// Store remaining fields as passthrough
	if len(obj) > 0 {
		ir.Passthrough = make(map[string]json.RawMessage, len(obj))
		for k, v := range obj {
			ir.Passthrough[k] = v
		}
	}

	return ir, nil
}

// SerializeClaudeRequest serializes an IR request into Claude Messages API format.
func SerializeClaudeRequest(ir *IRRequest) ([]byte, error) {
	obj := make(map[string]json.RawMessage)

	obj["model"] = mustMarshal(ir.Model)
	obj["stream"] = mustMarshal(ir.Stream)

	// system
	if len(ir.System) > 0 {
		if len(ir.System) == 1 && ir.System[0].CacheControl == nil {
			obj["system"] = mustMarshal(ir.System[0].Text)
		} else {
			arr := make([]map[string]any, len(ir.System))
			for i, sb := range ir.System {
				arr[i] = map[string]any{"type": sb.Type, "text": sb.Text}
				if sb.CacheControl != nil {
					arr[i]["cache_control"] = sb.CacheControl
				}
			}
			obj["system"] = mustMarshal(arr)
		}
	}

	// messages
	if len(ir.Messages) > 0 {
		msgs, err := serializeClaudeMessages(ir.Messages)
		if err != nil {
			return nil, err
		}
		obj["messages"] = msgs
	}

	// tools
	if len(ir.Tools) > 0 {
		tools, err := serializeClaudeTools(ir.Tools)
		if err != nil {
			return nil, err
		}
		obj["tools"] = tools
	}

	// tool_choice
	if ir.ToolChoice != nil {
		tc, err := serializeClaudeToolChoice(ir.ToolChoice)
		if err != nil {
			return nil, err
		}
		obj["tool_choice"] = tc
	}

	if ir.MaxTokens != nil {
		obj["max_tokens"] = mustMarshal(*ir.MaxTokens)
	}
	if ir.Temperature != nil {
		obj["temperature"] = mustMarshal(*ir.Temperature)
	}
	if ir.TopP != nil {
		obj["top_p"] = mustMarshal(*ir.TopP)
	}
	if len(ir.StopSequences) > 0 {
		obj["stop_sequences"] = mustMarshal(ir.StopSequences)
	}
	if ir.Thinking != nil {
		thinking := map[string]any{"type": ir.Thinking.Type}
		if ir.Thinking.BudgetTokens != nil {
			thinking["budget_tokens"] = *ir.Thinking.BudgetTokens
		}
		obj["thinking"] = mustMarshal(thinking)
	}

	// Merge passthrough fields
	for k, v := range ir.Passthrough {
		if _, exists := obj[k]; !exists {
			obj[k] = v
		}
	}

	return json.Marshal(obj)
}

// parseClaudeSystem parses the Claude system field (string or array).
func parseClaudeSystem(raw json.RawMessage) []SystemBlock {
	// Try as string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []SystemBlock{{Type: "text", Text: s}}
	}

	// Try as array
	var arr []struct {
		Type         string        `json:"type"`
		Text         string        `json:"text"`
		CacheControl *CacheControl `json:"cache_control,omitempty"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}

	var blocks []SystemBlock
	for _, a := range arr {
		if a.Type == "text" {
			blocks = append(blocks, SystemBlock{
				Type:         a.Type,
				Text:         a.Text,
				CacheControl: a.CacheControl,
			})
		}
	}
	return blocks
}

// parseClaudeMessages parses Claude messages into IR Messages.
func parseClaudeMessages(raw json.RawMessage) ([]Message, error) {
	var rawMsgs []json.RawMessage
	if err := json.Unmarshal(raw, &rawMsgs); err != nil {
		return nil, err
	}

	msgs := make([]Message, 0, len(rawMsgs))
	for _, rawMsg := range rawMsgs {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rawMsg, &obj); err != nil {
			continue
		}

		msg := Message{}
		if v, ok := obj["role"]; ok {
			_ = json.Unmarshal(v, &msg.Role)
		}
		if v, ok := obj["name"]; ok {
			_ = json.Unmarshal(v, &msg.Name)
		}

		// content — string or array of content blocks
		if v, ok := obj["content"]; ok {
			blocks, err := parseClaudeContent(v, msg.Role)
			if err != nil {
				return nil, err
			}
			msg.Content = blocks
		}

		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// parseClaudeContent parses Claude content (string or array of content blocks).
func parseClaudeContent(raw json.RawMessage, role string) ([]ContentBlock, error) {
	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []ContentBlock{{Type: "text", Text: s}}, nil
	}

	// Try as array
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(raw, &rawBlocks); err != nil {
		return nil, err
	}

	blocks := make([]ContentBlock, 0, len(rawBlocks))
	for _, rawBlock := range rawBlocks {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rawBlock, &obj); err != nil {
			continue
		}

		var blockType string
		if v, ok := obj["type"]; ok {
			_ = json.Unmarshal(v, &blockType)
		}

		cb := ContentBlock{Type: blockType}

		switch blockType {
		case "text":
			if v, ok := obj["text"]; ok {
				_ = json.Unmarshal(v, &cb.Text)
			}

		case "image":
			cb.Type = "image"
			// Store as passthrough to preserve source structure
			cb.Passthrough = make(map[string]json.RawMessage)
			for k, v := range obj {
				if k != "type" {
					cb.Passthrough[k] = v
				}
			}

		case "thinking":
			if v, ok := obj["thinking"]; ok {
				_ = json.Unmarshal(v, &cb.Thinking)
			}
			// Preserve signature in passthrough
			if v, ok := obj["signature"]; ok {
				cb.Passthrough = map[string]json.RawMessage{"signature": v}
			}

		case "tool_use":
			tc := &ToolCall{Type: "function"}
			if v, ok := obj["id"]; ok {
				_ = json.Unmarshal(v, &tc.ID)
			}
			if v, ok := obj["name"]; ok {
				_ = json.Unmarshal(v, &tc.Name)
			}
			if v, ok := obj["input"]; ok {
				tc.Arguments = v
			}
			cb.ToolUse = tc

		case "tool_result":
			tr := &ToolResult{}
			if v, ok := obj["tool_use_id"]; ok {
				_ = json.Unmarshal(v, &tr.ToolCallID)
			}
			if v, ok := obj["content"]; ok {
				// content can be string or array
				var s string
				if err := json.Unmarshal(v, &s); err == nil {
					tr.Content = s
				} else {
					// Store raw for complex content
					tr.Content = string(v)
				}
			}
			if v, ok := obj["is_error"]; ok {
				_ = json.Unmarshal(v, &tr.IsError)
			}
			cb.ToolResult = tr

		default:
			// Preserve unknown types
			cb.Passthrough = make(map[string]json.RawMessage)
			for k, v := range obj {
				if k != "type" {
					cb.Passthrough[k] = v
				}
			}
		}

		blocks = append(blocks, cb)
	}
	return blocks, nil
}

// parseClaudeTools parses Claude tool definitions into IR Tools.
func parseClaudeTools(raw json.RawMessage) ([]Tool, error) {
	var rawTools []json.RawMessage
	if err := json.Unmarshal(raw, &rawTools); err != nil {
		return nil, err
	}

	tools := make([]Tool, 0, len(rawTools))
	for _, rawTool := range rawTools {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rawTool, &obj); err != nil {
			continue
		}

		tool := Tool{}
		if v, ok := obj["name"]; ok {
			_ = json.Unmarshal(v, &tool.Name)
		}
		if v, ok := obj["description"]; ok {
			_ = json.Unmarshal(v, &tool.Description)
		}
		if v, ok := obj["input_schema"]; ok {
			tool.Parameters = v
		}
		if v, ok := obj["type"]; ok {
			_ = json.Unmarshal(v, &tool.Type)
		}
		// Skip cache_control, defer_loading

		tools = append(tools, tool)
	}
	return tools, nil
}

// parseClaudeToolChoice parses Claude tool_choice into IR ToolChoice.
func parseClaudeToolChoice(raw json.RawMessage) (*ToolChoice, error) {
	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		tc := &ToolChoice{Type: s}
		return tc, nil
	}

	// Try as object
	var obj struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return &ToolChoice{Type: obj.Type, Name: obj.Name, DisableParallelToolUse: obj.DisableParallelToolUse}, nil
}

// parseClaudeThinking parses Claude thinking config into IR ThinkingConfig.
func parseClaudeThinking(raw json.RawMessage) (*ThinkingConfig, error) {
	var obj struct {
		Type         string `json:"type"`
		BudgetTokens *int64 `json:"budget_tokens,omitempty"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return &ThinkingConfig{
		Type:         obj.Type,
		BudgetTokens: obj.BudgetTokens,
	}, nil
}

// serializeClaudeMessages serializes IR Messages to Claude format.
func serializeClaudeMessages(msgs []Message) (json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(msgs))
	for _, msg := range msgs {
		obj := make(map[string]json.RawMessage)
		obj["role"] = mustMarshal(msg.Role)
		if msg.Name != "" {
			obj["name"] = mustMarshal(msg.Name)
		}

		// Serialize content blocks
		if len(msg.Content) > 0 || len(msg.ToolCalls) > 0 {
			blocks := make([]json.RawMessage, 0, len(msg.Content)+len(msg.ToolCalls))

			for _, cb := range msg.Content {
				block, err := serializeClaudeContentBlock(cb)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, block)
			}

			if len(blocks) > 0 {
				obj["content"] = mustMarshal(blocks)
			}
		}

		data, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		result = append(result, data)
	}
	return mustMarshal(result), nil
}

// serializeClaudeContentBlock serializes a single IR ContentBlock to Claude format.
func serializeClaudeContentBlock(cb ContentBlock) (json.RawMessage, error) {
	obj := make(map[string]json.RawMessage)
	obj["type"] = mustMarshal(cb.Type)

	switch cb.Type {
	case "text":
		obj["text"] = mustMarshal(cb.Text)

	case "image":
		for k, v := range cb.Passthrough {
			obj[k] = v
		}

	case "thinking":
		obj["thinking"] = mustMarshal(cb.Thinking)
		if sig, ok := cb.Passthrough["signature"]; ok {
			obj["signature"] = sig
		}

	case "tool_use":
		if cb.ToolUse != nil {
			obj["id"] = mustMarshal(cb.ToolUse.ID)
			obj["name"] = mustMarshal(cb.ToolUse.Name)
			obj["input"] = cb.ToolUse.Arguments
		}

	case "tool_result":
		if cb.ToolResult != nil {
			obj["tool_use_id"] = mustMarshal(cb.ToolResult.ToolCallID)
			obj["content"] = mustMarshal(cb.ToolResult.Content)
			if cb.ToolResult.IsError {
				obj["is_error"] = mustMarshal(true)
			}
		}

	default:
		for k, v := range cb.Passthrough {
			obj[k] = v
		}
	}

	return json.Marshal(obj)
}

// serializeClaudeTools serializes IR Tools to Claude format.
func serializeClaudeTools(tools []Tool) (json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		obj := make(map[string]json.RawMessage)
		obj["name"] = mustMarshal(tool.Name)
		if tool.Description != "" {
			obj["description"] = mustMarshal(tool.Description)
		}
		if tool.Parameters != nil {
			obj["input_schema"] = tool.Parameters
		}
		if tool.Type != "" && tool.Type != "function" {
			obj["type"] = mustMarshal(tool.Type)
		}
		data, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		result = append(result, data)
	}
	return mustMarshal(result), nil
}

// serializeClaudeToolChoice serializes IR ToolChoice to Claude format.
func serializeClaudeToolChoice(tc *ToolChoice) (json.RawMessage, error) {
	if tc.Name == "" {
		return mustMarshal(tc.Type), nil
	}
	return json.Marshal(map[string]any{
		"type": tc.Type,
		"name": tc.Name,
	})
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
