package openai

import (
	"bytes"
	"context"
	"fmt"
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
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

type responsesInterruptUsageSink struct {
	authID  string
	records chan coreusage.Record
}

func (s *responsesInterruptUsageSink) HandleUsage(_ context.Context, record coreusage.Record) {
	if record.AuthID == s.authID {
		select {
		case s.records <- record:
		default:
		}
	}
}

func TestResponsesInterruptInFlightAndReuseWebSocket(t *testing.T) {
	for _, tc := range []struct {
		name    string
		discard bool
		filter  bool
	}{
		{"preserve_false", false, false},
		{"preserve_true", true, false},
		{"payload_filter_is_final", true, true},
		{"control_code_only", false, false},
		{"control_target_only", false, false},
		{"cancel_control", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			interrupt := []byte(fmt.Sprintf(`{"type":"response.interrupt","event_id":"ctrl-ok","response_id":"r1","discard_partial_items":%t,"future_option":{"keep":true}}`, tc.discard))
			expected := interrupt
			if tc.filter {
				expected = []byte(`{"type":"response.interrupt","event_id":"ctrl-ok","response_id":"r1"}`)
			}
			wrong := []byte(`{"type":"response.interrupt","event_id":"ctrl-wrong","response_id":"missing"}`)
			rejection := []byte(`{"type":"error","event_id":"ctrl-wrong","response_id":"missing","status":400,"error":{"type":"invalid_request_error","code":"response_not_found","message":"unknown response"}}`)
			if tc.name == "control_code_only" {
				wrong = []byte(`{"type":"response.interrupt","response_id":"missing"}`)
				rejection = []byte(`{"type":"error","event_id":"server-error","status":400,"error":{"type":"invalid_request_error","code":"response_not_found","message":"unknown response"}}`)
			}
			if tc.name == "control_target_only" {
				wrong = []byte(`{"type":"response.interrupt","response_id":"missing"}`)
				rejection = []byte(`{"type":"error","response_id":"missing","status":400,"error":{"type":"invalid_request_error","message":"unknown response"}}`)
			}
			if tc.name == "cancel_control" {
				wrong = []byte(`{"type":"response.cancel","event_id":"ctrl-wrong","response_id":"missing"}`)
				interrupt = []byte(`{"type":"response.cancel","event_id":"ctrl-ok","response_id":"r1"}`)
				expected = interrupt
			}
			terminalType := "response.completed"
			if tc.name == "cancel_control" {
				terminalType = "response.cancelled"
			}
			frames := make(chan []byte, 3)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if _, _, err = conn.ReadMessage(); err != nil {
					return
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`))
				_, frame, err := conn.ReadMessage()
				if err != nil {
					return
				}
				frames <- frame
				_ = conn.WriteMessage(websocket.TextMessage, rejection)
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_text.delta","response_id":"r1","delta":"still running"}`))
				_, frame, err = conn.ReadMessage()
				if err != nil {
					return
				}
				frames <- frame
				_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":%q,"response":{"id":"r1","output":[],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`, terminalType)))
				_, frame, err = conn.ReadMessage()
				if err != nil {
					return
				}
				frames <- frame
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r2"}}`))
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r2","output":[],"usage":{"input_tokens":20,"output_tokens":2,"total_tokens":22}}}`))
				_, _, _ = conn.ReadMessage()
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering, cfg.CodexResponseSteering = true, true
			model := "interrupt-" + tc.name
			if tc.filter {
				cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: model, Protocol: "codex", Match: []map[string]any{{"type": "response.interrupt"}}}}, Params: []string{"discard_partial_items", "future_option"}}}
			}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			authID := "auth-" + model
			records := make(chan coreusage.Record, 4)
			coreusage.RegisterNamedPlugin(t.Name(), &responsesInterruptUsageSink{authID: authID, records: records})
			if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}}); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(authID)
			handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router := gin.New()
			router.GET("/v1/responses", handler.ResponsesWebsocket)
			downstream := httptest.NewServer(router)
			defer downstream.Close()
			client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
			create := []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model))
			if err = client.WriteMessage(websocket.TextMessage, create); err != nil {
				t.Fatal(err)
			}
			readEvent := func(want string) []byte {
				t.Helper()
				_, frame, errRead := client.ReadMessage()
				if errRead != nil {
					t.Fatal(errRead)
				}
				if got := gjson.GetBytes(frame, "type").String(); got != want {
					t.Fatalf("expected %s, got %s", want, frame)
				}
				return frame
			}
			readEvent("response.created")
			if err = client.WriteMessage(websocket.TextMessage, wrong); err != nil {
				t.Fatal(err)
			}
			if frame := readEvent("error"); !bytes.Equal(frame, rejection) {
				t.Fatalf("control rejection changed: %s", frame)
			}
			if frame := <-frames; !bytes.Equal(frame, wrong) {
				t.Fatalf("wrong-target control changed: %s", frame)
			}
			readEvent("response.output_text.delta")
			if err = client.WriteMessage(websocket.TextMessage, interrupt); err != nil {
				t.Fatal(err)
			}
			readEvent(terminalType)
			if frame := <-frames; !bytes.Equal(frame, expected) {
				t.Fatalf("interrupt changed: got %s, want %s", frame, expected)
			}
			if err = client.WriteMessage(websocket.TextMessage, create); err != nil {
				t.Fatal(err)
			}
			frame := readEvent("response.created")
			if gjson.GetBytes(frame, "response.id").String() != "r2" {
				t.Fatalf("next response lost: %s", frame)
			}
			readEvent("response.completed")
			if frame = <-frames; gjson.GetBytes(frame, "type").String() != "response.create" {
				t.Fatalf("next create changed: %s", frame)
			}
			for _, total := range []int64{11, 22} {
				select {
				case record := <-records:
					if record.Failed || record.Detail.TotalTokens != total {
						t.Fatalf("control error corrupted generation usage: failed=%t tokens=%d want=%d", record.Failed, record.Detail.TotalTokens, total)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("missing generation usage")
				}
			}
		})
	}
}

