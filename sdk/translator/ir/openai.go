package ir

import (
	"encoding/json"
	"fmt"
)

// ParseOpenAIChatRequest parses an OpenAI Chat Completions request into the IR.
func ParseOpenAIChatRequest(rawJSON []byte) (*IRRequest, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &obj); err != nil {
		return nil, fmt.Errorf("ir: parse openai chat request: %w", err)
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

	// messages — extract system, parse rest
	if v, ok := obj["messages"]; ok {
		system, msgs, err := parseOpenAIChatMessages(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse openai chat messages: %w", err)
		}
		ir.System = system
		ir.Messages = msgs
		delete(obj, "messages")
	}

	// tools
	if v, ok := obj["tools"]; ok {
		tools, err := parseOpenAIChatTools(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse openai chat tools: %w", err)
		}
		ir.Tools = tools
		delete(obj, "tools")
	}

	// tool_choice
	if v, ok := obj["tool_choice"]; ok {
		tc, err := parseOpenAIChatToolChoice(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse openai chat tool_choice: %w", err)
		}
		ir.ToolChoice = tc
		delete(obj, "tool_choice")
	}

	// max_tokens / max_completion_tokens
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if v, ok := obj[key]; ok {
			var mt int64
			_ = json.Unmarshal(v, &mt)
			ir.MaxTokens = &mt
			delete(obj, key)
			break // only one
		}
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

	// stop
	if v, ok := obj["stop"]; ok {
		_ = json.Unmarshal(v, &ir.StopSequences)
		delete(obj, "stop")
	}

	// reasoning_effort — map to thinking
	if v, ok := obj["reasoning_effort"]; ok {
		var effort string
		_ = json.Unmarshal(v, &effort)
		ir.Thinking = &ThinkingConfig{Type: "enabled", Effort: effort}
		delete(obj, "reasoning_effort")
	}

	// Store remaining fields as passthrough (response_format, user, n, etc.)
	if len(obj) > 0 {
		ir.Passthrough = make(map[string]json.RawMessage, len(obj))
		for k, v := range obj {
			ir.Passthrough[k] = v
		}
	}

	return ir, nil
}

// SerializeOpenAIChatRequest serializes an IR request into OpenAI Chat Completions format.
func SerializeOpenAIChatRequest(ir *IRRequest) ([]byte, error) {
	obj := make(map[string]json.RawMessage)

	obj["model"] = mustMarshal(ir.Model)
	obj["stream"] = mustMarshal(ir.Stream)

	// messages — merge system into messages array
	msgs := make([]json.RawMessage, 0, len(ir.System)+len(ir.Messages))

	// System blocks become system role messages
	for _, sb := range ir.System {
		msg := map[string]json.RawMessage{
			"role":    mustMarshal("system"),
			"content": mustMarshal(sb.Text),
		}
		msgs = append(msgs, mustMarshal(msg))
	}

	// Regular messages
	for _, msg := range ir.Messages {
		m, err := serializeOpenAIChatMessage(msg)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}

	if len(msgs) > 0 {
		obj["messages"] = mustMarshal(msgs)
	}

	// tools
	if len(ir.Tools) > 0 {
		tools, err := serializeOpenAIChatTools(ir.Tools)
		if err != nil {
			return nil, err
		}
		obj["tools"] = tools
	}

	// tool_choice
	if ir.ToolChoice != nil {
		tc, err := serializeOpenAIChatToolChoice(ir.ToolChoice)
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
		obj["stop"] = mustMarshal(ir.StopSequences)
	}
	if ir.Thinking != nil && ir.Thinking.Effort != "" {
		obj["reasoning_effort"] = mustMarshal(ir.Thinking.Effort)
	}

	// Merge passthrough fields
	for k, v := range ir.Passthrough {
		if _, exists := obj[k]; !exists {
			obj[k] = v
		}
	}

	return json.Marshal(obj)
}

