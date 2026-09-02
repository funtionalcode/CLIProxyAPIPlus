package helps

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRequestLoggingDoesNotMarkUpstreamAttempt(t *testing.T) {
	tests := []struct {
		name   string
		record func(context.Context)
	}{
		{
			name: "HTTP",
			record: func(ctx context.Context) {
				RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{URL: "https://api.example.com", Method: http.MethodPost})
			},
		},
		{
			name: "websocket",
			record: func(ctx context.Context) {
				RecordAPIWebsocketRequest(ctx, &config.Config{}, UpstreamRequestLog{URL: "wss://api.example.com", Method: "WEBSOCKET"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
			test.record(ctx)
			if cliproxyexecutor.UpstreamAttempted(ctx) {
				t.Fatal("request logging marked an upstream attempt before transport")
			}
		})
	}
}

func TestRecordAPIRequestClonesDeferredBodyWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	body := []byte(`{"model":"original"}`)

	RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/responses",
		Method: http.MethodPost,
		Body:   body,
	})
	body[10] = 'X'

	value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		t.Fatal("deferred API request was not captured")
	}
	requests, ok := value.([]logging.DeferredAPIRequest)
	if !ok || len(requests) != 1 {
		t.Fatalf("deferred API requests = %#v, want one request", value)
	}
	captured := string(requests[0]())
	if !strings.Contains(captured, `{"model":"original"}`) {
		t.Fatalf("captured API request = %q, want original body", captured)
	}
}

func TestRecordAPIResponseMetadataStoresHeadersWhenRequestLogDisabled(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Add("X-Upstream-Request-Id", "upstream-req-1")

	RecordAPIResponseMetadata(ctx, &config.Config{}, http.StatusOK, headers)
	headers.Set("X-Upstream-Request-Id", "mutated")

	got := logging.GetResponseHeaders(ctx)
	if got.Get("X-Upstream-Request-Id") != "upstream-req-1" {
		t.Fatalf("response header = %q, want %q", got.Get("X-Upstream-Request-Id"), "upstream-req-1")
	}
}

func TestRecordAPIHTTPRequestCapturesOutboundHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{RequestLog: true}}
	auth := &cliproxyauth.Auth{
		ID:       "codex.json",
		Provider: "codex",
		Label:    "codex account",
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-5"}`))
	if errReq != nil {
		t.Fatalf("failed to build request: %v", errReq)
	}
	req.Header.Set("User-Agent", "codex_cli_rs/0.136.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9")
	req.Header.Set("Originator", "codex-tui")
	req.Header.Set("Authorization", "Bearer sk-1234567890")
	req.Header.Set("X-Api-Key", "key-abcdef1234")

	RecordAPIHTTPRequest(ctx, cfg, auth, req)

	raw, ok := ginCtx.Get(apiRequestKey)
	if !ok {
		t.Fatalf("expected API request to be recorded")
	}
	got, ok := raw.([]byte)
	if !ok {
		t.Fatalf("API request type = %T, want []byte", raw)
	}
	text := string(got)
	for _, want := range []string{
		"=== API REQUEST 1 ===",
		"Upstream URL: https://chatgpt.com/backend-api/codex/responses",
		"HTTP Method: POST",
		"Auth: provider=codex, auth_id=codex.json, label=codex account",
		"User-Agent: codex_cli_rs/0.136.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9",
		"Originator: codex-tui",
		"Authorization: Bearer sk-1...7890",
		"X-Api-Key: key-...1234",
		`{"model":"gpt-5"}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("recorded request missing %q:\n%s", want, text)
		}
	}
}

func TestRecordAPIHTTPRequestSkipsAlreadyRecordedAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{RequestLog: true}}
	auth := &cliproxyauth.Auth{ID: "claude.json", Provider: "claude"}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:      "https://api.anthropic.com/v1/messages",
		Method:   http.MethodPost,
		Headers:  http.Header{"User-Agent": []string{"manual-ua"}},
		Body:     []byte(`{"model":"claude"}`),
		Provider: "claude",
		AuthID:   "claude.json",
	})
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"claude"}`))
	if errReq != nil {
		t.Fatalf("failed to build request: %v", errReq)
	}
	req.Header.Set("User-Agent", "transport-ua")

	RecordAPIHTTPRequest(ctx, cfg, auth, req)

	raw, ok := ginCtx.Get(apiRequestKey)
	if !ok {
		t.Fatalf("expected API request to be recorded")
	}
	text := string(raw.([]byte))
	if got := strings.Count(text, "=== API REQUEST"); got != 1 {
		t.Fatalf("API request count = %d, want 1:\n%s", got, text)
	}
	if strings.Contains(text, "transport-ua") {
		t.Fatalf("transport fallback duplicated manually recorded request:\n%s", text)
	}
}

