package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

// ConvertClaudeRequestToCodexIR translates a Claude Messages API request to Codex
// (OpenAI Responses) format using the intermediate representation.
//
//nolint:unused // will be wired in once parity tests pass
func ConvertClaudeRequestToCodexIR(modelName string, inputRawJSON []byte, _ bool) []byte {
	irReq, err := ir.ParseClaudeRequest(inputRawJSON)
	if err != nil {
		return nil
	}

	if modelName != "" {
		irReq.Model = modelName
	}

	claudeToCodexTransform(irReq)

	out, err := serializeCodexFromIR(irReq)
	if err != nil {
		return nil
	}
	return out
}

// claudeToCodexTransform applies all format-specific transformations on the IR
// to convert from Claude semantics to Codex (OpenAI Responses) semantics.
func claudeToCodexTransform(r *ir.IRRequest) {
	nameMap := filterSystemAttribution(r)
	transformMessages(r)
	transformTools(r, nameMap)
	transformToolChoice(r, nameMap)
	shortenToolCallIDs(r)
	normalizeAllToolParams(r)
	addReasoningConfig(r)
}

// filterSystemAttribution removes Claude Code attribution boilerplate from system
// blocks and returns the tool name map built from pre-transform tool names.
func filterSystemAttribution(r *ir.IRRequest) map[string]string {
	if len(r.System) == 0 {
		return buildOriginalToolNameMap(r.Tools)
	}
	filtered := make([]ir.SystemBlock, 0, len(r.System))
	for _, sb := range r.System {
		if sb.Text != "" && util.IsClaudeCodeAttributionSystemText(sb.Text) {
			continue
		}
		filtered = append(filtered, sb)
	}
	r.System = filtered
	return buildOriginalToolNameMap(r.Tools)
}

// buildOriginalToolNameMap creates an original→short name map from the pre-transform
// tool list. Must be called before transformTools modifies the tool names.
func buildOriginalToolNameMap(tools []ir.Tool) map[string]string {
	var names []string
	for _, t := range tools {
		if t.Name != "" && !isWebSearchToolType(t.Type) {
			names = append(names, t.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}

	shortToOrig := ir.BuildShortNameMap(names)
	origToShort := make(map[string]string, len(shortToOrig))
	for short, orig := range shortToOrig {
		origToShort[orig] = short
	}
	return origToShort
}

// transformMessages converts Claude message content blocks to Codex format:
//   - thinking blocks with valid Fernet signatures → separate reasoning items
//   - tool_use blocks → extracted to Message.ToolCalls
//   - tool_result blocks → separate tool-role messages
//   - image source objects → data URLs
func transformMessages(r *ir.IRRequest) {
	if len(r.Messages) == 0 {
		return
	}

	var newMsgs []ir.Message

	for i := range r.Messages {
		msg := &r.Messages[i]

		// Extract thinking blocks and check for valid Fernet signatures.
		reasoningItems := extractReasoningItems(msg)

		// Extract tool_result blocks into separate messages.
		toolResultMsgs, _ := extractToolResults(msg)

		// Extract tool_use blocks into Message.ToolCalls.
		extractToolCalls(msg)

		// Transform image source objects to data URLs.
		transformImageURLs(msg)

		// Emit reasoning items first (must precede the message in Codex input).
		for _, ri := range reasoningItems {
			newMsgs = append(newMsgs, ri)
		}

		// Filter out empty assistant messages (e.g. thinking-only).
		if msg.Role == "assistant" && len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
			// Skip empty assistant messages.
		} else if len(msg.Content) > 0 || len(msg.ToolCalls) > 0 {
			newMsgs = append(newMsgs, *msg)
		}

		newMsgs = append(newMsgs, toolResultMsgs...)
	}

	r.Messages = newMsgs
}

// extractReasoningItems extracts thinking blocks with valid Fernet signatures into
// separate reasoning messages. Returns reasoning items and removes processed thinking
// blocks from the message content.
func extractReasoningItems(msg *ir.Message) []ir.Message {
	if msg.Role != "assistant" || len(msg.Content) == 0 {
		return nil
	}

	var items []ir.Message
	var remaining []ir.ContentBlock

	for _, cb := range msg.Content {
		if cb.Type != "thinking" {
			remaining = append(remaining, cb)
			continue
		}

		// Only convert thinking blocks with valid Fernet signatures.
		sig := extractSignature(cb)
		if !isFernetLikeReasoningSignature(sig) {
			// Not a valid Codex reasoning signature; drop silently.
			continue
		}

		// Create a reasoning item with encrypted_content in passthrough.
		item := ir.Message{Role: "assistant"}
		reasoningBlock := ir.ContentBlock{
			Type: "reasoning",
		}
		reasoningBlock.Passthrough = map[string]json.RawMessage{
			"encrypted_content": mustJSONMarshal(sig),
			"summary":           json.RawMessage(`[]`),
			"content":           json.RawMessage(`null`),
		}
		item.Content = []ir.ContentBlock{reasoningBlock}
		items = append(items, item)
	}

	msg.Content = remaining
	return items
}

// extractSignature extracts the signature string from a thinking content block.
func extractSignature(cb ir.ContentBlock) string {
	if cb.Passthrough != nil {
		if raw, ok := cb.Passthrough["signature"]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				return s
			}
		}
	}
	return ""
}

