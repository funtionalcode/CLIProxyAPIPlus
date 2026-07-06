package logging

import (
	"net/http"
	"os"
	"sort"
	"testing"
	"time"
)

func TestFileRequestLoggerUsesRequestModelInLogFilenames(t *testing.T) {
	t.Parallel()

	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0, 0)
	logger.SetSuccessEnabled(true)

	errLog := logger.LogRequest(
		"/v1/responses",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"model":"gpt-5.5","input":"hello"}`),
		http.StatusOK,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		"req:1",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("LogRequest() error = %v", errLog)
	}

	got := logFileNames(t, logsDir)
	want := []string{"gpt-5.5-req-1.log", "success-gpt-5.5-req-1.log"}
	if !equalStringSlices(got, want) {
		t.Fatalf("log filenames = %#v, want %#v", got, want)
	}
}

func TestFileRequestLoggerUsesRequestModelInForcedErrorFilename(t *testing.T) {
	t.Parallel()

	logsDir := t.TempDir()
	logger := NewFileRequestLogger(false, logsDir, "", 0, 0)

	errLog := logger.LogRequestWithOptions(
		"/v1/responses",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"model":"claude-sonnet-4.6","messages":[]}`),
		http.StatusTooManyRequests,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"error":"quota"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"quota-1",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("LogRequestWithOptions() error = %v", errLog)
	}

	got := logFileNames(t, logsDir)
	want := []string{"error-claude-sonnet-4.6-quota-1.log"}
	if !equalStringSlices(got, want) {
		t.Fatalf("log filenames = %#v, want %#v", got, want)
	}
}

func TestFileRequestLoggerUsesRequestModelInStreamingFilename(t *testing.T) {
	t.Parallel()

	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0, 0)

	writer, errLog := logger.LogStreamingRequest(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"model":"gpt-5.4-mini","stream":true}`),
		"stream-1",
	)
	if errLog != nil {
		t.Fatalf("LogStreamingRequest() error = %v", errLog)
	}
	if errStatus := writer.WriteStatus(http.StatusOK, map[string][]string{"Content-Type": {"text/event-stream"}}); errStatus != nil {
		t.Fatalf("WriteStatus() error = %v", errStatus)
	}
	writer.WriteChunkAsync([]byte("data: ok\n\n"))
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	got := logFileNames(t, logsDir)
	want := []string{"gpt-5.4-mini-stream-1.log"}
	if !equalStringSlices(got, want) {
		t.Fatalf("log filenames = %#v, want %#v", got, want)
	}
}

func TestFileRequestLoggerUsesURLModelFallback(t *testing.T) {
	t.Parallel()

	logger := NewFileRequestLogger(true, t.TempDir(), "", 0, 0)
	got := logger.generateFilename(
		"/v1beta/models/gemini-3-pro-preview:streamGenerateContent",
		[]byte(`{"contents":[]}`),
		"gemini-1",
	)
	want := "gemini-3-pro-preview-gemini-1.log"
	if got != want {
		t.Fatalf("generateFilename() = %q, want %q", got, want)
	}
}

func logFileNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		t.Fatalf("ReadDir(%s): %v", dir, errRead)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) >= 4 && name[len(name)-4:] == ".log" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