func TestRecordAPIHTTPRequestRecordsRetryAfterResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{RequestLog: true}}
	auth := &cliproxyauth.Auth{ID: "codex.json", Provider: "codex"}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:      "https://chatgpt.com/backend-api/codex/responses",
		Method:   http.MethodPost,
		Headers:  http.Header{"User-Agent": []string{"first-ua"}},
		Body:     []byte(`{"attempt":1}`),
		Provider: "codex",
		AuthID:   "codex.json",
	})
	RecordAPIResponseMetadata(ctx, cfg, http.StatusForbidden, http.Header{"Cf-Ray": []string{"ray-1"}})

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"attempt":2}`))
	if errReq != nil {
		t.Fatalf("failed to build request: %v", errReq)
	}
	req.Header.Set("User-Agent", "retry-ua")
	req.Header.Set("Originator", "codex-tui")
	RecordAPIHTTPRequest(ctx, cfg, auth, req)

	raw, ok := ginCtx.Get(apiRequestKey)
	if !ok {
		t.Fatalf("expected API request to be recorded")
	}
	text := string(raw.([]byte))
	if got := strings.Count(text, "=== API REQUEST"); got != 2 {
		t.Fatalf("API request count = %d, want 2:\n%s", got, text)
	}
	for _, want := range []string{"=== API REQUEST 2 ===", "User-Agent: retry-ua", `{"attempt":2}`} {
		if !strings.Contains(text, want) {
			t.Fatalf("retry request missing %q:\n%s", want, text)
		}
	}
}

func TestAPIResponseAttemptsAreSeparated(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		fileBacked        bool
		firstResponseBody []byte
	}{
		{name: "memory backed error"},
		{name: "file backed error", fileBacked: true},
		{name: "memory backed partial body", firstResponseBody: []byte("partial")},
		{name: "file backed partial body", fileBacked: true, firstResponseBody: []byte("partial")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(recorder)
			var responseSource *logging.FileBodySource
			if tt.fileBacked {
				var errSource error
				responseSource, errSource = logging.NewFileBodySourceInDir(t.TempDir(), "api-response")
				if errSource != nil {
					t.Fatalf("NewFileBodySourceInDir: %v", errSource)
				}
				t.Cleanup(func() {
					if errCleanup := responseSource.Cleanup(); errCleanup != nil {
						t.Errorf("Cleanup: %v", errCleanup)
					}
				})
				ginCtx.Set(logging.APIResponseSourceContextKey, responseSource)
			}

			ctx := context.WithValue(context.Background(), "gin", ginCtx)
			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
			RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://api.example.com/first", Method: http.MethodPost})
			if len(tt.firstResponseBody) > 0 {
				AppendAPIResponseChunk(ctx, cfg, tt.firstResponseBody)
			} else {
				RecordAPIResponseError(ctx, cfg, errors.New("EOF"))
			}
			RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://api.example.com/second", Method: http.MethodPost})
			RecordAPIResponseError(ctx, cfg, errors.New("retry failed"))

			var response []byte
			if responseSource != nil {
				var errBytes error
				response, errBytes = responseSource.Bytes()
				if errBytes != nil {
					t.Fatalf("responseSource.Bytes: %v", errBytes)
				}
			} else {
				value, exists := ginCtx.Get(apiResponseKey)
				if !exists {
					t.Fatal("API_RESPONSE was not captured")
				}
				response, _ = value.([]byte)
			}

			previousEnd := "Error: EOF"
			if len(tt.firstResponseBody) > 0 {
				previousEnd = string(tt.firstResponseBody)
			}
			wantBoundary := previousEnd + "\n\n=== API RESPONSE 2 ==="
			if !strings.Contains(string(response), wantBoundary) {
				t.Fatalf("API response attempts are not separated by one blank line:\n%q\nwant boundary %q", response, wantBoundary)
			}
		})
	}
}
