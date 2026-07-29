package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestFormatRequestLog_ExtractsResponsesAPIConversation(t *testing.T) {
	raw := `=== REQUEST INFO ===
URL: http://localhost:8317/v1/responses
Domain: localhost
Method: POST
Timestamp: 2026-06-04T14:11:58Z

=== REQUEST BODY ===
{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"请总结这段日志"}]}]}

=== RESPONSE ===
{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"这是总结结果"}]}]}
`

	formatted := formatRequestLog(raw, "error-v1-responses.log")
	for _, expected := range []string{
		"Original file: error-v1-responses.log",
		"URL: http://localhost:8317/v1/responses",
		"[1] user (request body)",
		"请总结这段日志",
		"[2] assistant (response)",
		"这是总结结果",
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("expected formatted log to contain %q, got:\n%s", expected, formatted)
		}
	}
}

func TestFormatRequestLog_MergesSSEDeltas(t *testing.T) {
	raw := `=== RESPONSE ===
data: {"choices":[{"delta":{"role":"assistant","content":"hello"}}]}
data: {"choices":[{"delta":{"content":" world"}}]}
data: [DONE]
`

	formatted := formatRequestLog(raw, "success-stream.log")
	if !strings.Contains(formatted, "hello world") {
		t.Fatalf("expected merged SSE delta, got:\n%s", formatted)
	}
}

func TestExtractMessagesFromSSE_KeepsLongDataLine(t *testing.T) {
	longText := strings.Repeat("a", logScannerMaxBuffer+1024)
	raw := `data: {"choices":[{"delta":{"role":"assistant","content":"` + longText + `"}}]}`

	messages := extractMessagesFromSSE(raw, "response")
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if !strings.Contains(messages[0].Text, strings.Repeat("a", 1024)) {
		t.Fatal("expected long SSE data line to be parsed")
	}
}

func TestFormatRequestLog_ExtractsErrorSection(t *testing.T) {
	raw := `=== API ERROR RESPONSE ===
HTTP Status: 500
{"error":{"message":"upstream failed","type":"server_error"}}
`

	formatted := formatRequestLog(raw, "error-upstream.log")
	if !strings.Contains(formatted, "=== Errors ===") || !strings.Contains(formatted, "upstream failed") {
		t.Fatalf("expected error details, got:\n%s", formatted)
	}
}

func TestSplitRequestLogSections_KeepsLongSingleLine(t *testing.T) {
	longText := strings.Repeat("a", logScannerMaxBuffer+1024)
	raw := "=== REQUEST BODY ===\n" + longText + "\n"

	sections := splitRequestLogSections(raw)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if !strings.Contains(sections[0].Body, longText) {
		t.Fatal("expected long single-line content to be preserved")
	}
}

func TestDownloadFormattedRequestErrorLog_ReturnsFormattedFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	fileName := "error-v1-responses-test.log"
	logPath := filepath.Join(logDir, fileName)
	content := `=== REQUEST INFO ===
URL: http://localhost/v1/responses
Method: POST

=== REQUEST BODY ===
{"input":"hello"}
`
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetLogDirectory(logDir)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "name", Value: fileName}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-error-logs/"+fileName+"/formatted", nil)

	h.DownloadFormattedRequestErrorLog(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "error-v1-responses-test.formatted.txt") {
		t.Fatalf("unexpected content disposition: %s", rec.Header().Get("Content-Disposition"))
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("expected formatted body to contain request text, got:\n%s", rec.Body.String())
	}
}

func TestDownloadFormattedRequestLog_RejectsSymlink(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret.log")
	if err := os.WriteFile(target, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	linkName := "error-symlink.log"
	if err := os.Symlink(target, filepath.Join(logDir, linkName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetLogDirectory(logDir)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "name", Value: linkName}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-error-logs/"+linkName+"/formatted", nil)

	h.DownloadFormattedRequestErrorLog(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDownloadFormattedRequestLog_RejectsInvalidNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetLogDirectory(t.TempDir())

	cases := []string{"../secret.log", `nested\\secret.log`, "success-test.log", "error-test.txt"}
	for _, name := range cases {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Params = gin.Params{{Key: "name", Value: name}}
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-error-logs/invalid/formatted", nil)

		h.DownloadFormattedRequestErrorLog(ctx)

		if rec.Code < http.StatusBadRequest {
			t.Fatalf("expected error for %q, got %d", name, rec.Code)
		}
	}
}

func TestReadRequestLogForFormat_KeepsHeadAndTail(t *testing.T) {
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "success-large-format.log")

	var content strings.Builder
	content.WriteString("=== REQUEST INFO ===\nURL: http://localhost/v1/responses\nMethod: POST\n\n")
	content.WriteString("=== REQUEST BODY ===\n")
	content.WriteString(`{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hello-large"}]}]}`)
	content.WriteString("\n")
	content.WriteString(strings.Repeat("m", 64*1024))
	content.WriteString("\n=== RESPONSE ===\n")
	content.WriteString(`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"tail-response-end"}]}]}`)
	content.WriteString("\n")
	if err := os.WriteFile(logPath, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	info, errStat := os.Stat(logPath)
	if errStat != nil {
		t.Fatal(errStat)
	}

	// Cap far below the real file so head/tail sampling is exercised.
	data, truncated, kept, errRead := readRequestLogForFormat(logPath, info.Size(), 8*1024)
	if errRead != nil {
		t.Fatalf("readRequestLogForFormat: %v", errRead)
	}
	if !truncated {
		t.Fatal("expected truncated=true for oversized file")
	}
	if kept <= 0 || kept > 8*1024+64 {
		t.Fatalf("kept=%d, expected around the 8KiB sample budget", kept)
	}
	raw := string(data)
	if !strings.Contains(raw, "hello-large") {
		t.Fatalf("expected head content, got:\n%s", raw)
	}
	if !strings.Contains(raw, "tail-response-end") {
		t.Fatalf("expected tail content, got:\n%s", raw)
	}
	if !strings.Contains(raw, "TRUNCATED MIDDLE OMITTED") {
		t.Fatalf("expected middle marker, got:\n%s", raw)
	}

	formatted := formatRequestLog(raw, "success-large-format.log")
	formatted = appendFormattedLogTruncationNotice(formatted, info.Size(), kept)
	if !strings.Contains(formatted, "hello-large") {
		t.Fatalf("expected formatted head content, got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "tail-response-end") {
		t.Fatalf("expected formatted tail content, got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "=== Truncation Notice ===") {
		t.Fatalf("expected truncation notice, got:\n%s", formatted)
	}
}

func TestDownloadFormattedRequestSuccessLog_FullFileUnderCap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	fileName := "success-under-cap.log"
	logPath := filepath.Join(logDir, fileName)
	content := `=== REQUEST INFO ===
URL: http://localhost/v1/responses
Method: POST

=== REQUEST BODY ===
{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hello-full"}]}]}

=== RESPONSE ===
{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done-full"}]}]}
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetLogDirectory(logDir)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "name", Value: fileName}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-success-logs/"+fileName+"/formatted", nil)

	h.DownloadFormattedRequestSuccessLog(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hello-full") || !strings.Contains(body, "done-full") {
		t.Fatalf("expected full formatted content, got:\n%s", body)
	}
	if strings.Contains(body, "=== Truncation Notice ===") {
		t.Fatalf("did not expect truncation notice for small file, got:\n%s", body)
	}
}
