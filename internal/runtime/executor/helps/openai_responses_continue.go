package helps

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIResponsesContinuation opens the next stored Responses stream after a
// length-limited terminal event.
type OpenAIResponsesContinuation func(previousResponseID string) (io.ReadCloser, error)

// NewOpenAIResponsesAutoContinueBody joins length-limited stored Responses
// streams while exposing one continuous SSE body to the protocol translator.
func NewOpenAIResponsesAutoContinueBody(body io.ReadCloser, maxContinuations int, openNext OpenAIResponsesContinuation) io.ReadCloser {
	return &openAIResponsesAutoContinueBody{
		current:   body,
		reader:    bufio.NewReader(body),
		remaining: maxContinuations,
		openNext:  openNext,
	}
}

type openAIResponsesAutoContinueBody struct {
	current   io.ReadCloser
	reader    *bufio.Reader
	remaining int
	openNext  OpenAIResponsesContinuation
	pending   bytes.Buffer
	usage     openAIResponsesContinuationUsage
	closed    bool
}

func (b *openAIResponsesAutoContinueBody) Read(p []byte) (int, error) {
	for b.pending.Len() == 0 {
		if errFill := b.fill(); errFill != nil {
			return 0, errFill
		}
	}
	return b.pending.Read(p)
}

func (b *openAIResponsesAutoContinueBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	if b.current == nil {
		return nil
	}
	return b.current.Close()
}

func (b *openAIResponsesAutoContinueBody) fill() error {
	for {
		frame, errRead := readOpenAIResponsesSSEFrame(b.reader)
		if len(frame) == 0 {
			if errRead != nil {
				return errRead
			}
			continue
		}

		payload := openAIResponsesSSEPayload(frame)
		if len(payload) == 0 || !gjson.ValidBytes(payload) {
			b.pending.Write(frame)
			return nil
		}

		terminalType := gjson.GetBytes(payload, "type").String()
		terminal := terminalType == "response.completed" || terminalType == "response.incomplete" || terminalType == "response.failed"
		if !terminal {
			b.pending.Write(frame)
			return nil
		}

		b.usage.add(payload)
		if previousResponseID, lengthLimited := openAIResponsesLengthLimitedID(payload); lengthLimited && b.remaining > 0 && b.openNext != nil {
			if errClose := b.current.Close(); errClose != nil {
				return fmt.Errorf("close length-limited Responses stream: %w", errClose)
			}
			next, errNext := b.openNext(previousResponseID)
			if errNext != nil {
				return errNext
			}
			b.current = next
			b.reader = bufio.NewReader(next)
			b.remaining--
			continue
		}

		payload = b.usage.apply(payload)
		b.pending.WriteString("data: ")
		b.pending.Write(payload)
		b.pending.WriteString("\n\n")
		return nil
	}
}

func readOpenAIResponsesSSEFrame(reader *bufio.Reader) ([]byte, error) {
	var frame bytes.Buffer
	for {
		line, errRead := reader.ReadBytes('\n')
		if len(line) > 0 {
			frame.Write(line)
			if len(bytes.TrimSpace(line)) == 0 {
				return frame.Bytes(), nil
			}
		}
		if errRead != nil {
			return frame.Bytes(), errRead
		}
	}
}

func openAIResponsesSSEPayload(frame []byte) []byte {
	var dataLines [][]byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		dataLines = append(dataLines, bytes.TrimSpace(trimmed[len("data:"):]))
	}
	return bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
}

func openAIResponsesLengthLimitedID(payload []byte) (string, bool) {
	if gjson.GetBytes(payload, "type").String() != "response.incomplete" {
		return "", false
	}
	reason := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.incomplete_details.reason").String()))
	if reason != "length" && reason != "max_tokens" && reason != "max_output_tokens" {
		return "", false
	}
	responseID := strings.TrimSpace(gjson.GetBytes(payload, "response.id").String())
	return responseID, responseID != ""
}

type openAIResponsesContinuationUsage struct {
	inputTokens     int64
	outputTokens    int64
	totalTokens     int64
	cachedTokens    int64
	reasoningTokens int64
	seen            bool
}

func (u *openAIResponsesContinuationUsage) add(payload []byte) {
	usageNode := gjson.GetBytes(payload, "response.usage")
	if !usageNode.Exists() || !usageNode.IsObject() {
		return
	}
	u.inputTokens += usageNode.Get("input_tokens").Int()
	u.outputTokens += usageNode.Get("output_tokens").Int()
	u.totalTokens += usageNode.Get("total_tokens").Int()
	u.cachedTokens += usageNode.Get("input_tokens_details.cached_tokens").Int()
	u.reasoningTokens += usageNode.Get("output_tokens_details.reasoning_tokens").Int()
	u.seen = true
}

func (u *openAIResponsesContinuationUsage) apply(payload []byte) []byte {
	if !u.seen {
		return payload
	}
	updated, _ := sjson.SetBytes(payload, "response.usage.input_tokens", u.inputTokens)
	updated, _ = sjson.SetBytes(updated, "response.usage.output_tokens", u.outputTokens)
	updated, _ = sjson.SetBytes(updated, "response.usage.total_tokens", u.totalTokens)
	updated, _ = sjson.SetBytes(updated, "response.usage.input_tokens_details.cached_tokens", u.cachedTokens)
	updated, _ = sjson.SetBytes(updated, "response.usage.output_tokens_details.reasoning_tokens", u.reasoningTokens)
	return updated
}
