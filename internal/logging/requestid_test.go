package logging

import (
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/google/uuid"
)

func TestGenerateRequestID_ValidUUIDv7(t *testing.T) {
	for i := 0; i < 100; i++ {
		id, errGenerate := GenerateRequestID()
		if errGenerate != nil {
			t.Fatalf("GenerateRequestID() failed: %v", errGenerate)
		}
		parsed, errParse := uuid.Parse(id)
		if errParse != nil {
			t.Fatalf("GenerateRequestID() = %q, invalid UUID: %v", id, errParse)
		}
		if parsed.Version() != 7 {
			t.Fatalf("GenerateRequestID() version = %d, want 7", parsed.Version())
		}
	}
}

func TestGenerateRequestID_Concurrency(t *testing.T) {
	const total = 1000
	var wg sync.WaitGroup
	ids := make(chan string, total)
	errChan := make(chan error, total)

	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, errGenerate := GenerateRequestID()
			if errGenerate != nil {
				errChan <- errGenerate
				return
			}
			ids <- id
		}()
	}

	wg.Wait()
	close(ids)
	close(errChan)

	for errWorker := range errChan {
		t.Fatalf("GenerateRequestID() concurrent worker failed: %v", errWorker)
	}

	seen := make(map[string]bool, total)
	for id := range ids {
		parsed, errParse := uuid.Parse(id)
		if errParse != nil {
			t.Fatalf("GenerateRequestID() produced invalid UUID %q: %v", id, errParse)
		}
		if parsed.Version() != 7 {
			t.Fatalf("expected version 7, got %d for %q", parsed.Version(), id)
		}
		if seen[id] {
			t.Fatalf("duplicate ID generated in concurrent run: %q", id)
		}
		seen[id] = true
	}

	if len(seen) != total {
		t.Fatalf("expected %d unique IDs, got %d", total, len(seen))
	}
}

func TestGenerateRequestIDFromReader_Error(t *testing.T) {
	expectedErr := errors.New("entropy source depleted")
	errReader := iotest.ErrReader(expectedErr)

	id, errGenerate := GenerateRequestIDFromReader(errReader)
	if errGenerate == nil {
		t.Fatalf("expected error from failing entropy reader, got id = %q", id)
	}
	if !errors.Is(errGenerate, expectedErr) && errGenerate.Error() != expectedErr.Error() {
		t.Fatalf("expected error %v, got %v", expectedErr, errGenerate)
	}
	if id != "" {
		t.Fatalf("expected empty id on failure, got %q", id)
	}
}

func TestShortRequestID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace only",
			input: "   ",
			want:  "",
		},
		{
			name:  "short string less than 8 chars",
			input: "req-1",
			want:  "req-1",
		},
		{
			name:  "exact 8 chars",
			input: "00000042",
			want:  "00000042",
		},
		{
			name:  "placeholder 8 dashes",
			input: "--------",
			want:  "--------",
		},
		{
			name:  "uuid v7 36 chars",
			input: "018f3a5b-1234-7abc-def0-12345678abcd",
			want:  "5678abcd",
		},
		{
			name:  "uuid v7 with trailing whitespace",
			input: " 018f3a5b-1234-7abc-def0-12345678abcd \n",
			want:  "5678abcd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShortRequestID(tt.input); got != tt.want {
				t.Fatalf("ShortRequestID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

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
	if id, err := uuid.Parse(requestIDFromContext); err != nil || id.Version() != 7 {
		t.Fatalf("expected UUIDv7 request ID, got %q", requestIDFromContext)
	}
}
