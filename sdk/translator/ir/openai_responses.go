package ir

import (
	"encoding/json"
	"fmt"
)

// ParseOpenAIResponsesRequest parses an OpenAI Responses API request into the IR.
func ParseOpenAIResponsesRequest(rawJSON []byte) (*IRRequest, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &obj); err != nil {
		return nil, fmt.Errorf("ir: parse openai responses request: %w", err)
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

	// instructions — treat as system
	if v, ok := obj["instructions"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			ir.System = []SystemBlock{{Type: "text", Text: s}}
		}
		delete(obj, "instructions")
	}

	// input — string or array of items
	if v, ok := obj["input"]; ok {
		msgs, err := parseOpenAIResponsesInput(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse openai responses input: %w", err)
		}
		ir.Messages = msgs
		delete(obj, "input")
	}

	// tools
	if v, ok := obj["tools"]; ok {
		tools, err := parseOpenAIResponsesTools(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse openai responses tools: %w", err)
		}
		ir.Tools = tools
		delete(obj, "tools")
	}

	// tool_choice
	if v, ok := obj["tool_choice"]; ok {
		tc, err := parseOpenAIResponsesToolChoice(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse openai responses tool_choice: %w", err)
		}
		ir.ToolChoice = tc
		delete(obj, "tool_choice")
	}

	// max_output_tokens / max_completion_tokens
	for _, key := range []string{"max_output_tokens", "max_completion_tokens"} {
		if v, ok := obj[key]; ok {
			var mt int64
			_ = json.Unmarshal(v, &mt)
			ir.MaxTokens = &mt
			delete(obj, key)
			break
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

	// reasoning — map to thinking
	if v, ok := obj["reasoning"]; ok {
		var reasoning struct {
			Effort  string `json:"effort"`
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal(v, &reasoning); err == nil {
			ir.Thinking = &ThinkingConfig{
				Type:   "enabled",
				Effort: reasoning.Effort,
			}
		}
		delete(obj, "reasoning")
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

// SerializeOpenAIResponsesRequest serializes an IR request into OpenAI Responses API format.
func SerializeOpenAIResponsesRequest(ir *IRRequest) ([]byte, error) {
	obj := make(map[string]json.RawMessage)

	obj["model"] = mustMarshal(ir.Model)
	obj["stream"] = mustMarshal(ir.Stream)

	// instructions
	if len(ir.System) > 0 {
		text := ""
		for _, sb := range ir.System {
			if sb.Text != "" {
				if text != "" {
					text += "\n"
				}
				text += sb.Text
			}
		}
		if text != "" {
			obj["instructions"] = mustMarshal(text)
		}
	}

	// input — build from messages
	if len(ir.Messages) > 0 {
		items, err := serializeOpenAIResponsesInput(ir.Messages)
		if err != nil {
			return nil, err
		}
		obj["input"] = items
	}

	// tools
	if len(ir.Tools) > 0 {
		tools, err := serializeOpenAIResponsesTools(ir.Tools)
		if err != nil {
			return nil, err
		}
		obj["tools"] = tools
	}

	// tool_choice
	if ir.ToolChoice != nil {
		tc, err := serializeOpenAIResponsesToolChoice(ir.ToolChoice)
		if err != nil {
			return nil, err
		}
		obj["tool_choice"] = tc
	}

	if ir.MaxTokens != nil {
		obj["max_output_tokens"] = mustMarshal(*ir.MaxTokens)
	}
	if ir.Temperature != nil {
		obj["temperature"] = mustMarshal(*ir.Temperature)
	}
	if ir.TopP != nil {
		obj["top_p"] = mustMarshal(*ir.TopP)
	}
	if ir.Thinking != nil {
		reasoning := map[string]any{"effort": ir.Thinking.Effort}
		obj["reasoning"] = mustMarshal(reasoning)
	}

	// Merge passthrough fields
	for k, v := range ir.Passthrough {
		if _, exists := obj[k]; !exists {
			obj[k] = v
		}
	}

	return json.Marshal(obj)
}

// parseOpenAIResponsesInput parses OpenAI Responses input (string or array of items).
func parseOpenAIResponsesInput(raw json.RawMessage) ([]Message, error) {
	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: s}},
		}}, nil
	}

	// Try as array
	var rawItems []json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		return nil, err
	}

	msgs := make([]Message, 0, len(rawItems))
	for _, rawItem := range rawItems {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &obj); err != nil {
			continue
		}

		var itemType string
		if v, ok := obj["type"]; ok {
			_ = json.Unmarshal(v, &itemType)
		}

		switch itemType {
		case "message":
			msg := Message{}
			if v, ok := obj["role"]; ok {
				_ = json.Unmarshal(v, &msg.Role)
			}
			if v, ok := obj["content"]; ok {
				blocks, err := parseOpenAIResponsesContent(v)
				if err != nil {
					return nil, err
				}
				msg.Content = blocks
			}
			msgs = append(msgs, msg)

		case "function_call":
			msg := Message{Role: "assistant"}
			tc := ToolCall{Type: "function"}
			if v, ok := obj["id"]; ok {
				_ = json.Unmarshal(v, &tc.ID)
			}
			if v, ok := obj["call_id"]; ok {
				_ = json.Unmarshal(v, &tc.ID)
			}
			if v, ok := obj["name"]; ok {
				_ = json.Unmarshal(v, &tc.Name)
			}
			if v, ok := obj["arguments"]; ok {
				tc.Arguments = v
			}
			msg.ToolCalls = []ToolCall{tc}
			msgs = append(msgs, msg)

		case "function_call_output":
			msg := Message{Role: "tool"}
			if v, ok := obj["call_id"]; ok {
				_ = json.Unmarshal(v, &msg.ToolCallID)
			}
			if v, ok := obj["output"]; ok {
				var output string
				if err := json.Unmarshal(v, &output); err == nil {
					msg.Content = []ContentBlock{{Type: "text", Text: output}}
				}
			}
			msgs = append(msgs, msg)

		case "reasoning":
			// Store reasoning as a passthrough message
			msg := Message{Role: "assistant"}
			cb := ContentBlock{Type: "thinking"}
			cb.Passthrough = make(map[string]json.RawMessage)
			for k, v := range obj {
				if k != "type" {
					cb.Passthrough[k] = v
				}
			}
			msg.Content = []ContentBlock{cb}
			msgs = append(msgs, msg)

		default:
			// Preserve unknown item types as passthrough
			msg := Message{Role: "assistant"}
			cb := ContentBlock{Type: itemType}
			cb.Passthrough = make(map[string]json.RawMessage)
			for k, v := range obj {
				if k != "type" {
					cb.Passthrough[k] = v
				}
			}
			msg.Content = []ContentBlock{cb}
			msgs = append(msgs, msg)
		}
	}
	return msgs, nil
}