// parseOpenAIChatMessages parses OpenAI Chat messages, extracting system messages separately.
func parseOpenAIChatMessages(raw json.RawMessage) ([]SystemBlock, []Message, error) {
	var rawMsgs []json.RawMessage
	if err := json.Unmarshal(raw, &rawMsgs); err != nil {
		return nil, nil, err
	}

	var system []SystemBlock
	var msgs []Message

	for _, rawMsg := range rawMsgs {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rawMsg, &obj); err != nil {
			continue
		}

		var role string
		if v, ok := obj["role"]; ok {
			_ = json.Unmarshal(v, &role)
		}

		if role == "system" || role == "developer" {
			var text string
			if v, ok := obj["content"]; ok {
				_ = json.Unmarshal(v, &text)
			}
			system = append(system, SystemBlock{Type: "text", Text: text})
			continue
		}

		msg := Message{Role: role}
		if v, ok := obj["name"]; ok {
			_ = json.Unmarshal(v, &msg.Name)
		}

		// content — string or array
		if v, ok := obj["content"]; ok {
			blocks, err := parseOpenAIChatContent(v, role)
			if err != nil {
				return nil, nil, err
			}
			msg.Content = blocks
		}

		// tool_calls (assistant messages)
		if v, ok := obj["tool_calls"]; ok {
			tcs, err := parseOpenAIChatToolCalls(v)
			if err != nil {
				return nil, nil, err
			}
			msg.ToolCalls = tcs
		}

		// tool_call_id (tool result messages)
		if v, ok := obj["tool_call_id"]; ok {
			_ = json.Unmarshal(v, &msg.ToolCallID)
		}

		msgs = append(msgs, msg)
	}
	return system, msgs, nil
}

// parseOpenAIChatContent parses OpenAI Chat content (string or array).
func parseOpenAIChatContent(raw json.RawMessage, role string) ([]ContentBlock, error) {
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

		case "image_url":
			cb.Type = "image"
			cb.Passthrough = make(map[string]json.RawMessage)
			for k, v := range obj {
				if k != "type" {
					cb.Passthrough[k] = v
				}
			}

		case "input_text":
			cb.Type = "text"
			if v, ok := obj["text"]; ok {
				_ = json.Unmarshal(v, &cb.Text)
			}

		case "input_image":
			cb.Type = "image"
			cb.Passthrough = make(map[string]json.RawMessage)
			for k, v := range obj {
				if k != "type" {
					cb.Passthrough[k] = v
				}
			}

		default:
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

// parseOpenAIChatToolCalls parses OpenAI Chat tool_calls array.
func parseOpenAIChatToolCalls(raw json.RawMessage) ([]ToolCall, error) {
	var rawTCs []json.RawMessage
	if err := json.Unmarshal(raw, &rawTCs); err != nil {
		return nil, err
	}

	tcs := make([]ToolCall, 0, len(rawTCs))
	for _, rawTC := range rawTCs {
		var obj struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		}
		if err := json.Unmarshal(rawTC, &obj); err != nil {
			continue
		}
		tcs = append(tcs, ToolCall{
			ID:        obj.ID,
			Type:      obj.Type,
			Name:      obj.Function.Name,
			Arguments: obj.Function.Arguments,
		})
	}
	return tcs, nil
}

// parseOpenAIChatTools parses OpenAI Chat tool definitions.
func parseOpenAIChatTools(raw json.RawMessage) ([]Tool, error) {
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

		var toolType string
		if v, ok := obj["type"]; ok {
			_ = json.Unmarshal(v, &toolType)
		}

		// Function tools have nested "function" object
		if toolType == "function" {
			if v, ok := obj["function"]; ok {
				var fn struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
					Strict      *bool           `json:"strict,omitempty"`
				}
				if err := json.Unmarshal(v, &fn); err == nil {
					tools = append(tools, Tool{
						Name:        fn.Name,
						Description: fn.Description,
						Parameters:  fn.Parameters,
						Type:        "function",
					})
				}
			}
		} else {
			// Non-function tools (e.g. web_search) — preserve as passthrough
			tools = append(tools, Tool{Type: toolType})
		}
	}
	return tools, nil
}

