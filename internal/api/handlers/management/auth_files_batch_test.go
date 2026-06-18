package management

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestUploadAuthFile_BatchMultipart(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	files := []struct {
		name    string
		content string
	}{
		{name: "alpha.json", content: `{"type":"codex","email":"alpha@example.com"}`},
		{name: "beta.json", content: `{"type":"claude","email":"beta@example.com"}`},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatalf("failed to create multipart file: %v", err)
		}
		if _, err = part.Write([]byte(file.content)); err != nil {
			t.Fatalf("failed to write multipart content: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, ok := payload["uploaded"].(float64); !ok || int(got) != len(files) {
		t.Fatalf("expected uploaded=%d, got %#v", len(files), payload["uploaded"])
	}

	for _, file := range files {
		fullPath := filepath.Join(authDir, file.name)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("expected uploaded file %s to exist: %v", file.name, err)
		}
		if string(data) != file.content {
			t.Fatalf("expected file %s content %q, got %q", file.name, file.content, string(data))
		}
	}

	auths := manager.List()
	if len(auths) != len(files) {
		t.Fatalf("expected %d auth entries, got %d", len(files), len(auths))
	}
}

func TestUploadAuthFile_OverwritePreservesLocalFields(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	ctx := context.Background()

	const fileName = "codex.json"
	if err := h.writeAuthFile(ctx, fileName, []byte(`{"type":"codex","email":"user@example.com"}`)); err != nil {
		t.Fatalf("failed to seed auth file: %v", err)
	}

	existing, ok := manager.GetByID(fileName)
	if !ok || existing == nil {
		t.Fatalf("expected seeded auth record")
	}
	existing.Prefix = "team"
	existing.ProxyURL = "socks5://127.0.0.1:1080"
	existing.Disabled = true
	existing.Status = coreauth.StatusDisabled
	existing.StatusMessage = "disabled by operator"
	existing.Attributes["priority"] = "9"
	existing.Attributes["note"] = "keep this account"
	existing.Attributes["websockets"] = "false"
	existing.Attributes["header:Cache-Control"] = "no-cache"
	existing.Attributes["header:Accept-Encoding"] = "identity"
	existing.Metadata["priority"] = float64(9)
	existing.Metadata["note"] = "keep this account"
	existing.Metadata["prefix"] = "team"
	existing.Metadata["proxy_url"] = "socks5://127.0.0.1:1080"
	existing.Metadata["websockets"] = false
	existing.Metadata["disabled"] = true
	existing.Metadata["tool_prefix_disabled"] = true
	existing.Metadata["request_retry"] = float64(2)
	existing.Metadata["headers"] = map[string]any{
		"Cache-Control":   "no-cache",
		"Accept-Encoding": "identity",
	}
	if _, errUpdate := manager.Update(ctx, existing); errUpdate != nil {
		t.Fatalf("failed to update existing auth: %v", errUpdate)
	}

	if err := h.writeAuthFile(ctx, fileName, []byte(`{"type":"codex","email":"user@example.com","access_token":"new-token"}`)); err != nil {
		t.Fatalf("failed to overwrite auth file: %v", err)
	}

	auths := manager.List()
	if len(auths) != 1 {
		t.Fatalf("expected one auth entry after overwrite, got %d", len(auths))
	}
	updated, ok := manager.GetByID(fileName)
	if !ok || updated == nil {
		t.Fatalf("expected auth record to remain after overwrite")
	}
	if updated.Prefix != "team" {
		t.Fatalf("prefix = %q, want %q", updated.Prefix, "team")
	}
	if updated.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy_url = %q, want %q", updated.ProxyURL, "socks5://127.0.0.1:1080")
	}
	if !updated.Disabled || updated.Status != coreauth.StatusDisabled {
		t.Fatalf("disabled/status = %v/%s, want true/%s", updated.Disabled, updated.Status, coreauth.StatusDisabled)
	}
	if got := updated.Attributes["priority"]; got != "9" {
		t.Fatalf("priority attr = %q, want %q", got, "9")
	}
	if got := updated.Attributes["note"]; got != "keep this account" {
		t.Fatalf("note attr = %q, want %q", got, "keep this account")
	}
	if got := updated.Attributes["websockets"]; got != "false" {
		t.Fatalf("websockets attr = %q, want %q", got, "false")
	}
	if got := updated.Attributes["header:Cache-Control"]; got != "no-cache" {
		t.Fatalf("Cache-Control attr = %q, want %q", got, "no-cache")
	}
	if got := updated.Attributes["header:Accept-Encoding"]; got != "identity" {
		t.Fatalf("Accept-Encoding attr = %q, want %q", got, "identity")
	}

	raw, errRead := os.ReadFile(filepath.Join(authDir, fileName))
	if errRead != nil {
		t.Fatalf("failed to read overwritten auth file: %v", errRead)
	}
	var disk map[string]any
	if errUnmarshal := json.Unmarshal(raw, &disk); errUnmarshal != nil {
		t.Fatalf("failed to decode overwritten auth file: %v", errUnmarshal)
	}
	if got := disk["access_token"]; got != "new-token" {
		t.Fatalf("disk access_token = %#v, want %q", got, "new-token")
	}
	if got := disk["note"]; got != "keep this account" {
		t.Fatalf("disk note = %#v, want %q", got, "keep this account")
	}
	if got := disk["priority"]; got != float64(9) {
		t.Fatalf("disk priority = %#v, want 9", got)
	}
	if got := disk["tool_prefix_disabled"]; got != true {
		t.Fatalf("disk tool_prefix_disabled = %#v, want true", got)
	}
	if got := disk["request_retry"]; got != float64(2) {
		t.Fatalf("disk request_retry = %#v, want 2", got)
	}
	headers, ok := disk["headers"].(map[string]any)
	if !ok {
		t.Fatalf("disk headers = %#v, want object", disk["headers"])
	}
	if got := headers["Cache-Control"]; got != "no-cache" {
		t.Fatalf("disk headers.Cache-Control = %#v, want %q", got, "no-cache")
	}
	if got := headers["Accept-Encoding"]; got != "identity" {
		t.Fatalf("disk headers.Accept-Encoding = %#v, want %q", got, "identity")
	}
}

