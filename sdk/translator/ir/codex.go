package ir

import (
	"encoding/json"
	"fmt"
)

// ParseCodexRequest parses a Codex (OpenAI Responses) request into the IR.
// Codex uses the same wire format as OpenAI Responses API.
func ParseCodexRequest(rawJSON []byte) (*IRRequest, error) {
	// Codex format is identical to OpenAI Responses
	return ParseOpenAIResponsesRequest(rawJSON)
}

// SerializeCodexRequest serializes an IR request into Codex format.
// Codex uses the same wire format as OpenAI Responses API.
func SerializeCodexRequest(ir *IRRequest) ([]byte, error) {
	return SerializeOpenAIResponsesRequest(ir)
}

// ParseCodexResponse parses a Codex response into the IR.
func ParseCodexResponse(rawJSON []byte) (*IRResponse, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &obj); err != nil {
		return nil, fmt.Errorf("ir: parse codex response: %w", err)
	}

	resp := &IRResponse{}

	if v, ok := obj["id"]; ok {
		_ = json.Unmarshal(v, &resp.ID)
		delete(obj, "id")
	}
	if v, ok := obj["model"]; ok {
		_ = json.Unmarshal(v, &resp.Model)
		delete(obj, "model")
	}

	// output — array of output items
	if v, ok := obj["output"]; ok {
		content, toolCalls, err := parseCodexOutput(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse codex output: %w", err)
		}
		resp.Content = content
		resp.ToolCalls = toolCalls
		delete(obj, "output")
	}

	// usage
	if v, ok := obj["usage"]; ok {
		usage, err := parseCodexUsage(v)
		if err != nil {
			return nil, fmt.Errorf("ir: parse codex usage: %w", err)
		}
		resp.Usage = usage
		delete(obj, "usage")
	}

	// finish_reason — from stop_reason or status
	for _, key := range []string{"stop_reason", "status"} {
		if v, ok := obj[key]; ok {
			_ = json.Unmarshal(v, &resp.FinishReason)
			delete(obj, key)
			break
		}
	}

	// Store remaining fields as passthrough
	if len(obj) > 0 {
		resp.Passthrough = make(map[string]json.RawMessage, len(obj))
		for k, v := range obj {
			resp.Passthrough[k] = v
		}
	}

	return resp, nil
}

// SerializeCodexResponse serializes an IR response into Codex format.
func SerializeCodexResponse(ir *IRResponse) ([]byte, error) {
	obj := make(map[string]json.RawMessage)

	if ir.ID != "" {
		obj["id"] = mustMarshal(ir.ID)
	}
	if ir.Model != "" {
		obj["model"] = mustMarshal(ir.Model)
	}

	// output
	if len(ir.Content) > 0 || len(ir.ToolCalls) > 0 {
		output, err := serializeCodexOutput(ir.Content, ir.ToolCalls)
		if err != nil {
			return nil, err
		}
		obj["output"] = output
	}

	if ir.Usage != nil {
		obj["usage"] = mustMarshal(ir.Usage)
	}
	if ir.FinishReason != "" {
		obj["stop_reason"] = mustMarshal(ir.FinishReason)
	}

	// Merge passthrough
	for k, v := range ir.Passthrough {
		if _, exists := obj[k]; !exists {
			obj[k] = v
		}
	}

	return json.Marshal(obj)
}

// parseCodexOutput parses Codex output items into content blocks and tool calls.
func parseCodexOutput(raw json.RawMessage) ([]ContentBlock, []ToolCall, error) {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		return nil, nil, err
	}

	var content []ContentBlock
	var toolCalls []ToolCall

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
			if v, ok := obj["content"]; ok {
				blocks, err := parseCodexOutputContent(v)
				if err != nil {
					return nil, nil, err
				}
				content = append(content, blocks...)
			}

		case "function_call":
			tc := ToolCall{Type: "function"}
			if v, ok := obj["call_id"]; ok {
				_ = json.Unmarshal(v, &tc.ID)
			}
			if v, ok := obj["id"]; ok && tc.ID == "" {
				_ = json.Unmarshal(v, &tc.ID)
			}
			if v, ok := obj["name"]; ok {
				_ = json.Unmarshal(v, &tc.Name)
			}
			if v, ok := obj["arguments"]; ok {
				tc.Arguments = v
			}
			toolCalls = append(toolCalls, tc)

		case "reasoning":
			cb := ContentBlock{Type: "thinking"}
			cb.Passthrough = make(map[string]json.RawMessage)
			for k, v := range obj {
				if k != "type" {
					cb.Passthrough[k] = v
				}
			}
			content = append(content, cb)

		default:
			cb := ContentBlock{Type: itemType}
			cb.Passthrough = make(map[string]json.RawMessage)
			for k, v := range obj {
				if k != "type" {
					cb.Passthrough[k] = v
				}
			}
			content = append(content, cb)
		}
	}

	return content, toolCalls, nil
}

// parseCodexOutputContent parses content blocks from a Codex output message.
func parseCodexOutputContent(raw json.RawMessage) ([]ContentBlock, error) {
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
		case "output_text", "input_text":
			cb.Type = "text"
			if v, ok := obj["text"]; ok {
				_ = json.Unmarshal(v, &cb.Text)
			}

		case "summary_text":
			cb.Type = "text"
			if v, ok := obj["text"]; ok {
				_ = json.Unmarshal(v, &cb.Text)
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

// parseCodexUsage parses Codex usage into IR Usage.
func parseCodexUsage(raw json.RawMessage) (*Usage, error) {
	var obj struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return &Usage{
		PromptTokens:     obj.InputTokens,
		CompletionTokens: obj.OutputTokens,
		TotalTokens:      obj.TotalTokens,
	}, nil
}

// serializeCodexOutput serializes content blocks and tool calls to Codex output format.
func serializeCodexOutput(content []ContentBlock, toolCalls []ToolCall) (json.RawMessage, error) {
	items := make([]json.RawMessage, 0, len(content)+len(toolCalls))

	// Content blocks become message items
	if len(content) > 0 {
		blocks := make([]json.RawMessage, 0, len(content))
		for _, cb := range content {
			block, err := serializeCodexOutputContentBlock(cb)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
		item := map[string]json.RawMessage{
			"type":    mustMarshal("message"),
			"role":    mustMarshal("assistant"),
			"content": mustMarshal(blocks),
		}
		items = append(items, mustMarshal(item))
	}

	// Tool calls become function_call items
	for _, tc := range toolCalls {
		item := map[string]json.RawMessage{
			"type":      mustMarshal("function_call"),
			"call_id":   mustMarshal(tc.ID),
			"name":      mustMarshal(tc.Name),
			"arguments": tc.Arguments,
		}
		items = append(items, mustMarshal(item))
	}

	return mustMarshal(items), nil
}

// serializeCodexOutputContentBlock serializes a ContentBlock to Codex output format.
func serializeCodexOutputContentBlock(cb ContentBlock) (json.RawMessage, error) {
	obj := make(map[string]json.RawMessage)

	switch cb.Type {
	case "text":
		obj["type"] = mustMarshal("output_text")
		obj["text"] = mustMarshal(cb.Text)

	case "thinking":
		obj["type"] = mustMarshal("reasoning")
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