// parseOpenAIChatToolChoice parses OpenAI Chat tool_choice.
func parseOpenAIChatToolChoice(raw json.RawMessage) (*ToolChoice, error) {
	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return &ToolChoice{Type: s}, nil
	}

	// Try as object: {"type":"function","function":{"name":"..."}}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return &ToolChoice{Type: obj.Type, Name: obj.Function.Name}, nil
}

// serializeOpenAIChatMessage serializes an IR Message to OpenAI Chat format.
func serializeOpenAIChatMessage(msg Message) (json.RawMessage, error) {
	obj := make(map[string]json.RawMessage)
	obj["role"] = mustMarshal(msg.Role)
	if msg.Name != "" {
		obj["name"] = mustMarshal(msg.Name)
	}

	// Content blocks
	if len(msg.Content) > 0 {
		blocks := make([]json.RawMessage, 0, len(msg.Content))
		for _, cb := range msg.Content {
			block, err := serializeOpenAIChatContentBlock(cb, msg.Role)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
		if len(blocks) == 1 && msg.Content[0].Type == "text" && msg.ToolCalls == nil {
			// Single text block — serialize as string
			obj["content"] = mustMarshal(msg.Content[0].Text)
		} else {
			obj["content"] = mustMarshal(blocks)
		}
	}

	// tool_calls
	if len(msg.ToolCalls) > 0 {
		tcs := make([]json.RawMessage, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			tcObj := map[string]json.RawMessage{
				"id":   mustMarshal(tc.ID),
				"type": mustMarshal(tc.Type),
				"function": mustMarshal(map[string]json.RawMessage{
					"name":      mustMarshal(tc.Name),
					"arguments": tc.Arguments,
				}),
			}
			tcs = append(tcs, mustMarshal(tcObj))
		}
		obj["tool_calls"] = mustMarshal(tcs)
	}

	// tool_call_id
	if msg.ToolCallID != "" {
		obj["tool_call_id"] = mustMarshal(msg.ToolCallID)
	}

	return json.Marshal(obj)
}

// serializeOpenAIChatContentBlock serializes a ContentBlock to OpenAI Chat format.
func serializeOpenAIChatContentBlock(cb ContentBlock, role string) (json.RawMessage, error) {
	obj := make(map[string]json.RawMessage)

	switch cb.Type {
	case "text":
		obj["type"] = mustMarshal("text")
		obj["text"] = mustMarshal(cb.Text)

	case "image":
		obj["type"] = mustMarshal("image_url")
		for k, v := range cb.Passthrough {
			obj[k] = v
		}

	default:
		obj["type"] = mustMarshal(cb.Type)
		for k, v := range cb.Passthrough {
			obj[k] = v
		}
	}

	return json.Marshal(obj)
}

// serializeOpenAIChatTools serializes IR Tools to OpenAI Chat format.
func serializeOpenAIChatTools(tools []Tool) (json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "" && tool.Type != "function" {
			// Non-function tools passthrough
			result = append(result, mustMarshal(map[string]string{"type": tool.Type}))
			continue
		}
		fn := map[string]json.RawMessage{
			"name":       mustMarshal(tool.Name),
			"parameters": tool.Parameters,
		}
		if tool.Description != "" {
			fn["description"] = mustMarshal(tool.Description)
		}
		obj := map[string]json.RawMessage{
			"type":     mustMarshal("function"),
			"function": mustMarshal(fn),
		}
		result = append(result, mustMarshal(obj))
	}
	return mustMarshal(result), nil
}

// serializeOpenAIChatToolChoice serializes IR ToolChoice to OpenAI Chat format.
func serializeOpenAIChatToolChoice(tc *ToolChoice) (json.RawMessage, error) {
	if tc.Name == "" {
		return mustMarshal(tc.Type), nil
	}
	return json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]string{
			"name": tc.Name,
		},
	})
}
