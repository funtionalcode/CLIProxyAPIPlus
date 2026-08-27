package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorUsesNativeResponsesProtocol(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"glm-5.3-flash","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := newNativeResponsesOpenAICompatExecutor(server.URL)
	response, errExecute := executor.Execute(
		context.Background(),
		newNativeResponsesOpenAICompatAuth(server.URL),
		cliproxyexecutor.Request{
			Model:   "glm-5.3-flash",
			Payload: []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hello"}],"max_tokens":65536,"stream":false}`),
		},
		cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatOpenAI,
			ResponseFormat: sdktranslator.FormatOpenAI,
		},
	)
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if gotPath != "/api/plan/v3/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/plan/v3/responses")
	}
	if got := gjson.GetBytes(gotBody, "max_output_tokens").Int(); got != 65536 {
		t.Fatalf("max_output_tokens = %d, want 65536; body=%s", got, gotBody)
	}
	if gjson.GetBytes(gotBody, "max_tokens").Exists() {
		t.Fatalf("max_tokens should not be sent to Responses API; body=%s", gotBody)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != "ok" {
		t.Fatalf("response content = %q, want ok; payload=%s", got, response.Payload)
	}
}

func TestOpenAICompatExecutorStreamsNativeResponsesProtocol(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1,\"model\":\"glm-5.3-flash\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\"ok\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1,\"status\":\"completed\",\"model\":\"glm-5.3-flash\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	executor := newNativeResponsesOpenAICompatExecutor(server.URL)
	result, errExecute := executor.ExecuteStream(
		context.Background(),
		newNativeResponsesOpenAICompatAuth(server.URL),
		cliproxyexecutor.Request{
			Model:   "glm-5.3-flash",
			Payload: []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hello"}],"max_tokens":65536,"stream":true,"stream_options":{"include_usage":true}}`),
		},
		cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatOpenAI,
			ResponseFormat: sdktranslator.FormatOpenAI,
			Stream:         true,
		},
	)
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}

	var responseBody bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		responseBody.Write(chunk.Payload)
	}

	if gotPath != "/api/plan/v3/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/plan/v3/responses")
	}
	if got := gjson.GetBytes(gotBody, "max_output_tokens").Int(); got != 65536 {
		t.Fatalf("max_output_tokens = %d, want 65536; body=%s", got, gotBody)
	}
	if gjson.GetBytes(gotBody, "stream_options").Exists() {
		t.Fatalf("stream_options should not be sent to Responses API; body=%s", gotBody)
	}
	if !bytes.Contains(responseBody.Bytes(), []byte(`"content":"ok"`)) {
		t.Fatalf("stream response missing content delta: %s", responseBody.Bytes())
	}
	if !bytes.Contains(responseBody.Bytes(), []byte(`"finish_reason":"stop"`)) {
		t.Fatalf("stream response missing stop finish reason: %s", responseBody.Bytes())
	}
}

