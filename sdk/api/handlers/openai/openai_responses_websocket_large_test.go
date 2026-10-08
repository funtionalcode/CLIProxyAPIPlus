package openai_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestCodexLargeWebsocketRequestTransportAndContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, duplex := range []bool{false, true} {
		for _, scenario := range []string{"small", "large", "large_incremental"} {
			t.Run(fmt.Sprintf("%s/duplex=%t", scenario, duplex), func(t *testing.T) {
				type upstreamRequest struct {
					websocket bool
					body      []byte
				}
				requests := make(chan upstreamRequest, 4)
				created := `{"type":"response.created","response":{"id":"large-test-response","output":[]}}`
				completed := `{"type":"response.completed","response":{"id":"large-test-response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if websocket.IsWebSocketUpgrade(r) {
						conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer func() { _ = conn.Close() }()
						for {
							_, body, err := conn.ReadMessage()
							if err != nil {
								return
							}
							requests <- upstreamRequest{true, body}
							if err = conn.WriteMessage(websocket.TextMessage, []byte(created)); err != nil {
								return
							}
							if err = conn.WriteMessage(websocket.TextMessage, []byte(completed)); err != nil {
								return
							}
						}
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					requests <- upstreamRequest{false, body}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", created, completed)
				}))
				defer upstream.Close()
				cfg := &config.Config{}
				cfg.DisableImageGeneration = config.DisableImageGenerationAll
				cfg.Codex.ResponseSteering = duplex
				cfg.CodexResponseSteering = duplex
				manager := auth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
				credential := &auth.Auth{ID: t.Name(), Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{
					"api_key": "test-key", "base_url": upstream.URL, "websockets": "true",
				}}
				if _, err := manager.Register(context.Background(), credential); err != nil {
					t.Fatal(err)
				}
				model := "gpt-5.4"
				registry.GetGlobalRegistry().RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: model}})
				defer registry.GetGlobalRegistry().UnregisterClient(credential.ID)
				handler := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
				router := gin.New()
				router.GET("/v1/responses", handler.ResponsesWebsocket)
				server := httptest.NewServer(router)
				defer server.Close()
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close() }()
				// Bound test failures without using server closure as the recovery trigger.
				_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
				initialText := "first"
				if scenario == "large" {
					initialText = strings.Repeat("x", executor.CodexWebsocketHTTPThreshold)
				}
				for turn := range 2 {
					text, parent := initialText, ""
					if turn == 1 {
						text, parent = "followup", `,"previous_response_id":"large-test-response"`
						if scenario == "large_incremental" {
							text = strings.Repeat("x", executor.CodexWebsocketHTTPThreshold)
						}
					}
					request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}]%s}`, model, text, parent)
					if err = conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
						t.Fatal(err)
					}
					for {
						_, payload, readErr := conn.ReadMessage()
						if turn == 1 && scenario == "large_incremental" {
							if !websocket.IsCloseError(readErr, websocket.CloseServiceRestart) {
								t.Fatalf("expected full replay close, got error=%v type=%s", readErr, gjson.GetBytes(payload, "type"))
							}
							select {
							case <-requests:
								t.Fatal("oversized incremental request reached upstream")
							default:
							}
							return
						}
						if readErr != nil {
							t.Fatal(readErr)
						}
						if gjson.GetBytes(payload, "type").String() == "response.completed" {
							break
						}
						if gjson.GetBytes(payload, "type").String() == "error" {
							t.Fatalf("unexpected response: %s", payload)
						}
					}
					captured := <-requests
					if captured.websocket != (scenario != "large") {
						t.Fatalf("wrong upstream transport: websocket=%t", captured.websocket)
					}
					if scenario == "large" {
						input := gjson.GetBytes(captured.body, "input").Array()
						if len(input) != 1+turn*2 || input[0].Get("content.0.text").String() != initialText {
							t.Fatal("HTTP continuation lost or duplicated the full input")
						}
						if turn == 1 && (input[1].Get("role").String() != "assistant" || input[2].Get("content.0.text").String() != "followup") {
							t.Fatal("HTTP continuation lost the completed output or followup")
						}
						if gjson.GetBytes(captured.body, "previous_response_id").Exists() {
							t.Fatal("HTTP request retained a websocket-only response reference")
						}
					}
				}
			})
		}
	}
}