// extractToolResults converts tool_result content blocks into separate tool-role
// messages and returns the remaining content blocks.
func extractToolResults(msg *ir.Message) ([]ir.Message, []ir.ContentBlock) {
	if len(msg.Content) == 0 {
		return nil, msg.Content
	}

	var toolResultMsgs []ir.Message
	var remaining []ir.ContentBlock

	for _, cb := range msg.Content {
		if cb.Type != "tool_result" || cb.ToolResult == nil {
			remaining = append(remaining, cb)
			continue
		}

		output := buildToolResultOutput(cb.ToolResult)
		toolMsg := ir.Message{
			Role:       "tool",
			ToolCallID: cb.ToolResult.ToolCallID,
			Content:    []ir.ContentBlock{{Type: "text", Text: output}},
		}
		toolResultMsgs = append(toolResultMsgs, toolMsg)
	}

	return toolResultMsgs, remaining
}

// buildToolResultOutput constructs the output string for a function_call_output
// from a tool result. Handles both plain string content and multi-part content
// (including images).
func buildToolResultOutput(tr *ir.ToolResult) string {
	raw := tr.Content
	if raw == "" {
		return ""
	}

	// Try to parse as JSON array of content blocks.
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Source *struct {
			Data      string `json:"data"`
			Base64    string `json:"base64"`
			MediaType string `json:"media_type"`
			MimeType  string `json:"mime_type"`
		} `json:"source"`
	}
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		// Not a JSON array; treat as plain string.
		return raw
	}

	// Build structured output for multi-part content.
	var parts []map[string]any
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, map[string]any{"type": "input_text", "text": b.Text})
		case "image":
			if b.Source != nil {
				data := b.Source.Data
				if data == "" {
					data = b.Source.Base64
				}
				if data != "" {
					mediaType := b.Source.MediaType
					if mediaType == "" {
						mediaType = b.Source.MimeType
					}
					if mediaType == "" {
						mediaType = "application/octet-stream"
					}
					dataURL := fmt.Sprintf("data:%s;base64,%s", mediaType, data)
					parts = append(parts, map[string]any{"type": "input_image", "image_url": dataURL})
				}
			}
		}
	}

	if len(parts) > 0 {
		out, _ := json.Marshal(parts)
		return string(out)
	}
	return raw
}

// extractToolCalls moves tool_use content blocks to Message.ToolCalls.
func extractToolCalls(msg *ir.Message) {
	if len(msg.Content) == 0 {
		return
	}

	var remaining []ir.ContentBlock
	for _, cb := range msg.Content {
		if cb.Type == "tool_use" && cb.ToolUse != nil {
			msg.ToolCalls = append(msg.ToolCalls, *cb.ToolUse)
		} else {
			remaining = append(remaining, cb)
		}
	}
	msg.Content = remaining
}

// transformImageURLs converts Claude image source objects to data URLs.
func transformImageURLs(msg *ir.Message) {
	for i := range msg.Content {
		cb := &msg.Content[i]
		if cb.Type != "image" || cb.Passthrough == nil {
			continue
		}

		// Already has an image URL; skip.
		if cb.ImageURL != "" {
			continue
		}

		data := extractPassthroughString(cb.Passthrough, "data")
		if data == "" {
			data = extractPassthroughString(cb.Passthrough, "base64")
		}
		if data == "" {
			continue
		}

		mediaType := extractPassthroughString(cb.Passthrough, "media_type")
		if mediaType == "" {
			mediaType = extractPassthroughString(cb.Passthrough, "mime_type")
		}
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		cb.ImageURL = fmt.Sprintf("data:%s;base64,%s", mediaType, data)
		cb.Passthrough = nil
	}
}