// TestResponsesSteerRejectedWhenDisabled verifies that when codex.response-steering is
// false (default), response.steer is rejected as an unsupported request type and is
// never rewritten or converted into response.create.
func TestResponsesSteerRejectedWhenDisabled(t *testing.T) {
	raw := []byte(`{"type":"response.steer","previous_response_id":"resp-1","input":"Keep the scope small."}`)
	lastRequest := []byte(`{"model":"gpt-6-astra","stream":true,"input":[]}`)

	_, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, []byte("[]"))
	if errMsg == nil {
		t.Fatal("expected response.steer to be rejected when steering is disabled")
	}
	if !strings.Contains(errMsg.Error.Error(), "unsupported websocket request type: response.steer") {
		t.Fatalf("unexpected error message: %v", errMsg.Error)
	}
}

// TestResponsesSteerInFlightWebSocket verifies that when codex.response-steering is true,
// an in-flight response.steer frame sent while generation is running is forwarded directly
// to the same upstream connection without waiting or converting to response.create.
func TestResponsesSteerInFlightWebSocket(t *testing.T) {
	upstreamReceivedSteer := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))

		// Read create
		_, p, err := c.ReadMessage()
		if err != nil {
			t.Errorf("read create: %v", err)
			return
		}
		if gjson.GetBytes(p, "type").String() != "response.create" {
			t.Errorf("expected response.create, got %s", p)
			return
		}

		// Emit response.created
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`))

		// Read next frame: in-flight response.steer
		_, steerPayload, err := c.ReadMessage()
		if err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		if gjson.GetBytes(steerPayload, "type").String() == "response.steer" {
			close(upstreamReceivedSteer)
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r1"}}`))
		} else {
			t.Errorf("expected response.steer, got %s", steerPayload)
		}

		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	authID := "steering-red-test"
	model := "steering-red-model"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         authID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"},
	}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID)

	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))

	create := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model)
	if err := c.WriteMessage(websocket.TextMessage, []byte(create)); err != nil {
		t.Fatal(err)
	}

	// Read response.created
	_, p, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read response.created: %v", err)
	}
	if gjson.GetBytes(p, "type").String() != "response.created" {
		t.Fatalf("expected response.created, got %s", p)
	}

	// Send in-flight response.steer while response is running
	steer := `{"type":"response.steer","previous_response_id":"r1","input":"Focus on networking"}`
	if err := c.WriteMessage(websocket.TextMessage, []byte(steer)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-upstreamReceivedSteer:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never received response.steer frame during in-flight generation")
	}
}
