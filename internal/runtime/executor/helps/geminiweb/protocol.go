// Package geminiweb implements the text conversation protocol used by Gemini Web.
package geminiweb

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

// Model identifies a selectable web mode, independently of the public Gemini API.
type Model struct {
	ID, Name, UpstreamID string
	Mode                 int
}

// Models returns only modes observed on the web client. Availability depends on the account.
func Models() []Model {
	return []Model{
		{"gemini-web-3.5-flash-lite", "Gemini Web 3.5 Flash-Lite", "8c46e95b1a07cecc", 6},
		{"gemini-web-3.8-flash", "Gemini Web 3.8 Flash", "56fdd199312815e2", 1},
		{"gemini-web-3.1-pro", "Gemini Web 3.1 Pro", "e6fa609c3fa255c0", 3},
		{"gemini-web-4-argon", "Gemini Web 4 Argon", "95a4c0abcba24364", 8},
	}
}

// Error carries a safe message without upstream HTML, cookies or session tokens.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string         { return "gemini-web: " + e.Message }
func (e *Error) StatusCode() int       { return e.Code }
func (e *Error) IsRequestScoped() bool { return e.Code == 400 || e.Code == 501 || e.Code == 502 }

// ValidateInput rejects unsupported source features before translators can drop them.
func ValidateInput(body []byte) error {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return &Error{400, "invalid JSON request"}
	}
	for _, key := range []string{"previous_response_id", "conversation"} {
		v := gjson.GetBytes(body, key)
		if v.Exists() && v.Raw != "null" && v.String() != "" {
			return &Error{400, "stateful continuation is not supported; send the complete text history"}
		}
	}
	for _, key := range []string{"response_format", "text.format", "generationConfig.responseSchema", "generationConfig.responseJsonSchema"} {
		v := gjson.GetBytes(body, key)
		if v.Exists() && v.Raw != "null" && v.Get("type").String() != "text" {
			return &Error{400, "structured output is not supported by the web text channel"}
		}
	}
	for _, key := range []string{"tools", "functions"} {
		v := gjson.GetBytes(body, key)
		if v.Exists() && v.Raw != "null" && v.Raw != "[]" {
			return &Error{400, "native tool calls are not supported by the web text channel"}
		}
	}
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			kind, _ := x["type"].(string)
			switch kind {
			case "image", "image_url", "input_image", "image_generation", "file", "input_file", "document", "audio", "input_audio", "video", "video_url", "tool_use", "tool_result", "function_call", "function_call_output", "computer_call", "computer_call_output":
				return &Error{400, "only text conversation input is supported by the web channel"}
			}
			for _, key := range []string{"inlineData", "inline_data", "fileData", "file_data", "functionCall", "functionResponse"} {
				if _, ok := x[key]; ok {
					return &Error{400, "only text conversation input is supported by the web channel"}
				}
			}
			for _, child := range x {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range x {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

// Prompt translates text history into one independent web conversation. No browser
// conversation ID is reused, which prevents cross-client history contamination.
func Prompt(body []byte) (string, error) {
	if !json.Valid(body) {
		return "", &Error{400, "invalid JSON request"}
	}
	for _, key := range []string{"tools", "functions"} {
		if v := gjson.GetBytes(body, key); v.Exists() && v.Raw != "null" && v.Raw != "[]" {
			return "", &Error{400, "native tool calls are not supported by the web text channel"}
		}
	}
	for _, key := range []string{"response_format", "audio", "modalities"} {
		if v := gjson.GetBytes(body, key); v.Exists() && v.Raw != "null" {
			return "", &Error{400, "structured or multimodal output is not supported by the web text channel"}
		}
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return "", &Error{400, "messages must contain text"}
	}
	var parts []string
	var single string
	for _, message := range messages.Array() {
		role := message.Get("role").String()
		switch role {
		case "system", "developer", "user", "assistant":
		default:
			return "", &Error{400, "only system, developer, user and assistant text messages are supported"}
		}
		if len(message.Get("tool_calls").Array()) != 0 || message.Get("function_call").IsObject() {
			return "", &Error{400, "tool call history is not supported by the web text channel"}
		}
		content := message.Get("content")
		var text string
		switch {
		case content.Type == gjson.String:
			text = content.String()
		case content.IsArray():
			var chunks []string
			for _, part := range content.Array() {
				if part.Get("type").String() != "text" || part.Get("text").Type != gjson.String {
					return "", &Error{400, "images, files and other non-text input are not supported by the web text channel"}
				}
				chunks = append(chunks, part.Get("text").String())
			}
			text = strings.Join(chunks, "\n")
		default:
			return "", &Error{400, "message content must be text"}
		}
		single = text
		parts = append(parts, "["+role+"]\n"+text)
	}
	if len(parts) == 1 && messages.Array()[0].Get("role").String() == "user" {
		if strings.TrimSpace(single) == "" {
			return "", &Error{400, "prompt is empty"}
		}
		return single, nil
	}
	return "Continue the following conversation. Follow the system and developer instructions and reply to the last user message.\n\n" + strings.Join(parts, "\n\n") + "\n\n[assistant]\n", nil
}

// Payload completes all semantic conversion before user payload rules are applied.
// The result is the inner positional JSON array accepted by StreamGenerate.
func Payload(prompt string, mode int) []byte {
	a := make([]any, 99)
	a[0] = []any{prompt, 0, nil, nil, nil, nil, 0}
	a[1] = []string{"zh-CN"}
	a[6], a[7], a[10], a[11] = []int{0}, 1, 1, 0
	a[17], a[18], a[27] = [][]int{{0}}, 0, 1
	a[30], a[41], a[49], a[53] = []int{4, 16, 19}, []int{1}, 0, 0
	a[61], a[67], a[68], a[79], a[80] = []any{}, 0, 1, mode, 1
	a[91], a[96], a[98] = 1, 1, 1
	b, _ := json.Marshal(a)
	return b
}

// ReadStream parses line-framed JSON, emits cumulative answer text and requires an
// explicit completed candidate. An HTTP 200 with a truncated body is not success.
func ReadStream(reader io.Reader, emit func(string) error) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var answer, candidateID string
	complete := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == ")]}'" {
			continue
		}
		if line[0] >= '0' && line[0] <= '9' {
			if strings.Trim(line, "0123456789") != "" {
				return "", &Error{502, "invalid stream frame length"}
			}
			continue
		}
		if !json.Valid([]byte(line)) || !strings.HasPrefix(line, "[") {
			return "", &Error{502, "invalid stream frame"}
		}
		for _, record := range gjson.Parse(line).Array() {
			kind := record.Get("0").String()
			if kind != "wrb.fr" {
				continue
			}
			data := record.Get("2")
			if data.Type != gjson.String {
				return "", &Error{502, "upstream rejected the web request"}
			}
			if !json.Valid([]byte(data.String())) {
				return "", &Error{502, "invalid stream payload"}
			}
			inner := gjson.Parse(data.String())
			candidate := inner.Get("4.0")
			if !candidate.Exists() {
				continue
			}
			id := candidate.Get("0").String()
			if candidateID != "" && id != candidateID {
				return "", &Error{502, "upstream changed the answer candidate"}
			}
			candidateID = id
			text := candidate.Get("1.0")
			if text.Type != gjson.String {
				continue
			}
			if text.String() != answer {
				if !strings.HasPrefix(text.String(), answer) {
					return "", &Error{502, "upstream revised already emitted text"}
				}
				answer = text.String()
				if emit != nil {
					if err := emit(answer); err != nil {
						return "", err
					}
				}
			}
			if candidate.Get("8.0").Int() == 2 || inner.Get("14").Bool() {
				complete = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("gemini-web: read stream: %w", err)
	}
	if !complete || answer == "" {
		return "", &Error{502, "stream ended without a completed text answer"}
	}
	return answer, nil
}
