package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	formattedMessageMaxLength = 256 * 1024
	formattedSectionMaxLength = 512 * 1024
)

type requestLogSection struct {
	Name string
	Body string
}

type formattedMessage struct {
	Source string
	Role   string
	Text   string
}

// formatRequestLog converts a raw request log into a readable text transcript.
func formatRequestLog(raw string, originalName string) string {
	sections := splitRequestLogSections(raw)
	messages := collectFormattedMessages(sections)

	var builder strings.Builder
	builder.WriteString("CLIProxyAPI formatted request log\n")
	if strings.TrimSpace(originalName) != "" {
		builder.WriteString("Original file: ")
		builder.WriteString(originalName)
		builder.WriteString("\n")
	}
	builder.WriteString("Generated at: ")
	builder.WriteString(time.Now().Format(time.RFC3339Nano))
	builder.WriteString("\n\n")

	appendRequestSummary(&builder, sections)
	appendConversation(&builder, messages)
	appendErrorSections(&builder, sections)
	appendFallbackSections(&builder, sections, len(messages) > 0)

	return strings.TrimRight(builder.String(), "\n") + "\n"
}

func splitRequestLogSections(raw string) []requestLogSection {
	sections := make([]requestLogSection, 0)
	currentName := ""
	var currentBody strings.Builder
	flush := func() {
		if currentName == "" {
			return
		}
		sections = append(sections, requestLogSection{
			Name: currentName,
			Body: strings.TrimRight(currentBody.String(), "\n"),
		})
		currentBody.Reset()
	}

	for _, line := range strings.SplitAfter(raw, "\n") {
		trimmedLine := strings.TrimSuffix(line, "\n")
		trimmedLine = strings.TrimSuffix(trimmedLine, "\r")
		if isRequestLogSectionHeader(trimmedLine) {
			flush()
			currentName = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(trimmedLine), "=== "), " ===")
			continue
		}
		if currentName == "" {
			continue
		}
		currentBody.WriteString(line)
	}
	flush()
	return sections
}

func isRequestLogSectionHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "=== ") && strings.HasSuffix(trimmed, " ===")
}

func collectFormattedMessages(sections []requestLogSection) []formattedMessage {
	messages := make([]formattedMessage, 0)
	for _, section := range sections {
		source := strings.ToLower(section.Name)
		switch section.Name {
		case "REQUEST BODY", "API REQUEST", "API RESPONSE", "RESPONSE":
			messages = append(messages, extractMessagesFromPayload(section.Body, source)...)
		case "WEBSOCKET TIMELINE", "API WEBSOCKET TIMELINE":
			messages = append(messages, extractMessagesFromWebsocketTimeline(section.Body, source)...)
		}
	}
	return mergeAdjacentDeltas(messages)
}

func extractMessagesFromPayload(raw string, source string) []formattedMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.Contains(trimmed, "\ndata:") || strings.HasPrefix(trimmed, "data:") {
		return extractMessagesFromSSE(trimmed, source)
	}
	value, ok := parseJSONValue(trimmed)
	if !ok {
		return nil
	}
	return extractMessagesFromJSON(value, source)
}

func parseJSONValue(raw string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

func extractMessagesFromJSON(value any, source string) []formattedMessage {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	messages := make([]formattedMessage, 0)
	if systemText := textFromValue(object["system"]); systemText != "" {
		messages = append(messages, formattedMessage{Source: source, Role: "system", Text: systemText})
	}
	if inputMessages := extractInputMessages(object["input"], source); len(inputMessages) > 0 {
		messages = append(messages, inputMessages...)
	}
	if chatMessages := extractRoleMessages(object["messages"], source); len(chatMessages) > 0 {
		messages = append(messages, chatMessages...)
	}
	if outputMessages := extractOutputMessages(object["output"], source); len(outputMessages) > 0 {
		messages = append(messages, outputMessages...)
	}
	if outputText := textFromValue(object["output_text"]); outputText != "" {
		messages = append(messages, formattedMessage{Source: source, Role: "assistant", Text: outputText})
	}
	if choices := extractChoices(object["choices"], source); len(choices) > 0 {
		messages = append(messages, choices...)
	}
	if delta := textFromValue(object["delta"]); delta != "" {
		messages = append(messages, formattedMessage{Source: source, Role: "assistant", Text: delta})
	}
	if response, ok := object["response"].(map[string]any); ok {
		messages = append(messages, extractMessagesFromJSON(response, source)...)
	}
	return messages
}

func extractInputMessages(value any, source string) []formattedMessage {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []formattedMessage{{Source: source, Role: "user", Text: typed}}
	case []any:
		return extractRoleMessages(typed, source)
	default:
		return nil
	}
}

func extractRoleMessages(value any, source string) []formattedMessage {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	messages := make([]formattedMessage, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := stringField(object, "role")
		if role == "" {
			role = stringField(object, "type")
		}
		text := textFromValue(object["content"])
		if text == "" {
			text = textFromValue(object["text"])
		}
		if text == "" {
			text = textFromValue(object["message"])
		}
		if text == "" {
			continue
		}
		if role == "" {
			role = "message"
		}
		messages = append(messages, formattedMessage{Source: source, Role: role, Text: text})
	}
	return messages
}

func extractOutputMessages(value any, source string) []formattedMessage {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	messages := make([]formattedMessage, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := stringField(object, "role")
		if role == "" && stringField(object, "type") == "message" {
			role = "assistant"
		}
		text := textFromValue(object["content"])
		if text == "" {
			text = textFromValue(object["text"])
		}
		if text == "" {
			continue
		}
		if role == "" {
			role = "assistant"
		}
		messages = append(messages, formattedMessage{Source: source, Role: role, Text: text})
	}
	return messages
}

