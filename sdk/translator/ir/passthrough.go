package ir

import "encoding/json"

// StorePassthrough extracts all top-level fields from rawJSON that are NOT in the
// knownFields set and stores them in a Passthrough map. This allows unrecognized
// or format-specific fields to survive translation without interpretation.
//
// knownFields should contain the JSON field names that are explicitly mapped to
// IR struct fields (e.g. "model", "messages", "tools", "stream").
func StorePassthrough(rawJSON []byte, knownFields map[string]bool) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &obj); err != nil {
		return nil
	}

	result := make(map[string]json.RawMessage)
	for key, val := range obj {
		if !knownFields[key] {
			result[key] = val
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// MergePassthrough merges passthrough fields back into the target JSON. Fields
// in the passthrough map are added to the target JSON only if they do not already
// exist in it (explicit IR fields take precedence).
func MergePassthrough(target []byte, passthrough map[string]json.RawMessage) []byte {
	if len(passthrough) == 0 {
		return target
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(target, &obj); err != nil {
		return target
	}

	for key, val := range passthrough {
		if _, exists := obj[key]; !exists {
			obj[key] = val
		}
	}

	result, err := json.Marshal(obj)
	if err != nil {
		return target
	}
	return result
}

// KnownRequestFields returns the set of top-level JSON field names that are
// explicitly mapped to IRRequest struct fields. This is used by StorePassthrough
// to determine which fields to skip.
func KnownRequestFields() map[string]bool {
	return map[string]bool{
		"model":                 true,
		"system":                true,
		"messages":              true,
		"input":                 true, // OpenAI Responses / Codex
		"instructions":          true, // OpenAI Responses / Codex
		"tools":                 true,
		"tool_choice":           true,
		"max_tokens":            true,
		"max_output_tokens":     true,
		"max_completion_tokens": true,
		"temperature":           true,
		"top_p":                 true,
		"stop_sequences":        true,
		"stream":                true,
		"thinking":              true,
		"reasoning":             true, // Codex
	}
}
