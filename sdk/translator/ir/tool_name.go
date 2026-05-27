package ir

import "strings"

// toolNameLimit is the maximum tool name length accepted by the OpenAI Responses API.
const toolNameLimit = 64

// ShortenToolName truncates a tool name to 64 characters using the mcp__ prefix
// heuristic. It returns the shortened name and the original name. If no shortening
// is needed, original is empty.
//
// For MCP tools (prefix "mcp__"), the algorithm preserves the prefix and the last
// "__"-segment (the actual tool name), truncating the middle server name portion.
// For regular tools, it truncates to the limit.
func ShortenToolName(name string) (short, original string) {
	if len(name) <= toolNameLimit {
		return name, ""
	}

	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			cand := "mcp__" + name[idx+2:]
			if len(cand) > toolNameLimit {
				cand = cand[:toolNameLimit]
			}
			return cand, name
		}
	}

	return name[:toolNameLimit], name
}

// BuildShortNameMap ensures uniqueness of shortened tool names within a request.
// It returns a map from shortened name to original name. When multiple original
// names produce the same shortened form, numeric suffixes (_1, _2, ...) are appended.
func BuildShortNameMap(names []string) map[string]string {
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		short, _ := ShortenToolName(n)
		return short
	}

	for _, name := range names {
		cand := baseCandidate(name)
		if _, exists := m[cand]; !exists {
			if _, taken := used[cand]; !taken {
				m[cand] = name
				used[cand] = struct{}{}
				continue
			}
		}

		// Collision: append numeric suffix.
		for i := 1; ; i++ {
			suffix := "_" + itoa(i)
			base := cand
			if len(base)+len(suffix) > toolNameLimit {
				base = base[:toolNameLimit-len(suffix)]
			}
			candidate := base + suffix
			if _, taken := used[candidate]; !taken {
				m[candidate] = name
				used[candidate] = struct{}{}
				break
			}
		}
	}

	return m
}

// RestoreToolName looks up a shortened tool name in the map and returns the
// original name. If not found, it returns the shortened name as-is.
func RestoreToolName(shortened string, originalMap map[string]string) string {
	if originalMap == nil {
		return shortened
	}
	if orig, ok := originalMap[shortened]; ok {
		return orig
	}
	return shortened
}

// itoa converts a positive integer to its decimal string representation.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
