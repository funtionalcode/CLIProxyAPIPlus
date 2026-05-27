package ir

import "encoding/json"

// BuildClaudeToolNameReverseMap extracts tool names from a raw Claude Messages API
// request and returns a map from shortened tool name to original tool name. This is
// used by response translators to restore original tool names in Codex→Claude
// response translation.
func BuildClaudeToolNameReverseMap(originalRequest []byte) map[string]string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(originalRequest, &obj); err != nil {
		return nil
	}

	rawTools, ok := obj["tools"]
	if !ok {
		return nil
	}

	var rawArr []json.RawMessage
	if err := json.Unmarshal(rawTools, &rawArr); err != nil {
		return nil
	}

	var names []string
	for _, rawTool := range rawArr {
		var tool struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(rawTool, &tool); err != nil || tool.Name == "" {
			continue
		}
		names = append(names, tool.Name)
	}

	if len(names) == 0 {
		return nil
	}

	nameMap := BuildShortNameMap(names)
	// BuildShortNameMap returns short→original, which is already the reverse map.
	return nameMap
}

// BuildClaudeToolCallIDMap extracts tool call IDs from a raw Claude Messages API
// request and returns a map from shortened ID to original ID. This is used by
// response translators to restore original call IDs in Codex→Claude response
// translation.
//
// It scans assistant messages for tool_use content blocks and user messages for
// tool_result content blocks, normalizing each ID via NormalizeToolCallID.
func BuildClaudeToolCallIDMap(originalRequest []byte) map[string]string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(originalRequest, &obj); err != nil {
		return nil
	}

	rawMsgs, ok := obj["messages"]
	if !ok {
		return nil
	}

	var rawArr []json.RawMessage
	if err := json.Unmarshal(rawMsgs, &rawArr); err != nil {
		return nil
	}

	idMap := make(map[string]string)
	for _, rawMsg := range rawArr {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			continue
		}

		// Parse content as array of blocks
		var rawBlocks []json.RawMessage
		if err := json.Unmarshal(msg.Content, &rawBlocks); err != nil {
			continue
		}

		for _, rawBlock := range rawBlocks {
			var block struct {
				Type       string `json:"type"`
				ID         string `json:"id"`
				ToolUseID  string `json:"tool_use_id"`
			}
			if err := json.Unmarshal(rawBlock, &block); err != nil {
				continue
			}

			switch block.Type {
			case "tool_use":
				if block.ID != "" {
					shortened := NormalizeToolCallID(block.ID, nil)
					idMap[shortened] = block.ID
				}
			case "tool_result":
				if block.ToolUseID != "" {
					shortened := NormalizeToolCallID(block.ToolUseID, nil)
					idMap[shortened] = block.ToolUseID
				}
			}
		}
	}

	if len(idMap) == 0 {
		return nil
	}
	return idMap
}