// parseOpenAIResponsesContent parses OpenAI Responses content array.
func parseOpenAIResponsesContent(raw json.RawMessage) ([]ContentBlock, error) {
	// Try as string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []ContentBlock{{Type: "text", Text: s}}, nil
	}

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
		case "input_text", "output_text":
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

// parseOpenAIResponsesTools parses OpenAI Responses tool definitions.
func parseOpenAIResponsesTools(raw json.RawMessage) ([]Tool, error) {
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

		if toolType == "function" {
			tool := Tool{Type: "function"}
			if v, ok := obj["name"]; ok {
				_ = json.Unmarshal(v, &tool.Name)
			}
			if v, ok := obj["description"]; ok {
				_ = json.Unmarshal(v, &tool.Description)
			}
			if v, ok := obj["parameters"]; ok {
				tool.Parameters = v
			}
			tools = append(tools, tool)
		} else {
			// Non-function tools (web_search, etc.)
			tool := Tool{Type: toolType}
			if v, ok := obj["name"]; ok {
				_ = json.Unmarshal(v, &tool.Name)
			}
			tools = append(tools, tool)
		}
	}
	return tools, nil
}

// parseOpenAIResponsesToolChoice parses OpenAI Responses tool_choice.
func parseOpenAIResponsesToolChoice(raw json.RawMessage) (*ToolChoice, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return &ToolChoice{Type: s}, nil
	}

	var obj struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return &ToolChoice{Type: obj.Type, Name: obj.Name}, nil
}