func TestUploadAuthFile_BatchMultipart_InvalidJSONDoesNotOverwriteExistingFile(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	existingName := "alpha.json"
	existingContent := `{"type":"codex","email":"alpha@example.com"}`
	if err := os.WriteFile(filepath.Join(authDir, existingName), []byte(existingContent), 0o600); err != nil {
		t.Fatalf("failed to seed existing auth file: %v", err)
	}

	files := []struct {
		name    string
		content string
	}{
		{name: existingName, content: `{"type":"codex"`},
		{name: "beta.json", content: `{"type":"claude","email":"beta@example.com"}`},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatalf("failed to create multipart file: %v", err)
		}
		if _, err = part.Write([]byte(file.content)); err != nil {
			t.Fatalf("failed to write multipart content: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusMultiStatus, rec.Code, rec.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(authDir, existingName))
	if err != nil {
		t.Fatalf("expected existing auth file to remain readable: %v", err)
	}
	if string(data) != existingContent {
		t.Fatalf("expected existing auth file to remain %q, got %q", existingContent, string(data))
	}

	betaData, err := os.ReadFile(filepath.Join(authDir, "beta.json"))
	if err != nil {
		t.Fatalf("expected valid auth file to be created: %v", err)
	}
	if string(betaData) != files[1].content {
		t.Fatalf("expected beta auth file content %q, got %q", files[1].content, string(betaData))
	}
}

func TestDeleteAuthFile_BatchQuery(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	files := []string{"alpha.json", "beta.json"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(authDir, name), []byte(`{"type":"codex"}`), 0o600); err != nil {
			t.Fatalf("failed to write auth file %s: %v", name, err)
		}
	}

	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/auth-files?name="+url.QueryEscape(files[0])+"&name="+url.QueryEscape(files[1]),
		nil,
	)
	ctx.Request = req

	h.DeleteAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, ok := payload["deleted"].(float64); !ok || int(got) != len(files) {
		t.Fatalf("expected deleted=%d, got %#v", len(files), payload["deleted"])
	}

	for _, name := range files {
		if _, err := os.Stat(filepath.Join(authDir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected auth file %s to be removed, stat err: %v", name, err)
		}
	}
}
