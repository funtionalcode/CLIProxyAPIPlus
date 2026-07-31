package logging

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSanitizeRequestID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "spaces", in: "   ", want: ""},
		{name: "plain", in: "202607311200000000000abcdef12345678", want: "202607311200000000000abcdef12345678"},
		{name: "unsafe", in: "abc/def:ghi", want: "abc-def-ghi"},
		{name: "trim-separators", in: "--abc--", want: "abc"},
		{name: "only-unsafe", in: "////", want: ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SanitizeRequestID(tc.in); got != tc.want {
				t.Fatalf("SanitizeRequestID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	long := strings.Repeat("a", maxIncomingRequestIDLen+20)
	got := SanitizeRequestID(long)
	if len(got) > maxIncomingRequestIDLen {
		t.Fatalf("SanitizeRequestID(long) length = %d, want <= %d", len(got), maxIncomingRequestIDLen)
	}
}

func TestResolveIncomingRequestIDPrefersClientHeader(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Client-Request-Id", "client-id-1")
	req.Header.Set("X-Oneapi-Request-Id", "oneapi-id-1")
	c.Request = req

	if got := ResolveIncomingRequestID(c); got != "client-id-1" {
		t.Fatalf("ResolveIncomingRequestID() = %q, want client-id-1", got)
	}
}

func TestResolveIncomingRequestIDFallsBackToOneapiHeader(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Oneapi-Request-Id", "oneapi-id-2")
	c.Request = req

	if got := ResolveIncomingRequestID(c); got != "oneapi-id-2" {
		t.Fatalf("ResolveIncomingRequestID() = %q, want oneapi-id-2", got)
	}
}

func TestGinLogrusLoggerUsesIncomingRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusLogger())

	var requestIDFromContext string
	var requestIDFromGin string
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		requestIDFromContext = GetRequestID(c.Request.Context())
		requestIDFromGin = GetGinRequestID(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Oneapi-Request-Id", "20260731120000000000aabbccdd11223344")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	want := "20260731120000000000aabbccdd11223344"
	if requestIDFromContext != want {
		t.Fatalf("context request ID = %q, want %q", requestIDFromContext, want)
	}
	if requestIDFromGin != want {
		t.Fatalf("gin request ID = %q, want %q", requestIDFromGin, want)
	}
}

func TestGinLogrusLoggerGeneratesLocalIDWithoutIncomingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusLogger())

	var requestIDFromContext string
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		requestIDFromContext = GetRequestID(c.Request.Context())
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if requestIDFromContext == "" {
		t.Fatalf("expected generated request ID")
	}
	if len(requestIDFromContext) != 8 {
		t.Fatalf("expected 8-char local request ID, got %q", requestIDFromContext)
	}
}
