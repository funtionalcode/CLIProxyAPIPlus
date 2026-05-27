package ir

// toolCallIDLimit is the maximum call_id length accepted by the OpenAI Responses API.
const toolCallIDLimit = 64

// NormalizeToolCallID ensures a tool call ID conforms to the 64-character limit.
// Unlike the previous SHA256-based approach, this uses deterministic truncation
// that preserves both prefix and suffix, making collisions extremely unlikely while
// enabling round-trip restoration via the side-channel map.
//
// The shortened form takes the first 32 characters and the last 16 characters of
// the original ID, joined by "_". This preserves the format prefix (usually at
// the start) and the unique suffix (usually at the end).
//
// When idMap is non-nil, the mapping from shortened to original ID is stored for
// later restoration.
func NormalizeToolCallID(id string, idMap map[string]string) string {
	if len(id) <= toolCallIDLimit {
		return id
	}

	// Deterministic truncation: first 32 + "_" + last 16 = 49 chars, well under limit.
	prefixLen := 32
	suffixLen := 16
	if prefixLen > len(id) {
		prefixLen = len(id)
	}
	if suffixLen > len(id) {
		suffixLen = len(id)
	}

	shortened := id[:prefixLen] + "_" + id[len(id)-suffixLen:]

	if idMap != nil {
		idMap[shortened] = id
	}
	return shortened
}

// RestoreToolCallID restores an original tool call ID from the side-channel map.
// If the map is nil or the shortened ID is not found, it returns the shortened ID as-is.
func RestoreToolCallID(shortened string, idMap map[string]string) string {
	if idMap == nil {
		return shortened
	}
	if original, ok := idMap[shortened]; ok {
		return original
	}
	return shortened
}