func TestOpenAICompatExecutorAutoContinuesLengthLimitedResponsesStream(t *testing.T) {
	var requests atomic.Int32
	var initialStore atomic.Bool
	var continuationPreviousResponseID atomic.Value
	var continuationInput atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if requestNumber == 1 {
			initialStore.Store(gjson.GetBytes(body, "store").Bool())
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_1\",\"delta\":\"part1\"}\n\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"length\"},\"usage\":{\"input_tokens\":10,\"input_tokens_details\":{\"cached_tokens\":2},\"output_tokens\":100,\"output_tokens_details\":{\"reasoning_tokens\":50},\"total_tokens\":110}}}\n\n")
			return
		}
		if requestNumber != 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		continuationPreviousResponseID.Store(gjson.GetBytes(body, "previous_response_id").String())
		continuationInput.Store(gjson.GetBytes(body, "input").String())
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_2\",\"delta\":\"part2\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"status\":\"completed\",\"usage\":{\"input_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":3},\"output_tokens\":200,\"output_tokens_details\":{\"reasoning_tokens\":75},\"total_tokens\":220}}}\n\n")
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatible-volcengine", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "volcengine",
			BaseURL: server.URL + "/api/plan/v3",
			Models: []config.OpenAICompatibilityModel{{
				Name:                  "glm-5.3-flash",
				Protocol:              "responses",
				ResponsesAutoContinue: 1,
			}},
		}},
	})
	result, errExecute := executor.ExecuteStream(
		context.Background(),
		newNativeResponsesOpenAICompatAuth(server.URL),
		cliproxyexecutor.Request{
			Model:   "glm-5.3-flash",
			Payload: []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hello"}],"max_tokens":65536,"stream":true}`),
		},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI, Stream: true},
	)
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}

	var responseBody bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		responseBody.Write(chunk.Payload)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
	if !initialStore.Load() {
		t.Fatal("initial request must enable store for continuation")
	}
	if got, _ := continuationPreviousResponseID.Load().(string); got != "resp_1" {
		t.Fatalf("previous_response_id = %q, want resp_1", got)
	}
	if got, _ := continuationInput.Load().(string); got != openAICompatResponsesContinuePrompt {
		t.Fatalf("continuation input = %q, want configured continuation prompt", got)
	}
	if !bytes.Contains(responseBody.Bytes(), []byte(`"content":"part1"`)) || !bytes.Contains(responseBody.Bytes(), []byte(`"content":"part2"`)) {
		t.Fatalf("continued stream missing content: %s", responseBody.Bytes())
	}
	if bytes.Contains(responseBody.Bytes(), []byte(`"finish_reason":"length"`)) {
		t.Fatalf("intermediate length terminal leaked to client: %s", responseBody.Bytes())
	}
	if !bytes.Contains(responseBody.Bytes(), []byte(`"finish_reason":"stop"`)) {
		t.Fatalf("final stop terminal missing: %s", responseBody.Bytes())
	}
	if !bytes.Contains(responseBody.Bytes(), []byte(`"prompt_tokens":30`)) || !bytes.Contains(responseBody.Bytes(), []byte(`"completion_tokens":300`)) {
		t.Fatalf("continued usage was not accumulated: %s", responseBody.Bytes())
	}
}

func TestOpenAICompatExecutorKeepsChatCompletionsAsDefaultProtocol(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatible-default", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "default",
				BaseURL: server.URL + "/v1",
				Models:  []config.OpenAICompatibilityModel{{Name: "chat-model"}},
			},
		},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatible-default",
		Attributes: map[string]string{
			"base_url":    server.URL + "/v1",
			"api_key":     "test-key",
			"compat_name": "default",
		},
	}

	_, errExecute := executor.Execute(
		context.Background(),
		auth,
		cliproxyexecutor.Request{
			Model:   "chat-model",
			Payload: []byte(`{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"max_tokens":65536,"stream":false}`),
		},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI},
	)
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if got := gjson.GetBytes(gotBody, "max_tokens").Int(); got != 65536 {
		t.Fatalf("max_tokens = %d, want 65536; body=%s", got, gotBody)
	}
	if gjson.GetBytes(gotBody, "max_output_tokens").Exists() {
		t.Fatalf("max_output_tokens should not be sent to Chat Completions; body=%s", gotBody)
	}
}

func newNativeResponsesOpenAICompatExecutor(baseURL string) *OpenAICompatExecutor {
	return NewOpenAICompatExecutor("openai-compatible-volcengine", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "volcengine",
				BaseURL: baseURL + "/api/plan/v3",
				Models: []config.OpenAICompatibilityModel{
					{Name: "glm-5.3-flash", Protocol: "responses"},
				},
			},
		},
	})
}

func newNativeResponsesOpenAICompatAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "openai-compatible-volcengine",
		Attributes: map[string]string{
			"base_url":     baseURL + "/api/plan/v3",
			"api_key":      "test-key",
			"compat_name":  "volcengine",
			"provider_key": "openai-compatible-volcengine",
		},
	}
}
