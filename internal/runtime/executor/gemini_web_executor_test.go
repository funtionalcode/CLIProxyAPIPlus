package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type geminiWebTransport func(*http.Request) (*http.Response, error)

func (f geminiWebTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func geminiWebTestFrame(text string, done bool) string {
	state := 1
	if done {
		state = 2
	}
	inner, _ := json.Marshal([]any{nil, nil, nil, nil, []any{[]any{"rc_test", []string{text}, nil, nil, nil, nil, nil, nil, []int{state}}}})
	frame, _ := json.Marshal([]any{[]any{"wrb.fr", nil, string(inner)}})
	return fmt.Sprintf("%d\n%s\n", len(frame), frame)
}

func TestGeminiWebExecutorPayloadBarrierAndProtocols(t *testing.T) {
	for _, format := range []translator.Format{translator.FormatOpenAI, translator.FormatOpenAIResponse, translator.FormatClaude, translator.FormatGemini} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", format, stream), func(t *testing.T) {
				cfg := &config.Config{Payload: config.PayloadConfig{
					Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-web-*", Protocol: "gemini-web"}}, Params: map[string]any{"0.0": "configured prompt"}}},
					Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gemini-web-*"}}, Params: []string{"30"}}},
				}}
				calls := 0
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(geminiWebTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					body := `{"SNlM0e":"token","cfb2h":"build","FdrFJe":"session"}`
					if r.Method == http.MethodPost {
						if err := r.ParseForm(); err != nil {
							t.Fatal(err)
						}
						payload := gjson.Get(r.Form.Get("f.req"), "1").String()
						if gjson.Get(payload, "0.0").String() != "configured prompt" || gjson.Get(payload, "30").IsArray() {
							t.Fatalf("rules lost after framing: %s", payload)
						}
						body = ")]}'\n\n" + geminiWebTestFrame("你", false) + geminiWebTestFrame("你好", true)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"secret=do-not-forward"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})))
				payload := `{"model":"gemini-web-3.5-flash-lite","messages":[{"role":"user","content":"original"}]}`
				if format == translator.FormatOpenAIResponse {
					payload = `{"model":"gemini-web-3.5-flash-lite","input":"original"}`
				}
				if format == translator.FormatClaude {
					payload = `{"model":"gemini-web-3.5-flash-lite","max_tokens":100,"messages":[{"role":"user","content":"original"}]}`
				}
				if format == translator.FormatGemini {
					payload = `{"contents":[{"role":"user","parts":[{"text":"original"}]}]}`
				}
				req := exec.Request{Model: "gemini-web-3.5-flash-lite", Payload: []byte(payload)}
				opts := exec.Options{SourceFormat: format, OriginalRequest: req.Payload}
				auth := &coreauth.Auth{ID: "web-test", Provider: "gemini-web", Metadata: map[string]any{"secure_1psid": "test-cookie"}}
				e := NewGeminiWebExecutor(cfg)
				var output string
				if stream {
					response, err := e.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					if response.Headers.Get("Set-Cookie") != "" {
						t.Fatal("leaked session cookie")
					}
					for chunk := range response.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output += string(chunk.Payload)
					}
					switch format {
					case translator.FormatOpenAI:
						// The API handler adds [DONE] after the executor channel closes.
						if !strings.Contains(output, `"finish_reason":"stop"`) {
							t.Fatal("missing terminal completion")
						}
					case translator.FormatOpenAIResponse:
						if !strings.Contains(output, "response.completed") {
							t.Fatal("missing terminal response")
						}
					case translator.FormatClaude:
						if !strings.Contains(output, "message_stop") {
							t.Fatal("missing terminal message")
						}
					case translator.FormatGemini:
						if !strings.Contains(output, "STOP") {
							t.Fatal("missing terminal candidate")
						}
					}
				} else {
					response, err := e.Execute(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					if response.Headers.Get("Set-Cookie") != "" {
						t.Fatal("leaked session cookie")
					}
					output = string(response.Payload)
				}
				if !strings.Contains(output, "你") || !strings.Contains(output, "好") || calls != 2 {
					t.Fatalf("calls=%d output=%s", calls, output)
				}
			})
		}
	}
}

func TestGeminiWebExecutorRejectsToolsAndReportsIncompleteStream(t *testing.T) {
	e := NewGeminiWebExecutor(&config.Config{})
	auth := &coreauth.Auth{Metadata: map[string]any{"secure_1psid": "test-cookie"}}
	req := exec.Request{Model: "gemini-web-3.5-flash-lite", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)}
	opts := exec.Options{SourceFormat: translator.FormatOpenAI}
	_, err := e.Execute(context.Background(), auth, req, opts)
	if err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("expected tool error, got %v", err)
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(geminiWebTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"SNlM0e":"token","cfb2h":"build","FdrFJe":"session"}`
		if r.Method == http.MethodPost {
			body = geminiWebTestFrame("partial", false)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})))
	req.Payload = []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	response, err := e.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	foundError := false
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			foundError = true
		}
		if strings.Contains(string(chunk.Payload), `"finish_reason":"stop"`) {
			t.Fatal("false successful completion")
		}
	}
	if !foundError {
		t.Fatal("truncated stream was accepted")
	}
}