func extractChoices(value any, source string) []formattedMessage {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	messages := make([]formattedMessage, 0, len(items))
	for _, item := range items {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"message", "delta"} {
			object, ok := choice[key].(map[string]any)
			if !ok {
				continue
			}
			text := textFromValue(object["content"])
			if text == "" {
				text = textFromValue(object["text"])
			}
			if text == "" {
				continue
			}
			role := stringField(object, "role")
			if role == "" {
				role = "assistant"
			}
			messages = append(messages, formattedMessage{Source: source, Role: role, Text: text})
		}
	}
	return messages
}

func extractMessagesFromSSE(raw string, source string) []formattedMessage {
	messages := make([]formattedMessage, 0)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		value, ok := parseJSONValue(payload)
		if !ok {
			continue
		}
		messages = append(messages, extractMessagesFromJSON(value, source)...)
	}
	return messages
}

func extractMessagesFromWebsocketTimeline(raw string, source string) []formattedMessage {
	messages := make([]formattedMessage, 0)
	for _, candidate := range extractJSONCandidates(raw) {
		value, ok := parseJSONValue(candidate)
		if !ok {
			continue
		}
		messages = append(messages, extractMessagesFromJSON(value, source)...)
	}
	return messages
}

func extractJSONCandidates(raw string) []string {
	candidates := make([]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "data:") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		}
		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			candidates = append(candidates, trimmed)
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			candidates = append(candidates, trimmed)
		}
	}
	return candidates
}

func textFromValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		if strings.TrimSpace(typed) == "" {
			return ""
		}
		return truncateFormattedText(typed, formattedMessageMaxLength)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := textFromValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return truncateFormattedText(strings.Join(parts, "\n"), formattedMessageMaxLength)
	case map[string]any:
		for _, key := range []string{"text", "content", "message", "delta", "output_text", "input_text", "arguments", "name"} {
			if text := textFromValue(typed[key]); text != "" {
				return text
			}
		}
		return prettyJSON(typed, formattedMessageMaxLength)
	default:
		return truncateFormattedText(fmt.Sprint(typed), formattedMessageMaxLength)
	}
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return strings.TrimSpace(value)
}

func mergeAdjacentDeltas(messages []formattedMessage) []formattedMessage {
	merged := make([]formattedMessage, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(message.Text) == "" {
			continue
		}
		lastIndex := len(merged) - 1
		if lastIndex >= 0 && merged[lastIndex].Source == message.Source && merged[lastIndex].Role == message.Role {
			merged[lastIndex].Text = truncateFormattedText(merged[lastIndex].Text+message.Text, formattedMessageMaxLength)
			continue
		}
		message.Text = strings.TrimSpace(message.Text)
		merged = append(merged, message)
	}
	return merged
}

func appendRequestSummary(builder *strings.Builder, sections []requestLogSection) {
	info := firstSectionBody(sections, "REQUEST INFO")
	if strings.TrimSpace(info) == "" {
		return
	}
	builder.WriteString("=== Request ===\n")
	for _, line := range strings.Split(info, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, prefix := range []string{"Version:", "URL:", "Domain:", "Method:", "Downstream Transport:", "Upstream Transport:", "Timestamp:"} {
			if strings.HasPrefix(trimmed, prefix) {
				builder.WriteString(trimmed)
				builder.WriteByte('\n')
				break
			}
		}
	}
	builder.WriteByte('\n')
}

func appendConversation(builder *strings.Builder, messages []formattedMessage) {
	builder.WriteString("=== Conversation ===\n")
	if len(messages) == 0 {
		builder.WriteString("No structured conversation content detected.\n\n")
		return
	}
	for i, message := range messages {
		builder.WriteString(fmt.Sprintf("[%d] %s (%s)\n", i+1, message.Role, message.Source))
		builder.WriteString(message.Text)
		builder.WriteString("\n\n")
	}
}

func appendErrorSections(builder *strings.Builder, sections []requestLogSection) {
	wroteHeader := false
	for _, section := range sections {
		if section.Name != "API ERROR RESPONSE" {
			continue
		}
		if !wroteHeader {
			builder.WriteString("=== Errors ===\n")
			wroteHeader = true
		}
		builder.WriteString(truncateFormattedText(strings.TrimSpace(section.Body), formattedSectionMaxLength))
		builder.WriteString("\n\n")
	}
}

func appendFallbackSections(builder *strings.Builder, sections []requestLogSection, hasMessages bool) {
	if hasMessages {
		return
	}
	builder.WriteString("=== Unparsed Sections ===\n")
	for _, section := range sections {
		if strings.TrimSpace(section.Body) == "" || section.Name == "HEADERS" {
			continue
		}
		builder.WriteString("--- ")
		builder.WriteString(section.Name)
		builder.WriteString(" ---\n")
		builder.WriteString(truncateFormattedText(strings.TrimSpace(section.Body), formattedSectionMaxLength))
		builder.WriteString("\n\n")
	}
}

func firstSectionBody(sections []requestLogSection, name string) string {
	for _, section := range sections {
		if section.Name == name {
			return section.Body
		}
	}
	return ""
}

func prettyJSON(value any, maxLength int) string {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ""
	}
	return truncateFormattedText(string(payload), maxLength)
}

func truncateFormattedText(text string, maxLength int) string {
	if maxLength <= 0 || len(text) <= maxLength {
		return text
	}
	var buffer bytes.Buffer
	buffer.WriteString(text[:maxLength])
	buffer.WriteString("\n[truncated]")
	return buffer.String()
}