// serializeOpenAIResponsesInput serializes IR Messages to OpenAI Responses input array.
func serializeOpenAIResponsesInput(msgs []Message) (json.RawMessage, error) {
	items := make([]json.RawMessage, 0, len(msgs))

	for _, msg := range msgs {
		// Tool calls become function_call items
		for _, tc := range msg.ToolCalls {
			item := map[string]json.RawMessage{
				"type":      mustMarshal("function_call"),
				"call_id":   mustMarshal(tc.ID),
				"name":      mustMarshal(tc.Name),
				"arguments": tc.Arguments,
			}
			items = append(items, mustMarshal(item))
		}

		// Tool results become function_call_output items
		if msg.Role == "tool" && msg.ToolCallID != "" {
			output := ""
			for _, cb := range msg.Content {
				if cb.Type == "text" {
					output += cb.Text
				}
			}
			item := map[string]json.RawMessage{
				"type":    mustMarshal("function_call_output"),
				"call_id": mustMarshal(msg.ToolCallID),
				"output":  mustMarshal(output),
			}
			items = append(items, mustMarshal(item))
			continue
		}

		// Regular messages
		if len(msg.Content) > 0 {
			// Check for thinking content blocks
			hasThinking := false
			for _, cb := range msg.Content {
				if cb.Type == "thinking" {
					hasThinking = true
					item := map[string]json.RawMessage{
						"type": mustMarshal("reasoning"),
					}
					for k, v := range cb.Passthrough {
						item[k] = v
					}
					items = append(items, mustMarshal(item))
				}
			}

			// Non-thinking content
			var contentBlocks []json.RawMessage
			for _, cb := range msg.Content {
				if cb.Type == "thinking" {
					continue
				}
				block, err := serializeOpenAIResponsesContentBlock(cb)
				if err != nil {
					return nil, err
				}
				contentBlocks = append(contentBlocks, block)
			}

			if len(contentBlocks) > 0 {
				item := map[string]json.RawMessage{
					"type":    mustMarshal("message"),
					"role":    mustMarshal(msg.Role),
					"content": mustMarshal(contentBlocks),
				}
				items = append(items, mustMarshal(item))
			} else if hasThinking {
				// Thinking-only messages already emitted
			}
		}
	}

	return mustMarshal(items), nil
}

// serializeOpenAIResponsesContentBlock serializes a ContentBlock to OpenAI Responses format.
func serializeOpenAIResponsesContentBlock(cb ContentBlock) (json.RawMessage, error) {
	obj := make(map[string]json.RawMessage)

	switch cb.Type {
	case "text":
		obj["type"] = mustMarshal("input_text")
		obj["text"] = mustMarshal(cb.Text)

	case "image":
		obj["type"] = mustMarshal("input_image")
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

// serializeOpenAIResponsesTools serializes IR Tools to OpenAI Responses format.
func serializeOpenAIResponsesTools(tools []Tool) (json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "" && tool.Type != "function" {
			result = append(result, mustMarshal(map[string]string{"type": tool.Type}))
			continue
		}
		obj := map[string]json.RawMessage{
			"type":       mustMarshal("function"),
			"name":       mustMarshal(tool.Name),
			"parameters": tool.Parameters,
		}
		if tool.Description != "" {
			obj["description"] = mustMarshal(tool.Description)
		}
		result = append(result, mustMarshal(obj))
	}
	return mustMarshal(result), nil
}

// serializeOpenAIResponsesToolChoice serializes IR ToolChoice to OpenAI Responses format.
func serializeOpenAIResponsesToolChoice(tc *ToolChoice) (json.RawMessage, error) {
	if tc.Name == "" {
		return mustMarshal(tc.Type), nil
	}
	return json.Marshal(map[string]string{
		"type": tc.Type,
		"name": tc.Name,
	})
}