// transformTools converts Claude tool definitions to Codex format:
//   - web_search_20250305/web_search_20260209 → web_search type
//   - regular tools → type:"function" with shortened names and normalized parameters
func transformTools(r *ir.IRRequest, nameMap map[string]string) {
	if len(r.Tools) == 0 {
		return
	}

	var newTools []ir.Tool

	for _, tool := range r.Tools {
		if isWebSearchToolType(tool.Type) {
			newTools = append(newTools, ir.Tool{
				Type: "web_search",
			})
			continue
		}

		name := tool.Name
		if short, ok := nameMap[name]; ok {
			name = short
		}

		newTool := ir.Tool{
			Name:        name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
			Type:        "function",
		}

		if name != tool.Name {
			newTool.OriginalName = tool.Name
		}

		newTools = append(newTools, newTool)
	}

	r.Tools = newTools
}

// transformToolChoice maps Claude tool_choice types to Codex types:
//   - "any" → "required"
//   - "tool" → "function" (with shortened name)
//   - "auto", "none" pass through unchanged
func transformToolChoice(r *ir.IRRequest, nameMap map[string]string) {
	if r.ToolChoice == nil {
		return
	}

	tc := r.ToolChoice
	switch tc.Type {
	case "any":
		tc.Type = "required"
	case "tool":
		tc.Type = "function"
		if tc.Name != "" {
			if short, ok := nameMap[tc.Name]; ok {
				tc.Name = short
			}
		}
	}
}

// shortenToolCallIDs truncates tool call IDs that exceed the 64-character limit
// using deterministic truncation (first 32 + "_" + last 16).
func shortenToolCallIDs(r *ir.IRRequest) {
	idMap := make(map[string]string)
	for i := range r.Messages {
		msg := &r.Messages[i]
		for j := range msg.ToolCalls {
			tc := &msg.ToolCalls[j]
			if tc.ID != "" {
				tc.ID = ir.NormalizeToolCallID(tc.ID, idMap)
			}
		}
		if msg.ToolCallID != "" {
			msg.ToolCallID = ir.NormalizeToolCallID(msg.ToolCallID, idMap)
		}
	}
}

// normalizeAllToolParams ensures all function tool parameters have type:"object"
// and an empty properties map if missing.
func normalizeAllToolParams(r *ir.IRRequest) {
	for i := range r.Tools {
		tool := &r.Tools[i]
		if tool.Type != "function" || tool.Parameters == nil {
			continue
		}
		tool.Parameters = normalizeToolParametersIR(tool.Parameters)
	}
}

// normalizeToolParametersIR ensures object schemas contain at least type:"object"
// and an empty properties map.
func normalizeToolParametersIR(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}

	var schema map[string]json.RawMessage
	if err := json.Unmarshal(raw, &schema); err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}

	if _, ok := schema["type"]; !ok {
		schema["type"] = json.RawMessage(`"object"`)
	}

	// Check if type is "object" and properties is missing.
	var schemaType string
	if t, ok := schema["type"]; ok {
		_ = json.Unmarshal(t, &schemaType)
	}
	if schemaType == "object" {
		if _, ok := schema["properties"]; !ok {
			schema["properties"] = json.RawMessage(`{}`)
		}
	}

	out, _ := json.Marshal(schema)
	return out
}

// addReasoningConfig translates Claude thinking configuration to Codex reasoning
// settings and adds them to the IR passthrough for serialization.
func addReasoningConfig(r *ir.IRRequest) {
	effort := computeReasoningEffort(r)

	if r.Passthrough == nil {
		r.Passthrough = make(map[string]json.RawMessage)
	}

	reasoning := map[string]any{
		"effort":  effort,
		"summary": "auto",
	}
	r.Passthrough["reasoning"] = mustJSONMarshal(reasoning)
	r.Passthrough["stream"] = json.RawMessage(`true`)
	r.Passthrough["store"] = json.RawMessage(`false`)
	r.Passthrough["include"] = json.RawMessage(`["reasoning.encrypted_content"]`)

	// parallel_tool_calls defaults to true unless tool_choice.disable_parallel_tool_use is set.
	parallelToolCalls := true
	if r.ToolChoice != nil && r.ToolChoice.DisableParallelToolUse != nil && *r.ToolChoice.DisableParallelToolUse {
		parallelToolCalls = false
	}
	r.Passthrough["parallel_tool_calls"] = mustJSONMarshal(parallelToolCalls)
}

// computeReasoningEffort derives the Codex reasoning effort level from the
// Claude thinking configuration.
func computeReasoningEffort(r *ir.IRRequest) string {
	if r.Thinking == nil {
		return "medium"
	}

	switch r.Thinking.Type {
	case "enabled":
		if r.Thinking.BudgetTokens != nil {
			if effort, ok := thinking.ConvertBudgetToLevel(int(*r.Thinking.BudgetTokens)); ok && effort != "" {
				return effort
			}
		}
		return "medium"
	case "adaptive", "auto":
		if r.Thinking.Effort != "" {
			return strings.ToLower(strings.TrimSpace(r.Thinking.Effort))
		}
		return string(thinking.LevelXHigh)
	case "disabled":
		if effort, ok := thinking.ConvertBudgetToLevel(0); ok && effort != "" {
			return effort
		}
		return "none"
	default:
		return "medium"
	}
}

// isWebSearchToolType checks if a tool type is a Claude web search tool.
func isWebSearchToolType(toolType string) bool {
	return toolType == "web_search_20250305" || toolType == "web_search_20260209"
}

// extractPassthroughString extracts a string value from a passthrough map.
func extractPassthroughString(pt map[string]json.RawMessage, key string) string {
	raw, ok := pt[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// mustJSONMarshal marshals a value to JSON, returning nil on error.
func mustJSONMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// serializeCodexFromIR serializes an IR request into Codex (OpenAI Responses) format
// using a custom serializer that handles Codex-specific semantics:
//   - System blocks → developer-role input items (not instructions)
//   - Reasoning content blocks → top-level reasoning items in input
//   - parallel_tool_calls included in output
func serializeCodexFromIR(r *ir.IRRequest) ([]byte, error) {
	obj := make(map[string]json.RawMessage)

	obj["model"] = mustJSONMarshal(r.Model)

	// Build input array from messages, prepending developer items for system blocks.
	var inputItems []json.RawMessage

	// System → single developer-role item with all system blocks as content.
	if len(r.System) > 0 {
		var sysContent []map[string]json.RawMessage
		for _, sb := range r.System {
			if sb.Text == "" {
				continue
			}
			sysContent = append(sysContent, map[string]json.RawMessage{
				"type": mustJSONMarshal("input_text"),
				"text": mustJSONMarshal(sb.Text),
			})
		}
		if len(sysContent) > 0 {
			item := map[string]json.RawMessage{
				"type":    mustJSONMarshal("message"),
				"role":    mustJSONMarshal("developer"),
				"content": mustJSONMarshal(sysContent),
			}
			inputItems = append(inputItems, mustJSONMarshal(item))
		}
	}

	// Messages → input items.
	for _, msg := range r.Messages {
		items, err := serializeCodexMessageInput(msg)
		if err != nil {
			return nil, err
		}
		inputItems = append(inputItems, items...)
	}

	if len(inputItems) > 0 {
		obj["input"] = mustJSONMarshal(inputItems)
	}

	// Tools.
	if len(r.Tools) > 0 {
		tools, err := serializeCodexTools(r.Tools)
		if err != nil {
			return nil, err
		}
		obj["tools"] = tools
	}

	// Tool choice.
	if r.ToolChoice != nil {
		tc := serializeCodexToolChoice(r.ToolChoice)
		obj["tool_choice"] = tc
	}

	if r.MaxTokens != nil {
		obj["max_output_tokens"] = mustJSONMarshal(*r.MaxTokens)
	}
	if r.Temperature != nil {
		obj["temperature"] = mustJSONMarshal(*r.Temperature)
	}
	if r.TopP != nil {
		obj["top_p"] = mustJSONMarshal(*r.TopP)
	}

	// Merge passthrough fields (reasoning, stream, store, include, parallel_tool_calls).
	for k, v := range r.Passthrough {
		if _, exists := obj[k]; !exists {
			obj[k] = v
		}
	}

	return json.Marshal(obj)
}

// serializeCodexMessageInput serializes a single IR message into Codex input items.
func serializeCodexMessageInput(msg ir.Message) ([]json.RawMessage, error) {
	var items []json.RawMessage

	// Tool calls → function_call items.
	for _, tc := range msg.ToolCalls {
		item := map[string]json.RawMessage{
			"type":      mustJSONMarshal("function_call"),
			"call_id":   mustJSONMarshal(tc.ID),
			"name":      mustJSONMarshal(tc.Name),
			"arguments": tc.Arguments,
		}
		items = append(items, mustJSONMarshal(item))
	}

	// Tool results → function_call_output items.
	if msg.Role == "tool" && msg.ToolCallID != "" {
		output := ""
		for _, cb := range msg.Content {
			if cb.Type == "text" {
				output += cb.Text
			}
		}
		item := map[string]json.RawMessage{
			"type":    mustJSONMarshal("function_call_output"),
			"call_id": mustJSONMarshal(msg.ToolCallID),
			"output":  mustJSONMarshal(output),
		}
		items = append(items, mustJSONMarshal(item))
		return items, nil
	}

	// Regular messages.
	if len(msg.Content) > 0 {
		// Check for reasoning content blocks and emit as top-level items.
		for _, cb := range msg.Content {
			if cb.Type == "reasoning" && cb.Passthrough != nil {
				item := map[string]json.RawMessage{
					"type": mustJSONMarshal("reasoning"),
				}
				for k, v := range cb.Passthrough {
					item[k] = v
				}
				items = append(items, mustJSONMarshal(item))
			}
		}

		// Non-reasoning content → message item.
		var contentBlocks []json.RawMessage
		for _, cb := range msg.Content {
			if cb.Type == "reasoning" {
				continue
			}
			block, err := serializeCodexContentBlock(cb, msg.Role)
			if err != nil {
				return nil, err
			}
			contentBlocks = append(contentBlocks, block)
		}

		if len(contentBlocks) > 0 {
			role := msg.Role
			if role == "tool" {
				role = "user"
			}
			item := map[string]json.RawMessage{
				"type":    mustJSONMarshal("message"),
				"role":    mustJSONMarshal(role),
				"content": mustJSONMarshal(contentBlocks),
			}
			items = append(items, mustJSONMarshal(item))
		}
	}

	return items, nil
}

// serializeCodexContentBlock serializes a ContentBlock to Codex format.
// The role parameter determines text block type: assistant → output_text, others → input_text.
func serializeCodexContentBlock(cb ir.ContentBlock, role string) (json.RawMessage, error) {
	obj := make(map[string]json.RawMessage)

	switch cb.Type {
	case "text":
		textType := "input_text"
		if role == "assistant" {
			textType = "output_text"
		}
		obj["type"] = mustJSONMarshal(textType)
		obj["text"] = mustJSONMarshal(cb.Text)

	case "image":
		obj["type"] = mustJSONMarshal("input_image")
		if cb.ImageURL != "" {
			obj["image_url"] = mustJSONMarshal(cb.ImageURL)
		}
		for k, v := range cb.Passthrough {
			obj[k] = v
		}

	default:
		obj["type"] = mustJSONMarshal(cb.Type)
		for k, v := range cb.Passthrough {
			obj[k] = v
		}
	}

	return json.Marshal(obj)
}

// serializeCodexTools serializes IR Tools to Codex format.
func serializeCodexTools(tools []ir.Tool) (json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "" && tool.Type != "function" {
			result = append(result, mustJSONMarshal(map[string]string{"type": tool.Type}))
			continue
		}
		obj := map[string]json.RawMessage{
			"type":       mustJSONMarshal("function"),
			"name":       mustJSONMarshal(tool.Name),
			"parameters": tool.Parameters,
		}
		if tool.Description != "" {
			obj["description"] = mustJSONMarshal(tool.Description)
		}
		result = append(result, mustJSONMarshal(obj))
	}
	return mustJSONMarshal(result), nil
}

// serializeCodexToolChoice serializes IR ToolChoice to Codex format.
func serializeCodexToolChoice(tc *ir.ToolChoice) json.RawMessage {
	if tc.Name == "" {
		return mustJSONMarshal(tc.Type)
	}
	return mustJSONMarshal(map[string]string{
		"type": tc.Type,
		"name": tc.Name,
	})
}
